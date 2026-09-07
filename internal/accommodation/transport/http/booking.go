package accommodationhttp

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/accommodation/application"
	"github.com/celikbros/kapsora/internal/accommodation/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// BookingMiddlewares are the wrappers the integrator applies in cmd/api.
//
// There is deliberately no entry for the voucher route. The Idempotency-Key middleware
// persists response bodies so a replay can be answered from one, and that response is the
// only place in this system a usable voucher token exists; wrapping it would put the token
// in a column. WP-I4-02's own issue route is registered the same way and for the same
// reason.
type BookingMiddlewares struct {
	CreateHold  func(http.Handler) http.Handler
	Confirm     func(http.Handler) http.Handler
	ReleaseHold func(http.Handler) http.Handler
}

// HoldRoutes mounts POST /accommodation/holds.
func (h *Handler) HoldRoutes(r chi.Router, mw BookingMiddlewares) {
	r.With(wrap(mw.CreateHold)).Post("/", h.CreateHold)
}

// BookingRoutes mounts everything below /accommodation/bookings.
func (h *Handler) BookingRoutes(r chi.Router, mw BookingMiddlewares) {
	r.Get("/", h.ListBookings)
	r.Get("/{bookingId}", h.GetBooking)
	r.With(wrap(mw.Confirm)).Post("/{bookingId}/confirm", h.ConfirmBooking)
	r.With(wrap(mw.ReleaseHold)).Post("/{bookingId}/release", h.ReleaseHold)
	// No Idempotency-Key wrapper here; see BookingMiddlewares.
	r.Post("/{bookingId}/voucher", h.ReissueBookingVoucher)
}

// CreateHold serves POST /accommodation/holds.
//
// The one thing this handler decides is whose stay it is, and it decides it exactly as the
// availability search does: a caller bound to a person goes through identity.RequirePerson,
// so a body naming somebody else is refused with PERSON_SCOPE rather than quietly holding a
// room for the member's neighbour.
func (h *Handler) CreateHold(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireBookingWrite(w, r)
	if !ok {
		return
	}
	var body kapsorav1.CreateHoldRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	personID, ok := h.personFor(w, r, rc, body.PersonId)
	if !ok {
		return
	}
	in := application.HoldInput{
		PersonID: personID, RoomTypeID: body.RoomTypeId,
		CheckIn: body.CheckIn.Time, CheckOut: body.CheckOut.Time,
		Adults: body.Adults, ProgramID: body.ProgramId,
	}
	if body.Children != nil {
		in.Children = *body.Children
	}
	if body.Channel != nil {
		in.Channel = string(*body.Channel)
	}
	if body.Guests != nil {
		for _, guest := range *body.Guests {
			entry := application.GuestInput{
				PersonID: guest.PersonId, DisplayName: guest.DisplayName,
				GuestType: string(guest.GuestType),
			}
			if guest.IsMinor != nil {
				entry.IsMinor = *guest.IsMinor
			}
			in.Guests = append(in.Guests, entry)
		}
	}

	view, err := h.svc.CreateHold(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, bookingView(view))
}

// ListBookings serves GET /accommodation/bookings.
func (h *Handler) ListBookings(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.BookingFilter{
		Cursor:     r.URL.Query().Get("cursor"),
		Limit:      queryLimit(r),
		PersonID:   queryUUID(r, "personId", &fields),
		PropertyID: queryUUID(r, "propertyId", &fields),
		Status:     r.URL.Query().Get("status"),
	}
	filter.CheckInFrom = optionalQueryDate(r, "checkInFrom", &fields)
	filter.CheckInTo = optionalQueryDate(r, "checkInTo", &fields)
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	page, err := h.svc.ListBookings(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.BookingList{Items: make([]kapsorav1.Booking, 0, len(page.Items))}
	for _, item := range page.Items {
		out.Items = append(out.Items, bookingView(item))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, out)
}

// GetBooking serves GET /accommodation/bookings/{bookingId}.
func (h *Handler) GetBooking(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "bookingId", application.ErrBookingNotFound)
	if !ok {
		return
	}
	view, err := h.svc.GetBooking(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bookingView(view))
}

// ConfirmBooking serves POST /accommodation/bookings/{bookingId}/confirm.
func (h *Handler) ConfirmBooking(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireBookingWrite(w, r)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "bookingId", application.ErrBookingNotFound)
	if !ok {
		return
	}
	view, err := h.svc.ConfirmBooking(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bookingView(view))
}

