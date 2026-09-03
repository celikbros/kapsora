package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// entryRow is the union of the three generated ledger row shapes, so one mapper serves
// GetEntitlementLedgerEntry, GetEntitlementLedgerEntryByKey and the paged list.
type entryRow struct {
	ID             uuid.UUID
	AccountID      uuid.UUID
	MovementType   string
	EffectiveAt    time.Time
	DeltaTotal     string
	DeltaAvailable string
	DeltaReserved  string
	DeltaConsumed  string
	DeltaExpired   string
	ReferenceType  string
	ReferenceID    uuid.UUID
	Key            string
	ReasonCode     *string
	ReasonText     *string
	ReservationID  uuid.NullUUID
	CreatedBy      uuid.NullUUID
	CreatedAt      time.Time
}

func entryFromRow(r entryRow) (Entry, error) {
	deltas, err := parseBalances(r.DeltaTotal, r.DeltaAvailable, r.DeltaReserved, r.DeltaConsumed, r.DeltaExpired)
	if err != nil {
		return Entry{}, err
	}
	return Entry{
		ID: r.ID, AccountID: r.AccountID, MovementType: r.MovementType, EffectiveAt: r.EffectiveAt,
		Deltas: deltas, ReferenceType: r.ReferenceType, ReferenceID: r.ReferenceID, Key: r.Key,
		ReasonCode: r.ReasonCode, ReasonText: r.ReasonText,
		ReservationID: uuidPtr(r.ReservationID), CreatedBy: uuidPtr(r.CreatedBy), CreatedAt: r.CreatedAt,
	}, nil
}

// reservationRow is the shape shared by the four reservation reads.
type reservationRow struct {
	ID            uuid.UUID
	AccountID     uuid.UUID
	ReferenceType string
	ReferenceID   uuid.UUID
	Quantity      string
	Consumed      string
	Released      string
	Status        string
	ExpiresAt     *time.Time
	Key           string
	CreatedAt     time.Time
	RowVersion    int64
}

func reservationFromRow(r reservationRow) (Reservation, error) {
	quantity, err := domain.ParseQuantity(r.Quantity)
	if err != nil {
		return Reservation{}, fmt.Errorf("benefit: reservation %s quantity: %w", r.ID, err)
	}
	consumed, err := domain.ParseQuantity(r.Consumed)
	if err != nil {
		return Reservation{}, fmt.Errorf("benefit: reservation %s consumed: %w", r.ID, err)
	}
	released, err := domain.ParseQuantity(r.Released)
	if err != nil {
		return Reservation{}, fmt.Errorf("benefit: reservation %s released: %w", r.ID, err)
	}
	return Reservation{
		ID: r.ID, AccountID: r.AccountID, ReferenceType: r.ReferenceType, ReferenceID: r.ReferenceID,
		Quantity: quantity, Consumed: consumed, Released: released, Status: r.Status,
		ExpiresAt: r.ExpiresAt, Key: r.Key, CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}, nil
}

// reservationByID reads one hold; forUpdate takes the row lock the settle path needs.
func reservationByID(ctx context.Context, tx pgx.Tx, tenantID, reservationID uuid.UUID, forUpdate bool) (Reservation, error) {
	q := sqlcgen.New(tx)
	var row reservationRow
	if forUpdate {
		r, err := q.GetEntitlementReservationForUpdate(ctx, sqlcgen.GetEntitlementReservationForUpdateParams{
			TenantID: tenantID, ID: reservationID,
		})
		if err != nil {
			return Reservation{}, reservationReadError(err)
		}
		row = reservationRow{
			ID: r.ID, AccountID: r.EntitlementAccountID, ReferenceType: r.ReferenceType, ReferenceID: r.ReferenceID,
			Quantity: r.Quantity, Consumed: r.ConsumedQuantity, Released: r.ReleasedQuantity, Status: r.Status,
			ExpiresAt: r.ExpiresAt, Key: r.IdempotencyKey, CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
		}
	} else {
		r, err := q.GetEntitlementReservation(ctx, sqlcgen.GetEntitlementReservationParams{
			TenantID: tenantID, ID: reservationID,
		})
		if err != nil {
			return Reservation{}, reservationReadError(err)
		}
		row = reservationRow{
			ID: r.ID, AccountID: r.EntitlementAccountID, ReferenceType: r.ReferenceType, ReferenceID: r.ReferenceID,
			Quantity: r.Quantity, Consumed: r.ConsumedQuantity, Released: r.ReleasedQuantity, Status: r.Status,
			ExpiresAt: r.ExpiresAt, Key: r.IdempotencyKey, CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
		}
	}
	return reservationFromRow(row)
}

// reservationByKey answers the idempotency lookup of Reserve; pgx.ErrNoRows means "new".
func reservationByKey(ctx context.Context, tx pgx.Tx, tenantID, accountID uuid.UUID, key string) (Reservation, error) {
	r, err := sqlcgen.New(tx).GetEntitlementReservationByKey(ctx, sqlcgen.GetEntitlementReservationByKeyParams{
		TenantID: tenantID, EntitlementAccountID: accountID, IdempotencyKey: key,
	})
	if err != nil {
		return Reservation{}, err
	}
	return reservationFromRow(reservationRow{
		ID: r.ID, AccountID: r.EntitlementAccountID, ReferenceType: r.ReferenceType, ReferenceID: r.ReferenceID,
		Quantity: r.Quantity, Consumed: r.ConsumedQuantity, Released: r.ReleasedQuantity, Status: r.Status,
		ExpiresAt: r.ExpiresAt, Key: r.IdempotencyKey, CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	})
}

func reservationReadError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrReservationNotFound
	}
	return fmt.Errorf("benefit: read reservation: %w", err)
}

// parseBalances turns the five numeric(20,6) columns into exact decimals.
func parseBalances(total, available, reserved, consumed, expired string) (Balances, error) {
	out := Balances{}
	for _, f := range []struct {
		name string
		raw  string
		dst  *domain.Quantity
	}{
		{"total", total, &out.Total},
		{"available", available, &out.Available},
		{"reserved", reserved, &out.Reserved},
		{"consumed", consumed, &out.Consumed},
		{"expired", expired, &out.Expired},
	} {
		q, err := domain.ParseQuantity(f.raw)
		if err != nil {
			return Balances{}, fmt.Errorf("benefit: %s balance %q: %w", f.name, f.raw, err)
		}
		*f.dst = q
	}
	return out, nil
}

func dateValue(d pgtype.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	return d.Time
}

func datePtr(d pgtype.Date) *time.Time {
	if !d.Valid || d.InfinityModifier != pgtype.Finite {
		return nil
	}
	t := d.Time
	return &t
}

func date(t *time.Time) pgtype.Date {
	if t == nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: domain.DateOnly(*t), Valid: true}
}

func dateOf(t time.Time) pgtype.Date {
	return pgtype.Date{Time: domain.DateOnly(t), Valid: true}
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

func optUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

func uuidPtr(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	id := n.UUID
	return &id
}
