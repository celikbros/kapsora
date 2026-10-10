package dbtests

import (
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/platform/dbmigrate"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

func TestNoShowEffectMigration60PreservesHistoryAndConstrainsEvidence(t *testing.T) {
	h := dbtest.NewAtVersion(t, 59)
	s, booking := seedConfirmedBooking(t, h, "NO_SHOW_EFFECT_60")
	reviewer := uuid.New()
	h.AdminExec(`INSERT INTO accommodation.no_show
		(tenant_id,booking_id,assessed_fee_amount,payer_amount,member_amount,currency_code,
		 status,reviewed_by,reviewed_at,consumed_nights)
		VALUES ($1,$2,100,0,100,'TRY','CONFIRMED',$3,clock_timestamp(),1)`, s.tenant, booking, reviewer)
	state, err := dbmigrate.UpTo(h.AdminURL, 60)
	if err != nil || state.Dirty || state.Version != 60 {
		t.Fatalf("migration 59 to 60: %+v %v", state, err)
	}
	ctx, cancel := h.Ctx()
	defer cancel()
	var historicalNull bool
	var historicalStatus, historicalFee string
	if err := h.Admin.QueryRow(ctx, `SELECT consumed_service_nights IS NULL AND
		released_service_nights IS NULL AND consumed_entitlement_units IS NULL AND
		released_entitlement_units IS NULL,status,assessed_fee_amount::text FROM accommodation.no_show
		WHERE tenant_id=$1 AND booking_id=$2`, s.tenant, booking).
		Scan(&historicalNull, &historicalStatus, &historicalFee); err != nil {
		t.Fatal(err)
	}
	if !historicalNull || historicalStatus != "CONFIRMED" || historicalFee != "100.000000" {
		t.Fatalf("historical confirmed report changed: null=%t status=%s fee=%s",
			historicalNull, historicalStatus, historicalFee)
	}
	newBooking, err := s.insertBooking(t, h, "BK-20260620-CCCCCCCC", "CONFIRMED", "2026-06-20", "2026-06-23", 3)
	if err != nil {
		t.Fatal(err)
	}
	h.AdminExec(`INSERT INTO accommodation.no_show (tenant_id,booking_id,currency_code)
		VALUES ($1,$2,'TRY')`, s.tenant, newBooking)
	err = h.AdminExecErr(`UPDATE accommodation.no_show SET consumed_service_nights=1,
		released_service_nights=0,consumed_entitlement_units=2,released_entitlement_units=0
		WHERE tenant_id=$1 AND booking_id=$2`, s.tenant, newBooking)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation,
		"unreviewed no-show effect")
	const update = `UPDATE accommodation.no_show SET status='CONFIRMED', reviewed_by=$3,
		reviewed_at=clock_timestamp(), consumed_nights=1, consumed_service_nights=$4,
		released_service_nights=$5, consumed_entitlement_units=$6,
		released_entitlement_units=$7 WHERE tenant_id=$1 AND booking_id=$2`
	err = h.AdminExecErr(update, s.tenant, newBooking, reviewer, "1.5", "0", "3", nil)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "partial no-show evidence")
	err = h.AdminExecErr(update, s.tenant, newBooking, reviewer, "1.5", "0", "3", "-1")
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "negative no-show evidence")
	h.AdminExec(update, s.tenant, newBooking, reviewer, "1.5", "0", "3", "0")
	var exact string
	if err := h.Admin.QueryRow(ctx, `SELECT consumed_service_nights::text FROM accommodation.no_show
		WHERE tenant_id=$1 AND booking_id=$2`, s.tenant, newBooking).Scan(&exact); err != nil {
		t.Fatal(err)
	}
	if exact != "1.500000" {
		t.Fatalf("exact no-show effect=%s", exact)
	}
}
