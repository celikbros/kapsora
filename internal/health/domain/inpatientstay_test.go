package domain

import (
	"testing"
	"time"
)

// A stay is paid for in days, and a day is not a unit anybody spends exactly. Somebody
// admitted on Monday morning and discharged on Thursday morning occupied a bed on three
// separate days, and the arithmetic that says so is a ceiling, not a division: a floor
// would hand the plan back a day the hospital actually used, and a rounding would do it
// half the time. The rule is also floored at one, because a person who came in and went
// home the same afternoon was still admitted.
//
// This test exists because the delivered suite proved the reconciliation's *release* and
// never proved the count under it: every fixture discharged on a whole number of days,
// where ceiling and floor agree, so the one line that decides what a hospital is paid was
// covered by nothing.
func TestActualDaysCountsAStartedDayAsADay(t *testing.T) {
	t.Parallel()

	admission := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		discharge time.Time
		want      int
	}{
		{"the same afternoon", admission.Add(6 * time.Hour), 1},
		{"an hour short of a full day", admission.Add(23 * time.Hour), 1},
		{"exactly one day", admission.Add(24 * time.Hour), 1},
		{"an hour into the second day", admission.Add(25 * time.Hour), 2},
		{"two days and a morning", admission.Add(2*24*time.Hour + 8*time.Hour), 3},
		{"a hair short of three days", admission.Add(3*24*time.Hour - time.Minute), 3},
		{"exactly three days", admission.Add(3 * 24 * time.Hour), 3},
		{"discharged before admission", admission.Add(-time.Hour), 1},
		{"discharged at the moment of admission", admission, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ActualDays(admission, tc.discharge); got != tc.want {
				t.Fatalf("ActualDays(%s) = %d, want %d", tc.discharge.Sub(admission), got, tc.want)
			}
		})
	}
}

// The window is a property of the tenant and both of its ends are real. The delivered suite
// covered the defaults through the service; this covers the function itself, including the
// day boundaries where an off-by-one lives.
func TestAdmissionWindowIsInclusiveAtBothEnds(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 10, 14, 30, 0, 0, time.UTC)
	day := 24 * time.Hour
	cases := []struct {
		name      string
		admission time.Time
		want      bool
	}{
		{"three days back, at the start of that day", now.Truncate(day).Add(-3 * day), true},
		{"a minute before three days back", now.Truncate(day).Add(-3*day - time.Minute), false},
		{"today", now, true},
		{"the last minute of the thirtieth day forward", now.Truncate(day).Add(31*day - time.Minute), true},
		{"the thirty-first day forward", now.Truncate(day).Add(31 * day), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := AdmissionInWindow(tc.admission, now, 3, 30); got != tc.want {
				t.Fatalf("AdmissionInWindow(%s) = %v, want %v", tc.admission, got, tc.want)
			}
		})
	}
}
