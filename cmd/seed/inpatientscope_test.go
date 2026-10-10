package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// The harness migrates and drops a separate database. No assertion reads the running demo.
func TestInpatientScopeCreatesUnfundedClosedRowsAndRefusesTampering(t *testing.T) {
	h := dbtest.New(t)
	for _, code := range []string{"DEMO_A", "DEMO_B"} {
		tenant := h.CreateTenant(code)
		h.AdminExec(`INSERT INTO party.membership_type (tenant_id,code,display_name) VALUES ($1,'MEMBER','Member')`, tenant)
		h.AdminExec(`INSERT INTO benefit.program_type (tenant_id,code,display_name) VALUES ($1,'MEMBER_PROGRAM','Member program')`, tenant)
	}
	s := &seeder{pool: h.App, provisioner: identityapp.NewProvisioner(identitypg.NewProvisioningRepository(h.App), nil)}
	ctx := context.Background()
	fixture := uuid.New()
	first, err := s.inpatientScope(ctx, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].TenantID == first[1].TenantID || first[0].StayID == first[1].StayID {
		t.Fatalf("tenant separation: %+v", first)
	}
	for _, row := range first {
		if row.Status != "CANCELLED" || row.RowVersion != 1 || row.ProviderID == uuid.Nil {
			t.Fatalf("incomplete closed fixture: %+v", row)
		}
	}
	snapshot := func() map[string]int {
		t.Helper()
		counts := map[string]int{}
		for label, query := range map[string]string{
			"people":         `SELECT count(*) FROM party.person`,
			"memberships":    `SELECT count(*) FROM party.sponsor_membership`,
			"programs":       `SELECT count(*) FROM benefit.program`,
			"plans":          `SELECT count(*) FROM benefit.plan`,
			"enrollments":    `SELECT count(*) FROM benefit.enrollment`,
			"cases":          `SELECT count(*) FROM health.health_case`,
			"requests":       `SELECT count(*) FROM service.service_request`,
			"versions":       `SELECT count(*) FROM service.service_request_version`,
			"stays":          `SELECT count(*) FROM health.inpatient_stay`,
			"accounts":       `SELECT count(*) FROM benefit.entitlement_account`,
			"authorizations": `SELECT count(*) FROM service.authorization`,
			"movements":      `SELECT count(*) FROM benefit.entitlement_ledger`,
			"grants":         `SELECT count(*) FROM iam.access_grant`,
		} {
			var count int
			if err := h.Admin.QueryRow(ctx, query).Scan(&count); err != nil {
				t.Fatalf("count %s: %v", label, err)
			}
			counts[label] = count
		}
		return counts
	}
	before := snapshot()
	for _, key := range []string{"people", "memberships", "programs", "plans", "enrollments", "cases", "requests", "versions", "stays"} {
		if before[key] != 2 {
			t.Fatalf("%s=%d, want 2", key, before[key])
		}
	}
	for _, key := range []string{"accounts", "authorizations", "movements", "grants"} {
		if before[key] != 0 {
			t.Fatalf("%s=%d, want 0", key, before[key])
		}
	}
	second, err := s.inpatientScope(ctx, fixture)
	if err != nil || !reflect.DeepEqual(first, second) || !reflect.DeepEqual(before, snapshot()) {
		t.Fatalf("rerun changed closed fixture: %v, first=%+v second=%+v", err, first, second)
	}
	// An exact deterministic UUID is insufficient proof of ownership. A later
	// operator edit must make this seed refuse reuse, without repairing that row.
	h.AdminExec(`UPDATE health.inpatient_stay SET cancel_reason_code='PC04_TAMPERED' WHERE tenant_id=$1 AND id=$2`, first[0].TenantID, first[0].StayID)
	tampered := snapshot()
	if _, err := s.inpatientScope(ctx, fixture); err == nil {
		t.Fatal("tampered fixture was accepted")
	}
	if !reflect.DeepEqual(tampered, snapshot()) {
		t.Fatal("refused rerun changed database")
	}
	var reason string
	if err := h.Admin.QueryRow(ctx, `SELECT cancel_reason_code FROM health.inpatient_stay WHERE tenant_id=$1 AND id=$2`, first[0].TenantID, first[0].StayID).Scan(&reason); err != nil || reason != "PC04_TAMPERED" {
		t.Fatalf("tampered row was overwritten: %q %v", reason, err)
	}
}
