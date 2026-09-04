package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/authorization/domain"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/identity"
)

// NewFulfilmentInput is the record command.
type NewFulfilmentInput struct {
	AuthorizationID   uuid.UUID
	ProviderProfileID *uuid.UUID
	LocationID        *uuid.UUID
	PractitionerID    *uuid.UUID
	PerformedAt       time.Time
	Items             []domain.FulfilmentItemInput
}

// ListFulfilments returns a page of fulfilments with the lines each delivered.
func (s *Service) ListFulfilments(ctx context.Context, rc identity.RequestContext, f FulfilmentFilter) (FulfilmentPage, error) {
	if err := domain.ValidateStatusFilter(f.Status, domain.FulfilmentStatuses); err != nil {
		return FulfilmentPage{}, err
	}
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return FulfilmentPage{}, err
	}

	var out FulfilmentPage
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := s.repo.ListFulfilments(ctx, tx, rc.TenantID, FulfilmentQuery{
			Scope: scopeOf(rc), Status: f.Status, AuthorizationID: f.AuthorizationID,
			ProviderProfileID: f.ProviderProfileID,
			PerformedFrom:     f.PerformedFrom, PerformedTo: f.PerformedTo,
			After: after, PageSize: pageSize + 1,
		})
		if err != nil {
			return err
		}
		if len(rows) > pageSize {
			out.NextCursor = s.cursors.Encode(fulfilmentCursor(rows[pageSize-1]))
			rows = rows[:pageSize]
		}
		out.Items = make([]FulfilmentView, 0, len(rows))
		for _, row := range rows {
			view, err := s.loadFulfilment(ctx, tx, rc.TenantID, row)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, view)
		}
		return nil
	})
	if err != nil {
		return FulfilmentPage{}, err
	}
	return out, nil
}

// GetFulfilment returns one fulfilment with its lines.
func (s *Service) GetFulfilment(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (FulfilmentView, error) {
	var out FulfilmentView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		view, err := s.reloadFulfilment(ctx, tx, rc.TenantID, id, scopeOf(rc))
		out = view
		return err
	})
	return out, err
}

// RecordFulfilment writes what is being delivered against an authorization. Nothing is
// consumed here: recording says a service happened, completing says the entitlement was
// spent on it, and keeping them apart is what lets a mistaken record be cancelled without
// a ledger reversal.
func (s *Service) RecordFulfilment(ctx context.Context, rc identity.RequestContext,
	in NewFulfilmentInput,
) (FulfilmentView, error) {
	if err := domain.ValidateNewFulfilment(domain.NewFulfilment{
		PerformedAt: in.PerformedAt, Items: in.Items,
	}); err != nil {
		return FulfilmentView{}, err
	}

	var out FulfilmentView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		scope := scopeOf(rc)
		authorization, err := s.repo.LockAuthorization(ctx, tx, rc.TenantID, in.AuthorizationID, scope)
		if err != nil {
			return err
		}
		record, err := s.recordFulfilment(ctx, tx, rc, authorization, in)
		if err != nil {
			return err
		}
		out, err = s.reloadFulfilment(ctx, tx, rc.TenantID, record.ID, scope)
		return err
	})
	return out, err
}

