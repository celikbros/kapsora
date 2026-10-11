package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

type inpatientScopeRow struct {
	TenantID   uuid.UUID `json:"tenantId"`
	ProviderID uuid.UUID `json:"providerId"`
	PersonID   uuid.UUID `json:"personId"`
	CaseID     uuid.UUID `json:"caseId"`
	RequestID  uuid.UUID `json:"requestId"`
	StayID     uuid.UUID `json:"stayId"`
	RowVersion int64     `json:"rowVersion"`
	Status     string    `json:"status"`
}

func scopeID(fixture, tenant uuid.UUID, kind string) uuid.UUID {
	return uuid.NewSHA1(fixture, []byte(tenant.String()+":"+kind))
}

// inpatientScope creates closed, unfunded scope rows. It intentionally creates no
// entitlement account, grant, authorization, movement, diagnosis or clinical note.
// Every UUID is derived from the explicit fixture and validated on reuse.
func (s *seeder) inpatientScope(ctx context.Context, fixture uuid.UUID) ([]inpatientScopeRow, error) {
	if fixture == uuid.Nil {
		return nil, fmt.Errorf("inpatient scope fixture needs a nonzero UUID")
	}
	marker := strings.ToUpper(strings.ReplaceAll(fixture.String(), "-", ""))
	var out []inpatientScopeRow
	for _, code := range []string{"DEMO_A", "DEMO_B"} {
		tenant, err := sqlcgen.New(s.pool).GetTenantIDByCode(ctx, code)
		if err != nil {
			return nil, fmt.Errorf("inpatient scope needs %s: %w", code, err)
		}
		provider, err := s.provisioner.EnsureProviderOrganization(ctx, tenant,
			"PC04_SCOPE_"+marker, "Synthetic inpatient scope "+fixture.String())
		if err != nil {
			return nil, err
		}
		row := inpatientScopeRow{
			TenantID: tenant, ProviderID: provider,
			PersonID: scopeID(fixture, tenant, "person"), CaseID: scopeID(fixture, tenant, "case"),
			RequestID: scopeID(fixture, tenant, "request"), StayID: scopeID(fixture, tenant, "stay"),
		}
		err = db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenant}, func(ctx context.Context, tx pgx.Tx) error {
			return seedClosedInpatientScope(ctx, tx, fixture, &row)
		})
		if err != nil {
			return nil, fmt.Errorf("inpatient scope %s: %w", code, err)
		}
		out = append(out, row)
	}
	return out, nil
}

