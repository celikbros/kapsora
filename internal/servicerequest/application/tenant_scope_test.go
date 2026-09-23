package application_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/celikbros/kapsora/internal/servicerequest/application"
	"github.com/celikbros/kapsora/internal/servicerequest/domain"
)

func TestForeignTenantCannotReadOrMutateRequest(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	draft := f.create(t)
	rc := f.rc()
	rc.TenantID = f.h.CreateTenant("FOREIGN_REQUEST_SCOPE")
	rc.MembershipID = f.h.CreateMembership(rc.TenantID, f.actor)
	id, version := draft.Request.ID, draft.Request.RowVersion
	assertHidden := func(err error) {
		t.Helper()
		if !errors.Is(err, application.ErrRequestNotFound) {
			t.Fatalf("want request not found, got %v", err)
		}
	}
	_, err := f.svc.Get(ctx, rc, id)
	assertHidden(err)
	page, err := f.svc.List(ctx, rc, application.ListFilter{})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("foreign request visible in list")
	}
	_, err = f.svc.ListVersions(ctx, rc, id)
	assertHidden(err)
	_, err = f.svc.GetVersion(ctx, rc, id, 1)
	assertHidden(err)
	_, err = f.svc.GetStatusHistory(ctx, rc, id)
	assertHidden(err)
	_, err = f.svc.Patch(ctx, rc, id, application.PatchInput{ServiceDate: &draft.Request.ServiceDate, ExpectedVersion: version})
	assertHidden(err)
	line := draft.Items[0]
	_, err = f.svc.ReplaceItems(ctx, rc, id, []domain.ItemInput{{ServiceDefinitionID: line.ServiceDefinitionID.String(), RequestedQuantity: "1", UnitType: line.UnitType}}, version)
	assertHidden(err)
	_, err = f.svc.Submit(ctx, rc, id, nil, version)
	assertHidden(err)
	_, err = f.svc.Cancel(ctx, rc, id, application.ReasonInput{ReasonCode: "TEST_SCOPE", ExpectedVersion: version})
	assertHidden(err)
	current, err := f.svc.Get(ctx, f.rc(), id)
	if err != nil || !reflect.DeepEqual(current, draft) {
		t.Fatal("foreign commands changed the request")
	}
}
