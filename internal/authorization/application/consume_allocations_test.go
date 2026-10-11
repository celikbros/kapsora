package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/authorization/application"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/platform/db"
)

func allocation(auth uuid.UUID, quantity string, line uuid.UUID) application.ConsumptionAllocation {
	return application.ConsumptionAllocation{AuthorizationID: auth, Quantity: benefitdomain.MustQuantity(quantity), Key: "claim-line:" + line.String() + ":authorization:" + auth.String()}
}

func consumeBatch(t *testing.T, f *fixture, in application.ConsumeAllocationsInput) (application.ConsumeAllocationsResult, error) {
	t.Helper()
	var out application.ConsumeAllocationsResult
	err := db.WithTenantTx(context.Background(), f.pool, db.TenantContext{TenantID: f.tenant}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = f.svc.ConsumeAllocations(ctx, tx, in)
		return err
	})
	return out, err
}

func TestConsumeAllocationsAcrossHoldsReplayAndReturn(t *testing.T) {
	f := newFixture(t)
	first := f.authorize(t, f.seedRequest(t, f.providerOr, line{f.physioDefinition, "5"}), "batch-first")
	second := f.authorize(t, f.seedRequest(t, f.providerOr, line{f.physioDefinition, "3"}), "batch-extension")
	lineID := uuid.New()
	a := allocation(first.Authorization.ID, "5", lineID)
	b := allocation(second.Authorization.ID, "1", lineID)
	in := application.ConsumeAllocationsInput{TenantID: f.tenant, ActorID: f.actor, ServiceDefinitionID: f.physioDefinition, Quantity: benefitdomain.MustQuantity("6"), ReasonCode: "CLAIM", Allocations: []application.ConsumptionAllocation{a, b}}
	for run := range 2 {
		out, err := consumeBatch(t, f, in)
		if err != nil || out.OverConsumed || out.Consumed.String() != "6" || len(out.Draws) != 2 {
			t.Fatalf("run %d: result=%+v error=%v", run, out, err)
		}
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Consumed, "6", "one batch draw")
	assertQuantity(t, f.balances(t, f.physioAccount).Reserved, "2", "extension remainder")
	// A reversed movement must never be silently accepted as the same claim again.
	err := db.WithTenantTx(context.Background(), f.pool, db.TenantContext{TenantID: f.tenant}, func(ctx context.Context, tx pgx.Tx) error {
		return f.svc.UndoConsumption(ctx, tx, application.ConsumeInput{TenantID: f.tenant, ActorID: f.actor, AuthorizationID: a.AuthorizationID, ServiceDefinitionID: f.physioDefinition, Quantity: a.Quantity, Key: a.Key, ReasonCode: "CLAIM"})
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = consumeBatch(t, f, in)
	if !errors.Is(err, application.ErrAllocationReplay) {
		t.Fatalf("reversed replay: %v", err)
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Consumed, "1", "reversal stays reversed")
}

func TestConsumeAllocationsPreflightAllOrNothing(t *testing.T) {
	f := newFixture(t)
	first := f.authorize(t, f.seedRequest(t, f.providerOr, line{f.physioDefinition, "5"}), "preflight-first")
	second := f.authorize(t, f.seedRequest(t, f.providerOr, line{f.physioDefinition, "3"}), "preflight-second")
	lineID := uuid.New()
	a := allocation(first.Authorization.ID, "5", lineID)
	b := allocation(second.Authorization.ID, "4", lineID)
	in := application.ConsumeAllocationsInput{TenantID: f.tenant, ActorID: f.actor, ServiceDefinitionID: f.physioDefinition, Quantity: benefitdomain.MustQuantity("9"), ReasonCode: "CLAIM", Allocations: []application.ConsumptionAllocation{a, b}}
	out, err := consumeBatch(t, f, in)
	if err != nil || !out.OverConsumed || !out.Consumed.IsZero() || len(out.Draws) != 0 {
		t.Fatalf("shortage: result=%+v error=%v", out, err)
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Consumed, "0", "shortage makes no movements")
	// A release makes the ledger remainder smaller than the nominal item remainder.
	err = db.WithTenantTx(context.Background(), f.pool, db.TenantContext{TenantID: f.tenant}, func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.svc.ReleaseUnused(ctx, tx, application.ReleaseUnusedInput{TenantID: f.tenant, ActorID: f.actor, AuthorizationID: second.Authorization.ID, Quantity: benefitdomain.MustQuantity("2"), ReasonCode: "DISCHARGE"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	in.Quantity = benefitdomain.MustQuantity("7")
	in.Allocations[1].Quantity = benefitdomain.MustQuantity("2")
	out, err = consumeBatch(t, f, in)
	if err != nil || !out.OverConsumed || !out.Consumed.IsZero() {
		t.Fatalf("released hold: result=%+v error=%v", out, err)
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Consumed, "0", "released hold makes no movements")
}

func TestConsumeAllocationsPartialReplayRejected(t *testing.T) {
	f := newFixture(t)
	first := f.authorize(t, f.seedRequest(t, f.providerOr, line{f.physioDefinition, "2"}), "partial-first")
	second := f.authorize(t, f.seedRequest(t, f.providerOr, line{f.physioDefinition, "2"}), "partial-second")
	lineID := uuid.New()
	a := allocation(first.Authorization.ID, "1", lineID)
	b := allocation(second.Authorization.ID, "1", lineID)
	err := db.WithTenantTx(context.Background(), f.pool, db.TenantContext{TenantID: f.tenant}, func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.svc.Consume(ctx, tx, application.ConsumeInput{TenantID: f.tenant, ActorID: f.actor, AuthorizationID: a.AuthorizationID, ServiceDefinitionID: f.physioDefinition, Quantity: a.Quantity, Key: a.Key, ReasonCode: "CLAIM"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	in := application.ConsumeAllocationsInput{TenantID: f.tenant, ActorID: f.actor, ServiceDefinitionID: f.physioDefinition, Quantity: benefitdomain.MustQuantity("2"), ReasonCode: "CLAIM", Allocations: []application.ConsumptionAllocation{a, b}}
	_, err = consumeBatch(t, f, in)
	if !errors.Is(err, application.ErrAllocationReplay) {
		t.Fatalf("partial replay: %v", err)
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Consumed, "1", "partial replay makes no new movement")
}

func TestConsumeAllocationsCumulativeFactorRounding(t *testing.T) {
	f := newFixtureWithFactor(t, true, "0.333333")
	auth := f.authorize(t, f.seedRequest(t, f.providerOr, line{f.physioDefinition, "1"}), "rounding")
	for _, lineID := range []uuid.UUID{uuid.New(), uuid.New()} {
		a := allocation(auth.Authorization.ID, "0.5", lineID)
		in := application.ConsumeAllocationsInput{TenantID: f.tenant, ActorID: f.actor, ServiceDefinitionID: f.physioDefinition, Quantity: a.Quantity, ReasonCode: "CLAIM", Allocations: []application.ConsumptionAllocation{a}}
		out, err := consumeBatch(t, f, in)
		if err != nil || out.OverConsumed || out.Consumed.String() != "0.5" {
			t.Fatalf("fractional draw: result=%+v error=%v", out, err)
		}
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Consumed, "0.333333", "cumulative rounded draw")
	assertQuantity(t, f.balances(t, f.physioAccount).Reserved, "0", "cumulative rounded remainder")
}

func TestConsumeAllocationsLaterWriteFailureRollsBackEarlierDraw(t *testing.T) {
	f := newFixture(t)
	first := f.authorize(t, f.seedRequest(t, f.providerOr, line{f.physioDefinition, "2"}), "write-first")
	second := f.authorize(t, f.seedRequest(t, f.providerOr, line{f.physioDefinition, "2"}), "write-second")
	other := f.authorize(t, f.seedRequest(t, f.providerOr, line{f.physioDefinition, "2"}), "write-other")
	lineID := uuid.New()
	a := allocation(first.Authorization.ID, "1", lineID)
	b := allocation(second.Authorization.ID, "1", lineID)
	// The old single-hold API permits this account-level key. It makes the later
	// ledger write fail after the first batch draw has succeeded.
	err := db.WithTenantTx(context.Background(), f.pool, db.TenantContext{TenantID: f.tenant}, func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.svc.Consume(ctx, tx, application.ConsumeInput{TenantID: f.tenant, ActorID: f.actor, AuthorizationID: other.Authorization.ID, ServiceDefinitionID: f.physioDefinition, Quantity: benefitdomain.MustQuantity("1"), Key: b.Key, ReasonCode: "CLAIM"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	in := application.ConsumeAllocationsInput{TenantID: f.tenant, ActorID: f.actor, ServiceDefinitionID: f.physioDefinition, Quantity: benefitdomain.MustQuantity("2"), ReasonCode: "CLAIM", Allocations: []application.ConsumptionAllocation{a, b}}
	_, err = consumeBatch(t, f, in)
	if err == nil {
		t.Fatal("expected later ledger key conflict")
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Consumed, "1", "savepoint rolls back earlier draw")
}
