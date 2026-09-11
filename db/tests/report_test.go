package dbtests

import (
	"testing"

	"github.com/google/uuid"

	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// The reconciliation and export schema of migration 000047 (WP-I7-05), with the application layer
// bypassed entirely: every statement below runs as the schema owner through the admin pool, so no
// Go code of ours is between them and the constraint.
//
// These are the rules the service is allowed to lean on. A rule the service can forget is not a
// rule, and the four this package stands on are all here: a run nobody can edit, arithmetic the
// database checks rather than trusts, a settlement that cannot be called reconciled while it is
// short, and a filter blob that cannot carry an identifier.

// base32Tail is eight characters of the alphabet every reference in this platform uses. A uuid's
// hex would carry 0, 1, 8 and 9, which the CHECK on every reference deliberately refuses -- they
// are the characters a person reading a reference aloud confuses with O, I, B and g.
func base32Tail() string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	raw := uuid.New()
	out := make([]byte, 8)
	for i := range out {
		out[i] = alphabet[raw[i]%byte(len(alphabet))]
	}
	return string(out)
}

// reconciliationSeed is everything a reconciliation run and an export row reference.
type reconciliationSeed struct {
	tenant   uuid.UUID
	actor    uuid.UUID
	provider uuid.UUID
	payer    uuid.UUID
	batch    uuid.UUID
}

