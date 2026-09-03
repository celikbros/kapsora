package ledger_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/outbox"
	"github.com/celikbros/kapsora/internal/platform/scheduler"
)

// asOf is the service date every fixture works on; it sits inside the published plan
// version's validity period and inside the 2026 calendar year.
var asOf = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// Definition codes seeded by the fixture.
const (
	defStandard  = "STANDARD"  // 100 units, no overdraft: the no-double-spend subject
	defOverdraft = "OVERDRAFT" // 100 units, overdraft allowed
	defShared    = "SHARED"    // 50 units, family_shared: opens on the principal
	defLifetime  = "LIFETIME"  // 5 units, LIFETIME period: unbounded benefit_period
)

type fixture struct {
	h    *dbtest.Harness
	pool *pgxpool.Pool
	svc  *ledger.Service
	lg   *ledger.Ledger

	tenant              uuid.UUID
	requester, approver uuid.UUID
	principalPerson     uuid.UUID
	dependantPerson     uuid.UUID
	principalEnrollment uuid.UUID
	dependantEnrollment uuid.UUID
	planID              uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := dbtest.New(t)

	// The concurrency tests need one connection per goroutine in flight; the harness
	// pool is deliberately small, so this one is opened against the same app role.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.NewPool(ctx, h.AppURL, db.PoolOptions{ApplicationName: "ledger-test", MaxConns: 24})
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	t.Cleanup(pool.Close)

	cursors, err := httpx.NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := ledger.New(ledger.Deps{Pool: pool, Audit: auditpg.New(), Cursors: cursors})
	if err != nil {
		t.Fatal(err)
	}

	f := &fixture{h: h, pool: pool, svc: svc, lg: svc.Ledger()}
	f.seed(t)
	return f
}

