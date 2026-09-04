package dbtests

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// contractSeed is one tenant with everything migration 000021 hangs together: a payer
// organization, a provider with a location, a catalog category and a definition under it,
// a contract and one draft version with a price list.
type contractSeed struct {
	tenant     uuid.UUID
	payer      uuid.UUID
	provider   uuid.UUID
	location   uuid.UUID
	category   uuid.UUID
	definition uuid.UUID
	contract   uuid.UUID
	version    uuid.UUID
	priceList  uuid.UUID
	actor      uuid.UUID
}

func seedContract(h *dbtest.Harness, code string) contractSeed {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()

	s := contractSeed{tenant: h.CreateTenant(code)}
	s.payer = h.CreateTenantOrganization(s.tenant, "Sponsor "+code, "SPONSOR")
	providerOrg := h.CreateTenantOrganization(s.tenant, "Hastane "+code, "PROVIDER")
	s.actor = h.CreateActor("contract-"+code, "Contract Operator "+code)
	must := func(err error, what string) {
		h.T.Helper()
		if err != nil {
			h.T.Fatalf("seed %s: %v", what, err)
		}
	}
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO provider.provider_profile (tenant_id, tenant_organization_id, provider_type, status)
		VALUES ($1, $2, 'HOSPITAL', 'ACTIVE') RETURNING id`, s.tenant, providerOrg).Scan(&s.provider), "provider")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO provider.location (tenant_id, provider_profile_id, code, name)
		VALUES ($1, $2, 'MERKEZ', 'Merkez') RETURNING id`, s.tenant, s.provider).Scan(&s.location), "location")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Sağlık', 'HEALTH') RETURNING id`, s.tenant).Scan(&s.category), "category")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name, fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'PHYSIO_SESSION', 'Fizyoterapi seansı', 'SESSION', 'SESSION') RETURNING id`,
		s.tenant, s.category).Scan(&s.definition), "definition")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO contract.contract (tenant_id, code, name, payer_organization_id, provider_profile_id, domain_code, status)
		VALUES ($1, 'HEALTH_2026', 'Sağlık 2026', $2, $3, 'HEALTH', 'ACTIVE') RETURNING id`,
		s.tenant, s.payer, s.provider).Scan(&s.contract), "contract")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO contract.contract_version (tenant_id, contract_id, version_no, valid_from)
		VALUES ($1, $2, 1, '2026-01-01') RETURNING id`, s.tenant, s.contract).Scan(&s.version), "version")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO contract.price_list (tenant_id, contract_version_id, code, name)
		VALUES ($1, $2, 'STANDART', 'Standart liste') RETURNING id`, s.tenant, s.version).Scan(&s.priceList), "price list")
	return s
}

// publish moves a version to PUBLISHED as the schema owner, which is how the tests reach
// the states the exclusion constraint is about without going through the service.
func publishVersion(h *dbtest.Harness, s contractSeed, versionID uuid.UUID, from string, to any) error {
	h.T.Helper()
	return h.AdminExecErr(`
		UPDATE contract.contract_version
		   SET status = 'PUBLISHED', valid_from = $3::date, valid_to = $4::date,
		       configuration_hash = 'deadbeef', published_by = $5, published_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, versionID, from, to, s.actor)
}

