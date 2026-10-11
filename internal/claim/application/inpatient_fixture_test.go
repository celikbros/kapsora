package application_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	authorizationapp "github.com/celikbros/kapsora/internal/authorization/application"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// inpatientStayFixture creates a discharged case with two real authorization holds.
// It uses the ordinary authorization service for reserve/release while seeding the
// clinical stay row directly; tests can then drive the real source and claim services.
func (f *fixture) inpatientStayFixture(t *testing.T, actual int) (caseID, stayID, originalID, extensionID uuid.UUID) {
	t.Helper()
	originalID = f.authorizeService(t, f.inpatient, "NIGHT", "5")
	extensionID = f.authorizeService(t, f.inpatient, "NIGHT", "3")
	ctx := context.Background()
	var originalRequest, extensionRequest uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, "SELECT request_id FROM service.authorization WHERE id=$1", originalID).Scan(&originalRequest); err != nil {
		t.Fatal(err)
	}
	if err := f.h.Admin.QueryRow(ctx, "SELECT request_id FROM service.authorization WHERE id=$1", extensionID).Scan(&extensionRequest); err != nil {
		t.Fatal(err)
	}
	admission := serviceDay.AddDate(0, 0, -8)
	discharge := admission.AddDate(0, 0, actual)
	if err := f.h.Admin.QueryRow(ctx, `INSERT INTO health.health_case
  (tenant_id, person_id, program_id, enrollment_id, case_type, provider_organization_id, opened_at)
  VALUES ($1,$2,$3,$4,'INPATIENT',$5,$6) RETURNING id`,
		f.tenant, f.person, f.program, f.enrollment, f.provider, admission).Scan(&caseID); err != nil {
		t.Fatal(err)
	}
	var encounterID, diagnosisID uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, `INSERT INTO health.encounter
  (tenant_id,case_id,encounter_type,started_at,ended_at,branch_code)
  VALUES ($1,$2,'INPATIENT',$3,$4,'WARD') RETURNING id`,
		f.tenant, caseID, admission, discharge).Scan(&encounterID); err != nil {
		t.Fatal(err)
	}
	if err := f.h.Admin.QueryRow(ctx, `INSERT INTO health.diagnosis
  (tenant_id,encounter_id,code_system_id,code_value_id,diagnosis_type,sensitive)
  SELECT tenant_id,$1,code_system_id,code_value_id,'PRIMARY',false
  FROM health.diagnosis WHERE id=$2 RETURNING id`,
		encounterID, f.diagnosisID).Scan(&diagnosisID); err != nil {
		t.Fatal(err)
	}
	over := actual > 8
	released := 8 - actual
	if released < 0 {
		released = 0
	}
	if err := f.h.Admin.QueryRow(ctx, `INSERT INTO health.inpatient_stay
  (tenant_id,case_id,provider_organization_id,admission_at,estimated_days,expected_discharge_at,
   discharge_at,status,service_request_id,authorization_id,admission_diagnosis_id,
   authorized_days,actual_days,released_days,over_authorization)
  VALUES ($1,$2,$3,$4,5,$5,$6,'DISCHARGED',$7,$8,$9,8,$10,$11,$12) RETURNING id`,
		f.tenant, caseID, f.provider, admission, admission.AddDate(0, 0, 8), discharge,
		originalRequest, originalID, diagnosisID, actual, released, over).Scan(&stayID); err != nil {
		t.Fatal(err)
	}
	f.h.AdminExec(`INSERT INTO health.stay_extension
  (tenant_id,stay_id,sequence_no,additional_days,reason_code,service_request_id,authorization_id,status)
  VALUES ($1,$2,1,3,'CLINICAL_NEED',$3,$4,'APPROVED')`,
		f.tenant, stayID, extensionRequest, extensionID)
	// Health discharge releases the newest extension first, then the original.
	left := released
	for _, hold := range []struct {
		id  uuid.UUID
		max int
	}{{extensionID, 3}, {originalID, 5}} {
		if left == 0 {
			break
		}
		n := left
		if n > hold.max {
			n = hold.max
		}
		err := db.WithTenantTx(ctx, f.h.App, db.TenantContext{TenantID: f.tenant}, func(ctx context.Context, tx pgx.Tx) error {
			_, err := f.authorizations.ReleaseUnused(ctx, tx, authorizationapp.ReleaseUnusedInput{
				TenantID: f.tenant, ActorID: f.actor, AuthorizationID: hold.id,
				Quantity: benefitdomain.MustQuantity(fmt.Sprint(n)), ReasonCode: "STAY_DISCHARGE",
			})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		left -= n
	}
	_ = time.Time{}

	return
}
