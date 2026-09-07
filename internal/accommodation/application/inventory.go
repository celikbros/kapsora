package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// maxInventoryRange is how many nights one allotment call may cover. A season is a few
// hundred nights and a typo is a few hundred thousand; the bound is here so a mistyped
// year is a 422 rather than a statement that writes until the transaction times out.
const maxInventoryRange = 730

// maxCapacity is the largest allotment a single night may carry. No provider gives one
// payer a hundred thousand rooms of one type on one night, and a number that large is a
// finger on a key rather than a decision.
const maxCapacity = 10000

// InventoryRange is a range of nights as the API answers it: one entry per date of the
// range, in order, whether or not there is a row behind it.
type InventoryRange struct {
	RoomTypeID uuid.UUID
	From       time.Time
	To         time.Time
	Days       []InventoryDayView
}

// InventoryDayView is one night. `Allotted` is false for a night with no row at all, and
// it is the field that keeps "no allotment" from being read as "zero free": the two look
// identical in the counters and mean different things to a provider deciding what to open.
type InventoryDayView struct {
	StayDate   time.Time
	Allotted   bool
	Capacity   int
	Held       int
	Confirmed  int
	Available  int
	UpdatedAt  *time.Time
	RowVersion int64
}

// PutInventoryInput is one allotment: a range of nights and the capacity to open on each.
type PutInventoryInput struct {
	From     time.Time
	To       time.Time
	Capacity int
}

// GetRoomTypeInventory answers a range of nights with the three counters and the
// availability the database computed, so no screen subtracts.
func (s *Service) GetRoomTypeInventory(ctx context.Context, rc identity.RequestContext,
	roomTypeID uuid.UUID, from, to time.Time,
) (InventoryRange, error) {
	from, to = domain.Day(from), domain.Day(to)
	if err := validateInventoryRange(from, to); err != nil {
		return InventoryRange{}, err
	}

	var out InventoryRange
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetRoomType(ctx, tx, rc.TenantID, roomTypeID, scopeOf(rc)); err != nil {
			return err
		}
		rows, err := s.repo.GetInventoryRange(ctx, tx, rc.TenantID, roomTypeID, from, to)
		if err != nil {
			return err
		}
		out = inventoryRange(roomTypeID, from, to, rows)
		return nil
	})
	if err != nil {
		return InventoryRange{}, err
	}
	return out, nil
}

// PutRoomTypeInventory opens a season's allotment in one call.
//
// The whole range is one transaction and one statement. Before it runs, the nights that
// already exist are locked in stay_date order — the order WP-I6-02 takes a hold in, so the
// two cannot deadlock — and the first night whose commitment exceeds the new capacity is
// refused by name. A provider opening ninety nights needs to be told which night to look
// at, not that something somewhere failed.
//
// The check is not what makes this safe. `held + confirmed <= capacity` is a CHECK on the
// row: if this function forgot to look, or a hold landed between the lock and the write,
// the statement would fail and take the range with it. That is the correct outcome for an
// allotment that was meant to apply to a whole season, and it is the database's answer
// rather than this service's.
func (s *Service) PutRoomTypeInventory(ctx context.Context, rc identity.RequestContext,
	roomTypeID uuid.UUID, in PutInventoryInput,
) (InventoryRange, error) {
	from, to := domain.Day(in.From), domain.Day(in.To)
	if err := validatePutInventory(from, to, in.Capacity); err != nil {
		return InventoryRange{}, err
	}

	var out InventoryRange
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetRoomType(ctx, tx, rc.TenantID, roomTypeID, scopeOf(rc)); err != nil {
			return err
		}
		committed, err := s.repo.LockInventoryRange(ctx, tx, rc.TenantID, roomTypeID, from, to)
		if err != nil {
			return err
		}
		if offender := firstBelowCommitment(committed, in.Capacity); offender != nil {
			return offender
		}
		if _, err := s.repo.SetInventoryCapacity(ctx, tx, rc.TenantID, roomTypeID,
			from, to, in.Capacity); err != nil {
			return err
		}
		rows, err := s.repo.GetInventoryRange(ctx, tx, rc.TenantID, roomTypeID, from, to)
		if err != nil {
			return err
		}
		out = inventoryRange(roomTypeID, from, to, rows)
		return s.record(ctx, tx, rc, ActionInventoryPut, ResourceRoomType, roomTypeID,
			map[string]any{
				"from": from.Format(time.DateOnly), "to": to.Format(time.DateOnly),
				"capacity": in.Capacity, "nights": len(out.Days),
			})
	})
	if err != nil {
		return InventoryRange{}, err
	}
	return out, nil
}

