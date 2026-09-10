package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/claim/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// The lodging claim: a completed stay, a confirmed no-show and a penalised cancellation, each
// arriving as an outbox event and each leaving as a claim the provider can invoice.
//
// Three sentences carry this file.
//
// **Nothing here re-prices anything.** Every amount is copied out of the row that froze it —
// `accommodation.booking_night` for a stay, `accommodation.no_show` and
// `accommodation.cancellation` for a fee. A claim that asked the contract what a night costs
// would be a claim that billed the member a price they were never shown, six weeks after they
// slept in the room; and the whole point of freezing the quote at the hold was that the answer
// stops moving.
//
// **The claim is created here rather than in the check-out.** A desk clerk closing a stay at
// eleven at night must not be told that the billing side is down. WP-I6-03's commands write an
// outbox row beside the status change and commit; this is the subscriber.
//
// **A second delivery creates nothing.** The outbox delivers at least once, so every handler
// below looks for the claim a first delivery made and stops when it finds one — and
// `uq_claim_live_booking` is underneath that read, for the case where two deliveries look at
// the same moment and both find nothing.

// The unit types a lodging claim's lines are measured in. NIGHT is a night slept; the two fee
// units are one event each, which is why their quantity is always exactly one.
const (
	UnitNight           = "NIGHT"
	UnitNoShowFee       = "NO_SHOW_FEE"
	UnitCancellationFee = "CANCELLATION_FEE"
)

// ReasonBookingConfirmed is why a night the plan carries is already approved when the claim is
// created: the decision was taken at confirmation, by the reviewer or by the rules, and the
// claim is recording it rather than making it again.
const ReasonBookingConfirmed = "BOOKING_CONFIRMED"

// **A fee claim writes no adjustment row.** Its provenance is already answerable without one:
// `claim.source_type = 'BOOKING'` with `source_id` reaches the booking, the cancellation and
// no-show rows are unique per booking, and the line's own unit type says which of the two
// assessed it. A zero-value row in a money ledger would be noise in the one table that must
// only ever hold money that moved, and `claim.adjustment.source_type` keeps CANCELLATION and
// NO_SHOW for the day something really does move against one of those rows.

// The statuses of the two fee rows that mean a fee is actually owed.
const (
	noShowConfirmed = "CONFIRMED"
)

// bookingEventPayload is the part of WP-I6-03's three booking events this package reads.
// Everything else in them is somebody else's business, and a consumer that unmarshalled the
// whole thing would be a consumer that broke when a field was added.
type bookingEventPayload struct {
	BookingID uuid.UUID `json:"bookingId"`
	Free      bool      `json:"free"`
}

// The three subscriptions kapsora-worker registers. They are three handlers rather than one
// with a switch because each answers a different question about the same booking, and a switch
// would be a place for the three answers to drift into each other.

// HandleBookingCheckedOut raises the claim of a completed stay.
func (s *Service) HandleBookingCheckedOut(ctx context.Context, d outbox.Delivery) error {
	tenantID, payload, err := bookingEvent(d)
	if err != nil {
		return err
	}
	return s.raiseBookingClaim(ctx, systemContext(tenantID), payload.BookingID, stayLines)
}

// HandleBookingNoShowConfirmed raises the claim of a confirmed no-show: one line, the fee the
// report assessed, at the split the row already carries.
func (s *Service) HandleBookingNoShowConfirmed(ctx context.Context, d outbox.Delivery) error {
	tenantID, payload, err := bookingEvent(d)
	if err != nil {
		return err
	}
	return s.raiseBookingClaim(ctx, systemContext(tenantID), payload.BookingID, noShowLines)
}

