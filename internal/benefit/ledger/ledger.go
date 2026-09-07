// Package ledger owns the entitlement balances: accounts, the append-only movement
// ledger and the reservations that stand between "held for a request" and "spent".
//
// The ledger is the record; the columns on benefit.entitlement_account are its
// materialisation. Every movement runs the same shape inside the caller's transaction:
// the account row is taken with SELECT ... FOR UPDATE, the delta vector is appended to
// benefit.entitlement_ledger, and exactly the same deltas are added to the account. The
// account lock is what makes concurrent reserves safe: two requests for the same account
// serialise on it, so the second one sees the first one's balance and cannot overspend.
// ck_entitlement_account_balance and ck_entitlement_ledger_conservation are the
// database's own proof that the ledger and the balances never diverge.
//
// Quantities are numeric(20,6) and are handled as domain.Quantity, an exact base-ten
// decimal. No balance ever passes through a float.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// Movement types of benefit.entitlement_ledger (migration 000005).
const (
	MovementGrant   = "GRANT"
	MovementReserve = "RESERVE"
	MovementRelease = "RELEASE"
	MovementConsume = "CONSUME"
	MovementReverse = "REVERSE"
	MovementExpire  = "EXPIRE"
	MovementAdjust  = "ADJUST"
)

// Reservation statuses (migration 000016).
const (
	ReservationHeld              = "HELD"
	ReservationPartiallyConsumed = "PARTIALLY_CONSUMED"
	ReservationConsumed          = "CONSUMED"
	ReservationReleased          = "RELEASED"
	ReservationExpired           = "EXPIRED"
)

// Account statuses (migration 000005).
const (
	AccountOpen   = "OPEN"
	AccountFrozen = "FROZEN"
	AccountClosed = "CLOSED"
)

// Reference types. The first four are the reservation's own CHECK constraint; the rest
// only ever appear on ledger rows, whose reference_type is free text.
const (
	ReferenceServiceRequest = "SERVICE_REQUEST"
	ReferenceBooking        = "BOOKING"
	ReferenceAuthorization  = "AUTHORIZATION"
	ReferenceManual         = "MANUAL"
	ReferenceEnrollment     = "ENROLLMENT"
	ReferenceLedgerEntry    = "LEDGER_ENTRY"
	ReferenceAdjustment     = "ADJUSTMENT"
)

// Reason codes this package writes itself.
const (
	ReasonExpired       = "EXPIRED"
	ReasonDirectConsume = "DIRECT_CONSUME"
)

// ExpireBatchSize is how many holds one Expire call releases (WP-I2-03 section 2.6).
const ExpireBatchSize = 200

// ReservationReferenceTypes is the set benefit.entitlement_reservation accepts.
var ReservationReferenceTypes = []string{
	ReferenceServiceRequest, ReferenceBooking, ReferenceAuthorization, ReferenceManual,
}

// Errors the transport layer maps to problem+json codes.
var (
	// ErrNotFound is a resource of this package that the tenant cannot see. Unknown and
	// foreign ids are indistinguishable: RLS simply hides the other tenant's rows.
	ErrNotFound            = errors.New("benefit: resource not found")
	ErrAccountNotFound     = errors.New("benefit: entitlement account not found")
	ErrReservationNotFound = errors.New("benefit: entitlement reservation not found")
	ErrLedgerEntryNotFound = errors.New("benefit: ledger entry not found")
	ErrAdjustmentNotFound  = errors.New("benefit: entitlement adjustment not found")
	ErrEnrollmentNotFound  = errors.New("benefit: enrollment not found")

	// ErrAccountFrozen refuses spending on an account the reconciliation job froze.
	ErrAccountFrozen = errors.New("benefit: entitlement account is frozen")
	ErrAccountClosed = errors.New("benefit: entitlement account is closed")
	// ErrInsufficient is the no-double-spend rule: available - quantity would go
	// negative and the definition does not allow an overdraft.
	ErrInsufficient = errors.New("benefit: insufficient entitlement balance")

	ErrQuantityInvalid   = errors.New("benefit: quantity must be a positive numeric(20,6) value")
	ErrQuantityRemainder = errors.New("benefit: quantity exceeds the remainder of the reservation")
	ErrReservationClosed = errors.New("benefit: reservation is no longer open")

	// ErrIdempotencyKeyReuse is the same key with different arguments: 409.
	ErrIdempotencyKeyReuse = errors.New("benefit: idempotency key reused with different arguments")
	// ErrIdempotentReplay reports that the movement had already been applied; the
	// original result is returned alongside it (see ReplayError).
	ErrIdempotentReplay = errors.New("benefit: idempotent replay")

	ErrEntryNotReversible   = errors.New("benefit: this movement cannot be reversed")
	ErrAlreadyReversed      = errors.New("benefit: this movement has already been reversed")
	ErrPeriodUnsupported    = errors.New("benefit: the benefit period of this definition cannot be computed")
	ErrAdjustmentNotPending = errors.New("benefit: the adjustment has already been decided")
	ErrMakerCheckerSame     = errors.New("benefit: the approver must differ from the requester")
	ErrVersionMismatch      = errors.New("benefit: row version does not match If-Match")
)

