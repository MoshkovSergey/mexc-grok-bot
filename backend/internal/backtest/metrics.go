package backtest

import (
	"math"
	"sort"
	"time"

	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/strategy"
)

// Metrics is the summary statistics of a run. Fields that can legitimately be NaN
// (undefined) or Inf (no losing trades) are StatFloat so the response always
// serializes; the rest are finite by construction and stay float64.
type Metrics struct {
	NumTrades       int       `json:"numTrades"`
	WinRate         float64   `json:"winRate"`      // 0..1, finite
	ProfitFactor    StatFloat `json:"profitFactor"` // grossProfit/grossLoss; inf if no losses; null if no trades
	ExpectancyNet   float64   `json:"expectancyNet"`
	MeanNetPnL      float64   `json:"meanNetPnl"`
	MedianNetPnL    StatFloat `json:"medianNetPnl"` // null when no trades
	MaxDrawdownPct  float64   `json:"maxDrawdownPct"`
	TotalReturnPct  float64   `json:"totalReturnPct"`
	FinalEquity     float64   `json:"finalEquity"`
	TotalFees       float64   `json:"totalFees"`
	TotalSlippage   float64   `json:"totalSlippage"`
	CostDragPct     float64   `json:"costDragPct"`
	GrossProfit     float64   `json:"grossProfit"`
	GrossLoss       float64   `json:"grossLoss"`

	SharpePerBar    StatFloat `json:"sharpePerBar"`
	SortinoPerBar   StatFloat `json:"sortinoPerBar"`
	SharpeAnnual    StatFloat `json:"sharpeAnnual"`
	SortinoAnnual   StatFloat `json:"sortinoAnnual"`
	BarsPerYear     StatFloat `json:"barsPerYear"`
}

// ComputeMetrics derives all statistics from closed trades + equity curve.
func ComputeMetrics(trades []Trade, equity []EquityPoint, startEquity float64, interval string) Metrics {
	m := Metrics{}
	if startEquity <= 0 {
		startEquity = 1
	}

	var wins, losses int
	var sumNet, grossProfit, grossLoss, totalFees, totalSlip float64
	netList := make([]float64, 0, len(trades))
	for _, t := range trades {
		netList = append(netList, t.NetPnL)
		sumNet += t.NetPnL
		totalFees += t.Fees
		totalSlip += t.Slippage
		if t.GrossPnL > 0 {
			grossProfit += t.GrossPnL
		} else {
			grossLoss += -t.GrossPnL
		}
		if t.NetPnL > 0 {
			wins++
		} else if t.NetPnL < 0 {
			losses++
		}
	}
	m.NumTrades = len(trades)
	m.TotalFees = totalFees
	m.TotalSlippage = totalSlip
	m.GrossProfit = grossProfit
	m.GrossLoss = grossLoss
	m.CostDragPct = (totalFees + totalSlip) / startEquity

	if m.NumTrades > 0 {
		m.WinRate = float64(wins) / float64(m.NumTrades)
		m.ExpectancyNet = sumNet / float64(m.NumTrades)
		m.MeanNetPnL = m.ExpectancyNet
		m.MedianNetPnL = StatFloat(median(netList))
		switch {
		case grossLoss > 0:
			m.ProfitFactor = StatFloat(grossProfit / grossLoss)
		case grossProfit > 0:
			m.ProfitFactor = StatFloat(math.Inf(1))
		default:
			m.ProfitFactor = StatFloat(math.NaN())
		}
	} else {
		m.ProfitFactor = StatFloat(math.NaN())
		m.MedianNetPnL = StatFloat(math.NaN())
	}

	if len(equity) > 0 {
		final := equity[len(equity)-1].Equity
		m.FinalEquity = final
		m.TotalReturnPct = final/startEquity - 1
		m.MaxDrawdownPct = maxDrawdown(equity)

		rets := make([]float64, 0, len(equity)-1)
		for i := 1; i < len(equity); i++ {
			prev := equity[i-1].Equity
			if prev != 0 {
				rets = append(rets, equity[i].Equity/prev-1)
			}
		}
		if len(rets) > 1 {
			mu, sd, dsd := meanStdDownside(rets)
			m.SharpePerBar = StatFloat(safeDiv(mu, sd))
			m.SortinoPerBar = StatFloat(safeDiv(mu, dsd))
			bpy := barsPerYear(interval)
			m.BarsPerYear = StatFloat(bpy)
			if !math.IsNaN(bpy) && bpy > 0 {
				ann := math.Sqrt(bpy)
				m.SharpeAnnual = StatFloat(m.SharpePerBar.Float() * ann)
				m.SortinoAnnual = StatFloat(m.SortinoPerBar.Float() * ann)
			}
		}
	}
	return m
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	cp := append([]float64(nil), xs...)
	sort.Float64s(cp)
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}

func maxDrawdown(eq []EquityPoint) float64 {
	peak := math.Inf(-1)
	mdd := 0.0
	for _, p := range eq {
		if p.Equity > peak {
			peak = p.Equity
		}
		if peak > 0 {
			dd := (peak - p.Equity) / peak
			if dd > mdd {
				mdd = dd
			}
		}
	}
	return mdd
}

func meanStdDownside(xs []float64) (mu, sd, dsd float64) {
	n := float64(len(xs))
	if n == 0 {
		return math.NaN(), math.NaN(), math.NaN()
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mu = sum / n
	var v, dv float64
	for _, x := range xs {
		d := x - mu
		v += d * d
		if x < 0 {
			dv += x * x
		}
	}
	sd = math.Sqrt(v / n)
	dsd = math.Sqrt(dv / n)
	return mu, sd, dsd
}

func safeDiv(a, b float64) float64 {
	if b == 0 || math.IsNaN(a) || math.IsNaN(b) {
		return math.NaN()
	}
	return a / b
}

func barsPerYear(interval string) float64 {
	dur, ok := strategy.IntervalSeconds(interval)
	if !ok || dur <= 0 {
		return math.NaN()
	}
	const year = 365 * 24 * time.Hour
	return float64(year) / float64(dur)
}