package accommodationhttp

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/accommodation/application"
	"github.com/celikbros/kapsora/internal/accommodation/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// The routes of WP-I6-03: what happens to a booking after it is agreed.
//
// One rule shapes them, the same one the rest of this package follows: **no handler here
// decides who a request is for**. The provider boundary is the service's, the member's own
// binding is identity's, and the two sides of a no-show are told apart by scope rather than
// by anything a body could say.
//
// The voucher token is the one value in this file that gets special treatment, and it gets
// exactly one: it arrives in a request body and is never read from a path, a query string or
// a header. Everything in a URL is in every proxy log between the desk and here.

// AfterMiddlewares are the Idempotency-Key wrappers the integrator applies in cmd/api.
//
// There is deliberately no entry for the check-in route. That route's body carries a usable
// voucher token, and the Idempotency-Key middleware persists request and response bodies for
// replay -- which would put the token in a column, which is the one thing this system does
// not do. A replayed check-in is refused by the voucher's own status instead, which is a
// better answer than a stored one.
type AfterMiddlewares struct {
	Cancel              func(http.Handler) http.Handler
	CheckOut            func(http.Handler) http.Handler
	ReportNoShow        func(http.Handler) http.Handler
	ReviewNoShow        func(http.Handler) http.Handler
	JoinWaitlist        func(http.Handler) http.Handler
	CancelWaitlistEntry func(http.Handler) http.Handler
	AcceptWaitlistOffer func(http.Handler) http.Handler
}

// AfterBookingRoutes mounts the commands that act on a booking somebody has agreed to. It is
// called with the same router BookingRoutes is given.
func (h *Handler) AfterBookingRoutes(r chi.Router, mw AfterMiddlewares) {
	r.Post("/{bookingId}/cancellation-preview", h.PreviewCancellation)
	r.With(wrap(mw.Cancel)).Post("/{bookingId}/cancel", h.CancelBooking)
	// No Idempotency-Key wrapper here; see AfterMiddlewares.
	r.Post("/{bookingId}/check-in", h.CheckInBooking)
	r.With(wrap(mw.CheckOut)).Post("/{bookingId}/check-out", h.CheckOutBooking)
	r.Get("/{bookingId}/no-show", h.GetNoShow)
	r.With(wrap(mw.ReportNoShow)).Post("/{bookingId}/no-show", h.ReportNoShow)
	r.With(wrap(mw.ReviewNoShow)).Post("/{bookingId}/no-show/review", h.ReviewNoShow)
}

// WaitlistRoutes mounts everything below /accommodation/waitlist.
func (h *Handler) WaitlistRoutes(r chi.Router, mw AfterMiddlewares) {
	r.Get("/", h.ListWaitlist)
	r.With(wrap(mw.JoinWaitlist)).Post("/", h.JoinWaitlist)
	r.With(wrap(mw.CancelWaitlistEntry)).Post("/{waitlistEntryId}/cancel", h.CancelWaitlistEntry)
	r.With(wrap(mw.AcceptWaitlistOffer)).Post("/{waitlistEntryId}/accept", h.AcceptWaitlistOffer)
}

// PreviewCancellation serves POST /accommodation/bookings/{bookingId}/cancellation-preview.
func (h *Handler) PreviewCancellation(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireBookingWrite(w, r)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "bookingId", application.ErrBookingNotFound)
	if !ok {
		return
	}
	view, err := h.svc.PreviewCancellation(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, kapsorav1.CancellationPreview{
		Booking: bookingView(view.Booking), Quote: cancellationQuoteView(view.Quote),
	})
}

// CancelBooking serves POST /accommodation/bookings/{bookingId}/cancel.
func (h *Handler) CancelBooking(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireBookingWrite(w, r)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "bookingId", application.ErrBookingNotFound)
	if !ok {
		return
	}
	var body kapsorav1.CancelBookingRequest
	if r.ContentLength > 0 && !decodeJSON(w, r, &body) {
		return
	}
	reason := ""
	if body.ReasonCode != nil {
		reason = *body.ReasonCode
	}
	view, err := h.svc.CancelBooking(r.Context(), rc, id, reason)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.CancellationResult{
		Booking: bookingView(view.Booking), Quote: cancellationQuoteView(view.Quote),
	}
	if view.Record != nil {
		out.Cancellation = cancellationView(*view.Record)
	}
	writeJSON(w, http.StatusOK, out)
}

// CheckInBooking serves POST /accommodation/bookings/{bookingId}/check-in.
//
// The token comes out of the body and goes straight into the command. It is not logged here,
// not echoed back, and not put into the problem detail of any refusal: a 404 for a token
// that belongs to another booking says only that this booking has no such code.
func (h *Handler) CheckInBooking(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, application.PermissionBookingManage)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "bookingId", application.ErrBookingNotFound)
	if !ok {
		return
	}
	var body kapsorav1.CheckInBookingRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.CheckInInput{Token: body.Token}
	if body.At != nil {
		in.At = *body.At
	}
	view, err := h.svc.CheckInBooking(r.Context(), rc, id, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bookingView(view))
}

