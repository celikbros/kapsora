package application

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/claim/domain"
	healthapp "github.com/celikbros/kapsora/internal/health/application"
	healthdomain "github.com/celikbros/kapsora/internal/health/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Service implements the claim use cases.
type Service struct {
	pool           *pgxpool.Pool
	repo           Repository
	pricing        PricingPort
	rules          RulesPort
	authorizations AuthorizationPort
	reports        ReportCoveragePort
	policies       PolicyPort
	workItems      WorkItemPort
	audit          audit.Recorder
	cursors        *httpx.CursorCodec
	logger         *slog.Logger
	now            func() time.Time
}

// Deps are the collaborators of the service. Every port has a default that refuses or does
// nothing rather than pretending: a process wired with no pricing service prices nothing and
// sends every line to a person, which is the honest behaviour of a deployment that was never
// given one.
type Deps struct {
	Pool *pgxpool.Pool
	Repo Repository
	// Pricing is WP-I3-05's ladder. It runs inside the submit's own transaction, so a claim
	// priced against balances that then moved is a state no reader ever observes.
	Pricing PricingPort
	// Rules is WP-I3-04's engine over the published ADJUDICATION versions.
	Rules RulesPort
	// Authorizations is WP-I4-02's hold: consume a line, give back a refused claim's.
	Authorizations AuthorizationPort
	// Reports is WP-I5-02's coverage port.
	Reports ReportCoveragePort
	// Policies is WP-I4-03 section 2.4's approval policy lookup.
	Policies PolicyPort
	// WorkItems raises the work a routed claim is, inside the submit's transaction.
	WorkItems WorkItemPort
	Audit     audit.Recorder
	// Cursors may be nil in a process that never pages.
	Cursors *httpx.CursorCodec
	Logger  *slog.Logger
	// Now defaults to time.Now; tests pin it so a decision is deterministic.
	Now func() time.Time
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil || d.Repo == nil {
		return nil, errors.New("claim: pool and repository are required")
	}
	if d.Audit == nil {
		d.Audit = audit.NopRecorder{}
	}
	if d.Pricing == nil {
		d.Pricing = NoPricing{}
	}
	if d.Rules == nil {
		d.Rules = NoRules{}
	}
	if d.Authorizations == nil {
		d.Authorizations = NoAuthorizations{}
	}
	if d.Reports == nil {
		d.Reports = NoReportCoverage{}
	}
	if d.Policies == nil {
		d.Policies = NoPolicy{}
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
		pool: d.Pool, repo: d.Repo, pricing: d.Pricing, rules: d.Rules,
		authorizations: d.Authorizations, reports: d.Reports, policies: d.Policies,
		workItems: d.WorkItems, audit: d.Audit, cursors: d.Cursors,
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

// validateAccess checks the purpose and reason a read stated, against WP-I5-01's own domain
// list and then against the reference table. There is one authority for what a purpose may
// be, and it is the table the health package seeded.
func (s *Service) validateAccess(ctx context.Context, tx pgx.Tx, req AccessRequest) error {
	if err := healthdomain.ValidateAccessHeaders(req.PurposeCode, req.ReasonText); err != nil {
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

// recordAccess writes the audit.access_event a clinical read of a claim owes, with resource
// type CLAIM. It is called for the clinical projection and for a refused sensitive read, and
// never for the financial projection: that projection carries nothing clinical, so there is
// no clinical access to record and a log full of them would bury the reads that matter.
func (s *Service) recordAccess(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	personID, claimID uuid.UUID, accessType audit.AccessType, req AccessRequest,
	outcome audit.Outcome,
) error {
	return s.audit.RecordAccess(ctx, tx, audit.AccessEvent{
		TenantID: rc.TenantID, ActorID: rc.Principal.ActorID,
		MembershipID: nullUUID(rc.MembershipID), PersonID: nullUUID(personID),
		ResourceType: domain.AggregateClaim, ResourceID: nullUUID(claimID),
		AccessType: accessType, Classification: audit.ClassHealth,
		PurposeCode: req.PurposeCode, ReasonText: req.ReasonText, Outcome: outcome,
	})
}

// recordDenial writes the DENIED access event of a refused sensitive read in a transaction of
// its own. The refusal rolls the read's transaction back, and an audit row that rolls back
// with the thing it was auditing is an audit row nobody ever sees.
func (s *Service) recordDenial(ctx context.Context, rc identity.RequestContext,
	personID, claimID uuid.UUID, req AccessRequest,
) {
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		return s.recordAccess(ctx, tx, rc, personID, claimID, audit.AccessView, req, audit.OutcomeDenied)
	})
	if err != nil {
		s.logger.Error("claim: could not record a denied clinical access", "error", err)
	}
}

// record writes one business audit row.
//
// Everything that reaches a detail map here is an id, a code, a count or a status. A line
// description, a diagnosis reference and a reviewer's comment never do — `audit.SanitizeDetail`
// would drop some of them and would not drop the rest, and "the sanitiser would have caught
// it" is not the standard a clinical field is held to.
func (s *Service) record(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	action string, claimID uuid.UUID, detail map[string]any,
) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID),
		MembershipID: nullUUID(rc.MembershipID), Category: audit.CategoryBusiness,
		ActionCode: action, ResourceType: domain.AggregateClaim, ResourceID: nullUUID(claimID),
		Outcome: audit.OutcomeSuccess, Detail: detail,
	})
}

