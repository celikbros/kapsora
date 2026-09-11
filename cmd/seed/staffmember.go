package main

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	documentapp "github.com/celikbros/kapsora/internal/document/application"
	documentdomain "github.com/celikbros/kapsora/internal/document/domain"
	healthapp "github.com/celikbros/kapsora/internal/health/application"
	healthdomain "github.com/celikbros/kapsora/internal/health/domain"
	"github.com/celikbros/kapsora/internal/identity"
	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	servicerequestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
	servicerequestdomain "github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// The demo staff member: one account that is a medical reviewer at work and a member of the
// benefit plan at home. It is the account the mock world calls `staff.member`, and it exists to
// show the one thing no other demo account can — that the grants an account holds are applied
// by the app a request comes from (X-Kapsora-App), so the same login decides files in the back
// office and sees only her own file in the member app.
//
// She is a second person rather than a second account of Melis: one person may be bound to only
// one account (migration 000039), and a reviewer who was the demo member would be a reviewer
// deciding her own reimbursement.
//
// The identifier is a synthetic TCKN in the same spirit as demoMemberTCKN: the first nine digits
// 200000000 followed by the two check digits the TCKN algorithm gives them (4 and 6). It passes
// the checksum and belongs to nobody.
const (
	staffMemberUsername  = "staff.member"
	staffMemberFirstName = "Deniz"
	staffMemberLastName  = "Çalışan"
	staffMemberTCKN      = "20000000046"
)

// staffReportType is the medical report the staff member's provider files for her. It is a
// physiotherapy report because that is the report a PHYSIO_SESSION line is covered by.
const staffReportType = "FIZIK_TEDAVI"

