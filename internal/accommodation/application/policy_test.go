package application

import "testing"

// The no-show penalty in nights is a percentage of the nights the plan carried, rounded
// up, and never more than that cover. Rounding up is the whole point of the function: half
// of one night is one night, because a no-show that cost the payer nothing would be a
// policy the hotel never agreed to. The exact cases are the ones a loop with the wrong
// comparison gets wrong -- 50 % of two nights is exactly one, and "strictly greater"
// would answer two.
func TestNoShowNightsRoundUpAndNeverPastTheCover(t *testing.T) {
	cases := []struct {
		covered int
		percent string
		want    int
	}{
		{covered: 2, percent: "50", want: 1},
		{covered: 4, percent: "25", want: 1},
		{covered: 4, percent: "50", want: 2},
		{covered: 3, percent: "50", want: 2},
		{covered: 1, percent: "50", want: 1},
		{covered: 1, percent: "1", want: 1},
		{covered: 2, percent: "100", want: 2},
		{covered: 3, percent: "100", want: 3},
		{covered: 5, percent: "0", want: 0},
		{covered: 0, percent: "100", want: 0},
		{covered: 3, percent: "-10", want: 0},
		{covered: 3, percent: "not a number", want: 0},
	}
	for _, c := range cases {
		if got := ceilPercentOfNights(c.covered, c.percent); got != c.want {
			t.Errorf("ceilPercentOfNights(%d, %q) = %d, want %d", c.covered, c.percent, got, c.want)
		}
	}
}
