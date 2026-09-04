// Package dbtests runs the schema against a real PostgreSQL 18 (v1.2 section 44.1)
// through the shared dbtest harness.
package dbtests

import (
	"testing"

	"github.com/celikbros/kapsora/internal/platform/dbmigrate"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// expectedSchemaVersion is the number of the newest migration file.
const expectedSchemaVersion = 25

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