// HandleBookingCancelled raises the claim of a penalised cancellation, and raises nothing at
// all for a free one.
//
// A free cancellation is not an error and not a warning: it is the ordinary case of a member
// who called off a stay inside the window they were promised, and there is nothing for
// anybody to invoice.
func (s *Service) HandleBookingCancelled(ctx context.Context, d outbox.Delivery) error {
	tenantID, payload, err := bookingEvent(d)
	if err != nil {
		return err
	}
	if payload.Free {
		return nil
	}
	return s.raiseBookingClaim(ctx, systemContext(tenantID), payload.BookingID, cancellationLines)
}

// bookingEvent decodes what all three handlers read. A malformed event is permanent: retrying
// a payload that cannot be parsed would retry it for ever.
func bookingEvent(d outbox.Delivery) (uuid.UUID, bookingEventPayload, error) {
	if !d.TenantID.Valid {
		return uuid.Nil, bookingEventPayload{},
			outbox.Permanent(errors.New("claim: a booking event carries no tenant"))
	}
	var payload bookingEventPayload
	if err := json.Unmarshal(d.Payload, &payload); err != nil {
		return uuid.Nil, bookingEventPayload{},
			outbox.Permanent(fmt.Errorf("claim: decode booking event: %w", err))
	}
	if payload.BookingID == uuid.Nil {
		return uuid.Nil, bookingEventPayload{},
			outbox.Permanent(errors.New("claim: a booking event names no booking"))
	}
	return d.TenantID.UUID, payload, nil
}

// systemContext is the caller these handlers act as: the tenant, and nobody in particular.
// There is no actor because there is no person — the clerk who closed the stay is already on
// the booking's audit row, and attributing a claim to them would say they raised a bill they
// have never seen.
//
// It holds no organization scope, which is what lets it read a booking whichever provider runs
// the hotel. A worker bound to one provider would leave every other property's stays unbilled.
func systemContext(tenantID uuid.UUID) identity.RequestContext {
	return identity.RequestContext{TenantID: tenantID}
}

// bookingClaimPlan is what one of the three shapes decided: the lines, the period they cover,
// and the ledger row the claim owes when a fee assessed it.
type bookingClaimPlan struct {
	Lines []NewLineInput
	// Decisions is one entry per line, in line order, holding the amount the system approves
	// at creation. An empty string means the line is left for the financial reviewer.
	Decisions []string
	From      time.Time
	To        time.Time
	Currency  string
	// Skip is a stay that owes nothing at all — a free cancellation reaching the handler by
	// another road, or a no-show somebody rejected while the event sat in a queue.
	Skip bool
}

// linesFor is the shape of the three builders below.
type linesFor func(ctx context.Context, tx pgx.Tx, s *Service, tenantID uuid.UUID,
	booking BookingRecord) (bookingClaimPlan, error)

// stayLines is one line per night slept, at the booking's own frozen `unit_amount`.
//
// The nights the plan carried — the ones whose frozen row already says the payer carries
// something — are decided APPROVED at that payer amount, by the system, with reason
// BOOKING_CONFIRMED: the decision was taken at confirmation and this is the claim recording
// it, not a second adjudication. A night the plan did not carry is a line the financial
// reviewer answers, and it is still a line, because the contract may bill the member's share
// through the payer.
func stayLines(ctx context.Context, tx pgx.Tx, s *Service, tenantID uuid.UUID,
	booking BookingRecord,
) (bookingClaimPlan, error) {
	nights, err := s.repo.BookingNights(ctx, tx, tenantID, booking.ID)
	if err != nil {
		return bookingClaimPlan{}, err
	}
	slept := booking.Nights
	if booking.ActualNights != nil {
		slept = *booking.ActualNights
	}
	if slept > len(nights) {
		// A stay that ran past what was booked has no frozen amount for the extra nights, and
		// this package will not invent one. The over-stay is already flagged on the booking
		// and is the financial reviewer's to price.
		slept = len(nights)
	}
	if slept <= 0 {
		return bookingClaimPlan{Skip: true}, nil
	}
	out := bookingClaimPlan{
		Lines: make([]NewLineInput, 0, slept), Decisions: make([]string, 0, slept),
		From: booking.CheckIn, To: booking.CheckIn.AddDate(0, 0, slept-1),
		Currency: nights[0].CurrencyCode,
	}
	for i := 0; i < slept; i++ {
		night := nights[i]
		unit := night.UnitAmount
		out.Lines = append(out.Lines, NewLineInput{
			LineNo: i + 1, ServiceDefinitionID: booking.ServiceDefinitionID,
			UnitType: UnitNight, Quantity: "1", UnitAmount: &unit, LineAmount: unit,
			CurrencyCode: &night.CurrencyCode,
		})
		payer := quantityOrZero(night.PayerAmount)
		if payer.IsPositive() {
			out.Decisions = append(out.Decisions, night.PayerAmount)
			continue
		}
		out.Decisions = append(out.Decisions, "")
	}
	return out, nil
}

