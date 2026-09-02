package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestRequireAndStepUp(t *testing.T) {
	ctx := context.Background()
	if _, err := Require(ctx, "organization.read"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("no context: err = %v", err)
	}

	rc := RequestContext{
		TenantID:    uuid.New(),
		Permissions: map[string]struct{}{"organization.read": {}},
	}
	ctx = WithRequestContext(ctx, rc)

	if _, err := Require(ctx, "organization.read"); err != nil {
		t.Fatalf("granted permission rejected: %v", err)
	}
	if _, err := Require(ctx, "organization.manage"); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("missing permission: err = %v", err)
	}
	if _, err := RequireStepUp(ctx, "organization.read"); !errors.Is(err, ErrStepUpRequired) {
		t.Fatalf("without step-up: err = %v", err)
	}

	rc.StepUpValid = true
	if _, err := RequireStepUp(WithRequestContext(ctx, rc), "organization.read"); err != nil {
		t.Fatalf("with step-up: %v", err)
	}
}