// ReplayError reports a movement that had already been applied under the same
// idempotency key and carries the original result, so callers can answer with it. Match
// it with errors.Is(err, ErrIdempotentReplay).
type ReplayError struct {
	Reservation Reservation
	Entry       Entry
}

func (e *ReplayError) Error() string { return ErrIdempotentReplay.Error() }

// Unwrap lets errors.Is(err, ErrIdempotentReplay) succeed.
func (e *ReplayError) Unwrap() error { return ErrIdempotentReplay }

// SQLSTATE codes and constraint names mapped to errors of this package.
const (
	uniqueViolation = "23505"
	checkViolation  = "23514"

	constraintLedgerIdempotency  = "uq_entitlement_ledger_idempotency"
	constraintReservationKey     = "uq_entitlement_reservation_key"
	constraintReservationRefer   = "uq_entitlement_reservation_reference"
	constraintAccountBalance     = "ck_entitlement_account_balance"
	constraintAccountNonNegative = "ck_entitlement_account_nonnegative"
)

// Balances is the five-column balance vector of an account or the sum of a ledger.
type Balances struct {
	Total     domain.Quantity
	Available domain.Quantity
	Reserved  domain.Quantity
	Consumed  domain.Quantity
	Expired   domain.Quantity
}

// Equal reports whether every component matches exactly.
func (b Balances) Equal(o Balances) bool {
	return b.Total.Cmp(o.Total) == 0 && b.Available.Cmp(o.Available) == 0 &&
		b.Reserved.Cmp(o.Reserved) == 0 && b.Consumed.Cmp(o.Consumed) == 0 &&
		b.Expired.Cmp(o.Expired) == 0
}

// DefinitionSummary is the part of the plan configuration a balance reader needs.
type DefinitionSummary struct {
	ID             uuid.UUID
	Code           string
	Name           string
	UnitType       string
	CurrencyCode   string
	FamilyShared   bool
	AllowOverdraft bool
}

// Account is one benefit.entitlement_account row with its definition summary.
type Account struct {
	ID           uuid.UUID
	EnrollmentID uuid.UUID
	// PersonID owns the enrollment; for a family-shared account that is the principal.
	PersonID   uuid.UUID
	PeriodFrom time.Time
	// PeriodTo is the exclusive upper bound; nil for lifetime accounts.
	PeriodTo   *time.Time
	Balances   Balances
	Status     string
	Definition DefinitionSummary
	// Shared is true when the reader reached this account through a principal membership.
	Shared           bool
	OpenReservations []Reservation
	CreatedAt        time.Time
	RowVersion       int64
}

// Reservation is one hold on an account.
type Reservation struct {
	ID            uuid.UUID
	AccountID     uuid.UUID
	ReferenceType string
	ReferenceID   uuid.UUID
	Quantity      domain.Quantity
	Consumed      domain.Quantity
	Released      domain.Quantity
	Status        string
	ExpiresAt     *time.Time
	Key           string
	CreatedAt     time.Time
	RowVersion    int64
}

// Remaining is the part of the hold that is neither consumed nor released yet.
func (r Reservation) Remaining() domain.Quantity {
	return r.Quantity.Sub(r.Consumed).Sub(r.Released)
}

// Open reports whether the reservation can still be consumed or released.
func (r Reservation) Open() bool {
	return r.Status == ReservationHeld || r.Status == ReservationPartiallyConsumed
}

// Entry is one append-only ledger row.
type Entry struct {
	ID            uuid.UUID
	AccountID     uuid.UUID
	MovementType  string
	EffectiveAt   time.Time
	Deltas        Balances
	ReferenceType string
	ReferenceID   uuid.UUID
	Key           string
	ReasonCode    *string
	ReasonText    *string
	ReservationID *uuid.UUID
	CreatedBy     *uuid.UUID
	CreatedAt     time.Time
}

