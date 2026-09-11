package main

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	accommodationapp "github.com/celikbros/kapsora/internal/accommodation/application"
	accommodationdomain "github.com/celikbros/kapsora/internal/accommodation/domain"
	"github.com/celikbros/kapsora/internal/identity"
	reportapp "github.com/celikbros/kapsora/internal/report/application"
	reportdomain "github.com/celikbros/kapsora/internal/report/domain"
)

// The two halves nothing else in the demo world produces: the hotel a member can actually book
// a room in, and the reconciliation runs the dashboard and the mutabakat screen read.

// inventoryHorizon is how far out the hotel's allotment is opened. The guide's fifth scenario
// asks the member to search "a month from now for three nights", so the season has to reach
// comfortably past that: four months means the demo still works in the week somebody finally
// gets round to running it.
const inventoryHorizon = 120

// demoRoomCapacity is how many rooms of each type are on sale each night. Five is enough that a
// demo booking never sells the last one and small enough that "sold out" is reachable by hand.
const demoRoomCapacity = 5

// ensureDemoAccommodation opens the hotel, its two room types and an allotment that reaches four
// months out, so scenario 5's search finds real availability priced from the hotel's own
// published contract.
func (s *seeder) ensureDemoAccommodation(ctx context.Context, sc *scenario) error {
	rc := rcOrganization(sc.tenant, sc.reservation, sc.hotelOrg,
		accommodationapp.PermissionRead, accommodationapp.PermissionManage)

	property, err := s.ensureProperty(ctx, rc, sc)
	if err != nil {
		return err
	}
	rooms := []struct {
		code, name              string
		adults, children, total int
	}{
		{"STD", "Standart oda", 2, 1, 3},
		{"SUITE", "Aile suiti", 2, 2, 4},
	}
	from := day(time.Now())
	to := from.AddDate(0, 0, inventoryHorizon)
	for _, r := range rooms {
		roomType, err := s.ensureRoomType(ctx, rc, sc, property, r.code, r.name,
			r.adults, r.children, r.total)
		if err != nil {
			return err
		}
		// The allotment is rewritten on every run rather than skipped when it is there: the
		// horizon is relative to today, so a demo database seeded in March and shown in June
		// would otherwise have run out of nights to sell. Rewriting is safe — the command
		// refuses any night whose capacity would fall under what is already held or confirmed.
		if _, err := s.biz.accommodation.PutRoomTypeInventory(ctx, rc, roomType,
			accommodationapp.PutInventoryInput{From: from, To: to, Capacity: demoRoomCapacity}); err != nil {
			return fmt.Errorf("open the allotment of %s: %w", r.code, err)
		}
	}
	step("hotel", "allotment", fmt.Sprintf("%d nights open from %s",
		inventoryHorizon+1, from.Format(time.DateOnly)))
	return nil
}

func (s *seeder) ensureProperty(ctx context.Context, rc identity.RequestContext, sc *scenario,
) (uuid.UUID, error) {
	provider := sc.hotelOrg
	page, err := s.biz.accommodation.ListProperties(ctx, rc, accommodationapp.PropertyFilter{
		ProviderOrganizationID: &provider, Limit: 50,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("list properties: %w", err)
	}
	for _, item := range page.Items {
		if item.Code == "DEMO_OTEL" {
			return item.ID, nil
		}
	}
	city, region := "Antalya", "TR-07"
	record, err := s.biz.accommodation.CreateProperty(ctx, rc, accommodationapp.NewPropertyInput{
		ProviderOrganizationID: sc.hotelOrg, Code: "DEMO_OTEL", Name: "Demo Sahil Oteli",
		PropertyType: accommodationdomain.PropertyHotel, Timezone: "Europe/Istanbul",
		City: &city, RegionCode: &region, Status: accommodationdomain.StatusActive,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("open the demo hotel: %w", err)
	}
	step("hotel", "DEMO_OTEL", "created")
	return record.ID, nil
}

func (s *seeder) ensureRoomType(ctx context.Context, rc identity.RequestContext, sc *scenario,
	propertyID uuid.UUID, code, name string, adults, children, occupancy int,
) (uuid.UUID, error) {
	rooms, err := s.biz.accommodation.ListRoomTypes(ctx, rc, propertyID, "")
	if err != nil {
		return uuid.Nil, fmt.Errorf("list room types: %w", err)
	}
	for _, item := range rooms {
		if item.Code == code {
			return item.ID, nil
		}
	}
	record, err := s.biz.accommodation.CreateRoomType(ctx, rc, accommodationapp.NewRoomTypeInput{
		PropertyID: propertyID, Code: code, Name: name,
		MaxAdults: adults, MaxChildren: children, MaxOccupancy: occupancy,
		ServiceDefinitionID: sc.services[serviceLodgingNight],
		Status:              accommodationdomain.StatusActive,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("create room type %s: %w", code, err)
	}
	return record.ID, nil
}

// ensureDemoReconciliation runs the daily comparison for the two days the settlements above fall
// due on: the day the paid one was due, which balances, and today, on which one settlement is
// still unpaid and therefore a difference.
//
// It is the scheduler's own entry point — `Reconcile` is what `scheduler.BillingReconcile` calls
// — given the work item port the scheduler gives it, so a differing run raises its item into the
// finance queue exactly as the nightly job would. Nothing about the runs is written here: the
// figures are PostgreSQL's, summed over the settlements the steps above produced.
func (s *seeder) ensureDemoReconciliation(ctx context.Context, sc *scenario) error {
	rc := rcTenant(sc.tenant, sc.reviewer, reportapp.PermissionRead)
	existing, err := s.biz.reports.ListReconciliationRuns(ctx, rc, reportapp.RunFilter{Limit: 50})
	if err != nil {
		return fmt.Errorf("list reconciliation runs: %w", err)
	}
	if len(existing.Items) > 0 {
		step("report", "reconciliation", fmt.Sprintf("%d runs already there", len(existing.Items)))
		return nil
	}

	today := day(time.Now())
	// The days the two settlements fall due on. They are worked out the way the settlements
	// were dated — the contract's thirty days applied to the day each icmal was decided — so a
	// change to either figure moves both together.
	days := []time.Time{today.AddDate(0, 0, -7), today}
	s.clock.at = time.Now().UTC()
	for _, when := range days {
		if _, err := s.biz.reports.Reconcile(ctx, when); err != nil {
			return fmt.Errorf("reconcile %s: %w", when.Format(time.DateOnly), err)
		}
	}

	runs, err := s.biz.reports.ListReconciliationRuns(ctx, rc, reportapp.RunFilter{Limit: 50})
	if err != nil {
		return fmt.Errorf("re-read the reconciliation runs: %w", err)
	}
	balanced, differing := 0, 0
	for _, run := range runs.Items {
		switch run.Status {
		case reportdomain.RunBalanced:
			balanced++
		case reportdomain.RunDifferences:
			differing++
		}
	}
	step("report", "reconciliation", fmt.Sprintf("%d runs: %d balanced, %d with differences",
		len(runs.Items), balanced, differing))
	return nil
}
