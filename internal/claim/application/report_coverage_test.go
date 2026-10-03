package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/celikbros/kapsora/internal/claim/application"
	"github.com/celikbros/kapsora/internal/claim/domain"
)

// The coverage port's refusal must survive the whole claim pipeline: route to medicine,
// keep the report's usage trace empty and refuse invoice readiness until reviewed.
func TestReportCoverageExceptionsBlockAutomaticClaimApproval(t *testing.T) {
	for _, scenario := range []struct{ name, code string }{
		{"approved", ""}, {"future", "REPORT_OUT_OF_WINDOW"},
		{"past", "REPORT_OUT_OF_WINDOW"}, {"unapproved", "REPORT_NOT_APPROVED"},
		{"superseded", "REPORT_NOT_APPROVED"}, {"other_service", "SERVICE_NOT_IN_REPORT"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f, _, report := sourceFixture(t)
			switch scenario.name {
			case "future":
				f.h.AdminExec(`UPDATE health.medical_report SET valid_from=$2,valid_to=$2 WHERE id=$1`, report, serviceDay.AddDate(0, 0, 1))
			case "past":
				f.h.AdminExec(`UPDATE health.medical_report SET valid_from=$2,valid_to=$2 WHERE id=$1`, report, serviceDay.AddDate(0, 0, -1))
			case "unapproved":
				f.h.AdminExec(`UPDATE health.medical_report SET status='REJECTED',reject_reason_code='TEST' WHERE id=$1`, report)
			case "superseded":
				f.h.AdminExec(`UPDATE health.medical_report SET status='SUPERSEDED' WHERE id=$1`, report)
			case "other_service":
				f.h.AdminExec(`UPDATE health.medical_report_service SET service_definition_id=$2 WHERE report_id=$1`, report, f.lab)
			}
			line := f.physioLine(1, "1", "250")
			line.MedicalReportID = &report
			submitted := submitClaim(t, f, f.newClaim(t, []application.NewLineInput{line}, nil))
			var wantUsage int
			if scenario.code == "" {
				wantUsage = 1
				if submitted.Claim.Status != domain.StatusApproved {
					t.Fatalf("usable report status=%s", submitted.Claim.Status)
				}
			} else {
				if submitted.Claim.Status != domain.StatusPendingMedical {
					t.Fatalf("refused coverage status=%s", submitted.Claim.Status)
				}
				clinical, err := f.claims.GetClaim(context.Background(), f.medicalRC(), submitted.Claim.ID, application.AccessRequest{PurposeCode: "MEDICAL_REVIEW", ReasonText: "review"})
				if err != nil {
					t.Fatal(err)
				}
				if _, found := exceptionByCode(submitted, scenario.code); found {
					t.Fatal("clinical exception leaked to financial projection")
				}
				if exception, found := exceptionByCode(clinical, scenario.code); !found || exception.Stage != domain.StageMedical {
					t.Fatalf("missing medical exception %s: %+v", scenario.code, clinical.Exceptions)
				}
				if _, err := f.claims.InvoiceReadiness(context.Background(), f.financialRC(), submitted.Claim.ID); !errors.Is(err, application.ErrNotDecided) {
					t.Fatalf("readiness=%v", err)
				}
			}
			var usages int
			if err := f.h.Admin.QueryRow(context.Background(), `SELECT count(*) FROM health.medical_report_usage WHERE tenant_id=$1 AND report_id=$2`, f.tenant, report).Scan(&usages); err != nil {
				t.Fatal(err)
			}
			if usages != wantUsage {
				t.Fatalf("usage=%d want=%d", usages, wantUsage)
			}
			f.ledgerConserved(t)
		})
	}
}
