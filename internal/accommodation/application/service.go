package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/accommodation/domain"
	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/benefit/eligibility"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// EligibilityChecker is the eligibility service as this package uses it. It is an
// interface rather than the concrete *eligibility.Service for one reason: the search must
// record an immutable evaluation on every run, and a test that could not observe that
// would be a test that passes when the recording is deleted.
type EligibilityChecker interface {
	Check(ctx context.Context, rc identity.RequestContext, in eligibility.CheckInput) (eligibility.ResultView, error)
}

// Service implements the accommodation use cases.
type Service struct {
	pool        *pgxpool.Pool
	repo        Repository
	bookings    BookingRepository
	ledger      LedgerPort
	requests    RequestPort
	auths       AuthorizationPort
	policies    LodgingPolicyPort
	eligibility EligibilityChecker
	audit       audit.Recorder
	cursors     *httpx.CursorCodec
	logger      *slog.Logger
	now         func() time.Time
}

// Deps are the collaborators of the service.
type Deps struct {
	Pool *pgxpool.Pool
	Repo Repository
	// Eligibility answers what the person is entitled to and writes the evaluation the
	// search is remembered by. nil leaves the search unavailable rather than
	// half-working: an availability answer with no record of what was shown is exactly
	// the thing WP-I2-04 exists to prevent.
	Eligibility EligibilityChecker
	// Bookings is the booking half's persistence. A process wired without it can search
	// and cannot hold, which is what a search-only deployment actually is.
	Bookings BookingRepository
	// Ledger is benefit/ledger. It must be the same movement engine the rest of the
	// process holds: two ledgers over one database would take two different account locks
	// for one account, which is exactly how a no-double-spend rule stops holding.
	Ledger LedgerPort
	// Requests, Authorizations and Policies default to their refusing implementations, so
	// a process that was never given them says so rather than half-confirming a booking.
	Requests       RequestPort
	Authorizations AuthorizationPort
	Policies       LodgingPolicyPort
	Audit          audit.Recorder
	// Cursors may be nil in a process that never pages.
	Cursors *httpx.CursorCodec
	Logger  *slog.Logger
	// Now overrides the clock in tests; nil means time.Now().UTC().
	Now func() time.Time
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil || d.Repo == nil {
		return nil, errors.New("accommodation: pool and repository are required")
	}
	if d.Audit == nil {
		d.Audit = audit.NopRecorder{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	if d.Requests == nil {
		d.Requests = NoRequests{}
	}
	if d.Authorizations == nil {
		d.Authorizations = NoAuthorizations{}
	}
	if d.Policies == nil {
		d.Policies = NoPolicies{}
	}
	return &Service{
		pool: d.Pool, repo: d.Repo, bookings: d.Bookings, ledger: d.Ledger,
		requests: d.Requests, auths: d.Authorizations, policies: d.Policies,
		eligibility: d.Eligibility, audit: d.Audit,
		cursors: d.Cursors, logger: d.Logger, now: d.Now,
	}, nil
}

// withTx runs fn inside a tenant-bound transaction, so RLS is active for every statement.
func (s *Service) withTx(ctx context.Context, rc identity.RequestContext,
	fn func(ctx context.Context, tx pgx.Tx) error,
) error {
	return db.WithTenantTx(ctx, s.pool,
		db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, fn)
}

// paging decodes the cursor and clamps the limit; the repository is asked for one row more
// than the page size so the caller learns whether a next page exists.
func (s *Service) paging(cursor string, limit int) (after *httpx.Cursor, pageSize int, err error) {
	if s.cursors == nil {
		return nil, httpx.ClampLimit(limit), nil
	}
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

// scopeOf is the provider boundary of a caller, as a set of tenant organization ids. A
// caller with no ORGANIZATION grant returns nil, which the queries read as "the whole
// tenant"; a provider clerk returns its own organizations and sees nothing else.
//
// This is the one place the boundary is computed. Every read and every write passes the
// result to the repository, so there is no path that forgets it.
func scopeOf(rc identity.RequestContext) []uuid.UUID {
	var out []uuid.UUID
	for _, scope := range rc.Scopes {
		if scope.Type != ScopeOrganization || !scope.ID.Valid || scope.ID.UUID == uuid.Nil {
			continue
		}
		out = append(out, scope.ID.UUID)
	}
	return out
}

// insideScope reports whether the organization is one the caller may act for. A caller
// with no ORGANIZATION grant may act for any of the tenant's providers; a provider clerk
// may act for its own and no other.
func insideScope(rc identity.RequestContext, organizationID uuid.UUID) bool {
	scopes := scopeOf(rc)
	if len(scopes) == 0 {
		return true
	}
	for _, id := range scopes {
		if id == organizationID {
			return true
		}
	}
	return false
}

// record writes one business audit row. A property carries no personal data, so the detail
// is ids, codes and counts only — and a search's detail never carries the person's name,
// their identifier or where they wanted to go, only the ids the record needs to be
// findable.
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

// Audit action and resource codes.
const (
	ActionPropertyCreate     = "accommodation.property.create"
	ActionPropertyUpdate     = "accommodation.property.update"
	ActionRoomTypeCreate     = "accommodation.room_type.create"
	ActionRoomTypeUpdate     = "accommodation.room_type.update"
	ActionInventoryPut       = "accommodation.inventory.put"
	ActionAvailabilitySearch = "accommodation.availability.search"

	ActionBookingHold            = "accommodation.booking.hold"
	ActionBookingRelease         = "accommodation.booking.release"
	ActionBookingConfirm         = "accommodation.booking.confirm"
	ActionBookingPendingApproval = "accommodation.booking.pending_approval"
	ActionBookingCancel          = "accommodation.booking.cancel"
	ActionBookingExpire          = "accommodation.booking.expire"
	ActionBookingVoucherIssue    = "accommodation.booking.voucher.issue"

	ResourceProperty  = "accommodation_property"
	ResourceRoomType  = "accommodation_room_type"
	ResourceInventory = "accommodation_inventory_day"
	ResourceBooking   = "accommodation_booking"
)

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

// fieldError builds a one-field validation error, which the transport answers 422 with.
func fieldError(field, code, message string) error {
	ve := &domain.ValidationError{}
	ve.Add(field, code, message)
	return ve
}

// attributesOrEmpty renders a room type's free-form attributes, refusing anything that is
// not a JSON object. The column has a CHECK saying the same thing; this is the half that
// answers 422 on the field rather than a constraint violation.
func attributesOrEmpty(field string, raw json.RawMessage, ve *domain.ValidationError) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		ve.Add(field, "FORMAT", "nesne olmalı")
		return []byte("{}")
	}
	return raw
}
