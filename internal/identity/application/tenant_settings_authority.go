package application

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
)

// TenantSettingsAuthority rechecks identity inside the settings transaction. The caller
// locks platform.tenant before invoking it; this is also the IAM command lock order.
type TenantSettingsAuthority interface {
	CheckTx(context.Context, pgx.Tx, identity.RequestContext, bool) (bool, error)
}