// recordFulfilment is the body RecordFulfilment and RedeemVoucher share: both write the
// same rows, and a voucher redemption is exactly "this fulfilment, and the token that
// authorised it, in one transaction".
func (s *Service) recordFulfilment(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	authorization AuthorizationRecord, in NewFulfilmentInput,
) (FulfilmentRecord, error) {
	if !domain.Open(authorization.Status) {
		return FulfilmentRecord{}, ErrAuthorizationNotActive
	}
	lines, err := s.repo.ListAuthorizationItems(ctx, tx, rc.TenantID, authorization.ID, true)
	if err != nil {
		return FulfilmentRecord{}, err
	}
	byID := make(map[uuid.UUID]AuthorizationItemRecord, len(lines))
	for _, line := range lines {
		byID[line.ID] = line
	}

	rows := make([]NewFulfilmentItemRow, 0, len(in.Items))
	for _, item := range in.Items {
		itemID, err := uuid.Parse(item.AuthorizationItemID)
		if err != nil {
			return FulfilmentRecord{}, ErrItemNotInAuthorization
		}
		line, ok := byID[itemID]
		if !ok {
			return FulfilmentRecord{}, ErrItemNotInAuthorization
		}
		quantity, err := benefitdomain.ParseQuantity(item.ActualQuantity)
		if err != nil {
			return FulfilmentRecord{}, fieldError("items.actualQuantity", "FORMAT",
				"kesin ondalık bir sayı olmalı")
		}
		remaining, err := remainderOf(line)
		if err != nil {
			return FulfilmentRecord{}, err
		}
		// The same ceiling is checked again when the fulfilment completes, against the
		// counters as they are then. Checking it here as well means a record that could
		// never be completed is refused while somebody is still at the counter.
		if quantity.Cmp(remaining) > 0 {
			return FulfilmentRecord{}, ErrOverFulfilment
		}
		row := NewFulfilmentItemRow{
			AuthorizationItemID: line.ID, ServiceDefinitionID: line.ServiceDefinitionID,
			ActualQuantity: quantity,
		}
		if item.ActualAmount != "" {
			amount, err := benefitdomain.ParseQuantity(item.ActualAmount)
			if err != nil {
				return FulfilmentRecord{}, fieldError("items.actualAmount", "FORMAT",
					"kesin ondalık bir sayı olmalı")
			}
			row.ActualAmount = &amount
		}
		rows = append(rows, row)
	}

	record, err := s.createFulfilmentWithReference(ctx, tx, rc, NewFulfilmentRow{
		AuthorizationID: authorization.ID, ProviderProfileID: in.ProviderProfileID,
		LocationID: in.LocationID, PractitionerID: in.PractitionerID,
		PerformedAt: in.PerformedAt.UTC(), ActorID: actorPtr(rc.Principal.ActorID),
	})
	if err != nil {
		return FulfilmentRecord{}, err
	}
	for _, row := range rows {
		row.FulfilmentID = record.ID
		if _, err := s.repo.CreateFulfilmentItem(ctx, tx, rc.TenantID, row); err != nil {
			return FulfilmentRecord{}, err
		}
	}
	if err := s.record(ctx, tx, rc, "fulfilment.record", "fulfilment", record.ID, map[string]any{
		"authorization_id": authorization.ID.String(), "reference": record.Reference,
		"item_count": len(rows),
	}); err != nil {
		return FulfilmentRecord{}, err
	}
	return record, nil
}

func (s *Service) createFulfilmentWithReference(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, row NewFulfilmentRow,
) (FulfilmentRecord, error) {
	for attempt := 0; attempt < referenceAttempts; attempt++ {
		reference, err := newReference("FUL", s.now())
		if err != nil {
			return FulfilmentRecord{}, err
		}
		row.Reference = reference
		record, err := s.repo.CreateFulfilment(ctx, tx, rc.TenantID, row)
		switch {
		case err == nil:
			return record, nil
		case errors.Is(err, ErrReferenceCollision):
			continue
		default:
			return FulfilmentRecord{}, err
		}
	}
	return FulfilmentRecord{}, ErrReferenceCollision
}

// CompleteFulfilment consumes what the recorded lines actually delivered, item by item,
// from the hold each authorization line took. Consuming less than was approved leaves the
// remainder reserved until the authorization expires or is cancelled: a member who was
// promised six sessions and had four still holds the other two.
func (s *Service) CompleteFulfilment(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	expected int64,
) (FulfilmentView, error) {
	var out FulfilmentView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		scope := scopeOf(rc)
		fulfilment, err := s.repo.LockFulfilment(ctx, tx, rc.TenantID, id, scope)
		if err != nil {
			return err
		}
		if fulfilment.Status != domain.FulfilmentRecorded {
			return ErrFulfilmentNotRecorded
		}
		if fulfilment.RowVersion != expected {
			return ErrVersionMismatch
		}
		authorization, err := s.repo.LockAuthorization(ctx, tx, rc.TenantID, fulfilment.AuthorizationID, scope)
		if err != nil {
			return err
		}
		if !domain.Open(authorization.Status) {
			return ErrAuthorizationNotActive
		}
		consumed, err := s.consume(ctx, tx, rc, fulfilment)
		if err != nil {
			return err
		}
		if err := s.repo.ApplyConsumption(ctx, tx, rc.TenantID, authorization.ID, consumed,
			actorPtr(rc.Principal.ActorID)); err != nil {
			return err
		}
		if err := s.repo.CompleteFulfilment(ctx, tx, rc.TenantID, id, s.now().UTC(), expected); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "fulfilment.complete", "fulfilment", id, map[string]any{
			"authorization_id": authorization.ID.String(), "consumed_total": consumed.String(),
		}); err != nil {
			return err
		}
		out, err = s.reloadFulfilment(ctx, tx, rc.TenantID, id, scope)
		return err
	})
	return out, err
}

