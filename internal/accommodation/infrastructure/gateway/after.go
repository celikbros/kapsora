package accommodationgw

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	accommodationapp "github.com/celikbros/kapsora/internal/accommodation/application"
	authorizationapp "github.com/celikbros/kapsora/internal/authorization/application"
	authorizationdomain "github.com/celikbros/kapsora/internal/authorization/domain"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/identity"
	servicerequestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
)

// The half of the bridge WP-I6-03 needs: withdrawing a reservation request, redeeming a
// voucher, spending and giving back nights, and reading what a promise actually covered.
//
// Nothing here decides anything either. The voucher's window and digest are WP-I4-02's, the
// over-consumption rule is WP-I4-02's, and whether a request may still be cancelled is
// WP-I4-01's. What these adapters do is speak both vocabularies -- nights and rooms on one
// side, quantities and lines on the other -- so neither module has to know the other exists.

// cancelReasonText is what the reservation request's own cancellation records. It is a
// sentence for a person reading the request page, next to the code, because "MEMBER
// CANCELLED" on its own does not say which member or what they cancelled.
const cancelReasonText = "Konaklama rezervasyonu iptal edildi."

// CancelReservation implements accommodationapp.RequestPort.
//
// The request is read first for its row version, because WP-I4-01's cancel is an optimistic
// write and this caller has no version of its own to send. A request that is already
// cancelled, or already decided, is not an error: the booking is being cancelled either way,
// and a member must not be left unable to cancel because a reviewer got there first.
func (r *Requests) CancelReservation(ctx context.Context, rc identity.RequestContext,
	requestID uuid.UUID, reasonCode string,
) error {
	view, err := r.svc.Get(ctx, rc, requestID)
	if err != nil {
		if errors.Is(err, servicerequestapp.ErrRequestNotFound) {
			return nil
		}
		return err
	}
	_, err = r.svc.Cancel(ctx, rc, requestID, servicerequestapp.ReasonInput{
		ReasonCode: reasonCode, ReasonText: reasonTextPtr(),
		ExpectedVersion: view.Request.RowVersion,
	})
	if err == nil {
		return nil
	}
	if errors.Is(err, servicerequestapp.ErrTransitionInvalid) {
		// Somebody decided it while the member was looking at the cancel button. The
		// booking's own cancellation is what the member asked for and it goes ahead.
		return nil
	}
	return err
}

func reasonTextPtr() *string {
	text := cancelReasonText
	return &text
}

// RedeemVoucherToken implements accommodationapp.AuthorizationPort.
//
// It runs in the caller's transaction, so a check-in that rolls back leaves the code still
// usable and one that commits leaves it spent. WP-I4-02 answers a token that belongs to a
// different promise with "not found", which is what the desk sees: a refusal that could tell
// a real code for another stay apart from one that does not exist would be an oracle for
// testing stolen codes.
func (a *Authorizations) RedeemVoucherToken(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, in accommodationapp.BookingRedeemInput,
) error {
	_, err := a.svc.RedeemToken(ctx, tx, rc, authorizationapp.RedeemTokenInput{
		Token: in.Token, AuthorizationID: in.AuthorizationID, RedeemedAt: in.RedeemedAt,
	})
	return voucherRefusal(err)
}

// voucherRefusal translates WP-I4-02's voucher errors into the accommodation module's own,
// so the desk gets a refusal in its own vocabulary and the accommodation transport does not
// have to import the authorization module's error surface to render one.
//
// Each refusal stays separate. "This does not work" tells the person holding the code
// nothing they can act on, and "come back tomorrow", "you already used it" and "this was
// cancelled" are three different things to do next.
func voucherRefusal(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, authorizationapp.ErrVoucherNotFound):
		return accommodationapp.ErrVoucherNotFound
	case errors.Is(err, authorizationapp.ErrVoucherAlreadyRedeemed):
		return accommodationapp.ErrVoucherAlreadyRedeemed
	case errors.Is(err, authorizationapp.ErrVoucherRevoked):
		return accommodationapp.ErrVoucherRevoked
	case errors.Is(err, authorizationapp.ErrVoucherExpired):
		return accommodationapp.ErrVoucherExpired
	default:
		return err
	}
}

// RevokeVouchers implements accommodationapp.AuthorizationPort.
func (a *Authorizations) RevokeVouchers(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, authorizationID uuid.UUID, reasonCode string,
) error {
	return a.svc.RevokeVouchersInTx(ctx, tx, rc, authorizationID, reasonCode)
}

