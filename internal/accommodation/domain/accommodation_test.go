package domain

import (
	"testing"
	"time"
)

func day(text string) time.Time {
	t, err := time.Parse(time.DateOnly, text)
	if err != nil {
		panic(err)
	}
	return t
}

// TestNightsCountsCalendarDays walks the boundaries the work package names: one night, a
// month end, a year end, a leap day, and the two refusals.
//
// It is a table rather than a handful of asserts because the mistake this function exists
// to prevent is not a wrong answer in the general case; it is a wrong answer on exactly the
// days somebody would never think to check.
func TestNightsCountsCalendarDays(t *testing.T) {
	cases := []struct {
		name           string
		checkIn        string
		checkOut       string
		want           int
		wantRefused    bool
		wantFirstNight string
		wantLastNight  string
	}{
		{name: "one night", checkIn: "2026-06-15", checkOut: "2026-06-16", want: 1,
			wantFirstNight: "2026-06-15", wantLastNight: "2026-06-15"},
		{name: "across a month end", checkIn: "2026-01-30", checkOut: "2026-02-02", want: 3,
			wantFirstNight: "2026-01-30", wantLastNight: "2026-02-01"},
		{name: "across a 31 day month end", checkIn: "2026-03-31", checkOut: "2026-04-01", want: 1,
			wantFirstNight: "2026-03-31", wantLastNight: "2026-03-31"},
		{name: "across a year end", checkIn: "2026-12-30", checkOut: "2027-01-02", want: 3,
			wantFirstNight: "2026-12-30", wantLastNight: "2027-01-01"},
		{name: "across a leap day", checkIn: "2028-02-28", checkOut: "2028-03-01", want: 2,
			wantFirstNight: "2028-02-28", wantLastNight: "2028-02-29"},
		{name: "thirty nights", checkIn: "2026-06-01", checkOut: "2026-07-01", want: 30,
			wantFirstNight: "2026-06-01", wantLastNight: "2026-06-30"},
		{name: "same day is refused", checkIn: "2026-06-15", checkOut: "2026-06-15", wantRefused: true},
		{name: "backwards is refused", checkIn: "2026-06-16", checkOut: "2026-06-15", wantRefused: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, out := day(tc.checkIn), day(tc.checkOut)
			nights, err := Nights(in, out)
			if tc.wantRefused {
				if err == nil {
					t.Fatalf("Nights(%s, %s) = %d, want a refusal", tc.checkIn, tc.checkOut, nights)
				}
				return
			}
			if err != nil {
				t.Fatalf("Nights(%s, %s): %v", tc.checkIn, tc.checkOut, err)
			}
			if nights != tc.want {
				t.Fatalf("Nights(%s, %s) = %d, want %d", tc.checkIn, tc.checkOut, nights, tc.want)
			}

			// The stay dates are the nights somebody slept, and the last of them is the
			// day before check-out. An off-by-one here would take an inventory row for a
			// night the guest was already gone.
			dates := StayDates(in, nights)
			if len(dates) != nights {
				t.Fatalf("StayDates gave %d dates for %d nights", len(dates), nights)
			}
			if got := dates[0].Format(time.DateOnly); got != tc.wantFirstNight {
				t.Errorf("first night = %s, want %s", got, tc.wantFirstNight)
			}
			if got := dates[len(dates)-1].Format(time.DateOnly); got != tc.wantLastNight {
				t.Errorf("last night = %s, want %s", got, tc.wantLastNight)
			}
			if got := LastNight(out).Format(time.DateOnly); got != tc.wantLastNight {
				t.Errorf("LastNight = %s, want %s", got, tc.wantLastNight)
			}
			// Every date is distinct and consecutive: a duplicate would double-count an
			// allotment and a gap would skip one.
			for i := 1; i < len(dates); i++ {
				if !dates[i].Equal(dates[i-1].AddDate(0, 0, 1)) {
					t.Fatalf("stay dates are not consecutive at %d: %s after %s",
						i, dates[i], dates[i-1])
				}
			}
		})
	}
}

