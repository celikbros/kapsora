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
		{"unknown version", `{"version":4,"firstNightContractVersionId":"` + selected.String() + `"}`, uuid.Nil, false, true},
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

func TestNightConversionSnapshotRequiresExactUnitsAndIdentity(t *testing.T) {
	base := NightConversion{
		PlanVersionID: uuid.New(), DefinitionID: uuid.New(), AccountID: uuid.New(),
		UnitType: "NIGHT", UnitFactor: "0.333333", ReservedUnits: "0.999999",
	}
	if units, err := base.validate(3); err != nil || units.String() != "0.999999" {
		t.Fatalf("exact six-decimal conversion = %s, %v", units.String(), err)
	}
	for _, tc := range []struct {
		name   string
		change func(*NightConversion)
	}{
		{"rounded incomplete", func(c *NightConversion) { c.ReservedUnits = "1" }},
		{"zero factor", func(c *NightConversion) { c.UnitFactor = "0" }},
		{"wrong unit", func(c *NightConversion) { c.UnitType = "MONEY" }},
		{"missing definition", func(c *NightConversion) { c.DefinitionID = uuid.Nil }},
		{"missing account", func(c *NightConversion) { c.AccountID = uuid.Nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conversion := base
			tc.change(&conversion)
			if _, err := conversion.validate(3); !errors.Is(err, ErrQuoteStale) {
				t.Fatalf("malformed conversion = %v, want stale quote", err)
			}
		})
	}
}