// seed writes the whole chain an entitlement account needs: a tenant with a sponsor, a
// principal member and a dependant whose membership points at the principal, an active
// plan with one published version, four entitlement definitions and two enrollments.
func (f *fixture) seed(t *testing.T) {
	t.Helper()
	h := f.h
	ctx, cancel := h.Ctx()
	defer cancel()

	f.tenant = h.CreateTenant("LEDGER")
	f.requester = h.CreateActor("ledger-requester", "Ledger Requester")
	f.approver = h.CreateActor("ledger-approver", "Ledger Approver")
	sponsor := h.CreateTenantOrganization(f.tenant, "Ledger Sponsor", "SPONSOR")
	payer := h.CreateTenantOrganization(f.tenant, "Ledger Payer", "PAYER")

	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}

	h.AdminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name) VALUES ($1, 'MEMBER', 'Üye')`, f.tenant)
	scan(&f.principalPerson, "principal person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Asli', 'Yildiz', 'asli yildiz') RETURNING id`, f.tenant)
	scan(&f.dependantPerson, "dependant person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Emir', 'Yildiz', 'emir yildiz') RETURNING id`, f.tenant)

	var principalMembership, dependantMembership uuid.UUID
	scan(&principalMembership, "principal membership", `
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                      membership_type, status, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, f.principalPerson, sponsor)
	scan(&dependantMembership, "dependant membership", `
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                      principal_membership_id, membership_type, status, valid_period)
		VALUES ($1, $2, $3, $4, 'MEMBER', 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, f.dependantPerson, sponsor, principalMembership)

	h.AdminExec(`INSERT INTO benefit.program_type (tenant_id, code, display_name) VALUES ($1, 'BENEFIT', 'Fayda')`, f.tenant)
	var programID uuid.UUID
	scan(&programID, "program", `
		INSERT INTO benefit.program (tenant_id, sponsor_tenant_organization_id, payer_tenant_organization_id,
		                             code, name, program_type, status, valid_period)
		VALUES ($1, $2, $3, 'PRG', 'Program', 'BENEFIT', 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, sponsor, payer)
	scan(&f.planID, "plan", `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name, status)
		VALUES ($1, $2, 'PLAN', 'Plan', 'ACTIVE') RETURNING id`, f.tenant, programID)

	var versionID uuid.UUID
	scan(&versionID, "plan version", `
		INSERT INTO benefit.plan_version (tenant_id, plan_id, version_no, status, valid_period,
		                                  published_at, published_by)
		VALUES ($1, $2, 1, 'PUBLISHED', daterange('2026-01-01','2027-01-01','[)'), clock_timestamp(), $3)
		RETURNING id`, f.tenant, f.planID, f.approver)

	definitions := []struct {
		code       string
		quantity   string
		periodType string
		overdraft  bool
		shared     bool
	}{
		{defStandard, "100", "CALENDAR_YEAR", false, false},
		{defOverdraft, "100", "CALENDAR_YEAR", true, false},
		{defShared, "50", "CALENDAR_YEAR", false, true},
		{defLifetime, "5", "LIFETIME", false, false},
	}
	for _, d := range definitions {
		h.AdminExec(`
			INSERT INTO benefit.entitlement_definition (tenant_id, plan_version_id, code, name, unit_type,
			                                            period_type, initial_quantity, allow_overdraft, family_shared)
			VALUES ($1, $2, $3, $3, 'COUNT', $4, $5::text::numeric, $6, $7)`,
			f.tenant, versionID, d.code, d.periodType, d.quantity, d.overdraft, d.shared)
	}

	scan(&f.principalEnrollment, "principal enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, principalMembership, f.planID)
	scan(&f.dependantEnrollment, "dependant enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, dependantMembership, f.planID)
}

// ensureAccounts opens the accounts of an enrollment and fails the test on error.
func (f *fixture) ensureAccounts(t *testing.T, enrollmentID uuid.UUID) ledger.EnsureResult {
	t.Helper()
	result, err := f.svc.EnsureAccounts(context.Background(), f.tenant, enrollmentID, asOf)
	if err != nil {
		t.Fatalf("ensure accounts: %v", err)
	}
	return result
}

// account returns the id of the account of one enrollment and definition code.
func (f *fixture) account(t *testing.T, enrollmentID uuid.UUID, code string) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var id uuid.UUID
	err := f.h.Admin.QueryRow(ctx, `
		SELECT a.id FROM benefit.entitlement_account a
		  JOIN benefit.entitlement_definition d ON d.id = a.entitlement_definition_id
		 WHERE a.tenant_id = $1 AND a.enrollment_id = $2 AND d.code = $3`,
		f.tenant, enrollmentID, code).Scan(&id)
	if err != nil {
		t.Fatalf("account %s of %s: %v", code, enrollmentID, err)
	}
	return id
}

// balances reads the materialised account columns as canonical decimal text.
func (f *fixture) balances(t *testing.T, accountID uuid.UUID) ledger.Balances {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var total, available, reserved, consumed, expired string
	err := f.h.Admin.QueryRow(ctx, `
		SELECT total_granted::text, available_quantity::text, reserved_quantity::text,
		       consumed_quantity::text, expired_quantity::text
		  FROM benefit.entitlement_account WHERE id = $1`, accountID).
		Scan(&total, &available, &reserved, &consumed, &expired)
	if err != nil {
		t.Fatalf("read balances: %v", err)
	}
	return ledger.Balances{
		Total: domain.MustQuantity(total), Available: domain.MustQuantity(available),
		Reserved: domain.MustQuantity(reserved), Consumed: domain.MustQuantity(consumed),
		Expired: domain.MustQuantity(expired),
	}
}

// ledgerSum adds up every movement of the account: it must equal the account columns.
func (f *fixture) ledgerSum(t *testing.T, accountID uuid.UUID) ledger.Balances {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var total, available, reserved, consumed, expired string
	err := f.h.Admin.QueryRow(ctx, `
		SELECT coalesce(sum(delta_total), 0)::text, coalesce(sum(delta_available), 0)::text,
		       coalesce(sum(delta_reserved), 0)::text, coalesce(sum(delta_consumed), 0)::text,
		       coalesce(sum(delta_expired), 0)::text
		  FROM benefit.entitlement_ledger WHERE entitlement_account_id = $1`, accountID).
		Scan(&total, &available, &reserved, &consumed, &expired)
	if err != nil {
		t.Fatalf("read ledger sum: %v", err)
	}
	return ledger.Balances{
		Total: domain.MustQuantity(total), Available: domain.MustQuantity(available),
		Reserved: domain.MustQuantity(reserved), Consumed: domain.MustQuantity(consumed),
		Expired: domain.MustQuantity(expired),
	}
}

// assertLedgerMatchesAccount is the invariant every test ends with.
func (f *fixture) assertLedgerMatchesAccount(t *testing.T, accountID uuid.UUID) {
	t.Helper()
	account, sum := f.balances(t, accountID), f.ledgerSum(t, accountID)
	if !account.Equal(sum) {
		t.Fatalf("account %v does not equal ledger sum %v", account, sum)
	}
}

// tx runs fn in a tenant transaction on the test pool.
func (f *fixture) tx(fn func(ctx context.Context, tx pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return db.WithTenantTx(ctx, f.pool, db.TenantContext{TenantID: f.tenant, ActorID: f.requester}, fn)
}

func (f *fixture) rc(actor uuid.UUID) identity.RequestContext {
	perms := map[string]struct{}{"entitlement.read": {}, "entitlement.adjust": {}}
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: actor}, StepUpValid: true, Permissions: perms,
	}
}

func qty(t *testing.T, s string) domain.Quantity {
	t.Helper()
	q, err := domain.ParseQuantity(s)
	if err != nil {
		t.Fatalf("quantity %q: %v", s, err)
	}
	return q
}

func assertQuantity(t *testing.T, got domain.Quantity, want, what string) {
	t.Helper()
	if got.String() != want {
		t.Fatalf("%s = %s, want %s", what, got.String(), want)
	}
}

// reserveConcurrently runs n goroutines, each reserving quantity with its own connection
// and its own transaction, and returns how many succeeded and how many were refused for
// lack of balance. sameKey makes every goroutine use one idempotency key.
func (f *fixture) reserveConcurrently(t *testing.T, accountID uuid.UUID, n int, quantity string, sameKey bool) (ok, insufficient, replays int) {
	t.Helper()
	amount := qty(t, quantity)
	results := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("reserve-%d", i)
			reference := uuid.New()
			if sameKey {
				key = "reserve-shared"
				reference = uuid.MustParse("00000000-0000-4000-8000-00000000beef")
			}
			<-start
			results[i] = f.tx(func(ctx context.Context, tx pgx.Tx) error {
				_, err := f.lg.Reserve(ctx, tx, ledger.ReserveInput{
					TenantID: f.tenant, AccountID: accountID, Quantity: amount,
					ReferenceType: ledger.ReferenceServiceRequest, ReferenceID: reference,
					Key: key, ActorID: f.requester,
				})
				return err
			})
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ledger.ErrInsufficient):
			insufficient++
		case errors.Is(err, ledger.ErrIdempotentReplay):
			replays++
		default:
			t.Fatalf("goroutine %d: unexpected error: %v", i, err)
		}
	}
	return ok, insufficient, replays
}

// TestReserveUnderConcurrencyNeverDoubleSpends is the acceptance criterion of the work
// package: 100 requests race for 100 units in units of two, each on its own connection.
// The account row lock is the only thing standing between them, and it must let exactly
// half of them through.
func TestReserveUnderConcurrencyNeverDoubleSpends(t *testing.T) {
	f := newFixture(t)
	f.ensureAccounts(t, f.principalEnrollment)
	accountID := f.account(t, f.principalEnrollment, defStandard)

	ok, insufficient, replays := f.reserveConcurrently(t, accountID, 100, "2", false)
	if ok != 50 || insufficient != 50 || replays != 0 {
		t.Fatalf("reserves: %d succeeded, %d insufficient, %d replays; want 50/50/0", ok, insufficient, replays)
	}

	balances := f.balances(t, accountID)
	t.Logf("100 reserves of 2 on a 100-unit account: %d succeeded, %d insufficient, %d replays; available=%s reserved=%s",
		ok, insufficient, replays, balances.Available, balances.Reserved)
	assertQuantity(t, balances.Available, "0", "available")
	assertQuantity(t, balances.Reserved, "100", "reserved")
	assertQuantity(t, balances.Total, "100", "total granted")
	f.assertLedgerMatchesAccount(t, accountID)

	var reservations int
	ctx, cancel := f.h.Ctx()
	defer cancel()
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT count(*) FROM benefit.entitlement_reservation WHERE entitlement_account_id = $1`,
		accountID).Scan(&reservations); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if reservations != 50 {
		t.Fatalf("reservations = %d, want 50", reservations)
	}
}

