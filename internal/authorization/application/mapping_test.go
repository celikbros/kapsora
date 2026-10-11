package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/authorization/application"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/platform/db"
)

func TestMappedAuthorizationReservesConsumesAndCancelsInEntitlementUnits(t *testing.T) {
	f := newFixtureWithMapping(t, true)
	request := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "6"})
	a := f.authorize(t, request, "mapped")
	if a.Items[0].EntitlementUnitFactor != "2" || a.Items[0].ApprovedQuantity != "6" {
		t.Fatal("authorization did not preserve service quantity and mapping factor")
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Reserved, "12", "mapped reservation")
	replay := f.authorize(t, request, "mapped")
	if replay.Authorization.ID != a.Authorization.ID {
		t.Fatal("retry created another authorization")
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Reserved, "12", "reservation after replay")
	r := f.record(t, a.Authorization.ID, a.Items[0].ID, "2")
	if _, err := f.svc.CompleteFulfilment(context.Background(), f.rc(), r.Fulfilment.ID, r.Fulfilment.RowVersion); err != nil {
		t.Fatal(err)
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Consumed, "4", "mapped consumption")
	assertQuantity(t, f.balances(t, f.physioAccount).Reserved, "8", "remaining hold")
	a, err := f.svc.Get(context.Background(), f.rc(), a.Authorization.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Authorization.ConsumedTotal != "2" {
		t.Fatal("service counter became entitlement quantity")
	}
	_, err = f.svc.Cancel(context.Background(), f.rc(), a.Authorization.ID, application.ReasonInput{ReasonCode: "TEST_CANCEL", ExpectedVersion: a.Authorization.RowVersion})
	if err != nil {
		t.Fatal(err)
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Reserved, "0", "cancelled hold")
	assertQuantity(t, f.balances(t, f.physioAccount).Available, "16", "unused entitlement returned")
	f.assertConservation(t, f.physioAccount)
}

func TestMappedAuthorizationClaimConsumptionAndUnusedRelease(t *testing.T) {
	f := newFixtureWithMapping(t, true)
	r := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "5"})
	a := f.authorize(t, r, "claim-mapped")
	for range 2 {
		err := db.WithTenantTx(context.Background(), f.pool, db.TenantContext{TenantID: f.tenant}, func(ctx context.Context, tx pgx.Tx) error {
			out, err := f.svc.Consume(ctx, tx, application.ConsumeInput{TenantID: f.tenant, ActorID: f.actor, AuthorizationID: a.Authorization.ID, ServiceDefinitionID: f.physioDefinition, Quantity: benefitdomain.MustQuantity("2"), Key: "claim-one", ReasonCode: "CLAIM"})
			if err != nil {
				return err
			}
			if !out.Matched || out.OverConsumed {
				t.Fatal("mapped claim did not consume")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Consumed, "4", "one claim consumption after replay")
	for range 2 {
		err := db.WithTenantTx(context.Background(), f.pool, db.TenantContext{TenantID: f.tenant}, func(ctx context.Context, tx pgx.Tx) error {
			_, err := f.svc.ReleaseUnused(ctx, tx, application.ReleaseUnusedInput{TenantID: f.tenant, ActorID: f.actor, AuthorizationID: a.Authorization.ID, Quantity: benefitdomain.MustQuantity("3"), ReasonCode: "DISCHARGE"})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Reserved, "0", "mapped release")
	assertQuantity(t, f.balances(t, f.physioAccount).Available, "16", "mapped release replay")
	f.assertConservation(t, f.physioAccount)
}

func TestMappedAuthorizationInsufficientBalanceRollsBack(t *testing.T) {
	f := newFixtureWithMapping(t, true)
	r := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "11"})
	_, err := f.svc.Create(context.Background(), f.rc(), application.NewAuthorizationInput{RequestID: r, ValidTo: testNow.AddDate(0, 0, 1), IdempotencyKey: "mapped-too-much"})
	if err == nil {
		t.Fatal("twenty-two units reserved from twenty")
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Available, "20", "failed reservation")
	assertQuantity(t, f.balances(t, f.physioAccount).Reserved, "0", "no partial reservation")
	f.assertConservation(t, f.physioAccount)
}

func TestMappedAuthorizationCancellationAfterPartialUnusedRelease(t *testing.T) {
	f := newFixtureWithMapping(t, true)
	request := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "5"})
	a := f.authorize(t, request, "partial-release-cancel")
	err := db.WithTenantTx(context.Background(), f.pool, db.TenantContext{TenantID: f.tenant}, func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.svc.ReleaseUnused(ctx, tx, application.ReleaseUnusedInput{TenantID: f.tenant, ActorID: f.actor, AuthorizationID: a.Authorization.ID, Quantity: benefitdomain.MustQuantity("1"), ReasonCode: "DISCHARGE"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Reserved, "8", "partial release")
	_, err = f.svc.Cancel(context.Background(), f.rc(), a.Authorization.ID, application.ReasonInput{ReasonCode: "TEST_CANCEL", ExpectedVersion: a.Authorization.RowVersion})
	if err != nil {
		t.Fatal(err)
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Reserved, "0", "cancel after release")
	assertQuantity(t, f.balances(t, f.physioAccount).Available, "20", "all returned")
	f.assertConservation(t, f.physioAccount)
}

func TestAuthorizationRetryStillEnforcesProviderScope(t *testing.T) {
	f := newFixtureWithMapping(t, true)
	request := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "1"})
	f.authorize(t, request, "scope-replay")
	_, err := f.svc.Create(context.Background(), f.providerRC(f.otherOr), application.NewAuthorizationInput{RequestID: request, ValidTo: testNow.AddDate(0, 0, 1), IdempotencyKey: "scope-replay"})
	if !errors.Is(err, application.ErrAuthorizationNotFound) {
		t.Fatalf("out-of-scope retry: %v", err)
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Reserved, "2", "no second hold")
}