func seedReconciliation(t *testing.T, h *dbtest.Harness) reconciliationSeed {
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	var s reconciliationSeed
	s.tenant = h.CreateTenant("RPT" + uuid.NewString()[:6])
	s.actor = h.CreateActor("report-db-"+uuid.NewString()[:8], "Report DB")
	s.payer = h.CreateTenantOrganization(s.tenant, "Payer", "PAYER")
	s.provider = h.CreateTenantOrganization(s.tenant, "Provider", "PROVIDER")
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO billing.batch (tenant_id, reference, provider_organization_id,
		                           payer_organization_id, currency_code, period_from, period_to,
		                           status, submitted_at, submitted_by, decided_at, decided_by,
		                           invoice_count, submitted_total, approved_total)
		VALUES ($1, $2, $3, $4, 'TRY', '2026-03-01', '2026-03-31', 'DECIDED',
		        clock_timestamp(), $5, clock_timestamp(), $5, 1, 1000, 1000)
		RETURNING id`,
		s.tenant, "IC-202603-"+base32Tail(), s.provider, s.payer,
		s.actor).Scan(&s.batch); err != nil {
		t.Fatalf("seed batch: %v", err)
	}
	return s
}

// insertRun writes one run with the figures given and returns its id, or the error the database
// answered. Every arithmetic test below is a call to this with one number changed.
func (s reconciliationSeed) insertRun(t *testing.T, h *dbtest.Harness,
	settled, paid, open, difference, status string, differences string, count int,
) (uuid.UUID, error) {
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	var id uuid.UUID
	err := h.Admin.QueryRow(ctx, `
		INSERT INTO billing.reconciliation_run (tenant_id, scope, period_from, period_to, run_no,
		                                        currency_code, settled_total, paid_total,
		                                        open_total, difference, difference_count,
		                                        differences, status)
		VALUES ($1, 'TENANT', '2026-03-10', '2026-03-10',
		        (SELECT COALESCE(max(run_no), 0) + 1 FROM billing.reconciliation_run
		          WHERE tenant_id = $1 AND scope = 'TENANT'),
		        'TRY', $2::text::numeric, $3::text::numeric, $4::text::numeric,
		        $5::text::numeric, $6, $7::jsonb, $8)
		RETURNING id`,
		s.tenant, settled, paid, open, difference, count, differences, status).Scan(&id)
	return id, err
}

// **A reconciliation run is append-only.** Not "the service does not update it": the trigger
// refuses, with the application bypassed and the schema owner asking.
func TestReconciliationRunIsAppendOnly(t *testing.T) {
	h := dbtest.New(t)
	s := seedReconciliation(t, h)

	id, err := s.insertRun(t, h, "1000", "1000", "0", "0", "BALANCED", "[]", 0)
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}

	if err := h.AdminExecErr(`UPDATE billing.reconciliation_run SET status = 'BALANCED'
	                           WHERE tenant_id = $1 AND id = $2`, s.tenant, id); err == nil {
		t.Error("a reconciliation run was updated")
	}
	if err := h.AdminExecErr(`UPDATE billing.reconciliation_run SET paid_total = 999
	                           WHERE tenant_id = $1 AND id = $2`, s.tenant, id); err == nil {
		t.Error("a reconciliation run's figures were rewritten")
	}
	if err := h.AdminExecErr(`DELETE FROM billing.reconciliation_run
	                           WHERE tenant_id = $1 AND id = $2`, s.tenant, id); err == nil {
		t.Error("a reconciliation run was deleted")
	}
	var n int
	ctx, cancel := h.Ctx()
	defer cancel()
	if err := h.Admin.QueryRow(ctx, `SELECT count(*) FROM billing.reconciliation_run
	                                  WHERE tenant_id = $1`, s.tenant).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("the table holds %d runs, want the one that was written", n)
	}
}

// **The run's arithmetic is the database's.** `open_total` has to be settled minus paid and
// `difference` has to be settled minus the ERP's figure or the paid one, so a job that got either
// wrong fails at the constraint rather than writing a run two people read differently.
func TestReconciliationRunArithmeticIsChecked(t *testing.T) {
	h := dbtest.New(t)
	s := seedReconciliation(t, h)

	if _, err := s.insertRun(t, h, "3000", "1500", "1500", "1500", "DIFFERENCES",
		`[{"reference":"ST-202603-AAAA1111","kind":"UNDERPAID"}]`, 1); err != nil {
		t.Fatalf("a correct run was refused: %v", err)
	}
	// open_total that is not settled minus paid.
	if _, err := s.insertRun(t, h, "3000", "1500", "0", "1500", "DIFFERENCES",
		`[{"reference":"x"}]`, 1); err == nil {
		t.Error("a run whose open total is not settled minus paid was accepted")
	}
	// difference that is not settled minus paid, with no ERP figure to justify it.
	if _, err := s.insertRun(t, h, "3000", "1500", "1500", "0", "DIFFERENCES",
		`[{"reference":"x"}]`, 1); err == nil {
		t.Error("a run whose difference is not settled minus paid was accepted")
	}
	// A BALANCED run carrying a difference.
	if _, err := s.insertRun(t, h, "3000", "1500", "1500", "1500", "BALANCED",
		`[{"reference":"x"}]`, 1); err == nil {
		t.Error("a BALANCED run carrying a difference was accepted")
	}
	// A DIFFERENCES run carrying none.
	if _, err := s.insertRun(t, h, "1000", "1000", "0", "0", "DIFFERENCES", "[]", 0); err == nil {
		t.Error("a DIFFERENCES run carrying no difference was accepted")
	}
	// The count and the array have to agree.
	if _, err := s.insertRun(t, h, "3000", "1500", "1500", "1500", "DIFFERENCES",
		`[{"reference":"x"},{"reference":"y"}]`, 1); err == nil {
		t.Error("a run whose count and list disagree was accepted")
	}
}

// A PROVIDER run names a provider and a TENANT run names none. A tenant-wide run carrying one
// organization would be a total nobody could reproduce.
func TestReconciliationRunScopeAndProviderAgree(t *testing.T) {
	h := dbtest.New(t)
	s := seedReconciliation(t, h)

	if err := h.AdminExecErr(`
		INSERT INTO billing.reconciliation_run (tenant_id, scope, provider_organization_id,
		                                        period_from, period_to, run_no, currency_code,
		                                        status)
		VALUES ($1, 'TENANT', $2, '2026-03-10', '2026-03-10', 1, 'TRY', 'BALANCED')`,
		s.tenant, s.provider); err == nil {
		t.Error("a TENANT run naming a provider was accepted")
	}
	if err := h.AdminExecErr(`
		INSERT INTO billing.reconciliation_run (tenant_id, scope, period_from, period_to, run_no,
		                                        currency_code, status)
		VALUES ($1, 'PROVIDER', '2026-03-10', '2026-03-10', 1, 'TRY', 'BALANCED')`,
		s.tenant); err == nil {
		t.Error("a PROVIDER run naming no provider was accepted")
	}
}

// **One run number per scope, period and currency.** Two TENANT runs both calling themselves run
// one of the tenth of March would be two answers to "what did we find".
func TestReconciliationRunNumberIsUniquePerPeriod(t *testing.T) {
	h := dbtest.New(t)
	s := seedReconciliation(t, h)

	insert := func(runNo int) error {
		return h.AdminExecErr(`
			INSERT INTO billing.reconciliation_run (tenant_id, scope, period_from, period_to,
			                                        run_no, currency_code, status)
			VALUES ($1, 'TENANT', '2026-03-10', '2026-03-10', $2, 'TRY', 'BALANCED')`,
			s.tenant, runNo)
	}
	if err := insert(1); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := insert(1); err == nil {
		t.Error("a second run numbered one of the same day was accepted")
	}
	if err := insert(2); err != nil {
		t.Errorf("run number two of the same day was refused: %v", err)
	}
}

// **RECONCILED is above PAID.** A settlement that is a kuruş short cannot be marked reconciled by
// anybody — not by the run, not by a command, and not by a psql session. This is the third arm of
// WP-I7-04's "the status follows the sum", and it is what makes "only the run marks a settlement"
// safe to rely on.
func TestASettlementCannotBeReconciledWhileItIsShort(t *testing.T) {
	h := dbtest.New(t)
	s := seedReconciliation(t, h)
	ctx, cancel := h.Ctx()
	defer cancel()

	var settlement uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		WITH created AS (
			INSERT INTO billing.settlement (tenant_id, reference, batch_id,
			                                provider_organization_id, payer_organization_id,
			                                currency_code, approved_amount, withheld_amount,
			                                payable_amount, paid_amount, due_date,
			                                settlement_method, status, approved_by, approved_at)
			VALUES ($1, $2, $3, $4, $5, 'TRY', 1000, 0, 1000, 400, '2026-03-10',
			        'BANK_TRANSFER', 'PARTIALLY_PAID', $6, clock_timestamp())
			RETURNING id, tenant_id, provider_organization_id
		), recorded AS (
			INSERT INTO billing.payment_record (tenant_id, settlement_id,
			                                    provider_organization_id, external_reference,
			                                    amount, currency_code, paid_at, source, status)
			SELECT c.tenant_id, c.id, c.provider_organization_id, $7, 400, 'TRY',
			       clock_timestamp(), 'MANUAL', 'RECORDED' FROM created c
			RETURNING settlement_id
		)
		SELECT id FROM created`,
		s.tenant, "ST-202603-"+base32Tail(), s.batch, s.provider,
		s.payer, s.actor, "EFT"+base32Tail()).Scan(&settlement); err != nil {
		t.Fatalf("seed settlement: %v", err)
	}

	if err := h.AdminExecErr(`UPDATE billing.settlement SET status = 'RECONCILED'
	                           WHERE tenant_id = $1 AND id = $2`, s.tenant, settlement); err == nil {
		t.Fatal("a settlement that is short was marked RECONCILED")
	}
	// Paid in full, it may be — and that is the only way it may. The record and the stored
	// figure move in one statement because WP-I7-04's deferred trigger checks at commit that
	// they agree, which is the constraint doing its job.
	h.AdminExec(`
		WITH recorded AS (
			INSERT INTO billing.payment_record (tenant_id, settlement_id,
			                                    provider_organization_id, external_reference,
			                                    amount, currency_code, paid_at, source, status)
			VALUES ($1, $2, $3, $4, 600, 'TRY', clock_timestamp(), 'MANUAL', 'RECORDED')
			RETURNING settlement_id
		)
		UPDATE billing.settlement SET paid_amount = 1000, status = 'PAID'
		 WHERE tenant_id = $1 AND id = $2`,
		s.tenant, settlement, s.provider, "EFT"+base32Tail())
	if err := h.AdminExecErr(`UPDATE billing.settlement SET status = 'RECONCILED'
	                           WHERE tenant_id = $1 AND id = $2`, s.tenant, settlement); err != nil {
		t.Errorf("a fully paid settlement could not be marked RECONCILED: %v", err)
	}
}