// TestReserveUnderConcurrencyWithOverdraft shows the same race on a definition that
// allows an overdraft: nothing is refused and the available balance goes negative, while
// the conservation CHECK still holds.
func TestReserveUnderConcurrencyWithOverdraft(t *testing.T) {
	f := newFixture(t)
	f.ensureAccounts(t, f.principalEnrollment)
	accountID := f.account(t, f.principalEnrollment, defOverdraft)

	ok, insufficient, replays := f.reserveConcurrently(t, accountID, 100, "2", false)
	if ok != 100 || insufficient != 0 || replays != 0 {
		t.Fatalf("reserves: %d succeeded, %d insufficient, %d replays; want 100/0/0", ok, insufficient, replays)
	}
	balances := f.balances(t, accountID)
	t.Logf("100 overdraft reserves of 2: %d succeeded, %d insufficient; available=%s reserved=%s",
		ok, insufficient, balances.Available, balances.Reserved)
	assertQuantity(t, balances.Available, "-100", "available")
	assertQuantity(t, balances.Reserved, "200", "reserved")
	f.assertLedgerMatchesAccount(t, accountID)
}

// TestReserveUnderConcurrencyWithOneKey proves the idempotency rule under the same race:
// one hold is created and the other ninety-nine callers are told they are replaying it.
func TestReserveUnderConcurrencyWithOneKey(t *testing.T) {
	f := newFixture(t)
	f.ensureAccounts(t, f.principalEnrollment)
	accountID := f.account(t, f.principalEnrollment, defStandard)

	ok, insufficient, replays := f.reserveConcurrently(t, accountID, 100, "2", true)
	if ok != 1 || insufficient != 0 || replays != 99 {
		t.Fatalf("reserves: %d succeeded, %d insufficient, %d replays; want 1/0/99", ok, insufficient, replays)
	}
	balances := f.balances(t, accountID)
	t.Logf("100 reserves under one key: %d created, %d replays; available=%s reserved=%s",
		ok, replays, balances.Available, balances.Reserved)
	assertQuantity(t, balances.Available, "98", "available")
	assertQuantity(t, balances.Reserved, "2", "reserved")
	f.assertLedgerMatchesAccount(t, accountID)

	// The same key with a different quantity is a client bug, not a replay.
	err := f.tx(func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.lg.Reserve(ctx, tx, ledger.ReserveInput{
			TenantID: f.tenant, AccountID: accountID, Quantity: qty(t, "3"),
			ReferenceType: ledger.ReferenceServiceRequest, ReferenceID: uuid.New(),
			Key: "reserve-shared", ActorID: f.requester,
		})
		return err
	})
	if !errors.Is(err, ledger.ErrIdempotencyKeyReuse) {
		t.Fatalf("reuse with another quantity: %v, want ErrIdempotencyKeyReuse", err)
	}
}

