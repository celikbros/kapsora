// Package application implements the authorization use cases: turning an approval into a
// hold on entitlement, recording and completing what was delivered against it, releasing
// what was never used, and the vouchers a member shows at a counter. Transactions are
// opened here with db.WithTenantTx, so a write, its ledger movements and its audit row
// commit together and RLS is bound for every statement.
//
// One thing this package never does: it never writes a balance. Every reserve, release
// and consume goes through the ledger port below, which is benefit/ledger, so the account
// lock that makes concurrent holds safe is always taken and every movement lands in the
// append-only ledger with its own idempotency key. There is no statement in this package's
// repository that touches benefit.entitlement_account at all.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding this package (migration 000026). They live here rather than in the
// transport because who may promise entitlement is a business rule, not a routing detail.
const (
	PermissionManage = "authorization.manage"
	PermissionRecord = "fulfilment.record"
	PermissionRedeem = "voucher.redeem"
	// PermissionRequestRead is the service request's own read permission. An
	// authorization carries out a request's decision, so anybody who may read the
	// request may read what it promised.
	PermissionRequestRead = "service_request.read"
)

// ScopeOrganization is the iam.access_grant.scope_type of provider-side roles (v1.2 6.3).
const ScopeOrganization = "ORGANIZATION"

// Errors mapped by the transport layer to problem codes.
var (
	ErrAuthorizationNotFound = errors.New("authorization: authorization not found")
	ErrFulfilmentNotFound    = errors.New("authorization: fulfilment not found")
	ErrVoucherNotFound       = errors.New("authorization: voucher not found")
	ErrRequestNotFound       = errors.New("authorization: service request not found")

	// ErrRequestNotApproved refuses a hold on a decision nobody made.
	ErrRequestNotApproved = errors.New("authorization: the request has not been approved")
	// ErrNoApprovedItems refuses an authorization with nothing to promise.
	ErrNoApprovedItems = errors.New("authorization: the request has no approved line to authorize")
	// ErrAccountNotFound is a line that maps onto no open entitlement account.
	ErrAccountNotFound = errors.New("authorization: no entitlement account covers this line")

	ErrAuthorizationNotActive = errors.New("authorization: the authorization no longer holds entitlement")
	ErrFulfilmentNotRecorded  = errors.New("authorization: only a recorded fulfilment can be changed")
	// ErrFulfilmentCompleted refuses cancelling something already consumed: undoing a
	// ledger movement is a reversal, not a status change.
	ErrFulfilmentCompleted    = errors.New("authorization: a completed fulfilment cannot be cancelled")
	ErrOverFulfilment         = errors.New("authorization: more was delivered than was approved")
	ErrItemNotInAuthorization = errors.New("authorization: the line does not belong to this authorization")

	ErrVoucherAlreadyRedeemed = errors.New("authorization: the voucher has already been redeemed")
	ErrVoucherExpired         = errors.New("authorization: the voucher is outside its validity window")
	ErrVoucherRevoked         = errors.New("authorization: the voucher has been revoked")
	ErrVoucherAlreadyIssued   = errors.New("authorization: the authorization already has a live voucher")

	ErrVersionMismatch    = errors.New("authorization: row version does not match If-Match")
	ErrReferenceCollision = errors.New("authorization: could not allocate a free reference")
	// ErrIdempotencyKeyRequired is a command that changes state without a key. The
	// middleware normally refuses it first; the service refuses it again because a
	// create without a key is a create that can reserve twice.
	ErrIdempotencyKeyRequired = errors.New("authorization: an idempotency key is required")
	// ErrAdoptionNotSingleLine refuses an authorization that adopts an existing hold and
	// has more than one line. One hold covers one account; two lines may draw on two, and
	// there is no honest way to split a quantity somebody else reserved between them.
	ErrAdoptionNotSingleLine = errors.New("authorization: only a single-line authorization may adopt a reservation")
	// ErrAdoptedReservationTooSmall refuses an approval for more than the adopted hold
	// actually holds. Promising entitlement nobody reserved is the failure this whole
	// path exists to avoid, and it is refused here rather than found at consumption.
	ErrAdoptedReservationTooSmall = errors.New("authorization: the adopted reservation does not cover what was approved")
	// ErrKeyReplay reports that this key already produced an authorization. The caller
	// re-reads it in a fresh transaction: a unique violation has aborted this one.
	ErrKeyReplay = errors.New("authorization: this idempotency key already created an authorization")
)

