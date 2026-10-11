package dbtests

import (
	"testing"

	"github.com/celikbros/kapsora/internal/platform/dbmigrate"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// Migration 59 adds optional evidence to an append-only commercial record. An old
// cancellation remains readable with four NULLs; a new exact claim must be whole,
// nonnegative, and just as immutable as the original fee and policy snapshot.
func TestCancellationEffectMigration59PreservesHistoryAndConstrainsRows(t *testing.T) {
	h := dbtest.NewAtVersion(t, 58)
	s, historicalBooking := seedConfirmedBooking(t, h, "CANCEL_EFFECT_59")
	h.AdminExec(`INSERT INTO accommodation.cancellation
		(tenant_id,booking_id,cancelled_at,reason_code,policy_snapshot,free,
		 penalty_nights,released_nights,fee_amount,payer_fee,member_fee,currency_code)
		VALUES ($1,$2,clock_timestamp(),'MEMBER_CANCELLED','{}'::jsonb,true,
		 0,2,0,0,0,'TRY')`, s.tenant, historicalBooking)

	state, err := dbmigrate.UpTo(h.AdminURL, 59)
	if err != nil || state.Dirty || state.Version != 59 {
		t.Fatalf("migration 58 to 59: %+v %v", state, err)
	}
	ctx, cancel := h.Ctx()
	defer cancel()
	var historicalNull, historicalWhole bool
	if err := h.Admin.QueryRow(ctx, `SELECT
		consumed_service_nights IS NULL AND released_service_nights IS NULL AND
		consumed_entitlement_units IS NULL AND released_entitlement_units IS NULL,
		released_nights=2 FROM accommodation.cancellation
		WHERE tenant_id=$1 AND booking_id=$2`, s.tenant, historicalBooking).
		Scan(&historicalNull, &historicalWhole); err != nil {
		t.Fatal(err)
	}
	if !historicalNull || !historicalWhole {
		t.Fatal("CANCELLATION_EFFECT_MIGRATION_HISTORY: old effect was invented or legacy value changed")
	}

	newBooking, err := s.insertBooking(t, h, "BK-20260620-BBBBBBBB", "CONFIRMED",
		"2026-06-20", "2026-06-23", 3)
	if err != nil {
		t.Fatal(err)
	}
	const insert = `INSERT INTO accommodation.cancellation
		(tenant_id,booking_id,cancelled_at,reason_code,policy_snapshot,free,
		 penalty_nights,released_nights,fee_amount,payer_fee,member_fee,currency_code,
		 consumed_service_nights,released_service_nights,
		 consumed_entitlement_units,released_entitlement_units)
		VALUES ($1,$2,clock_timestamp(),'MEMBER_CANCELLED','{}'::jsonb,false,
		 1,0,1000,900,100,'TRY',$3,$4,$5,$6)`
	err = h.AdminExecErr(insert, s.tenant, newBooking, "1", "0.5", "2", nil)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation,
		"a partially NULL exact cancellation effect")
	err = h.AdminExecErr(insert, s.tenant, newBooking, "1", "0.5", "2", "-1")
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation,
		"a negative exact cancellation effect")
	h.AdminExec(insert, s.tenant, newBooking, "1", "0.5", "2", "1")
	var consumedService, releasedService, consumedUnits, releasedUnits string
	if err := h.Admin.QueryRow(ctx, `SELECT consumed_service_nights::text,
		released_service_nights::text,consumed_entitlement_units::text,
		released_entitlement_units::text FROM accommodation.cancellation
		WHERE tenant_id=$1 AND booking_id=$2`, s.tenant, newBooking).
		Scan(&consumedService, &releasedService, &consumedUnits, &releasedUnits); err != nil {
		t.Fatal(err)
	}
	if consumedService != "1.000000" || releasedService != "0.500000" ||
		consumedUnits != "2.000000" || releasedUnits != "1.000000" {
		t.Fatalf("CANCELLATION_EFFECT_MIGRATION_EXACT: stored %s/%s/%s/%s",
			consumedService, releasedService, consumedUnits, releasedUnits)
	}
	err = h.AdminExecErr(`UPDATE accommodation.cancellation
		SET released_entitlement_units=0 WHERE tenant_id=$1 AND booking_id=$2`, s.tenant, newBooking)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint,
		"an exact cancellation effect edited after insertion")
	err = h.AdminExecErr(`UPDATE accommodation.cancellation
		SET consumed_service_nights=0 WHERE tenant_id=$1 AND booking_id=$2`, s.tenant, historicalBooking)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint,
		"an old cancellation given retrospective exact evidence")
	err = h.AdminExecErr(`DELETE FROM accommodation.cancellation
		WHERE tenant_id=$1 AND booking_id=$2`, s.tenant, newBooking)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint,
		"an exact cancellation effect deleted")
}