// TestPublishedContractVersionsMayNotOverlap is the constraint the whole package exists
// for: "which price applied on this date" must have exactly one answer, so two published
// versions of one contract may not cover the same day.
func TestPublishedContractVersionsMayNotOverlap(t *testing.T) {
	h := dbtest.New(t)
	s := seedContract(h, "CONTRACT_OVERLAP")

	if err := publishVersion(h, s, s.version, "2026-01-01", "2027-01-01"); err != nil {
		t.Fatalf("publish first version: %v", err)
	}

	var second uuid.UUID
	ctx, cancel := h.Ctx()
	defer cancel()
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO contract.contract_version (tenant_id, contract_id, version_no, valid_from)
		VALUES ($1, $2, 2, '2026-06-01') RETURNING id`, s.tenant, s.contract).Scan(&second); err != nil {
		t.Fatalf("second version: %v", err)
	}
	err := publishVersion(h, s, second, "2026-06-01", nil)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateExclusionViolation, "overlapping published version")

	// The period is part of the exclusion key, so a version starting where the first ends
	// is a different agreement rather than a competing one.
	if err := publishVersion(h, s, second, "2027-01-01", nil); err != nil {
		t.Fatalf("adjacent published version: %v", err)
	}

	// A draft covering the same days is not a competing answer: only published versions
	// are ever read, so only published versions are excluded.
	var draft uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO contract.contract_version (tenant_id, contract_id, version_no, valid_from, valid_to)
		VALUES ($1, $2, 3, '2026-03-01', '2026-09-01') RETURNING id`, s.tenant, s.contract).Scan(&draft); err != nil {
		t.Fatalf("overlapping draft: %v", err)
	}
}

// TestPublishedContractVersionMustBeComplete covers ck_contract_version_published_complete
// and ck_contract_version_retire_reason: no code path can publish a half-filled row or
// retire one without saying why.
func TestPublishedContractVersionMustBeComplete(t *testing.T) {
	h := dbtest.New(t)
	s := seedContract(h, "CONTRACT_COMPLETE")

	err := h.AdminExecErr(`
		UPDATE contract.contract_version SET status = 'PUBLISHED' WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.version)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "publish without hash or publisher")

	err = h.AdminExecErr(`
		UPDATE contract.contract_version SET status = 'RETIRED' WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.version)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "retire without a reason")
}

// TestPriceItemNamesExactlyOneTarget covers ck_price_item_target. The specificity ladder of
// internal/contract/selection depends on exactly one of the three being set, so the
// database is the one that guarantees it.
func TestPriceItemNamesExactlyOneTarget(t *testing.T) {
	h := dbtest.New(t)
	s := seedContract(h, "PRICE_TARGET")

	insert := func(definition, category any) error {
		return h.AdminExecErr(`
			INSERT INTO contract.price_item (tenant_id, price_list_id, service_definition_id,
			                                 service_category_id, unit_type, pricing_method, amount, valid_from)
			VALUES ($1, $2, $3, $4, 'SESSION', 'FIXED', 100, '2026-01-01')`,
			s.tenant, s.priceList, definition, category)
	}
	dbtest.ExpectSQLState(t, insert(nil, nil), dbtest.SQLStateCheckViolation, "price item naming nothing")
	dbtest.ExpectSQLState(t, insert(s.definition, s.category), dbtest.SQLStateCheckViolation, "price item naming two targets")
	if err := insert(s.definition, nil); err != nil {
		t.Fatalf("price item naming one target: %v", err)
	}
}

// TestPriceItemMethodMatchesItsFields covers ck_price_item_method and
// ck_price_item_member_share: a row can never be half specified in a way the calculator
// would have to guess about.
func TestPriceItemMethodMatchesItsFields(t *testing.T) {
	h := dbtest.New(t)
	s := seedContract(h, "PRICE_METHOD")

	insert := func(method string, amount, percent, formula any, share string, shareAmount, sharePercent any) error {
		return h.AdminExecErr(`
			INSERT INTO contract.price_item (tenant_id, price_list_id, service_definition_id, unit_type,
			                                 pricing_method, amount, percent, formula_key,
			                                 member_share_method, member_share_amount, member_share_percent,
			                                 valid_from)
			VALUES ($1, $2, $3, 'SESSION', $4, $5, $6, $7, $8, $9, $10, '2026-01-01')`,
			s.tenant, s.priceList, s.definition, method, amount, percent, formula, share, shareAmount, sharePercent)
	}
	dbtest.ExpectSQLState(t, insert("FIXED", nil, nil, nil, "NONE", nil, nil),
		dbtest.SQLStateCheckViolation, "FIXED without an amount")
	dbtest.ExpectSQLState(t, insert("PERCENT_OF_LIST", 100, nil, nil, "NONE", nil, nil),
		dbtest.SQLStateCheckViolation, "PERCENT_OF_LIST with an amount")
	dbtest.ExpectSQLState(t, insert("FORMULA", nil, nil, nil, "NONE", nil, nil),
		dbtest.SQLStateCheckViolation, "FORMULA without a key")
	dbtest.ExpectSQLState(t, insert("FIXED", 100, nil, nil, "PERCENT", 5, nil),
		dbtest.SQLStateCheckViolation, "PERCENT member share carrying an amount")
	if err := insert("FIXED", 100, nil, nil, "PERCENT", nil, 20); err != nil {
		t.Fatalf("well formed price item: %v", err)
	}
}

