package backtest

import (
	"math"

	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/strategy"
)

// SensRow is one grid point. PF is StatFloat because a point with zero trades has an
// undefined profit factor (NaN) and a point with only winning trades has +Inf.
type SensRow struct {
	Label  string    `json:"label"`
	NetRet float64   `json:"netReturnPct"`
	PF     StatFloat `json:"profitFactor"`
	Trades int       `json:"numTrades"`
	MaxDD  float64   `json:"maxDrawdownPct"`
}

// SensSummary aggregates the grid to separate reproducible edge from overfitting.
type SensSummary struct {
	Count        int       `json:"count"`
	MeanNet      StatFloat `json:"meanNetPct"`
	MedianNet    StatFloat `json:"medianNetPct"`
	MinNet       StatFloat `json:"minNetPct"`
	MaxNet       StatFloat `json:"maxNetPct"`
	CVNet        StatFloat `json:"cvNet"`
	MeanPF       StatFloat `json:"meanProfitFactor"`
	MedianPF     StatFloat `json:"medianProfitFactor"`
	FracPFgt1    float64   `json:"fracProfitFactorGt1"`
	FracTradesOk float64   `json:"fracTradesGeMin"`
	BaseNet      StatFloat `json:"baseNetPct"`
	BasePF       StatFloat `json:"baseProfitFactor"`
	Spike        bool      `json:"spikeSuspected"`
}

// SensGrid runs a parameter sweep around baseCfg and returns rows + summary.
func SensGrid(candles []strategy.Candle, htf []strategy.Candle, baseCfg Config, step, minTrades int) ([]SensRow, SensSummary) {
	var rows []SensRow
	add := func(label string, mutate func(*Config)) {
		c := baseCfg
		mutate(&c)
		r := Run(candles, htf, c)
		rows = append(rows, SensRow{
			Label:  label,
			NetRet: r.Metrics.TotalReturnPct * 100,
			PF:     r.Metrics.ProfitFactor,
			Trades: r.Metrics.NumTrades,
			MaxDD:  r.Metrics.MaxDrawdownPct * 100,
		})
	}

	if baseCfg.Source == "cts" {
		base := strategy.DefaultParams()
		if baseCfg.CTSParams != nil {
			base = *baseCfg.CTSParams
		}
		withParam := func(name string, adjust func(*strategy.Params)) {
			p := base
			adjust(&p)
			pc := p
			add(name, func(c *Config) { c.CTSParams = &pc })
		}
		for _, d := range []int{-step, -1, 1, step} {
			withParam("tsLen"+itoa(d), func(p *strategy.Params) { p.TSLen = maxInt(1, p.TSLen+d) })
			withParam("tsSigLen"+itoa(d), func(p *strategy.Params) { p.TSsigLen = maxInt(1, p.TSsigLen+d) })
			withParam("tsNormLen"+itoa(d), func(p *strategy.Params) { p.TSnormLen = maxInt(2, p.TSnormLen+d*5) })
			withParam("swingLen"+itoa(d), func(p *strategy.Params) { p.SwingLen = clampInt(p.SwingLen+d, 1, 50) })
			withParam("tolerance"+itoa(d), func(p *strategy.Params) { p.Tolerance = clampF(p.Tolerance+float64(d)*0.05, 0, 1) })
			withParam("emaSlowLen"+itoa(d), func(p *strategy.Params) { p.EMASlowLen = maxInt(2, p.EMASlowLen+d*5) })
		}
		add("base", func(c *Config) {})
	} else {
		for df := -step; df <= step; df++ {
			for ds := -step; ds <= step; ds++ {
				f := baseCfg.FastPeriod + df
				s := baseCfg.SlowPeriod + ds
				if f < 1 || s < 2 || f >= s {
					continue
				}
				ff, ss := f, s
				add("fast="+itoa(f)+",slow="+itoa(s), func(c *Config) { c.FastPeriod = ff; c.SlowPeriod = ss })
			}
		}
	}

	sum := summarize(rows, baseCfg, minTrades)
	return rows, sum
}

func summarize(rows []SensRow, baseCfg Config, minTrades int) SensSummary {
	s := SensSummary{Count: len(rows)}
	if len(rows) == 0 {
		return s
	}
	nets := make([]float64, 0, len(rows))
	pfs := make([]float64, 0, len(rows))
	var pfGt1, tradesOk int
	for _, r := range rows {
		if !math.IsNaN(r.NetRet) && !math.IsInf(r.NetRet, 0) {
			nets = append(nets, r.NetRet)
		}
		pfv := r.PF.Float()
		if !math.IsNaN(pfv) && !math.IsInf(pfv, 0) {
			pfs = append(pfs, pfv)
			if pfv > 1 {
				pfGt1++
			}
		}
		if r.Trades >= minTrades {
			tradesOk++
		}
	}
	s.MeanNet = StatFloat(mean(nets))
	s.MedianNet = StatFloat(median(nets))
	mn, mx := minMax(nets)
	s.MinNet = StatFloat(mn)
	s.MaxNet = StatFloat(mx)
	if len(nets) > 1 {
		_, sd, _ := meanStdDownside(nets)
		if s.MeanNet.Float() != 0 {
			s.CVNet = StatFloat(sd / math.Abs(s.MeanNet.Float()))
		}
	}
	s.MeanPF = StatFloat(mean(pfs))
	s.MedianPF = StatFloat(median(pfs))
	s.FracPFgt1 = float64(pfGt1) / float64(len(rows))
	s.FracTradesOk = float64(tradesOk) / float64(len(rows))

	for _, r := range rows {
		if r.Label == "base" {
			s.BaseNet = StatFloat(r.NetRet)
			s.BasePF = r.PF
			break
		}
	}
	if baseCfg.Source != "cts" {
		want := "fast=" + itoa(baseCfg.FastPeriod) + ",slow=" + itoa(baseCfg.SlowPeriod)
		for _, r := range rows {
			if r.Label == want {
				s.BaseNet = StatFloat(r.NetRet)
				s.BasePF = r.PF
				break
			}
		}
	}
	if len(nets) > 2 && s.BaseNet.Float() != 0 {
		_, sd, _ := meanStdDownside(nets)
		maxNeighbor := math.Inf(-1)
		for _, r := range rows {
			if r.Label == "base" {
				continue
			}
			if r.NetRet > maxNeighbor {
				maxNeighbor = r.NetRet
			}
		}
		if !math.IsInf(maxNeighbor, 0) && sd > 0 && s.BaseNet.Float() > maxNeighbor+sd {
			s.Spike = true
		}
	}
	return s
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func minMax(xs []float64) (float64, float64) {
	if len(xs) == 0 {
		return math.NaN(), math.NaN()
	}
	mn, mx := xs[0], xs[0]
	for _, x := range xs[1:] {
		if x < mn {
			mn = x
		}
		if x > mx {
			mx = x
		}
	}
	return mn, mx
}

func itoa(i int) string {
	if i >= 0 {
		return "+" + strconvItoa(i)
	}
	return "-" + strconvItoa(-i)
}

// strconvItoa avoids importing strconv here just for small grid labels.
func strconvItoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [8]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}