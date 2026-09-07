// Package postgres also implements what happens to a booking after it is agreed
// (WP-I6-03): the cancellation, the check-in and check-out, the no-show and the waitlist.
//
// Two adapter-level rules live in this file and nowhere else.
//
// A status write reports whether it moved a row, and every caller reads that as the answer
// to "had somebody already done this". None of these methods turns a zero row count into an
// error, because a replayed command finding nothing to do is success and not failure.
//
// A constraint violation is translated into the application's own named error exactly where
// the constraint name is still visible: `uq_waitlist_live_entry` and `uq_no_show_booking`
// become sentences a member can act on rather than a SQLSTATE reaching a screen.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/accommodation/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// Constraint names this file turns into named errors.
const (
	constraintWaitlistLive = "uq_waitlist_live_entry"
	constraintNoShowExists = "uq_no_show_booking"
)

// ---------------------------------------------------------------------------
// Booking transitions and the confirmed-side counter
// ---------------------------------------------------------------------------

// AddConfirmed implements application.BookingRepository.
func (Bookings) AddConfirmed(ctx context.Context, tx pgx.Tx, tenantID, roomTypeID uuid.UUID,
	from, to time.Time, delta int,
) error {
	_, err := sqlcgen.New(tx).AddInventoryConfirmed(ctx, sqlcgen.AddInventoryConfirmedParams{
		TenantID: tenantID, RoomTypeID: roomTypeID, FromDate: dateOf(from), ToDate: dateOf(to),
		Delta: int32(delta), //nolint:gosec // a booking moves one room
	})
	if err != nil {
		return fmt.Errorf("accommodation: move confirmed: %w", err)
	}
	return nil
}

// CancelConfirmedBookingRow implements application.BookingRepository.
func (Bookings) CancelConfirmedBookingRow(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	cancelledAt time.Time, reasonCode string, actorID uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).CancelConfirmedBooking(ctx, sqlcgen.CancelConfirmedBookingParams{
		TenantID: tenantID, ID: id, CancelledAt: &cancelledAt,
		CancelReasonCode: &reasonCode, ActorID: actorUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("accommodation: cancel confirmed booking: %w", err)
	}
	return affected == 1, nil
}

// CheckInBookingRow implements application.BookingRepository.
func (Bookings) CheckInBookingRow(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	checkedInAt time.Time, actorID uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).CheckInBooking(ctx, sqlcgen.CheckInBookingParams{
		TenantID: tenantID, ID: id, CheckedInAt: &checkedInAt, ActorID: actorUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("accommodation: check in booking: %w", err)
	}
	return affected == 1, nil
}

// CheckOutBookingRow implements application.BookingRepository.
func (Bookings) CheckOutBookingRow(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	checkedOutAt time.Time, actualNights int, overBooking bool, actorID uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).CheckOutBooking(ctx, sqlcgen.CheckOutBookingParams{
		TenantID: tenantID, ID: id, CheckedOutAt: &checkedOutAt,
		ActualNights: int32(actualNights), //nolint:gosec // bounded by the tenant's max stay
		OverBooking:  overBooking, ActorID: actorUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("accommodation: check out booking: %w", err)
	}
	return affected == 1, nil
}

// MarkBookingNoShowRow implements application.BookingRepository.
func (Bookings) MarkBookingNoShowRow(ctx context.Context, tx pgx.Tx, tenantID, id,
	actorID uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).MarkBookingNoShow(ctx, sqlcgen.MarkBookingNoShowParams{
		TenantID: tenantID, ID: id, ActorID: actorUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("accommodation: mark booking no-show: %w", err)
	}
	return affected == 1, nil
}

// ---------------------------------------------------------------------------
// Cancellation
// ---------------------------------------------------------------------------