// TestReleaseAndConsumePartials walks a hold through partial settlement and back to zero.
func TestReleaseAndConsumePartials(t *testing.T) {
	f := newFixture(t)
	f.ensureAccounts(t, f.principalEnrollment)
	accountID := f.account(t, f.principalEnrollment, defStandard)

	var hold ledger.Reservation
	if err := f.tx(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		hold, err = f.lg.Reserve(ctx, tx, ledger.ReserveInput{
			TenantID: f.tenant, AccountID: accountID, Quantity: qty(t, "10"),
			ReferenceType: ledger.ReferenceBooking, ReferenceID: uuid.New(),
			Key: "hold-1", ActorID: f.requester,
		})
		return err
	}); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	// Consume four of ten: the hold is partially consumed and still open.
	var after ledger.Reservation
	if err := f.tx(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		after, err = f.lg.Consume(ctx, tx, ledger.MovementInput{
			TenantID: f.tenant, ReservationID: hold.ID, Quantity: qty(t, "4"),
			Key: "consume-1", ActorID: f.requester,
		})
		return err
	}); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if after.Status != ledger.ReservationPartiallyConsumed {
		t.Fatalf("status after partial consume = %s", after.Status)
	}
	balances := f.balances(t, accountID)
	assertQuantity(t, balances.Available, "90", "available")
	assertQuantity(t, balances.Reserved, "6", "reserved")
	assertQuantity(t, balances.Consumed, "4", "consumed")

	// Release the remaining six: the hold closes as CONSUMED because something was spent.
	if err := f.tx(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		after, err = f.lg.Release(ctx, tx, ledger.MovementInput{
			TenantID: f.tenant, ReservationID: hold.ID, Quantity: qty(t, "6"),
			Key: "release-1", ReasonCode: "CANCELLED", ActorID: f.requester,
		})
		return err
	}); err != nil {
		t.Fatalf("release: %v", err)
	}
	if after.Status != ledger.ReservationConsumed {
		t.Fatalf("status after full settlement = %s", after.Status)
	}
	balances = f.balances(t, accountID)
	assertQuantity(t, balances.Available, "96", "available")
	assertQuantity(t, balances.Reserved, "0", "reserved")
	assertQuantity(t, balances.Consumed, "4", "consumed")
	f.assertLedgerMatchesAccount(t, accountID)

	// Nothing is left to settle, and a replayed key returns the hold unchanged.
	err := f.tx(func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.lg.Release(ctx, tx, ledger.MovementInput{
			TenantID: f.tenant, ReservationID: hold.ID, Quantity: qty(t, "1"),
			Key: "release-2", ActorID: f.requester,
		})
		return err
	})
	if !errors.Is(err, ledger.ErrReservationClosed) {
		t.Fatalf("release of a closed hold: %v, want ErrReservationClosed", err)
	}
	err = f.tx(func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.lg.Consume(ctx, tx, ledger.MovementInput{
			TenantID: f.tenant, ReservationID: hold.ID, Quantity: qty(t, "4"),
			Key: "consume-1", ActorID: f.requester,
		})
		return err
	})
	if !errors.Is(err, ledger.ErrIdempotentReplay) {
		t.Fatalf("replayed consume: %v, want ErrIdempotentReplay", err)
	}
}

// TestReverseOfAConsume corrects a movement without editing history: the ledger keeps
// both rows and the hold gets its quantity back.
func TestReverseOfAConsume(t *testing.T) {
	f := newFixture(t)
	f.ensureAccounts(t, f.principalEnrollment)
	accountID := f.account(t, f.principalEnrollment, defStandard)

	var hold ledger.Reservation
	var consumed ledger.Reservation
	var entry ledger.Entry
	if err := f.tx(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		hold, err = f.lg.Reserve(ctx, tx, ledger.ReserveInput{
			TenantID: f.tenant, AccountID: accountID, Quantity: qty(t, "8"),
			ReferenceType: ledger.ReferenceAuthorization, ReferenceID: uuid.New(),
			Key: "hold-r", ActorID: f.requester,
		})
		return err
	}); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := f.tx(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		consumed, err = f.lg.Consume(ctx, tx, ledger.MovementInput{
			TenantID: f.tenant, ReservationID: hold.ID, Quantity: qty(t, "3"),
			Key: "consume-r", ActorID: f.requester,
		})
		return err
	}); err != nil {
		t.Fatalf("consume: %v", err)
	}
	_ = consumed

	consumeEntry := f.entryByKey(t, accountID, "consume-r")
	if err := f.tx(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		entry, err = f.lg.Reverse(ctx, tx, ledger.ReverseInput{
			TenantID: f.tenant, LedgerEntry: consumeEntry, Key: "reverse-r",
			ReasonCode: "WRONG_SERVICE", ActorID: f.approver,
		})
		return err
	}); err != nil {
		t.Fatalf("reverse: %v", err)
	}
	if entry.MovementType != ledger.MovementReverse || entry.ReferenceID != consumeEntry {
		t.Fatalf("reversal points at %s (%s), want a REVERSE of %s", entry.ReferenceID, entry.MovementType, consumeEntry)
	}
	assertQuantity(t, entry.Deltas.Consumed, "-3", "reversal consumed delta")

	balances := f.balances(t, accountID)
	assertQuantity(t, balances.Consumed, "0", "consumed")
	assertQuantity(t, balances.Reserved, "8", "reserved")
	f.assertLedgerMatchesAccount(t, accountID)

	// The hold is whole again and reversing twice is refused.
	restored := f.reservation(t, hold.ID)
	assertQuantity(t, restored.Consumed, "0", "hold consumed")
	if restored.Status != ledger.ReservationHeld {
		t.Fatalf("hold status after reversal = %s", restored.Status)
	}
	err := f.tx(func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.lg.Reverse(ctx, tx, ledger.ReverseInput{
			TenantID: f.tenant, LedgerEntry: consumeEntry, Key: "reverse-r2", ActorID: f.approver,
		})
		return err
	})
	if !errors.Is(err, ledger.ErrAlreadyReversed) {
		t.Fatalf("second reversal: %v, want ErrAlreadyReversed", err)
	}

	// History is never edited: both rows are still there.
	if got := f.movementCount(t, accountID); got != 4 {
		t.Fatalf("ledger rows = %d, want 4 (grant, reserve, consume, reverse)", got)
	}
}