// noShowLines is the single line of a confirmed no-show: the fee, at the split the report
// already carries.
func noShowLines(ctx context.Context, tx pgx.Tx, s *Service, tenantID uuid.UUID,
	booking BookingRecord,
) (bookingClaimPlan, error) {
	fee, found, err := s.repo.BookingNoShow(ctx, tx, tenantID, booking.ID)
	if err != nil {
		return bookingClaimPlan{}, err
	}
	if !found || fee.Status != noShowConfirmed {
		return bookingClaimPlan{Skip: true}, nil
	}
	return feePlan(booking, fee, UnitNoShowFee)
}

// cancellationLines is the single line of a penalised cancellation. A free one produces
// nothing: there is no fee, and a claim for nought is a document nobody can invoice.
func cancellationLines(ctx context.Context, tx pgx.Tx, s *Service, tenantID uuid.UUID,
	booking BookingRecord,
) (bookingClaimPlan, error) {
	fee, found, err := s.repo.BookingCancellation(ctx, tx, tenantID, booking.ID)
	if err != nil {
		return bookingClaimPlan{}, err
	}
	if !found || fee.Free {
		return bookingClaimPlan{Skip: true}, nil
	}
	return feePlan(booking, fee, UnitCancellationFee)
}

// feePlan is the shape both fee claims share. The line is the whole fee and the decision is
// the payer's half of it; the member's half is on the line and undecided, exactly as an
// uncovered night is, so the financial reviewer answers who carries it.
func feePlan(booking BookingRecord, fee BookingFeeRecord, unitType string,
) (bookingClaimPlan, error) {
	amount := quantityOrZero(fee.Amount)
	if !amount.IsPositive() {
		return bookingClaimPlan{Skip: true}, nil
	}
	currency := fee.CurrencyCode
	total := fee.Amount
	plan := bookingClaimPlan{
		Lines: []NewLineInput{{
			LineNo: 1, ServiceDefinitionID: booking.ServiceDefinitionID, UnitType: unitType,
			Quantity: "1", UnitAmount: &total, LineAmount: total, CurrencyCode: &currency,
		}},
		Decisions: []string{""},
		From:      booking.CheckIn, To: booking.CheckIn, Currency: currency,
	}
	if payer := quantityOrZero(fee.PayerAmount); payer.IsPositive() {
		plan.Decisions[0] = fee.PayerAmount
	}
	return plan, nil
}

