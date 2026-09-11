package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/domain"
	"github.com/celikbros/kapsora/internal/accommodation/settings"
	"github.com/celikbros/kapsora/internal/identity"
)

// The no-show, which is a claim before it is a fact.
//
// One sentence carries this file: **a no-show never costs the member anything until a second
// person confirms it.** The provider reports that nobody came and attaches evidence; the
// booking stays CONFIRMED and no entitlement moves; and only a reviewer on the payer's side
// -- who may not be the person who reported it -- turns the claim into a consumed night and
// a closed booking. A refused report leaves the stay exactly as it was, still cancellable
// and still checkable-in, because a provider who was wrong must not have cost the member a
// booking.

// NoShowView is a report with the booking it is about.
type NoShowView struct {
	Report  NoShowRecord
	Booking BookingView
}

// ReportNoShowInput is the provider's claim.
type ReportNoShowInput struct {
	// EvidenceDocumentID is the WP-I4-04 document object the provider is pointing at. It
	// must be linked to this booking and clean; a report without one is refused, because a
	// claim that costs a member money and rests on nothing is a claim nobody can review.
	EvidenceDocumentID *uuid.UUID
	// At overrides the moment, for a desk recording an absence it did not enter at the
	// time. Zero means now.
	At time.Time
}

// ReviewNoShowInput is the payer's answer.
type ReviewNoShowInput struct {
	// Status is CONFIRMED, REJECTED or DISPUTED.
	Status  string
	Comment *string
}

// ReportNoShow records a provider's claim that nobody arrived.
//
// It changes nothing except the report: the booking stays CONFIRMED, the room stays
// confirmed in the allotment and not one night moves on the plan. That is the whole design.
// A provider that could close a booking by asserting an absence could close a booking a
// guest is standing in the lobby of, and the member would learn about it from a fee.
//
// Two refusals happen here. A report before the check-in window has closed is refused --
// a guest who is late is not a guest who did not come, and the line between them is the
// tenant's own `accommodation.checkin_late_hours` measured on the property's clock. And a
// report with no clean document behind it is refused, whatever the reason.
func (s *Service) ReportNoShow(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in ReportNoShowInput,
) (NoShowView, error) {
	if s.bookings == nil {
		return NoShowView{}, ErrBookingNotFound
	}
	var out NoShowView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		view, err := s.reportNoShow(ctx, tx, rc, id, in)
		out = view
		return err
	})
	if err != nil {
		return NoShowView{}, err
	}
	return out, nil
}