// consume moves each delivered quantity out of the hold its line took and reports the
// total. The authorization lines are taken FOR UPDATE, so the remainder checked here is
// one nobody else can change before this transaction commits.
func (s *Service) consume(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	fulfilment FulfilmentRecord,
) (benefitdomain.Quantity, error) {
	items, err := s.repo.ListFulfilmentItems(ctx, tx, rc.TenantID, fulfilment.ID)
	if err != nil {
		return benefitdomain.Quantity{}, err
	}
	lines, err := s.repo.ListAuthorizationItems(ctx, tx, rc.TenantID, fulfilment.AuthorizationID, true)
	if err != nil {
		return benefitdomain.Quantity{}, err
	}
	byID := make(map[uuid.UUID]AuthorizationItemRecord, len(lines))
	for _, line := range lines {
		byID[line.ID] = line
	}

	total := benefitdomain.ZeroQuantity()
	for _, item := range items {
		line, ok := byID[item.AuthorizationItemID]
		if !ok {
			return benefitdomain.Quantity{}, ErrItemNotInAuthorization
		}
		quantity, err := benefitdomain.ParseQuantity(item.ActualQuantity)
		if err != nil {
			return benefitdomain.Quantity{}, err
		}
		remaining, err := remainderOf(line)
		if err != nil {
			return benefitdomain.Quantity{}, err
		}
		if quantity.Cmp(remaining) > 0 {
			return benefitdomain.Quantity{}, ErrOverFulfilment
		}
		if line.ReservationID == nil {
			return benefitdomain.Quantity{}, ErrAccountNotFound
		}
		if _, err := s.ledger.Consume(ctx, tx, ledger.MovementInput{
			TenantID: rc.TenantID, ReservationID: *line.ReservationID, Quantity: quantity,
			Key: consumeKey(item.ID), ReasonCode: "FULFILMENT", ActorID: rc.Principal.ActorID,
		}); err != nil {
			return benefitdomain.Quantity{}, err
		}
		if err := s.repo.AddAuthorizationItemConsumption(ctx, tx, rc.TenantID, line.ID, quantity); err != nil {
			return benefitdomain.Quantity{}, err
		}
		total = total.Add(quantity)
	}
	return total, nil
}

// CancelFulfilment withdraws a record that never became a delivery. A completed
// fulfilment is refused: the ledger is append-only and undoing a consumption is a
// reversal movement, not a status change.
func (s *Service) CancelFulfilment(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in ReasonInput,
) (FulfilmentView, error) {
	if err := domain.ValidateReason(in.ReasonCode, in.ReasonText); err != nil {
		return FulfilmentView{}, err
	}

	var out FulfilmentView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		scope := scopeOf(rc)
		fulfilment, err := s.repo.LockFulfilment(ctx, tx, rc.TenantID, id, scope)
		if err != nil {
			return err
		}
		switch {
		case fulfilment.Status == domain.FulfilmentCompleted:
			return ErrFulfilmentCompleted
		case fulfilment.Status != domain.FulfilmentRecorded:
			return ErrFulfilmentNotRecorded
		}
		if fulfilment.RowVersion != in.ExpectedVersion {
			return ErrVersionMismatch
		}
		if err := s.repo.CancelFulfilment(ctx, tx, rc.TenantID, id, s.now().UTC(),
			in.ReasonCode, in.ExpectedVersion); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "fulfilment.cancel", "fulfilment", id, map[string]any{
			"authorization_id": fulfilment.AuthorizationID.String(), "reason_code": in.ReasonCode,
		}); err != nil {
			return err
		}
		out, err = s.reloadFulfilment(ctx, tx, rc.TenantID, id, scope)
		return err
	})
	return out, err
}
