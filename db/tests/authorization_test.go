package dbtests

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// authorizationSeed is one tenant with everything migration 000026 hangs off: an approved
// request with one line, an entitlement account with a hold on it, and an authorization
// carrying that hold.
type authorizationSeed struct {
	request       requestSeed
	account       uuid.UUID
	reservation   uuid.UUID
	authorization uuid.UUID
	item          uuid.UUID
}

func seedAuthorization(h *dbtest.Harness, code string) authorizationSeed {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()

	s := authorizationSeed{request: seedServiceRequest(h, code)}
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		h.T.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			h.T.Fatalf("seed %s: %v", what, err)
		}
	}

	// The plan version the request's plan already has carries no entitlement definition,
	// so one is added here together with the account and the hold the authorization owns.
	var planID, planVersion, definitionID uuid.UUID
	scan(&planID, "plan id", `
		SELECT plan_id FROM benefit.enrollment WHERE tenant_id = $1 AND id = $2`,
		s.request.tenant, s.request.enrollment)
	scan(&planVersion, "plan version", `
		INSERT INTO benefit.plan_version (tenant_id, plan_id, version_no, status, valid_period,
		                                  published_at, published_by)
		VALUES ($1, $2, 1, 'PUBLISHED', daterange('2026-01-01','2027-01-01','[)'), clock_timestamp(), $3)
		RETURNING id`, s.request.tenant, planID, s.request.actor)
	scan(&definitionID, "entitlement definition", `
		INSERT INTO benefit.entitlement_definition (tenant_id, plan_version_id, code, name, unit_type,
		                                            period_type, initial_quantity)
		VALUES ($1, $2, 'PHYSIO', 'PHYSIO', 'SESSION', 'CALENDAR_YEAR', 10) RETURNING id`,
		s.request.tenant, planVersion)
	scan(&s.account, "entitlement account", `
		INSERT INTO benefit.entitlement_account (tenant_id, enrollment_id, entitlement_definition_id,
		                                         benefit_period, total_granted, available_quantity,
		                                         reserved_quantity)
		VALUES ($1, $2, $3, daterange('2026-01-01','2027-01-01','[)'), 10, 8, 2) RETURNING id`,
		s.request.tenant, s.request.enrollment, definitionID)
	scan(&s.reservation, "entitlement reservation", `
		INSERT INTO benefit.entitlement_reservation (tenant_id, entitlement_account_id, reference_type,
		                                             reference_id, quantity, status, idempotency_key)
		VALUES ($1, $2, 'AUTHORIZATION', $3, 2, 'HELD', $4) RETURNING id`,
		s.request.tenant, s.account, s.request.item, "seed:"+code)

	scan(&s.authorization, "authorization", `
		INSERT INTO service.authorization (tenant_id, request_id, authorization_reference, valid_to,
		                                   reserved_total, idempotency_key)
		VALUES ($1, $2, 'AUT-20260615-AAAAAAAA', now() + interval '1 day', 2, 'seed-key')
		RETURNING id`, s.request.tenant, s.request.request)
	scan(&s.item, "authorization item", `
		INSERT INTO service.authorization_item (tenant_id, authorization_id, request_item_id,
		                                        service_definition_id, approved_quantity,
		                                        entitlement_reservation_id)
		VALUES ($1, $2, $3, $4, 2, $5) RETURNING id`,
		s.request.tenant, s.authorization, s.request.item, s.request.definition, s.reservation)
	return s
}

// TestAuthorizationCannotConsumeMoreThanItReserved is the constraint the whole package
// rests on: a promise cannot deliver more than it held.
func TestAuthorizationCannotConsumeMoreThanItReserved(t *testing.T) {
	h := dbtest.New(t)
	s := seedAuthorization(h, "AUTH_CONSUMED")

	err := h.AdminExecErr(`UPDATE service.authorization SET consumed_total = 3
	                        WHERE tenant_id = $1 AND id = $2`, s.request.tenant, s.authorization)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "consumed above reserved")

	if err := h.AdminExecErr(`UPDATE service.authorization SET consumed_total = 2
	                           WHERE tenant_id = $1 AND id = $2`, s.request.tenant, s.authorization); err != nil {
		t.Fatalf("consuming exactly what was reserved must be allowed: %v", err)
	}

	err = h.AdminExecErr(`UPDATE service.authorization_item SET consumed_quantity = 3
	                       WHERE tenant_id = $1 AND id = $2`, s.request.tenant, s.item)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "line consumed above approved")
}

