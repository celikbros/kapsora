package memberimport

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// HandleStaged validates and matches a batch that was too large to finish inside the
// upload request. It is registered on kapsora-worker for StagedEvent.
func (s *Service) HandleStaged(ctx context.Context, d outbox.Delivery) error {
	tenantID, batchID, err := deliveryTarget(d)
	if err != nil {
		return outbox.Permanent(err)
	}
	return classify(s.RunValidation(ctx, tenantID, batchID))
}

// HandleApply writes an accepted batch to the live tables. It is registered on
// kapsora-worker for ApplyEvent and is safe to redeliver: an APPLIED batch is a no-op and
// a half-applied one resumes at the first row that is still VALID or MATCHED.
func (s *Service) HandleApply(ctx context.Context, d outbox.Delivery) error {
	tenantID, batchID, err := deliveryTarget(d)
	if err != nil {
		return outbox.Permanent(err)
	}
	return classify(s.RunApply(ctx, tenantID, batchID))
}

// deliveryTarget reads the tenant and batch of a delivery. The aggregate id is the batch,
// so a payload that lost its field still routes correctly.
func deliveryTarget(d outbox.Delivery) (tenantID, batchID uuid.UUID, err error) {
	if !d.TenantID.Valid || d.TenantID.UUID == uuid.Nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("memberimport: event %s has no tenant", d.ID)
	}
	if d.AggregateID == uuid.Nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("memberimport: event %s has no batch id", d.ID)
	}
	return d.TenantID.UUID, d.AggregateID, nil
}

// classify turns the states a job can legitimately not recover from into permanent
// failures, so the dispatcher dead-letters them instead of retrying for hours.
func classify(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrStateInvalid):
		return outbox.Permanent(err)
	default:
		return err
	}
}
