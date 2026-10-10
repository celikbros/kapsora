package application

import benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"

// entitlementConsumption converts the cumulative service usage, then takes the delta.
// This keeps split deliveries at numeric(20,6) equal to a single complete delivery.
func entitlementConsumption(item AuthorizationItemRecord, quantity benefitdomain.Quantity) (benefitdomain.Quantity, error) {
	factor, err := benefitdomain.ParseQuantity(item.EntitlementUnitFactor)
	if err != nil {
		return benefitdomain.Quantity{}, err
	}
	consumed, err := benefitdomain.ParseQuantity(item.ConsumedQuantity)
	if err != nil {
		return benefitdomain.Quantity{}, err
	}
	return consumed.Add(quantity).Mul(factor).Sub(consumed.Mul(factor)), nil
}

// Release the unused tail of the approved quantity. Rounding both cumulative
// boundaries preserves a fractional hold after a split delivery and discharge.
func entitlementRelease(item AuthorizationItemRecord, quantity benefitdomain.Quantity) (benefitdomain.Quantity, error) {
	factor, err := benefitdomain.ParseQuantity(item.EntitlementUnitFactor)
	if err != nil {
		return benefitdomain.Quantity{}, err
	}
	approved, err := benefitdomain.ParseQuantity(item.ApprovedQuantity)
	if err != nil {
		return benefitdomain.Quantity{}, err
	}
	return approved.Mul(factor).Sub(approved.Sub(quantity).Mul(factor)), nil
}
