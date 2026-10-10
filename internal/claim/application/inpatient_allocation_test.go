package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	authorizationapp "github.com/celikbros/kapsora/internal/authorization/application"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/claim/application"
	"github.com/celikbros/kapsora/internal/claim/domain"
	"github.com/celikbros/kapsora/internal/platform/db"
)

func inpatientDraft(t *testing.T, f *fixture, caseID uuid.UUID, quantity string) application.ClaimView {
	t.Helper()
	source, err := f.claims.GetCaseSource(context.Background(), f.providerRC(), caseID)
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Lines) != 1 || benefitdomain.MustQuantity(source.Lines[0].Quantity).Cmp(benefitdomain.MustQuantity(quantity)) != 0 {
		t.Fatalf("source lines=%+v, want %s nights", source.Lines, quantity)
	}
	amount := benefitdomain.MustQuantity(quantity).Mul(benefitdomain.MustQuantity("400")).String()
	draft, err := f.claims.CreateFromCase(context.Background(), f.providerRC(), caseID,
		source.RowVersion, []application.CaseCharge{{ServiceID: f.inpatient, Quantity: quantity, LineAmount: amount}})
	if err != nil {
		t.Fatal(err)
	}
	return draft
}

func allocationRows(t *testing.T, f *fixture, claimID uuid.UUID, versionNo int) []struct {
	auth                  uuid.UUID
	planned, applied, key string
} {
	t.Helper()
	rows, err := f.h.Admin.Query(context.Background(), `
 SELECT a.authorization_id,a.planned_quantity::text,a.applied_quantity::text,a.idempotency_key
 FROM claim.claim_line_authorization_allocation a
 JOIN claim.claim_version v ON v.tenant_id=a.tenant_id AND v.id=a.version_id
 WHERE v.claim_id=$1 AND v.version_no=$2 ORDER BY a.allocation_order`, claimID, versionNo)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []struct {
		auth                  uuid.UUID
		planned, applied, key string
	}{}
	for rows.Next() {
		var row struct {
			auth                  uuid.UUID
			planned, applied, key string
		}
		if err := rows.Scan(&row.auth, &row.planned, &row.applied, &row.key); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInpatientClaimAllocatesOriginalThenExtensionAndInvoicesActualNights(t *testing.T) {
	f := newFixture(t)
	caseID, stayID, original, extension := f.inpatientStayFixture(t, 6)
	draft := inpatientDraft(t, f, caseID, "6")
	for _, change := range []struct {
		column string
		value  uuid.UUID
	}{
		{"case_id", f.caseID}, {"provider_organization_id", f.otherOrg},
		{"authorization_id", extension},
	} {
		query := "UPDATE claim.claim SET " + change.column + "=$1 WHERE id=$2"
		if _, err := f.h.Admin.Exec(context.Background(), query, change.value, draft.Claim.ID); err == nil {
			t.Fatalf("inpatient source accepted changed %s", change.column)
		}
	}
	if draft.Claim.SourceType == nil || *draft.Claim.SourceType != "INPATIENT_STAY" ||
		draft.Claim.SourceID == nil || *draft.Claim.SourceID != stayID {
		t.Fatal("claim did not retain exact stay source")
	}
	submitted := submitClaim(t, f, draft)
	if submitted.Claim.Status != domain.StatusApproved {
		t.Fatalf("status=%s", submitted.Claim.Status)
	}
	rows := allocationRows(t, f, submitted.Claim.ID, 1)
	if len(rows) != 2 || rows[0].auth != original || rows[0].planned != "5.000000" || rows[0].applied != "5.000000" ||
		rows[1].auth != extension || rows[1].planned != "1.000000" || rows[1].applied != "1.000000" {
		t.Fatalf("allocations=%+v", rows)
	}
	if f.consumedTotal(t, original) != "5" || f.consumedTotal(t, extension) != "1" {
		t.Fatal("hold draws differ from receipt")
	}
	ready, err := f.claims.InvoiceReadiness(context.Background(), f.financialRC(), submitted.Claim.ID)
	if err != nil || !ready.Ready || ready.ApprovedTotal != "2400" || ready.PayerTotal != "2400" || ready.MemberTotal != "0" {
		t.Fatalf("readiness=%+v err=%v", ready, err)
	}
	if _, err := f.h.Admin.Exec(context.Background(),
		"UPDATE claim.claim_line_authorization_allocation SET planned_quantity=4 WHERE idempotency_key=$1", rows[0].key); err == nil {
		t.Fatal("submitted allocation receipt was mutable")
	}
	foreignTenant := f.h.CreateTenant("OTHER_INPATIENT_ALLOCATION")
	for _, boundary := range []struct {
		tenant uuid.UUID
		want   int
	}{{f.tenant, 2}, {foreignTenant, 0}} {
		err := db.WithTenantTx(context.Background(), f.h.App, db.TenantContext{TenantID: boundary.tenant},
			func(ctx context.Context, tx pgx.Tx) error {
				var count int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM claim.claim_line_authorization_allocation
					WHERE tenant_id=$1`, f.tenant).Scan(&count); err != nil {
					return err
				}
				if count != boundary.want {
					t.Fatalf("allocation RLS count=%d want=%d", count, boundary.want)
				}
				if boundary.tenant != f.tenant {
					command, err := tx.Exec(ctx, `DELETE FROM claim.claim_line_authorization_allocation WHERE tenant_id=$1`, f.tenant)
					if err != nil {
						return err
					}
					if command.RowsAffected() != 0 {
						t.Fatal("foreign tenant deleted allocation receipt")
					}
				}
				return nil
			})
		if err != nil {
			t.Fatal(err)
		}
	}
	f.ledgerConserved(t)
}

func TestInpatientClaimShortageIsAtomicAndReceiptedAsUnapplied(t *testing.T) {
	f := newFixture(t)
	caseID, _, original, extension := f.inpatientStayFixture(t, 6)
	err := db.WithTenantTx(context.Background(), f.h.App, db.TenantContext{TenantID: f.tenant}, func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.authorizations.ReleaseUnused(ctx, tx, authorizationapp.ReleaseUnusedInput{
			TenantID: f.tenant, ActorID: f.actor, AuthorizationID: extension,
			Quantity: benefitdomain.MustQuantity("1"), ReasonCode: "TEST_SHORTAGE"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	submitted := submitClaim(t, f, inpatientDraft(t, f, caseID, "6"))
	if submitted.Claim.Status != domain.StatusPendingMedical {
		t.Fatalf("status=%s", submitted.Claim.Status)
	}
	rows := allocationRows(t, f, submitted.Claim.ID, 1)
	if len(rows) != 2 || rows[0].planned != "5.000000" || rows[1].planned != "1.000000" ||
		rows[0].applied != "0.000000" || rows[1].applied != "0.000000" {
		t.Fatalf("unapplied plan=%+v", rows)
	}
	if f.consumedTotal(t, original) != "0" || f.consumedTotal(t, extension) != "0" {
		t.Fatal("shortage partially consumed holds")
	}
	decided, err := f.claims.DecideLines(context.Background(), f.medicalRC(), submitted.Claim.ID,
		application.DecideInput{Decisions: []application.DecisionInput{{
			LineNo: 1, Decision: domain.DecisionApproved, ApprovedQuantity: "6",
			ApprovedAmount: "2400", PayerAmount: "2400", MemberAmount: "0", ReasonCode: "MANUAL_OK",
		}}, ExpectedVersion: submitted.Claim.RowVersion}, application.AccessRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.claims.Approve(context.Background(), f.medicalRC(), decided.Claim.ID,
		application.ReasonInput{ReasonCode: "COMPLETE", ExpectedVersion: decided.Claim.RowVersion}); !errors.Is(err, application.ErrInpatientAllocationMissing) {
		t.Fatalf("manual positive approval without draws: %v", err)
	}
	// An inconsistent historical row must never yield a usable invoice-readiness response.
	f.h.AdminExec(`UPDATE claim.claim SET status='APPROVED' WHERE tenant_id=$1 AND id=$2`,
		f.tenant, decided.Claim.ID)
	if _, err := f.claims.InvoiceReadiness(context.Background(), f.providerRC(), decided.Claim.ID); !errors.Is(err, application.ErrInpatientAllocationMissing) {
		t.Fatalf("readiness without allocation receipt: %v", err)
	}
}

func TestInpatientClaimReturnUndoesOnlyFrozenDrawsAndKeepsHistory(t *testing.T) {
	f := newFixture(t)
	f.publishAdjudicationRule(t, "INPATIENT_FINANCIAL", "serviceCode == \"INPATIENT_DAY\"", actionFinancialReview)
	caseID, _, original, extension := f.inpatientStayFixture(t, 6)
	submitted := submitClaim(t, f, inpatientDraft(t, f, caseID, "6"))
	if submitted.Claim.Status != domain.StatusPendingFinancial {
		t.Fatalf("status=%s", submitted.Claim.Status)
	}
	returned, err := f.claims.Return(context.Background(), f.financialRC(), submitted.Claim.ID,
		application.ReasonInput{ReasonCode: "AMOUNT_CORRECTION", ExpectedVersion: submitted.Claim.RowVersion})
	if err != nil {
		t.Fatal(err)
	}
	if f.consumedTotal(t, original) != "0" || f.consumedTotal(t, extension) != "0" {
		t.Fatal("return did not restore both holds")
	}
	old := allocationRows(t, f, submitted.Claim.ID, 1)
	if len(old) != 2 || old[0].applied != "5.000000" || old[1].applied != "1.000000" {
		t.Fatal("frozen receipt changed")
	}
	edited, err := f.claims.PutLines(context.Background(), f.providerRC(), returned.Claim.ID,
		[]application.NewLineInput{{LineNo: 1, ServiceDefinitionID: f.inpatient, UnitType: "NIGHT", Quantity: "6", LineAmount: "2340"}},
		returned.Claim.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	again := submitClaim(t, f, edited)
	latest := allocationRows(t, f, again.Claim.ID, 2)
	if len(latest) != 2 || latest[0].key == old[0].key || latest[1].key == old[1].key {
		t.Fatal("correction reused frozen draw keys")
	}
	if f.consumedTotal(t, original) != "5" || f.consumedTotal(t, extension) != "1" {
		t.Fatal("corrected claim did not draw once")
	}
}

func TestInpatientOverstayRoutesToMedicalWithoutAnyDraw(t *testing.T) {
	f := newFixture(t)
	caseID, _, original, extension := f.inpatientStayFixture(t, 9)
	submitted := submitClaim(t, f, inpatientDraft(t, f, caseID, "9"))
	if submitted.Claim.Status != domain.StatusPendingMedical {
		t.Fatalf("overstay status=%s", submitted.Claim.Status)
	}
	if _, found := exceptionByCode(submitted, application.ReasonStayOverAuthorization); !found {
		t.Fatal("missing stay over-authorization exception")
	}
	if len(allocationRows(t, f, submitted.Claim.ID, 1)) != 0 ||
		f.consumedTotal(t, original) != "0" || f.consumedTotal(t, extension) != "0" {
		t.Fatal("overstay drew from a hold")
	}
}

func TestInpatientRuleCutNeedsFullFundedStayDraw(t *testing.T) {
	for _, shortage := range []bool{false, true} {
		name := "funded"
		if shortage {
			name = "shortage"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.publishAdjudicationRule(t, "INPATIENT_CUT", `serviceCode == "INPATIENT_DAY"`,
				`[{"type":"PARTIAL_APPROVE","payload":{"amount":"2000"}}]`)
			caseID, _, original, extension := f.inpatientStayFixture(t, 6)
			if shortage {
				err := db.WithTenantTx(context.Background(), f.h.App, db.TenantContext{TenantID: f.tenant}, func(ctx context.Context, tx pgx.Tx) error {
					_, err := f.authorizations.ReleaseUnused(ctx, tx, authorizationapp.ReleaseUnusedInput{
						TenantID: f.tenant, ActorID: f.actor, AuthorizationID: extension,
						Quantity: benefitdomain.MustQuantity("1"), ReasonCode: "TEST_CUT_SHORTAGE"})
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			submitted := submitClaim(t, f, inpatientDraft(t, f, caseID, "6"))
			if shortage {
				if submitted.Claim.Status != domain.StatusPendingMedical || lineByNo(t, submitted, 1).Decision != nil {
					t.Fatalf("unfunded cut auto-decided: %+v", submitted)
				}
				if f.consumedTotal(t, original) != "0" || f.consumedTotal(t, extension) != "0" {
					t.Fatal("unfunded cut drew")
				}
			} else {
				if submitted.Claim.Status != domain.StatusApproved ||
					lineByNo(t, submitted, 1).Decision == nil ||
					lineByNo(t, submitted, 1).Decision.Decision != domain.DecisionCut ||
					lineByNo(t, submitted, 1).Decision.ApprovedAmount != "2000" {
					t.Fatalf("funded cut result=%+v", submitted)
				}
				if f.consumedTotal(t, original) != "5" || f.consumedTotal(t, extension) != "1" {
					t.Fatal("funded cut did not draw full six nights")
				}
			}
		})
	}
}

func TestGenericClaimCannotBypassInpatientStaySource(t *testing.T) {
	f := newFixture(t)
	caseID, _, original, _ := f.inpatientStayFixture(t, 2)
	_, err := f.claims.CreateClaim(context.Background(), f.providerRC(), application.NewClaimInput{
		PersonID: f.person, ProgramID: f.program, EnrollmentID: f.enrollment,
		ProviderOrganizationID: f.provider, CaseID: &caseID, AuthorizationID: &original,
		ServiceDateFrom: serviceDay, ServiceDateTo: serviceDay,
		Lines: []application.NewLineInput{{LineNo: 1, ServiceDefinitionID: f.inpatient, UnitType: "NIGHT",
			Quantity: "2", LineAmount: "800"}},
	}, application.AccessRequest{})
	if err == nil {
		t.Fatal("generic claim bypassed discharged stay handoff")
	}
}

func TestEarlyInpatientDischargeUsesOnlyOriginalHold(t *testing.T) {
	f := newFixture(t)
	caseID, _, original, extension := f.inpatientStayFixture(t, 2)
	submitted := submitClaim(t, f, inpatientDraft(t, f, caseID, "2"))
	if submitted.Claim.Status != domain.StatusApproved {
		t.Fatalf("status=%s", submitted.Claim.Status)
	}
	rows := allocationRows(t, f, submitted.Claim.ID, 1)
	if len(rows) != 1 || rows[0].auth != original || rows[0].planned != "2.000000" ||
		rows[0].applied != "2.000000" {
		t.Fatalf("early allocation=%+v", rows)
	}
	if f.consumedTotal(t, original) != "2" || f.consumedTotal(t, extension) != "0" {
		t.Fatal("early discharge touched extension")
	}
}

func TestInpatientTerminalDecisionsKeepUsedNightsAndReleaseOnlyUnused(t *testing.T) {
	for _, terminal := range []string{"CANCELLED", "REJECTED"} {
		t.Run(terminal, func(t *testing.T) {
			f := newFixture(t)
			f.publishAdjudicationRule(t, "INPATIENT_TERMINAL", `serviceCode == "INPATIENT_DAY"`, actionFinancialReview)
			caseID, _, original, extension := f.inpatientStayFixture(t, 6)
			submitted := submitClaim(t, f, inpatientDraft(t, f, caseID, "6"))
			if submitted.Claim.Status != domain.StatusPendingFinancial {
				t.Fatalf("status=%s", submitted.Claim.Status)
			}
			var err error
			if terminal == "CANCELLED" {
				_, err = f.claims.Cancel(context.Background(), f.providerRC(), submitted.Claim.ID,
					application.ReasonInput{ReasonCode: "WITHDRAWN", ExpectedVersion: submitted.Claim.RowVersion})
			} else {
				_, err = f.claims.Reject(context.Background(), f.financialRC(), submitted.Claim.ID,
					application.ReasonInput{ReasonCode: "NOT_PAYABLE", ExpectedVersion: submitted.Claim.RowVersion})
			}
			if err != nil {
				t.Fatal(err)
			}
			if f.consumedTotal(t, original) != "5" || f.consumedTotal(t, extension) != "1" {
				t.Fatal("terminal decision undid actual nights")
			}
			rows := allocationRows(t, f, submitted.Claim.ID, 1)
			if len(rows) != 2 || rows[0].applied != "5.000000" || rows[1].applied != "1.000000" {
				t.Fatal("terminal decision changed receipt")
			}
			if terminal == "REJECTED" {
				_, err = f.claims.GetCaseSource(context.Background(), f.providerRC(), caseID)
				if !errors.Is(err, application.ErrSourceAlreadyClaimed) {
					t.Fatalf("rejected stay became fresh source: %v", err)
				}
			}
		})
	}
}
