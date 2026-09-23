package application_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/claim/application"
	"github.com/celikbros/kapsora/internal/identity"
)

func sourceFixture(t *testing.T) (*fixture, uuid.UUID, uuid.UUID) {
	t.Helper()
	f := newFixture(t)
	authorization := f.authorizeSessions(t, "2")
	f.h.AdminExec(`UPDATE health.health_case SET service_request_id=(SELECT request_id FROM service.authorization WHERE id=$2) WHERE id=$1`, f.caseID, authorization)
	report := uuid.New()
	f.h.AdminExec(`INSERT INTO health.medical_report(id,tenant_id,person_id,case_id,reference,root_report_id,report_type,issuing_provider_organization_id,issued_at,valid_from,valid_to,status,reviewed_by,reviewed_at,submitted_by,submitted_at)
 VALUES($1,$2,$3,$4,'MR-20260615-ABCDEFGH',$1,'FIZIK_TEDAVI',$5,$6,$6,$6,'APPROVED',$7,$8,$7,$8)`, report, f.tenant, f.person, f.caseID, f.provider, serviceDay, f.reviewer, fixtureNow)
	f.h.AdminExec(`INSERT INTO health.medical_report_service(tenant_id,report_id,service_definition_id,covered_quantity) VALUES($1,$2,$3,2)`, f.tenant, report, f.physio)
	return f, authorization, report
}

