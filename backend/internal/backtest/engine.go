package backtest

import (
	"fmt"
	"math"
	"time"

	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/strategy"
)

// ExecutionMode selects the fill price relative to the signal bar.
type ExecutionMode int

const (
	// NextBarOpen executes on the OPEN of the bar AFTER the signal bar, with a
	// symmetric spread/slippage penalty. This is the HONEST default: a signal
	// confirmed at close[N] cannot be filled at close[N].
	NextBarOpen ExecutionMode = iota
	// SameBarClose executes at close[N] (mirrors the live paper engine). Exposed
	// ONLY to quantify the execution-lag bias; never use for go/no-go decisions.
	SameBarClose
)

// Config parameterizes a single backtest run. It intentionally mirrors the live
// engine's sizing and risk rules so the backtest measures the SAME policy.
type Config struct {
	Source         string  // "sma" | "cts"
	Interval       string  // primary candle interval (for bars/year + loader cut)
	FastPeriod     int     // SMA
	SlowPeriod     int     // SMA
	MaxPositionPct float64 // fixed-fraction sizing, same as live
	MaxDrawdownPct float64 // kill-switch, same as live
	StartEquity    float64
	Fee            FeeModel
	Execution      ExecutionMode
	CTSParams      *strategy.Params // nil => strategy.DefaultParams()
}

// Trade is one closed round-trip (flat -> ... -> flat). Open positions at end-of-data
// are NOT emitted as trades (they live only in the equity curve) and are flagged.
type Trade struct {
	EntryTime  time.Time `json:"entryTime"`
	ExitTime   time.Time `json:"exitTime"`
	Side       string    `json:"side"`       // always "LONG" (spot long-only)
	EntryPrice float64   `json:"entryPrice"` // volume-weighted base entry (pre-slippage)
	ExitPrice  float64   `json:"exitPrice"`  // base exit (pre-slippage)
	Qty        float64   `json:"qty"`
	GrossPnL   float64   `json:"grossPnl"` // before fees & slippage
	Fees       float64   `json:"fees"`
	Slippage   float64   `json:"slippage"`
	NetPnL     float64   `json:"netPnl"`
	ReturnPct  float64   `json:"returnPct"` // net / equity-at-round-start
	BarsHeld   int       `json:"barsHeld"`
	ExitReason string    `json:"exitReason"` // "signal" | "risk_stop"
}

// EquityPoint is a mark-to-market snapshot at a bar close.
type EquityPoint struct {
	Time   time.Time `json:"time"`
	Equity float64   `json:"equity"`
}

// Result is the full output of a run.
type Result struct {
	Config      Config        `json:"config"`
	Fee         FeeModel      `json:"fee"`
	Assumptions []string      `json:"assumptions"`
	Trades      []Trade       `json:"trades"`
	Equity      []EquityPoint `json:"equityCurve"`
	Metrics     Metrics       `json:"metrics"`
	RiskStopped bool          `json:"riskStopped"`
	RiskStopAt  *time.Time    `json:"riskStopAt,omitempty"`
	OpenAtEnd   bool          `json:"openAtEnd"` // a position was still open at end-of-data
	NumBars     int           `json:"numBars"`
}

// round accumulates an open position's cost basis and incurred costs.
type round struct {
	active     bool
	entryTime  time.Time
	baseQty    float64 // total bought qty (for weighted base entry)
	costBasis  float64 // sum(qty_i * basePrice_i)
	qty        float64 // current open qty
	fees       float64
	slippage   float64
	grossRealz float64 // realized gross from sells
	eqAtStart  float64
	bars       int
}