// CreateCancellation implements application.BookingRepository.
func (Bookings) CreateCancellation(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewCancellationRow,
) (application.CancellationRecord, error) {
	row, err := sqlcgen.New(tx).CreateCancellation(ctx, sqlcgen.CreateCancellationParams{
		TenantID: tenantID, BookingID: in.BookingID, CancelledAt: in.CancelledAt,
		CancelledBy: actorUUID(in.CancelledBy), ReasonCode: in.ReasonCode,
		PolicySnapshot: in.PolicySnapshot, Free: in.Free,
		PenaltyNights:  int32(in.PenaltyNights),  //nolint:gosec // bounded by the stay's length
		ReleasedNights: int32(in.ReleasedNights), //nolint:gosec // bounded by the stay's length
		FeeAmount:      in.FeeAmount, PayerFee: in.PayerFee, MemberFee: in.MemberFee,
		CurrencyCode: in.CurrencyCode,
	})
	if err != nil {
		return application.CancellationRecord{}, fmt.Errorf("accommodation: create cancellation: %w", err)
	}
	return application.CancellationRecord{
		ID: row.ID, BookingID: row.BookingID, CancelledAt: row.CancelledAt,
		CancelledBy: uuidPtr(row.CancelledBy), ReasonCode: row.ReasonCode,
		PolicySnapshot: row.PolicySnapshot, Free: row.Free,
		PenaltyNights: int(row.PenaltyNights), ReleasedNights: int(row.ReleasedNights),
		FeeAmount: row.FeeAmount, PayerFee: row.PayerFee, MemberFee: row.MemberFee,
		CurrencyCode: row.CurrencyCode, CreatedAt: row.CreatedAt,
	}, nil
}

