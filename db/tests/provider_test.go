package dbtests

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// providerSeed is one tenant with a provider profile, a location, a catalog category and a
// definition under it, and one practitioner: everything migration 000020 hangs together.
type providerSeed struct {
	tenant       uuid.UUID
	organization uuid.UUID
	provider     uuid.UUID
	location     uuid.UUID
	category     uuid.UUID
	definition   uuid.UUID
	practitioner uuid.UUID
}

// registrationHash is a stand-in for crypto.BlindIndexer output: the column only requires
// 32 bytes, and these tests are about the constraints, not the cipher.
func registrationHash(seed byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = seed
	}
	return out
}

func seedProvider(h *dbtest.Harness, code string) providerSeed {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()

	s := providerSeed{tenant: h.CreateTenant(code)}
	s.organization = h.CreateTenantOrganization(s.tenant, "Hastane "+code, "PROVIDER")
	must := func(err error, what string) {
		h.T.Helper()
		if err != nil {
			h.T.Fatalf("seed %s: %v", what, err)
		}
	}
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO provider.provider_profile (tenant_id, tenant_organization_id, provider_type, status)
		VALUES ($1, $2, 'HOSPITAL', 'ACTIVE') RETURNING id`, s.tenant, s.organization).Scan(&s.provider), "profile")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO provider.location (tenant_id, provider_profile_id, code, name, city)
		VALUES ($1, $2, 'MERKEZ', 'Merkez', 'İstanbul') RETURNING id`, s.tenant, s.provider).Scan(&s.location), "location")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Sağlık', 'HEALTH') RETURNING id`, s.tenant).Scan(&s.category), "category")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name, fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'PHYSIO_SESSION', 'Fizyoterapi seansı', 'SESSION', 'SESSION') RETURNING id`,
		s.tenant, s.category).Scan(&s.definition), "definition")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO provider.practitioner (tenant_id, provider_profile_id, full_name, registration_authority,
		                                   registration_number_cipher, registration_number_hash,
		                                   registration_number_masked)
		VALUES ($1, $2, 'Dr. Test', 'TTB', '\x00'::bytea, $3, '12****') RETURNING id`,
		s.tenant, s.provider, registrationHash(1)).Scan(&s.practitioner), "practitioner")
	return s
}

// TestProviderProfileIsUniquePerOrganization covers uq_provider_profile_organization: the
// relationship is what the tenant contracts with, so two profiles for it would make "which
// provider" ambiguous.
func TestProviderProfileIsUniquePerOrganization(t *testing.T) {
	h := dbtest.New(t)
	s := seedProvider(h, "PROV_UQ")

	err := h.AdminExecErr(`
		INSERT INTO provider.provider_profile (tenant_id, tenant_organization_id, provider_type)
		VALUES ($1, $2, 'CLINIC')`, s.tenant, s.organization)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "second profile for one relationship")

	// A profile pointing at another tenant's relationship is refused by the composite key.
	other := seedProvider(h, "PROV_UQ_B")
	err = h.AdminExecErr(`
		INSERT INTO provider.provider_profile (tenant_id, tenant_organization_id, provider_type)
		VALUES ($1, $2, 'CLINIC')`, s.tenant, other.organization)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateForeignKeyViolation, "cross-tenant organization reference")
}

// TestCapabilityDefinitionOverlapRejected is the first of the two exclusion constraints:
// two rows naming the same service definition may not cover the same day, or "can this
// location deliver this on this date" would have two answers.
func TestCapabilityDefinitionOverlapRejected(t *testing.T) {
	h := dbtest.New(t)
	s := seedProvider(h, "CAP_DEF")

	h.AdminExec(`
		INSERT INTO provider.capability (tenant_id, location_id, service_definition_id, valid_from, valid_to)
		VALUES ($1, $2, $3, '2026-01-01', NULL)`, s.tenant, s.location, s.definition)

	err := h.AdminExecErr(`
		INSERT INTO provider.capability (tenant_id, location_id, service_definition_id, valid_from)
		VALUES ($1, $2, $3, '2026-06-01')`, s.tenant, s.location, s.definition)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateExclusionViolation, "overlapping definition capability")

	// The period is part of the exclusion key, so a closed row may be followed by another.
	h.AdminExec(`UPDATE provider.capability SET valid_to = '2026-06-01' WHERE tenant_id = $1`, s.tenant)
	h.AdminExec(`
		INSERT INTO provider.capability (tenant_id, location_id, service_definition_id, valid_from)
		VALUES ($1, $2, $3, '2026-06-01')`, s.tenant, s.location, s.definition)

	// Another location of the same provider may cover the same definition at the same time.
	var second uuid.UUID
	ctx, cancel := h.Ctx()
	defer cancel()
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO provider.location (tenant_id, provider_profile_id, code, name)
		VALUES ($1, $2, 'SUBE', 'Şube') RETURNING id`, s.tenant, s.provider).Scan(&second); err != nil {
		t.Fatalf("second location: %v", err)
	}
	h.AdminExec(`
		INSERT INTO provider.capability (tenant_id, location_id, service_definition_id, valid_from)
		VALUES ($1, $2, $3, '2026-06-01')`, s.tenant, second, s.definition)
}

