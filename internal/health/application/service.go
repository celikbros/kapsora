package application

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/health/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Service implements the health case use cases.
type Service struct {
	pool      *pgxpool.Pool
	repo      Repository
	reports   ReportRepository
	workItems WorkItemPort
	stays     StayPort
	// stayRepo, requests and authorizations are WP-I5-03's three collaborators: the stay's
	// own tables, the request its preauthorization is, and the hold that request's approval
	// produces. A process wired with none of them serves the case and the report and answers
	// every stay endpoint with a refusal, which is the honest behaviour of a deployment that
	// has not been given them.
	stayRepo       StayRepository
	requests       RequestPort
	authorizations AuthorizationPort
	audit          audit.Recorder
	cursors        *httpx.CursorCodec
	logger         *slog.Logger
	now            func() time.Time
}

// Deps are the collaborators of the service.
type Deps struct {
	Pool *pgxpool.Pool
	Repo Repository
	// Reports is the treatment report's own repository (WP-I5-02). It is a second port
	// rather than more methods on the first because a report is a different aggregate with
	// a different lifecycle, and one interface holding both would be an interface no test
	// double could implement half of.
	Reports ReportRepository
	// WorkItems raises the work a submitted report is, inside the command's transaction.
	// nil means "raise nothing", which is the honest behaviour of a deployment with no
	// medical review queue configured.
	WorkItems WorkItemPort
	Audit     audit.Recorder
	// Cursors may be nil in a process that only runs the expiry job: it never pages, and
	// asking the scheduler for a cursor key would only add a secret it has no reason to
	// hold.
	Cursors *httpx.CursorCodec
	// Stays answers whether a case still has an inpatient stay running. It defaults to
	// StayRepo when one is given and to "none open" when neither is: a schema with no stays
	// in it could honestly give no other answer.
	Stays StayPort
	// StayRepo is the inpatient stay's own persistence port (WP-I5-03). nil leaves the stay
	// endpoints unavailable rather than half-working.
	StayRepo StayRepository
	// Requests raises the PREAUTHORIZATION a stay is, inside the create command's own
	// transaction; Authorizations takes, extends and gives back the hold its approval
	// produces. Both default to a port that refuses, so a process that was never given them
	// says so rather than writing half an admission.
	Requests       RequestPort
	Authorizations AuthorizationPort
	Logger         *slog.Logger
	// Now defaults to time.Now; tests pin it so a close is deterministic.
	Now func() time.Time
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil || d.Repo == nil || d.Reports == nil {
		return nil, errors.New("health: pool and both repositories are required")
	}
	if d.Audit == nil {
		d.Audit = audit.NopRecorder{}
	}
	if d.Stays == nil {
		if d.StayRepo != nil {
			d.Stays = repoStays{d.StayRepo}
		} else {
			d.Stays = NoOpenStays{}
		}
	}
	if d.Requests == nil {
		d.Requests = NoRequests{}
	}
	if d.Authorizations == nil {
		d.Authorizations = NoAuthorizations{}
	}
	if d.WorkItems == nil {
		d.WorkItems = NoWorkItems{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{
		pool: d.Pool, repo: d.Repo, reports: d.Reports, workItems: d.WorkItems,
		stays: d.Stays, stayRepo: d.StayRepo, requests: d.Requests,
		authorizations: d.Authorizations, audit: d.Audit, cursors: d.Cursors,
		logger: d.Logger, now: d.Now,
	}, nil
}

// withTx runs fn inside a tenant-bound transaction.
func (s *Service) withTx(ctx context.Context, rc identity.RequestContext,
	fn func(ctx context.Context, tx pgx.Tx) error,
) error {
	return db.WithTenantTx(ctx, s.pool,
		db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, fn)
}

// paging decodes the cursor and clamps the limit; the repository is asked for one row more
// than the page size so the caller learns whether a next page exists.
func (s *Service) paging(cursor string, limit int) (after *httpx.Cursor, pageSize int, err error) {
	decoded, hasCursor, err := s.cursors.Decode(cursor)
	if err != nil {
		return nil, 0, err
	}
	pageSize = httpx.ClampLimit(limit)
	if hasCursor {
		after = &decoded
	}
	return after, pageSize, nil
}

// validateAccess checks the purpose and reason a read stated, against the domain's list and
// then against the reference table. The second check is the authority: a purpose seeded
// later has to work without a Go release, and one removed has to stop working with it.
func (s *Service) validateAccess(ctx context.Context, tx pgx.Tx, req AccessRequest) error {
	if err := domain.ValidateAccessHeaders(req.PurposeCode, req.ReasonText); err != nil {
		return err
	}
	if req.PurposeCode == "" {
		return nil
	}
	ok, err := s.repo.AccessPurposeExists(ctx, tx, req.PurposeCode)
	if err != nil {
		return err
	}
	if !ok {
		return fieldError("X-Access-Purpose", "ENUM", "tanımlı bir erişim amacı olmalı")
	}
	return nil
}

// recordAccess writes the audit.access_event a clinical read owes. It is called for the
// clinical projection and for a refused sensitive read, and never for the financial
// projection: that projection carries nothing clinical, so there is no clinical access to
// record and a log full of them would bury the reads that matter.
func (s *Service) recordAccess(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	personID uuid.UUID, resourceType string, resourceID uuid.UUID,
	accessType audit.AccessType, req AccessRequest, outcome audit.Outcome,
) error {
	return s.audit.RecordAccess(ctx, tx, audit.AccessEvent{
		TenantID: rc.TenantID, ActorID: rc.Principal.ActorID,
		MembershipID: nullUUID(rc.MembershipID), PersonID: nullUUID(personID),
		ResourceType: resourceType, ResourceID: nullUUID(resourceID),
		AccessType: accessType, Classification: audit.ClassHealth,
		PurposeCode: req.PurposeCode, ReasonText: req.ReasonText, Outcome: outcome,
	})
}

// recordDenial writes the DENIED access event of a refused sensitive read in a transaction
// of its own. The refusal rolls the read's transaction back, and an audit row that rolls
// back with the thing it was auditing is an audit row nobody ever sees — the lesson
// WP-I4-04 wrote down and this package inherits.
// The access type is not a parameter: every caller is a single read, because a list narrows
// rather than refusing and a command records nothing. A SEARCH that was denied would be a
// page that refused itself, which no endpoint here does.
func (s *Service) recordDenial(ctx context.Context, rc identity.RequestContext,
	personID uuid.UUID, resourceType string, resourceID uuid.UUID, req AccessRequest,
) {
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		return s.recordAccess(ctx, tx, rc, personID, resourceType, resourceID,
			audit.AccessView, req, audit.OutcomeDenied)
	})
	if err != nil {
		// The caller is being refused either way; losing the record of it is worth a log
		// line rather than a different answer, which would tell the caller the write
		// happened at all.
		s.logger.Error("health: could not record a denied clinical access", "error", err)
	}
}