// TestExpiryJobReleasesStaleHolds runs the scheduler body over a hold whose expiry has
// passed. The movement is a RELEASE with reason EXPIRED, because the ledger CHECK
// demands a reservation on RELEASE rows, and the hold ends as EXPIRED.
func TestExpiryJobReleasesStaleHolds(t *testing.T) {
	f := newFixture(t)
	f.ensureAccounts(t, f.principalEnrollment)
	accountID := f.account(t, f.principalEnrollment, defStandard)

	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)
	var stale, fresh ledger.Reservation
	if err := f.tx(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		stale, err = f.lg.Reserve(ctx, tx, ledger.ReserveInput{
			TenantID: f.tenant, AccountID: accountID, Quantity: qty(t, "7"),
			ReferenceType: ledger.ReferenceBooking, ReferenceID: uuid.New(),
			Key: "stale", ExpiresAt: &past, ActorID: f.requester,
		})
		if err != nil {
			return err
		}
		fresh, err = f.lg.Reserve(ctx, tx, ledger.ReserveInput{
			TenantID: f.tenant, AccountID: accountID, Quantity: qty(t, "3"),
			ReferenceType: ledger.ReferenceBooking, ReferenceID: uuid.New(),
			Key: "fresh", ExpiresAt: &future, ActorID: f.requester,
		})
		return err
	}); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	released, err := f.svc.ExpireReservations(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if released != 1 {
		t.Fatalf("released = %d, want 1", released)
	}
	if got := f.reservation(t, stale.ID); got.Status != ledger.ReservationExpired {
		t.Fatalf("stale hold status = %s, want EXPIRED", got.Status)
	}
	if got := f.reservation(t, fresh.ID); got.Status != ledger.ReservationHeld {
		t.Fatalf("fresh hold status = %s, want HELD", got.Status)
	}
	balances := f.balances(t, accountID)
	assertQuantity(t, balances.Available, "97", "available")
	assertQuantity(t, balances.Reserved, "3", "reserved")
	f.assertLedgerMatchesAccount(t, accountID)

	// A second pass finds nothing left to do.
	released, err = f.svc.ExpireReservations(context.Background(), time.Now().UTC())
	if err != nil || released != 0 {
		t.Fatalf("second expiry pass released %d (%v), want 0", released, err)
	}
}

