package application_test

import (
	"reflect"
	"testing"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
)

func TestQuoteQuantityEntitlementIsNotMoney(t *testing.T) {
	f := newQuantityFixture(t)
	before := f.ledgerState(t)
	in := f.request()
	// A published service mapping must work without a caller-supplied entitlement hint.
	in.Context = nil
	quote := f.quote(t, in)
	if quote.Outcome != "QUOTED" || quote.ContractAmount != "500" || quote.PayerAmount != "400" || quote.MemberAmount != "100" {
		t.Fatalf("20 sessions for a 500 TRY service with 20%% share: %s %s/%s/%s", quote.Outcome, quote.ContractAmount, quote.PayerAmount, quote.MemberAmount)
	}
	if !reflect.DeepEqual(before, f.ledgerState(t)) {
		t.Fatal("quote changed the entitlement ledger")
	}
	// The mapping draws two sessions per service: eleven services exceed twenty sessions.
	in.Items[0].Quantity = benefitdomain.MustQuantity("11")
	quote = f.quote(t, in)
	if quote.Outcome != "NOT_ELIGIBLE" || quote.PayerAmount != "0" {
		t.Fatalf("mapping factor was ignored: %s payer=%s", quote.Outcome, quote.PayerAmount)
	}
	// A conflicting hint cannot override the published service mapping.
	in = f.request()
	in.Context = map[string]any{"entitlementCode": "NOT_THE_MAPPED_ENTITLEMENT"}
	quote = f.quote(t, in)
	if quote.PayerAmount != "400" {
		t.Fatalf("hint overrode mapping: payer=%s", quote.PayerAmount)
	}
	if !reflect.DeepEqual(before, f.ledgerState(t)) {
		t.Fatal("quantity checks changed balances or ledger rows")
	}
}

func TestQuoteQuantityLinesShareTheMappedBalance(t *testing.T) {
	f := newQuantityFixture(t)
	in := f.request()
	in.Context = nil
	in.Items[0].Quantity = benefitdomain.MustQuantity("6") // 12 entitlement units
	in.Items = append(in.Items, in.Items[0])               // another 12 cannot fit in the same 20
	before := f.ledgerState(t)
	quote := f.quote(t, in)
	if quote.Items[0].PayerAmount != "400" || quote.Items[1].PayerAmount != "0" || quote.Items[1].Outcome != "NOT_ELIGIBLE" {
		t.Fatalf("same account promised twice: %+v", quote.Items)
	}
	if !hasExplanation(quote.Items[1].Explanations, "BALANCE_INSUFFICIENT") {
		t.Fatal("second line does not explain the exhausted quantity")
	}
	if !reflect.DeepEqual(before, f.ledgerState(t)) {
		t.Fatal("multi-line quote moved the ledger")
	}
}
