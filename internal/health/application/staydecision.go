package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/health/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// The request statuses this subscriber acts on. A partially approved request is approved for
// fewer days, which is still an approved admission: the reviewer said yes to four of the five
// days, and the hold is taken for four.
const (
	requestApproved          = "APPROVED"
	requestPartiallyApproved = "PARTIALLY_APPROVED"
	requestRejected          = "REJECTED"
)

// The reason codes the authorization movements of this package are recorded under.
const (
	extendReasonCode = "STAY_EXTENSION"
)

// decidedPayload is the part of WP-I4-01's `service_request.decided` payload this package
// reads. Everything else in it is somebody else's business, and a consumer that unmarshalled
// the whole thing would be a consumer that broke when a field was added.
type decidedPayload struct {
	ServiceRequestID uuid.UUID `json:"serviceRequestId"`
	Status           string    `json:"status"`
}

// HandleServiceRequestDecided is the seam of section 2.2: a reviewer decides a request on the
// request page, and the admission behind it follows without the reviewer knowing an admission
// exists.
//
// It is a subscription rather than a call from WP-I4-01 for two reasons. The reviewer's
// decision must not be able to fail because this package is slow, down, or has a bug in it.
// And WP-I4-01 must not have to know that admissions exist at all — the next thing that hangs
// off a decision subscribes too, and nothing in the request module changes.
//
// **It is idempotent, and not by trying to be.** The outbox delivers at least once, so every
// write below is guarded by a predicate rather than by a flag: `AuthorizeStay` names
// REQUESTED, `ApproveExtension` names REQUESTED, and the authorization is created under an
// idempotency key derived from the stay rather than from a clock, so a redelivery finds the
// hold the first delivery took instead of reserving the same entitlement twice.
//
// An event about a request no admission hangs off is not an error and is not a warning: most
// requests are not admissions, and this handler is one of several that will each recognise
// their own.
func (s *Service) HandleServiceRequestDecided(ctx context.Context, d outbox.Delivery) error {
	if s.stayRepo == nil {
		return nil
	}
	if !d.TenantID.Valid {
		return outbox.Permanent(errors.New("health: a decided request event carries no tenant"))
	}
	var payload decidedPayload
	if err := json.Unmarshal(d.Payload, &payload); err != nil {
		return outbox.Permanent(fmt.Errorf("health: decode decided request event: %w", err))
	}
	if payload.ServiceRequestID == uuid.Nil {
		return outbox.Permanent(errors.New("health: a decided request event names no request"))
	}
	tenantID := d.TenantID.UUID
	rc := systemContext(tenantID)

	stay, extension, err := s.decisionSubject(ctx, rc, payload.ServiceRequestID)
	if err != nil {
		return err
	}
	switch {
	case stay != nil:
		return s.applyStayDecision(ctx, rc, *stay, payload.Status)
	case extension != nil:
		return s.applyExtensionDecision(ctx, rc, *extension, payload.Status)
	default:
		return nil
	}
}

// systemContext is the caller this handler acts as: the tenant, and nobody in particular.
// There is no actor because there is no person — the reviewer's decision is already recorded
// on the request, and attributing the stay's move to them would say they moved something they
// have never seen.
//
// It holds no organization scope, which is what lets it find a stay whichever provider
// admitted the person. A worker bound to one provider would silently leave every other
// tenant's admission in REQUESTED forever.
func systemContext(tenantID uuid.UUID) identity.RequestContext {
	return identity.RequestContext{TenantID: tenantID}
}

// decisionSubject locates the stay or extension named by a request. Its short
// transaction releases the lookup lock before the decision transaction begins;
// the latter locks and rereads the parent stay before any funding or transition.
func (s *Service) decisionSubject(ctx context.Context, rc identity.RequestContext,
	requestID uuid.UUID,
) (*StayRecord, *StayExtensionRecord, error) {
	var (
		stay      *StayRecord
		extension *StayExtensionRecord
	)
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.stayRepo.LockStayByRequest(ctx, tx, rc.TenantID, requestID)
		switch {
		case err == nil:
			stay = &record
			return nil
		case !errors.Is(err, ErrStayNotFound):
			return err
		}
		row, err := s.stayRepo.LockExtensionByRequest(ctx, tx, rc.TenantID, requestID)
		switch {
		case err == nil:
			extension = &row
			return nil
		case errors.Is(err, ErrExtensionNotFound):
			// Not an admission and not an extension of one. Most requests are neither.
			return nil
		default:
			return err
		}
	})
	if err != nil {
		return nil, nil, err
	}
	return stay, extension, nil
}