func (s *Service) reportNoShow(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	id uuid.UUID, in ReportNoShowInput,
) (NoShowView, error) {
	if _, err := s.bookings.GetBooking(ctx, tx, rc.TenantID, id, personBoundary(rc), scopeOf(rc)); err != nil {
		return NoShowView{}, err
	}
	record, err := s.bookings.LockBooking(ctx, tx, rc.TenantID, id)
	if err != nil {
		return NoShowView{}, err
	}
	if record.Status != domain.BookingConfirmed {
		return NoShowView{}, ErrBookingTransitionInvalid
	}
	switch _, err := s.bookings.GetNoShow(ctx, tx, rc.TenantID, record.ID); {
	case err == nil:
		// A refused report is REJECTED rather than deleted, so this covers the decided ones
		// as well: a provider gets one claim per booking and cannot quietly file a second.
		return NoShowView{}, ErrNoShowAlreadyReported
	case !errors.Is(err, ErrNoShowNotFound):
		return NoShowView{}, err
	}

	room, err := s.bookings.RoomTypeBookingContext(ctx, tx, rc.TenantID, record.RoomTypeID, nil)
	if err != nil {
		return NoShowView{}, err
	}
	tenantSettings, err := settings.Load(ctx, tx, rc.TenantID)
	if err != nil {
		return NoShowView{}, err
	}
	at := in.At
	if at.IsZero() {
		at = s.now()
	}
	at = at.UTC()
	if at.Before(checkInWindowClosesAt(record.CheckIn, room.PropertyTimezone, tenantSettings)) {
		return NoShowView{}, ErrNoShowTooEarly
	}

	// The evidence. A named document narrows the check to that one, so a provider says
	// which file it means rather than relying on whatever happens to be attached; either
	// way the scanner has to have cleared it, because a link to something in quarantine is
	// not a document a reviewer can open.
	documents, err := s.bookings.CountCleanBookingDocuments(ctx, tx, rc.TenantID, record.ID,
		in.EvidenceDocumentID)
	if err != nil {
		return NoShowView{}, err
	}
	if documents == 0 {
		return NoShowView{}, ErrNoShowEvidenceRequired
	}

	snapshot, err := decodeQuoteSnapshot(record.QuoteSnapshot)
	if err != nil {
		return NoShowView{}, err
	}
	policy, err := DecodeLodgingPolicy(record.PolicySnapshot)
	if err != nil {
		return NoShowView{}, err
	}
	quote, err := QuoteNoShow(policy, snapshot)
	if err != nil {
		return NoShowView{}, err
	}

	report, err := s.bookings.CreateNoShow(ctx, tx, rc.TenantID, NewNoShowRow{
		BookingID: record.ID, ReportedByActorID: rc.Principal.ActorID, ReportedAt: at,
		EvidenceDocumentID: in.EvidenceDocumentID,
		AssessedFeeAmount:  quote.FeeAmount, PayerAmount: quote.PayerAmount,
		MemberAmount: quote.MemberAmount, CurrencyCode: quote.CurrencyCode,
		ActorID: rc.Principal.ActorID,
	})
	if err != nil {
		return NoShowView{}, err
	}
	if err := s.record(ctx, tx, rc, ActionBookingNoShowReport, ResourceBooking, record.ID,
		map[string]any{
			"reference": record.Reference, "no_show_id": report.ID.String(),
			"assessed_fee": quote.FeeAmount, "currency_code": quote.CurrencyCode,
		}); err != nil {
		return NoShowView{}, err
	}
	// The member is told what has been claimed and what the contract says it costs, so the
	// first they hear of it is not an invoice. Only the member: the property is the one who
	// reported it, and telling it what it just said would be noise.
	if err := s.notifyNoShowReported(ctx, tx, rc.TenantID, record, room.PropertyName, quote); err != nil {
		return NoShowView{}, err
	}
	view, err := s.loadBooking(ctx, tx, rc.TenantID, record)
	if err != nil {
		return NoShowView{}, err
	}
	return NoShowView{Report: report, Booking: view}, nil
}

// GetNoShow returns the report on a booking, inside the caller's own boundaries.
func (s *Service) GetNoShow(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (
	NoShowView, error,
) {
	if s.bookings == nil {
		return NoShowView{}, ErrBookingNotFound
	}
	var out NoShowView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.bookings.GetBooking(ctx, tx, rc.TenantID, id, personBoundary(rc), scopeOf(rc))
		if err != nil {
			return err
		}
		report, err := s.bookings.GetNoShow(ctx, tx, rc.TenantID, record.ID)
		if err != nil {
			return err
		}
		view, err := s.loadBooking(ctx, tx, rc.TenantID, record)
		if err != nil {
			return err
		}
		out = NoShowView{Report: report, Booking: view}
		return nil
	})
	if err != nil {
		return NoShowView{}, err
	}
	return out, nil
}

// ReviewNoShow is the payer's answer to a provider's claim, and the only place a no-show
// costs anybody anything.
//
// **The reviewer may not be the reporter.** It is checked here so the caller reads a sentence
// naming the rule, and it is a CHECK on the row underneath, so it holds whatever reaches the
// table. That pairing is the same one WP-I4-01 uses for an approval and WP-I2-03 for an
// entitlement correction, and it is the reason `reported_by_actor_id` is stored at all.
//
// The three answers do three different things. CONFIRMED closes the booking as NO_SHOW,
// frees the room for the rest of the allotment, consumes what the policy says off the plan
// and releases the rest. REJECTED changes nothing but the report: the stay is still
// CONFIRMED, still cancellable and still checkable-in, because a provider who was wrong must
// not have cost the member their booking. DISPUTED raises a work item and leaves everything
// where it is, because somebody has to look.
func (s *Service) ReviewNoShow(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in ReviewNoShowInput,
) (NoShowView, error) {
	if s.bookings == nil {
		return NoShowView{}, ErrBookingNotFound
	}
	if !domain.InList(in.Status, NoShowDecisions) {
		return NoShowView{}, fieldError("status", "ENUM", "tanınmayan karar")
	}
	if in.Comment != nil {
		trimmed := strings.TrimSpace(*in.Comment)
		if trimmed == "" || len([]rune(trimmed)) > 2000 {
			return NoShowView{}, fieldError("comment", "RANGE", "1-2000 karakter olmalı")
		}
		in.Comment = &trimmed
	}
	var out NoShowView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		view, err := s.reviewNoShow(ctx, tx, rc, id, in)
		out = view
		return err
	})
	if err != nil {
		return NoShowView{}, err
	}
	return out, nil
}

