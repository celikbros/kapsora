package domain

import (
	"errors"
	"math/big"
	"strings"
)

// Quantity is an exact numeric(20,6) value held as an integer number of micro-units
// (value = Units / 10^6). Entitlement balances are money and counts, so they never pass
// through a float: parsing, arithmetic and rendering all stay in base 10 (handbook
// section 3). The zero Quantity is 0.
type Quantity struct {
	units *big.Int // nil means zero
}

// QuantityScale is the scale of the numeric(20,6) entitlement columns.
const QuantityScale = MaxScale

// ErrQuantityRange reports a value outside the numeric(20,6) envelope.
var ErrQuantityRange = errors.New("miktar numeric(20,6) sınırlarının dışında")

// maxUnits is 10^20 in micro-units, the exclusive bound of numeric(20,6).
var maxUnits = new(big.Int).Exp(big.NewInt(10), big.NewInt(MaxPrecision), nil)

// ParseQuantity reads an exact decimal. It accepts the canonical form produced by
// NormalizeDecimal as well as the fully padded text PostgreSQL renders for numeric(20,6).
func ParseQuantity(raw string) (Quantity, error) {
	s := strings.TrimSpace(raw)
	if s == "" || !decimalPattern.MatchString(s) {
		return Quantity{}, ErrDecimalFormat
	}
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, fracPart, _ := strings.Cut(s, ".")
	fracPart = strings.TrimRight(fracPart, "0")
	if len(fracPart) > QuantityScale {
		return Quantity{}, ErrDecimalFormat
	}
	digits := intPart + fracPart + strings.Repeat("0", QuantityScale-len(fracPart))
	units, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return Quantity{}, ErrDecimalFormat
	}
	if units.CmpAbs(maxUnits) >= 0 {
		return Quantity{}, ErrQuantityRange
	}
	if negative {
		units.Neg(units)
	}
	return Quantity{units: units}, nil
}

// MustQuantity parses a constant known to be valid; it panics otherwise.
func MustQuantity(raw string) Quantity {
	q, err := ParseQuantity(raw)
	if err != nil {
		panic("benefit: invalid quantity literal " + raw + ": " + err.Error())
	}
	return q
}

// ZeroQuantity is the additive identity.
func ZeroQuantity() Quantity { return Quantity{} }

func (q Quantity) value() *big.Int {
	if q.units == nil {
		return new(big.Int)
	}
	return q.units
}

// Add returns q + o.
func (q Quantity) Add(o Quantity) Quantity {
	return Quantity{units: new(big.Int).Add(q.value(), o.value())}
}

// Sub returns q - o.
func (q Quantity) Sub(o Quantity) Quantity {
	return Quantity{units: new(big.Int).Sub(q.value(), o.value())}
}

// Neg returns -q.
func (q Quantity) Neg() Quantity { return Quantity{units: new(big.Int).Neg(q.value())} }

// Cmp compares q with o the way big.Int.Cmp does.
func (q Quantity) Cmp(o Quantity) int { return q.value().Cmp(o.value()) }

// Sign reports the sign of q: -1, 0 or +1.
func (q Quantity) Sign() int { return q.value().Sign() }

// IsZero reports whether q is exactly zero.
func (q Quantity) IsZero() bool { return q.Sign() == 0 }

// IsPositive reports whether q is strictly greater than zero.
func (q Quantity) IsPositive() bool { return q.Sign() > 0 }

// IsNegative reports whether q is strictly less than zero.
func (q Quantity) IsNegative() bool { return q.Sign() < 0 }

// Min returns the smaller of q and o.
func (q Quantity) Min(o Quantity) Quantity {
	if q.Cmp(o) <= 0 {
		return q
	}
	return o
}

// String renders the canonical decimal form: no trailing fractional zeros, "0" for zero.
// This is the text handed to PostgreSQL as ::numeric and returned on the wire.
func (q Quantity) String() string {
	units := q.value()
	negative := units.Sign() < 0
	digits := new(big.Int).Abs(units).String()
	if len(digits) <= QuantityScale {
		digits = strings.Repeat("0", QuantityScale-len(digits)+1) + digits
	}
	intPart := digits[:len(digits)-QuantityScale]
	fracPart := strings.TrimRight(digits[len(digits)-QuantityScale:], "0")
	out := intPart
	if fracPart != "" {
		out += "." + fracPart
	}
	if negative && out != "0" {
		out = "-" + out
	}
	return out
}

// scaleUnits is 10^QuantityScale, the divisor between a product of two micro-unit values
// and a micro-unit value.
var scaleUnits = new(big.Int).Exp(big.NewInt(10), big.NewInt(QuantityScale), nil)

var hundredUnits = new(big.Int).Mul(big.NewInt(100), scaleUnits)

// Mul returns q × o at the working scale, rounding half away from zero. Two values at
// six decimals multiply to twelve, and the type carries six, so a product is the one
// place ordinary arithmetic has to give something up; every other operation here is
// exact. Pricing therefore does its single presentation rounding separately, with
// RoundTo, and never relies on this one.
func (q Quantity) Mul(o Quantity) Quantity {
	product := new(big.Int).Mul(q.value(), o.value())
	return Quantity{units: divRoundHalfAway(product, scaleUnits)}
}

// Percent returns p per cent of q, rounded half away from zero at the working scale. It
// divides once rather than multiplying by a converted rate, so 20 % of 500 is exactly
// 100 rather than 99.999999.
func (q Quantity) Percent(p Quantity) Quantity {
	product := new(big.Int).Mul(q.value(), p.value())
	return Quantity{units: divRoundHalfAway(product, hundredUnits)}
}

// RoundTo returns q rounded half away from zero to the given number of decimals, which is
// how a currency's minor unit is applied. A scale at or above the working scale is a
// no-op; a negative scale is treated as zero.
func (q Quantity) RoundTo(scale int) Quantity {
	if scale >= QuantityScale {
		return q
	}
	if scale < 0 {
		scale = 0
	}
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(QuantityScale-scale)), nil)
	rounded := divRoundHalfAway(q.value(), factor)
	return Quantity{units: new(big.Int).Mul(rounded, factor)}
}

// divRoundHalfAway divides n by d, rounding halves away from zero. Half away from zero is
// what invoices and tariffs mean by rounding: 2.5 kuruş becomes 3, and -2.5 becomes -3,
// so a refund rounds the same distance as the charge it reverses.
func divRoundHalfAway(n, d *big.Int) *big.Int {
	quotient, remainder := new(big.Int).QuoRem(n, d, new(big.Int))
	if remainder.Sign() == 0 {
		return quotient
	}
	twice := new(big.Int).Abs(remainder)
	twice.Lsh(twice, 1)
	if twice.Cmp(new(big.Int).Abs(d)) < 0 {
		return quotient
	}
	if n.Sign() < 0 {
		return quotient.Sub(quotient, big.NewInt(1))
	}
	return quotient.Add(quotient, big.NewInt(1))
}