// TestReconciliationFreezesDriftedAccounts injects a balance that no ledger row explains
// and checks that the daily job notices, freezes the account and stops spending on it.
func TestReconciliationFreezesDriftedAccounts(t *testing.T) {
	f := newFixture(t)
	f.ensureAccounts(t, f.principalEnrollment)
	accountID := f.account(t, f.principalEnrollment, defStandard)

	// A clean tenant reconciles silently.
	metrics, err := f.svc.Reconcile(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if metrics.Drifts != 0 {
		t.Fatalf("drifts on a clean tenant = %d, want 0", metrics.Drifts)
	}

	// Someone hands out ten units behind the ledger's back. The account CHECK still
	// holds, so only the reconciliation can catch it.
	f.h.AdminExec(`UPDATE benefit.entitlement_account
	                  SET total_granted = total_granted + 10, available_quantity = available_quantity + 10
	                WHERE id = $1`, accountID)

	metrics, err = f.svc.Reconcile(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if metrics.Drifts != 1 {
		t.Fatalf("drifts = %d, want 1", metrics.Drifts)
	}

	account, err := f.svc.GetAccount(context.Background(), f.rc(f.requester), accountID)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if account.Status != ledger.AccountFrozen {
		t.Fatalf("account status = %s, want FROZEN", account.Status)
	}

	// A frozen account refuses to spend.
	err = f.tx(func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.lg.Reserve(ctx, tx, ledger.ReserveInput{
			TenantID: f.tenant, AccountID: accountID, Quantity: qty(t, "1"),
			ReferenceType: ledger.ReferenceManual, ReferenceID: uuid.New(),
			Key: "after-freeze", ActorID: f.requester,
		})
		return err
	})
	if !errors.Is(err, ledger.ErrAccountFrozen) {
		t.Fatalf("reserve on a frozen account: %v, want ErrAccountFrozen", err)
	}

	// The drift is audited as a system security event and announced on the outbox.
	if got := f.auditCount(t, "entitlement.drift", accountID); got != 1 {
		t.Fatalf("entitlement.drift audit rows = %d, want 1", got)
	}
	if got := f.outboxCount(t, ledger.DriftEvent, accountID); got != 1 {
		t.Fatalf("%s outbox rows = %d, want 1", ledger.DriftEvent, got)
	}
}

// TestFamilySharedAccountsOpenOnThePrincipal checks that a shared definition opens one
// account on the principal's enrollment and that the dependant reaches it.
func TestFamilySharedAccountsOpenOnThePrincipal(t *testing.T) {
	f := newFixture(t)
	f.ensureAccounts(t, f.principalEnrollment)
	dependant := f.ensureAccounts(t, f.dependantEnrollment)

	// The dependant reaches four accounts but only opened the three unshared ones: the
	// shared account already existed on the principal's enrollment.
	if len(dependant.Accounts) != 4 || dependant.Opened != 3 {
		t.Fatalf("dependant ensure: %d accounts, %d opened; want 4/3", len(dependant.Accounts), dependant.Opened)
	}
	sharedAccount := f.account(t, f.principalEnrollment, defShared)
	var dependantShared int
	ctx, cancel := f.h.Ctx()
	defer cancel()
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM benefit.entitlement_account a
		  JOIN benefit.entitlement_definition d ON d.id = a.entitlement_definition_id
		 WHERE a.enrollment_id = $1 AND d.code = $2`, f.dependantEnrollment, defShared).Scan(&dependantShared); err != nil {
		t.Fatalf("count dependant shared accounts: %v", err)
	}
	if dependantShared != 0 {
		t.Fatalf("dependant opened its own shared account (%d)", dependantShared)
	}

	accounts, err := f.svc.ListPersonEntitlements(context.Background(), f.rc(f.requester), f.dependantPerson, asOf)
	if err != nil {
		t.Fatalf("list dependant entitlements: %v", err)
	}
	var sawShared, ownAccounts int
	for _, a := range accounts {
		if a.ID == sharedAccount {
			sawShared++
			if !a.Shared || a.PersonID != f.principalPerson {
				t.Fatalf("shared account reported shared=%v person=%s", a.Shared, a.PersonID)
			}
			assertQuantity(t, a.Balances.Available, "50", "shared available")
			continue
		}
		if a.Shared {
			t.Fatalf("own account %s reported as shared", a.ID)
		}
		ownAccounts++
	}
	if sawShared != 1 || ownAccounts != 3 {
		t.Fatalf("dependant sees %d shared and %d own accounts; want 1/3", sawShared, ownAccounts)
	}

	// The lifetime definition has no upper bound; the calendar-year ones close in 2027.
	for _, a := range accounts {
		switch a.Definition.Code {
		case defLifetime:
			if a.PeriodTo != nil {
				t.Fatalf("lifetime account ends at %s", a.PeriodTo)
			}
			if got := a.PeriodFrom.Format(time.DateOnly); got != "2026-01-01" {
				t.Fatalf("lifetime account starts at %s, want the enrollment start", got)
			}
		default:
			if a.PeriodTo == nil || a.PeriodTo.Format(time.DateOnly) != "2027-01-01" {
				t.Fatalf("%s account ends at %v, want 2027-01-01", a.Definition.Code, a.PeriodTo)
			}
		}
	}
}

// TestAdjustmentMakerChecker: the requester cannot approve their own correction, and an
// approval writes the ADJUST movement and records the ledger row on the adjustment.
func TestAdjustmentMakerChecker(t *testing.T) {
	f := newFixture(t)
	f.ensureAccounts(t, f.principalEnrollment)
	accountID := f.account(t, f.principalEnrollment, defStandard)
	ctx := context.Background()

	adjustment, err := f.svc.CreateAdjustment(ctx, f.rc(f.requester), accountID, ledger.NewAdjustmentInput{
		Delta: qty(t, "-25.5"), ReasonCode: "DATA_ENTRY_ERROR",
	})
	if err != nil {
		t.Fatalf("create adjustment: %v", err)
	}
	if adjustment.Status != ledger.AdjustmentPending {
		t.Fatalf("status = %s, want PENDING", adjustment.Status)
	}
	// Nothing moved yet.
	assertQuantity(t, f.balances(t, accountID).Available, "100", "available before approval")

	if _, err := f.svc.ApproveAdjustment(ctx, f.rc(f.requester), adjustment.ID, nil, adjustment.RowVersion); !errors.Is(err, ledger.ErrMakerCheckerSame) {
		t.Fatalf("self-approval: %v, want ErrMakerCheckerSame", err)
	}
	if got := f.auditDeniedCount(t, "entitlement.adjustment.approve", adjustment.ID); got != 1 {
		t.Fatalf("denied audit rows = %d, want 1", got)
	}
	// The refusal changed nothing.
	if got, err := f.svc.ListAdjustments(ctx, f.rc(f.approver), ledger.AdjustmentFilter{}); err != nil {
		t.Fatalf("list adjustments: %v", err)
	} else if len(got.Items) != 1 || got.Items[0].Status != ledger.AdjustmentPending {
		t.Fatalf("pending list = %+v", got.Items)
	}

	approved, err := f.svc.ApproveAdjustment(ctx, f.rc(f.approver), adjustment.ID, nil, adjustment.RowVersion)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.Status != ledger.AdjustmentApproved || approved.LedgerEntryID == nil {
		t.Fatalf("approved adjustment = %+v", approved)
	}
	balances := f.balances(t, accountID)
	assertQuantity(t, balances.Total, "74.5", "total granted")
	assertQuantity(t, balances.Available, "74.5", "available")
	f.assertLedgerMatchesAccount(t, accountID)

	// The movement is on the ledger and points back at the adjustment.
	page, err := f.svc.ListLedger(ctx, f.rc(f.requester), accountID, "", 10)
	if err != nil {
		t.Fatalf("list ledger: %v", err)
	}
	if len(page.Items) != 2 || page.Items[0].MovementType != ledger.MovementAdjust {
		t.Fatalf("ledger page = %+v", page.Items)
	}
	if page.Items[0].ReferenceID != adjustment.ID || page.Items[0].ID != *approved.LedgerEntryID {
		t.Fatalf("adjust movement %s references %s", page.Items[0].ID, page.Items[0].ReferenceID)
	}

	// A second decision on a decided adjustment is refused.
	if _, err := f.svc.RejectAdjustment(ctx, f.rc(f.approver), adjustment.ID, "TOO_LATE", nil, approved.RowVersion); !errors.Is(err, ledger.ErrAdjustmentNotPending) {
		t.Fatalf("reject after approval: %v, want ErrAdjustmentNotPending", err)
	}
}

// TestAdjustmentRejectionMovesNothing keeps the second half of the maker-checker rule.
func TestAdjustmentRejectionMovesNothing(t *testing.T) {
	f := newFixture(t)
	f.ensureAccounts(t, f.principalEnrollment)
	accountID := f.account(t, f.principalEnrollment, defStandard)
	ctx := context.Background()

	adjustment, err := f.svc.CreateAdjustment(ctx, f.rc(f.requester), accountID, ledger.NewAdjustmentInput{
		Delta: qty(t, "10"), ReasonCode: "GOODWILL",
	})
	if err != nil {
		t.Fatalf("create adjustment: %v", err)
	}
	rejected, err := f.svc.RejectAdjustment(ctx, f.rc(f.approver), adjustment.ID, "NOT_ELIGIBLE", nil, adjustment.RowVersion)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejected.Status != ledger.AdjustmentRejected || rejected.LedgerEntryID != nil {
		t.Fatalf("rejected adjustment = %+v", rejected)
	}
	assertQuantity(t, f.balances(t, accountID).Total, "100", "total granted")
	if got := f.movementCount(t, accountID); got != 1 {
		t.Fatalf("ledger rows = %d, want 1 (the grant)", got)
	}
}

// TestEnrollmentCreatedConsumerOpensAccounts drives the outbox handler the worker
// registers, including its idempotency on redelivery.
func TestEnrollmentCreatedConsumerOpensAccounts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	payload, err := json.Marshal(map[string]any{
		"enrollmentId": f.principalEnrollment,
		"personId":     f.principalPerson,
		"planId":       f.planID,
		"validFrom":    asOf.Format(time.DateOnly),
	})
	if err != nil {
		t.Fatal(err)
	}
	delivery := outbox.Delivery{
		ID: uuid.New(), TenantID: uuid.NullUUID{UUID: f.tenant, Valid: true},
		AggregateType: "benefit.enrollment", AggregateID: f.principalEnrollment,
		Type: benefitapp.EnrollmentCreatedEvent, Payload: payload, OccurredAt: time.Now(), Attempt: 1,
	}

	if err := f.svc.HandleEnrollmentCreated(ctx, delivery); err != nil {
		t.Fatalf("handle event: %v", err)
	}
	accounts, err := f.svc.ListPersonEntitlements(ctx, f.rc(f.requester), f.principalPerson, asOf)
	if err != nil {
		t.Fatalf("list entitlements: %v", err)
	}
	if len(accounts) != 4 {
		t.Fatalf("accounts opened = %d, want 4", len(accounts))
	}
	for _, a := range accounts {
		if a.Balances.Total.IsZero() {
			t.Fatalf("account %s was opened without its grant", a.Definition.Code)
		}
		f.assertLedgerMatchesAccount(t, a.ID)
	}

	// Handlers must be idempotent: a redelivery opens nothing and grants nothing.
	if err := f.svc.HandleEnrollmentCreated(ctx, delivery); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	standard := f.account(t, f.principalEnrollment, defStandard)
	assertQuantity(t, f.balances(t, standard).Total, "100", "total granted after redelivery")
	if got := f.movementCount(t, standard); got != 1 {
		t.Fatalf("ledger rows after redelivery = %d, want 1", got)
	}

	// An enrollment nobody knows is a permanent failure, not an endless retry.
	unknown := delivery
	unknown.Payload, _ = json.Marshal(map[string]any{"enrollmentId": uuid.New()})
	err = f.svc.HandleEnrollmentCreated(ctx, unknown)
	if outbox.KindOf(err) != outbox.KindPermanent {
		t.Fatalf("unknown enrollment: kind %v, want permanent (%v)", outbox.KindOf(err), err)
	}
}

// TestAccountReadsAreTenantScoped keeps the negative authorization case: an account of
// another tenant is simply not found, and an unknown person is a 404 as well.
func TestAccountReadsAreTenantScoped(t *testing.T) {
	f := newFixture(t)
	f.ensureAccounts(t, f.principalEnrollment)
	accountID := f.account(t, f.principalEnrollment, defStandard)

	other := f.h.CreateTenant("LEDGER_OTHER")
	rc := f.rc(f.requester)
	rc.TenantID = other
	ctx := context.Background()

	if _, err := f.svc.GetAccount(ctx, rc, accountID); !errors.Is(err, ledger.ErrAccountNotFound) {
		t.Fatalf("cross-tenant read: %v, want ErrAccountNotFound", err)
	}
	if _, err := f.svc.ListLedger(ctx, rc, accountID, "", 10); !errors.Is(err, ledger.ErrAccountNotFound) {
		t.Fatalf("cross-tenant ledger read: %v, want ErrAccountNotFound", err)
	}
	if _, err := f.svc.ListPersonEntitlements(ctx, rc, f.principalPerson, asOf); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("cross-tenant person read: %v, want ErrNotFound", err)
	}
	if _, err := f.svc.CreateAdjustment(ctx, rc, accountID, ledger.NewAdjustmentInput{
		Delta: qty(t, "1"), ReasonCode: "TEST",
	}); !errors.Is(err, ledger.ErrAccountNotFound) {
		t.Fatalf("cross-tenant adjustment: %v, want ErrAccountNotFound", err)
	}
}

// TestLedgerPagingIsNewestFirst walks the keyset cursor over a handful of movements.
func TestLedgerPagingIsNewestFirst(t *testing.T) {
	f := newFixture(t)
	f.ensureAccounts(t, f.principalEnrollment)
	accountID := f.account(t, f.principalEnrollment, defStandard)

	for i := 0; i < 5; i++ {
		if err := f.tx(func(ctx context.Context, tx pgx.Tx) error {
			_, err := f.lg.Reserve(ctx, tx, ledger.ReserveInput{
				TenantID: f.tenant, AccountID: accountID, Quantity: qty(t, "1"),
				ReferenceType: ledger.ReferenceManual, ReferenceID: uuid.New(),
				Key: fmt.Sprintf("page-%d", i), ActorID: f.requester,
			})
			return err
		}); err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
	}

	ctx := context.Background()
	rc := f.rc(f.requester)
	var seen []uuid.UUID
	cursor := ""
	for page := 0; page < 5; page++ {
		got, err := f.svc.ListLedger(ctx, rc, accountID, cursor, 2)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		for _, e := range got.Items {
			seen = append(seen, e.ID)
		}
		cursor = got.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 6 {
		t.Fatalf("paged through %d entries, want 6 (grant + 5 reserves)", len(seen))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i-1].String() <= seen[i].String() {
			t.Fatalf("entries are not newest first: %s then %s", seen[i-1], seen[i])
		}
	}
	if _, err := f.svc.ListLedger(ctx, rc, accountID, "not-a-cursor", 2); !errors.Is(err, httpx.ErrInvalidCursor) {
		t.Fatalf("tampered cursor: %v, want ErrInvalidCursor", err)
	}
}

// TestSchedulerJobsRegister guards the two job definitions: the registry panics at
// process start on an invalid code or interval, which would take the scheduler down.
func TestSchedulerJobsRegister(t *testing.T) {
	f := newFixture(t)
	registry := scheduler.NewRegistry()
	registry.Register(scheduler.EntitlementReservationExpire(f.svc))
	registry.Register(scheduler.EntitlementReconcile(f.svc))

	jobs := registry.Jobs()
	if len(jobs) != 2 {
		t.Fatalf("registered %d jobs, want 2", len(jobs))
	}
	if jobs[0].Code != "entitlement.reservation.expire" || jobs[0].Every != time.Minute {
		t.Fatalf("expiry job = %s every %s", jobs[0].Code, jobs[0].Every)
	}
	if jobs[1].Code != "entitlement.reconcile" || jobs[1].Every != 24*time.Hour {
		t.Fatalf("reconcile job = %s every %s", jobs[1].Code, jobs[1].Every)
	}
	for _, job := range jobs {
		if _, err := job.Run(context.Background()); err != nil {
			t.Fatalf("job %s: %v", job.Code, err)
		}
	}
}

// entryByKey returns the id of the ledger row written under an idempotency key.
func (f *fixture) entryByKey(t *testing.T, accountID uuid.UUID, key string) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var id uuid.UUID
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT id FROM benefit.entitlement_ledger WHERE entitlement_account_id = $1 AND idempotency_key = $2`,
		accountID, key).Scan(&id); err != nil {
		t.Fatalf("ledger entry %q: %v", key, err)
	}
	return id
}