func (s *Service) reviewNoShow(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	id uuid.UUID, in ReviewNoShowInput,
) (NoShowView, error) {
	if _, err := s.bookings.GetBooking(ctx, tx, rc.TenantID, id, personBoundary(rc), scopeOf(rc)); err != nil {
		return NoShowView{}, err
	}
	record, err := s.bookings.LockBooking(ctx, tx, rc.TenantID, id)
	if err != nil {
		return NoShowView{}, err
	}
	existing, err := s.bookings.GetNoShow(ctx, tx, rc.TenantID, record.ID)
	if err != nil {
		return NoShowView{}, err
	}
	report, err := s.bookings.LockNoShow(ctx, tx, rc.TenantID, existing.ID)
	if err != nil {
		return NoShowView{}, err
	}
	if report.Status != NoShowReported {
		return NoShowView{}, ErrNoShowDecided
	}
	// Maker-checker. The person who said nobody came may never be the person who decides
	// it costs the member anything.
	if report.ReportedByActorID != nil && rc.Principal.ActorID != uuid.Nil &&
		*report.ReportedByActorID == rc.Principal.ActorID {
		return NoShowView{}, ErrNoShowSameActor
	}
	// Nor may a reviewer decide what their own no-show costs them.
	if err := identity.RefuseOwnFile(rc, record.PersonID); err != nil {
		return NoShowView{}, err
	}

	consumed := 0
	if in.Status == NoShowConfirmed {
		if record.Status != domain.BookingConfirmed {
			// Somebody cancelled or checked in while the report sat in a queue. The claim
			// cannot be confirmed against a stay that is no longer waiting for a guest.
			return NoShowView{}, ErrBookingTransitionInvalid
		}
		consumed, err = s.settleNoShow(ctx, tx, rc, record)
		if err != nil {
			return NoShowView{}, err
		}
	}

	reviewedAt := s.now().UTC()
	moved, err := s.bookings.ReviewNoShowRow(ctx, tx, rc.TenantID, NoShowReviewRow{
		ID: report.ID, Status: in.Status, ReviewedBy: rc.Principal.ActorID,
		ReviewedAt: reviewedAt, ReviewComment: in.Comment, ConsumedNights: consumed,
	})
	if err != nil {
		return NoShowView{}, err
	}
	if !moved {
		return NoShowView{}, ErrNoShowDecided
	}

	if in.Status == NoShowDisputed {
		// Somebody has to look at it, and the queue is where looking happens. The title
		// carries the booking's reference and nothing else: a queue is a list people read
		// across a room.
		if err := s.workItems.Raise(ctx, tx, rc.TenantID, RaiseWorkItem{
			QueueCode: NoShowReviewQueueCode, AggregateType: workItemAggregateBooking,
			AggregateID: record.ID, Title: "Gelmedi itirazı " + record.Reference,
			ActorID: actorOrNil(rc.Principal.ActorID),
		}); err != nil {
			return NoShowView{}, err
		}
	}

	if err := s.record(ctx, tx, rc, ActionBookingNoShowReview, ResourceBooking, record.ID,
		map[string]any{
			"reference": record.Reference, "no_show_id": report.ID.String(),
			"status": in.Status, "consumed_nights": consumed,
		}); err != nil {
		return NoShowView{}, err
	}
	// Only a confirmation is announced. A rejected report cost nobody anything and a
	// disputed one is still a question, and publishing either would tell the billing side
	// that a fee exists when it does not.
	if in.Status == NoShowConfirmed {
		if err := s.publishNoShowConfirmed(ctx, tx, rc.TenantID, record, report.ID); err != nil {
			return NoShowView{}, err
		}
	}

	updated := report
	updated.Status = in.Status
	updated.ReviewedAt = &reviewedAt
	reviewer := rc.Principal.ActorID
	updated.ReviewedBy = &reviewer
	updated.ReviewComment = in.Comment
	updated.ConsumedNights = consumed

	if in.Status == NoShowConfirmed {
		record.Status = domain.BookingNoShow
		reason := CancelReasonNoShow
		record.CancelReasonCode = &reason
	}
	view, err := s.loadBooking(ctx, tx, rc.TenantID, record)
	if err != nil {
		return NoShowView{}, err
	}
	return NoShowView{Report: updated, Booking: view}, nil
}