// Scope is the caller's provider boundary. A nil slice means "no restriction"; an empty
// non-nil slice restricts the caller to nothing, which is the safe reading of a grant that
// names no organization.
type Scope struct {
	OrganizationIDs []uuid.UUID
}

// Restricted reports whether the caller is bound to a set of organizations.
func (s Scope) Restricted() bool { return s.OrganizationIDs != nil }

// scopeOf reads the caller's organization grants. A tenant-wide actor has none.
func scopeOf(rc identity.RequestContext) Scope {
	var ids []uuid.UUID
	for _, s := range rc.Scopes {
		if s.Type != ScopeOrganization {
			continue
		}
		if ids == nil {
			ids = []uuid.UUID{}
		}
		if s.ID.Valid {
			ids = append(ids, s.ID.UUID)
		}
	}
	return Scope{OrganizationIDs: ids}
}

// AuthorizationRecord is one service.authorization row. Every decimal is carried as the
// exact text the numeric column holds.
type AuthorizationRecord struct {
	ID               uuid.UUID
	RequestID        uuid.UUID
	Reference        string
	ValidFrom        time.Time
	ValidTo          time.Time
	Status           string
	ReservedTotal    string
	ConsumedTotal    string
	CurrencyCode     *string
	PriceQuoteID     *uuid.UUID
	ApprovedBy       *uuid.UUID
	ApprovedAt       time.Time
	CancelReasonCode *string
	CreatedAt        time.Time
	RowVersion       int64
}

// NewAuthorizationRow is the insert payload of an authorization header.
type NewAuthorizationRow struct {
	RequestID      uuid.UUID
	Reference      string
	ValidFrom      time.Time
	ValidTo        time.Time
	ReservedTotal  benefitdomain.Quantity
	CurrencyCode   *string
	PriceQuoteID   *uuid.UUID
	ApprovedAt     time.Time
	IdempotencyKey string
	ActorID        *uuid.UUID
}

// AuthorizationQuery is the repository-level authorization filter.
type AuthorizationQuery struct {
	Scope                  Scope
	Status                 string
	RequestID              *uuid.UUID
	PersonID               *uuid.UUID
	ProviderOrganizationID *uuid.UUID
	ValidFrom              *time.Time
	ValidTo                *time.Time
	After                  *httpx.Cursor
	PageSize               int
}

// AuthorizationItemRecord is one service.authorization_item row.
type AuthorizationItemRecord struct {
	ID                    uuid.UUID
	AuthorizationID       uuid.UUID
	RequestItemID         uuid.UUID
	ServiceDefinitionID   uuid.UUID
	EntitlementUnitFactor string
	ApprovedQuantity      string
	ApprovedAmount        *string
	MemberAmount          string
	ReservationID         *uuid.UUID
	ConsumedQuantity      string
	RowVersion            int64
}

// Remaining is what this line still holds: what was approved less what has been
// delivered. It is the quantity a release gives back and the ceiling a consume may not
// pass.
func (r AuthorizationItemRecord) Remaining() (benefitdomain.Quantity, error) {
	approved, err := benefitdomain.ParseQuantity(r.ApprovedQuantity)
	if err != nil {
		return benefitdomain.Quantity{}, err
	}
	consumed, err := benefitdomain.ParseQuantity(r.ConsumedQuantity)
	if err != nil {
		return benefitdomain.Quantity{}, err
	}
	return approved.Sub(consumed), nil
}