// applyStayDecision locks and rereads the admission before funding it. The hold,
// transition and audit record commit together; cancellation can never strand a hold.
func (s *Service) applyStayDecision(ctx context.Context, rc identity.RequestContext,
	observed StayRecord, status string,
) error {
	if status != requestRejected && status != requestApproved && status != requestPartiallyApproved {
		return nil
	}
	return s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		stay, err := s.stayRepo.LockStay(ctx, tx, rc.TenantID, observed.ID, Scope{})
		if err != nil {
			return err
		}
		if stay.Status != domain.StayRequested {
			return nil
		}
		if status == requestRejected {
			moved, err := s.stayRepo.RejectStay(ctx, tx, rc.TenantID, stay.ID, nil)
			if err != nil {
				return err
			}
			if !moved {
				return ErrStayTransitionInvalid
			}
			return s.recordStay(ctx, tx, rc, "inpatient_stay.reject", stay,
				map[string]any{"source": "SERVICE_REQUEST_DECISION"})
		}
		hold, err := s.authorizations.CreateForRequestInTx(ctx, tx, rc, StayAuthorizationInput{
			RequestID: stay.ServiceRequestID, ValidFrom: stay.AdmissionAt,
			ValidTo: stay.ExpectedDischargeAt, IdempotencyKey: stayAuthorizationKey(stay.ID),
		})
		if err != nil {
			return fmt.Errorf("health: authorize stay %s: %w", stay.ID, err)
		}
		moved, err := s.stayRepo.AuthorizeStay(ctx, tx, rc.TenantID, stay.ID, hold.ID,
			parseDays(hold.ApprovedDays).String(), nil)
		if err != nil {
			return err
		}
		if !moved {
			return ErrStayTransitionInvalid
		}
		return s.recordStay(ctx, tx, rc, "inpatient_stay.authorize", stay, map[string]any{
			"authorization": hold.ID, "authorized_days": parseDays(hold.ApprovedDays).String(),
			"source": "SERVICE_REQUEST_DECISION",
		})
	})
}