func seedClosedInpatientScope(ctx context.Context, tx pgx.Tx, fixture uuid.UUID, row *inpatientScopeRow) error {
	ids := func(kind string) uuid.UUID { return scopeID(fixture, row.TenantID, kind) }
	marker := strings.ToUpper(strings.ReplaceAll(fixture.String(), "-", ""))
	code := "PC04_SCOPE_" + marker
	lastName := "YatisScope" + marker
	// Historical closed records need their tenant-composite foreign keys, but no
	// published benefit plan or account. These support rows cannot make a claim or hold.
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO party.person (id, tenant_id, first_name, last_name, normalized_name)
			 VALUES ($1,$2,'Deneme',$3,$4) ON CONFLICT (id) DO NOTHING`, []any{row.PersonID, row.TenantID, lastName, "deneme " + strings.ToLower(lastName)}},
		{`INSERT INTO party.sponsor_membership (id, tenant_id, person_id, sponsor_tenant_organization_id, membership_type, status, valid_period)
			 VALUES ($1,$2,$3,$4,'MEMBER','ENDED',daterange('2026-01-01','2026-12-31','[)')) ON CONFLICT (id) DO NOTHING`, []any{ids("membership"), row.TenantID, row.PersonID, row.ProviderID}},
		{`INSERT INTO benefit.program (id, tenant_id, sponsor_tenant_organization_id, payer_tenant_organization_id, code, name, program_type, status, valid_period)
			 VALUES ($1,$2,$3,$3,$4,$5,'MEMBER_PROGRAM','CLOSED',daterange('2026-01-01','2026-12-31','[)')) ON CONFLICT (id) DO NOTHING`, []any{ids("program"), row.TenantID, row.ProviderID, code, "Synthetic inpatient scope " + fixture.String()}},
		{`INSERT INTO benefit.plan (id, tenant_id, program_id, code, name, status)
			 VALUES ($1,$2,$3,$4,$5,'RETIRED') ON CONFLICT (id) DO NOTHING`, []any{ids("plan"), row.TenantID, ids("program"), code, "Synthetic inpatient scope"}},
		{`INSERT INTO benefit.enrollment (id, tenant_id, sponsor_membership_id, plan_id, status, valid_period)
			 VALUES ($1,$2,$3,$4,'ENDED',daterange('2026-01-01','2026-12-31','[)')) ON CONFLICT (id) DO NOTHING`, []any{ids("enrollment"), row.TenantID, ids("membership"), ids("plan")}},
		{`INSERT INTO health.health_case (id, tenant_id, person_id, program_id, enrollment_id, case_type, provider_organization_id, opened_at, closed_at, status)
			 VALUES ($1,$2,$3,$4,$5,'INPATIENT',$6,'2026-06-01T10:00:00Z','2026-06-02T10:00:00Z','CLOSED') ON CONFLICT (id) DO NOTHING`, []any{row.CaseID, row.TenantID, row.PersonID, ids("program"), ids("enrollment"), row.ProviderID}},
		{`INSERT INTO service.service_request (id, tenant_id, request_reference, request_type, person_id, program_id, enrollment_id,
			 provider_tenant_organization_id, service_date, channel, status, closed_at)
			 VALUES ($1,$2,$3,'PREAUTHORIZATION',$4,$5,$6,$7,'2026-06-01','PROVIDER_PORTAL','CANCELLED','2026-06-02T10:00:00Z') ON CONFLICT (id) DO NOTHING`, []any{row.RequestID, row.TenantID, "SR-" + code, row.PersonID, ids("program"), ids("enrollment"), row.ProviderID}},
		{`INSERT INTO service.service_request_version (id, tenant_id, service_request_id, version_no, status)
			 VALUES ($1,$2,$3,1,'DRAFT') ON CONFLICT (id) DO NOTHING`, []any{ids("request-version"), row.TenantID, row.RequestID}},
		{`INSERT INTO health.inpatient_stay (id, tenant_id, case_id, provider_organization_id, admission_at, estimated_days,
			 expected_discharge_at, status, service_request_id, cancel_reason_code)
			 VALUES ($1,$2,$3,$4,'2026-06-01T10:00:00Z',1,'2026-06-02T10:00:00Z','CANCELLED',$5,'PC04_SCOPE_FIXTURE') ON CONFLICT (id) DO NOTHING`, []any{row.StayID, row.TenantID, row.CaseID, row.ProviderID, row.RequestID}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	var personName, programCode, caseStatus, caseType, requestStatus, requestType, stayReason string
	var provider uuid.UUID
	var admission time.Time
	err := tx.QueryRow(ctx, `SELECT p.last_name, pr.code, c.status, c.case_type, r.status, r.request_type,
		 s.cancel_reason_code, s.provider_organization_id, s.admission_at, s.status, s.row_version
		 FROM health.inpatient_stay s
		 JOIN health.health_case c ON c.tenant_id=s.tenant_id AND c.id=s.case_id
		 JOIN party.person p ON p.tenant_id=c.tenant_id AND p.id=c.person_id
		 JOIN benefit.program pr ON pr.tenant_id=c.tenant_id AND pr.id=c.program_id
		 JOIN service.service_request r ON r.tenant_id=s.tenant_id AND r.id=s.service_request_id
		 WHERE s.tenant_id=$1 AND s.id=$2 AND c.id=$3 AND p.id=$4 AND r.id=$5`,
		row.TenantID, row.StayID, row.CaseID, row.PersonID, row.RequestID).Scan(
		&personName, &programCode, &caseStatus, &caseType, &requestStatus, &requestType,
		&stayReason, &provider, &admission, &row.Status, &row.RowVersion)
	if err != nil {
		return err
	}
	if personName != lastName || programCode != code || caseStatus != "CLOSED" || caseType != "INPATIENT" ||
		requestStatus != "CANCELLED" || requestType != "PREAUTHORIZATION" || stayReason != "PC04_SCOPE_FIXTURE" ||
		provider != row.ProviderID || row.Status != "CANCELLED" || !admission.Equal(time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)) {
		return fmt.Errorf("unexpected existing inpatient scope rows")
	}
	var supportCount int
	err = tx.QueryRow(ctx, `SELECT count(*)
		 FROM health.inpatient_stay s
		 JOIN health.health_case c ON c.tenant_id=s.tenant_id AND c.id=s.case_id
		 JOIN service.service_request r ON r.tenant_id=s.tenant_id AND r.id=s.service_request_id
		 JOIN service.service_request_version v ON v.tenant_id=r.tenant_id AND v.service_request_id=r.id
		 JOIN benefit.enrollment e ON e.tenant_id=c.tenant_id AND e.id=c.enrollment_id
		 JOIN party.sponsor_membership m ON m.tenant_id=e.tenant_id AND m.id=e.sponsor_membership_id
		 JOIN benefit.plan p ON p.tenant_id=e.tenant_id AND p.id=e.plan_id
		 JOIN benefit.program pr ON pr.tenant_id=p.tenant_id AND pr.id=p.program_id
		 WHERE s.tenant_id=$1 AND s.id=$2 AND c.provider_organization_id=$3 AND r.provider_tenant_organization_id=$3
		   AND c.person_id=$4 AND r.person_id=$4 AND r.enrollment_id=e.id AND r.program_id=pr.id
		   AND m.person_id=$4 AND m.sponsor_tenant_organization_id=$3 AND m.status='ENDED'
		   AND e.status='ENDED' AND p.status='RETIRED' AND pr.status='CLOSED'
		   AND pr.sponsor_tenant_organization_id=$3 AND pr.payer_tenant_organization_id=$3
		   AND v.id=$5 AND v.status='DRAFT' AND s.authorization_id IS NULL`,
		row.TenantID, row.StayID, row.ProviderID, row.PersonID, ids("request-version")).Scan(&supportCount)
	if err != nil || supportCount != 1 {
		return fmt.Errorf("unexpected inpatient scope support rows: count=%d: %w", supportCount, err)
	}
	var fundedCount int
	err = tx.QueryRow(ctx, `SELECT
		 (SELECT count(*) FROM benefit.entitlement_account WHERE tenant_id=$1 AND enrollment_id=$2)
		 + (SELECT count(*) FROM service.authorization WHERE tenant_id=$1 AND request_id=$3)`,
		row.TenantID, ids("enrollment"), row.RequestID).Scan(&fundedCount)
	if err != nil || fundedCount != 0 {
		return fmt.Errorf("inpatient scope unexpectedly funded: count=%d: %w", fundedCount, err)
	}
	return nil
}
