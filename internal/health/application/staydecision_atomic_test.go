package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/health/application"
	"github.com/celikbros/kapsora/internal/health/domain"
	"github.com/celikbros/kapsora/internal/platform/outbox"
	requestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
)

func approvedDecisionWithoutDelivery(t *testing.T, f *stayFixture, requestID uuid.UUID) outbox.Delivery {
	t.Helper()
	ctx := context.Background()
	rc := f.reviewerRC()
	request, err := f.requests.Get(ctx, rc, requestID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.requests.Approve(ctx, rc, requestID, requestapp.DecisionInput{
		ReasonCode: "MEDICALLY_NECESSARY", ExpectedVersion: request.Request.RowVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	var d outbox.Delivery
	var payload []byte
	var version int32
	err = f.h.Admin.QueryRow(ctx, `SELECT id, tenant_id, aggregate_type, aggregate_id,
        event_type, event_schema_version, payload_json, occurred_at
        FROM system.outbox_event WHERE event_type=$1 AND status='PENDING'
        AND payload_json->>'serviceRequestId'=$2 ORDER BY occurred_at DESC LIMIT 1`,
		requestapp.DecidedEvent, requestID.String()).Scan(&d.ID, &d.TenantID, &d.AggregateType,
		&d.AggregateID, &d.Type, &version, &payload, &d.OccurredAt)
	if err != nil {
		t.Fatal(err)
	}
	d.Payload, d.SchemaVersion, d.Attempt = json.RawMessage(payload), int(version), 1
	return d
}

func countStayAuthorizations(t *testing.T, f *stayFixture) int {
	t.Helper()
	return f.count(t, `SELECT count(*) FROM service.authorization WHERE tenant_id=$1`, f.tenant)
}

func TestAdmissionDecisionAndCancelEitherOrderKeepNoOrphanHold(t *testing.T) {
	for _, cancelFirst := range []bool{true, false} {
		name := "decision-first"
		if cancelFirst {
			name = "cancel-first"
		}
		t.Run(name, func(t *testing.T) {
			f := newStayFixture(t)
			stay := f.mustCreateStay(t, f.newCase(t, f.provider), 5)
			event := approvedDecisionWithoutDelivery(t, f, stay.Stay.ServiceRequestID)
			if cancelFirst {
				_, err := f.health.CancelStay(context.Background(), f.rc(), stay.Stay.ID,
					"ADMISSION_NOT_NEEDED", nil, stay.Stay.RowVersion, application.AccessRequest{})
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := f.health.HandleServiceRequestDecided(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			if !cancelFirst {
				current := f.stay(t, stay.Stay.ID)
				_, err := f.health.CancelStay(context.Background(), f.rc(), stay.Stay.ID,
					"ADMISSION_NOT_NEEDED", nil, current.Stay.RowVersion, application.AccessRequest{})
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := f.health.HandleServiceRequestDecided(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			current := f.stay(t, stay.Stay.ID)
			if current.Stay.Status != domain.StayCancelled || !f.balances(t).Reserved.IsZero() {
				t.Fatalf("cancel race left status=%s reserved=%s", current.Stay.Status, f.balances(t).Reserved.String())
			}
			want := 1
			if cancelFirst {
				want = 0
			}
			if got := countStayAuthorizations(t, f); got != want {
				t.Fatalf("authorizations=%d want=%d", got, want)
			}
		})
	}
}

func TestLateExtensionDecisionAfterDischargeOrCancelTakesNoHold(t *testing.T) {
	for _, closeWith := range []string{"discharge", "cancel"} {
		t.Run(closeWith, func(t *testing.T) {
			f := newStayFixture(t)
			stay := f.authorizedStay(t, f.newCase(t, f.provider), 5)
			extended, err := f.health.ExtendStay(context.Background(), f.rc(), stay.Stay.ID,
				application.StayExtensionInput{AdditionalDays: 3, ReasonCode: "COMPLICATION"},
				stay.Stay.RowVersion, application.AccessRequest{})
			if err != nil {
				t.Fatal(err)
			}
			event := approvedDecisionWithoutDelivery(t, f, extended.Extensions[0].ServiceRequestID)
			current := f.stay(t, stay.Stay.ID)
			if closeWith == "discharge" {
				f.advance(2)
				_, err = f.health.DischargeStay(context.Background(), f.rc(), stay.Stay.ID,
					f.clock(), current.Stay.RowVersion, application.AccessRequest{})
			} else {
				_, err = f.health.CancelStay(context.Background(), f.rc(), stay.Stay.ID,
					"ADMISSION_NOT_NEEDED", nil, current.Stay.RowVersion, application.AccessRequest{})
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := f.health.HandleServiceRequestDecided(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			after := f.stay(t, stay.Stay.ID)
			if after.Extensions[0].Status != domain.ExtensionCancelled || after.Extensions[0].AuthorizationID != nil ||
				after.Stay.AuthorizedDays != "5" || countStayAuthorizations(t, f) != 1 {
				t.Fatalf("late approval changed closed stay: %+v", after)
			}
			want := "0"
			if closeWith == "discharge" {
				want = "2"
			}
			if got := f.balances(t).Reserved.String(); got != want {
				t.Fatalf("reserved=%s want=%s", got, want)
			}
		})
	}
}

func TestFailedAdmissionTransitionRollsBackHoldAndRetriesOnce(t *testing.T) {
	f := newStayFixture(t)
	stay := f.mustCreateStay(t, f.newCase(t, f.provider), 5)
	event := approvedDecisionWithoutDelivery(t, f, stay.Stay.ServiceRequestID)
	f.h.AdminExec(`CREATE FUNCTION health.pc04_refuse_authorize() RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN IF NEW.status='AUTHORIZED' THEN RAISE EXCEPTION 'synthetic stay write failure'; END IF; RETURN NEW; END $$`)
	f.h.AdminExec(`CREATE TRIGGER pc04_refuse_authorize BEFORE UPDATE ON health.inpatient_stay
        FOR EACH ROW EXECUTE FUNCTION health.pc04_refuse_authorize()`)
	err := f.health.HandleServiceRequestDecided(context.Background(), event)
	if err == nil {
		t.Fatal("expected failure after authorization reserve")
	}
	if countStayAuthorizations(t, f) != 0 || !f.balances(t).Reserved.IsZero() ||
		f.stay(t, stay.Stay.ID).Stay.Status != domain.StayRequested {
		t.Fatal("failed transition committed a partial hold")
	}
	f.h.AdminExec(`DROP TRIGGER pc04_refuse_authorize ON health.inpatient_stay`)
	if err := f.health.HandleServiceRequestDecided(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := f.health.HandleServiceRequestDecided(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if countStayAuthorizations(t, f) != 1 || f.balances(t).Reserved.String() != "5" {
		t.Fatal("retry did not reserve exactly once")
	}
}

func TestFailedExtensionTransitionRollsBackHoldAndValidity(t *testing.T) {
	f := newStayFixture(t)
	stay := f.authorizedStay(t, f.newCase(t, f.provider), 5)
	original, err := f.authorizations.Get(context.Background(), f.rc(), *stay.Stay.AuthorizationID)
	if err != nil {
		t.Fatal(err)
	}
	extended, err := f.health.ExtendStay(context.Background(), f.rc(), stay.Stay.ID,
		application.StayExtensionInput{AdditionalDays: 3, ReasonCode: "COMPLICATION"},
		stay.Stay.RowVersion, application.AccessRequest{})
	if err != nil {
		t.Fatal(err)
	}
	event := approvedDecisionWithoutDelivery(t, f, extended.Extensions[0].ServiceRequestID)
	f.h.AdminExec(`CREATE FUNCTION health.pc04_refuse_extension() RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN IF NEW.status='APPROVED' THEN RAISE EXCEPTION 'synthetic extension write failure'; END IF; RETURN NEW; END $$`)
	f.h.AdminExec(`CREATE TRIGGER pc04_refuse_extension BEFORE UPDATE ON health.stay_extension
        FOR EACH ROW EXECUTE FUNCTION health.pc04_refuse_extension()`)
	if err := f.health.HandleServiceRequestDecided(context.Background(), event); err == nil {
		t.Fatal("expected failure after extension hold and validity update")
	}
	current := f.stay(t, stay.Stay.ID)
	unchanged, err := f.authorizations.Get(context.Background(), f.rc(), *stay.Stay.AuthorizationID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Extensions[0].Status != domain.ExtensionRequested || current.Stay.AuthorizedDays != "5" ||
		countStayAuthorizations(t, f) != 1 || f.balances(t).Reserved.String() != "5" ||
		!unchanged.Authorization.ValidTo.Equal(original.Authorization.ValidTo) {
		t.Fatal("failed extension committed funding, validity or day count")
	}
	f.h.AdminExec(`DROP TRIGGER pc04_refuse_extension ON health.stay_extension`)
	for range 2 {
		if err := f.health.HandleServiceRequestDecided(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	approved := f.stay(t, stay.Stay.ID)
	moved, err := f.authorizations.Get(context.Background(), f.rc(), *stay.Stay.AuthorizationID)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Extensions[0].Status != domain.ExtensionApproved || approved.Stay.AuthorizedDays != "8" ||
		countStayAuthorizations(t, f) != 2 || f.balances(t).Reserved.String() != "8" ||
		!moved.Authorization.ValidTo.After(original.Authorization.ValidTo) {
		t.Fatal("extension retry did not commit exactly once")
	}
}

func TestConcurrentAdmissionDecisionReservesOnce(t *testing.T) {
	f := newStayFixture(t)
	stay := f.mustCreateStay(t, f.newCase(t, f.provider), 5)
	event := approvedDecisionWithoutDelivery(t, f, stay.Stay.ServiceRequestID)
	begin := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-begin
			errs <- f.health.HandleServiceRequestDecided(context.Background(), event)
		}()
	}
	close(begin)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	current := f.stay(t, stay.Stay.ID)
	if current.Stay.Status != domain.StayAuthorized || countStayAuthorizations(t, f) != 1 ||
		f.balances(t).Reserved.String() != "5" {
		t.Fatal("concurrent approval did not leave exactly one valid hold")
	}
}

func TestLegacyPendingExtensionOnClosedStayIsCancelledWithoutFunding(t *testing.T) {
	f := newStayFixture(t)
	stay := f.authorizedStay(t, f.newCase(t, f.provider), 5)
	extended, err := f.health.ExtendStay(context.Background(), f.rc(), stay.Stay.ID,
		application.StayExtensionInput{AdditionalDays: 3, ReasonCode: "COMPLICATION"},
		stay.Stay.RowVersion, application.AccessRequest{})
	if err != nil {
		t.Fatal(err)
	}
	event := approvedDecisionWithoutDelivery(t, f, extended.Extensions[0].ServiceRequestID)
	f.advance(2)
	current := f.stay(t, stay.Stay.ID)
	_, err = f.health.DischargeStay(context.Background(), f.rc(), stay.Stay.ID,
		f.clock(), current.Stay.RowVersion, application.AccessRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// Model a row written before discharge cancelled pending extensions.
	f.h.AdminExec(`UPDATE health.stay_extension SET status='REQUESTED' WHERE tenant_id=$1 AND id=$2`,
		f.tenant, extended.Extensions[0].ID)
	if err := f.health.HandleServiceRequestDecided(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	after := f.stay(t, stay.Stay.ID)
	if after.Stay.Status != domain.StayDischarged || after.Extensions[0].Status != domain.ExtensionCancelled ||
		after.Extensions[0].AuthorizationID != nil || countStayAuthorizations(t, f) != 1 ||
		f.balances(t).Reserved.String() != "2" {
		t.Fatal("legacy late extension decision funded a closed stay")
	}
}

func TestConcurrentAdmissionApprovalAndCancelLeaveNoHold(t *testing.T) {
	f := newStayFixture(t)
	stay := f.mustCreateStay(t, f.newCase(t, f.provider), 5)
	event := approvedDecisionWithoutDelivery(t, f, stay.Stay.ServiceRequestID)
	begin := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-begin
		errs <- f.health.HandleServiceRequestDecided(context.Background(), event)
	}()
	go func() {
		defer wg.Done()
		<-begin
		_, err := f.health.CancelStay(context.Background(), f.rc(), stay.Stay.ID,
			"ADMISSION_NOT_NEEDED", nil, stay.Stay.RowVersion, application.AccessRequest{})
		if errors.Is(err, application.ErrVersionMismatch) {
			current := f.stay(t, stay.Stay.ID)
			_, err = f.health.CancelStay(context.Background(), f.rc(), stay.Stay.ID,
				"ADMISSION_NOT_NEEDED", nil, current.Stay.RowVersion, application.AccessRequest{})
		}
		errs <- err
	}()
	close(begin)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	current := f.stay(t, stay.Stay.ID)
	if current.Stay.Status != domain.StayCancelled || !f.balances(t).Reserved.IsZero() {
		t.Fatal("concurrent approval and cancellation left a valid hold")
	}
	if got := countStayAuthorizations(t, f); got > 1 {
		t.Fatalf("duplicate authorizations: %d", got)
	}
}
