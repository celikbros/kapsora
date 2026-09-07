package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// The waiting list, and the sweep that empties it.
//
// The tenants are read from platform.tenant, like every other sweep in this vertical, and
// never from the queue itself. `accommodation.waitlist_entry` carries forced row-level
// security, so a cross-tenant read of it outside a tenant transaction sees nothing at all --
// a "which tenants have anybody waiting" query would come back empty and the sweep would
// silently offer nothing, for ever, with no error anywhere.
//
// The whole package is one sentence: **a freed room reaches the queue in queue order without
// anybody watching.** A member whose dates are full asks to be told; a cancellation, an
// expiry or a check-out frees a night; and five minutes later the person at the front of the
// queue is holding that room with a countdown running and a message on the way. Nobody at
// the payer or the property does anything.
//
// Two properties make that safe to run every five minutes on more than one scheduler. The
// queue read is `FOR UPDATE SKIP LOCKED`, so two sweeps that both believe they lead take
// different entries and neither waits. And the offer is placed by WP-I6-02's own
// `CreateHold` -- the same lock order, the same counters, the same entitlement reservation --
// so a room offered to a waiting member and a room taken by somebody at the search screen
// are the same act and cannot oversell each other.

// WaitlistOfferBatch bounds one pass of the offer sweep per tenant. A five-minute window's
// worth of freed rooms is a handful; the bound is here so a tenant nobody has swept for a day
// cannot hold one transaction open over ten thousand entries.
const WaitlistOfferBatch = 200

// JoinWaitlistInput is a member asking to be told when a room frees up.
type JoinWaitlistInput struct {
	PersonID   uuid.UUID
	PropertyID uuid.UUID
	// RoomTypeID is optional. Nil means any room of the property, which is what a member
	// who wants the hotel rather than the suite is actually asking for.
	RoomTypeID *uuid.UUID
	CheckIn    time.Time
	CheckOut   time.Time
	Adults     int
	Children   int
	// Priority is the plan's own ordering. It is the desk's to set -- a programme that gives
	// its own people the first refusal -- and a member joining for themselves always joins
	// at zero, because a queue a member could push themselves up is not a queue.
	Priority  int
	ProgramID *uuid.UUID
}

// WaitlistFilter is the API-level list request.
type WaitlistFilter struct {
	PersonID   *uuid.UUID
	PropertyID *uuid.UUID
	Status     string
	Limit      int
}

// WaitlistView is one entry with the booking it was offered, when it has one.
type WaitlistView struct {
	Entry WaitlistRecord
	// Offer is the held booking the sweep created on this member's behalf. It is nil for an
	// entry that is still waiting, and it is what `acceptWaitlistOffer` confirms.
	Offer *BookingView
}

// JoinWaitlist puts a member in the queue for a property and a set of dates.
//
// It checks nothing about availability, and that is the point: a member joins a queue
// *because* the dates are full, and a join that refused when the room was free would refuse
// exactly nobody -- they would have booked it. What it does check is the plan, because an
// entry with no enrollment behind it is a place in a queue that could never become a booking.
func (s *Service) JoinWaitlist(ctx context.Context, rc identity.RequestContext,
	in JoinWaitlistInput,
) (WaitlistView, error) {
	if s.bookings == nil {
		return WaitlistView{}, errors.New("accommodation: this process cannot hold a waitlist")
	}
	if in.PersonID == uuid.Nil {
		return WaitlistView{}, ErrPersonRequired
	}
	checkIn, checkOut := domain.Day(in.CheckIn), domain.Day(in.CheckOut)
	if err := validateWaitlistEntry(in, checkIn, checkOut); err != nil {
		return WaitlistView{}, err
	}

	var out WaitlistView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		property, err := s.repo.GetProperty(ctx, tx, rc.TenantID, in.PropertyID, scopeOf(rc))
		if err != nil {
			return err
		}
		if in.RoomTypeID != nil {
			room, err := s.repo.GetRoomType(ctx, tx, rc.TenantID, *in.RoomTypeID, scopeOf(rc))
			if err != nil {
				return err
			}
			if room.RoomType.PropertyID != property.ID {
				return ErrRoomTypeNotFound
			}
		}
		plan, err := s.bookings.PersonEnrollmentForStay(ctx, tx, rc.TenantID, in.PersonID,
			checkIn, in.ProgramID)
		if err != nil {
			return err
		}
		entry, err := s.bookings.CreateWaitlistEntry(ctx, tx, rc.TenantID, NewWaitlistRow{
			PersonID: in.PersonID, EnrollmentID: plan.EnrollmentID, PropertyID: property.ID,
			RoomTypeID: in.RoomTypeID, CheckIn: checkIn, CheckOut: checkOut,
			Adults: in.Adults, Children: in.Children, Priority: in.Priority,
			CreatedAt: s.now().UTC(), ActorID: rc.Principal.ActorID,
		})
		if err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, ActionWaitlistJoin, ResourceWaitlist, entry.ID,
			map[string]any{
				"property_id": property.ID.String(), "check_in": checkIn.Format(time.DateOnly),
				"nights": entry.Nights(), "priority": entry.Priority,
			}); err != nil {
			return err
		}
		out = WaitlistView{Entry: entry}
		return nil
	})
	if err != nil {
		return WaitlistView{}, err
	}
	return out, nil
}