// ReleaseUnused implements accommodationapp.AuthorizationPort. It runs in the caller's
// transaction, so a cancellation or a check-out and the entitlement it gives back commit
// together or not at all.
func (a *Authorizations) ReleaseUnused(ctx context.Context, tx pgx.Tx,
	in accommodationapp.BookingReleaseInput,
) (string, error) {
	quantity, err := benefitdomain.ParseQuantity(in.Nights)
	if err != nil {
		return "", fmt.Errorf("accommodation: release quantity %q: %w", in.Nights, err)
	}
	released, err := a.svc.ReleaseUnused(ctx, tx, authorizationapp.ReleaseUnusedInput{
		TenantID: in.TenantID, ActorID: in.ActorID, AuthorizationID: in.AuthorizationID,
		Quantity: quantity, ReasonCode: in.ReasonCode,
	})
	if err != nil {
		return "", err
	}
	return released.String(), nil
}

// Consume implements accommodationapp.AuthorizationPort.
//
// An over-consumption comes back as an error rather than as a quantity, because there is no
// sensible thing for a cancellation to do with "I took less than you asked for": the penalty
// the member has been told about is a number, and taking a different one silently would make
// the ledger and the cancellation row disagree.
func (a *Authorizations) Consume(ctx context.Context, tx pgx.Tx,
	in accommodationapp.BookingConsumeInput,
) (string, error) {
	quantity, err := benefitdomain.ParseQuantity(in.Nights)
	if err != nil {
		return "", fmt.Errorf("accommodation: consume quantity %q: %w", in.Nights, err)
	}
	result, err := a.svc.Consume(ctx, tx, authorizationapp.ConsumeInput{
		TenantID: in.TenantID, ActorID: in.ActorID, AuthorizationID: in.AuthorizationID,
		ServiceDefinitionID: in.ServiceDefinitionID, Quantity: quantity,
		Key: in.Key, ReasonCode: in.ReasonCode,
	})
	if err != nil {
		return "", err
	}
	if !result.Matched {
		// The authorization approved no line for this room's service. There is nothing to
		// take, and pretending otherwise would be a penalty with no hold behind it.
		return "0", nil
	}
	if result.OverConsumed {
		return "", fmt.Errorf(
			"accommodation: the penalty of %s nights is more than the %s the authorization still holds",
			in.Nights, result.Remaining.String())
	}
	return result.Consumed.String(), nil
}

// RecordStayFulfilment implements accommodationapp.AuthorizationPort.
//
// The authorization's own line for the room's service is found here rather than named by the
// caller: an authorization approves a service once, and a booking that could name a line
// could record a stay against somebody else's promise.
func (a *Authorizations) RecordStayFulfilment(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, in accommodationapp.BookingFulfilmentInput,
) error {
	lines, err := a.svc.AuthorizationLines(ctx, tx, rc.TenantID, in.AuthorizationID)
	if err != nil {
		return err
	}
	for _, line := range lines {
		if line.ServiceDefinitionID != in.ServiceDefinitionID {
			continue
		}
		_, _, err := a.svc.RecordAndCompleteInTx(ctx, tx, rc, authorizationapp.NewFulfilmentInput{
			AuthorizationID:   in.AuthorizationID,
			ProviderProfileID: in.ProviderProfileID,
			PerformedAt:       in.PerformedAt,
			Items: []authorizationdomain.FulfilmentItemInput{{
				AuthorizationItemID: line.ID.String(), ActualQuantity: in.Nights,
			}},
		})
		return err
	}
	// The authorization approved no line for this room's service. There is nothing to
	// record against, and inventing one would be a delivery nobody promised.
	return nil
}

// Lines implements accommodationapp.AuthorizationPort.
func (a *Authorizations) Lines(ctx context.Context, tx pgx.Tx, tenantID,
	authorizationID uuid.UUID,
) ([]accommodationapp.BookingAuthorizationLine, error) {
	lines, err := a.svc.AuthorizationLines(ctx, tx, tenantID, authorizationID)
	if err != nil {
		return nil, err
	}
	out := make([]accommodationapp.BookingAuthorizationLine, 0, len(lines))
	for _, line := range lines {
		out = append(out, accommodationapp.BookingAuthorizationLine{
			ID: line.ID, ServiceDefinitionID: line.ServiceDefinitionID,
			Approved: line.Approved.String(), Remaining: line.Remaining.String(),
		})
	}
	return out, nil
}
