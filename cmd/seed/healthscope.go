package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	healthapp "github.com/celikbros/kapsora/internal/health/application"
	healthpg "github.com/celikbros/kapsora/internal/health/infrastructure/postgres"
	partyapp "github.com/celikbros/kapsora/internal/party/application"
	partydomain "github.com/celikbros/kapsora/internal/party/domain"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

type healthScopeReport struct {
	TenantID   uuid.UUID `json:"tenantId"`
	PersonID   uuid.UUID `json:"personId"`
	ProviderID uuid.UUID `json:"providerId"`
	ReportID   uuid.UUID `json:"reportId"`
	RowVersion int64     `json:"rowVersion"`
	Status     string    `json:"status"`
}

// healthScope creates closed, unfunded reports in both demo tenants through the
// application services. It never changes a login, role, enrollment or existing case.
// The UUID names a dedicated fixture; repeating it only reads the closed reports.
func (s *seeder) healthScope(ctx context.Context, fixture uuid.UUID) ([]healthScopeReport, error) {
	if fixture == uuid.Nil {
		return nil, fmt.Errorf("health scope fixture needs a nonzero UUID")
	}
	cursors, err := httpx.NewCursorCodec(cursorKey)
	if err != nil {
		return nil, err
	}
	svc, err := healthapp.New(healthapp.Deps{
		Pool: s.pool, Repo: healthpg.New(), Reports: healthpg.NewReports(),
		Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		return nil, err
	}
	// Resolve both tenants before creating anything. This command does not bootstrap demo.
	tenants := make([]uuid.UUID, 0, 2)
	for _, code := range []string{"DEMO_A", "DEMO_B"} {
		id, err := sqlcgen.New(s.pool).GetTenantIDByCode(ctx, code)
		if err != nil {
			return nil, fmt.Errorf("health scope needs existing %s: %w", code, err)
		}
		tenants = append(tenants, id)
	}
	marker := strings.ReplaceAll(fixture.String(), "-", "")
	lastName := "Kapsam" + marker
	summary := "Synthetic scope acceptance " + fixture.String()
	var out []healthScopeReport
	for _, tenant := range tenants {
		rc := rcTenant(tenant, uuid.Nil, "health.clinical.read")
		provider, err := s.provisioner.EnsureProviderOrganization(ctx, tenant,
			"PC03_SCOPE_"+strings.ToUpper(marker), "Synthetic scope provider "+marker)
		if err != nil {
			return nil, err
		}
		people, err := s.party.List(ctx, rc, partyapp.ListFilter{Query: lastName, Limit: 2})
		if err != nil {
			return nil, err
		}
		var personID uuid.UUID
		switch len(people.Items) {
		case 0:
			person, err := s.party.Create(ctx, rc, partydomain.NewPerson{FirstName: "Deneme", LastName: lastName})
			if err != nil {
				return nil, err
			}
			personID = person.ID
		case 1:
			if people.Items[0].DisplayName != "Deneme "+lastName {
				return nil, fmt.Errorf("unexpected scope fixture person")
			}
			personID = people.Items[0].ID
		default:
			return nil, fmt.Errorf("ambiguous scope fixture person")
		}
		reports, err := svc.ListReports(ctx, rc, healthapp.ReportFilter{PersonID: &personID, Limit: 2}, healthapp.AccessRequest{})
		if err != nil {
			return nil, err
		}
		var view healthapp.ReportView
		switch len(reports.Items) {
		case 0:
			today := time.Now().UTC().Truncate(24 * time.Hour)
			view, err = svc.CreateReport(ctx, rc, healthapp.NewReportInput{
				PersonID: personID, IssuingProviderOrganizationID: &provider,
				ReportType: "PC03_SCOPE", ClinicalSummary: &summary,
				IssuedAt: &today, ValidFrom: &today, ValidTo: &today,
			})
		case 1:
			view = reports.Items[0]
		default:
			return nil, fmt.Errorf("ambiguous scope fixture report")
		}
		if err != nil {
			return nil, err
		}
		if view.Report.IssuingProviderOrganizationID == nil || *view.Report.IssuingProviderOrganizationID != provider ||
			view.Report.ClinicalSummary == nil || *view.Report.ClinicalSummary != summary || view.Report.ReportType != "PC03_SCOPE" {
			return nil, fmt.Errorf("unexpected scope fixture report")
		}
		if view.Report.Status == "DRAFT" {
			view, err = svc.CancelReport(ctx, rc, view.Report.ID, view.Report.RowVersion)
			if err != nil {
				return nil, err
			}
		}
		if view.Report.Status != "CANCELLED" {
			return nil, fmt.Errorf("scope report must be closed")
		}
		out = append(out, healthScopeReport{tenant, personID, provider, view.Report.ID, view.Report.RowVersion, view.Report.Status})
	}
	return out, nil
}