// CheckOutBooking serves POST /accommodation/bookings/{bookingId}/check-out.
func (h *Handler) CheckOutBooking(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, application.PermissionBookingManage)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "bookingId", application.ErrBookingNotFound)
	if !ok {
		return
	}
	var body kapsorav1.CheckOutBookingRequest
	if r.ContentLength > 0 && !decodeJSON(w, r, &body) {
		return
	}
	in := application.CheckOutInput{}
	if body.At != nil {
		in.At = *body.At
	}
	view, err := h.svc.CheckOutBooking(r.Context(), rc, id, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bookingView(view))
}

// GetNoShow serves GET /accommodation/bookings/{bookingId}/no-show.
func (h *Handler) GetNoShow(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "bookingId", application.ErrBookingNotFound)
	if !ok {
		return
	}
	view, err := h.svc.GetNoShow(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, noShowResultView(view))
}

// ReportNoShow serves POST /accommodation/bookings/{bookingId}/no-show.
func (h *Handler) ReportNoShow(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, application.PermissionBookingManage)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "bookingId", application.ErrBookingNotFound)
	if !ok {
		return
	}
	var body kapsorav1.ReportNoShowRequest
	if r.ContentLength > 0 && !decodeJSON(w, r, &body) {
		return
	}
	in := application.ReportNoShowInput{EvidenceDocumentID: body.EvidenceDocumentId}
	if body.At != nil {
		in.At = *body.At
	}
	view, err := h.svc.ReportNoShow(r.Context(), rc, id, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, noShowResultView(view))
}

// ReviewNoShow serves POST /accommodation/bookings/{bookingId}/no-show/review.
func (h *Handler) ReviewNoShow(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, application.PermissionBookingManage)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "bookingId", application.ErrBookingNotFound)
	if !ok {
		return
	}
	var body kapsorav1.ReviewNoShowRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := h.svc.ReviewNoShow(r.Context(), rc, id, application.ReviewNoShowInput{
		Status: string(body.Status), Comment: body.Comment,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, noShowResultView(view))
}

// ListWaitlist serves GET /accommodation/waitlist.
func (h *Handler) ListWaitlist(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.WaitlistFilter{
		Limit:  queryLimit(r),
		Status: r.URL.Query().Get("status"),
	}
	filter.PersonID = queryUUID(r, "personId", &fields)
	filter.PropertyID = queryUUID(r, "propertyId", &fields)
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	entries, err := h.svc.ListWaitlist(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.WaitlistEntryList{Items: make([]kapsorav1.WaitlistEntry, 0, len(entries))}
	for _, entry := range entries {
		out.Items = append(out.Items, waitlistEntryView(entry))
	}
	writeJSON(w, http.StatusOK, out)
}

// JoinWaitlist serves POST /accommodation/waitlist.
//
// A member joining for themselves always joins at priority zero, whatever they send: a queue
// a member can push themselves up is not a queue. The desk grant is what raises it.
func (h *Handler) JoinWaitlist(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireWaitlistWrite(w, r)
	if !ok {
		return
	}
	var body kapsorav1.JoinWaitlistRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	personID, ok := h.personFor(w, r, rc, body.PersonId)
	if !ok {
		return
	}
	in := application.JoinWaitlistInput{
		PersonID: personID, PropertyID: body.PropertyId, RoomTypeID: body.RoomTypeId,
		CheckIn: body.CheckIn.Time, CheckOut: body.CheckOut.Time,
		Adults: body.Adults, ProgramID: body.ProgramId,
	}
	if body.Children != nil {
		in.Children = *body.Children
	}
	if body.Priority != nil && deskCaller(rc) {
		in.Priority = *body.Priority
	}
	view, err := h.svc.JoinWaitlist(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, waitlistEntryView(view))
}

// CancelWaitlistEntry serves POST /accommodation/waitlist/{waitlistEntryId}/cancel.
func (h *Handler) CancelWaitlistEntry(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireWaitlistWrite(w, r)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "waitlistEntryId", application.ErrWaitlistEntryNotFound)
	if !ok {
		return
	}
	view, err := h.svc.CancelWaitlistEntry(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, waitlistEntryView(view))
}

// AcceptWaitlistOffer serves POST /accommodation/waitlist/{waitlistEntryId}/accept.
func (h *Handler) AcceptWaitlistOffer(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireWaitlistWrite(w, r)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "waitlistEntryId", application.ErrWaitlistEntryNotFound)
	if !ok {
		return
	}
	view, err := h.svc.AcceptWaitlistOffer(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, waitlistEntryView(view))
}