// record writes one business audit row. A case carries no personal data beyond the ids it
// names, so the detail is ids, codes, counts and statuses only — and never a diagnosis
// code, which is why nothing below ever puts one in a detail map.
func (s *Service) record(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	action, resourceType string, resourceID uuid.UUID, detail map[string]any,
) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID),
		MembershipID: nullUUID(rc.MembershipID), Category: audit.CategoryBusiness,
		ActionCode: action, ResourceType: resourceType, ResourceID: nullUUID(resourceID),
		Outcome: audit.OutcomeSuccess, Detail: detail,
	})
}

// caseCursor is the keyset position of a row on the (opened_at DESC, id DESC) order.
func caseCursor(r CaseRecord) httpx.Cursor {
	return httpx.Cursor{CreatedAt: r.OpenedAt, ID: r.ID}
}

// accessEventCursor is the keyset position of an access event.
func accessEventCursor(r AccessEventRecord) httpx.Cursor {
	return httpx.Cursor{CreatedAt: r.OccurredAt, ID: r.ID}
}

func actorPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

func trimmedPtr(s *string) *string {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*s)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// fieldError builds a one-field validation error.
func fieldError(field, code, message string) error {
	ve := &domain.ValidationError{}
	ve.Add(field, code, message)
	return ve
}