// ensureStaffMember creates the staff member's person and account and gives the account its two
// grants in the tenant: MEDICAL_REVIEWER for the whole tenant, and MEMBER with a PERSON scope
// naming her own person. It returns the account's actor.
func (s *seeder) ensureStaffMember(ctx context.Context, tenantID uuid.UUID, demoPassword string,
) (uuid.UUID, error) {
	personID, err := s.ensurePerson(ctx, identity.RequestContext{TenantID: tenantID},
		staffMemberFirstName, staffMemberLastName, staffMemberTCKN)
	if err != nil {
		return uuid.Nil, err
	}
	actorID, err := s.ensureAccount(ctx, staffMemberUsername,
		staffMemberFirstName+" "+staffMemberLastName, staffMemberUsername+"@demo.test", demoPassword)
	if err != nil {
		return uuid.Nil, err
	}
	reviewer, err := s.provisioner.GrantRole(ctx, identityapp.GrantRoleInput{
		TenantID: tenantID, ActorID: actorID, RoleCode: "MEDICAL_REVIEWER",
		Reason: "seed demo staff member",
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("grant %s MEDICAL_REVIEWER: %w", staffMemberUsername, err)
	}
	fmt.Printf("grant   %-22s %s in DEMO_A (%s)\n", "MEDICAL_REVIEWER",
		created(reviewer, "granted", "exists"), staffMemberUsername)
	member, err := s.provisioner.GrantRole(ctx, identityapp.GrantRoleInput{
		TenantID: tenantID, ActorID: actorID, RoleCode: "MEMBER",
		ScopeType: identityapp.ScopePerson,
		ScopeID:   uuid.NullUUID{UUID: personID, Valid: true},
		Reason:    "seed demo staff member binding",
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("bind %s to a person: %w", staffMemberUsername, err)
	}
	fmt.Printf("member  %-22s %s (person %s)\n", staffMemberUsername,
		created(member, "granted", "exists"), personID)
	return actorID, nil
}

// ensureStaffMemberFiles enrols the staff member in the demo plan and leaves two files of hers
// waiting for a medical reviewer: a service request in PENDING_REVIEW and a medical report in
// SUBMITTED. Both are raised by the hospital's clinic desk (`provider.a`), which is who raises
// them in production, and both are decided in the back office by a MEDICAL_REVIEWER.
func (s *seeder) ensureStaffMemberFiles(ctx context.Context, sc *scenario) error {
	s.clock.at = time.Now().UTC()
	personID, err := s.ensurePerson(ctx, identity.RequestContext{TenantID: sc.tenant},
		staffMemberFirstName, staffMemberLastName, staffMemberTCKN)
	if err != nil {
		return err
	}
	_, enrollmentID, err := s.ensurePersonEnrollment(ctx, sc, personID, staffMemberUsername)
	if err != nil {
		return err
	}
	if err := s.ensureStaffMemberRequest(ctx, sc, personID, enrollmentID); err != nil {
		return err
	}
	return s.ensureStaffMemberReport(ctx, sc, personID)
}

// ensureStaffMemberRequest raises an MRI the hospital asks the payer to approve for the staff
// member. Nothing here chooses its status: the gate finds her eligible, no rule objects, and the
// tenant carries no `service_request.review_required` setting — whose absence means a person
// reviews it — so the request lands in PENDING_REVIEW by the same path a real one does.
func (s *seeder) ensureStaffMemberRequest(ctx context.Context, sc *scenario,
	personID, enrollmentID uuid.UUID,
) error {
	rc := rcOrganization(sc.tenant, sc.provider, sc.hospitalOrg, servicerequestapp.PermissionRead,
		servicerequestapp.PermissionCreate, servicerequestapp.PermissionSubmit)
	provider := sc.hospitalOrg
	page, err := s.biz.requests.List(ctx, rc, servicerequestapp.ListFilter{
		PersonID: &personID, ProviderOrganizationID: &provider, Limit: 50,
	})
	if err != nil {
		return fmt.Errorf("list the requests of %s: %w", staffMemberUsername, err)
	}
	var request servicerequestapp.RequestRecord
	for _, item := range page.Items {
		if item.Request.RequestType == "DIRECT_SERVICE" {
			request = item.Request
			break
		}
	}
	if request.ID == uuid.Nil {
		draft, err := s.biz.requests.Create(ctx, rc, servicerequestapp.NewRequestInput{
			RequestType: "DIRECT_SERVICE", PersonID: personID, ProgramID: sc.program,
			EnrollmentID: enrollmentID, ProviderOrganizationID: &provider,
			ServiceDate: day(s.clock.at), Channel: "PROVIDER_PORTAL",
			Items: []servicerequestdomain.ItemInput{{
				ServiceDefinitionID: sc.services[serviceMRI].String(),
				RequestedQuantity:   "1", UnitType: "COUNT",
				RequestedAmount: priceMRI, CurrencyCode: "TRY",
			}},
		})
		if err != nil {
			return fmt.Errorf("raise the request of %s: %w", staffMemberUsername, err)
		}
		request = draft.Request
	}
	// A draft left behind by a run that stopped between the create and the submit is handed on,
	// not duplicated.
	if request.Status == servicerequestdomain.StatusDraft {
		submitted, err := s.biz.requests.Submit(ctx, rc, request.ID, nil, request.RowVersion)
		if err != nil {
			return fmt.Errorf("submit the request of %s: %w", staffMemberUsername, err)
		}
		request = submitted.Request
		if request.Status != servicerequestdomain.StatusPendingReview {
			return fmt.Errorf("the request of %s came out of the gate %s; the medical reviewer "+
				"would have nothing to decide (is service_request.review_required set to false?)",
				staffMemberUsername, request.Status)
		}
		step("request", staffMemberUsername, request.Status+" (submitted)")
		return nil
	}
	step("request", staffMemberUsername, request.Status)
	return nil
}

// ensureStaffMemberReport files a physiotherapy report for the staff member — header, one
// covered service line and a scanned-clean report document — and submits it, which is what puts
// it in the MEDICAL_REVIEW queue as SUBMITTED.
//
//nolint:funlen // one linear filing sequence reads better whole
func (s *seeder) ensureStaffMemberReport(ctx context.Context, sc *scenario, personID uuid.UUID,
) error {
	rc := rcOrganization(sc.tenant, sc.provider, sc.hospitalOrg,
		healthapp.PermissionReportManage, healthapp.PermissionCaseRead,
		healthapp.PermissionClinicalRead, documentapp.PermissionRead, documentapp.PermissionLink)
	// The financial projection is enough to find the report and read its status, and it records
	// no clinical access: a second seed run should not look like somebody reading her file.
	page, err := s.biz.health.ListReports(ctx, rc, healthapp.ReportFilter{
		PersonID: &personID, Limit: 20,
	}, healthapp.AccessRequest{FinancialOnly: true})
	if err != nil {
		return fmt.Errorf("list the reports of %s: %w", staffMemberUsername, err)
	}
	var view healthapp.ReportView
	switch {
	case len(page.Items) > 0 && page.Items[0].Report.Status != healthdomain.ReportStatusDraft:
		step("report", staffMemberUsername, page.Items[0].Report.Status)
		return nil
	case len(page.Items) > 0:
		// A draft left behind by a run that stopped halfway; finish it.
		if view, err = s.biz.health.GetReport(ctx, rc, page.Items[0].Report.ID,
			healthapp.AccessRequest{}); err != nil {
			return fmt.Errorf("read the draft report of %s: %w", staffMemberUsername, err)
		}
	default:
		issued := day(s.clock.at)
		validTo := issued.AddDate(0, 3, 0)
		hospital := sc.hospitalOrg
		summary := "Demo veri: bel ağrısı nedeniyle 10 seans fizik tedavi önerilir."
		if view, err = s.biz.health.CreateReport(ctx, rc, healthapp.NewReportInput{
			PersonID: personID, ReportType: staffReportType,
			IssuingProviderOrganizationID: &hospital,
			IssuedAt:                      &issued, ValidFrom: &issued, ValidTo: &validTo,
			ClinicalSummary: &summary,
		}); err != nil {
			return fmt.Errorf("file the report of %s: %w", staffMemberUsername, err)
		}
	}
	reportID := view.Report.ID

	if len(view.Services) == 0 {
		sessions := "10"
		if view, err = s.biz.health.PutReportServices(ctx, rc, reportID,
			[]healthdomain.ReportServiceInput{{
				ServiceDefinitionID: sc.services[servicePhysio].String(), CoveredQuantity: &sessions,
			}}, view.Report.RowVersion); err != nil {
			return fmt.Errorf("write the services of the report of %s: %w", staffMemberUsername, err)
		}
	}
	if len(view.Documents) == 0 {
		documentID, err := s.storeDemoDocument(ctx, sc.tenant, sc.hospitalOrg,
			"saglik-raporu-"+view.Report.Reference+".pdf", documentdomain.ClassHealth,
			"KAPSORA demo saglik raporu "+view.Report.Reference)
		if err != nil {
			return err
		}
		// The link names health.clinical.read, so the report file is downloadable by the
		// medical reviewer and by nobody who may merely read documents.
		if _, err := s.biz.documents.LinkDocument(ctx, rc, documentID, documentapp.NewLinkInput{
			AggregateType: healthdomain.AggregateMedicalReport, AggregateID: reportID,
			DocumentTypeCode: "MEDICAL_REPORT", Purpose: "Demo sağlık raporu",
			RequiredPermission: healthapp.PermissionClinicalRead,
		}); err != nil {
			return fmt.Errorf("attach the report file of %s: %w", staffMemberUsername, err)
		}
	}
	submitted, err := s.biz.health.SubmitReport(ctx, rc, reportID, view.Report.RowVersion)
	if err != nil {
		return fmt.Errorf("submit the report of %s: %w", staffMemberUsername, err)
	}
	step("report", staffMemberUsername, submitted.Report.Status+" (submitted)")
	return nil
}