// ListWaitlist returns the queue this caller may see, in the order the sweep walks it, so the
// list a member is shown their place in is the list the offer actually comes out of.
func (s *Service) ListWaitlist(ctx context.Context, rc identity.RequestContext, f WaitlistFilter) (
	[]WaitlistView, error,
) {
	if s.bookings == nil {
		return nil, nil
	}
	if f.Status != "" && !domain.InList(f.Status, WaitlistStatuses) {
		return nil, fieldError("status", "ENUM", "tanınmayan bekleme listesi durumu")
	}
	person := personBoundary(rc)
	if person == nil {
		person = f.PersonID
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var out []WaitlistView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := s.bookings.ListWaitlistEntries(ctx, tx, rc.TenantID, WaitlistQuery{
			ScopeIDs: scopeOf(rc), PersonID: person, PropertyID: f.PropertyID,
			Status: f.Status, PageSize: limit,
		})
		if err != nil {
			return err
		}
		out = make([]WaitlistView, 0, len(rows))
		for _, row := range rows {
			view := WaitlistView{Entry: row}
			if row.OfferedBookingID != nil {
				booking, err := s.bookings.GetBooking(ctx, tx, rc.TenantID, *row.OfferedBookingID,
					nil, nil)
				if err == nil {
					loaded, err := s.loadBooking(ctx, tx, rc.TenantID, booking)
					if err != nil {
						return err
					}
					view.Offer = &loaded
				} else if !errors.Is(err, ErrBookingNotFound) {
					return err
				}
			}
			out = append(out, view)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CancelWaitlistEntry is the member giving up their place. An entry that was holding an
// offer gives the room back with it, because a room set aside for somebody who has walked
// away is a room nobody can book and nobody will use.
func (s *Service) CancelWaitlistEntry(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID,
) (WaitlistView, error) {
	if s.bookings == nil {
		return WaitlistView{}, ErrWaitlistEntryNotFound
	}
	var out WaitlistView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.bookings.GetWaitlistEntry(ctx, tx, rc.TenantID, id, personBoundary(rc),
			scopeOf(rc)); err != nil {
			return err
		}
		entry, err := s.bookings.LockWaitlistEntry(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		if entry.Status != WaitlistWaiting && entry.Status != WaitlistOffered {
			return ErrWaitlistTransitionInvalid
		}
		if entry.OfferedBookingID != nil {
			if err := s.releaseOfferedHold(ctx, tx, rc, *entry.OfferedBookingID); err != nil {
				return err
			}
		}
		moved, err := s.bookings.CancelWaitlistEntryRow(ctx, tx, rc.TenantID, entry.ID,
			rc.Principal.ActorID)
		if err != nil {
			return err
		}
		if !moved {
			return ErrWaitlistTransitionInvalid
		}
		if err := s.record(ctx, tx, rc, ActionWaitlistCancel, ResourceWaitlist, entry.ID,
			map[string]any{"property_id": entry.PropertyID.String()}); err != nil {
			return err
		}
		entry.Status = WaitlistCancelled
		entry.OfferedBookingID = nil
		entry.OfferExpiresAt = nil
		out = WaitlistView{Entry: entry}
		return nil
	})
	if err != nil {
		return WaitlistView{}, err
	}
	return out, nil
}

// AcceptWaitlistOffer turns the hold the sweep placed into a confirmation.
//
// It confirms the booking through WP-I6-02's own path -- the same quote staleness rule, the
// same step-up threshold, the same reservation request -- because an offer accepted is a
// booking confirmed, and a second way to confirm one would be a second set of rules for the
// same act.
func (s *Service) AcceptWaitlistOffer(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID,
) (WaitlistView, error) {
	if s.bookings == nil {
		return WaitlistView{}, ErrWaitlistEntryNotFound
	}
	var out WaitlistView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.bookings.GetWaitlistEntry(ctx, tx, rc.TenantID, id, personBoundary(rc),
			scopeOf(rc)); err != nil {
			return err
		}
		entry, err := s.bookings.LockWaitlistEntry(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		if entry.Status != WaitlistOffered || entry.OfferedBookingID == nil {
			return ErrWaitlistNotOffered
		}
		booking, err := s.confirm(ctx, tx, rc, *entry.OfferedBookingID)
		if err != nil {
			return err
		}
		moved, err := s.bookings.AcceptWaitlistEntryRow(ctx, tx, rc.TenantID, entry.ID,
			rc.Principal.ActorID)
		if err != nil {
			return err
		}
		if !moved {
			return ErrWaitlistNotOffered
		}
		if err := s.record(ctx, tx, rc, ActionWaitlistAccept, ResourceWaitlist, entry.ID,
			map[string]any{"booking_id": booking.Booking.ID.String()}); err != nil {
			return err
		}
		entry.Status = WaitlistAccepted
		entry.OfferExpiresAt = nil
		out = WaitlistView{Entry: entry, Offer: &booking}
		return nil
	})
	if err != nil {
		return WaitlistView{}, err
	}
	return out, nil
}

// releaseOfferedHold gives back a room a waiting member was offered and did not take. It is
// WP-I6-02's own give-back, so the counters and the entitlement move exactly as they do when
// a member releases a hold themselves.
func (s *Service) releaseOfferedHold(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	bookingID uuid.UUID,
) error {
	booking, err := s.bookings.LockBooking(ctx, tx, rc.TenantID, bookingID)
	if errors.Is(err, ErrBookingNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !domain.InList(booking.Status, domain.HeldBookingStatuses) {
		// The hold has already expired, been confirmed or been released. Whatever happened
		// to it, it is no longer this entry's to give back.
		return nil
	}
	if err := s.giveBackRoom(ctx, tx, rc, booking, reasonHoldReleased); err != nil {
		return err
	}
	moved, err := s.bookings.CancelBookingRow(ctx, tx, rc.TenantID, booking.ID, s.now().UTC(),
		domain.CancelReasonHoldReleased, rc.Principal.ActorID)
	if err != nil || !moved {
		return err
	}
	return s.notifyBookingCancelled(ctx, tx, rc.TenantID, booking,
		domain.CancelReasonHoldReleased, "0")
}

// validateWaitlistEntry answers 422 before anything is read or written.
func validateWaitlistEntry(in JoinWaitlistInput, checkIn, checkOut time.Time) error {
	ve := &domain.ValidationError{}
	if in.PropertyID == uuid.Nil {
		ve.Add("propertyId", "REQUIRED", "tesis zorunlu")
	}
	if in.Adults < 1 || in.Adults > 20 {
		ve.Add("adults", "RANGE", "1-20 arasında olmalı")
	}
	if in.Children < 0 || in.Children > 20 {
		ve.Add("children", "RANGE", "0-20 arasında olmalı")
	}
	if in.Priority < 0 || in.Priority > 1000 {
		ve.Add("priority", "RANGE", "0-1000 arasında olmalı")
	}
	if checkIn.IsZero() {
		ve.Add("checkIn", "REQUIRED", "giriş tarihi zorunlu")
	}
	if checkOut.IsZero() {
		ve.Add("checkOut", "REQUIRED", "çıkış tarihi zorunlu")
	}
	if !checkIn.IsZero() && !checkOut.IsZero() && !checkOut.After(checkIn) {
		ve.Add("checkOut", "RANGE", "giriş tarihinden sonra olmalı")
	}
	if ve.Len() > 0 {
		return ve
	}
	return nil
}

// OfferWaitlistRooms is the sweep: it expires the offers nobody took and hands the rooms that
// are free to whoever is at the front of the queue.
//
// It runs every five minutes and on nothing else -- no command calls it, and no release
// triggers it -- because a sweep that some paths ran and others did not would be a queue that
// worked for cancellations and quietly did not for expiries. Running it changes nothing when
// there is nothing free, and running it twice offers each room once: the entry's status
// predicate and the hold's own inventory lock together make the second pass a no-op.
func (s *Service) OfferWaitlistRooms(ctx context.Context, now time.Time) (int, error) {
	tenants, err := s.activeTenants(ctx)
	if err != nil {
		return 0, err
	}
	offered := 0
	for _, tenantID := range tenants {
		count, err := s.sweepTenantWaitlist(ctx, tenantID, now.UTC())
		if err != nil {
			return offered, err
		}
		offered += count
	}
	return offered, nil
}

func (s *Service) sweepTenantWaitlist(ctx context.Context, tenantID uuid.UUID, now time.Time) (int, error) {
	rc := systemActor(tenantID)
	// The offers nobody took go back first, so a room freed by an expiring offer is
	// available to the entry behind it in the very same pass.
	if err := s.expireWaitlistOffers(ctx, rc, now); err != nil {
		return 0, err
	}

	var queue []WaitlistRecord
	if err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := s.bookings.ListWaitlistQueue(ctx, tx, tenantID, WaitlistOfferBatch)
		queue = rows
		return err
	}); err != nil {
		return 0, fmt.Errorf("accommodation: read waitlist queue for tenant %s: %w", tenantID, err)
	}

	offered := 0
	for _, entry := range queue {
		made, err := s.offerOne(ctx, rc, entry)
		if err != nil {
			return offered, fmt.Errorf("accommodation: offer waitlist entry %s: %w", entry.ID, err)
		}
		if made {
			offered++
		}
	}
	return offered, nil
}

