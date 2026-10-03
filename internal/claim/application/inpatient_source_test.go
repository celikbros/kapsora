package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/claim/application"
	"github.com/celikbros/kapsora/internal/claim/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

func TestInpatientSourceUsesActualDaysAndRetainsPrivateLinks(t *testing.T) {
	f := newFixture(t)
	caseID, stayID, originalID, extensionID := f.inpatientStayFixture(t, 2)
	ctx := context.Background()
	rc := f.providerRC()
	// A second ended encounter must not make the admission's explicit diagnosis ambiguous.
	f.h.AdminExec(`WITH encounter AS (
  INSERT INTO health.encounter(tenant_id,case_id,encounter_type,started_at,ended_at)
  VALUES($1,$2,'INPATIENT',$3,$3) RETURNING id)
  INSERT INTO health.diagnosis(tenant_id,encounter_id,code_system_id,code_value_id,diagnosis_type,sensitive)
  SELECT d.tenant_id,e.id,d.code_system_id,d.code_value_id,'PRIMARY',false
  FROM health.diagnosis d CROSS JOIN encounter e WHERE d.id=$4`, f.tenant, caseID, serviceDay, f.diagnosisID)
	source, err := f.claims.GetCaseSource(ctx, rc, caseID)
	if err != nil {
		t.Fatal(err)
	}
	if source.StayID == nil || *source.StayID != stayID || source.DischargeAt == nil || len(source.Lines) != 1 ||
		source.Lines[0].Quantity != "2.000000" || source.Lines[0].Code != "INPATIENT_DAY" || len(source.DiagnosisIDs) != 1 {
		t.Fatalf("inpatient source is incomplete: %+v", source)
	}
	if !source.ServiceDate.Before(*source.DischargeAt) {
		t.Fatal("source dates do not describe the admission")
	}
	rows, _, err := f.claims.ListCaseSources(ctx, rc, "", 20)
	if err != nil || len(rows) != 1 || rows[0].ID != caseID {
		t.Fatalf("sources=%v err=%v", rows, err)
	}
	charges := []application.CaseCharge{{ServiceID: f.inpatient, Quantity: "2", LineAmount: "800"}}
	if _, err = f.claims.CreateFromCase(ctx, rc, caseID, source.RowVersion+1, charges); !errors.Is(err, application.ErrVersionMismatch) {
		t.Fatalf("stale=%v", err)
	}
	if _, err = f.claims.CreateFromCase(ctx, rc, caseID, source.RowVersion, []application.CaseCharge{{ServiceID: f.inpatient, Quantity: "1", LineAmount: "400"}}); err == nil {
		t.Fatal("partial inpatient billing accepted")
	}
	draft, err := f.claims.CreateFromCase(ctx, rc, caseID, source.RowVersion, charges)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Claim.SourceType == nil || *draft.Claim.SourceType != "INPATIENT_STAY" || draft.Claim.SourceID == nil || *draft.Claim.SourceID != stayID {
		t.Fatal("exact stay source not retained")
	}
	if draft.Claim.AuthorizationID == nil || *draft.Claim.AuthorizationID != originalID {
		t.Fatal("original hold not retained")
	}
	if draft.Projection != application.ProjectionFinancial || draft.Lines[0].Line.DiagnosisID != nil || draft.Lines[0].Line.MedicalReportID != nil {
		t.Fatal("clinical references exposed")
	}
	if _, err = f.claims.CreateFromCase(ctx, rc, caseID, source.RowVersion, charges); !errors.Is(err, application.ErrSourceAlreadyClaimed) {
		t.Fatalf("duplicate=%v", err)
	}
	clinical, err := f.claims.GetClaim(ctx, f.medicalRC(), draft.Claim.ID, application.AccessRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if clinical.Lines[0].Line.DiagnosisID == nil || *clinical.Lines[0].Line.DiagnosisID != source.DiagnosisIDs[0] {
		t.Fatal("clinical diagnosis lost")
	}
	submitted := submitClaim(t, f, draft)
	if submitted.Claim.Status != domain.StatusApproved {
		t.Fatalf("status=%s", submitted.Claim.Status)
	}
	ready, err := f.claims.InvoiceReadiness(ctx, f.financialRC(), submitted.Claim.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ready.Ready || ready.ApprovedTotal != "800" || ready.PayerTotal != "800" || ready.MemberTotal != "0" {
		t.Fatalf("readiness=%+v", ready)
	}
	if got := f.consumedTotal(t, originalID); got != "2" {
		t.Fatalf("original consumption=%s", got)
	}
	if got := f.consumedTotal(t, extensionID); got != "0" {
		t.Fatalf("extension consumption=%s", got)
	}
	f.ledgerConserved(t)
}

func TestInpatientSourceRejectsForeignAndUndischargedCases(t *testing.T) {
	f := newFixture(t)
	caseID, stayID, _, _ := f.inpatientStayFixture(t, 6)
	ctx := context.Background()
	rc := f.providerRC()
	other := rc
	other.Scopes = []identity.Scope{{Type: application.ScopeOrganization, ID: uuid.NullUUID{UUID: f.otherOrg, Valid: true}}}
	for _, foreign := range []identity.RequestContext{other, {TenantID: uuid.New(), Principal: rc.Principal, Permissions: rc.Permissions, Scopes: rc.Scopes}} {
		rows, _, err := f.claims.ListCaseSources(ctx, foreign, "", 20)
		if err != nil || len(rows) != 0 {
			t.Fatalf("foreign sources=%v err=%v", rows, err)
		}
		if _, err = f.claims.GetCaseSource(ctx, foreign, caseID); !errors.Is(err, application.ErrSourceNotFound) {
			t.Fatalf("foreign detail=%v", err)
		}
		if _, err = f.claims.CreateFromCase(ctx, foreign, caseID, 1, []application.CaseCharge{{ServiceID: f.inpatient, Quantity: "6", LineAmount: "2400"}}); !errors.Is(err, application.ErrSourceNotFound) {
			t.Fatalf("foreign create=%v", err)
		}
	}
	source, err := f.claims.GetCaseSource(ctx, rc, caseID)
	if err != nil || len(source.Lines) != 1 || source.Lines[0].Quantity != "6.000000" {
		t.Fatalf("split hold source=%+v err=%v", source, err)
	}
	// Isolated database fixture: roll the stay back to an active admission to exercise read refusal.
	f.h.AdminExec(`UPDATE health.inpatient_stay SET status='ADMITTED',discharge_at=NULL,actual_days=NULL,released_days=NULL WHERE id=$1`, stayID)
	if _, err = f.claims.GetCaseSource(ctx, rc, caseID); !errors.Is(err, application.ErrSourceNotFound) {
		t.Fatalf("undischarged detail=%v", err)
	}
	rows, _, err := f.claims.ListCaseSources(ctx, rc, "", 20)
	if err != nil || len(rows) != 0 {
		t.Fatalf("undischarged sources=%v err=%v", rows, err)
	}
}