// raiseBookingClaim is the one path all three events take.
//
// It runs in one transaction: the claim, its version, its lines, the decisions the system
// already knows, the freeze, the status and the work item are one fact. A claim with no
// version, or a claim in PENDING_FINANCIAL with nobody watching, is a state no reader ever
// observes.
func (s *Service) raiseBookingClaim(ctx context.Context, rc identity.RequestContext,
	bookingID uuid.UUID, build linesFor,
) error {
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		// The first half of the idempotency. A redelivered event finds the claim the first
		// delivery made and writes nothing at all — not a second version, not a second line,
		// not a second work item.
		if _, found, err := s.repo.FindLiveClaimBySource(ctx, tx, rc.TenantID,
			domain.SourceBooking, bookingID); err != nil || found {
			return err
		}
		booking, err := s.repo.BookingForClaim(ctx, tx, rc.TenantID, bookingID)
		if err != nil {
			return err
		}
		plan, err := build(ctx, tx, s, rc.TenantID, booking)
		if err != nil {
			return err
		}
		if plan.Skip {
			return nil
		}
		return s.writeBookingClaim(ctx, tx, rc, booking, plan)
	})
	switch {
	case errors.Is(err, ErrClaimAlreadyRaised):
		// The other half. Two deliveries looked at the same moment, both found nothing, and
		// the index refused the second. That is the guarantee working, not a failure.
		return nil
	case errors.Is(err, ErrBookingNotFound):
		return outbox.Permanent(err)
	default:
		return err
	}
}

// writeBookingClaim writes the whole claim, in the order the schema requires.
func (s *Service) writeBookingClaim(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	booking BookingRecord, plan bookingClaimPlan,
) error {
	sourceType := domain.SourceBooking
	sourceID := booking.ID
	record, err := s.createSourceClaim(ctx, tx, rc, NewClaimRow{
		PersonID: booking.PersonID, ProgramID: booking.ProgramID,
		EnrollmentID:           booking.EnrollmentID,
		ProviderOrganizationID: booking.ProviderOrganizationID,
		DomainCode:             domain.DomainAccommodation,
		SourceType:             &sourceType, SourceID: &sourceID,
		AuthorizationID: booking.AuthorizationID,
		ServiceDateFrom: domain.DateOnly(plan.From), ServiceDateTo: domain.DateOnly(plan.To),
		Channel: domain.DefaultChannel,
	})
	if err != nil {
		return err
	}
	version, err := s.repo.CreateVersion(ctx, tx, rc.TenantID, record.ID, 1, nil)
	if err != nil {
		return err
	}
	if err := s.repo.ReplaceLines(ctx, tx, rc.TenantID, version.ID,
		lineRows(version.ID, plan.Lines, nil)); err != nil {
		return err
	}
	lines, err := s.repo.ListLines(ctx, tx, rc.TenantID, version.ID)
	if err != nil {
		return err
	}

	// What the plan already decided at confirmation, recorded rather than decided again. The
	// stage is AUTO and the actor is nobody, which is what `ck_line_decision_actor` asserts
	// and what "nobody looked at this" honestly means.
	now := s.now().UTC()
	byLineNo := make(map[int]LineRecord, len(lines))
	for _, line := range lines {
		byLineNo[line.LineNo] = line
	}
	approved := 0
	for i, amount := range plan.Decisions {
		if amount == "" {
			continue
		}
		line, ok := byLineNo[plan.Lines[i].LineNo]
		if !ok {
			return ErrLineNotFound
		}
		if _, err := s.repo.CreateDecision(ctx, tx, rc.TenantID, NewDecisionRow{
			LineID: line.ID, DecidedInVersionNo: version.VersionNo,
			Decision: domain.DecisionApproved, ApprovedQuantity: "1",
			ApprovedAmount: amount, PayerAmount: amount, MemberAmount: zero().String(),
			ReasonCode: ReasonBookingConfirmed, DecidedAt: now, Stage: domain.StageAuto,
		}); err != nil {
			return err
		}
		approved++
	}

	snapshot, err := sourceClaimSnapshot(record, version, lines, plan.Currency)
	if err != nil {
		return err
	}
	frozen, err := s.repo.FreezeVersion(ctx, tx, rc.TenantID, version.ID, FreezeRow{
		Snapshot: snapshot, SubmittedAt: now,
	})
	if err != nil {
		return err
	}
	if !frozen {
		return ErrTransitionInvalid
	}

	// The claim lands where a health claim lands after its medical stage: in front of the
	// financial reviewer. There is no medical stage on a hotel bill.
	moved, err := s.repo.SetStatus(ctx, tx, rc.TenantID, record.ID, StatusRow{
		Status: domain.StatusPendingFinancial, CurrentVersionNo: version.VersionNo,
		FromStatuses: []string{domain.StatusDraft},
	}, record.RowVersion)
	if err != nil {
		return err
	}
	if !moved {
		return ErrTransitionInvalid
	}

	if err := s.workItems.Raise(ctx, tx, rc.TenantID, RaiseWorkItem{
		QueueCode: QueueFinancialReview, AggregateType: domain.AggregateClaim,
		AggregateID: record.ID, Title: "Hasar dosyası " + record.Reference,
	}); err != nil {
		return err
	}
	return s.record(ctx, tx, rc, "claim.booking.raise", record.ID, map[string]any{
		"reference": record.Reference, "booking_reference": booking.Reference,
		"source_type": domain.SourceBooking, "source_id": booking.ID.String(),
		"line_count": len(plan.Lines), "auto_approved_lines": approved,
		"covered_nights": booking.CoveredNights, "over_booking": booking.OverBooking,
		"currency_code": plan.Currency,
	})
}

