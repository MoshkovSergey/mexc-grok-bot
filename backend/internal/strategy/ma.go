package strategy

import "math"

// NaN is the port's representation of Pine's na.
func nan() float64 { return math.NaN() }
func isNa(x float64) bool { return math.IsNaN(x) }

// FMA dispatches to the requested moving-average type (Pine f_ma).
// Seeds follow TradingView ta.* conventions: EMA/RMA seed with the first value,
// SMA/WMA produce na until the window is full. Small boundary differences vs TV
// are expected and must be caught by the validation step (see README).
func FMA(src []float64, length int, maType string) []float64 {
	n := len(src)
	out := make([]float64, n)
	if length <= 0 || n == 0 {
		for i := range out {
			out[i] = nan()
		}
		return out
	}
	switch maType {
	case "SMA":
		return SMA(src, length)
	case "RMA":
		return RMA(src, length)
	case "WMA":
		return WMA(src, length)
	default: // EMA
		return EMA(src, length)
	}
}

// EMA reproduces ta.ema: alpha = 2/(n+1), seed = src[0].
func EMA(src []float64, n int) []float64 {
	out := make([]float64, len(src))
	if n <= 0 || len(src) == 0 {
		for i := range out {
			out[i] = nan()
		}
		return out
	}
	alpha := 2.0 / float64(n+1)
	out[0] = src[0]
	for i := 1; i < len(src); i++ {
		if isNa(src[i]) {
			out[i] = out[i-1]
			continue
		}
		out[i] = alpha*src[i] + (1-alpha)*out[i-1]
	}
	return out
}

// SMA reproduces ta.sma: na until i >= n-1.
func SMA(src []float64, n int) []float64 {
	out := make([]float64, len(src))
	if n <= 0 {
		for i := range out {
			out[i] = nan()
		}
		return out
	}
	sum := 0.0
	for i := 0; i < len(src); i++ {
		if isNa(src[i]) {
			out[i] = nan()
			continue
		}
		sum += src[i]
		if i >= n {
			sum -= src[i-n]
		}
		if i >= n-1 {
			out[i] = sum / float64(n)
		} else {
			out[i] = nan()
		}
	}
	return out
}

// RMA reproduces ta.rma (Wilder): seed = src[0], rma[i] = (src[i] + (n-1)*rma[i-1]) / n.
func RMA(src []float64, n int) []float64 {
	out := make([]float64, len(src))
	if n <= 0 || len(src) == 0 {
		for i := range out {
			out[i] = nan()
		}
		return out
	}
	out[0] = src[0]
	for i := 1; i < len(src); i++ {
		if isNa(src[i]) {
			out[i] = out[i-1]
			continue
		}
		out[i] = (src[i] + float64(n-1)*out[i-1]) / float64(n)
	}
	return out
}

// WMA reproduces ta.wma: na until i >= n-1.
func WMA(src []float64, n int) []float64 {
	out := make([]float64, len(src))
	if n <= 0 {
		for i := range out {
			out[i] = nan()
		}
		return out
	}
	for i := 0; i < len(src); i++ {
		if i < n-1 {
			out[i] = nan()
			continue
		}
		num, den := 0.0, 0.0
		for j := 0; j < n; j++ {
			w := float64(n - j)
			v := src[i-j]
			if isNa(v) {
				num = nan()
				break
			}
			num += w * v
			den += w
		}
		if isNa(num) || den == 0 {
			out[i] = nan()
		} else {
			out[i] = num / den
		}
	}
	return out
}

// RollingLowest / RollingHighest reproduce ta.lowest / ta.highest (na until full window).
func RollingLowest(src []float64, n int) []float64 {
	out := make([]float64, len(src))
	for i := 0; i < len(src); i++ {
		if i < n-1 {
			out[i] = nan()
			continue
		}
		m := src[i]
		for j := i - n + 1; j <= i; j++ {
			if isNa(src[j]) {
				m = nan()
				break
			}
			if src[j] < m {
				m = src[j]
			}
		}
		out[i] = m
	}
	return out
}

func RollingHighest(src []float64, n int) []float64 {
	out := make([]float64, len(src))
	for i := 0; i < len(src); i++ {
		if i < n-1 {
			out[i] = nan()
			continue
		}
		m := src[i]
		for j := i - n + 1; j <= i; j++ {
			if isNa(src[j]) {
				m = nan()
				break
			}
			if src[j] > m {
				m = src[j]
			}
		}
		out[i] = m
	}
	return out
}

// Crossover / Crossunder reproduce ta.crossover / ta.crossunder (na-aware => false).
func Crossover(a, b []float64, i int) bool {
	if i < 1 {
		return false
	}
	if isNa(a[i]) || isNa(b[i]) || isNa(a[i-1]) || isNa(b[i-1]) {
		return false
	}
	return a[i-1] <= b[i-1] && a[i] > b[i]
}

func Crossunder(a, b []float64, i int) bool {
	if i < 1 {
		return false
	}
	if isNa(a[i]) || isNa(b[i]) || isNa(a[i-1]) || isNa(b[i-1]) {
		return false
	}
	return a[i-1] >= b[i-1] && a[i] < b[i]
}