// expireWaitlistOffers puts every offer nobody accepted back in the queue, behind the people
// who were already waiting when it was made.
//
// The hold behind it is left to WP-I6-02's own expiry sweep rather than released here: both
// run on the same schedule, the hold's release is keyed by the booking, and a second command
// racing the sweep for the same room is exactly the double release this design avoids.
func (s *Service) expireWaitlistOffers(ctx context.Context, rc identity.RequestContext,
	now time.Time,
) error {
	var ids []uuid.UUID
	if err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		found, err := s.bookings.ListExpiredWaitlistOffers(ctx, tx, rc.TenantID, now,
			WaitlistOfferBatch)
		ids = found
		return err
	}); err != nil {
		return fmt.Errorf("accommodation: list expired offers for tenant %s: %w", rc.TenantID, err)
	}
	for _, id := range ids {
		if err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
			entry, err := s.bookings.LockWaitlistEntry(ctx, tx, rc.TenantID, id)
			if err != nil {
				return err
			}
			if entry.Status != WaitlistOffered {
				return nil
			}
			// The queue is ordered by created_at, so moving it to now *is* going to the
			// back. A member who let a room go waits again rather than being handed the
			// next one first.
			moved, err := s.bookings.ReturnWaitlistEntryToQueue(ctx, tx, rc.TenantID, entry.ID, now)
			if err != nil || !moved {
				return err
			}
			return s.record(ctx, tx, rc, ActionWaitlistExpire, ResourceWaitlist, entry.ID,
				map[string]any{"property_id": entry.PropertyID.String()})
		}); err != nil {
			return fmt.Errorf("accommodation: requeue waitlist entry %s: %w", id, err)
		}
	}
	return nil
}

