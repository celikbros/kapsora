package application_test

import (
	"testing"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
)

func TestTenantDirectoryCapabilityRequiresPermissionOnSameTenantGrant(t *testing.T) {
	g := application.Grants{Items: []application.Grant{
		{Scope: identity.Scope{Type: application.ScopeTenant}, Permissions: []string{"audit.read"}},
		{Scope: identity.Scope{Type: application.ScopeOrganization}, Permissions: []string{"identity.user.read"}},
	}}
	if g.CanReadTenantUsers() {
		t.Fatal("unrelated tenant scope and provider permission were combined")
	}
	g.Items = append(g.Items, application.Grant{Scope: identity.Scope{Type: application.ScopeTenant}, Permissions: []string{"identity.user.read"}})
	if !g.CanReadTenantUsers() {
		t.Fatal("correlated tenant read grant was denied")
	}
}
