package accommodationhttp

import (
	"encoding/json"
	"testing"

	"github.com/celikbros/kapsora/internal/accommodation/application"
)

// Clients must be able to distinguish an ordinary completed stay from an over-stay.
// Test the serialized response: application-only checks missed the absent wire field.
func TestBookingResponsePreservesOverStayFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		over bool
	}{
		{"ordinary_stay", false}, {"over_stay", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(bookingView(application.BookingView{
				Booking: application.BookingRecord{Status: "COMPLETED", OverBooking: tc.over},
			}))
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			flag, exists := body["overBooking"]
			if !exists {
				t.Fatal("booking response omits overBooking")
			}
			var got bool
			if err := json.Unmarshal(flag, &got); err != nil {
				t.Fatal(err)
			}
			if got != tc.over {
				t.Fatalf("overBooking = %v, want %v", got, tc.over)
			}
		})
	}
}
