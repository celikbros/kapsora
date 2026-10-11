package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/authorization/application"
	"github.com/celikbros/kapsora/internal/platform/db"
)

func TestCreateAndExtendValidityInCallerTransaction(t *testing.T) {
	f := newFixture(t)
	request := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "3"})
	in := application.NewAuthorizationInput{
		RequestID: request, ValidTo: testNow.Add(24 * time.Hour), IdempotencyKey: "atomic-stay",
	}
	stop := errors.New("caller rollback")
	run := func(fn func(context.Context, pgx.Tx) error) error {
		return db.WithTenantTx(context.Background(), f.pool, db.TenantContext{TenantID: f.tenant}, fn)
	}
	err := run(func(ctx context.Context, tx pgx.Tx) error {
		if _, err := f.svc.CreateInTx(ctx, tx, f.rc(), in); err != nil {
			return err
		}
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("create rollback: %v", err)
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Reserved, "0", "rolled back create")
	var id string
	err = f.h.Admin.QueryRow(context.Background(), `SELECT count(*)::text FROM service.authorization WHERE tenant_id=$1`, f.tenant).Scan(&id)
	if err != nil || id != "0" {
		t.Fatalf("create persisted after rollback: count=%s err=%v", id, err)
	}
	var original application.AuthorizationView
	err = run(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		original, err = f.svc.CreateInTx(ctx, tx, f.rc(), in)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Reserved, "3", "committed create")
	later := in.ValidTo.Add(3 * time.Hour)
	err = run(func(ctx context.Context, tx pgx.Tx) error {
		if err := f.svc.ExtendValidityInTx(ctx, tx, f.rc(), original.Authorization.ID, later, "STAY_EXTENSION"); err != nil {
			return err
		}
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("extension rollback: %v", err)
	}
	current, err := f.svc.Get(context.Background(), f.rc(), original.Authorization.ID)
	if err != nil || !current.Authorization.ValidTo.Equal(original.Authorization.ValidTo) {
		t.Fatalf("validity changed after rollback: %v", err)
	}
	err = run(func(ctx context.Context, tx pgx.Tx) error {
		if err := f.svc.ExtendValidityInTx(ctx, tx, f.rc(), original.Authorization.ID, later, "STAY_EXTENSION"); err != nil {
			return err
		}
		return f.svc.ExtendValidityInTx(ctx, tx, f.rc(), original.Authorization.ID, later, "STAY_EXTENSION")
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err = f.svc.Get(context.Background(), f.rc(), original.Authorization.ID)
	if err != nil || !current.Authorization.ValidTo.Equal(later) || current.Authorization.RowVersion != original.Authorization.RowVersion+1 {
		t.Fatalf("extension not exactly once: %+v %v", current.Authorization, err)
	}
}