func (f *fixture) movementCount(t *testing.T, accountID uuid.UUID) int {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var n int
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT count(*) FROM benefit.entitlement_ledger WHERE entitlement_account_id = $1`, accountID).Scan(&n); err != nil {
		t.Fatalf("count movements: %v", err)
	}
	return n
}

func (f *fixture) reservation(t *testing.T, reservationID uuid.UUID) ledger.Reservation {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var status, quantity, consumed, released string
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT status, quantity::text, consumed_quantity::text, released_quantity::text
		  FROM benefit.entitlement_reservation WHERE id = $1`, reservationID).
		Scan(&status, &quantity, &consumed, &released); err != nil {
		t.Fatalf("read reservation: %v", err)
	}
	return ledger.Reservation{
		ID: reservationID, Status: status, Quantity: domain.MustQuantity(quantity),
		Consumed: domain.MustQuantity(consumed), Released: domain.MustQuantity(released),
	}
}

func (f *fixture) auditCount(t *testing.T, action string, resourceID uuid.UUID) int {
	t.Helper()
	return f.countAudit(t, action, resourceID, "SUCCESS", "FAILURE")
}

func (f *fixture) auditDeniedCount(t *testing.T, action string, resourceID uuid.UUID) int {
	t.Helper()
	return f.countAudit(t, action, resourceID, "DENIED", "DENIED")
}

func (f *fixture) countAudit(t *testing.T, action string, resourceID uuid.UUID, outcomes ...string) int {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var n int
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT count(*) FROM audit.event WHERE action_code = $1 AND resource_id = $2 AND outcome = ANY($3)`,
		action, resourceID, outcomes).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}

func (f *fixture) outboxCount(t *testing.T, eventType string, aggregateID uuid.UUID) int {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var n int
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT count(*) FROM system.outbox_event WHERE event_type = $1 AND aggregate_id = $2`,
		eventType, aggregateID).Scan(&n); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	return n
}
