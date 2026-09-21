package strategy

import "math"

// ComputeCTS runs the full CTS-CISD indicator over CONFIRMED candles and returns
// per-bar states plus the final composite signal on the last bar. See package doc
// for the faithful-port caveat and the non-repaint contract.
func ComputeCTS(closed []Candle, htfClosed []Candle, p Params) Result {
	res := Result{PerBar: make([]BarState, len(closed))}
	n := len(closed)
	if n == 0 {
		return res
	}

	closeS := make([]float64, n)
	for i := range closed {
		closeS[i] = closed[i].Close
	}
	emaFast := EMA(closeS, p.EMAFastLen)
	emaSlow := EMA(closeS, p.EMASlowLen)
	rsiS := RSI(closeS, p.RSILen)
	atrS := ATR(closed, p.ATRLen)
	volR := VolRatio(closed, p.VolLen)

	atrPct := make([]float64, n)
	for i := range atrPct {
		if isNa(atrS[i]) || closed[i].Close == 0 {
			atrPct[i] = nan()
		} else {
			atrPct[i] = atrS[i] / closed[i].Close * 100.0
		}
	}

	htfEmaAt := mapHTFEma(closed, htfClosed, p.HTFLen)

	src := PriceSource(closed, p.SrcType)
	tsV1 := FMA(FMA(FMA(src, p.TSLen, p.TSmat), p.TSLen, p.TSmat), p.TSLen, p.TSmat)
	tsV2 := FMA(tsV1, p.TSsigLen, p.TSsigMat)
	tsDist := make([]float64, n)
	for i := range tsDist {
		if isNa(tsV2[i]) || isNa(tsV1[i]) {
			tsDist[i] = nan()
		} else {
			tsDist[i] = tsV2[i] - tsV1[i]
		}
	}
	tsLo := RollingLowest(tsDist, p.TSnormLen)
	tsHi := RollingHighest(tsDist, p.TSnormLen)
	tsSymDist := make([]float64, n)
	for i := range tsSymDist {
		if isNa(tsHi[i]) || isNa(tsLo[i]) {
			tsSymDist[i] = nan()
			continue
		}
		absRng := math.Max(math.Abs(tsHi[i]), math.Abs(tsLo[i]))
		if absRng == 0 {
			tsSymDist[i] = 0
		} else {
			tsSymDist[i] = tsDist[i] / absRng
		}
	}

	st := &cisdState{
		trend:           0,
		lastWickHigh:    nan(),
		lastWickHighBar: -1,
		lastWickLow:     nan(),
		lastWickLowBar:  -1,
	}

	longSig := make([]bool, n)
	shortSig := make([]bool, n)

	for i := 0; i < n; i++ {
		confirmed := true // iterating only confirmed candles; mirrors the Pine gate

		k := i - p.SwingLen
		if k >= 0 {
			ph, pl := pivotAt(closed, k, p.SwingLen)
			if !isNa(ph) {
				st.swingHigh = append(st.swingHigh, swingLine{level: ph, x1: k})
			}
			if !isNa(pl) {
				st.swingLow = append(st.swingLow, swingLine{level: pl, x1: k})
			}
		}

		wickedHigh := false
		wickedLow := false
		st.swingHigh = filterSwings(st.swingHigh, i, p.ExpiryBars, closed[i].High, true, &wickedHigh, &st.lastWickHigh, &st.lastWickHighBar)
		st.swingLow = filterSwings(st.swingLow, i, p.ExpiryBars, closed[i].Low, false, &wickedLow, &st.lastWickLow, &st.lastWickLowBar)

		if i >= 1 {
			prev := closed[i-1]
			cur := closed[i]
			if prev.Close < prev.Open && cur.Close > cur.Open {
				rt := RunTop(closed, i, p.MaxScan)
				if !isNa(rt) && rt > cur.Open {
					st.bearLvl = append(st.bearLvl, pending{level: cur.Open, bar: i, extreme: rt})
					if len(st.bearLvl) > p.MaxPending {
						st.bearLvl = st.bearLvl[len(st.bearLvl)-p.MaxPending:]
					}
				}
			}
			if prev.Close > prev.Open && cur.Close < cur.Open {
				rb := RunBottom(closed, i, p.MaxScan)
				if !isNa(rb) && rb < cur.Open {
					st.bullLvl = append(st.bullLvl, pending{level: cur.Open, bar: i, extreme: rb})
					if len(st.bullLvl) > p.MaxPending {
						st.bullLvl = st.bullLvl[len(st.bullLvl)-p.MaxPending:]
					}
				}
			}
		}

		cisdBear := false
		cisdBull := false
		st.bearLvl, cisdBear = processPendingBear(st.bearLvl, closed, i, p)
		if !cisdBear {
			st.bullLvl, cisdBull = processPendingBull(st.bullLvl, closed, i, p)
		}
		if cisdBear {
			st.trend = -1
		} else if cisdBull {
			st.trend = 1
		}

		bearSweep := false
		bullSweep := false
		if cisdBear && st.lastWickHighBar >= 0 && (i-st.lastWickHighBar) <= p.LiquidityLookback && !isNa(st.lastWickHigh) && closed[i].Close < st.lastWickHigh {
			bearSweep = true
		}
		if cisdBull && st.lastWickLowBar >= 0 && (i-st.lastWickLowBar) <= p.LiquidityLookback && !isNa(st.lastWickLow) && closed[i].Close > st.lastWickLow {
			bullSweep = true
		}

		tsBullRaw := Crossunder(tsV2, tsV1, i)
		tsBearRaw := Crossover(tsV2, tsV1, i)
		tsBull := tsBullRaw && (!p.ConfirmOnClose || confirmed)
		tsBear := tsBearRaw && (!p.ConfirmOnClose || confirmed)

		emaBull := !isNa(emaFast[i]) && !isNa(emaSlow[i]) && emaFast[i] > emaSlow[i]
		emaBear := !isNa(emaFast[i]) && !isNa(emaSlow[i]) && emaFast[i] < emaSlow[i]
		volumeOK := !p.UseVolumeFilter || (!isNa(volR[i]) && volR[i] >= p.VolumeMult)
		rsiLongOK := !p.UseRSIFilter || (!isNa(rsiS[i]) && rsiS[i] > p.RSIMid && rsiS[i] < p.RSIOverbought)
		rsiShortOK := !p.UseRSIFilter || (!isNa(rsiS[i]) && rsiS[i] < p.RSIMid && rsiS[i] > p.RSIOversold)
		atrOK := !p.UseATRFIlter || isNa(atrPct[i]) || (atrPct[i] >= p.MinATRPct && atrPct[i] <= p.MaxATRPct)
		htfVal := htfEmaAt[i]
		htfLongOK := !p.UseHTFFilter || (!isNa(htfVal) && closed[i].Close > htfVal)
		htfShortOK := !p.UseHTFFilter || (!isNa(htfVal) && closed[i].Close < htfVal)
		emaLongOK := !p.UseEMAFilter || emaBull
		emaShortOK := !p.UseEMAFilter || emaBear

		longFilters := volumeOK && rsiLongOK && atrOK && htfLongOK && emaLongOK
		shortFilters := volumeOK && rsiShortOK && atrOK && htfShortOK && emaShortOK

		var longTrig, shortTrig bool
		switch p.SignalMode {
		case "Any":
			longTrig = tsBull || cisdBull
			shortTrig = tsBear || cisdBear
		case "Triple Smoothed only":
			longTrig = tsBull
			shortTrig = tsBear
		case "CISD only":
			longTrig = cisdBull
			shortTrig = cisdBear
		default: // Confluence
			longTrig = tsBull && cisdBull
			shortTrig = tsBear && cisdBear
		}
		longSig[i] = longTrig && longFilters
		shortSig[i] = shortTrig && shortFilters

		res.PerBar[i] = BarState{
			Time: closed[i].OpenTime,
			EMAFast: fNF(emaFast[i]), EMASlow: fNF(emaSlow[i]),
			RSI: fNF(rsiS[i]), ATRPct: fNF(atrPct[i]), VolRatio: fNF(volR[i]), HTFEma: fNF(htfVal),
			TSV1: fNF(tsV1[i]), TSV2: fNF(tsV2[i]), TSSymDist: fNF(tsSymDist[i]),
			TSBull: tsBull, TSBear: tsBear,
			CISDBull: cisdBull, CISDBear: cisdBear,
			BullSweep: bullSweep, BearSweep: bearSweep,
			EMALongOK: emaLongOK, EMAShortOK: emaShortOK,
			VolumeOK: volumeOK, RSILongOK: rsiLongOK, RSIShortOK: rsiShortOK,
			ATROK: atrOK, HTFLongOK: htfLongOK, HTFShortOK: htfShortOK,
			LongFilters: longFilters, ShortFilters: shortFilters,
			LongSignal: longSig[i], ShortSignal: shortSig[i],
		}
	}

	last := n - 1
	res.LongSignal = longSig[last]
	res.ShortSignal = shortSig[last]
	switch {
	case longSig[last] && !shortSig[last]:
		res.Side = "LONG"
	case shortSig[last] && !longSig[last]:
		res.Side = "SHORT"
	}
	res.CISDTrend = st.trend
	res.LastWickHigh = fNF(st.lastWickHigh)
	res.LastWickLow = fNF(st.lastWickLow)
	need := p.TSnormLen + p.SwingLen + p.MaxScan + 1
	if p.ExpiryBars+1 > need {
		need = p.ExpiryBars + 1
	}
	res.Progressed = n >= need
	return res
}