// firstBelowCommitment finds the earliest night the new capacity would fall under.
//
// It takes the earliest rather than the first row it happens to see. The locked read does
// order by stay_date -- that ordering is what keeps opening a season and taking a hold from
// deadlocking each other -- but the answer a provider is given should not depend on it: the
// night they are told to look at has to be the first one that is wrong, not whichever the
// planner reached first.
func firstBelowCommitment(rows []InventoryCommitment, capacity int) *InventoryBelowCommitment {
	var offender *InventoryBelowCommitment
	for _, row := range rows {
		if row.Held+row.Confirmed <= capacity {
			continue
		}
		if offender != nil && !row.StayDate.Before(offender.StayDate) {
			continue
		}
		offender = &InventoryBelowCommitment{
			StayDate: row.StayDate, Capacity: capacity,
			Held: row.Held, Confirmed: row.Confirmed,
		}
	}
	return offender
}

// inventoryRange fills the gaps. Every date of the range gets an entry; a date with no row
// is `allotted: false` with zeroes, which is the only honest rendering of "this provider
// has opened nothing here" — and the reason the availability search treats a missing night
// as unavailable rather than as free.
func inventoryRange(roomTypeID uuid.UUID, from, to time.Time,
	rows []InventoryDayRecord,
) InventoryRange {
	byDate := make(map[string]InventoryDayRecord, len(rows))
	for _, row := range rows {
		byDate[domain.Day(row.StayDate).Format(time.DateOnly)] = row
	}
	out := InventoryRange{RoomTypeID: roomTypeID, From: from, To: to}
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		key := day.Format(time.DateOnly)
		row, found := byDate[key]
		if !found {
			out.Days = append(out.Days, InventoryDayView{StayDate: day})
			continue
		}
		updated := row.UpdatedAt
		out.Days = append(out.Days, InventoryDayView{
			StayDate: day, Allotted: true, Capacity: row.Capacity, Held: row.Held,
			Confirmed: row.Confirmed, Available: row.Available,
			UpdatedAt: &updated, RowVersion: row.RowVersion,
		})
	}
	return out
}

func validateInventoryRange(from, to time.Time) error {
	ve := &domain.ValidationError{}
	if from.IsZero() {
		ve.Add("from", "REQUIRED", "başlangıç tarihi zorunlu")
	}
	if to.IsZero() {
		ve.Add("to", "REQUIRED", "bitiş tarihi zorunlu")
	}
	if ve.Len() > 0 {
		return ve
	}
	if to.Before(from) {
		ve.Add("to", "RANGE", "başlangıç tarihinden önce olamaz")
		return ve
	}
	nights := int(to.Sub(from).Hours()/24) + 1
	if nights > maxInventoryRange {
		ve.Add("to", "RANGE", "bir çağrıda en fazla 730 gece ayarlanabilir")
	}
	return ve.OrNil()
}

func validatePutInventory(from, to time.Time, capacity int) error {
	if err := validateInventoryRange(from, to); err != nil {
		return err
	}
	ve := &domain.ValidationError{}
	if capacity < 0 || capacity > maxCapacity {
		ve.Add("capacity", "RANGE", "0-10000 arasında olmalı")
	}
	return ve.OrNil()
}
