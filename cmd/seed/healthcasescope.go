package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	claimapp "github.com/celikbros/kapsora/internal/claim/application"
	claimpg "github.com/celikbros/kapsora/internal/claim/infrastructure/postgres"
	healthapp "github.com/celikbros/kapsora/internal/health/application"
	healthpg "github.com/celikbros/kapsora/internal/health/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	requestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
	requestdomain "github.com/celikbros/kapsora/internal/servicerequest/domain"
	requestpg "github.com/celikbros/kapsora/internal/servicerequest/infrastructure/postgres"
)

type healthCaseScope struct {
	RequestID        uuid.UUID `json:"requestId"`
	RequestVersion   int64     `json:"requestVersion"`
	ProviderID       uuid.UUID `json:"providerId"`
	CaseID           uuid.UUID `json:"caseId"`
	EncounterID      uuid.UUID `json:"encounterId"`
	ClaimID          uuid.UUID `json:"claimId"`
	CaseVersion      int64     `json:"caseVersion"`
	EncounterVersion int64     `json:"encounterVersion"`
	ClaimVersion     int64     `json:"claimVersion"`
}

// healthCaseBoundary creates a closed case and cancelled draft at a dedicated
// provider, plus a cancelled request. It reuses only the synthetic outpatient enrollment; it never submits
// a claim, reserves entitlement, assigns permissions or changes the source episode.
func (s *seeder) healthCaseBoundary(ctx context.Context, sourceID, fixture uuid.UUID) (healthCaseScope, error) {
	var out healthCaseScope
	if sourceID == uuid.Nil || fixture == uuid.Nil {
		return out, fmt.Errorf("nonzero source and fixture UUIDs required")
	}
	tenant, err := sqlcgen.New(s.pool).GetTenantIDByCode(ctx, "DEMO_A")
	if err != nil {
		return out, err
	}
	rc := rcTenant(tenant, uuid.Nil, "health.clinical.read")
	cursors, err := httpx.NewCursorCodec(cursorKey)
	if err != nil {
		return out, err
	}
	health, err := healthapp.New(healthapp.Deps{
		Pool: s.pool, Repo: healthpg.New(), Reports: healthpg.NewReports(),
		Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		return out, err
	}
	claims, err := claimapp.New(claimapp.Deps{Pool: s.pool, Repo: claimpg.New(), Audit: auditpg.New(), Cursors: cursors})
	if err != nil {
		return out, err
	}
	source, err := claims.GetClaim(ctx, rc, sourceID, claimapp.AccessRequest{})
	if err != nil {
		return out, err
	}
	if source.Claim.Status != "APPROVED" || source.Claim.CaseID == nil || len(source.Lines) != 1 {
		return out, fmt.Errorf("source must be approved single-line outpatient claim")
	}
	person, err := s.party.Get(ctx, rc, source.Claim.PersonID)
	if err != nil {
		return out, err
	}
	if person.DisplayName != "Deneme Ayaktan" {
		return out, fmt.Errorf("source must belong to synthetic outpatient person")
	}
	original, err := health.GetCase(ctx, rc, *source.Claim.CaseID, healthapp.AccessRequest{})
	if err != nil {
		return out, err
	}
	if original.Case.Status != "CLOSED" || original.Case.CaseType != "OUTPATIENT" {
		return out, fmt.Errorf("source case must be closed outpatient")
	}
	marker := "Synthetic case scope " + fixture.String()
	provider, err := s.provisioner.EnsureProviderOrganization(ctx, tenant, "PC03_CASE_"+strings.ReplaceAll(strings.ToUpper(fixture.String()), "-", ""), marker)
	if err != nil {
		return out, err
	}
	if provider == source.Claim.ProviderOrganizationID {
		return out, fmt.Errorf("fixture provider must differ from source")
	}
	cases, err := health.ListCases(ctx, rc, healthapp.ListFilter{ProviderOrganizationID: &provider, Limit: 2}, healthapp.AccessRequest{})
	if err != nil {
		return out, err
	}
	var record healthapp.CaseView
	switch len(cases.Items) {
	case 0:
		record, err = health.CreateCase(ctx, rc, healthapp.NewCaseInput{
			PersonID: source.Claim.PersonID, EnrollmentID: source.Claim.EnrollmentID,
			CaseType: "OUTPATIENT", ProviderOrganizationID: &provider,
		})
	case 1:
		record = cases.Items[0]
	default:
		return out, fmt.Errorf("ambiguous scope case")
	}
	if err != nil {
		return out, err
	}
	if record.Case.PersonID != source.Claim.PersonID || record.Case.EnrollmentID != source.Claim.EnrollmentID {
		return out, fmt.Errorf("unexpected scope case")
	}
	if len(record.Encounters) == 0 && record.Case.Status == "OPEN" {
		ended := time.Now().UTC()
		_, err = health.CreateEncounter(ctx, rc, healthapp.NewEncounterInput{
			CaseID: record.Case.ID, EncounterType: "OUTPATIENT",
			StartedAt: ended.Add(-time.Hour), EndedAt: &ended, NotesClinical: &marker,
		})
		if err != nil {
			return out, err
		}
		record, err = health.GetCase(ctx, rc, record.Case.ID, healthapp.AccessRequest{})
		if err != nil {
			return out, err
		}
	}
	if len(record.Encounters) != 1 || record.Encounters[0].NotesClinical == nil || *record.Encounters[0].NotesClinical != marker || record.Encounters[0].EndedAt == nil {
		return out, fmt.Errorf("unexpected scope encounter")
	}
	if record.Case.Status == "OPEN" {
		record, err = health.CloseCase(ctx, rc, record.Case.ID, &marker, record.Case.RowVersion, healthapp.AccessRequest{})
		if err != nil {
			return out, err
		}
	}
	if record.Case.Status != "CLOSED" {
		return out, fmt.Errorf("scope case must be closed")
	}
	page, err := claims.ListClaims(ctx, rc, claimapp.ClaimFilter{CaseID: &record.Case.ID, Limit: 2}, claimapp.AccessRequest{})
	if err != nil {
		return out, err
	}
	var claim claimapp.ClaimView
	switch len(page.Items) {
	case 0:
		line := source.Lines[0].Line
		claim, err = claims.CreateClaim(ctx, rc, claimapp.NewClaimInput{
			PersonID: source.Claim.PersonID, ProgramID: source.Claim.ProgramID,
			EnrollmentID: source.Claim.EnrollmentID, ProviderOrganizationID: provider,
			CaseID: &record.Case.ID, Channel: "PROVIDER_PORTAL",
			ServiceDateFrom: source.Claim.ServiceDateFrom, ServiceDateTo: source.Claim.ServiceDateTo,
			Lines: []claimapp.NewLineInput{{
				LineNo: 1, ServiceDefinitionID: line.ServiceDefinitionID, UnitType: line.UnitType,
				Quantity: "1", LineAmount: line.LineAmount, CurrencyCode: &line.CurrencyCode, Description: &marker,
			}},
		}, claimapp.AccessRequest{})
	case 1:
		claim = page.Items[0]
	default:
		return out, fmt.Errorf("ambiguous scope claim")
	}
	if err != nil {
		return out, err
	}
	if claim.Claim.ProviderOrganizationID != provider || len(claim.Lines) != 1 || claim.Lines[0].Line.Description == nil || *claim.Lines[0].Line.Description != marker {
		return out, fmt.Errorf("unexpected scope claim")
	}
	if claim.Claim.Status == "DRAFT" {
		claim, err = claims.Cancel(ctx, rc, claim.Claim.ID, claimapp.ReasonInput{ReasonCode: "PC03_SCOPE_FIXTURE", ExpectedVersion: claim.Claim.RowVersion})
		if err != nil {
			return out, err
		}
	}
	if claim.Claim.Status != "CANCELLED" {
		return out, fmt.Errorf("scope claim must be cancelled")
	}
	requests, err := requestapp.New(requestapp.Deps{Pool: s.pool, Repo: requestpg.New(), Audit: auditpg.New(), Cursors: cursors})
	if err != nil {
		return out, err
	}
	requestPage, err := requests.List(ctx, rc, requestapp.ListFilter{ProviderOrganizationID: &provider, Limit: 2})
	if err != nil {
		return out, err
	}
	line := source.Lines[0].Line
	var request requestapp.RequestView
	switch len(requestPage.Items) {
	case 0:
		request, err = requests.Create(ctx, rc, requestapp.NewRequestInput{
			RequestType: "PREAUTHORIZATION", PersonID: source.Claim.PersonID,
			ProgramID: source.Claim.ProgramID, EnrollmentID: source.Claim.EnrollmentID,
			ProviderOrganizationID: &provider, ServiceDate: source.Claim.ServiceDateFrom,
			Channel: "PROVIDER_PORTAL", Items: []requestdomain.ItemInput{{
				ServiceDefinitionID: line.ServiceDefinitionID.String(), RequestedQuantity: "1", UnitType: line.UnitType,
			}},
		})
	case 1:
		request = requestPage.Items[0]
	default:
		return out, fmt.Errorf("ambiguous scope request")
	}
	if err != nil {
		return out, err
	}
	if request.Request.PersonID != source.Claim.PersonID || request.Request.EnrollmentID != source.Claim.EnrollmentID ||
		request.Request.RequestType != "PREAUTHORIZATION" || len(request.Items) != 1 || request.Items[0].ServiceDefinitionID != line.ServiceDefinitionID {
		return out, fmt.Errorf("unexpected scope request")
	}
	if request.Request.Status == "DRAFT" {
		request, err = requests.Cancel(ctx, rc, request.Request.ID, requestapp.ReasonInput{ReasonCode: "PC02_SCOPE_FIXTURE", ExpectedVersion: request.Request.RowVersion})
		if err != nil {
			return out, err
		}
	}
	if request.Request.Status != "CANCELLED" {
		return out, fmt.Errorf("scope request must be cancelled")
	}
	return healthCaseScope{
		RequestID: request.Request.ID, RequestVersion: request.Request.RowVersion,
		ProviderID: provider, CaseID: record.Case.ID, EncounterID: record.Encounters[0].ID, ClaimID: claim.Claim.ID,
		CaseVersion: record.Case.RowVersion, EncounterVersion: record.Encounters[0].RowVersion, ClaimVersion: claim.Claim.RowVersion,
	}, nil
}