// requireWaitlistWrite accepts either of the two grants that may act on a queue: a member's
// own `accommodation.booking.create`, which lets them join and leave for themselves, and the
// desk's `accommodation.waitlist.manage`, which is the same commands for somebody else.
// Whose entry it is remains the person binding's answer, not this check's.
func (h *Handler) requireWaitlistWrite(w http.ResponseWriter, r *http.Request) (
	identity.RequestContext, bool,
) {
	rc, err := identity.Require(r.Context(), application.PermissionBookingCreate)
	if err == nil {
		return rc, true
	}
	if desk, deskErr := identity.Require(r.Context(), application.PermissionWaitlistManage); deskErr == nil {
		return desk, true
	}
	h.deny.Deny(w, r, err, application.PermissionWaitlistManage)
	return identity.RequestContext{}, false
}

// deskCaller reports whether this caller is running the queue for somebody else rather than
// standing in it themselves. Only a desk may set a priority.
func deskCaller(rc identity.RequestContext) bool {
	if _, bound := rc.PersonScope(); bound {
		return false
	}
	return rc.Has(application.PermissionWaitlistManage)
}

// cancellationQuoteView renders what a cancellation costs. Every amount stays the exact
// decimal string the arithmetic produced; nothing here reformats a number.
func cancellationQuoteView(q application.CancellationQuote) kapsorav1.CancellationQuote {
	out := kapsorav1.CancellationQuote{
		Free: q.Free, PenaltyNights: q.PenaltyNights, ReleasedNights: q.ReleasedNights,
		FeeAmount: q.FeeAmount, PayerFee: q.PayerFee, MemberFee: q.MemberFee,
		CurrencyCode: q.CurrencyCode,
	}
	if !q.FreeUntil.IsZero() {
		until := q.FreeUntil.UTC()
		out.FreeUntil = &until
	}
	return out
}

// cancellationView renders the written row, including the policy it was judged by. The
// policy is decoded and re-rendered rather than passed through as raw JSON, so the contract's
// own shape is what a client sees and a snapshot written by an older version cannot leak a
// field the schema does not describe.
func cancellationView(c application.CancellationRecord) kapsorav1.Cancellation {
	out := kapsorav1.Cancellation{
		Id: c.ID, BookingId: c.BookingID, CancelledAt: c.CancelledAt.UTC(),
		CancelledBy: c.CancelledBy, ReasonCode: c.ReasonCode, Free: c.Free,
		PenaltyNights: c.PenaltyNights, ReleasedNights: c.ReleasedNights,
		FeeAmount: c.FeeAmount, PayerFee: c.PayerFee, MemberFee: c.MemberFee,
		CurrencyCode: c.CurrencyCode,
	}
	out.PolicySnapshot = policySnapshotView(c.PolicySnapshot)
	return out
}

// noShowResultView renders a report with the booking it is about.
func noShowResultView(in application.NoShowView) kapsorav1.NoShowResult {
	report := kapsorav1.NoShowReport{
		Id: in.Report.ID, BookingId: in.Report.BookingID,
		ReportedByActorId:  in.Report.ReportedByActorID,
		ReportedAt:         in.Report.ReportedAt.UTC(),
		EvidenceDocumentId: in.Report.EvidenceDocumentID,
		AssessedFeeAmount:  in.Report.AssessedFeeAmount,
		PayerAmount:        in.Report.PayerAmount, MemberAmount: in.Report.MemberAmount,
		CurrencyCode: in.Report.CurrencyCode,
		Status:       kapsorav1.NoShowStatus(in.Report.Status),
		ReviewedBy:   in.Report.ReviewedBy, ReviewComment: in.Report.ReviewComment,
		ConsumedNights: in.Report.ConsumedNights,
	}
	if in.Report.ReviewedAt != nil {
		at := in.Report.ReviewedAt.UTC()
		report.ReviewedAt = &at
	}
	return kapsorav1.NoShowResult{Report: report, Booking: bookingView(in.Booking)}
}

// waitlistEntryView renders one place in the queue, with the offer it is holding if the
// sweep has reached it.
func waitlistEntryView(in application.WaitlistView) kapsorav1.WaitlistEntry {
	entry := in.Entry
	out := kapsorav1.WaitlistEntry{
		Id: entry.ID, PersonId: entry.PersonID, PropertyId: entry.PropertyID,
		RoomTypeId: entry.RoomTypeID,
		CheckIn:    openapi_types.Date{Time: entry.CheckIn},
		CheckOut:   openapi_types.Date{Time: entry.CheckOut},
		Adults:     entry.Adults, Children: entry.Children, Priority: entry.Priority,
		Status:           kapsorav1.WaitlistStatus(entry.Status),
		OfferedBookingId: entry.OfferedBookingID,
		CreatedAt:        entry.CreatedAt.UTC(),
	}
	out.EnrollmentId = optionalUUID(entry.EnrollmentID)
	if entry.OfferExpiresAt != nil {
		at := entry.OfferExpiresAt.UTC()
		out.OfferExpiresAt = &at
	}
	if !entry.UpdatedAt.IsZero() {
		updated := entry.UpdatedAt.UTC()
		out.UpdatedAt = &updated
	}
	if entry.RowVersion > 0 {
		version := int(entry.RowVersion)
		out.RowVersion = &version
	}
	if in.Offer != nil {
		offer := bookingView(*in.Offer)
		out.Offer = &offer
	}
	return out
}