// Run replays signals over confirmed candles with explicit costs and next-bar fills.
// candles MUST be confirmed (closed) and ascending; htf is the HTF series for CTS
// (may be empty -> CTS HTF filter rejects all signals, which is reported honestly).
func Run(candles []strategy.Candle, htf []strategy.Candle, cfg Config) Result {
	res := Result{Config: cfg, Fee: cfg.Fee, NumBars: len(candles)}
	res.Assumptions = append(res.Assumptions, cfg.Fee.Assumptions()...)

	// Drop malformed bars (non-positive prices) before any arithmetic, otherwise a
	// single bad candle in exchange_candles would produce Inf in qty/equity and crash
	// serialization downstream. Works on a NEW slice so the caller's candles (reused
	// by SensGrid across runs) are never mutated.
	candles = filterValidCandles(candles, &res.Assumptions)

	if len(candles) < 2 || cfg.StartEquity <= 0 {
		res.Assumptions = append(res.Assumptions, "недостаточно свечей или ненулевой стартовый капитал; расчёт не выполнялся")
		return res
	}
	if cfg.Source == "cts" && len(htf) == 0 {
		res.Assumptions = append(res.Assumptions, "прогон CTS с ПУСТОЙ HTF‑серией: старший трендовый фильтр (useHtfFilter) отбраковывает каждый сигнал → 0 сделок. Загрузите HTF‑историю (4h), чтобы оценить CTS.")
	}

	// 1) Build a causal per-bar signal array: +1 BUY, -1 SELL, 0 none.
	signals := buildSignals(candles, htf, cfg)

	// 2) Replay with sizing + kill-switch identical to the live engine.
	const eps = 1e-12
	feeRate := cfg.Fee.TakerFeeBps / 1e4
	slipRate := cfg.Fee.SlippageBps / 1e4

	cash := cfg.StartEquity
	var r round
	peak := cash
	var eq []EquityPoint
	var trades []Trade
	riskStopped := false
	var riskStopAt *time.Time

	for i := 0; i < len(candles); i++ {
		bar := candles[i]

		// Mark-to-market equity at this bar's close (no slippage on valuation).
		mark := bar.Close
		equityNow := cash + r.qty*mark
		if equityNow > peak {
			peak = equityNow
		}
		eq = append(eq, EquityPoint{Time: bar.OpenTime, Equity: equityNow})

		if r.active {
			r.bars++
		}

		// Kill-switch: same rule as live (drawdown from peak >= limit => flatten + stop).
		if !riskStopped && peak > 0 {
			dd := (peak - equityNow) / peak
			if dd >= cfg.MaxDrawdownPct {
				// Flatten at this bar's execution price (risk exits are market too).
				if r.qty > eps {
					exitBase := execBase(candles, i, cfg.Execution)
					doSell(&r, &cash, exitBase, feeRate, slipRate, bar.OpenTime, "risk_stop", &trades)
				}
				riskStopped = true
				t := bar.OpenTime
				riskStopAt = &t
				res.Assumptions = append(res.Assumptions, "сработал kill‑switch по максимальной просадке; торговля остановлена до конца окна (повторяет поведение живого движка)") // continue building equity curve flat (cash only)
				continue
			}
		}

		if riskStopped {
			continue // no new entries after halt
		}

		// Act on the signal from bar i, filling at bar i+1 open (next-bar) or i close.
		sig := signals[i]
		if sig == 0 {
			continue
		}
		fillIdx := i
		if cfg.Execution == NextBarOpen {
			fillIdx = i + 1
			if fillIdx >= len(candles) {
				// Signal on the last bar cannot be executed within the window.
				res.Assumptions = append(res.Assumptions, "сигнал на последнем баре отброшен (нет следующего бара для исполнения); это корректное поведение next‑bar")
				continue
			}
		}
		fillBase := execBase(candles, fillIdx, cfg.Execution)
		fillTime := candles[fillIdx].OpenTime

		if sig > 0 { // BUY / enter-or-add
			equityForSizing := cash + r.qty*fillBase
			target := equityForSizing * cfg.MaxPositionPct
			curVal := r.qty * fillBase
			delta := target - curVal
			if delta <= eps {
				continue
			}
			qty := delta / fillBase
			notional := qty * fillBase
			if notional > cash+eps {
				qty = cash / fillBase
				notional = qty * fillBase
			}
			if qty <= eps {
				continue
			}
			if !r.active {
				r = round{active: true, entryTime: fillTime, eqAtStart: equityForSizing}
			}
			doBuy(&r, &cash, qty, fillBase, feeRate, slipRate)
		} else { // SELL / exit all
			if r.qty <= eps {
				continue
			}
			doSell(&r, &cash, fillBase, feeRate, slipRate, fillTime, "signal", &trades)
		}
	}

	// End-of-data: an open position is NOT force-closed (would add fictitious exit
	// costs); it is reflected in the equity curve and flagged.
	if r.active && r.qty > eps {
		res.OpenAtEnd = true
		res.Assumptions = append(res.Assumptions, "на конце данных оставалась открытая LONG‑позиция; исключена из статистики закрытых сделок, учтена только в кривой equity")
	}

	res.Trades = trades
	res.Equity = eq
	res.RiskStopped = riskStopped
	res.RiskStopAt = riskStopAt
	res.Metrics = ComputeMetrics(trades, eq, cfg.StartEquity, cfg.Interval)
	return res
}

