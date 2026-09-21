package strategy

import (
	"math"
	"time"
)

// SmartMoneyParams mirrors the Pine inputs (defaults = original script).
type SmartMoneyParams struct {
	LiquidityLen   int
	FadeLiquidity  int
	ATRLen         int
	ProfOffset     int  // display-only parity; NOT used in level math
	ShowPOC        bool
	MaxProfileBins int // safety cap on bin count (deviation note #2)
}

// DefaultSmartMoneyParams reproduces the original script's defaults.
func DefaultSmartMoneyParams() SmartMoneyParams {
	return SmartMoneyParams{
		LiquidityLen: 100, FadeLiquidity: 100, ATRLen: 100,
		ProfOffset: 50, ShowPOC: true, MaxProfileBins: 500,
	}
}

// SmartMoneyBar is the per-bar structural-liquidity output (NaNFloat => na becomes null).
type SmartMoneyBar struct {
	Time        time.Time `json:"time"`
	LiqTop      NaNFloat  `json:"liqTop"`
	LiqBottom   NaNFloat  `json:"liqBottom"`
	TopSweep    bool      `json:"topSweep"`
	BottomSweep bool      `json:"bottomSweep"`
}

// SmartMoneyProfile is the POC matrix snapshot at the last confirmed bar.
type SmartMoneyProfile struct {
	Valid    bool      `json:"valid"`
	StartBar int       `json:"startBar"`
	IndexTop int       `json:"indexTop"`
	IndexBot int       `json:"indexBot"`
	ProfTop  NaNFloat  `json:"profTop"`
	ProfBot  NaNFloat  `json:"profBot"`
	BinSize  NaNFloat  `json:"binSize"`
	Bins     []float64 `json:"bins"`   // volume per price bin (low->high); volumes are not NaN
	MaxVol   float64   `json:"maxVol"`
	POCIndex int       `json:"pocIndex"`
	POCPrice NaNFloat  `json:"pocPrice"`
	MidPrice NaNFloat  `json:"midPrice"`
}

// SmartMoneyResult bundles per-bar levels + the terminal profile snapshot.
type SmartMoneyResult struct {
	PerBar  []SmartMoneyBar   `json:"perBar"`
	Profile SmartMoneyProfile `json:"profile"`
}

// ComputeSmartMoney runs the structural-liquidity + POC-port matrix over CONFIRMED candles.
func ComputeSmartMoney(closed []Candle, p SmartMoneyParams) SmartMoneyResult {
	res := SmartMoneyResult{PerBar: make([]SmartMoneyBar, len(closed))}
	n := len(closed)
	if n == 0 {
		return res
	}

	highs := make([]float64, n)
	lows := make([]float64, n)
	closes := make([]float64, n)
	vols := make([]float64, n)
	for i := range closed {
		highs[i] = closed[i].High
		lows[i] = closed[i].Low
		closes[i] = closed[i].Close
		vols[i] = closed[i].Volume
	}

	H := RollingHighest(highs, p.LiquidityLen)
	L := RollingLowest(lows, p.LiquidityLen)

	atrRaw := ATR(closed, p.ATRLen)
	binSize := nan()
	if !isNa(atrRaw[n-1]) {
		binSize = atrRaw[n-1] * 0.2
	}

	var (
		curTop, curBot     = nan(), nan()
		ageTop, ageBot     = 0, 0
		indexTop, indexBot = -1, -1
		profTop, profBot   = nan(), nan()
	)

	for i := 0; i < n; i++ {
		topTouch := !isNa(H[i]) && highs[i] == H[i]
		botTouch := !isNa(L[i]) && lows[i] == L[i]

		if topTouch {
			curTop = H[i]
			ageTop = 0
			indexTop = i
			profTop = H[i]
		} else {
			ageTop++
			if ageTop >= p.FadeLiquidity {
				curTop = nan()
			}
		}
		if botTouch {
			curBot = L[i]
			ageBot = 0
			indexBot = i
			profBot = L[i]
		} else {
			ageBot++
			if ageBot >= p.FadeLiquidity {
				curBot = nan()
			}
		}

		res.PerBar[i] = SmartMoneyBar{
			Time: closed[i].OpenTime,
			LiqTop: fNF(curTop), LiqBottom: fNF(curBot),
			TopSweep: topTouch, BottomSweep: botTouch,
		}
	}

	prof := SmartMoneyProfile{
		IndexTop: indexTop, IndexBot: indexBot,
		ProfTop: fNF(profTop), ProfBot: fNF(profBot), BinSize: fNF(binSize),
		POCIndex: -1, POCPrice: naF(), MidPrice: naF(),
	}

	start := -1
	switch {
	case indexTop >= 0 && indexBot >= 0:
		start = indexTop
		if indexBot < start {
			start = indexBot
		}
	case indexTop >= 0:
		start = indexTop
	case indexBot >= 0:
		start = indexBot
	}
	prof.StartBar = start

	if start >= 0 && start < n && !isNa(profTop) && !isNa(profBot) && profTop > profBot && !isNa(binSize) && binSize > 0 {
		size := int(math.Round((profTop - profBot) / binSize))
		if size < 1 {
			size = 1
		}
		if p.MaxProfileBins > 0 && size > p.MaxProfileBins {
			size = p.MaxProfileBins
		}
		bins := make([]float64, size)
		maxVol := 0.0
		for j := start; j < n; j++ {
			binIdx := int(math.Floor((closes[j] - profBot) / binSize))
			if binIdx >= 0 && binIdx < size {
				bins[binIdx] += vols[j]
				if bins[binIdx] > maxVol {
					maxVol = bins[binIdx]
				}
			}
		}
		prof.Bins = bins
		prof.MaxVol = maxVol
		prof.MidPrice = fNF(profBot + (profTop-profBot)/2.0)
		if maxVol > 0 {
			for k, v := range bins {
				if v == maxVol {
					prof.POCIndex = k
					break
				}
			}
			if p.ShowPOC && prof.POCIndex >= 0 {
				prof.POCPrice = fNF(profBot + (float64(prof.POCIndex)+0.5)*binSize)
			}
			prof.Valid = true
		}
	}

	res.Profile = prof
	return res
}