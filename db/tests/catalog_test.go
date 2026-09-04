package dbtests

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// catalogSeed is one tenant with a category, a definition and a code system edition,
// which is all the catalog tables of migrations 000006 and 000019 need.
type catalogSeed struct {
	tenant     uuid.UUID
	category   uuid.UUID
	definition uuid.UUID
	system     uuid.UUID
}

func seedCatalog(h *dbtest.Harness, code string) catalogSeed {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()

	s := catalogSeed{tenant: h.CreateTenant(code)}
	must := func(err error, what string) {
		h.T.Helper()
		if err != nil {
			h.T.Fatalf("seed %s: %v", what, err)
		}
	}
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Sağlık', 'HEALTH') RETURNING id`, s.tenant).Scan(&s.category), "category")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name, fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'PHYSIO_SESSION', 'Fizyoterapi seansı', 'SESSION', 'SESSION') RETURNING id`,
		s.tenant, s.category).Scan(&s.definition), "definition")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.code_system (tenant_id, code, name, version, authority, valid_from)
		VALUES ($1, 'SUT', 'Sağlık Uygulama Tebliği', '2026', 'SGK', '2026-01-01') RETURNING id`,
		s.tenant).Scan(&s.system), "code system")
	return s
}

// addCategory inserts a child category and returns its id.
func addCategory(h *dbtest.Harness, tenant uuid.UUID, parent *uuid.UUID, code string) uuid.UUID {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	var id uuid.UUID
	err := h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_category (tenant_id, parent_id, code, name, domain_code)
		VALUES ($1, $2, $3, $3, 'HEALTH') RETURNING id`, tenant, parent, code).Scan(&id)
	if err != nil {
		h.T.Fatalf("insert category %s: %v", code, err)
	}
	return id
}

func TestServiceCategoryRefusesSelfParent(t *testing.T) {
	h := dbtest.New(t)
	s := seedCatalog(h, "CAT_SELF")

	// The direct cycle is refused by the schema itself (ck_service_category_not_self_parent).
	err := h.AdminExecErr(`UPDATE catalog.service_category SET parent_id = id WHERE id = $1`, s.category)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "category as its own parent")
}

// TestServiceCategoryAncestorChain exercises the recursive query the catalog service uses
// to refuse a cycle and to measure depth. A cycle three levels up is visible in the chain
// of the proposed parent, and the chain is what the depth cap is counted from.
func TestServiceCategoryAncestorChain(t *testing.T) {
	h := dbtest.New(t)
	s := seedCatalog(h, "CAT_TREE")

	l2 := addCategory(h, s.tenant, &s.category, "L2")
	l3 := addCategory(h, s.tenant, &l2, "L3")
	l4 := addCategory(h, s.tenant, &l3, "L4")

	ancestors := func(tenant, id uuid.UUID) []uuid.UUID {
		t.Helper()
		var out []uuid.UUID
		err := h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `
				WITH RECURSIVE up AS (
				    SELECT c.id, c.parent_id, 1 AS depth
				      FROM catalog.service_category c
				     WHERE c.tenant_id = $1 AND c.id = $2
				    UNION ALL
				    SELECT p.id, p.parent_id, up.depth + 1
				      FROM catalog.service_category p
				      JOIN up ON p.id = up.parent_id
				     WHERE p.tenant_id = $1 AND up.depth < 64
				)
				SELECT id FROM up ORDER BY depth`, tenant, id)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var got uuid.UUID
				if err := rows.Scan(&got); err != nil {
					return err
				}
				out = append(out, got)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatalf("ancestors: %v", err)
		}
		return out
	}

	chain := ancestors(s.tenant, l4)
	want := []uuid.UUID{l4, l3, l2, s.category}
	if len(chain) != len(want) {
		t.Fatalf("chain has %d levels, want %d", len(chain), len(want))
	}
	for i := range want {
		if chain[i] != want[i] {
			t.Fatalf("chain[%d] = %s, want %s", i, chain[i], want[i])
		}
	}

	// Re-parenting the root under L4 would close a loop three levels deep, and the root
	// is on L4's chain, which is exactly what the service checks before it writes.
	found := false
	for _, id := range chain {
		if id == s.category {
			found = true
		}
	}
	if !found {
		t.Fatal("the root must appear on the chain of its own descendant")
	}

	// RLS keeps another tenant from seeing the tree at all.
	other := h.CreateTenant("CAT_TREE_B")
	if got := ancestors(other, l4); len(got) != 0 {
		t.Fatalf("another tenant sees %d ancestor rows, want 0", len(got))
	}
}

