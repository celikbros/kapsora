package application

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestBookingContractBindingResolverRequiresV2Identity(t *testing.T) {
	selected := uuid.New()
	for _, tc := range []struct {
		name       string
		raw        string
		want       uuid.UUID
		wantLegacy bool
		wantError  bool
	}{
		{"selected v2", `{"version":2,"firstNightContractVersionId":"` + selected.String() + `"}`, selected, false, false},
		{"legacy v1", `{"version":1}`, uuid.Nil, true, true},
		{"v2 missing identity", `{"version":2}`, uuid.Nil, false, true},
		{"v2 null identity", `{"version":2,"firstNightContractVersionId":null}`, uuid.Nil, false, true},
		{"v2 zero identity", `{"version":2,"firstNightContractVersionId":"00000000-0000-0000-0000-000000000000"}`, uuid.Nil, false, true},
		{"v2 malformed identity", `{"version":2,"firstNightContractVersionId":"not-a-uuid"}`, uuid.Nil, false, true},
		{"unknown version", `{"version":3,"firstNightContractVersionId":"` + selected.String() + `"}`, uuid.Nil, false, true},
		{"malformed JSON", `{"version":2,`, uuid.Nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := confirmationContractVersion(json.RawMessage(tc.raw))
			if got != tc.want || (err != nil) != tc.wantError ||
				errors.Is(err, errLegacyQuoteIdentity) != tc.wantLegacy {
				t.Fatalf("confirmationContractVersion = %s, %v; want %s, error=%t, legacy=%t",
					got, err, tc.want, tc.wantError, tc.wantLegacy)
			}
		})
	}
}

func TestBookingContractBindingLegacySnapshotRemainsReadable(t *testing.T) {
	raw := json.RawMessage(`{"version":1,"totalAmount":"500","nights":[{"stayDate":"2026-06-15","amount":"500"}]}`)
	snapshot, err := DecodeQuoteSnapshot(raw)
	if err != nil || snapshot.Version != 1 || snapshot.TotalAmount != "500" ||
		len(snapshot.Nights) != 1 || snapshot.FirstNightContractVersionID != nil {
		t.Fatalf("legacy snapshot = %+v, %v; want its original price/night and no new identity", snapshot, err)
	}
}
