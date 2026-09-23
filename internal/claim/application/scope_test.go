package application_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/claim/application"
	"github.com/celikbros/kapsora/internal/identity"
)

// Exercise actual tenant-bound SQL with a valid record and identical permissions.
// Not-found must precede draft mutation, evaluation or readiness disclosure.
func TestClaimTenantAndProviderBoundariesPreserveDraft(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	line := f.consultLine(1)
	draft := f.newClaim(t, []application.NewLineInput{line}, nil)
	id, version := draft.Claim.ID, draft.Claim.RowVersion
	foreignTenant := f.providerRC()
	foreignTenant.TenantID = f.h.CreateTenant("OTHER_CLAIM_SCOPE")
	foreignTenant.MembershipID = f.h.CreateMembership(foreignTenant.TenantID, f.actor)
	foreignProvider := f.providerRC()
	foreignProvider.Scopes = []identity.Scope{{Type: "ORGANIZATION", ID: uuid.NullUUID{UUID: f.otherOrg, Valid: true}}}
	for name, rc := range map[string]identity.RequestContext{"tenant": foreignTenant, "provider": foreignProvider} {
		t.Run(name, func(t *testing.T) {
			assertHidden := func(err error) {
				t.Helper()
				if !errors.Is(err, application.ErrClaimNotFound) {
					t.Fatalf("want not found, got %v", err)
				}
			}
			_, err := f.claims.GetClaim(ctx, rc, id, application.AccessRequest{})
			assertHidden(err)
			rows, err := f.claims.ListClaims(ctx, rc, application.ClaimFilter{}, application.AccessRequest{})
			if err != nil || len(rows.Items) != 0 {
				t.Fatal("foreign claim visible in list")
			}
			_, err = f.claims.ListVersions(ctx, rc, id)
			assertHidden(err)
			_, err = f.claims.GetVersion(ctx, rc, id, 1, application.AccessRequest{})
			assertHidden(err)
			_, err = f.claims.InvoiceReadiness(ctx, rc, id)
			assertHidden(err)
			_, err = f.claims.PatchDraft(ctx, rc, id, application.DraftInput{ServiceDateFrom: serviceDay, ServiceDateTo: serviceDay, ExpectedVersion: version})
			assertHidden(err)
			_, err = f.claims.PutLines(ctx, rc, id, []application.NewLineInput{line}, version)
			assertHidden(err)
			_, err = f.claims.Submit(ctx, rc, id, version, application.AccessRequest{})
			assertHidden(err)
			_, err = f.claims.Cancel(ctx, rc, id, application.ReasonInput{ReasonCode: "TEST_SCOPE", ExpectedVersion: version})
			assertHidden(err)
		})
	}
	current, err := f.claims.GetClaim(ctx, f.providerRC(), id, application.AccessRequest{})
	if err != nil || !reflect.DeepEqual(current, draft) {
		t.Fatal("foreign operations changed the draft")
	}
}