// **The export's parameters hold no identifier.** The service refuses one too; this is the half
// that holds against a backfill, a psql session and a service somebody changes next year.
func TestExportParametersRefuseIdentifiers(t *testing.T) {
	h := dbtest.New(t)
	s := seedReconciliation(t, h)

	insert := func(parameters string) error {
		return h.AdminExecErr(`
			INSERT INTO report.export (tenant_id, kind, parameters, format, requested_by,
			                           expires_at, watermark)
			VALUES ($1, 'SETTLEMENTS', $2::jsonb, 'CSV', $3,
			        clock_timestamp() + interval '24 hours', 'KAPSORA TEST · a · b · c')`,
			s.tenant, parameters, s.actor)
	}
	for name, parameters := range map[string]string{
		"a uuid value":     `{"filter":"0195f2a0-0000-7000-8000-000000000001"}`,
		"a uuid in prose":  `{"note":"see 0195f2a0-0000-7000-8000-000000000001"}`,
		"a nested uuid":    `{"any":{"deep":"0195f2a0-0000-7000-8000-000000000001"}}`,
		"a camel-case key": `{"providerId":"x"}`,
		"a snake-case key": `{"claim_id":"x"}`,
		"a bare id key":    `{"id":"x"}`,
		"an array":         `["not an object"]`,
	} {
		if err := insert(parameters); err == nil {
			t.Errorf("%s was accepted into report.export.parameters", name)
		}
	}
	for name, parameters := range map[string]string{
		"statuses":       `{"status":"APPROVED"}`,
		"ordinary words": `{"valid":true,"paid":false,"overdue":true}`,
		"a date filter":  `{"from":"2026-03-01","to":"2026-03-31"}`,
		"nothing at all": `{}`,
	} {
		if err := insert(parameters); err != nil {
			t.Errorf("%s was refused: %v", name, err)
		}
	}
}

