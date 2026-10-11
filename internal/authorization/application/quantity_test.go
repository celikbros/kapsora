package application

import (
	"testing"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
)

func TestFractionalConsumptionAndReleaseConserveRoundedHold(t *testing.T) {
	item := AuthorizationItemRecord{ApprovedQuantity: "1", ConsumedQuantity: "0", EntitlementUnitFactor: "0.333333"}
	consumed, err := entitlementConsumption(item, benefitdomain.MustQuantity("0.5"))
	if err != nil {
		t.Fatal(err)
	}
	item.ConsumedQuantity = "0.5"
	released, err := entitlementRelease(item, benefitdomain.MustQuantity("0.5"))
	if err != nil {
		t.Fatal(err)
	}
	if consumed.Add(released).String() != "0.333333" {
		t.Fatalf("split changed hold: consume %s release %s", consumed.String(), released.String())
	}
	second, err := entitlementConsumption(item, benefitdomain.MustQuantity("0.5"))
	if err != nil {
		t.Fatal(err)
	}
	if consumed.Add(second).String() != "0.333333" {
		t.Fatal("split consumption changed hold")
	}
}