// settleNoShow closes the booking and moves the plan, in the order a cancellation moves it:
// the penalty is consumed first and the remainder released second, because both draw on the
// same reservation and a release that ran first would leave nothing to consume.
//
// The room goes back to the allotment. The guest did not come, so the hotel may sell those
// nights to somebody else -- which is the difference between a no-show and a stay: a no-show
// frees a room that a stay would have occupied.
func (s *Service) settleNoShow(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record BookingRecord,
) (int, error) {
	snapshot, err := decodeQuoteSnapshot(record.QuoteSnapshot)
	if err != nil {
		return 0, err
	}
	policy, err := DecodeLodgingPolicy(record.PolicySnapshot)
	if err != nil {
		return 0, err
	}
	quote, err := QuoteNoShow(policy, snapshot)
	if err != nil {
		return 0, err
	}

	if _, err := s.bookings.LockInventoryNights(ctx, tx, rc.TenantID, record.RoomTypeID,
		record.CheckIn, record.LastNight()); err != nil {
		return 0, err
	}
	if err := s.bookings.AddConfirmed(ctx, tx, rc.TenantID, record.RoomTypeID,
		record.CheckIn, record.LastNight(), -1); err != nil {
		return 0, err
	}
	moved, err := s.bookings.MarkBookingNoShowRow(ctx, tx, rc.TenantID, record.ID,
		rc.Principal.ActorID)
	if err != nil {
		return 0, err
	}
	if !moved {
		return 0, ErrBookingTransitionInvalid
	}

	consumed := quote.Nights
	if consumed > snapshot.CoveredNights {
		consumed = snapshot.CoveredNights
	}
	if record.AuthorizationID == nil {
		return consumed, nil
	}
	if consumed > 0 {
		if _, err := s.auths.Consume(ctx, tx, BookingConsumeInput{
			TenantID: rc.TenantID, ActorID: rc.Principal.ActorID,
			AuthorizationID:     *record.AuthorizationID,
			ServiceDefinitionID: snapshot.ServiceDefinitionID,
			Nights:              fmt.Sprintf("%d", consumed),
			Key:                 noShowPenaltyKey(record.ID), ReasonCode: ReasonNoShowPenalty,
		}); err != nil {
			return 0, err
		}
	}
	if rest := snapshot.CoveredNights - consumed; rest > 0 {
		if _, err := s.auths.ReleaseUnused(ctx, tx, BookingReleaseInput{
			TenantID: rc.TenantID, ActorID: rc.Principal.ActorID,
			AuthorizationID: *record.AuthorizationID,
			Nights:          fmt.Sprintf("%d", rest), ReasonCode: ReasonNoShowRelease,
		}); err != nil {
			return 0, err
		}
	}
	return consumed, s.auths.RevokeVouchers(ctx, tx, rc, *record.AuthorizationID, CancelReasonNoShow)
}

// workItemAggregateBooking is what a work item raised from here points at.
const workItemAggregateBooking = "BOOKING"

// actorOrNil renders the caller for a work item, or nothing for a background job that has
// no person behind it.
func actorOrNil(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	out := id
	return &out
}
