// Package dbtests runs the schema against a real PostgreSQL 18 (v1.2 section 44.1)
// through the shared dbtest harness.
package dbtests

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/platform/dbmigrate"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

func TestInvitationMigration56To57PreservesAcceptedExisting(t *testing.T) {
	h := dbtest.NewAtVersion(t, 56)
	ctx := context.Background()
	tenantID := h.CreateTenant("INV_UPGRADE")
	var actorID, memberID, invitationID uuid.UUID
	if err := h.Admin.QueryRow(ctx, `INSERT INTO iam.actor(identity_issuer,identity_subject,actor_type,display_name,status)
	 VALUES('kapsora','migration-existing','HUMAN','Existing','ACTIVE') RETURNING id`).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if err := h.Admin.QueryRow(ctx, `INSERT INTO iam.tenant_membership(tenant_id,actor_id,created_by)
	 VALUES($1,$2,$2) RETURNING id`, tenantID, actorID).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	proof := make([]byte, 32)
	if err := h.Admin.QueryRow(ctx, `INSERT INTO iam.tenant_invitation(tenant_id,masked_recipient,proof_digest,
	 expires_at,status,accepted_actor_id,accepted_membership_id,accept_key,terminal_at)
	 VALUES($1,'e***@***',$2,clock_timestamp()+interval '1 day','ACCEPTED',$3,$4,'existing-key-0001',clock_timestamp()) RETURNING id`,
		tenantID, proof, actorID, memberID).Scan(&invitationID); err != nil {
		t.Fatal(err)
	}
	st, err := dbmigrate.UpTo(h.AdminURL, 57)
	if err != nil || st.Version != 57 || st.Dirty {
		t.Fatalf("upgrade: %+v %v", st, err)
	}
	var mode string
	var fingerprint []byte
	var preservedActor, preservedMember uuid.UUID
	if err := h.Admin.QueryRow(ctx, `SELECT accepted_mode,accept_new_fingerprint,accepted_actor_id,accepted_membership_id
	 FROM iam.tenant_invitation WHERE id=$1`, invitationID).Scan(&mode, &fingerprint, &preservedActor, &preservedMember); err != nil {
		t.Fatal(err)
	}
	if mode != "EXISTING" || fingerprint != nil || preservedActor != actorID || preservedMember != memberID {
		t.Fatal("B1 accepted outcome changed by B2 migration")
	}
	if _, err := h.Admin.Exec(ctx, `UPDATE iam.tenant_invitation SET accepted_mode=NULL WHERE id=$1`, invitationID); err == nil {
		t.Fatal("accepted invitation allowed a NULL acceptance mode")
	}
}

// expectedSchemaVersion is the number of the newest migration file.
const expectedSchemaVersion = 58

func TestMigrateUpFromEmptyDatabase(t *testing.T) {
	h := dbtest.New(t)

	if h.Migration.Dirty {
		t.Fatalf("schema is dirty after migrate up")
	}
	if h.Migration.Version != expectedSchemaVersion {
		t.Fatalf("schema version = %d, want %d", h.Migration.Version, expectedSchemaVersion)
	}

	// Re-running must be a no-op.
	again, err := dbmigrate.Up(h.AdminURL)
	if err != nil {
		t.Fatalf("second migrate up: %v", err)
	}
	if again.Version != expectedSchemaVersion || again.Dirty {
		t.Fatalf("second run: version=%d dirty=%t", again.Version, again.Dirty)
	}
}

func TestEveryTenantTableHasRLSAndTenantColumn(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	// Every table with a tenant_id column in a business schema must have RLS forced,
	// except the deliberate exceptions documented in the migrations.
	exceptions := map[string]bool{
		"system.outbox_event": true, // worker processes all tenants
		"audit.event":         true, // platform-written, permission-filtered reads
		"audit.access_event":  true,
	}

	rows, err := h.Admin.Query(ctx, `
		SELECT n.nspname || '.' || c.relname AS tbl, c.relrowsecurity, c.relforcerowsecurity
		  FROM pg_attribute a
		  JOIN pg_class c ON c.oid = a.attrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE a.attname = 'tenant_id'
		   AND NOT a.attisdropped
		   AND c.relkind IN ('r','p')
		   AND n.nspname NOT IN ('pg_catalog','information_schema','public')
		   AND c.relispartition = false`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer rows.Close()

	checked := 0
	for rows.Next() {
		var tbl string
		var enabled, forced bool
		if err := rows.Scan(&tbl, &enabled, &forced); err != nil {
			t.Fatalf("scan: %v", err)
		}
		checked++
		if exceptions[tbl] {
			continue
		}
		if !enabled || !forced {
			t.Errorf("%s has tenant_id but RLS enabled=%t forced=%t", tbl, enabled, forced)
		}
	}
	if checked < 30 {
		t.Fatalf("expected at least 30 tenant tables, found %d", checked)
	}
}

func TestPermissionCatalogSeeded(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	var n int
	if err := h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.permission`).Scan(&n); err != nil {
		t.Fatalf("count permissions: %v", err)
	}
	if n < 80 {
		t.Fatalf("permission catalog has %d rows, want at least 80", n)
	}
	for _, code := range []string{"security.break_glass", "fiscal.response.send", "accounting.posting.send"} {
		var sensitivity string
		if err := h.Admin.QueryRow(ctx, `SELECT sensitivity FROM iam.permission WHERE code = $1`, code).Scan(&sensitivity); err != nil {
			t.Fatalf("permission %s missing: %v", code, err)
		}
		if sensitivity != "PRIVILEGED" {
			t.Errorf("permission %s sensitivity = %s, want PRIVILEGED", code, sensitivity)
		}
	}
}
