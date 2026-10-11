package authorizationpg

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/authorization/application"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// FindConsumption reads only the exact draw associated with this authorization hold.
func (Repository) FindConsumption(ctx context.Context, tx pgx.Tx, tenant, authorization, reservation uuid.UUID, key, reason string) (application.ConsumptionRecord, bool, error) {
	r, err := sqlcgen.New(tx).FindAuthorizationConsumption(ctx, sqlcgen.FindAuthorizationConsumptionParams{TenantID: tenant, AuthorizationID: authorization, ReservationID: uuid.NullUUID{UUID: reservation, Valid: true}, Key: key, ReasonCode: &reason})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ConsumptionRecord{}, false, nil
	}
	if err != nil {
		return application.ConsumptionRecord{}, false, err
	}
	quantity, err := benefitdomain.ParseQuantity(r.Quantity)
	return application.ConsumptionRecord{ID: r.ID, Quantity: quantity, Reversed: r.Reversed}, true, err
}

// RestoreConsumption keeps terminal authorizations terminal and restores counters atomically.
func (Repository) RestoreConsumption(ctx context.Context, tx pgx.Tx, tenant, id uuid.UUID, quantity benefitdomain.Quantity, now time.Time, actor *uuid.UUID) error {
	who := uuid.NullUUID{}
	if actor != nil {
		who = uuid.NullUUID{UUID: *actor, Valid: true}
	}
	n, err := sqlcgen.New(tx).RestoreAuthorizationConsumption(ctx, sqlcgen.RestoreAuthorizationConsumptionParams{TenantID: tenant, ID: id, Quantity: quantity.String(), Now: now, ActorID: who})
	if err != nil {
		return err
	}
	if n != 1 {
		return application.ErrAuthorizationNotActive
	}
	return nil
}
