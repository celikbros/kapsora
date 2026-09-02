package dbtests

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// TestTenantIsolation is the Sprint-1 acceptance check: tenant A data is invisible to
// tenant B through the application role, and writes tagged with another tenant fail.
func TestTenantIsolation(t *testing.T) {
	h := dbtest.New(t)

	tenantA := h.CreateTenant("TENANT_A")
	tenantB := h.CreateTenant("TENANT_B")
	h.CreateTenantOrganization(tenantA, "Alpha Hospital", "PROVIDER")
	h.CreateTenantOrganization(tenantA, "Alpha Sponsor", "SPONSOR")
	h.CreateTenantOrganization(tenantB, "Beta Hotel", "PROVIDER")

	countFor := func(tenant uuid.UUID) int {
		t.Helper()
		var n int
		err := h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM directory.tenant_organization`).Scan(&n)
		})
		if err != nil {
			t.Fatalf("count as tenant %s: %v", tenant, err)
		}
		return n
	}

	if got := countFor(tenantA); got != 2 {
		t.Fatalf("tenant A sees %d organizations, want 2", got)
	}
	if got := countFor(tenantB); got != 1 {
		t.Fatalf("tenant B sees %d organizations, want 1", got)
	}

	// Without a tenant context the application role sees nothing (fail closed).
	ctx, cancel := h.Ctx()
	defer cancel()
	var n int
	if err := h.App.QueryRow(ctx, `SELECT count(*) FROM directory.tenant_organization`).Scan(&n); err != nil {
		t.Fatalf("count without tenant context: %v", err)
	}
	if n != 0 {
		t.Fatalf("application role without tenant context sees %d rows, want 0", n)
	}

	// A write tagged with tenant B inside a tenant A transaction violates WITH CHECK.
	var orgID uuid.UUID
	if err := h.Admin.QueryRow(ctx, `SELECT organization_id FROM directory.tenant_organization LIMIT 1`).Scan(&orgID); err != nil {
		t.Fatalf("pick organization: %v", err)
	}
	err := h.AppTx(tenantA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO directory.tenant_organization (tenant_id, organization_id, relationship_role)
			 VALUES ($1, $2, 'VENDOR')`, tenantB, orgID)
		return err
	})
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateInsufficientPrivilege, "cross-tenant insert")

	// Updating another tenant's row silently affects zero rows; the row stays intact.
	var affected int64
	err = h.AppTx(tenantA, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE directory.tenant_organization SET status = 'SUSPENDED' WHERE tenant_id = $1`, tenantB)
		affected = tag.RowsAffected()
		return err
	})
	if err != nil {
		t.Fatalf("cross-tenant update: %v", err)
	}
	if affected != 0 {
		t.Fatalf("cross-tenant update affected %d rows, want 0", affected)
	}
}

// TestActorSeesOnlyOwnMembershipsBeforeTenantSelection covers migration 000009.
func TestActorSeesOnlyOwnMembershipsBeforeTenantSelection(t *testing.T) {
	h := dbtest.New(t)
	tenantA := h.CreateTenant("MEM_A")
	tenantB := h.CreateTenant("MEM_B")
	alice := h.CreateActor("alice", "Alice")
	bob := h.CreateActor("bob", "Bob")
	h.CreateMembership(tenantA, alice)
	h.CreateMembership(tenantB, alice)
	h.CreateMembership(tenantB, bob)

	countFor := func(actor uuid.UUID) int {
		t.Helper()
		var n int
		err := h.AppActorTx(actor, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM iam.tenant_membership`).Scan(&n)
		})
		if err != nil {
			t.Fatalf("count memberships for %s: %v", actor, err)
		}
		return n
	}
	if got := countFor(alice); got != 2 {
		t.Fatalf("alice sees %d memberships, want 2", got)
	}
	if got := countFor(bob); got != 1 {
		t.Fatalf("bob sees %d memberships, want 1", got)
	}

	// The actor-only context grants no write access: inserting a membership fails.
	err := h.AppActorTx(alice, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO iam.tenant_membership (tenant_id, actor_id) VALUES ($1, $2)`, tenantA, bob)
		return err
	})
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateInsufficientPrivilege, "actor-context insert")
}

// TestApplicationRoleCannotBypass verifies the role has neither BYPASSRLS nor ownership.
func TestApplicationRoleCannotBypass(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	var bypass, super bool
	if err := h.Admin.QueryRow(ctx, `SELECT rolbypassrls, rolsuper FROM pg_roles WHERE rolname = $1`, dbtest.AppRole).Scan(&bypass, &super); err != nil {
		t.Fatalf("read role: %v", err)
	}
	if bypass || super {
		t.Fatalf("application role must not be superuser (%t) or bypass RLS (%t)", super, bypass)
	}

	var owned int
	if err := h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner
		 WHERE r.rolname = $1 AND c.relkind IN ('r','p')`, dbtest.AppRole).Scan(&owned); err != nil {
		t.Fatalf("count owned tables: %v", err)
	}
	if owned != 0 {
		t.Fatalf("application role owns %d tables, want 0", owned)
	}
}

// TestAuditIsAppendOnlyForApplicationRole checks both the privilege boundary and the trigger.
func TestAuditIsAppendOnlyForApplicationRole(t *testing.T) {
	h := dbtest.New(t)
	tenant := h.CreateTenant("AUDIT_T")
	ctx, cancel := h.Ctx()
	defer cancel()

	// The application role may insert audit rows (no tenant RLS on audit tables).
	var eventID uuid.UUID
	err := h.App.QueryRow(ctx, `
		INSERT INTO audit.event (tenant_id, event_category, action_code, outcome)
		VALUES ($1, 'ADMIN', 'test.insert', 'SUCCESS') RETURNING id`, tenant).Scan(&eventID)
	if err != nil {
		t.Fatalf("app insert audit event: %v", err)
	}

	_, err = h.App.Exec(ctx, `UPDATE audit.event SET outcome = 'FAILURE' WHERE id = $1`, eventID)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateInsufficientPrivilege, "app update audit")

	_, err = h.App.Exec(ctx, `DELETE FROM audit.event WHERE id = $1`, eventID)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateInsufficientPrivilege, "app delete audit")

	// Even the owner is stopped by the append-only trigger.
	err = h.AdminExecErr(`UPDATE audit.event SET outcome = 'FAILURE' WHERE id = $1`, eventID)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "owner update audit")
}