// ReleaseHold serves POST /accommodation/bookings/{bookingId}/release.
func (h *Handler) ReleaseHold(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireBookingWrite(w, r)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "bookingId", application.ErrBookingNotFound)
	if !ok {
		return
	}
	view, err := h.svc.ReleaseHold(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bookingView(view))
}

// ReissueBookingVoucher serves POST /accommodation/bookings/{bookingId}/voucher.
//
// This is the only response in the vertical that carries a secret, and the route is
// deliberately not wrapped by the Idempotency-Key middleware: that middleware stores
// response bodies for replay, and storing this one would write a usable voucher into a
// column. A replay mints a new token and retires the previous digest, so nobody ends up
// holding two codes that both work.
func (h *Handler) ReissueBookingVoucher(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireBookingWrite(w, r)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "bookingId", application.ErrBookingNotFound)
	if !ok {
		return
	}
	issued, err := h.svc.IssueBookingVoucher(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, kapsorav1.BookingVoucher{
		Id: issued.ID, Token: issued.Token, MaskedToken: issued.MaskedToken,
		ValidFrom: issued.ValidFrom.UTC(), ValidTo: issued.ValidTo.UTC(),
	})
}

// requireBookingWrite accepts either of the two grants that may hold and confirm a room.
//
// They are two on purpose and neither is a superset of the other in the role catalogue:
// `accommodation.booking.create` is what a member holds, so they may book for themselves,
// and `accommodation.booking.manage` is what a reservation desk holds, which is the same
// commands for somebody else. Whose stay it actually is remains the person binding's
// answer, not this check's — a member holding create still cannot name their neighbour.
func (h *Handler) requireBookingWrite(w http.ResponseWriter, r *http.Request) (identity.RequestContext, bool) {
	rc, err := identity.Require(r.Context(), application.PermissionBookingCreate)
	if err == nil {
		return rc, true
	}
	if desk, deskErr := identity.Require(r.Context(), application.PermissionBookingManage); deskErr == nil {
		return desk, true
	}
	h.deny.Deny(w, r, err, application.PermissionBookingCreate)
	return identity.RequestContext{}, false
}

// personFor resolves whose stay a command is about. A caller bound to a person is held to
// that binding; one that is not must name somebody, because a booking is a booking for
// exactly one member and there is no such thing without one.
func (h *Handler) personFor(w http.ResponseWriter, r *http.Request, rc identity.RequestContext,
	requested *uuid.UUID,
) (uuid.UUID, bool) {
	asked := uuid.Nil
	if requested != nil {
		asked = *requested
	}
	personID := asked
	if _, bound := rc.PersonScope(); bound {
		var err error
		if _, personID, err = identity.RequirePerson(r.Context(), asked); err != nil {
			h.writeError(w, r, err)
			return uuid.Nil, false
		}
	}
	if personID == uuid.Nil {
		h.writeError(w, r, application.ErrPersonRequired)
		return uuid.Nil, false
	}
	return personID, true
}