// projectionFor is the one place this package decides what a caller may see, and it decides
// it by asking WP-I5-01's own `decide` rather than by having a rule of its own.
//
// The sensitivity is the claim's case's. A claim hanging off no case is STANDARD: a claim
// with no episode of care behind it carries a line description and nothing else clinical, and
// "STANDARD" is the only honest answer a record with no case can give.
func (s *Service) projectionFor(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ClaimRecord, req AccessRequest,
) (healthapp.ClinicalDecision, error) {
	sensitivity := healthdomain.SensitivityStandard
	if record.CaseID != nil {
		value, err := s.repo.CaseSensitivity(ctx, tx, rc.TenantID, *record.CaseID)
		if err != nil {
			return healthapp.ClinicalDecision{}, err
		}
		if value != "" {
			sensitivity = value
		}
	}
	return healthapp.DecideProjection(rc, sensitivity, req)
}

// checkProviderScope refuses a provider-scoped caller acting for somebody else's provider.
func checkProviderScope(rc identity.RequestContext, organizationID uuid.UUID) error {
	scope := scopeOf(rc)
	if !scope.Restricted() {
		return nil
	}
	for _, id := range scope.OrganizationIDs {
		if id == organizationID {
			return nil
		}
	}
	return ErrProviderScope
}

// referenceEncoding is the alphabet a claim reference's random tail is rendered in: upper
// case, no padding, and no characters the column CHECK would refuse.
var referenceEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newReference builds a claim reference: a prefix, the day, and forty random bits. The tail
// is random rather than sequential because a sequential reference tells a competitor how many
// claims a tenant settled last month.
func newReference(now time.Time) (string, error) {
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("claim: generate reference: %w", err)
	}
	return fmt.Sprintf("CLM-%s-%s", now.UTC().Format("20060102"),
		referenceEncoding.EncodeToString(buf)), nil
}

// referenceAttempts is how many times a create retries a reference collision. The tail is
// forty random bits, so two attempts is already generous; the loop exists so that a collision
// is a retry rather than an error somebody has to read.
const referenceAttempts = 5

// claimCursor is the keyset position of a row on the (created_at DESC, id DESC) order.
func claimCursor(r ClaimRecord) httpx.Cursor {
	return httpx.Cursor{CreatedAt: r.CreatedAt, ID: r.ID}
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

// quantityOf parses an exact decimal or reports which field could not be read. Nothing in
// this package ever turns one of these into a float.
func quantityOf(raw, field string) (benefitdomain.Quantity, error) {
	value, err := benefitdomain.ParseQuantity(raw)
	if err != nil {
		return benefitdomain.Quantity{}, fieldError(field, "FORMAT", "kesin ondalık bir sayı olmalı")
	}
	return value, nil
}

// zero is the exact decimal zero, spelled once.
func zero() benefitdomain.Quantity { return benefitdomain.ZeroQuantity() }