// An export that expired before it was asked for would be an export nobody could ever open, and a
// READY one with no file would be a link that answers 500.
func TestExportLifecycleColumnsAgree(t *testing.T) {
	h := dbtest.New(t)
	s := seedReconciliation(t, h)

	if err := h.AdminExecErr(`
		INSERT INTO report.export (tenant_id, kind, format, requested_by, requested_at,
		                           expires_at, watermark)
		VALUES ($1, 'SETTLEMENTS', 'CSV', $2, clock_timestamp(),
		        clock_timestamp() - interval '1 hour', 'KAPSORA TEST · a · b · c')`,
		s.tenant, s.actor); err == nil {
		t.Error("an export that expired before it was asked for was accepted")
	}
	if err := h.AdminExecErr(`
		INSERT INTO report.export (tenant_id, kind, format, status, requested_by, expires_at,
		                           watermark)
		VALUES ($1, 'SETTLEMENTS', 'CSV', 'READY', $2,
		        clock_timestamp() + interval '24 hours', 'KAPSORA TEST · a · b · c')`,
		s.tenant, s.actor); err == nil {
		t.Error("a READY export with no document was accepted")
	}
	if err := h.AdminExecErr(`
		INSERT INTO report.export (tenant_id, kind, format, status, failure_code, requested_by,
		                           expires_at, watermark)
		VALUES ($1, 'SETTLEMENTS', 'CSV', 'QUEUED', 'RENDER_FAILED', $2,
		        clock_timestamp() + interval '24 hours', 'KAPSORA TEST · a · b · c')`,
		s.tenant, s.actor); err == nil {
		t.Error("a QUEUED export carrying a failure code was accepted")
	}
	if err := h.AdminExecErr(`
		INSERT INTO report.export (tenant_id, kind, format, requested_by, expires_at, watermark)
		VALUES ($1, 'PROVIDER_STATEMENT', 'CSV', $2,
		        clock_timestamp() + interval '24 hours', 'KAPSORA TEST · a · b · c')`,
		s.tenant, s.actor); err == nil {
		t.Error("a statement export with no provider and no period was accepted")
	}
	if err := h.AdminExecErr(`
		INSERT INTO report.export (tenant_id, kind, format, provider_organization_id, period_from,
		                           period_to, requested_by, expires_at, watermark)
		VALUES ($1, 'PROVIDER_STATEMENT', 'CSV', $2, '2026-03-01', '2026-03-31', $3,
		        clock_timestamp() + interval '24 hours', 'KAPSORA TEST · a · b · c')`,
		s.tenant, s.provider, s.actor); err != nil {
		t.Errorf("a scoped statement export was refused: %v", err)
	}
	if err := h.AdminExecErr(`
		INSERT INTO report.export (tenant_id, kind, format, requested_by, expires_at, watermark)
		VALUES ($1, 'SETTLEMENTS', 'CSV', $2, clock_timestamp() + interval '24 hours', 'kısa')`,
		s.tenant, s.actor); err == nil {
		t.Error("an export carrying a watermark too short to trace anything was accepted")
	}
}

