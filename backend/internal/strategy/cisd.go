package strategy

import "math"

// CISD state carried across bars inside a single ComputeCTS pass. This mirrors the
// Pine `var array<...>` globals: state lives for the duration of the pass and is
// reset at the start, which is exactly how TV behaves once history is fully warmed.
//
// Field naming convention (fixed): swingHigh / swingLow correspond to Pine
// ta.pivothigh / ta.pivotlow. Earlier revisions had a typo (swungHigh/swungLow)
// causing a compile mismatch with cts.go; do not reintroduce the "n".
type cisdState struct {
	trend           int
	lastWickHigh    float64
	lastWickHighBar int
	lastWickLow     float64
	lastWickLowBar  int

	// Active swing lines (level + originating bar index in the closed slice).
	swingHigh []swingLine
	swingLow  []swingLine

	// Pending change-of-state levels.
	bearLvl []pending
	bullLvl []pending
}

type swingLine struct {
	level float64
	x1    int
}

type pending struct {
	level   float64
	bar     int
	extreme float64
}

// pivotAt reproduces ta.pivothigh/ta.pivotlow with (left=right=swingLen).
// A pivot at center index k is KNOWN only at bar i = k + swingLen (the right side
// must exist). Strictness uses >= on both sides; this is one of the documented-
// ambiguous spots vs TV and is flagged for validation against TradingView.
// Returns (pivotHighValue, pivotLowValue) at k, or na if k is not a pivot.
func pivotAt(c []Candle, k, swingLen int) (float64, float64) {
	ph, pl := nan(), nan()
	if k-swingLen < 0 || k+swingLen >= len(c) {
		return ph, pl
	}
	h := c[k].High
	isHigh := true
	for j := k - swingLen; j <= k+swingLen; j++ {
		if j == k {
			continue
		}
		if c[j].High > h {
			isHigh = false
			break
		}
	}
	if isHigh {
		ph = h
	}
	l := c[k].Low
	isLow := true
	for j := k - swingLen; j <= k+swingLen; j++ {
		if j == k {
			continue
		}
		if c[j].Low < l {
			isLow = false
			break
		}
	}
	if isLow {
		pl = l
	}
	return ph, pl
}

// RunTop reproduces Pine f_runTop: scan backwards from offset 1 while bars are
// bearish (close<open), tracking max(open,high); stop at the first bullish bar.
func RunTop(c []Candle, i, maxBars int) float64 {
	res := nan()
	offset := 1
	for offset <= maxBars && i-offset >= 0 {
		b := c[i-offset]
		if b.Close < b.Open {
			top := math.Max(b.Open, b.High)
			if isNa(res) || top > res {
				res = top
			}
			offset++
		} else {
			break
		}
	}
	return res
}

// RunBottom reproduces Pine f_runBottom (bullish run, min(open,low)).
func RunBottom(c []Candle, i, maxBars int) float64 {
	res := nan()
	offset := 1
	for offset <= maxBars && i-offset >= 0 {
		b := c[i-offset]
		if b.Close > b.Open {
			bot := math.Min(b.Open, b.Low)
			if isNa(res) || bot < res {
				res = bot
			}
			offset++
		} else {
			break
		}
	}
	return res
}

// HighestCloseIn / LowestCloseIn reproduce ta.highest/ta.lowest over close[bBar..i].
func HighestCloseIn(c []Candle, bBar, i int) float64 {
	m := nan()
	for j := bBar; j <= i; j++ {
		if isNa(m) || c[j].Close > m {
			m = c[j].Close
		}
	}
	return m
}

func LowestCloseIn(c []Candle, bBar, i int) float64 {
	m := nan()
	for j := bBar; j <= i; j++ {
		if isNa(m) || c[j].Close < m {
			m = c[j].Close
		}
	}
	return m
}