// TestProviderQuotaCapacityAndScope covers the capacity CHECK, the overdraft CHECK and the
// NULLS NOT DISTINCT scope index: a scope that means "everything" has to be unique too.
func TestProviderQuotaCapacityAndScope(t *testing.T) {
	h := dbtest.New(t)
	s := seedContract(h, "QUOTA")

	err := h.AdminExecErr(`
		INSERT INTO contract.provider_quota (tenant_id, contract_version_id, period_type, period_from, period_to, capacity)
		VALUES ($1, $2, 'YEAR', '2026-01-01', '2027-01-01', 0)`, s.tenant, s.version)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "zero capacity")

	h.AdminExec(`
		INSERT INTO contract.provider_quota (tenant_id, contract_version_id, period_type, period_from, period_to, capacity)
		VALUES ($1, $2, 'YEAR', '2026-01-01', '2027-01-01', 100)`, s.tenant, s.version)

	err = h.AdminExecErr(`
		INSERT INTO contract.provider_quota (tenant_id, contract_version_id, period_type, period_from, period_to, capacity)
		VALUES ($1, $2, 'YEAR', '2026-01-01', '2027-01-01', 200)`, s.tenant, s.version)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "second tenant-wide quota for one period")

	// Naming a service narrows the scope, so it is a different quota.
	h.AdminExec(`
		INSERT INTO contract.provider_quota (tenant_id, contract_version_id, service_definition_id,
		                                     period_type, period_from, period_to, capacity)
		VALUES ($1, $2, $3, 'YEAR', '2026-01-01', '2027-01-01', 50)`, s.tenant, s.version, s.definition)

	err = h.AdminExecErr(`
		UPDATE contract.provider_quota SET consumed = 200
		 WHERE tenant_id = $1 AND contract_version_id = $2 AND service_definition_id IS NULL`, s.tenant, s.version)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "consumed above capacity without overdraft")

	h.AdminExec(`
		UPDATE contract.provider_quota SET allow_overdraft = true, consumed = 200
		 WHERE tenant_id = $1 AND contract_version_id = $2 AND service_definition_id IS NULL`, s.tenant, s.version)
}

// TestPaymentTermRequiresVatUnlessExempt covers ck_payment_term_vat and the one row per
// version rule.
func TestPaymentTermRequiresVatUnlessExempt(t *testing.T) {
	h := dbtest.New(t)
	s := seedContract(h, "PAYMENT_TERM")

	err := h.AdminExecErr(`
		INSERT INTO contract.payment_term (tenant_id, contract_version_id, due_days, settlement_method, tax_behaviour)
		VALUES ($1, $2, 30, 'BANK_TRANSFER', 'EXCLUSIVE')`, s.tenant, s.version)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "non-exempt term without a VAT rate")

	h.AdminExec(`
		INSERT INTO contract.payment_term (tenant_id, contract_version_id, due_days, settlement_method, tax_behaviour)
		VALUES ($1, $2, 30, 'BANK_TRANSFER', 'EXEMPT')`, s.tenant, s.version)

	err = h.AdminExecErr(`
		INSERT INTO contract.payment_term (tenant_id, contract_version_id, due_days, settlement_method, tax_behaviour)
		VALUES ($1, $2, 60, 'OFFSET', 'EXEMPT')`, s.tenant, s.version)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "second payment term for one version")
}