// applyExtensionDecision takes the parent stay lock first, then the extension lock.
// Discharge and cancellation use the same parent lock, so a decision cannot fund a
// closed stay or lose the race after checking its status.
func (s *Service) applyExtensionDecision(ctx context.Context, rc identity.RequestContext,
	observed StayExtensionRecord, status string,
) error {
	if status != requestRejected && status != requestApproved && status != requestPartiallyApproved {
		return nil
	}
	return s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		stay, err := s.stayRepo.LockStay(ctx, tx, rc.TenantID, observed.StayID, Scope{})
		if err != nil {
			return err
		}
		extension, err := s.stayRepo.LockExtensionByRequest(ctx, tx, rc.TenantID, observed.ServiceRequestID)
		if err != nil {
			return err
		}
		if extension.ID != observed.ID || extension.StayID != stay.ID {
			return ErrStayTransitionInvalid
		}
		if extension.Status != domain.ExtensionRequested {
			return nil
		}
		if stay.Status != domain.StayAuthorized && stay.Status != domain.StayAdmitted {
			// A legacy closed stay may still have a REQUESTED extension from before
			// discharge cancelled pending extensions. Resolve it here, under the
			// parent lock, without funding it or retrying this event forever.
			if stay.Status == domain.StayDischarged || stay.Status == domain.StayCancelled || stay.Status == domain.StayRejected {
				cancelled, err := s.stayRepo.CancelExtensions(ctx, tx, rc.TenantID, stay.ID, nil)
				if err != nil {
					return err
				}
				return s.record(ctx, tx, rc, "stay_extension.cancel_late", domain.AggregateStayExtension,
					extension.ID, map[string]any{"stay": stay.ID, "cancelled_extensions": cancelled,
						"source": "SERVICE_REQUEST_DECISION"})
			}
			return ErrStayTransitionInvalid
		}
		if status == requestRejected {
			moved, err := s.stayRepo.RejectExtension(ctx, tx, rc.TenantID, extension.ID, nil)
			if err != nil {
				return err
			}
			if !moved {
				return ErrStayTransitionInvalid
			}
			return s.record(ctx, tx, rc, "stay_extension.reject", domain.AggregateStayExtension,
				extension.ID, map[string]any{
					"stay": stay.ID, "sequence_no": extension.SequenceNo,
					"source": "SERVICE_REQUEST_DECISION",
				})
		}
		if stay.AuthorizationID == nil {
			return ErrStayTransitionInvalid
		}
		hold, err := s.authorizations.CreateForRequestInTx(ctx, tx, rc, StayAuthorizationInput{
			RequestID: extension.ServiceRequestID, ValidFrom: stay.ExpectedDischargeAt,
			ValidTo:        stay.ExpectedDischargeAt.AddDate(0, 0, extension.AdditionalDays),
			IdempotencyKey: extensionAuthorizationKey(extension.ID),
		})
		if err != nil {
			return fmt.Errorf("health: authorize stay extension %s: %w", extension.ID, err)
		}
		approved := parseDays(hold.ApprovedDays)
		newExpected := stay.ExpectedDischargeAt.AddDate(0, 0, wholeDays(approved))
		if err := s.authorizations.ExtendValidityInTx(ctx, tx, rc, *stay.AuthorizationID,
			newExpected, extendReasonCode); err != nil {
			return fmt.Errorf("health: extend authorization of stay %s: %w", stay.ID, err)
		}
		moved, err := s.stayRepo.ApproveExtension(ctx, tx, rc.TenantID, extension.ID, hold.ID, nil)
		if err != nil {
			return err
		}
		if !moved {
			return ErrStayTransitionInvalid
		}
		moved, err = s.stayRepo.AddAuthorizedDays(ctx, tx, rc.TenantID, stay.ID,
			approved.String(), newExpected, nil)
		if err != nil {
			return err
		}
		if !moved {
			return ErrStayTransitionInvalid
		}
		return s.record(ctx, tx, rc, "stay_extension.approve", domain.AggregateStayExtension,
			extension.ID, map[string]any{
				"stay": stay.ID, "sequence_no": extension.SequenceNo,
				"authorization": hold.ID, "additional_days": approved.String(),
				"source": "SERVICE_REQUEST_DECISION",
			})
	})
}

// stayAuthorizationKey and extensionAuthorizationKey are the idempotency keys the two holds
// are created under. They are derived from the row rather than from a clock, so replaying a
// decision can never take a second hold on the same entitlement.
func stayAuthorizationKey(stayID uuid.UUID) string { return "inpatient-stay:" + stayID.String() }

func extensionAuthorizationKey(extensionID uuid.UUID) string {
	return "stay-extension:" + extensionID.String()
}

// wholeDays is the day count a date shift uses. A hold is an exact decimal and a calendar is
// not, so the fraction is rounded up: half a day of entitlement still needs a whole day of
// window to be used inside.
func wholeDays(q benefitdomain.Quantity) int {
	text := q.String()
	whole, fraction, hasFraction := strings.Cut(text, ".")
	days := 0
	for _, r := range whole {
		if r < '0' || r > '9' {
			continue
		}
		days = days*10 + int(r-'0')
	}
	if hasFraction && strings.Trim(fraction, "0") != "" {
		days++
	}
	return days
}

// StayDecidedHandler is the handler kapsora-worker registers for the decided-request event.
// It is a method value rather than the method itself so cmd/worker names the event and the
// handler in one line, the way every other subscription there reads.
func (s *Service) StayDecidedHandler() outbox.HandlerFunc {
	return s.HandleServiceRequestDecided
}