// TestNightsSurvivesADaylightSavingChange is the test the whole civil-date design exists
// for.
//
// Europe/Berlin loses an hour on the last Sunday of March and gains one on the last Sunday
// of October, so a stay over either boundary is 23 or 25 hours long per night in wall-clock
// terms. An implementation that divided an elapsed duration by twenty-four would answer one
// night short in spring and one night long in autumn — and a stay one night short is a room
// the guest has nowhere to sleep in.
//
// Turkey has kept a single offset since 2016, so the bug would never have shown in a
// Turkish property. It shows the moment a payer books a room abroad.
func TestNightsSurvivesADaylightSavingChange(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("Europe/Berlin is not in this system's zone database: %v", err)
	}

	cases := []struct {
		name     string
		checkIn  string
		checkOut string
		nights   int
		// shortNight is the civil date whose wall-clock length is not 24 hours.
		shortNight string
		wantHours  float64
	}{
		// 2026-03-29: 02:00 becomes 03:00, so that night is 23 hours long.
		{name: "spring forward", checkIn: "2026-03-28", checkOut: "2026-03-31", nights: 3,
			shortNight: "2026-03-29", wantHours: 23},
		// 2026-10-25: 03:00 becomes 02:00, so that night is 25 hours long.
		{name: "fall back", checkIn: "2026-10-24", checkOut: "2026-10-27", nights: 3,
			shortNight: "2026-10-25", wantHours: 25},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, out := day(tc.checkIn), day(tc.checkOut)
			nights, err := Nights(in, out)
			if err != nil {
				t.Fatalf("Nights: %v", err)
			}
			if nights != tc.nights {
				t.Fatalf("Nights over the %s boundary = %d, want %d", tc.name, nights, tc.nights)
			}

			// And the boundary really is one: the night named below is not 24 hours long in
			// Berlin, so a duration-based count would have been wrong here and this test
			// would be asserting nothing if the zone database disagreed.
			short := day(tc.shortNight)
			start := NightStart(short, berlin)
			next := NightStart(short.AddDate(0, 0, 1), berlin)
			if hours := next.Sub(start).Hours(); hours != tc.wantHours {
				t.Fatalf("the night of %s is %.0f hours in Berlin, want %.0f — the fixture no "+
					"longer straddles a clock change and proves nothing",
					tc.shortNight, hours, tc.wantHours)
			}

			// The stay covers each civil night exactly once, the boundary night included.
			dates := StayDates(in, nights)
			seen := map[string]int{}
			for _, d := range dates {
				seen[d.Format(time.DateOnly)]++
			}
			if seen[tc.shortNight] != 1 {
				t.Errorf("the night of %s appears %d times in the stay, want once",
					tc.shortNight, seen[tc.shortNight])
			}
		})
	}
}

// TestDayInReadsThePropertysCalendar pins the other half of the zone rule: which civil date
// an instant belongs to is decided by the property's own zone and by nothing else. The same
// instant is two different nights in two hotels three time zones apart, and each of them is
// right about its own building.
func TestDayInReadsThePropertysCalendar(t *testing.T) {
	istanbul, err := time.LoadLocation("Europe/Istanbul")
	if err != nil {
		t.Skipf("Europe/Istanbul is not in this system's zone database: %v", err)
	}
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("Europe/Berlin is not in this system's zone database: %v", err)
	}

	// 2026-06-29T21:30Z is already the 30th in Istanbul (UTC+3 all year) and still the
	// 29th in Berlin (UTC+2 in summer).
	instant := time.Date(2026, 6, 29, 21, 30, 0, 0, time.UTC)
	if got := DayIn(instant, istanbul).Format(time.DateOnly); got != "2026-06-30" {
		t.Errorf("DayIn(Istanbul) = %s, want 2026-06-30", got)
	}
	if got := DayIn(instant, berlin).Format(time.DateOnly); got != "2026-06-29" {
		t.Errorf("DayIn(Berlin) = %s, want 2026-06-29", got)
	}
}

func TestValidTimezoneRefusesWhatCannotBeResolved(t *testing.T) {
	if !ValidTimezone("Europe/Istanbul") {
		t.Error("Europe/Istanbul is not accepted")
	}
	for _, bad := range []string{"", "Mars/Olympus", "GMT+3", "istanbul"} {
		if ValidTimezone(bad) {
			t.Errorf("timezone %q is accepted; a property whose zone cannot be resolved "+
				"would have its nights counted against the wrong calendar", bad)
		}
	}
}

// TestNormaliseAmenitiesRefusesAnUnknownKey keeps the closed list closed. A key the
// catalogue does not have is a field error naming the position it was in, not a value that
// reaches a jsonb column and comes back as an enum the contract does not admit.
func TestNormaliseAmenitiesRefusesAnUnknownKey(t *testing.T) {
	ve := &ValidationError{}
	out := NormaliseAmenities("amenities", []string{AmenityPool, "MASSAGE_THERAPY", AmenityWifi}, ve)
	if ve.Len() != 1 {
		t.Fatalf("collected %d field errors, want 1", ve.Len())
	}
	if ve.Fields[0].Field != "amenities[1]" {
		t.Errorf("field = %s, want amenities[1]", ve.Fields[0].Field)
	}
	// The survivors come back in the catalogue's order, not the caller's, so two providers
	// who listed the same amenities differently store the same array.
	if len(out) != 2 || out[0] != AmenityWifi || out[1] != AmenityPool {
		t.Errorf("normalised = %v, want [WIFI POOL]", out)
	}
}

func TestNormaliseAmenitiesDropsDuplicates(t *testing.T) {
	ve := &ValidationError{}
	out := NormaliseAmenities("amenities", []string{AmenityWifi, AmenityWifi, AmenityWifi}, ve)
	if ve.Len() != 0 {
		t.Fatalf("unexpected field errors: %v", ve.Fields)
	}
	if len(out) != 1 {
		t.Errorf("normalised = %v, want one WIFI", out)
	}
}
