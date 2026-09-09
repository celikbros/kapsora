package application

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/billing/settings"
	healthapp "github.com/celikbros/kapsora/internal/health/application"
	healthdomain "github.com/celikbros/kapsora/internal/health/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Service implements the invoice use cases.
type Service struct {
	pool    *pgxpool.Pool
	repo    Repository
	claims  ClaimsPort
	audit   audit.Recorder
	cursors *httpx.CursorCodec
	logger  *slog.Logger
	now     func() time.Time
}

// Deps are the collaborators of the service.
type Deps struct {
	Pool *pgxpool.Pool
	Repo Repository
	// Claims is the claim module's INVOICED transition and the way back. It runs inside this
	// package's transaction. The default refuses, which is the honest behaviour of a process
	// that was never given one.
	Claims ClaimsPort
	Audit  audit.Recorder
	// Cursors may be nil in a process that never pages.
	Cursors *httpx.CursorCodec
	Logger  *slog.Logger
	// Now defaults to time.Now; tests pin it so a submitted_at is deterministic.
	Now func() time.Time
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil || d.Repo == nil {
		return nil, errors.New("billing: pool and repository are required")
	}
	if d.Claims == nil {
		d.Claims = NoClaims{}
	}
	if d.Audit == nil {
		d.Audit = audit.NopRecorder{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{
		pool: d.Pool, repo: d.Repo, claims: d.Claims, audit: d.Audit,
		cursors: d.Cursors, logger: d.Logger, now: d.Now,
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

// settingsFor reads the tenant's tolerance and image policy inside the caller's transaction.
func (s *Service) settingsFor(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (settings.Values, error) {
	return settings.Load(ctx, tx, tenantID)
}

// projectionFor is the one place this package decides what a caller may see, and it decides
// it by asking WP-I5-01's own rule rather than by having a rule of its own.
//
// The sensitivity is STANDARD, always. An invoice hangs off no episode of care: what is
// clinical about it is the line description of the claims it covers, which v1.2 §2.11 marks as
// possibly clinical, and possibly clinical is clinical. A caller holding `health.clinical.read`
// reads it; the sponsor's HR user, who deliberately does not hold it, does not — and neither
// does a payer's finance user, who has no reason to.
//
// There is no purpose header and no access event, because a STANDARD record needs neither:
// the sensitive path of WP-I5-01 is about a case marked sensitive, and an invoice is not one.
func projectionFor(rc identity.RequestContext) Projection {
	decision, err := healthapp.DecideProjection(rc, healthdomain.SensitivityStandard,
		healthapp.AccessRequest{})
	if err != nil {
		// DecideProjection errors only on the sensitive path, which STANDARD never reaches.
		// The safe reading of an impossible error is the narrower projection.
		return ProjectionFinancial
	}
	return decision.Projection
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

// record writes one business audit row.
//
// Everything that reaches a detail map here is an id, a code, a count, a status or an exact
// decimal. The provider's tax identity never does — not the number, which this package never
// holds, and not the hash, which is a stable identifier of a taxpayer across every tenant and
// therefore exactly the thing an audit log must not correlate on.
func (s *Service) record(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	action string, invoiceID uuid.UUID, detail map[string]any,
) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID),
		MembershipID: nullUUID(rc.MembershipID), Category: audit.CategoryBusiness,
		ActionCode: action, ResourceType: domain.AggregateInvoice,
		ResourceID: nullUUID(invoiceID), Outcome: audit.OutcomeSuccess, Detail: detail,
	})
}

// buildView assembles the invoice a caller reads: the header, its allocations, the sum of the
// active ones and the gap between that sum and what the invoice bills — all projected.
//
// The total is summed here, in Go, in exact decimals, from the rows themselves. The list
// endpoint sums the same figure in SQL because a page of fifty invoices would otherwise be
// fifty extra reads, and a test compares the two: two sums are two answers unless something
// makes them agree.
func buildView(rec InvoiceRecord, rows []AllocationRecord, p Projection) InvoiceView {
	total := zero()
	for _, row := range rows {
		if !row.Active {
			continue
		}
		total = total.Add(quantityOrZero(row.AllocatedAmount))
	}
	payable := quantityOrZero(rec.PayableAmount)
	return InvoiceView{
		Projection: p, Invoice: rec, Allocations: projectAllocations(rows, p),
		AllocationTotal: total.String(), AllocationDifference: payable.Sub(total).String(),
	}
}

// invoiceCursor is the keyset position of a row on the (created_at DESC, id DESC) order.
func invoiceCursor(r InvoiceSummaryRecord) httpx.Cursor {
	return httpx.Cursor{CreatedAt: r.CreatedAt, ID: r.ID}
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

func actorPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// fieldError builds a one-field validation error.
func fieldError(field, code, message string) error {
	ve := &domain.ValidationError{}
	ve.Add(field, code, message)
	return ve
}

// quantityOrZero parses an exact decimal that the database produced, treating an unreadable
// one as zero. Nothing that reaches it can fail: every money column is numeric(20,6) rendered
// by `trim_scale(...)::text`.
func quantityOrZero(raw string) benefitdomain.Quantity {
	value, err := benefitdomain.ParseQuantity(raw)
	if err != nil {
		return zero()
	}
	return value
}

// zero is the exact decimal zero, spelled once.
func zero() benefitdomain.Quantity { return benefitdomain.ZeroQuantity() }