// offerOne tries to place a hold for one entry, on the room type it named or on any of the
// property's if it named none.
//
// The hold is WP-I6-02's own `CreateHold`, with everything that comes with it: the stay-date
// lock order, the availability check under that lock, the frozen quote and the entitlement
// reservation with its own deadline. A room that is not free refuses the hold, and that
// refusal is exactly what "this entry's turn has not come" means -- so it is passed over
// silently and the next entry is tried.
func (s *Service) offerOne(ctx context.Context, rc identity.RequestContext, entry WaitlistRecord) (bool, error) {
	roomTypes, err := s.offerCandidates(ctx, rc, entry)
	if err != nil {
		return false, err
	}
	for _, roomTypeID := range roomTypes {
		view, err := s.CreateHold(ctx, rc, HoldInput{
			PersonID: entry.PersonID, RoomTypeID: roomTypeID,
			CheckIn: entry.CheckIn, CheckOut: entry.CheckOut,
			Adults: entry.Adults, Children: entry.Children,
			Channel: domain.ChannelBackoffice,
		})
		if err != nil {
			if offerRefusalIsOrdinary(err) {
				continue
			}
			return false, err
		}
		made, err := s.recordOffer(ctx, rc, entry, view)
		if err != nil {
			return false, err
		}
		if made {
			return true, nil
		}
		// Somebody took this entry between the queue read and here. The room this hold set
		// aside is given straight back rather than left standing for nobody.
		if err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
			return s.releaseOfferedHold(ctx, tx, rc, view.Booking.ID)
		}); err != nil {
			return false, err
		}
		return false, nil
	}
	return false, nil
}

