package application

import "strings"

// DefaultMinorUnits is how many decimals a currency's smallest unit has when nothing more
// is known about it. Two is right for every currency the pilot will see and for the great
// majority of ISO 4217 as a whole, and it is the safe answer to be wrong with: rounding a
// zero-decimal currency to two decimals leaves the figure intact and merely trailing,
// whereas rounding a two-decimal currency to none would silently lose the kuruş.
const DefaultMinorUnits = 2

// minorUnitsByCurrency is the exceptions table. It is empty of exceptions today on
// purpose: every currency below carries two decimals, and the map exists so the one place
// a zero-decimal currency (JPY, KRW) or a three-decimal one (KWD, BHD) has to be named is
// this map, rather than a conditional somewhere inside the arithmetic.
var minorUnitsByCurrency = map[string]int{
	"TRY": 2,
	"EUR": 2,
	"USD": 2,
	"GBP": 2,
	"CHF": 2,
}

// MinorUnits is the currency's scale: the single point at which a quote rounds, after
// every earlier step has stayed at the full numeric(20,6) precision (v1.2 11.6). An
// unknown code falls back to DefaultMinorUnits rather than failing the quote, because a
// currency nobody configured is a configuration gap and not a reason to refuse a number.
func MinorUnits(currencyCode string) int {
	if n, ok := minorUnitsByCurrency[strings.ToUpper(strings.TrimSpace(currencyCode))]; ok {
		return n
	}
	return DefaultMinorUnits
}