// TestAuthorizationIdempotencyKeyIsUniquePerTenant is what makes createAuthorization take
// one set of reservations however many times it is replayed.
func TestAuthorizationIdempotencyKeyIsUniquePerTenant(t *testing.T) {
	h := dbtest.New(t)
	s := seedAuthorization(h, "AUTH_KEY")

	err := h.AdminExecErr(`
		INSERT INTO service.authorization (tenant_id, request_id, authorization_reference, valid_to,
		                                   reserved_total, idempotency_key)
		VALUES ($1, $2, 'AUT-20260615-BBBBBBBB', now() + interval '1 day', 1, 'seed-key')`,
		s.request.tenant, s.request.request)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "the same key twice")

	// Another tenant may hold the same key: the uniqueness is per tenant, not global.
	other := seedServiceRequest(h, "AUTH_KEY_B")
	if err := h.AdminExecErr(`
		INSERT INTO service.authorization (tenant_id, request_id, authorization_reference, valid_to,
		                                   reserved_total, idempotency_key)
		VALUES ($1, $2, 'AUT-20260615-CCCCCCCC', now() + interval '1 day', 1, 'seed-key')`,
		other.tenant, other.request); err != nil {
		t.Fatalf("another tenant's key collided: %v", err)
	}
}

// TestAuthorizationStatusAndWindowInvariants covers the CHECKs that make a status readable
// on its own: a cancelled promise says why, and a window always moves forwards.
func TestAuthorizationStatusAndWindowInvariants(t *testing.T) {
	h := dbtest.New(t)
	s := seedAuthorization(h, "AUTH_STATUS")

	err := h.AdminExecErr(`UPDATE service.authorization SET status = 'CANCELLED'
	                        WHERE tenant_id = $1 AND id = $2`, s.request.tenant, s.authorization)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "cancelled without a reason")

	if err := h.AdminExecErr(`
		UPDATE service.authorization SET status = 'CANCELLED', cancel_reason_code = 'MEMBER_WITHDREW'
		 WHERE tenant_id = $1 AND id = $2`, s.request.tenant, s.authorization); err != nil {
		t.Fatalf("cancelling with a reason must be allowed: %v", err)
	}

	err = h.AdminExecErr(`
		INSERT INTO service.authorization (tenant_id, request_id, authorization_reference, valid_from,
		                                   valid_to, reserved_total, idempotency_key)
		VALUES ($1, $2, 'AUT-20260615-DDDDDDDD', now(), now() - interval '1 hour', 1, 'backwards')`,
		s.request.tenant, s.request.request)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a window that ends before it starts")

	err = h.AdminExecErr(`UPDATE service.authorization SET status = 'SOMETHING_ELSE'
	                        WHERE tenant_id = $1 AND id = $2`, s.request.tenant, s.authorization)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a status outside the closed list")
}

// TestVoucherStoresOnlyADigest: the column is a 32-byte digest and nothing else fits.
func TestVoucherStoresOnlyADigest(t *testing.T) {
	h := dbtest.New(t)
	s := seedAuthorization(h, "AUTH_VOUCHER")

	err := h.AdminExecErr(`
		INSERT INTO service.voucher (tenant_id, authorization_id, token_hash, token_masked, valid_to)
		VALUES ($1, $2, '\x00'::bytea, '****abcd', now() + interval '1 day')`,
		s.request.tenant, s.authorization)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a token hash that is not 32 bytes")

	if err := h.AdminExecErr(`
		INSERT INTO service.voucher (tenant_id, authorization_id, token_hash, token_masked, valid_to)
		VALUES ($1, $2, sha256('token'::bytea), '****oken', now() + interval '1 day')`,
		s.request.tenant, s.authorization); err != nil {
		t.Fatalf("a 32-byte digest must be accepted: %v", err)
	}
	err = h.AdminExecErr(`
		INSERT INTO service.voucher (tenant_id, authorization_id, token_hash, token_masked, valid_to)
		VALUES ($1, $2, sha256('token'::bytea), '****oken', now() + interval '1 day')`,
		s.request.tenant, s.authorization)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "the same digest twice")

	err = h.AdminExecErr(`
		UPDATE service.voucher SET status = 'REDEEMED'
		 WHERE tenant_id = $1 AND authorization_id = $2`, s.request.tenant, s.authorization)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "redeemed without a redemption time")
}