// execBase returns the BASE (pre-slippage) fill price for a given bar index/mode.
func execBase(candles []strategy.Candle, idx int, mode ExecutionMode) float64 {
	if mode == SameBarClose {
		return candles[idx].Close
	}
	return candles[idx].Open
}

// doBuy applies a buy fill: cash decreases by notional+fee; round cost basis grows.
func doBuy(r *round, cash *float64, qty, base, feeRate, slipRate float64) {
	if base <= 0 || qty <= 0 {
		return
	}
	exec := base * (1 + slipRate) // buy pays up
	notional := qty * exec
	fee := notional * feeRate

	*cash -= notional + fee

	r.baseQty += qty
	r.costBasis += qty * base
	r.qty += qty
	r.fees += fee
	r.slippage += qty * (exec - base)
}

// doSell applies a sell fill and, if the round goes flat, emits a closed Trade.
// Guards against division by zero so Trade never carries Inf/NaN.
func doSell(r *round, cash *float64, base, feeRate, slipRate float64, exitTime time.Time, reason string, trades *[]Trade) {
	if r.qty <= 0 || base <= 0 {
		return
	}
	qty := r.qty
	exec := base * (1 - slipRate) // sell receives down
	notional := qty * exec
	fee := notional * feeRate
	*cash += notional - fee

	wbe := 0.0
	if r.baseQty > 0 {
		wbe = r.costBasis / r.baseQty
	}
	r.grossRealz += qty * (base - wbe)
	r.fees += fee
	r.slippage += qty * (base - exec)
	r.qty = 0

	if reason != "" {
		net := r.grossRealz - r.fees - r.slippage
		ret := 0.0
		if r.eqAtStart > 0 {
			ret = net / r.eqAtStart
		}
		*trades = append(*trades, Trade{
			EntryTime: r.entryTime, ExitTime: exitTime, Side: "LONG",
			EntryPrice: wbe, ExitPrice: base, Qty: r.baseQty,
			GrossPnL: r.grossRealz, Fees: r.fees, Slippage: r.slippage,
			NetPnL: net, ReturnPct: ret, BarsHeld: r.bars, ExitReason: reason,
		})
		r.active = false
	}
}

// buildSignals produces the causal per-bar signal array for the configured source.
func buildSignals(candles []strategy.Candle, htf []strategy.Candle, cfg Config) []int8 {
	n := len(candles)
	sig := make([]int8, n)
	switch cfg.Source {
	case "cts":
		p := cfg.CTSParams
		if p == nil {
			def := strategy.DefaultParams()
			p = &def
		}
		res := strategy.ComputeCTS(candles, htf, *p)
		for i, b := range res.PerBar {
			if i >= n {
				break
			}
			switch {
			case b.LongSignal && !b.ShortSignal:
				sig[i] = 1
			case b.ShortSignal && !b.LongSignal:
				sig[i] = -1
			}
		}
	default: // "sma"
		closes := make([]float64, n)
		for i := range candles {
			closes[i] = candles[i].Close
		}
		fast := strategy.SMA(closes, cfg.FastPeriod)
		slow := strategy.SMA(closes, cfg.SlowPeriod)
		for i := 1; i < n; i++ {
			if math.IsNaN(fast[i]) || math.IsNaN(slow[i]) || math.IsNaN(fast[i-1]) || math.IsNaN(slow[i-1]) {
				continue
			}
			switch {
			case fast[i-1] <= slow[i-1] && fast[i] > slow[i]:
				sig[i] = 1
			case fast[i-1] >= slow[i-1] && fast[i] < slow[i]:
				sig[i] = -1
			}
		}
	}
	return sig
}

// filterValidCandles returns a new slice containing only bars with strictly positive
// OHLC (and High>=Low sanity). It appends an assumption when bars are dropped. The
// input slice is never mutated, so repeated SensGrid runs over the same candles stay
// idempotent.
func filterValidCandles(in []strategy.Candle, assumptions *[]string) []strategy.Candle {
	out := make([]strategy.Candle, 0, len(in))
	dropped := 0
	for _, c := range in {
		if c.Open > 0 && c.High > 0 && c.Low > 0 && c.Close > 0 && c.High >= c.Low {
			out = append(out, c)
		} else {
			dropped++
		}
	}
	if dropped > 0 {
		*assumptions = append(*assumptions,
			fmt.Sprintf("отброшено %d некорректных свечей с неположительными/несогласованными OHLC перед бэктестом", dropped))
	}
	return out
}
