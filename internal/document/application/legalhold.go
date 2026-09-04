package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/document/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// NewLegalHoldInput names what is being held and why. Exactly one of the three targets is
// enough; a hold that names none would look like protection and protect nothing.
type NewLegalHoldInput struct {
	ObjectID      *uuid.UUID
	PersonID      *uuid.UUID
	AggregateType string
	AggregateID   *uuid.UUID
	Reason        string
}

// PutLegalHold places a hold. From this moment nothing deletes what it covers — not
// retention, not a purge, not an operator. That is the whole point of the table, and it is
// enforced where deletion happens rather than by asking callers to remember.
func (s *Service) PutLegalHold(ctx context.Context, rc identity.RequestContext,
	in NewLegalHoldInput,
) (LegalHoldRecord, error) {
	if err := domain.ValidateLegalHold(in.Reason, in.AggregateType,
		in.ObjectID != nil, in.PersonID != nil, in.AggregateID != nil); err != nil {
		return LegalHoldRecord{}, err
	}

	var out LegalHoldRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		// A hold over a document the caller cannot see is refused the same way reading it
		// would be: it is not found. Otherwise placing holds would be a way of learning
		// which documents exist outside the caller's scope.
		if in.ObjectID != nil {
			if _, err := s.repo.GetObject(ctx, tx, rc.TenantID, *in.ObjectID, scopeOf(rc)); err != nil {
				return err
			}
		}
		var err error
		out, err = s.repo.CreateLegalHold(ctx, tx, rc.TenantID, NewLegalHoldRow{
			ObjectID: in.ObjectID, PersonID: in.PersonID,
			AggregateType: optionalPtr(in.AggregateType), AggregateID: in.AggregateID,
			Reason: in.Reason, ActorID: actorPtr(rc.Principal.ActorID),
		})
		if err != nil {
			return err
		}
		// The reason is a person's free text and stays out of the detail; what is
		// recorded is which target was held, which is what a report groups by.
		return s.record(ctx, tx, rc, "document.legal_hold.place", holdResource(out),
			map[string]any{
				"legal_hold_id": out.ID, "object_id": in.ObjectID,
				"person_id": in.PersonID, "aggregate_type": in.AggregateType,
				"aggregate_id": in.AggregateID,
			})
	})
	if err != nil {
		return LegalHoldRecord{}, err
	}
	return out, nil
}

// ReleaseLegalHold lifts a hold, after which retention may act on what it covered again.
// Releasing is recorded with who did it: a hold that could be lifted anonymously would be
// no protection at all.
func (s *Service) ReleaseLegalHold(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID, expected int64,
) (LegalHoldRecord, error) {
	var out LegalHoldRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		hold, err := s.repo.GetLegalHold(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		if hold.ReleasedAt != nil {
			return ErrHoldReleased
		}
		if expected > 0 && hold.RowVersion != expected {
			return ErrVersionMismatch
		}
		at := s.now()
		released, err := s.repo.ReleaseLegalHold(ctx, tx, rc.TenantID, id,
			actorPtr(rc.Principal.ActorID), at, hold.RowVersion)
		if err != nil {
			return err
		}
		if !released {
			return ErrVersionMismatch
		}
		hold.ReleasedAt = &at
		hold.ReleasedBy = actorPtr(rc.Principal.ActorID)
		hold.RowVersion++
		out = hold
		return s.record(ctx, tx, rc, "document.legal_hold.release", holdResource(hold),
			map[string]any{
				"legal_hold_id": hold.ID, "object_id": hold.ObjectID,
				"person_id": hold.PersonID, "aggregate_id": hold.AggregateID,
			})
	})
	if err != nil {
		return LegalHoldRecord{}, err
	}
	return out, nil
}

// holdResource is the id the audit row points at: the document when the hold names one,
// otherwise the hold itself, so a resource is never empty.
func holdResource(hold LegalHoldRecord) uuid.UUID {
	if hold.ObjectID != nil {
		return *hold.ObjectID
	}
	return hold.ID
}