// Ledger applies movements inside a caller-owned transaction. It is stateless apart from
// the clock, so one instance is shared by the API, the worker and the scheduler.
type Ledger struct {
	now func() time.Time
}

// NewLedger returns the movement engine; a nil clock means time.Now().UTC().
func NewLedger(now func() time.Time) *Ledger {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Ledger{now: now}
}

// ReserveInput is a hold request. Quantity must be strictly positive; the pair
// (AccountID, Key) makes the call idempotent.
type ReserveInput struct {
	TenantID      uuid.UUID
	AccountID     uuid.UUID
	Quantity      domain.Quantity
	ReferenceType string
	ReferenceID   uuid.UUID
	Key           string
	ExpiresAt     *time.Time
	ReasonCode    string
	ReasonText    string
	ActorID       uuid.UUID
}

// MovementInput releases or consumes part of an existing hold.
type MovementInput struct {
	TenantID      uuid.UUID
	ReservationID uuid.UUID
	Quantity      domain.Quantity
	Key           string
	ReasonCode    string
	ReasonText    string
	ActorID       uuid.UUID
}

// ReverseInput undoes an earlier movement by writing its mirror image.
type ReverseInput struct {
	TenantID    uuid.UUID
	LedgerEntry uuid.UUID
	Key         string
	ReasonCode  string
	ReasonText  string
	ActorID     uuid.UUID
}

// AdjustInput is a manual correction; Delta is signed and must not be zero.
type AdjustInput struct {
	TenantID      uuid.UUID
	AccountID     uuid.UUID
	Delta         domain.Quantity
	Key           string
	ReferenceType string
	ReferenceID   uuid.UUID
	ReasonCode    string
	ReasonText    string
	ActorID       uuid.UUID
}