// bookingView renders a booking. The frozen quote is decoded and re-rendered rather than
// passed through as raw JSON, so the contract's own shape is what a client sees and a
// snapshot written by an older version cannot leak a field the schema does not describe.
func bookingView(in application.BookingView) kapsorav1.Booking {
	record := in.Booking
	out := kapsorav1.Booking{
		Id: record.ID, Reference: record.Reference, PersonId: record.PersonID,
		PropertyId: record.PropertyID, RoomTypeId: record.RoomTypeID,
		CheckIn:  openapi_types.Date{Time: record.CheckIn},
		CheckOut: openapi_types.Date{Time: record.CheckOut},
		Nights:   record.Nights, Adults: record.Adults, Children: record.Children,
		Status:          kapsorav1.BookingStatus(record.Status),
		Channel:         kapsorav1.ServiceRequestChannel(record.Channel),
		QuoteSnapshot:   quoteSnapshotView(record.QuoteSnapshot),
		SecondsToExpiry: in.SecondsToExpiry,
		NightlyAmounts:  make([]kapsorav1.BookingNight, 0, len(in.Nights)),
		Guests:          make([]kapsorav1.BookingGuest, 0, len(in.Guests)),
		CreatedAt:       record.CreatedAt,
	}
	out.EnrollmentId = optionalUUID(record.EnrollmentID)
	out.ProgramId = optionalUUID(record.ProgramID)
	out.HoldExpiresAt = record.HoldExpiresAt
	out.EntitlementReservationId = record.EntitlementReservationID
	out.ServiceRequestId = record.ServiceRequestID
	out.AuthorizationId = record.AuthorizationID
	out.VoucherId = record.VoucherID
	out.ConfirmedAt = record.ConfirmedAt
	out.CheckedInAt = record.CheckedInAt
	out.CheckedOutAt = record.CheckedOutAt
	out.CancelledAt = record.CancelledAt
	out.CancelReasonCode = record.CancelReasonCode
	out.ActualNights = record.ActualNights
	if !record.UpdatedAt.IsZero() {
		updated := record.UpdatedAt
		out.UpdatedAt = &updated
	}
	if record.RowVersion > 0 {
		version := int(record.RowVersion)
		out.RowVersion = &version
	}
	if len(record.PolicySnapshot) > 0 {
		var policy kapsorav1.LodgingPolicySnapshot
		if err := json.Unmarshal(record.PolicySnapshot, &policy); err == nil {
			out.PolicySnapshot = &policy
		}
	}
	for _, night := range in.Nights {
		out.NightlyAmounts = append(out.NightlyAmounts, kapsorav1.BookingNight{
			StayDate:   openapi_types.Date{Time: night.StayDate},
			UnitAmount: night.UnitAmount, PayerAmount: night.PayerAmount,
			MemberAmount: night.MemberAmount, CurrencyCode: night.CurrencyCode,
		})
	}
	for _, guest := range in.Guests {
		id := guest.ID
		out.Guests = append(out.Guests, kapsorav1.BookingGuest{
			Id: &id, PersonId: guest.PersonID, DisplayName: guest.DisplayName,
			GuestType: kapsorav1.BookingGuestType(guest.GuestType), IsMinor: guest.IsMinor,
		})
	}
	return out
}

// quoteSnapshotView renders the frozen quote. A snapshot that cannot be decoded renders as
// an empty one rather than failing the read: the booking is still a fact, and a member
// looking at their reservation should not get a 500 because of a document shape.
func quoteSnapshotView(raw []byte) kapsorav1.BookingQuoteSnapshot {
	snapshot, err := application.DecodeQuoteSnapshot(raw)
	if err != nil {
		return kapsorav1.BookingQuoteSnapshot{}
	}
	out := kapsorav1.BookingQuoteSnapshot{
		Version: snapshot.Version, QuotedAt: snapshot.QuotedAt.UTC(),
		EvaluationId: snapshot.EvaluationID, PropertyId: snapshot.PropertyID,
		RoomTypeId: snapshot.RoomTypeID, ServiceDefinitionId: snapshot.ServiceDefinitionID,
		CurrencyCode: snapshot.CurrencyCode, TotalAmount: snapshot.TotalAmount,
		PayerAmount: snapshot.PayerAmount, MemberAmount: snapshot.MemberAmount,
		CoveredNights: snapshot.CoveredNights, Eligible: snapshot.Eligible,
		Nights: make([]kapsorav1.BookingQuoteNight, 0, len(snapshot.Nights)),
	}
	for _, night := range snapshot.Nights {
		day, err := time.Parse(time.DateOnly, night.StayDate)
		if err != nil {
			continue
		}
		out.Nights = append(out.Nights, kapsorav1.BookingQuoteNight{
			StayDate: openapi_types.Date{Time: day}, Amount: night.Amount,
			PayerAmount: night.PayerAmount, MemberAmount: night.MemberAmount,
		})
	}
	if snapshot.Entitlement != nil {
		out.Entitlement = &kapsorav1.AvailabilityEntitlement{
			EntitlementCode: snapshot.Entitlement.EntitlementCode,
			Unit:            snapshot.Entitlement.Unit,
			Remaining:       snapshot.Entitlement.Remaining,
		}
	}
	return out
}

func optionalUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	value := id
	return &value
}

// optionalQueryDate reads an optional YYYY-MM-DD filter; a malformed one is a field error
// rather than a silently empty page.
func optionalQueryDate(r *http.Request, name string, fields *[]domain.FieldError) *time.Time {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil
	}
	day, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		*fields = append(*fields, domain.FieldError{
			Field: name, Code: "FORMAT", Message: "YYYY-MM-DD biçiminde olmalı",
		})
		return nil
	}
	return &day
}
