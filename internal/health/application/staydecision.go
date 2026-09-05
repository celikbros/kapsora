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
	"github.com/celikbros/kapsora/internal/platform/db"
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

// decisionSubject finds what this decision belongs to: an admission, an extension of one, or
// neither. It reads without locking, because the writes that follow take their own locks and
// hold them for as long as they need — and one of the steps between is an authorization,
// which opens a transaction of its own and must not be waited on with a row lock held.
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

// applyStayDecision is the admission half: a hold for the days the reviewer approved and the
// stay in AUTHORIZED, or the stay in REJECTED with no hold at all.
func (s *Service) applyStayDecision(ctx context.Context, rc identity.RequestContext,
	stay StayRecord, status string,
) error {
	if stay.Status != domain.StayRequested {
		// A redelivery of a decision that has already been applied, or a stay somebody
		// cancelled while the event sat in the queue. Either way there is nothing to do:
		// the stay has moved on and moving it again would undo whatever moved it.
		return nil
	}
	switch {
	case status == requestRejected:
		return s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
			moved, err := s.stayRepo.RejectStay(ctx, tx, rc.TenantID, stay.ID, nil)
			if err != nil || !moved {
				return err
			}
			return s.recordStay(ctx, tx, rc, "inpatient_stay.reject", stay,
				map[string]any{"source": "SERVICE_REQUEST_DECISION"})
		})
	case status != requestApproved && status != requestPartiallyApproved:
		// A return or a cancellation. The request is going round again and the admission
		// is still waiting for somebody to decide it.
		return nil
	}

	// The hold, in the authorization module's own transaction. The key is the stay, so a
	// redelivered event finds the authorization the first delivery created rather than
	// reserving the same days twice.
	hold, err := s.authorizations.CreateForRequest(ctx, rc, StayAuthorizationInput{
		RequestID: stay.ServiceRequestID, ValidFrom: stay.AdmissionAt,
		ValidTo: stay.ExpectedDischargeAt, IdempotencyKey: stayAuthorizationKey(stay.ID),
	})
	if err != nil {
		return fmt.Errorf("health: authorize stay %s: %w", stay.ID, err)
	}
	return s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		moved, err := s.stayRepo.AuthorizeStay(ctx, tx, rc.TenantID, stay.ID, hold.ID,
			parseDays(hold.ApprovedDays).String(), nil)
		if err != nil || !moved {
			return err
		}
		return s.recordStay(ctx, tx, rc, "inpatient_stay.authorize", stay, map[string]any{
			"authorization":   hold.ID,
			"authorized_days": parseDays(hold.ApprovedDays).String(),
			"source":          "SERVICE_REQUEST_DECISION",
		})
	})
}

// applyExtensionDecision is the extension half. An approval does two things to the promise
// and they are both needed: the added days are reserved by the extension's own authorization,
// and the original authorization's validity is moved forward so the window covers them. Doing
// only the first would hold days inside a promise that expires before they are used; doing
// only the second would extend a window over days nobody reserved.
//
// The order is deliberate and it is the order that survives a redelivery. The hold is taken
// first, under a key derived from the extension. The validity is moved second, and the port
// skips an authorization that already ends late enough, so running it again is a no-op rather
// than a refusal. The extension is marked approved last, under a REQUESTED predicate — so if
// anything above failed, the next delivery starts again from the top and finds the extension
// still waiting.
func (s *Service) applyExtensionDecision(ctx context.Context, rc identity.RequestContext,
	extension StayExtensionRecord, status string,
) error {
	if extension.Status != domain.ExtensionRequested {
		return nil
	}
	if status == requestRejected {
		return s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
			moved, err := s.stayRepo.RejectExtension(ctx, tx, rc.TenantID, extension.ID, nil)
			if err != nil || !moved {
				return err
			}
			// The stay itself is untouched: a refused extension changes nothing about the
			// admission that was already approved.
			return s.record(ctx, tx, rc, "stay_extension.reject", domain.AggregateStayExtension,
				extension.ID, map[string]any{
					"stay": extension.StayID, "sequence_no": extension.SequenceNo,
					"source": "SERVICE_REQUEST_DECISION",
				})
		})
	}
	if status != requestApproved && status != requestPartiallyApproved {
		return nil
	}

	stay, err := s.stayOf(ctx, rc, extension.StayID)
	if err != nil {
		return err
	}
	hold, err := s.authorizations.CreateForRequest(ctx, rc, StayAuthorizationInput{
		RequestID: extension.ServiceRequestID, ValidFrom: stay.ExpectedDischargeAt,
		ValidTo:        stay.ExpectedDischargeAt.AddDate(0, 0, extension.AdditionalDays),
		IdempotencyKey: extensionAuthorizationKey(extension.ID),
	})
	if err != nil {
		return fmt.Errorf("health: authorize stay extension %s: %w", extension.ID, err)
	}
	approved := parseDays(hold.ApprovedDays)
	// The date arithmetic uses the days that were actually approved, not the days that were
	// asked for: a reviewer who granted two of the three extra days has moved the discharge
	// two days, and a window three days long would promise a day nobody reserved.
	newExpected := stay.ExpectedDischargeAt.AddDate(0, 0, wholeDays(approved))
	if stay.AuthorizationID != nil {
		if err := s.authorizations.ExtendValidity(ctx, rc, *stay.AuthorizationID,
			newExpected, extendReasonCode); err != nil {
			return fmt.Errorf("health: extend the authorization of stay %s: %w", stay.ID, err)
		}
	}
	return s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		moved, err := s.stayRepo.ApproveExtension(ctx, tx, rc.TenantID, extension.ID, hold.ID, nil)
		if err != nil || !moved {
			return err
		}
		// Only inside the same transaction as the approval, and only when the approval
		// actually moved the row: adding days is the one write here that is not idempotent
		// on its own, and the predicate above is what makes it run once.
		if _, err := s.stayRepo.AddAuthorizedDays(ctx, tx, rc.TenantID, stay.ID,
			approved.String(), newExpected, nil); err != nil {
			return err
		}
		return s.record(ctx, tx, rc, "stay_extension.approve", domain.AggregateStayExtension,
			extension.ID, map[string]any{
				"stay": extension.StayID, "sequence_no": extension.SequenceNo,
				"authorization": hold.ID, "additional_days": approved.String(),
				"source": "SERVICE_REQUEST_DECISION",
			})
	})
}

// stayOf reads a stay outside any command, for the subscriber. The scope is empty: the worker
// acts for the tenant rather than for a provider.
func (s *Service) stayOf(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (StayRecord, error) {
	var out StayRecord
	err := db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: rc.TenantID},
		func(ctx context.Context, tx pgx.Tx) error {
			record, err := s.stayRepo.GetStay(ctx, tx, rc.TenantID, id, Scope{})
			out = record
			return err
		})
	return out, err
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