// TestAuthorizationCompositeKeysStayInsideTheTenant: every foreign key of migration 000026
// carries the tenant, so a row can never point at another tenant's request or line.
func TestAuthorizationCompositeKeysStayInsideTheTenant(t *testing.T) {
	h := dbtest.New(t)
	a := seedAuthorization(h, "AUTH_FK_A")
	b := seedAuthorization(h, "AUTH_FK_B")

	err := h.AdminExecErr(`
		INSERT INTO service.authorization (tenant_id, request_id, authorization_reference, valid_to,
		                                   reserved_total, idempotency_key)
		VALUES ($1, $2, 'AUT-20260615-EEEEEEEE', now() + interval '1 day', 1, 'cross-tenant')`,
		a.request.tenant, b.request.request)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateForeignKeyViolation, "an authorization on another tenant's request")

	err = h.AdminExecErr(`
		INSERT INTO service.authorization_item (tenant_id, authorization_id, request_item_id,
		                                        service_definition_id, approved_quantity)
		VALUES ($1, $2, $3, $4, 1)`,
		a.request.tenant, a.authorization, b.request.item, a.request.definition)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateForeignKeyViolation, "a line from another tenant's request")

	err = h.AdminExecErr(`
		INSERT INTO service.fulfilment (tenant_id, fulfilment_reference, authorization_id, performed_at)
		VALUES ($1, 'FUL-20260615-AAAAAAAA', $2, now())`,
		a.request.tenant, b.authorization)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateForeignKeyViolation, "a fulfilment on another tenant's authorization")
}

// TestAuthorizationTenantIsolation: every table of this package is invisible across
// tenants through the application role.
func TestAuthorizationTenantIsolation(t *testing.T) {
	h := dbtest.New(t)
	a := seedAuthorization(h, "AUTH_RLS_A")
	b := seedAuthorization(h, "AUTH_RLS_B")
	for _, s := range []authorizationSeed{a, b} {
		h.AdminExec(`
			INSERT INTO service.fulfilment (tenant_id, fulfilment_reference, authorization_id, performed_at)
			VALUES ($1, $3, $2, now())`,
			s.request.tenant, s.authorization, "FUL-"+s.authorization.String()[:12])
		h.AdminExec(`
			INSERT INTO service.voucher (tenant_id, authorization_id, token_hash, token_masked, valid_to)
			VALUES ($1, $2, sha256($3::bytea), '****aaaa', now() + interval '1 day')`,
			s.request.tenant, s.authorization, []byte(s.authorization.String()))
	}

	visible := func(tenant uuid.UUID, table string) int {
		t.Helper()
		var n int
		err := h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n)
		})
		if err != nil {
			t.Fatalf("count %s for %s: %v", table, tenant, err)
		}
		return n
	}
	for _, table := range []string{
		"service.authorization", "service.authorization_item",
		"service.fulfilment", "service.voucher",
	} {
		if got := visible(a.request.tenant, table); got != 1 {
			t.Fatalf("%s: tenant A sees %d rows, want only its own one", table, got)
		}
		if got := visible(b.request.tenant, table); got != 1 {
			t.Fatalf("%s: tenant B sees %d rows, want only its own one", table, got)
		}
	}

	// A write tagged with another tenant fails the WITH CHECK of the policy.
	err := h.AppTx(a.request.tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `
			INSERT INTO service.authorization (tenant_id, request_id, authorization_reference, valid_to,
			                                   reserved_total, idempotency_key)
			VALUES ($1, $2, 'AUT-20260615-FFFFFFFF', now() + interval '1 day', 1, 'wrong-tenant')`,
			b.request.tenant, b.request.request)
		return execErr
	})
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateInsufficientPrivilege, "writing another tenant's authorization")
}

// TestAuthorizationPermissionsAreSeededAndGrantable checks both halves of the same fact.
// The permission catalogue and the role templates are two separate places, and a
// permission seeded into the first but missing from the second is a permission nobody can
// ever hold — which has already happened once, with pricing.quote.
func TestAuthorizationPermissionsAreSeededAndGrantable(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	granted := map[string][]string{}
	for _, tpl := range identityapp.RoleTemplates() {
		for _, code := range tpl.Permissions {
			granted[code] = append(granted[code], tpl.Code)
		}
	}
	for _, code := range []string{"authorization.manage", "fulfilment.record", "voucher.redeem"} {
		var n int
		if err := h.Admin.QueryRow(ctx,
			`SELECT count(*) FROM iam.permission WHERE code = $1`, code).Scan(&n); err != nil {
			t.Fatalf("read permission %s: %v", code, err)
		}
		if n != 1 {
			t.Fatalf("permission %s is seeded %d times, want once", code, n)
		}
		if len(granted[code]) == 0 {
			t.Fatalf("permission %s is in the catalogue but in no role template: nobody can hold it", code)
		}
	}
	for _, want := range []struct{ role, permission string }{
		{"PROGRAM_MANAGER", "authorization.manage"},
		{"MEDICAL_REVIEWER", "authorization.manage"},
		{"PROVIDER_STAFF", "fulfilment.record"},
		{"PROGRAM_MANAGER", "fulfilment.record"},
		{"PROVIDER_STAFF", "voucher.redeem"},
		{"PROGRAM_MANAGER", "voucher.redeem"},
	} {
		found := false
		for _, role := range granted[want.permission] {
			if role == want.role {
				found = true
			}
		}
		if !found {
			t.Fatalf("role %s does not hold %s", want.role, want.permission)
		}
	}
}