// NewAuthorizationItemRow is one approved line of a new authorization.
type NewAuthorizationItemRow struct {
	AuthorizationID       uuid.UUID
	RequestItemID         uuid.UUID
	ServiceDefinitionID   uuid.UUID
	EntitlementUnitFactor benefitdomain.Quantity
	ApprovedQuantity      benefitdomain.Quantity
	ApprovedAmount        *benefitdomain.Quantity
	MemberAmount          benefitdomain.Quantity
}

// FulfilmentRecord is one service.fulfilment row.
type FulfilmentRecord struct {
	ID                uuid.UUID
	Reference         string
	AuthorizationID   uuid.UUID
	ProviderProfileID *uuid.UUID
	LocationID        *uuid.UUID
	PractitionerID    *uuid.UUID
	PerformedAt       time.Time
	Status            string
	CompletedAt       *time.Time
	CancelledAt       *time.Time
	CancelReasonCode  *string
	RecordedBy        *uuid.UUID
	CreatedAt         time.Time
	RowVersion        int64
}

// NewFulfilmentRow is the insert payload of a fulfilment header.
type NewFulfilmentRow struct {
	Reference         string
	AuthorizationID   uuid.UUID
	ProviderProfileID *uuid.UUID
	LocationID        *uuid.UUID
	PractitionerID    *uuid.UUID
	PerformedAt       time.Time
	ActorID           *uuid.UUID
}

// FulfilmentQuery is the repository-level fulfilment filter.
type FulfilmentQuery struct {
	Scope             Scope
	Status            string
	AuthorizationID   *uuid.UUID
	ProviderProfileID *uuid.UUID
	PerformedFrom     *time.Time
	PerformedTo       *time.Time
	After             *httpx.Cursor
	PageSize          int
}

// FulfilmentItemRecord is one service.fulfilment_item row.
type FulfilmentItemRecord struct {
	ID                  uuid.UUID
	FulfilmentID        uuid.UUID
	AuthorizationItemID uuid.UUID
	ServiceDefinitionID uuid.UUID
	ActualQuantity      string
	ActualAmount        *string
}

// NewFulfilmentItemRow is one delivered line.
type NewFulfilmentItemRow struct {
	FulfilmentID        uuid.UUID
	AuthorizationItemID uuid.UUID
	ServiceDefinitionID uuid.UUID
	ActualQuantity      benefitdomain.Quantity
	ActualAmount        *benefitdomain.Quantity
}

// VoucherRecord is one service.voucher row. There is no token field: the plaintext is
// returned once by the issue command and is not stored anywhere this could read it from.
type VoucherRecord struct {
	ID                uuid.UUID
	AuthorizationID   uuid.UUID
	MaskedToken       string
	ValidFrom         time.Time
	ValidTo           time.Time
	Status            string
	RedeemedAt        *time.Time
	RedeemedByActorID *uuid.UUID
	IssuedAt          time.Time
	CreatedAt         time.Time
	RowVersion        int64
}

// NewVoucherRow is the insert payload of a voucher. Only the digest and the masked tail
// travel this far; the plaintext never leaves the command that generated it.
type NewVoucherRow struct {
	AuthorizationID uuid.UUID
	TokenHash       []byte
	TokenMasked     string
	ValidFrom       time.Time
	ValidTo         time.Time
	ActorID         *uuid.UUID
}

// RequestRecord is the part of a service request an authorization needs: what was
// decided, for whom and on which day the balances are read.
type RequestRecord struct {
	ID               uuid.UUID
	Reference        string
	PersonID         uuid.UUID
	ProgramID        uuid.UUID
	EnrollmentID     uuid.UUID
	ServiceDate      time.Time
	Status           string
	CurrentVersionNo int
}

// ExpiringAuthorizationRow is one authorization about to run out, with the member to tell.
type ExpiringAuthorizationRow struct {
	ID        uuid.UUID
	Reference string
	ValidTo   time.Time
	PersonID  uuid.UUID
}

