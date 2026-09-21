package strategy

import "math"

// RSI reproduces ta.rsi (Wilder): na until enough deltas accumulated.
func RSI(close []float64, n int) []float64 {
	out := make([]float64, len(close))
	for i := range out {
		out[i] = nan()
	}
	if n <= 0 || len(close) < n+1 {
		return out
	}
	up := make([]float64, len(close))
	dn := make([]float64, len(close))
	for i := 1; i < len(close); i++ {
		ch := close[i] - close[i-1]
		if ch > 0 {
			up[i] = ch
		}
		if ch < 0 {
			dn[i] = -ch
		}
	}
	// Wilder smoothing over deltas starting at index 1.
	ru := RMA(up[1:], n)
	rd := RMA(dn[1:], n)
	for i := 1; i < len(close); i++ {
		u := ru[i-1]
		d := rd[i-1]
		if isNa(u) || isNa(d) {
			continue
		}
		if d == 0 {
			out[i] = 100
			continue
		}
		rs := u / d
		out[i] = 100 - 100/(1+rs)
	}
	return out
}

// ATR reproduces ta.atr via true range + RMA.
func ATR(c []Candle, n int) []float64 {
	tr := make([]float64, len(c))
	for i := range c {
		if i == 0 {
			tr[i] = c[i].High - c[i].Low
			continue
		}
		pc := c[i-1].Close
		tr[i] = math.Max(c[i].High-c[i].Low, math.Max(math.Abs(c[i].High-pc), math.Abs(c[i].Low-pc)))
	}
	return RMA(tr, n)
}

// VolRatio = volume / SMA(volume, n); na when the SMA is na or zero.
func VolRatio(c []Candle, n int) []float64 {
	vol := make([]float64, len(c))
	for i := range c {
		vol[i] = c[i].Volume
	}
	sma := SMA(vol, n)
	out := make([]float64, len(c))
	for i := range out {
		if isNa(sma[i]) || sma[i] == 0 {
			out[i] = nan()
		} else {
			out[i] = vol[i] / sma[i]
		}
	}
	return out
}

// HTFCloseSeries extracts the close of confirmed HTF candles (caller passes only closed bars).
func HTFCloseSeries(htf []Candle) []float64 {
	out := make([]float64, len(htf))
	for i := range htf {
		out[i] = htf[i].Close
	}
	return out
}

// PriceSource resolves Pine srcType to a per-bar series.
func PriceSource(c []Candle, srcType string) []float64 {
	out := make([]float64, len(c))
	for i, b := range c {
		switch srcType {
		case "open":
			out[i] = b.Open
		case "high":
			out[i] = b.High
		case "low":
			out[i] = b.Low
		case "hl2":
			out[i] = (b.High + b.Low) / 2
		case "hlc3":
			out[i] = (b.High + b.Low + b.Close) / 3
		case "ohlc4":
			out[i] = (b.Open + b.High + b.Low + b.Close) / 4
		default: // close
			out[i] = b.Close
		}
	}
	return out
}