// GetCancellation implements application.BookingRepository.
func (Bookings) GetCancellation(ctx context.Context, tx pgx.Tx, tenantID, bookingID uuid.UUID) (
	application.CancellationRecord, error,
) {
	row, err := sqlcgen.New(tx).GetCancellation(ctx, sqlcgen.GetCancellationParams{
		TenantID: tenantID, BookingID: bookingID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.CancellationRecord{}, application.ErrBookingNotFound
	}
	if err != nil {
		return application.CancellationRecord{}, fmt.Errorf("accommodation: read cancellation: %w", err)
	}
	return application.CancellationRecord{
		ID: row.ID, BookingID: row.BookingID, CancelledAt: row.CancelledAt,
		CancelledBy: uuidPtr(row.CancelledBy), ReasonCode: row.ReasonCode,
		PolicySnapshot: row.PolicySnapshot, Free: row.Free,
		PenaltyNights: int(row.PenaltyNights), ReleasedNights: int(row.ReleasedNights),
		FeeAmount: row.FeeAmount, PayerFee: row.PayerFee, MemberFee: row.MemberFee,
		CurrencyCode: row.CurrencyCode, CreatedAt: row.CreatedAt,
	}, nil
}

// ---------------------------------------------------------------------------
// No-show
// ---------------------------------------------------------------------------

// CountCleanBookingDocuments implements application.BookingRepository.
func (Bookings) CountCleanBookingDocuments(ctx context.Context, tx pgx.Tx, tenantID,
	bookingID uuid.UUID, objectID *uuid.UUID,
) (int, error) {
	n, err := sqlcgen.New(tx).CountCleanBookingDocuments(ctx, sqlcgen.CountCleanBookingDocumentsParams{
		TenantID: tenantID, BookingID: bookingID, ObjectID: nullUUID(objectID),
	})
	if err != nil {
		return 0, fmt.Errorf("accommodation: count booking documents: %w", err)
	}
	return int(n), nil
}

// CreateNoShow implements application.BookingRepository.
func (Bookings) CreateNoShow(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewNoShowRow,
) (application.NoShowRecord, error) {
	row, err := sqlcgen.New(tx).CreateNoShow(ctx, sqlcgen.CreateNoShowParams{
		TenantID: tenantID, BookingID: in.BookingID,
		ReportedByActorID: actorUUID(in.ReportedByActorID), ReportedAt: in.ReportedAt,
		EvidenceDocumentID: nullUUID(in.EvidenceDocumentID),
		AssessedFeeAmount:  in.AssessedFeeAmount, PayerAmount: in.PayerAmount,
		MemberAmount: in.MemberAmount, CurrencyCode: in.CurrencyCode,
		ActorID: actorUUID(in.ActorID),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation &&
			pgErr.ConstraintName == constraintNoShowExists {
			return application.NoShowRecord{}, application.ErrNoShowAlreadyReported
		}
		return application.NoShowRecord{}, fmt.Errorf("accommodation: create no-show: %w", err)
	}
	return noShowOf(noShowColumns(row)), nil
}

// GetNoShow implements application.BookingRepository.
func (Bookings) GetNoShow(ctx context.Context, tx pgx.Tx, tenantID, bookingID uuid.UUID) (
	application.NoShowRecord, error,
) {
	row, err := sqlcgen.New(tx).GetNoShow(ctx, sqlcgen.GetNoShowParams{
		TenantID: tenantID, BookingID: bookingID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.NoShowRecord{}, application.ErrNoShowNotFound
	}
	if err != nil {
		return application.NoShowRecord{}, fmt.Errorf("accommodation: read no-show: %w", err)
	}
	return noShowOf(noShowColumns(row)), nil
}

// LockNoShow implements application.BookingRepository.
func (Bookings) LockNoShow(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (
	application.NoShowRecord, error,
) {
	row, err := sqlcgen.New(tx).LockNoShow(ctx, sqlcgen.LockNoShowParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.NoShowRecord{}, application.ErrNoShowNotFound
	}
	if err != nil {
		return application.NoShowRecord{}, fmt.Errorf("accommodation: lock no-show: %w", err)
	}
	return noShowOf(noShowColumns(row)), nil
}

// ReviewNoShowRow implements application.BookingRepository.
//
// `ck_no_show_reviewer_differs` is the database's half of the maker-checker rule. The
// service refuses the same actor with a sentence a person can read; if that check were ever
// removed, this violation is what would still refuse the write, so it is translated rather
// than wrapped.
func (Bookings) ReviewNoShowRow(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NoShowReviewRow,
) (bool, error) {
	affected, err := sqlcgen.New(tx).ReviewNoShow(ctx, sqlcgen.ReviewNoShowParams{
		TenantID: tenantID, ID: in.ID, Status: in.Status,
		ReviewedBy: uuid.NullUUID{UUID: in.ReviewedBy, Valid: in.ReviewedBy != uuid.Nil},
		ReviewedAt: &in.ReviewedAt, ReviewComment: in.ReviewComment,
		ConsumedNights: int32(in.ConsumedNights), //nolint:gosec // bounded by the stay's length
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.ConstraintName == "ck_no_show_reviewer_differs" {
			return false, application.ErrNoShowSameActor
		}
		return false, fmt.Errorf("accommodation: review no-show: %w", err)
	}
	return affected == 1, nil
}

// noShowColumns is the one shape every no-show read comes back as, so the three generated
// row types convert into it directly and one mapper turns it into the application record.
type noShowColumns struct {
	ID                 uuid.UUID
	BookingID          uuid.UUID
	ReportedByActorID  uuid.NullUUID
	ReportedAt         time.Time
	EvidenceDocumentID uuid.NullUUID
	AssessedFeeAmount  string
	PayerAmount        string
	MemberAmount       string
	CurrencyCode       string
	Status             string
	ReviewedBy         uuid.NullUUID
	ReviewedAt         *time.Time
	ReviewComment      *string
	ConsumedNights     int32
	CreatedAt          time.Time
	UpdatedAt          time.Time
	RowVersion         int64
}

func noShowOf(c noShowColumns) application.NoShowRecord {
	return application.NoShowRecord{
		ID: c.ID, BookingID: c.BookingID, ReportedByActorID: uuidPtr(c.ReportedByActorID),
		ReportedAt: c.ReportedAt, EvidenceDocumentID: uuidPtr(c.EvidenceDocumentID),
		AssessedFeeAmount: c.AssessedFeeAmount, PayerAmount: c.PayerAmount,
		MemberAmount: c.MemberAmount, CurrencyCode: c.CurrencyCode, Status: c.Status,
		ReviewedBy: uuidPtr(c.ReviewedBy), ReviewedAt: c.ReviewedAt,
		ReviewComment: c.ReviewComment, ConsumedNights: int(c.ConsumedNights),
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, RowVersion: c.RowVersion,
	}
}

// ---------------------------------------------------------------------------
// Waitlist
// ---------------------------------------------------------------------------

// CreateWaitlistEntry implements application.BookingRepository.
func (Bookings) CreateWaitlistEntry(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewWaitlistRow,
) (application.WaitlistRecord, error) {
	row, err := sqlcgen.New(tx).CreateWaitlistEntry(ctx, sqlcgen.CreateWaitlistEntryParams{
		TenantID: tenantID, PersonID: in.PersonID, EnrollmentID: in.EnrollmentID,
		PropertyID: in.PropertyID, RoomTypeID: nullUUID(in.RoomTypeID),
		CheckIn: dateOf(in.CheckIn), CheckOut: dateOf(in.CheckOut),
		Adults: int32(in.Adults), Children: int32(in.Children), //nolint:gosec // bounded by validation
		Priority: int32(in.Priority), CreatedAt: in.CreatedAt, //nolint:gosec // bounded by validation
		ActorID: actorUUID(in.ActorID),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation &&
			pgErr.ConstraintName == constraintWaitlistLive {
			return application.WaitlistRecord{}, application.ErrWaitlistAlreadyWaiting
		}
		return application.WaitlistRecord{}, fmt.Errorf("accommodation: create waitlist entry: %w", err)
	}
	return waitlistOf(waitlistColumns(row)), nil
}

// GetWaitlistEntry implements application.BookingRepository.
func (Bookings) GetWaitlistEntry(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	personID *uuid.UUID, scopeIDs []uuid.UUID,
) (application.WaitlistRecord, error) {
	row, err := sqlcgen.New(tx).GetWaitlistEntry(ctx, sqlcgen.GetWaitlistEntryParams{
		TenantID: tenantID, ID: id, PersonID: nullUUID(personID), ScopeIds: scopeIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.WaitlistRecord{}, application.ErrWaitlistEntryNotFound
	}
	if err != nil {
		return application.WaitlistRecord{}, fmt.Errorf("accommodation: read waitlist entry: %w", err)
	}
	return waitlistOf(waitlistColumns(row)), nil
}

// LockWaitlistEntry implements application.BookingRepository.
func (Bookings) LockWaitlistEntry(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (
	application.WaitlistRecord, error,
) {
	row, err := sqlcgen.New(tx).LockWaitlistEntry(ctx, sqlcgen.LockWaitlistEntryParams{
		TenantID: tenantID, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.WaitlistRecord{}, application.ErrWaitlistEntryNotFound
	}
	if err != nil {
		return application.WaitlistRecord{}, fmt.Errorf("accommodation: lock waitlist entry: %w", err)
	}
	return waitlistOf(waitlistColumns(row)), nil
}

// ListWaitlistEntries implements application.BookingRepository.
func (Bookings) ListWaitlistEntries(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.WaitlistQuery,
) ([]application.WaitlistRecord, error) {
	rows, err := sqlcgen.New(tx).ListWaitlistEntries(ctx, sqlcgen.ListWaitlistEntriesParams{
		TenantID: tenantID, PersonID: nullUUID(q.PersonID), PropertyID: nullUUID(q.PropertyID),
		Status: optionalString(q.Status), ScopeIds: q.ScopeIDs,
		PageSize: int32(q.PageSize), //nolint:gosec // clamped by the service
	})
	if err != nil {
		return nil, fmt.Errorf("accommodation: list waitlist entries: %w", err)
	}
	out := make([]application.WaitlistRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, waitlistOf(waitlistColumns(row)))
	}
	return out, nil
}

// ListWaitlistQueue implements application.BookingRepository. The ordering is the query's and
// is part of the contract; see the package comment of accommodation_cancellation.sql.
func (Bookings) ListWaitlistQueue(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, limit int) (
	[]application.WaitlistRecord, error,
) {
	rows, err := sqlcgen.New(tx).ListWaitlistQueue(ctx, sqlcgen.ListWaitlistQueueParams{
		TenantID: tenantID,
		PageSize: int32(limit), //nolint:gosec // a constant batch size
	})
	if err != nil {
		return nil, fmt.Errorf("accommodation: read waitlist queue: %w", err)
	}
	out := make([]application.WaitlistRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, waitlistOf(waitlistColumns(row)))
	}
	return out, nil
}

// ListExpiredWaitlistOffers implements application.BookingRepository.
func (Bookings) ListExpiredWaitlistOffers(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	before time.Time, limit int,
) ([]uuid.UUID, error) {
	ids, err := sqlcgen.New(tx).ListExpiredWaitlistOffers(ctx, sqlcgen.ListExpiredWaitlistOffersParams{
		TenantID: tenantID, Before: &before,
		PageSize: int32(limit), //nolint:gosec // a constant batch size
	})
	if err != nil {
		return nil, fmt.Errorf("accommodation: list expired waitlist offers: %w", err)
	}
	return ids, nil
}

// OfferWaitlistEntry implements application.BookingRepository.
func (Bookings) OfferWaitlistEntry(ctx context.Context, tx pgx.Tx, tenantID, id,
	bookingID uuid.UUID, expiresAt time.Time,
) (bool, error) {
	affected, err := sqlcgen.New(tx).OfferWaitlistEntry(ctx, sqlcgen.OfferWaitlistEntryParams{
		TenantID: tenantID, ID: id,
		OfferedBookingID: uuid.NullUUID{UUID: bookingID, Valid: true},
		OfferExpiresAt:   &expiresAt,
	})
	if err != nil {
		return false, fmt.Errorf("accommodation: offer waitlist entry: %w", err)
	}
	return affected == 1, nil
}

// ReturnWaitlistEntryToQueue implements application.BookingRepository.
func (Bookings) ReturnWaitlistEntryToQueue(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	requeuedAt time.Time,
) (bool, error) {
	affected, err := sqlcgen.New(tx).ReturnWaitlistEntryToQueue(ctx,
		sqlcgen.ReturnWaitlistEntryToQueueParams{
			TenantID: tenantID, ID: id, RequeuedAt: requeuedAt,
		})
	if err != nil {
		return false, fmt.Errorf("accommodation: requeue waitlist entry: %w", err)
	}
	return affected == 1, nil
}

// AcceptWaitlistEntryRow implements application.BookingRepository.
func (Bookings) AcceptWaitlistEntryRow(ctx context.Context, tx pgx.Tx, tenantID, id,
	actorID uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).AcceptWaitlistEntry(ctx, sqlcgen.AcceptWaitlistEntryParams{
		TenantID: tenantID, ID: id, ActorID: actorUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("accommodation: accept waitlist entry: %w", err)
	}
	return affected == 1, nil
}

// CancelWaitlistEntryRow implements application.BookingRepository.
func (Bookings) CancelWaitlistEntryRow(ctx context.Context, tx pgx.Tx, tenantID, id,
	actorID uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).CancelWaitlistEntry(ctx, sqlcgen.CancelWaitlistEntryParams{
		TenantID: tenantID, ID: id, ActorID: actorUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("accommodation: cancel waitlist entry: %w", err)
	}
	return affected == 1, nil
}

// ListPropertyRoomTypeIDs implements application.BookingRepository.
func (Bookings) ListPropertyRoomTypeIDs(ctx context.Context, tx pgx.Tx, tenantID,
	propertyID uuid.UUID,
) ([]uuid.UUID, error) {
	ids, err := sqlcgen.New(tx).ListPropertyRoomTypeIDs(ctx, sqlcgen.ListPropertyRoomTypeIDsParams{
		TenantID: tenantID, PropertyID: propertyID,
	})
	if err != nil {
		return nil, fmt.Errorf("accommodation: list property room types: %w", err)
	}
	return ids, nil
}

// waitlistColumns is the one shape every waitlist read comes back as.
type waitlistColumns struct {
	ID               uuid.UUID
	PersonID         uuid.UUID
	EnrollmentID     uuid.UUID
	PropertyID       uuid.UUID
	RoomTypeID       uuid.NullUUID
	CheckIn          pgtype.Date
	CheckOut         pgtype.Date
	Adults           int32
	Children         int32
	Priority         int32
	Status           string
	OfferedBookingID uuid.NullUUID
	OfferExpiresAt   *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
	RowVersion       int64
}

func waitlistOf(c waitlistColumns) application.WaitlistRecord {
	return application.WaitlistRecord{
		ID: c.ID, PersonID: c.PersonID, EnrollmentID: c.EnrollmentID,
		PropertyID: c.PropertyID, RoomTypeID: uuidPtr(c.RoomTypeID),
		CheckIn: dateTime(c.CheckIn), CheckOut: dateTime(c.CheckOut),
		Adults: int(c.Adults), Children: int(c.Children), Priority: int(c.Priority),
		Status: c.Status, OfferedBookingID: uuidPtr(c.OfferedBookingID),
		OfferExpiresAt: c.OfferExpiresAt, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		RowVersion: c.RowVersion,
	}
}