func filterSwings(in []swingLine, i, expiry int, price float64, isHigh bool, wicked *bool, lastWick *float64, lastWickBar *int) []swingLine {
	out := in[:0]
	for _, l := range in {
		age := i - l.x1
		if age >= expiry {
			continue
		}
		hit := false
		if isHigh && price >= l.level {
			hit = true
		}
		if !isHigh && price <= l.level {
			hit = true
		}
		if hit {
			*wicked = true
			*lastWick = l.level
			*lastWickBar = i
			continue
		}
		out = append(out, l)
	}
	return out
}

func processPendingBear(in []pending, c []Candle, i int, p Params) ([]pending, bool) {
	out := in[:0]
	for _, e := range in {
		age := i - e.bar
		if age >= p.ExpiryBars {
			continue
		}
		if age > 0 && c[i].Close < e.level && e.extreme > e.level {
			highSince := HighestCloseIn(c, e.bar, i)
			denom := e.extreme - e.level
			if !isNa(highSince) && denom > 0 {
				ratio := (highSince - e.level) / denom
				if ratio > p.Tolerance {
					return nil, true
				}
			}
		}
		out = append(out, e)
	}
	return out, false
}

func processPendingBull(in []pending, c []Candle, i int, p Params) ([]pending, bool) {
	out := in[:0]
	for _, e := range in {
		age := i - e.bar
		if age >= p.ExpiryBars {
			continue
		}
		if age > 0 && c[i].Close > e.level && e.extreme < e.level {
			lowSince := LowestCloseIn(c, e.bar, i)
			denom := e.level - e.extreme
			if !isNa(lowSince) && denom > 0 {
				ratio := (e.level - lowSince) / denom
				if ratio > p.Tolerance {
					return nil, true
				}
			}
		}
		out = append(out, e)
	}
	return out, false
}

func mapHTFEma(closed []Candle, htf []Candle, htfLen int) []float64 {
	out := make([]float64, len(closed))
	for i := range out {
		out[i] = nan()
	}
	if len(htf) == 0 || htfLen <= 0 {
		return out
	}
	htfClose := HTFCloseSeries(htf)
	ema := EMA(htfClose, htfLen)
	hi := 0
	for i := 0; i < len(closed); i++ {
		for hi+1 < len(htf) && htf[hi+1].OpenTime.Before(closed[i].OpenTime) {
			hi++
		}
		idx := hi - 1
		if idx >= 0 && idx < len(ema) {
			out[i] = ema[idx]
		}
	}
	return out
}