// TestCapabilityCategoryOverlapRejected is the second exclusion constraint, and the proof
// that the two are independent: a category row and a definition row may cover the same day.
func TestCapabilityCategoryOverlapRejected(t *testing.T) {
	h := dbtest.New(t)
	s := seedProvider(h, "CAP_CAT")

	h.AdminExec(`
		INSERT INTO provider.capability (tenant_id, location_id, service_category_id, valid_from)
		VALUES ($1, $2, $3, '2026-01-01')`, s.tenant, s.location, s.category)

	err := h.AdminExecErr(`
		INSERT INTO provider.capability (tenant_id, location_id, service_category_id, valid_from)
		VALUES ($1, $2, $3, '2026-06-01')`, s.tenant, s.location, s.category)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateExclusionViolation, "overlapping category capability")

	// A definition row over the same period is a different constraint and is allowed: the
	// definition row narrows what the category already covers.
	h.AdminExec(`
		INSERT INTO provider.capability (tenant_id, location_id, service_definition_id, valid_from)
		VALUES ($1, $2, $3, '2026-01-01')`, s.tenant, s.location, s.definition)

	// Exactly one target: neither and both are refused by ck_capability_target.
	err = h.AdminExecErr(`
		INSERT INTO provider.capability (tenant_id, location_id, valid_from)
		VALUES ($1, $2, '2027-01-01')`, s.tenant, s.location)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "capability with no target")
	err = h.AdminExecErr(`
		INSERT INTO provider.capability (tenant_id, location_id, service_definition_id, service_category_id, valid_from)
		VALUES ($1, $2, $3, $4, '2027-01-01')`, s.tenant, s.location, s.definition, s.category)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "capability with both targets")
}