// TestReportPermissionsAreSeededAndGrantable checks both halves of the same fact: the catalogue
// rows of migration 000047 and the role templates in internal/identity/application/roles.go have
// to agree, because a permission that exists in one and not the other is a permission nobody can
// hold or one nobody can be given.
//
// `report.export.sensitive` is the row that matters. It is a grant of its own rather than a
// stricter reading of `report.export` so that a tenant can give its finance clerk every other
// export and not the one carrying claim line descriptions — which is impossible while the two are
// one permission.
func TestReportPermissionsAreSeededAndGrantable(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	granted := map[string][]string{}
	byRole := map[string]map[string]bool{}
	for _, tpl := range identityapp.RoleTemplates() {
		byRole[tpl.Code] = map[string]bool{}
		for _, code := range tpl.Permissions {
			granted[code] = append(granted[code], tpl.Code)
			byRole[tpl.Code][code] = true
		}
	}

	for code, wantSensitivity := range map[string]string{
		"report.read":             "NORMAL",
		"report.export":           "NORMAL",
		"report.export.sensitive": "SENSITIVE",
	} {
		var n int
		if err := h.Admin.QueryRow(ctx,
			`SELECT count(*) FROM iam.permission WHERE code = $1`, code).Scan(&n); err != nil {
			t.Fatalf("read permission %s: %v", code, err)
		}
		if n != 1 {
			t.Fatalf("permission %s is seeded %d times, want once", code, n)
		}
		var sensitivity string
		if err := h.Admin.QueryRow(ctx,
			`SELECT sensitivity FROM iam.permission WHERE code = $1`, code).Scan(
			&sensitivity); err != nil {
			t.Fatalf("read sensitivity of %s: %v", code, err)
		}
		if sensitivity != wantSensitivity {
			t.Errorf("permission %s sensitivity = %s, want %s", code, sensitivity, wantSensitivity)
		}
		if len(granted[code]) == 0 {
			t.Errorf("permission %s is in the catalogue but in no role template: nobody can "+
				"hold it", code)
		}
	}

	for _, want := range []struct{ role, permission string }{
		{"FINANCIAL_REVIEWER", "report.export"},
		{"FINANCIAL_REVIEWER", "report.export.sensitive"},
		{"PAYER_APPROVER", "report.export"},
		{"PROGRAM_MANAGER", "report.export"},
		{"AUDITOR", "report.export"},
		{"AUDITOR", "report.export.sensitive"},
		// WP-I7-06 §2.1.4: the provider exports its own cari ekstre. The service scopes the file
		// to the caller's organization, and it is watermarked and audited per download.
		{"PROVIDER_BILLING", "report.export"},
	} {
		if !byRole[want.role][want.permission] {
			t.Errorf("%s does not hold %s", want.role, want.permission)
		}
	}

	// The payer's approver releases money and has no reason to hold a spreadsheet of what
	// members were treated for. A grant quietly added here would defeat the whole point of
	// splitting the two permissions.
	if byRole["PAYER_APPROVER"]["report.export.sensitive"] {
		t.Error("PAYER_APPROVER must not hold report.export.sensitive")
	}
	if byRole["PROGRAM_MANAGER"]["report.export.sensitive"] {
		t.Error("PROGRAM_MANAGER must not hold report.export.sensitive")
	}
	// The provider's billing desk exports its own statement and nothing sensitive: the claims
	// file carries line descriptions, and those are never the provider's to take out.
	if byRole["PROVIDER_BILLING"]["report.export.sensitive"] {
		t.Error("PROVIDER_BILLING must not hold report.export.sensitive")
	}
	// The rest of the provider side, the member and the sponsor's HR export nothing at all.
	for _, role := range []string{"PROVIDER_STAFF", "MEMBER", "SPONSOR_HR"} {
		if byRole[role]["report.export"] || byRole[role]["report.export.sensitive"] {
			t.Errorf("%s holds an export grant", role)
		}
	}
	// Whoever may export may read: a role that could take the numbers out and not look at them
	// would have no way to check what it sent.
	for _, tpl := range identityapp.RoleTemplates() {
		if byRole[tpl.Code]["report.export"] && !byRole[tpl.Code]["report.read"] {
			t.Errorf("role %s may export reports but not read one", tpl.Code)
		}
		if byRole[tpl.Code]["report.export.sensitive"] && !byRole[tpl.Code]["report.export"] {
			t.Errorf("role %s holds the sensitive export grant and not the ordinary one, "+
				"which is a grant it cannot use", tpl.Code)
		}
	}
}