// offerCandidates is the room types this entry would accept: the one it named, or every
// active room type of the property when it named none. "Any room of this hotel" is what that
// member asked for, and the sweep tries each rather than guessing which one they meant.
func (s *Service) offerCandidates(ctx context.Context, rc identity.RequestContext,
	entry WaitlistRecord,
) ([]uuid.UUID, error) {
	if entry.RoomTypeID != nil {
		return []uuid.UUID{*entry.RoomTypeID}, nil
	}
	var out []uuid.UUID
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		ids, err := s.bookings.ListPropertyRoomTypeIDs(ctx, tx, rc.TenantID, entry.PropertyID)
		out = ids
		return err
	})
	return out, err
}

// recordOffer marks the entry OFFERED with the hold's own deadline and tells the member.
//
// The deadline is the hold's rather than one of this package's own, because they are the same
// deadline: the room comes back when the hold expires, and an offer that outlived its hold
// would be an offer whose room somebody else had already been sold.
func (s *Service) recordOffer(ctx context.Context, rc identity.RequestContext,
	entry WaitlistRecord, view BookingView,
) (bool, error) {
	expires := view.Booking.HoldExpiresAt
	if expires == nil {
		return false, errors.New("accommodation: the offered hold carries no deadline")
	}
	made := false
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		moved, err := s.bookings.OfferWaitlistEntry(ctx, tx, rc.TenantID, entry.ID,
			view.Booking.ID, *expires)
		if err != nil || !moved {
			return err
		}
		made = true
		if err := s.record(ctx, tx, rc, ActionWaitlistOffer, ResourceWaitlist, entry.ID,
			map[string]any{
				"booking_id": view.Booking.ID.String(),
				"expires_at": expires.Format(time.RFC3339),
			}); err != nil {
			return err
		}
		room, err := s.bookings.RoomTypeBookingContext(ctx, tx, rc.TenantID,
			view.Booking.RoomTypeID, nil)
		if err != nil {
			return err
		}
		return s.notifyWaitlistOffered(ctx, tx, rc.TenantID, entry, view.Booking,
			room.PropertyName, *expires)
	})
	return made, err
}

// offerRefusalIsOrdinary answers whether a refused hold means "not this entry's turn" rather
// than "something is wrong".
//
// A full room, a room the plan carries no night of, a member who already holds this room for
// these dates, a room type that cannot be priced, a party that does not fit: every one of
// them is an ordinary answer to "may this entry have this room now", and the sweep moves on.
// Anything else is a fault and stops the pass, because a sweep that swallowed a database
// error would be a queue that quietly stopped working.
func offerRefusalIsOrdinary(err error) bool {
	switch {
	case errors.Is(err, ErrRoomUnavailable),
		errors.Is(err, ErrEntitlementInsufficient),
		errors.Is(err, ErrEntitlementAccountNotFound),
		errors.Is(err, ErrBookingAlreadyLive),
		errors.Is(err, ErrQuoteUnavailable),
		errors.Is(err, ErrOccupancyExceeded),
		errors.Is(err, ErrEnrollmentNotFound),
		errors.Is(err, ErrRoomTypeNotFound),
		errors.Is(err, ErrPropertyNotFound):
		return true
	default:
		return false
	}
}