// TestPackageDefinitionMinLines covers ck_package_definition_min_lines: ANY_OF_N needs a
// minimum and ALL forbids one.
func TestPackageDefinitionMinLines(t *testing.T) {
	h := dbtest.New(t)
	s := seedContract(h, "PACKAGE")

	err := h.AdminExecErr(`
		INSERT INTO contract.package_definition (tenant_id, contract_version_id, code, name, inclusion_rule)
		VALUES ($1, $2, 'PAKET', 'Paket', 'ANY_OF_N')`, s.tenant, s.version)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "ANY_OF_N without min_lines")

	err = h.AdminExecErr(`
		INSERT INTO contract.package_definition (tenant_id, contract_version_id, code, name, inclusion_rule, min_lines)
		VALUES ($1, $2, 'PAKET', 'Paket', 'ALL', 2)`, s.tenant, s.version)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "ALL with min_lines")

	var pkg uuid.UUID
	ctx, cancel := h.Ctx()
	defer cancel()
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO contract.package_definition (tenant_id, contract_version_id, code, name, inclusion_rule, min_lines)
		VALUES ($1, $2, 'PAKET', 'Paket', 'ANY_OF_N', 2) RETURNING id`, s.tenant, s.version).Scan(&pkg); err != nil {
		t.Fatalf("well formed package: %v", err)
	}
	err = h.AdminExecErr(`
		INSERT INTO contract.package_line (tenant_id, package_definition_id, service_definition_id, included_quantity)
		VALUES ($1, $2, $3, 0)`, s.tenant, pkg, s.definition)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "package line with zero quantity")
}

// TestContractMoneyColumnsAreExactDecimals asserts the storage type itself: every money and
// quantity column of the schema is numeric(20,6), so no value in this module can ever have
// been rounded by a float on its way in.
func TestContractMoneyColumnsAreExactDecimals(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	rows, err := h.Admin.Query(ctx, `
		SELECT table_name, column_name, data_type, numeric_precision, numeric_scale
		  FROM information_schema.columns
		 WHERE table_schema = 'contract'
		   AND column_name IN ('amount','min_amount','max_amount','member_share_amount',
		                       'capacity','consumed','included_quantity')
		 ORDER BY table_name, column_name`)
	if err != nil {
		t.Fatalf("read column types: %v", err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var table, column, dataType string
		var precision, scale int
		if err := rows.Scan(&table, &column, &dataType, &precision, &scale); err != nil {
			t.Fatalf("scan column type: %v", err)
		}
		seen++
		if dataType != "numeric" || precision != 20 || scale != 6 {
			t.Fatalf("contract.%s.%s is %s(%d,%d), want numeric(20,6)", table, column, dataType, precision, scale)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("column types: %v", err)
	}
	if seen < 7 {
		t.Fatalf("found %d money columns in the contract schema, want at least 7", seen)
	}
}

// TestContractMoneyRoundTripsAsDecimalText stores an amount that no float64 can hold
// exactly and reads it back as text: the value that goes in is the value that comes out.
func TestContractMoneyRoundTripsAsDecimalText(t *testing.T) {
	h := dbtest.New(t)
	s := seedContract(h, "MONEY")
	ctx, cancel := h.Ctx()
	defer cancel()

	const exact = "12345678901.123457"
	h.AdminExec(`
		INSERT INTO contract.price_item (tenant_id, price_list_id, service_definition_id, unit_type,
		                                 pricing_method, amount, valid_from)
		VALUES ($1, $2, $3, 'SESSION', 'FIXED', $4::text::numeric, '2026-01-01')`,
		s.tenant, s.priceList, s.definition, exact)

	var stored string
	if err := h.Admin.QueryRow(ctx,
		`SELECT amount::text FROM contract.price_item WHERE tenant_id = $1`, s.tenant).Scan(&stored); err != nil {
		t.Fatalf("read amount: %v", err)
	}
	if stored != exact {
		t.Fatalf("amount round-tripped as %q, want %q", stored, exact)
	}
}

// TestContractTablesAreTenantIsolated is the RLS check for all seven tables of migration
// 000021.
func TestContractTablesAreTenantIsolated(t *testing.T) {
	h := dbtest.New(t)
	a := seedContract(h, "CONTRACT_RLS_A")
	b := seedContract(h, "CONTRACT_RLS_B")

	for _, s := range []contractSeed{a, b} {
		h.AdminExec(`
			INSERT INTO contract.price_item (tenant_id, price_list_id, service_definition_id, unit_type,
			                                 pricing_method, amount, valid_from)
			VALUES ($1, $2, $3, 'SESSION', 'FIXED', 100, '2026-01-01')`, s.tenant, s.priceList, s.definition)
		var pkg uuid.UUID
		ctx, cancel := h.Ctx()
		if err := h.Admin.QueryRow(ctx, `
			INSERT INTO contract.package_definition (tenant_id, contract_version_id, code, name)
			VALUES ($1, $2, 'PAKET', 'Paket') RETURNING id`, s.tenant, s.version).Scan(&pkg); err != nil {
			cancel()
			t.Fatalf("seed package: %v", err)
		}
		cancel()
		h.AdminExec(`
			INSERT INTO contract.package_line (tenant_id, package_definition_id, service_definition_id, included_quantity)
			VALUES ($1, $2, $3, 1)`, s.tenant, pkg, s.definition)
		h.AdminExec(`
			INSERT INTO contract.provider_quota (tenant_id, contract_version_id, period_type, period_from, period_to, capacity)
			VALUES ($1, $2, 'YEAR', '2026-01-01', '2027-01-01', 10)`, s.tenant, s.version)
		h.AdminExec(`
			INSERT INTO contract.payment_term (tenant_id, contract_version_id, due_days, settlement_method, tax_behaviour)
			VALUES ($1, $2, 30, 'BANK_TRANSFER', 'EXEMPT')`, s.tenant, s.version)
	}

	tables := []string{
		"contract.contract",
		"contract.contract_version",
		"contract.price_list",
		"contract.price_item",
		"contract.package_definition",
		"contract.package_line",
		"contract.provider_quota",
		"contract.payment_term",
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
			INSERT INTO contract.price_list (tenant_id, contract_version_id, code, name)
			VALUES ($1, $2, 'KACAK', 'Kaçak')`, b.tenant, b.version)
		return err
	})
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateInsufficientPrivilege, "cross-tenant price list insert")
}

