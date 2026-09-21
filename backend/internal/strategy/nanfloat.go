package strategy

import (
	"math"
	"strconv"
)

// NaNFloat is a float64 that serializes to JSON null when it holds NaN or Inf.
//
// Why this exists: the strategy ports represent Pine's `na` as math.NaN().
// encoding/json refuses NaN/Inf; fed directly into a response struct it yields a
// truncated/empty 200 body, which the browser surfaces as
// "Unexpected end of JSON input" (the exact bug this file fixes). Mapping
// na -> null is semantically correct and is already handled by the frontend
// (isNum / toLinePoints / lastFinite / fmt6 treat null as "no value"), so no
// frontend change is required.
//
// Trade-off vs a reflection sanitizer in writeJSON: this is checked by the
// compiler (a wrong conversion is a build error, not a silent runtime gap),
// which is precisely why we prefer it here.
type NaNFloat float64

// MarshalJSON writes null for NaN/Inf, otherwise a plain decimal number
// (no scientific notation, consistent with the rest of the API float formatting).
func (f NaNFloat) MarshalJSON() ([]byte, error) {
	v := float64(f)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return []byte("null"), nil
	}
	return []byte(strconv.FormatFloat(v, 'f', -1, 64)), nil
}

// naF is a convenience constructor for an "absent" NaNFloat (Pine na).
func naF() NaNFloat { return NaNFloat(math.NaN()) }

// fNF converts a computed float64 (possibly NaN) into a NaNFloat response field.
func fNF(v float64) NaNFloat { return NaNFloat(v) }