// Reserve holds Quantity on the account.
//
// The account row is locked first, so concurrent reserves queue behind each other and
// the availability check below is taken on a balance nobody else can change until this
// transaction ends. The reservation lookup runs after the lock for the same reason: a
// replay of the same key always sees the row the winning transaction committed.
//
// It returns ErrInsufficient when available - quantity would go negative and the
// definition does not allow an overdraft, a *ReplayError (errors.Is ErrIdempotentReplay)
// carrying the existing reservation when the key was already used with the same
// quantity, and ErrIdempotencyKeyReuse when the quantity differs.
func (l *Ledger) Reserve(ctx context.Context, tx pgx.Tx, in ReserveInput) (Reservation, error) {
	if !in.Quantity.IsPositive() {
		return Reservation{}, ErrQuantityInvalid
	}
	if in.Key == "" {
		return Reservation{}, ErrQuantityInvalid
	}
	if !domain.Contains(ReservationReferenceTypes, in.ReferenceType) {
		return Reservation{}, fmt.Errorf("%w: reference type %q", ErrQuantityInvalid, in.ReferenceType)
	}

	account, err := l.lockAccount(ctx, tx, in.TenantID, in.AccountID)
	if err != nil {
		return Reservation{}, err
	}
	if err := spendable(account); err != nil {
		return Reservation{}, err
	}

	existing, err := reservationByKey(ctx, tx, in.TenantID, in.AccountID, in.Key)
	switch {
	case err == nil && existing.Quantity.Cmp(in.Quantity) != 0:
		return existing, ErrIdempotencyKeyReuse
	case err == nil:
		return existing, &ReplayError{Reservation: existing}
	case !errors.Is(err, pgx.ErrNoRows):
		return Reservation{}, err
	}

	if !account.Definition.AllowOverdraft && account.Balances.Available.Sub(in.Quantity).IsNegative() {
		return Reservation{}, ErrInsufficient
	}

	row, err := sqlcgen.New(tx).CreateEntitlementReservation(ctx, sqlcgen.CreateEntitlementReservationParams{
		TenantID: in.TenantID, AccountID: in.AccountID,
		ReferenceType: in.ReferenceType, ReferenceID: in.ReferenceID,
		Quantity: in.Quantity.String(), ExpiresAt: in.ExpiresAt,
		IdempotencyKey: in.Key, CreatedBy: nullUUID(in.ActorID),
	})
	if err != nil {
		return Reservation{}, reservationWriteError(err)
	}

	if _, err := l.post(ctx, tx, postInput{
		TenantID: in.TenantID, AccountID: in.AccountID, MovementType: MovementReserve,
		Deltas:        Balances{Available: in.Quantity.Neg(), Reserved: in.Quantity},
		ReferenceType: in.ReferenceType, ReferenceID: in.ReferenceID, Key: in.Key,
		ReasonCode: in.ReasonCode, ReasonText: in.ReasonText,
		ReservationID: &row.ID, ActorID: in.ActorID,
	}); err != nil {
		return Reservation{}, err
	}

	return Reservation{
		ID: row.ID, AccountID: in.AccountID, ReferenceType: in.ReferenceType, ReferenceID: in.ReferenceID,
		Quantity: in.Quantity, Status: row.Status, ExpiresAt: in.ExpiresAt, Key: in.Key,
		CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// Release returns part of a hold to the available balance. Partial releases are allowed
// up to the un-consumed remainder.
func (l *Ledger) Release(ctx context.Context, tx pgx.Tx, in MovementInput) (Reservation, error) {
	return l.settle(ctx, tx, in, MovementRelease, "")
}

// Consume spends part of a hold. Partial consumption is allowed; the reservation moves
// to PARTIALLY_CONSUMED and then to CONSUMED when nothing is left.
func (l *Ledger) Consume(ctx context.Context, tx pgx.Tx, in MovementInput) (Reservation, error) {
	return l.settle(ctx, tx, in, MovementConsume, "")
}

// AdoptInput hands an existing hold to a longer-lived owner (WP-I6-02).
type AdoptInput struct {
	TenantID      uuid.UUID
	ReservationID uuid.UUID
	// ExpiresAt is the new deadline. It is only ever moved later; a value at or before
	// the hold's current deadline leaves the row untouched.
	ExpiresAt time.Time
}

// AdoptReservation hands an open hold to whatever now owns it, by moving its deadline out.
//
// It exists so that a promise taken once is not taken twice. A booking reserves its nights
// at the hold, under a fifteen-minute deadline; when the reservation request is approved,
// the authorization has to hold the same nights for the length of the stay. Reserving again
// would leave the member two holds for one stay — both real, both counted, the ledger's own
// conservation still satisfied, and the plan drawn down twice. So the authorization adopts
// this row instead, and this is the whole of what adopting means.
//
// No movement is posted, because nothing moves: the quantity is already reserved and stays
// reserved. Only the deadline changes, only forwards, and only for a hold that is still
// open — so a redelivered approval that runs this a second time changes nothing, and a
// caller cannot use it to expire somebody's hold early.
func (l *Ledger) AdoptReservation(ctx context.Context, tx pgx.Tx, in AdoptInput) (Reservation, error) {
	current, err := reservationByID(ctx, tx, in.TenantID, in.ReservationID, true)
	if err != nil {
		return Reservation{}, err
	}
	if !current.Open() {
		return Reservation{}, ErrReservationClosed
	}
	if in.ExpiresAt.IsZero() {
		return current, nil
	}
	expiresAt := in.ExpiresAt.UTC()
	if _, err := sqlcgen.New(tx).ExtendEntitlementReservationExpiry(ctx,
		sqlcgen.ExtendEntitlementReservationExpiryParams{
			TenantID: in.TenantID, ID: in.ReservationID, ExpiresAt: &expiresAt,
		}); err != nil {
		return Reservation{}, fmt.Errorf("benefit: extend reservation expiry: %w", err)
	}
	// The row is re-read rather than patched in memory: the statement above may have
	// changed nothing (a hold that already outlives the new date), and returning a
	// deadline the database does not hold would be a lie the caller could act on.
	return reservationByID(ctx, tx, in.TenantID, in.ReservationID, true)
}

// settle is the shared body of Release and Consume: both move quantity out of reserved,
// one into available and the other into consumed, and both update the hold's counters.
// finalStatus overrides the computed status (the expiry job passes EXPIRED).
func (l *Ledger) settle(ctx context.Context, tx pgx.Tx, in MovementInput, movement, finalStatus string) (Reservation, error) {
	if !in.Quantity.IsPositive() || in.Key == "" {
		return Reservation{}, ErrQuantityInvalid
	}
	// The hold is read once without a lock to learn its account; the account lock taken
	// next is what actually serialises the movement, and the hold is re-read under it.
	head, err := reservationByID(ctx, tx, in.TenantID, in.ReservationID, false)
	if err != nil {
		return Reservation{}, err
	}
	account, err := l.lockAccount(ctx, tx, in.TenantID, head.AccountID)
	if err != nil {
		return Reservation{}, err
	}
	if movement == MovementConsume {
		if err := spendable(account); err != nil {
			return Reservation{}, err
		}
	} else if account.Status == AccountClosed {
		return Reservation{}, ErrAccountClosed
	}

	if replay, err := l.replayed(ctx, tx, in.TenantID, head.AccountID, in.Key); err != nil {
		return Reservation{}, err
	} else if replay != nil {
		current, rErr := reservationByID(ctx, tx, in.TenantID, in.ReservationID, false)
		if rErr != nil {
			return Reservation{}, rErr
		}
		return current, &ReplayError{Reservation: current, Entry: *replay}
	}

	reservation, err := reservationByID(ctx, tx, in.TenantID, in.ReservationID, true)
	if err != nil {
		return Reservation{}, err
	}
	if !reservation.Open() {
		return reservation, ErrReservationClosed
	}
	if in.Quantity.Cmp(reservation.Remaining()) > 0 {
		return reservation, ErrQuantityRemainder
	}

	deltas := Balances{Available: in.Quantity, Reserved: in.Quantity.Neg()}
	next := reservation
	next.Released = reservation.Released.Add(in.Quantity)
	if movement == MovementConsume {
		deltas = Balances{Reserved: in.Quantity.Neg(), Consumed: in.Quantity}
		next.Released = reservation.Released
		next.Consumed = reservation.Consumed.Add(in.Quantity)
	}
	next.Status = reservationStatus(next)
	if finalStatus != "" {
		next.Status = finalStatus
	}

	if _, err := l.post(ctx, tx, postInput{
		TenantID: in.TenantID, AccountID: head.AccountID, MovementType: movement, Deltas: deltas,
		ReferenceType: reservation.ReferenceType, ReferenceID: reservation.ReferenceID, Key: in.Key,
		ReasonCode: in.ReasonCode, ReasonText: in.ReasonText,
		ReservationID: &reservation.ID, ActorID: in.ActorID,
	}); err != nil {
		return Reservation{}, err
	}
	if err := updateReservation(ctx, tx, in.TenantID, next); err != nil {
		return Reservation{}, err
	}
	return next, nil
}

// Reverse writes the mirror image of an earlier movement. History is never edited: the
// correction is a new REVERSE row that points at the row it undoes. A movement can be
// reversed once, and only movements whose meaning survives an undo qualify — a RESERVE
// or RELEASE is undone through Release and Consume so the hold's counters stay true.
func (l *Ledger) Reverse(ctx context.Context, tx pgx.Tx, in ReverseInput) (Entry, error) {
	if in.Key == "" {
		return Entry{}, ErrQuantityInvalid
	}
	q := sqlcgen.New(tx)
	row, err := q.GetEntitlementLedgerEntry(ctx, sqlcgen.GetEntitlementLedgerEntryParams{
		TenantID: in.TenantID, ID: in.LedgerEntry,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, ErrLedgerEntryNotFound
	}
	if err != nil {
		return Entry{}, fmt.Errorf("benefit: read ledger entry: %w", err)
	}
	original, err := entryFromRow(entryRow{
		ID: row.ID, AccountID: row.EntitlementAccountID, MovementType: row.MovementType,
		EffectiveAt: row.EffectiveAt, DeltaTotal: row.DeltaTotal, DeltaAvailable: row.DeltaAvailable,
		DeltaReserved: row.DeltaReserved, DeltaConsumed: row.DeltaConsumed, DeltaExpired: row.DeltaExpired,
		ReferenceType: row.ReferenceType, ReferenceID: row.ReferenceID, Key: row.IdempotencyKey,
		ReasonCode: row.ReasonCode, ReasonText: row.ReasonText, ReservationID: row.ReservationID,
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt,
	})
	if err != nil {
		return Entry{}, err
	}
	switch original.MovementType {
	case MovementGrant, MovementConsume, MovementAdjust, MovementExpire:
	default:
		return Entry{}, ErrEntryNotReversible
	}

	account, err := l.lockAccount(ctx, tx, in.TenantID, original.AccountID)
	if err != nil {
		return Entry{}, err
	}
	if account.Status == AccountClosed {
		return Entry{}, ErrAccountClosed
	}
	if replay, err := l.replayed(ctx, tx, in.TenantID, original.AccountID, in.Key); err != nil {
		return Entry{}, err
	} else if replay != nil {
		return *replay, &ReplayError{Entry: *replay}
	}
	reversals, err := q.CountEntitlementLedgerReversals(ctx, sqlcgen.CountEntitlementLedgerReversalsParams{
		TenantID: in.TenantID, ReferenceID: original.ID,
	})
	if err != nil {
		return Entry{}, fmt.Errorf("benefit: count reversals: %w", err)
	}
	if reversals > 0 {
		return Entry{}, ErrAlreadyReversed
	}

	entry, err := l.post(ctx, tx, postInput{
		TenantID: in.TenantID, AccountID: original.AccountID, MovementType: MovementReverse,
		Deltas: Balances{
			Total: original.Deltas.Total.Neg(), Available: original.Deltas.Available.Neg(),
			Reserved: original.Deltas.Reserved.Neg(), Consumed: original.Deltas.Consumed.Neg(),
			Expired: original.Deltas.Expired.Neg(),
		},
		ReferenceType: ReferenceLedgerEntry, ReferenceID: original.ID, Key: in.Key,
		ReasonCode: in.ReasonCode, ReasonText: in.ReasonText,
		ReservationID: original.ReservationID, ActorID: in.ActorID,
	})
	if err != nil {
		return Entry{}, err
	}

	// Reversing a consume gives the quantity back to the hold it was taken from, so the
	// reservation's own counters keep matching the ledger.
	if original.MovementType == MovementConsume && original.ReservationID != nil {
		reservation, err := reservationByID(ctx, tx, in.TenantID, *original.ReservationID, true)
		if err != nil {
			return Entry{}, err
		}
		reservation.Consumed = reservation.Consumed.Sub(original.Deltas.Consumed)
		if reservation.Consumed.IsNegative() {
			return Entry{}, fmt.Errorf("benefit: reversing %s would make the reservation negative", original.ID)
		}
		reservation.Status = reservationStatus(reservation)
		if err := updateReservation(ctx, tx, in.TenantID, reservation); err != nil {
			return Entry{}, err
		}
	}
	return entry, nil
}

// Adjust applies a manual correction to the granted and available balance. It is the
// only movement allowed on a FROZEN account: freezing stops spending, and an adjustment
// is how an operator repairs what the reconciliation job found.
func (l *Ledger) Adjust(ctx context.Context, tx pgx.Tx, in AdjustInput) (Entry, error) {
	if in.Delta.IsZero() || in.Key == "" {
		return Entry{}, ErrQuantityInvalid
	}
	account, err := l.lockAccount(ctx, tx, in.TenantID, in.AccountID)
	if err != nil {
		return Entry{}, err
	}
	if account.Status == AccountClosed {
		return Entry{}, ErrAccountClosed
	}
	if replay, err := l.replayed(ctx, tx, in.TenantID, in.AccountID, in.Key); err != nil {
		return Entry{}, err
	} else if replay != nil {
		return *replay, &ReplayError{Entry: *replay}
	}
	if in.Delta.IsNegative() {
		if account.Balances.Total.Add(in.Delta).IsNegative() {
			return Entry{}, ErrInsufficient
		}
		if !account.Definition.AllowOverdraft && account.Balances.Available.Add(in.Delta).IsNegative() {
			return Entry{}, ErrInsufficient
		}
	}
	referenceType, referenceID := in.ReferenceType, in.ReferenceID
	if referenceType == "" {
		referenceType = ReferenceManual
	}
	return l.post(ctx, tx, postInput{
		TenantID: in.TenantID, AccountID: in.AccountID, MovementType: MovementAdjust,
		Deltas:        Balances{Total: in.Delta, Available: in.Delta},
		ReferenceType: referenceType, ReferenceID: referenceID, Key: in.Key,
		ReasonCode: in.ReasonCode, ReasonText: in.ReasonText, ActorID: in.ActorID,
	})
}

// Expire releases the holds of one tenant whose expires_at has passed, at most batch at
// a time (ExpireBatchSize by default). Expiry is a RELEASE with reason code EXPIRED —
// the ledger CHECK requires a reservation on RELEASE rows — and the hold ends as EXPIRED.
// The movement key is derived from the reservation, so re-running the job is a no-op.
func (l *Ledger) Expire(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, now time.Time, batch int) (int, error) {
	if batch <= 0 || batch > ExpireBatchSize {
		batch = ExpireBatchSize
	}
	rows, err := sqlcgen.New(tx).ListExpiredEntitlementReservations(ctx, sqlcgen.ListExpiredEntitlementReservationsParams{
		TenantID: tenantID, ExpiredBefore: &now, PageSize: int32(batch),
	})
	if err != nil {
		return 0, fmt.Errorf("benefit: list expired reservations: %w", err)
	}
	released := 0
	for _, row := range rows {
		reservation, err := reservationByID(ctx, tx, tenantID, row.ID, false)
		if err != nil {
			return released, err
		}
		remainder := reservation.Remaining()
		if !reservation.Open() || !remainder.IsPositive() {
			continue
		}
		_, err = l.settle(ctx, tx, MovementInput{
			TenantID: tenantID, ReservationID: reservation.ID, Quantity: remainder,
			Key: "expire:" + reservation.ID.String(), ReasonCode: ReasonExpired,
		}, MovementRelease, ReservationExpired)
		switch {
		case errors.Is(err, ErrIdempotentReplay), errors.Is(err, ErrReservationClosed):
			continue
		case err != nil:
			return released, err
		}
		released++
	}
	return released, nil
}

// postInput is one ledger row plus the account update that must accompany it.
type postInput struct {
	TenantID      uuid.UUID
	AccountID     uuid.UUID
	MovementType  string
	Deltas        Balances
	ReferenceType string
	ReferenceID   uuid.UUID
	Key           string
	ReasonCode    string
	ReasonText    string
	ReservationID *uuid.UUID
	ActorID       uuid.UUID
}

// post appends the ledger row and applies the identical deltas to the account. The two
// statements are inseparable: ck_entitlement_ledger_conservation checks the row on its
// own and ck_entitlement_account_balance checks the account, so a mismatch between them
// can only end as a rolled-back transaction.
func (l *Ledger) post(ctx context.Context, tx pgx.Tx, in postInput) (Entry, error) {
	effectiveAt := l.now()
	q := sqlcgen.New(tx)
	row, err := q.InsertEntitlementLedgerEntry(ctx, sqlcgen.InsertEntitlementLedgerEntryParams{
		TenantID: in.TenantID, AccountID: in.AccountID, MovementType: in.MovementType,
		EffectiveAt:    effectiveAt,
		DeltaTotal:     in.Deltas.Total.String(),
		DeltaAvailable: in.Deltas.Available.String(),
		DeltaReserved:  in.Deltas.Reserved.String(),
		DeltaConsumed:  in.Deltas.Consumed.String(),
		DeltaExpired:   in.Deltas.Expired.String(),
		ReferenceType:  in.ReferenceType, ReferenceID: in.ReferenceID, IdempotencyKey: in.Key,
		ReasonCode: optString(in.ReasonCode), ReasonText: optString(in.ReasonText),
		ReservationID: optUUID(in.ReservationID), CreatedBy: nullUUID(in.ActorID),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == constraintLedgerIdempotency {
			return Entry{}, ErrIdempotencyKeyReuse
		}
		return Entry{}, fmt.Errorf("benefit: append ledger entry: %w", err)
	}

	affected, err := q.ApplyEntitlementAccountDeltas(ctx, sqlcgen.ApplyEntitlementAccountDeltasParams{
		TenantID: in.TenantID, ID: in.AccountID,
		DeltaTotal:     in.Deltas.Total.String(),
		DeltaAvailable: in.Deltas.Available.String(),
		DeltaReserved:  in.Deltas.Reserved.String(),
		DeltaConsumed:  in.Deltas.Consumed.String(),
		DeltaExpired:   in.Deltas.Expired.String(),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == checkViolation &&
			(pgErr.ConstraintName == constraintAccountBalance || pgErr.ConstraintName == constraintAccountNonNegative) {
			return Entry{}, ErrInsufficient
		}
		return Entry{}, fmt.Errorf("benefit: apply account deltas: %w", err)
	}
	if affected != 1 {
		return Entry{}, ErrAccountNotFound
	}

	return Entry{
		ID: row.ID, AccountID: in.AccountID, MovementType: in.MovementType,
		EffectiveAt: row.EffectiveAt, Deltas: in.Deltas,
		ReferenceType: in.ReferenceType, ReferenceID: in.ReferenceID, Key: in.Key,
		ReasonCode: optString(in.ReasonCode), ReasonText: optString(in.ReasonText),
		ReservationID: in.ReservationID, CreatedBy: uuidPtr(nullUUID(in.ActorID)),
		CreatedAt: row.CreatedAt,
	}, nil
}

// replayed returns the ledger entry a previous call under the same key wrote, or nil.
func (l *Ledger) replayed(ctx context.Context, tx pgx.Tx, tenantID, accountID uuid.UUID, key string) (*Entry, error) {
	row, err := sqlcgen.New(tx).GetEntitlementLedgerEntryByKey(ctx, sqlcgen.GetEntitlementLedgerEntryByKeyParams{
		TenantID: tenantID, EntitlementAccountID: accountID, IdempotencyKey: key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("benefit: read ledger entry by key: %w", err)
	}
	entry, err := entryFromRow(entryRow{
		ID: row.ID, AccountID: row.EntitlementAccountID, MovementType: row.MovementType,
		EffectiveAt: row.EffectiveAt, DeltaTotal: row.DeltaTotal, DeltaAvailable: row.DeltaAvailable,
		DeltaReserved: row.DeltaReserved, DeltaConsumed: row.DeltaConsumed, DeltaExpired: row.DeltaExpired,
		ReferenceType: row.ReferenceType, ReferenceID: row.ReferenceID, Key: row.IdempotencyKey,
		ReasonCode: row.ReasonCode, ReasonText: row.ReasonText, ReservationID: row.ReservationID,
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt,
	})
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

// lockAccount is the serialisation point of every movement.
func (l *Ledger) lockAccount(ctx context.Context, tx pgx.Tx, tenantID, accountID uuid.UUID) (Account, error) {
	row, err := sqlcgen.New(tx).LockEntitlementAccount(ctx, sqlcgen.LockEntitlementAccountParams{
		TenantID: tenantID, ID: accountID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("benefit: lock entitlement account: %w", err)
	}
	balances, err := parseBalances(row.TotalGranted, row.AvailableQuantity, row.ReservedQuantity,
		row.ConsumedQuantity, row.ExpiredQuantity)
	if err != nil {
		return Account{}, err
	}
	return Account{
		ID: row.ID, EnrollmentID: row.EnrollmentID, PeriodFrom: dateValue(row.PeriodFrom),
		PeriodTo: datePtr(row.PeriodTo), Balances: balances, Status: row.Status,
		Definition: DefinitionSummary{
			ID: row.DefinitionID, Code: row.DefinitionCode, Name: row.DefinitionName,
			UnitType: row.UnitType, CurrencyCode: deref(row.CurrencyCode),
			FamilyShared: row.FamilyShared, AllowOverdraft: row.AllowOverdraft,
		},
		CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// spendable refuses the movements that take value out of an account.
func spendable(a Account) error {
	switch a.Status {
	case AccountFrozen:
		return ErrAccountFrozen
	case AccountClosed:
		return ErrAccountClosed
	default:
		return nil
	}
}

// reservationStatus derives the hold's status from its counters, matching
// ck_reservation_terminal: an exhausted hold is CONSUMED when anything was spent and
// RELEASED when nothing was.
func reservationStatus(r Reservation) string {
	settled := r.Consumed.Add(r.Released)
	switch {
	case settled.Cmp(r.Quantity) < 0 && r.Consumed.IsZero():
		return ReservationHeld
	case settled.Cmp(r.Quantity) < 0:
		return ReservationPartiallyConsumed
	case r.Consumed.IsPositive():
		return ReservationConsumed
	default:
		return ReservationReleased
	}
}

func updateReservation(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, r Reservation) error {
	affected, err := sqlcgen.New(tx).UpdateEntitlementReservationProgress(ctx, sqlcgen.UpdateEntitlementReservationProgressParams{
		TenantID: tenantID, ID: r.ID,
		ConsumedQuantity: r.Consumed.String(), ReleasedQuantity: r.Released.String(), Status: r.Status,
	})
	if err != nil {
		return fmt.Errorf("benefit: update reservation: %w", err)
	}
	if affected != 1 {
		return ErrReservationNotFound
	}
	return nil
}

func reservationWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		switch pgErr.ConstraintName {
		case constraintReservationKey, constraintReservationRefer:
			return ErrIdempotencyKeyReuse
		}
	}
	return fmt.Errorf("benefit: create reservation: %w", err)
}