// TestContractRowVersionIsOwnedByDatabase covers the ETag of every contract resource: the
// touch trigger moves row_version on every update, including one that changes nothing, and
// a stale token matches no row.
func TestContractRowVersionIsOwnedByDatabase(t *testing.T) {
	h := dbtest.New(t)
	s := seedContract(h, "CONTRACT_VER")
	ctx, cancel := h.Ctx()
	defer cancel()

	for _, tc := range []struct {
		table, column string
		id            uuid.UUID
	}{
		{"contract.contract", "name", s.contract},
		{"contract.contract_version", "currency_code", s.version},
		{"contract.price_list", "name", s.priceList},
	} {
		var before, after int64
		// The table and column names are fixed literals from the list above.
		read := `SELECT row_version FROM ` + tc.table + ` WHERE id = $1`
		if err := h.Admin.QueryRow(ctx, read, tc.id).Scan(&before); err != nil {
			t.Fatalf("read %s row_version: %v", tc.table, err)
		}
		h.AdminExec(`UPDATE `+tc.table+` SET `+tc.column+` = `+tc.column+` WHERE id = $1`, tc.id)
		if err := h.Admin.QueryRow(ctx, read, tc.id).Scan(&after); err != nil {
			t.Fatalf("read %s row_version: %v", tc.table, err)
		}
		if after != before+1 {
			t.Fatalf("%s row_version %d -> %d, want +1", tc.table, before, after)
		}

		var stale int64
		err := h.AppTx(s.tenant, func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx,
				`UPDATE `+tc.table+` SET `+tc.column+` = `+tc.column+` WHERE id = $1 AND row_version = $2`, tc.id, before)
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