// RequestItemRecord is one decided line of a request's current version.
type RequestItemRecord struct {
	ID                  uuid.UUID
	LineNo              int
	ServiceDefinitionID uuid.UUID
	UnitType            string
	Status              string
	ApprovedQuantity    string
	ApprovedAmount      string
	CurrencyCode        *string
}

// EntitlementTarget binds a service to a definition in the request's published plan.
type EntitlementTarget struct {
	DefinitionID uuid.UUID
	Factor       benefitdomain.Quantity
}

// Repository is the persistence port; every method runs inside the caller's transaction,
// which db.WithTenantTx has already bound to the tenant so RLS is active. The provider
// boundary is a repository concern too: the scope is passed down rather than checked
// above, so a row outside it is genuinely not there rather than fetched and then hidden.
//
// Nothing here writes benefit.entitlement_account or benefit.entitlement_ledger. The only
// entitlement column this package's SQL touches is the reservation id it stores on a line.
type Repository interface {
	ReservationRemaining(ctx context.Context, tx pgx.Tx, tenantID, reservationID uuid.UUID) (benefitdomain.Quantity, error)
	ResolveEntitlements(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID uuid.UUID, day time.Time, services []uuid.UUID) (map[uuid.UUID]EntitlementTarget, error)
	CreateAuthorization(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewAuthorizationRow) (AuthorizationRecord, error)
	GetAuthorization(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (AuthorizationRecord, error)
	// LockAuthorization reads the row FOR UPDATE, so two commands on one authorization
	// serialise instead of racing each other into two different outcomes.
	LockAuthorization(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (AuthorizationRecord, error)
	GetAuthorizationByKey(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, key string) (AuthorizationRecord, error)
	ListAuthorizations(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q AuthorizationQuery) ([]AuthorizationRecord, error)
	ExtendAuthorization(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, validTo time.Time, actorID *uuid.UUID, expected int64) error
	CancelAuthorization(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, reasonCode string, actorID *uuid.UUID, expected int64) error
	// ApplyConsumption adds what a completed fulfilment delivered to the header total and
	// moves the status the new total implies. It never subtracts: history is not edited.
	ApplyConsumption(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, delta benefitdomain.Quantity, actorID *uuid.UUID) error
	// ListExpirableAuthorizations takes the holds past their end FOR UPDATE SKIP LOCKED,
	// so two schedulers that both believe they lead cannot expire the same row twice.
	ListExpirableAuthorizations(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, before time.Time, limit int) ([]uuid.UUID, error)
	MarkAuthorizationExpired(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error)

	CreateAuthorizationItem(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewAuthorizationItemRow) (uuid.UUID, error)
	SetAuthorizationItemReservation(ctx context.Context, tx pgx.Tx, tenantID, itemID, reservationID uuid.UUID) error
	// ListAuthorizationItems reads the lines; forUpdate takes them FOR UPDATE, which a
	// consuming or releasing command needs so the remainder it computes is one nobody
	// else can change before it commits.
	ListAuthorizationItems(ctx context.Context, tx pgx.Tx, tenantID, authorizationID uuid.UUID, forUpdate bool) ([]AuthorizationItemRecord, error)
	AddAuthorizationItemConsumption(ctx context.Context, tx pgx.Tx, tenantID, itemID uuid.UUID, delta benefitdomain.Quantity) error

	CreateFulfilment(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewFulfilmentRow) (FulfilmentRecord, error)
	GetFulfilment(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (FulfilmentRecord, error)
	LockFulfilment(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (FulfilmentRecord, error)
	ListFulfilments(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q FulfilmentQuery) ([]FulfilmentRecord, error)
	CompleteFulfilment(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, completedAt time.Time, expected int64) error
	CancelFulfilment(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, cancelledAt time.Time, reasonCode string, expected int64) error
	CreateFulfilmentItem(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewFulfilmentItemRow) (uuid.UUID, error)
	ListFulfilmentItems(ctx context.Context, tx pgx.Tx, tenantID, fulfilmentID uuid.UUID) ([]FulfilmentItemRecord, error)

	CountLiveVouchers(ctx context.Context, tx pgx.Tx, tenantID, authorizationID uuid.UUID) (int64, error)
	CreateVoucher(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewVoucherRow) (VoucherRecord, error)
	// LockVoucherByTokenHash finds a voucher by the digest of the token presented. The
	// plaintext is never a query argument: a statement log that kept one would be a list
	// of usable vouchers.
	LockVoucherByTokenHash(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, hash []byte, scope Scope) (VoucherRecord, error)
	ListVouchers(ctx context.Context, tx pgx.Tx, tenantID, authorizationID uuid.UUID) ([]VoucherRecord, error)
	MarkVoucherRedeemed(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, redeemedAt time.Time, actorID *uuid.UUID) (bool, error)
	// MarkVoucherRevoked withdraws a live voucher so a replacement may be issued. It
	// reports false for a voucher that was not ISSUED, which is what makes a second
	// revoke a no-op rather than an error.
	MarkVoucherRevoked(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, reasonCode string) (bool, error)

	GetRequest(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (RequestRecord, error)

	// ListExpiringAuthorizations lists the authorizations whose validity ends inside one
	// day, with the member behind each of them, for the reminder sweep (WP-I5-05).
	ListExpiringAuthorizations(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
		dayStart, dayEnd time.Time, limit int) ([]ExpiringAuthorizationRow, error)
	// ListDecidedItems returns the lines of the request's current version with the
	// decisions a reviewer recorded on them.
	ListDecidedItems(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID, versionNo int) ([]RequestItemRecord, error)
	// ServiceDefinitionCodes maps the catalogue ids of the lines onto their codes, which
	// is how a line is matched to an entitlement account until a catalogue-to-entitlement
	// table exists (the convention WP-I4-01's submit gate already uses).
	ServiceDefinitionCodes(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]string, error)
	// ResolveAccounts lists the entitlement accounts a person may spend from on a day,
	// including the family-shared ones of their principal. It is the ledger's own read
	// side: this package neither opens an account nor computes a balance.
	ResolveAccounts(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID, asOf time.Time) ([]ledger.Account, error)
	// ActiveTenants lists the tenants the expiry job walks. platform.tenant carries no
	// RLS, so it is read outside a tenant transaction like the other cross-tenant jobs.
	ActiveTenants(ctx context.Context, tx pgx.Tx) ([]uuid.UUID, error)
}

// Ledger is the movement engine this package drives and never reimplements. It is
// benefit/ledger.Ledger, narrowed to the three movements an authorization makes, so that
// the compiler agrees this package cannot post a GRANT or an ADJUST.
type Ledger interface {
	Reserve(ctx context.Context, tx pgx.Tx, in ledger.ReserveInput) (ledger.Reservation, error)
	Release(ctx context.Context, tx pgx.Tx, in ledger.MovementInput) (ledger.Reservation, error)
	Consume(ctx context.Context, tx pgx.Tx, in ledger.MovementInput) (ledger.Reservation, error)
	// AdoptReservation takes over a hold somebody else already placed, moving its deadline
	// out to this promise's own end. It posts no movement, because nothing moves: the
	// quantity is already reserved.
	//
	// It is the fourth verb because of the booking (WP-I6-02). A held room reserves its
	// nights the moment the countdown starts, fifteen minutes before anybody has approved
	// anything; when the approval arrives, this package must promise the same nights for
	// the length of the stay. Reserving them again would be two real holds for one stay —
	// the ledger conserved, the constraints satisfied, and the member's plan drawn down
	// twice with nothing anywhere saying so.
	AdoptReservation(ctx context.Context, tx pgx.Tx, in ledger.AdoptInput) (ledger.Reservation, error)
}