func TestMappedClaimReturnRestoresOnlyItsDrawAndPreservesClosedState(t *testing.T) {
	for _, status := range []string{"ACTIVE", "CANCELLED", "EXPIRED"} {
		t.Run(status, func(t *testing.T) {
			f := newFixtureWithMapping(t, true)
			request := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "5"})
			auth := f.authorize(t, request, "correction-mapped")
			original := application.ConsumeInput{TenantID: f.tenant, ActorID: f.actor, AuthorizationID: auth.Authorization.ID, ServiceDefinitionID: f.physioDefinition, Quantity: benefitdomain.MustQuantity("2"), Key: "claim-original", ReasonCode: "CLAIM"}
			run := func(fn func(context.Context, pgx.Tx) error) {
				t.Helper()
				if err := db.WithTenantTx(context.Background(), f.pool, db.TenantContext{TenantID: f.tenant}, fn); err != nil {
					t.Fatal(err)
				}
			}
			run(func(ctx context.Context, tx pgx.Tx) error { _, err := f.svc.Consume(ctx, tx, original); return err })
			assertQuantity(t, f.balances(t, f.physioAccount).Consumed, "4", "mapped first draw")
			switch status {
			case "CANCELLED":
				view, err := f.svc.Get(context.Background(), f.rc(), auth.Authorization.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.svc.Cancel(context.Background(), f.rc(), auth.Authorization.ID, application.ReasonInput{ReasonCode: "CLOSED_TEST", ExpectedVersion: view.Authorization.RowVersion}); err != nil {
					t.Fatal(err)
				}
			case "EXPIRED":
				f.h.AdminExec(`UPDATE service.authorization SET valid_from=valid_from-interval '2 days',valid_to=valid_from-interval '1 day' WHERE id=$1`, auth.Authorization.ID)
			}
			for range 2 {
				run(func(ctx context.Context, tx pgx.Tx) error { return f.svc.UndoConsumption(ctx, tx, original) })
			}
			assertQuantity(t, f.balances(t, f.physioAccount).Consumed, "0", "undo mapped draw once")
			view, err := f.svc.Get(context.Background(), f.rc(), auth.Authorization.ID)
			if err != nil {
				t.Fatal(err)
			}
			if view.Authorization.ConsumedTotal != "0" || view.Authorization.Status != status {
				t.Fatalf("status=%s consumed=%s", view.Authorization.Status, view.Authorization.ConsumedTotal)
			}
			if status == "ACTIVE" {
				assertQuantity(t, f.balances(t, f.physioAccount).Reserved, "10", "restored mapped hold")
			} else {
				assertQuantity(t, f.balances(t, f.physioAccount).Reserved, "0", "closed hold stays released")
			}
			f.assertConservation(t, f.physioAccount)
		})
	}
}