func TestCaseSourceRetainsLinksAndSerializesCreate(t *testing.T) {
	f, authorization, report := sourceFixture(t)
	ctx := context.Background()
	rc := f.providerRC()
	rows, _, err := f.claims.ListCaseSources(ctx, rc, "", 20)
	if err != nil || len(rows) != 1 {
		t.Fatalf("sources count=%d err=%v", len(rows), err)
	}
	source, err := f.claims.GetCaseSource(ctx, rc, f.caseID)
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Lines) != 1 || source.Lines[0].ReportIDs[0] != report || source.DiagnosisIDs[0] != f.diagnosisID {
		t.Fatal("associations missing")
	}
	charges := []application.CaseCharge{{ServiceID: f.physio, Quantity: "1", LineAmount: "250"}}
	if _, err := f.claims.CreateFromCase(ctx, rc, f.caseID, source.RowVersion+1, charges); !errors.Is(err, application.ErrVersionMismatch) {
		t.Fatalf("stale=%v", err)
	}
	if _, err := f.claims.CreateFromCase(ctx, rc, f.caseID, source.RowVersion, []application.CaseCharge{{ServiceID: f.physio, Quantity: "3", LineAmount: "750"}}); err == nil {
		t.Fatal("excess quantity accepted")
	}
	var wg sync.WaitGroup
	results := make(chan application.ClaimView, 2)
	failures := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		wg.Go(func() {
			<-start
			v, err := f.claims.CreateFromCase(ctx, rc, f.caseID, source.RowVersion, charges)
			if err != nil {
				failures <- err
			} else {
				results <- v
			}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	close(failures)
	if len(results) != 1 || len(failures) != 1 {
		t.Fatalf("created %d errors %d", len(results), len(failures))
	}
	if err := <-failures; !errors.Is(err, application.ErrSourceAlreadyClaimed) {
		t.Fatalf("rival=%v", err)
	}
	draft := <-results
	if draft.Projection != application.ProjectionFinancial || draft.Lines[0].Line.DiagnosisID != nil || draft.Lines[0].Line.MedicalReportID != nil {
		t.Fatal("financial projection leaked clinical references")
	}
	if draft.Claim.AuthorizationID == nil || *draft.Claim.AuthorizationID != authorization {
		t.Fatal("authorization lost")
	}
	clinical, err := f.claims.GetClaim(ctx, f.medicalRC(), draft.Claim.ID, application.AccessRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if *clinical.Lines[0].Line.MedicalReportID != report || *clinical.Lines[0].Line.DiagnosisID != f.diagnosisID {
		t.Fatal("clinical links lost")
	}
	edited, err := f.claims.PutLines(ctx, rc, draft.Claim.ID, []application.NewLineInput{f.physioLine(1, "2", "500")}, draft.Claim.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	clinical, err = f.claims.GetClaim(ctx, f.medicalRC(), draft.Claim.ID, application.AccessRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if *clinical.Lines[0].Line.MedicalReportID != report || *clinical.Lines[0].Line.DiagnosisID != f.diagnosisID {
		t.Fatal("financial edit dropped clinical links")
	}
	if _, err := f.claims.PutLines(ctx, rc, draft.Claim.ID, []application.NewLineInput{f.consultLine(1)}, edited.Claim.RowVersion); err == nil {
		t.Fatal("linked service could be replaced")
	}
	rows, _, err = f.claims.ListCaseSources(ctx, rc, "", 20)
	if err != nil || len(rows) != 0 {
		t.Fatal("claimed case still offered")
	}
}

func TestCaseSourceScopeAndIncompleteRefusal(t *testing.T) {
	f, authorization, report := sourceFixture(t)
	ctx := context.Background()
	rc := f.providerRC()
	other := rc
	other.Scopes = []identity.Scope{{Type: application.ScopeOrganization, ID: uuid.NullUUID{UUID: f.otherOrg, Valid: true}}}
	rows, _, err := f.claims.ListCaseSources(ctx, other, "", 20)
	if err != nil || len(rows) != 0 {
		t.Fatal("other provider listed case")
	}
	if _, err := f.claims.GetCaseSource(ctx, other, f.caseID); !errors.Is(err, application.ErrSourceNotFound) {
		t.Fatalf("other provider detail=%v", err)
	}
	if _, err := f.claims.CreateFromCase(ctx, other, f.caseID, 1, []application.CaseCharge{{ServiceID: f.physio, Quantity: "1", LineAmount: "250"}}); !errors.Is(err, application.ErrSourceNotFound) {
		t.Fatalf("other provider create=%v", err)
	}
	otherTenant := rc
	otherTenant.TenantID = uuid.New()
	if _, err := f.claims.GetCaseSource(ctx, otherTenant, f.caseID); !errors.Is(err, application.ErrSourceNotFound) {
		t.Fatalf("other tenant=%v", err)
	}
	noPermission := rc
	noPermission.Permissions = map[string]struct{}{}
	if _, err := f.claims.GetCaseSource(ctx, noPermission, f.caseID); err == nil {
		t.Fatal("missing permission accepted")
	}
	f.h.AdminExec(`UPDATE health.encounter SET ended_at=NULL WHERE id=$1`, f.encounterID)
	if _, err := f.claims.GetCaseSource(ctx, rc, f.caseID); !errors.Is(err, application.ErrSourceNotReady) {
		t.Fatalf("ongoing=%v", err)
	}
	f.h.AdminExec(`UPDATE health.encounter SET ended_at=$2 WHERE id=$1`, f.encounterID, serviceDay)
	f.h.AdminExec(`UPDATE health.medical_report SET status='EXPIRED' WHERE id=$1`, report)
	if _, err := f.claims.GetCaseSource(ctx, rc, f.caseID); !errors.Is(err, application.ErrSourceNotReady) {
		t.Fatalf("unapproved report=%v", err)
	}
	f.h.AdminExec(`UPDATE health.medical_report SET status='APPROVED',valid_from=$2,valid_to=$2 WHERE id=$1`, report, serviceDay.AddDate(0, 0, 1))
	if _, err := f.claims.GetCaseSource(ctx, rc, f.caseID); !errors.Is(err, application.ErrSourceNotReady) {
		t.Fatalf("out-of-window=%v", err)
	}
	f.h.AdminExec(`UPDATE service.authorization SET status='EXPIRED' WHERE id=$1`, authorization)
	if _, err := f.claims.GetCaseSource(ctx, rc, f.caseID); !errors.Is(err, application.ErrSourceNotReady) {
		t.Fatalf("expired authorization=%v", err)
	}
}