// createSourceClaim opens the header with a generated reference, retrying a collision rather
// than making somebody read one. It is `createWithReference` for a claim nobody asked for: the
// caller is an outbox handler and there is no NewClaimInput behind it.
func (s *Service) createSourceClaim(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	row NewClaimRow,
) (ClaimRecord, error) {
	for attempt := 0; attempt < referenceAttempts; attempt++ {
		reference, err := newReference(s.now())
		if err != nil {
			return ClaimRecord{}, err
		}
		row.Reference = reference
		record, err := s.repo.CreateClaim(ctx, tx, rc.TenantID, row)
		switch {
		case err == nil:
			return record, nil
		case errors.Is(err, ErrReferenceCollision):
			continue
		default:
			return ClaimRecord{}, err
		}
	}
	return ClaimRecord{}, ErrReferenceCollision
}

// sourceClaimSnapshot freezes the version the way a submitted health claim's is frozen: what
// was sent, in the shape every reader of `claim.claim_version.snapshot` already knows.
//
// The routing says neither stage is still owed, which is the truth for both claims that use
// it: a lodging claim is already in the financial stage, a reimbursement claim has already
// been decided in it, and `readRouting`'s flag exists only to carry "financial is still owed"
// across a medical stage neither ever had.
func sourceClaimSnapshot(record ClaimRecord, version VersionRecord, lines []LineRecord,
	currency string,
) ([]byte, error) {
	doc := claimSnapshot{
		SnapshotVersion: snapshotVersion, ClaimID: record.ID.String(),
		Reference: record.Reference, VersionNo: version.VersionNo,
		ServiceDateFrom: record.ServiceDateFrom.UTC().Format(time.DateOnly),
		ServiceDateTo:   record.ServiceDateTo.UTC().Format(time.DateOnly),
		CurrencyCode:    currency,
		Lines:           make([]snapshotLine, 0, len(lines)),
		Pricing:         []snapshotPricedLine{},
		Routing:         snapshotRouting{Exceptions: []snapshotException{}},
	}
	for _, line := range lines {
		row := snapshotLine{
			LineNo: line.LineNo, ServiceDefinitionID: line.ServiceDefinitionID.String(),
			UnitType: line.UnitType, Quantity: line.Quantity, LineAmount: line.LineAmount,
			CurrencyCode: line.CurrencyCode,
		}
		if line.ServiceCode != nil {
			row.ServiceCode = *line.ServiceCode
		}
		if line.UnitAmount != nil {
			row.UnitAmount = *line.UnitAmount
		}
		doc.Lines = append(doc.Lines, row)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("claim: encode booking snapshot: %w", err)
	}
	return out, nil
}
