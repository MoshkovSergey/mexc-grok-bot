package backtest

import (
	"math"
	"strconv"
)

// StatFloat is a float64 that survives encoding/json for the backtest metrics layer.
//
// Why a separate type from strategy.NaNFloat: the backtest distinguishes two
// different "absent" cases that must NOT collapse to the same wire value:
//   - NaN  -> JSON null   : metric undefined (no trades, zero dispersion, etc.)
//   - +Inf -> JSON "inf"  : profit factor with zero losing trades (a strong result)
//   - -Inf -> JSON "-inf" : symmetric negative infinity (not expected, handled anyway)
// Collapsing Inf to null would silently discard "no drawdown-side losses", which is
// exactly the kind of misleading simplification a research instrument must avoid.
//
// Internal arithmetic stays float64; the wrapper is applied only at assignment into
// response structs. Because StatFloat is a named type, assigning a bare float64 to a
// StatFloat field is a COMPILE error, so forgotten conversions cannot ship silently.
type StatFloat float64

// MarshalJSON emits null for NaN, "inf"/"-inf" for infinities, else a plain decimal
// (no scientific notation), matching the rest of the API's float formatting.
func (f StatFloat) MarshalJSON() ([]byte, error) {
	v := float64(f)
	switch {
	case math.IsNaN(v):
		return []byte("null"), nil
	case math.IsInf(v, 1):
		return []byte(`"inf"`), nil
	case math.IsInf(v, -1):
		return []byte(`"-inf"`), nil
	default:
		return []byte(strconv.FormatFloat(v, 'f', -1, 64)), nil
	}
}

// Float returns the underlying float64 for printing/formatting outside JSON.
func (f StatFloat) Float() float64 { return float64(f) }

// IsFinite reports whether the value is a usable finite number (false for NaN/Inf).
// Implemented without math.IsFinite for compatibility with older Go toolchains.
func (f StatFloat) IsFinite() bool {
	v := float64(f)
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}