// TestCategoryCapabilityCoversALaterDefinition is the read-time resolution the search
// performs: a definition created under a covered category afterwards is covered without a
// single provider row changing.
func TestCategoryCapabilityCoversALaterDefinition(t *testing.T) {
	h := dbtest.New(t)
	s := seedProvider(h, "CAP_TREE")
	ctx, cancel := h.Ctx()
	defer cancel()

	var leaf, later uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_category (tenant_id, parent_id, code, name, domain_code)
		VALUES ($1, $2, 'PHYSIO', 'Fizyoterapi', 'HEALTH') RETURNING id`, s.tenant, s.category).Scan(&leaf); err != nil {
		t.Fatalf("leaf category: %v", err)
	}
	// The capability names the root, one level above the definition's own category.
	h.AdminExec(`
		INSERT INTO provider.capability (tenant_id, location_id, service_category_id, valid_from)
		VALUES ($1, $2, $3, '2026-01-01')`, s.tenant, s.location, s.category)
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name, fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'PHYSIO_NEW', 'Sonradan eklenen', 'SESSION', 'SESSION') RETURNING id`,
		s.tenant, leaf).Scan(&later); err != nil {
		t.Fatalf("later definition: %v", err)
	}

	var matched int
	err := h.AppTx(s.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			WITH RECURSIVE seed AS (
			    SELECT d.category_id AS id
			      FROM catalog.service_definition d
			     WHERE d.tenant_id = $1 AND d.id = $2
			), up AS (
			    SELECT c.id, c.parent_id, 1 AS depth
			      FROM catalog.service_category c
			      JOIN seed s ON s.id = c.id
			     WHERE c.tenant_id = $1
			    UNION ALL
			    SELECT p.id, p.parent_id, up.depth + 1
			      FROM catalog.service_category p
			      JOIN up ON p.id = up.parent_id
			     WHERE p.tenant_id = $1 AND up.depth < 64
			)
			SELECT count(*)::int
			  FROM provider.capability c
			 WHERE c.tenant_id = $1
			   AND c.valid_from <= '2026-06-01'::date
			   AND (c.valid_to IS NULL OR c.valid_to > '2026-06-01'::date)
			   AND (c.service_definition_id = $2 OR c.service_category_id IN (SELECT id FROM up))`,
			s.tenant, later).Scan(&matched)
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if matched != 1 {
		t.Fatalf("the category capability covered %d rows for a later definition, want 1", matched)
	}
}

// TestPractitionerRegistrationIsUniquePerAuthority covers uq_practitioner_registration: one
// registration number belongs to one practitioner within its issuing body, and the index is
// what is compared, never the plaintext.
func TestPractitionerRegistrationIsUniquePerAuthority(t *testing.T) {
	h := dbtest.New(t)
	s := seedProvider(h, "PRAC_UQ")

	err := h.AdminExecErr(`
		INSERT INTO provider.practitioner (tenant_id, provider_profile_id, full_name, registration_authority,
		                                   registration_number_cipher, registration_number_hash, registration_number_masked)
		VALUES ($1, $2, 'Dr. Kopya', 'TTB', '\x00'::bytea, $3, '12****')`,
		s.tenant, s.provider, registrationHash(1))
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "duplicate registration number")

	// The same index under another issuing body is a different registration.
	h.AdminExec(`
		INSERT INTO provider.practitioner (tenant_id, provider_profile_id, full_name, registration_authority,
		                                   registration_number_cipher, registration_number_hash, registration_number_masked)
		VALUES ($1, $2, 'Dr. Diğer Otorite', 'SB', '\x00'::bytea, $3, '12****')`,
		s.tenant, s.provider, registrationHash(1))

	// And so is the same index in another tenant: the uniqueness key carries the tenant, so
	// the seed of a second tenant writes the identical digest without a conflict.
	other := seedProvider(h, "PRAC_UQ_B")
	if other.practitioner == uuid.Nil {
		t.Fatal("the same registration index must be free in another tenant")
	}

	// The index column is a fixed width, so a truncated or foreign digest is refused.
	err = h.AdminExecErr(`
		INSERT INTO provider.practitioner (tenant_id, provider_profile_id, full_name, registration_authority,
		                                   registration_number_cipher, registration_number_hash, registration_number_masked)
		VALUES ($1, $2, 'Dr. Kısa', 'TDB', '\x00'::bytea, '\x0102'::bytea, '12****')`, s.tenant, s.provider)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "short registration index")
}

// TestPractitionerLocationOverlapRejected is the third exclusion constraint: a practitioner
// may not hold the same role at the same location twice over the same days.
func TestPractitionerLocationOverlapRejected(t *testing.T) {
	h := dbtest.New(t)
	s := seedProvider(h, "PRAC_LOC")

	h.AdminExec(`
		INSERT INTO provider.practitioner_location (tenant_id, practitioner_id, location_id, role, valid_from)
		VALUES ($1, $2, $3, 'ATTENDING', '2026-01-01')`, s.tenant, s.practitioner, s.location)

	err := h.AdminExecErr(`
		INSERT INTO provider.practitioner_location (tenant_id, practitioner_id, location_id, role, valid_from)
		VALUES ($1, $2, $3, 'ATTENDING', '2026-06-01')`, s.tenant, s.practitioner, s.location)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateExclusionViolation, "overlapping practitioner assignment")

	// A second role at the same location over the same period is a different assignment.
	h.AdminExec(`
		INSERT INTO provider.practitioner_location (tenant_id, practitioner_id, location_id, role, valid_from)
		VALUES ($1, $2, $3, 'CONSULTANT', '2026-06-01')`, s.tenant, s.practitioner, s.location)

	// Closing the first spell frees the role for the next one.
	h.AdminExec(`UPDATE provider.practitioner_location SET valid_to = '2026-06-01'
	              WHERE tenant_id = $1 AND role = 'ATTENDING'`, s.tenant)
	h.AdminExec(`
		INSERT INTO provider.practitioner_location (tenant_id, practitioner_id, location_id, role, valid_from)
		VALUES ($1, $2, $3, 'ATTENDING', '2026-06-01')`, s.tenant, s.practitioner, s.location)

	// A practitioner and a location of two different tenants cannot be joined at all.
	other := seedProvider(h, "PRAC_LOC_B")
	err = h.AdminExecErr(`
		INSERT INTO provider.practitioner_location (tenant_id, practitioner_id, location_id, role, valid_from)
		VALUES ($1, $2, $3, 'ATTENDING', '2026-01-01')`, other.tenant, s.practitioner, other.location)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateForeignKeyViolation, "cross-tenant practitioner reference")
}

// TestProviderTablesAreTenantIsolated is the RLS check for all five tables of migration
// 000020.
func TestProviderTablesAreTenantIsolated(t *testing.T) {
	h := dbtest.New(t)
	a := seedProvider(h, "PROV_RLS_A")
	b := seedProvider(h, "PROV_RLS_B")

	for _, s := range []providerSeed{a, b} {
		h.AdminExec(`
			INSERT INTO provider.capability (tenant_id, location_id, service_definition_id, valid_from)
			VALUES ($1, $2, $3, '2026-01-01')`, s.tenant, s.location, s.definition)
		h.AdminExec(`
			INSERT INTO provider.practitioner_location (tenant_id, practitioner_id, location_id, role, valid_from)
			VALUES ($1, $2, $3, 'ATTENDING', '2026-01-01')`, s.tenant, s.practitioner, s.location)
	}

	tables := []string{
		"provider.provider_profile",
		"provider.location",
		"provider.capability",
		"provider.practitioner",
		"provider.practitioner_location",
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
		if got := count(a.tenant); got != 1 {
			t.Fatalf("%s: tenant A sees %d rows of its own, want 1", table, got)
		}
		if got := count(b.tenant); got != 1 {
			t.Fatalf("%s: tenant B sees %d rows, want only its own", table, got)
		}
	}

	// A write tagged with another tenant violates the RLS WITH CHECK clause.
	err := h.AppTx(a.tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO provider.location (tenant_id, provider_profile_id, code, name)
			VALUES ($1, $2, 'KACAK', 'Kaçak')`, b.tenant, b.provider)
		return err
	})
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateInsufficientPrivilege, "cross-tenant location insert")

	// Reading another tenant's rows through the application role finds nothing, even with
	// the id in hand.
	var n int
	err = h.AppTx(b.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM provider.practitioner WHERE provider_profile_id = $1`, a.provider).Scan(&n)
	})
	if err != nil {
		t.Fatalf("cross-tenant practitioner read: %v", err)
	}
	if n != 0 {
		t.Fatalf("tenant B sees %d of tenant A's practitioners, want 0", n)
	}
}

// TestProviderRowVersionIsOwnedByDatabase covers the ETag of every provider resource: the
// touch trigger moves row_version on every update, including one that changes nothing, and
// a stale token matches no row.
func TestProviderRowVersionIsOwnedByDatabase(t *testing.T) {
	h := dbtest.New(t)
	s := seedProvider(h, "PROV_VER")
	ctx, cancel := h.Ctx()
	defer cancel()

	for _, tc := range []struct{ table, column string }{
		{"provider.provider_profile", "notes"},
		{"provider.location", "name"},
		{"provider.practitioner", "full_name"},
	} {
		id := map[string]uuid.UUID{
			"provider.provider_profile": s.provider,
			"provider.location":         s.location,
			"provider.practitioner":     s.practitioner,
		}[tc.table]

		var before, after int64
		// The table and column names are fixed literals from the list above.
		read := `SELECT row_version FROM ` + tc.table + ` WHERE id = $1`
		if err := h.Admin.QueryRow(ctx, read, id).Scan(&before); err != nil {
			t.Fatalf("read %s row_version: %v", tc.table, err)
		}
		h.AdminExec(`UPDATE `+tc.table+` SET `+tc.column+` = `+tc.column+` WHERE id = $1`, id)
		if err := h.Admin.QueryRow(ctx, read, id).Scan(&after); err != nil {
			t.Fatalf("read %s row_version: %v", tc.table, err)
		}
		if after != before+1 {
			t.Fatalf("%s row_version %d -> %d, want +1", tc.table, before, after)
		}

		var stale int64
		err := h.AppTx(s.tenant, func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx,
				`UPDATE `+tc.table+` SET `+tc.column+` = `+tc.column+` WHERE id = $1 AND row_version = $2`, id, before)
			stale = tag.RowsAffected()
			return err
		})
		if err != nil {
			t.Fatalf("stale update on %s: %v", tc.table, err)
		}
		if stale != 0 {
			t.Fatalf("%s: stale update affected %d rows, want 0", tc.table, stale)
		}
	}
}