func TestServiceCatalogCodesAreUniquePerTenant(t *testing.T) {
	h := dbtest.New(t)
	s := seedCatalog(h, "CAT_CODE")

	err := h.AdminExecErr(`
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Kopya', 'HEALTH')`, s.tenant)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "duplicate category code")

	err = h.AdminExecErr(`
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name, fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'PHYSIO_SESSION', 'Kopya', 'SESSION', 'SESSION')`, s.tenant, s.category)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "duplicate definition code")

	// The same codes are free in another tenant: the uniqueness is tenant scoped.
	other := seedCatalog(h, "CAT_CODE_B")
	if other.definition == uuid.Nil {
		t.Fatal("the second tenant must be able to reuse the codes")
	}
}

// TestServiceDefinitionRowVersionIsOwnedByDatabase covers the ETag of both catalog
// resources: the touch trigger moves row_version on every update, including one that
// changes nothing, and a stale token matches no row.
func TestServiceDefinitionRowVersionIsOwnedByDatabase(t *testing.T) {
	h := dbtest.New(t)
	s := seedCatalog(h, "CAT_VER")
	ctx, cancel := h.Ctx()
	defer cancel()

	var before, after int64
	if err := h.Admin.QueryRow(ctx,
		`SELECT row_version FROM catalog.service_definition WHERE id = $1`, s.definition).Scan(&before); err != nil {
		t.Fatalf("read row_version: %v", err)
	}
	h.AdminExec(`UPDATE catalog.service_definition SET name = name WHERE id = $1`, s.definition)
	if err := h.Admin.QueryRow(ctx,
		`SELECT row_version FROM catalog.service_definition WHERE id = $1`, s.definition).Scan(&after); err != nil {
		t.Fatalf("read row_version: %v", err)
	}
	if after != before+1 {
		t.Fatalf("row_version %d -> %d, want +1", before, after)
	}

	var stale int64
	err := h.AppTx(s.tenant, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE catalog.service_definition SET active = false WHERE id = $1 AND row_version = $2`, s.definition, before)
		stale = tag.RowsAffected()
		return err
	})
	if err != nil {
		t.Fatalf("stale update: %v", err)
	}
	if stale != 0 {
		t.Fatalf("stale update affected %d rows, want 0", stale)
	}

	// The category got the same pair in 000019; a no-op update still moves it.
	var categoryBefore, categoryAfter int64
	if err := h.Admin.QueryRow(ctx,
		`SELECT row_version FROM catalog.service_category WHERE id = $1`, s.category).Scan(&categoryBefore); err != nil {
		t.Fatalf("read category row_version: %v", err)
	}
	h.AdminExec(`UPDATE catalog.service_category SET name = name WHERE id = $1`, s.category)
	if err := h.Admin.QueryRow(ctx,
		`SELECT row_version FROM catalog.service_category WHERE id = $1`, s.category).Scan(&categoryAfter); err != nil {
		t.Fatalf("read category row_version: %v", err)
	}
	if categoryAfter != categoryBefore+1 {
		t.Fatalf("category row_version %d -> %d, want +1", categoryBefore, categoryAfter)
	}
}

func TestServiceCodeMappingOverlapRejected(t *testing.T) {
	h := dbtest.New(t)
	s := seedCatalog(h, "MAP_OV")

	h.AdminExec(`
		INSERT INTO catalog.service_code_mapping (tenant_id, service_definition_id, code_system_id, code, valid_from, valid_to)
		VALUES ($1, $2, $3, 'P701010', '2026-01-01', NULL)`, s.tenant, s.definition, s.system)

	// The same code over an overlapping period would make "which code applied on this
	// date" ambiguous.
	err := h.AdminExecErr(`
		INSERT INTO catalog.service_code_mapping (tenant_id, service_definition_id, code_system_id, code, valid_from)
		VALUES ($1, $2, $3, 'P701010', '2026-06-01')`, s.tenant, s.definition, s.system)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateExclusionViolation, "overlapping code mapping")

	// A different code in the same system over the same period is allowed, as long as
	// neither is primary.
	h.AdminExec(`
		INSERT INTO catalog.service_code_mapping (tenant_id, service_definition_id, code_system_id, code, valid_from)
		VALUES ($1, $2, $3, 'P701011', '2026-06-01')`, s.tenant, s.definition, s.system)
}

func TestServiceCodeMappingPrimaryUniqueness(t *testing.T) {
	h := dbtest.New(t)
	s := seedCatalog(h, "MAP_PRI")

	h.AdminExec(`
		INSERT INTO catalog.service_code_mapping (tenant_id, service_definition_id, code_system_id, code, valid_from, is_primary)
		VALUES ($1, $2, $3, 'P701010', '2026-01-01', true)`, s.tenant, s.definition, s.system)

	err := h.AdminExecErr(`
		INSERT INTO catalog.service_code_mapping (tenant_id, service_definition_id, code_system_id, code, valid_from, is_primary)
		VALUES ($1, $2, $3, 'P701011', '2026-06-01', true)`, s.tenant, s.definition, s.system)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateExclusionViolation, "second primary mapping over an overlapping period")

	// A primary mapping that ends may be followed by another one: the period is part of
	// the exclusion key.
	h.AdminExec(`UPDATE catalog.service_code_mapping SET valid_to = '2026-06-01' WHERE tenant_id = $1`, s.tenant)
	h.AdminExec(`
		INSERT INTO catalog.service_code_mapping (tenant_id, service_definition_id, code_system_id, code, valid_from, is_primary)
		VALUES ($1, $2, $3, 'P701011', '2026-06-01', true)`, s.tenant, s.definition, s.system)
}

// TestCodeValueAsOfReturnsHistoricalValue is the reader guarantee of section 2.4: a claim
// from last year resolves against the codes that were valid then, not today's.
func TestCodeValueAsOfReturnsHistoricalValue(t *testing.T) {
	h := dbtest.New(t)
	s := seedCatalog(h, "CV_ASOF")

	h.AdminExec(`
		INSERT INTO catalog.code_value (tenant_id, code_system_id, code, display, valid_from, valid_to)
		VALUES ($1, $2, 'P701010', 'Eski karşılık', '2025-01-01', '2026-01-01')`, s.tenant, s.system)
	h.AdminExec(`
		INSERT INTO catalog.code_value (tenant_id, code_system_id, code, display, valid_from)
		VALUES ($1, $2, 'P701010', 'Yeni karşılık', '2026-01-01')`, s.tenant, s.system)

	asOf := func(tenant uuid.UUID, date string) []string {
		t.Helper()
		var out []string
		err := h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `
				SELECT display FROM catalog.code_value
				 WHERE tenant_id = $1 AND code_system_id = $2
				   AND valid_from <= $3::date AND (valid_to IS NULL OR valid_to > $3::date)`,
				tenant, s.system, date)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var display string
				if err := rows.Scan(&display); err != nil {
					return err
				}
				out = append(out, display)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatalf("as-of read: %v", err)
		}
		return out
	}

	if got := asOf(s.tenant, "2025-06-01"); len(got) != 1 || got[0] != "Eski karşılık" {
		t.Fatalf("as of 2025-06-01 = %v, want [Eski karşılık]", got)
	}
	if got := asOf(s.tenant, "2026-06-01"); len(got) != 1 || got[0] != "Yeni karşılık" {
		t.Fatalf("as of 2026-06-01 = %v, want [Yeni karşılık]", got)
	}
	// The day the old edition ends is the first day of the new one (half-open period).
	if got := asOf(s.tenant, "2026-01-01"); len(got) != 1 || got[0] != "Yeni karşılık" {
		t.Fatalf("as of 2026-01-01 = %v, want [Yeni karşılık]", got)
	}
	if got := asOf(s.tenant, "2024-01-01"); len(got) != 0 {
		t.Fatalf("as of 2024-01-01 = %v, want no rows", got)
	}
}

func TestCodeValueUniqueOnCodeAndValidFrom(t *testing.T) {
	h := dbtest.New(t)
	s := seedCatalog(h, "CV_UQ")

	h.AdminExec(`
		INSERT INTO catalog.code_value (tenant_id, code_system_id, code, display, valid_from)
		VALUES ($1, $2, 'P701010', 'İlk', '2026-01-01')`, s.tenant, s.system)

	// The import upserts on (code, valid_from), so a second row with that key is refused
	// and the ON CONFLICT clause takes over instead.
	err := h.AdminExecErr(`
		INSERT INTO catalog.code_value (tenant_id, code_system_id, code, display, valid_from)
		VALUES ($1, $2, 'P701010', 'İkinci', '2026-01-01')`, s.tenant, s.system)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "duplicate code value key")

	h.AdminExec(`
		INSERT INTO catalog.code_value (tenant_id, code_system_id, code, display, valid_from)
		VALUES ($1, $2, 'P701010', 'İkinci', '2026-01-01')
		ON CONFLICT (tenant_id, code_system_id, code, valid_from) DO UPDATE SET display = excluded.display`,
		s.tenant, s.system)

	ctx, cancel := h.Ctx()
	defer cancel()
	var display string
	if err := h.Admin.QueryRow(ctx,
		`SELECT display FROM catalog.code_value WHERE tenant_id = $1 AND code = 'P701010'`, s.tenant).Scan(&display); err != nil {
		t.Fatalf("read upserted row: %v", err)
	}
	if display != "İkinci" {
		t.Fatalf("display = %q, want İkinci", display)
	}
}

func TestCodeSystemUniquePerCodeAndVersion(t *testing.T) {
	h := dbtest.New(t)
	s := seedCatalog(h, "CS_UQ")

	err := h.AdminExecErr(`
		INSERT INTO catalog.code_system (tenant_id, code, name, version, authority, valid_from)
		VALUES ($1, 'SUT', 'Kopya', '2026', 'SGK', '2026-01-01')`, s.tenant)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "duplicate code system edition")

	// A second edition of the same system is a separate row: SUT 2024 and SUT 2026 live
	// side by side.
	h.AdminExec(`
		INSERT INTO catalog.code_system (tenant_id, code, name, version, authority, valid_from)
		VALUES ($1, 'SUT', 'Sağlık Uygulama Tebliği', '2024', 'SGK', '2024-01-01')`, s.tenant)
}

// TestCatalogTablesAreTenantIsolated is the RLS check for every table this work package
// touches, including the three added by migration 000019.
func TestCatalogTablesAreTenantIsolated(t *testing.T) {
	h := dbtest.New(t)
	a := seedCatalog(h, "CAT_RLS_A")
	b := seedCatalog(h, "CAT_RLS_B")

	h.AdminExec(`
		INSERT INTO catalog.code_value (tenant_id, code_system_id, code, display, valid_from)
		VALUES ($1, $2, 'P701010', 'A', '2026-01-01')`, a.tenant, a.system)
	h.AdminExec(`
		INSERT INTO catalog.service_code_mapping (tenant_id, service_definition_id, code_system_id, code, valid_from)
		VALUES ($1, $2, $3, 'P701010', '2026-01-01')`, a.tenant, a.definition, a.system)

	tables := []string{
		"catalog.service_category",
		"catalog.service_definition",
		"catalog.code_system",
		"catalog.code_value",
		"catalog.service_code_mapping",
	}
	for _, table := range tables {
		count := func(tenant uuid.UUID) int {
			t.Helper()
			var n int
			err := h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
				// The table name is a fixed literal from the list above, never user input.
				return tx.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n)
			})
			if err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
			return n
		}
		if got := count(a.tenant); got == 0 {
			t.Fatalf("%s: tenant A sees no rows of its own", table)
		}
		if got := count(b.tenant); got > 1 {
			t.Fatalf("%s: tenant B sees %d rows, want only its own seed", table, got)
		}
	}

	// A write tagged with another tenant violates the RLS WITH CHECK clause.
	err := h.AppTx(a.tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO catalog.code_system (tenant_id, code, name, version, authority, valid_from)
			VALUES ($1, 'ICD10', 'ICD-10', '2026', 'WHO', '2026-01-01')`, b.tenant)
		return err
	})
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateInsufficientPrivilege, "cross-tenant code system insert")

	// Reading another tenant's code values through the application role finds nothing,
	// even with the id in hand.
	var n int
	err = h.AppTx(b.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM catalog.code_value WHERE code_system_id = $1`, a.system).Scan(&n)
	})
	if err != nil {
		t.Fatalf("cross-tenant code value read: %v", err)
	}
	if n != 0 {
		t.Fatalf("tenant B sees %d of tenant A's code values, want 0", n)
	}
}

// TestServiceCodeMappingRejectsCrossTenantParents covers the composite foreign keys of
// migration 000019: a mapping may only join a definition and a code system of its own
// tenant.
func TestServiceCodeMappingRejectsCrossTenantParents(t *testing.T) {
	h := dbtest.New(t)
	a := seedCatalog(h, "MAP_FK_A")
	b := seedCatalog(h, "MAP_FK_B")

	err := h.AdminExecErr(`
		INSERT INTO catalog.service_code_mapping (tenant_id, service_definition_id, code_system_id, code, valid_from)
		VALUES ($1, $2, $3, 'P701010', '2026-01-01')`, b.tenant, a.definition, b.system)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateForeignKeyViolation, "cross-tenant definition reference")

	err = h.AdminExecErr(`
		INSERT INTO catalog.code_value (tenant_id, code_system_id, code, display, valid_from)
		VALUES ($1, $2, 'P701010', 'A', '2026-01-01')`, b.tenant, a.system)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateForeignKeyViolation, "cross-tenant code system reference")
}
