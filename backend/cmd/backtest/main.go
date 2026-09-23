// Command backtest runs the offline, cost-aware backtest over historical candles in
// PostgreSQL and prints headline metrics + (optionally) a parameter-sensitivity grid.
// It NEVER places orders and NEVER mutates bot_state.
//
// Example:
//
//	go run ./cmd/backtest \
//	  --symbol KASUSDT --interval 1m --source cts \
//	  --start 2026-09-01 --end 2026-09-22 \
//	  --taker-bps 8 --slippage-bps 5 \
//	  --max-pos 0.2 --max-dd 0.05 --equity 10000 \
//	  --sensitivity --step 2 --min-trades 30
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/backtest"
	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/config"
	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/mexc"
	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/strategy"
)

func main() {
	var (
		symbol     = flag.String("symbol", envOr("SYMBOL", "BTCUSDT"), "trading pair")
		interval   = flag.String("interval", envOr("INTERVAL", "1m"), "primary candle interval")
		source     = flag.String("source", "sma", `signal source: "sma" | "cts"`)
		startS     = flag.String("start", "", "start time (RFC3339 or YYYY-MM-DD); empty = earliest")
		endS       = flag.String("end", "", "end time; empty = now (forming bar dropped)")
		fast       = flag.Int("fast", 9, "SMA fast period")
		slow       = flag.Int("slow", 21, "SMA slow period")
		maxPos     = flag.Float64("max-pos", 0.20, "max position fraction (0..1]")
		maxDD      = flag.Float64("max-dd", 0.05, "max drawdown kill-switch (0..1]")
		equity     = flag.Float64("equity", 10000, "starting equity (quote ccy)")
		takerBps   = flag.Float64("taker-bps", 0, "taker fee in basis points (0 = optimistic zero, will be flagged)")
		slipBps    = flag.Float64("slippage-bps", 0, "symmetric spread/slippage penalty in basis points")
		execMode   = flag.String("exec", "next", `fill mode: "next" (honest) | "same" (lag-bias demo)`)
		sens       = flag.Bool("sensitivity", false, "also run a parameter-sensitivity grid")
		step       = flag.Int("step", 2, "sensitivity +/- step")
		minTrades  = flag.Int("min-trades", 30, "statistical-relevance floor for sensitivity summary")
		outJSON    = flag.String("out", "", "optional path to write full JSON result")
		crossCheck = flag.Bool("cross-check-fees", false, "if MEXC keys present, fetch raw account commissions for manual scale verification (NOT auto-applied)")
	)
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("конфигурация: %v", err)
	}
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("БД: не удалось открыть соединение: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	start, err := parseT(*startS)
	if err != nil {
		log.Fatalf("параметр --start: %v", err)
	}
	end, err := parseT(*endS)
	if err != nil {
		log.Fatalf("параметр --end: %v", err)
	}

	candles, err := backtest.LoadCandles(ctx, db, *symbol, *interval, start, end)
	if err != nil {
		log.Fatalf("загрузка свечей: %v", err)
	}
	if len(candles) < 50 {
		log.Fatalf("в окне только %d подтверждённых свечей; нужно >=50 (расширьте --start/--end или дождитесь накопления данных)", len(candles))
	}

	var htf []strategy.Candle
	var client *mexc.Client
	if *source == "cts" {
		p := strategy.DefaultParams()
		n, _ := backtest.CountCandles(ctx, db, *symbol, p.HTFTimeframe)
		if n > 0 {
			htf, err = backtest.LoadCandles(ctx, db, *symbol, p.HTFTimeframe, time.Time{}, time.Time{})
			if err != nil {
				log.Fatalf("загрузка HTF: %v", err)
			}
		} else {
			// DB has no HTF history yet -> pull public HTF from the exchange (read-only).
			client = mexc.NewClient(cfg.MexcBaseURL, cfg.MexcAPIKey, cfg.MexcAPISecret)
			raw, e := client.GetKlines(ctx, *symbol, p.HTFTimeframe, p.HTFLen+60)
			if e != nil {
				log.Printf("ВНИМАНИЕ: в БД нет HTF и загрузка с биржи не удалась: %v; CTS даст 0 сделок", e)
			} else {
				htf = toStrategy(raw)
			}
		}
	}

	fee := backtest.ResolveFees(*takerBps, *slipBps)
	if *crossCheck && client != nil {
		for _, a := range backtest.CrossCheckAccount(ctx, client) {
			fmt.Fprintln(os.Stderr, "NOTE:", a)
		}
	} else if *crossCheck {
		client = mexc.NewClient(cfg.MexcBaseURL, cfg.MexcAPIKey, cfg.MexcAPISecret)
		for _, a := range backtest.CrossCheckAccount(ctx, client) {
			fmt.Fprintln(os.Stderr, "NOTE:", a)
		}
	}

	mode := backtest.NextBarOpen
	if *execMode == "same" {
		mode = backtest.SameBarClose
		fmt.Fprintln(os.Stderr, "ПРЕДУПРЕЖДЕНИЕ: --exec same повторяет исполнение живого paper‑движка в том же баре; результаты СМЕЩЕНЫ В ОПТИМИЗМУ. Для решений используйте --exec next.")
	}

	bcfg := backtest.Config{
		Source: *source, Interval: *interval,
		FastPeriod: *fast, SlowPeriod: *slow,
		MaxPositionPct: *maxPos, MaxDrawdownPct: *maxDD,
		StartEquity: *equity, Fee: fee, Execution: mode,
	}
	res := backtest.Run(candles, htf, bcfg)

	printResult(res)

	var sensRows []backtest.SensRow
	var sensSum backtest.SensSummary
	if *sens {
		sensRows, sensSum = backtest.SensGrid(candles, htf, bcfg, *step, *minTrades)
		printSensitivity(sensRows, sensSum)
	}

	if *outJSON != "" {
		dumpJSON(*outJSON, map[string]any{"run": res, "sensitivityRows": sensRows, "sensitivitySummary": sensSum})
		fmt.Fprintf(os.Stderr, "full JSON written to %s\n", *outJSON)
	}
}

func printResult(r backtest.Result) {
	m := r.Metrics
	fmt.Printf("\n=== BACKTEST %s / %s  source=%s  exec=%s ===\n",
		r.Config.Source, r.Config.Interval, r.Config.Source, execName(r.Config.Execution))
	fmt.Printf("bars=%d  trades=%d  riskStopped=%v  openAtEnd=%v\n",
		r.NumBars, m.NumTrades, r.RiskStopped, r.OpenAtEnd)
	fmt.Printf("fees=%s (%.0f bps)  slippage=%s (%.0f bps)\n",
		r.Fee.TakerSource, r.Fee.TakerFeeBps, r.Fee.SlippageSource, r.Fee.SlippageBps)
	fmt.Printf("startEquity=%.2f  finalEquity=%.2f  totalReturn=%.2f%%\n",
		r.Config.StartEquity, m.FinalEquity, m.TotalReturnPct*100)
	fmt.Printf("cost drag=%.2f%% (fees=%.2f + slip=%.2f)\n",
		m.CostDragPct*100, m.TotalFees, m.TotalSlippage)
	fmt.Printf("winRate=%.1f%%  profitFactor=%s  expectancyNet=%.4f  meanNet=%.4f  medianNet=%s\n",
		m.WinRate*100, fmtStat(m.ProfitFactor), m.ExpectancyNet, m.MeanNetPnL, fmtStat(m.MedianNetPnL))
	fmt.Printf("maxDrawdown=%.2f%%  sharpePerBar=%s  sortinoPerBar=%s  (annualized: %s / %s, barsPerYear=%s)\n",
		m.MaxDrawdownPct*100, fmtStat(m.SharpePerBar), fmtStat(m.SortinoPerBar),
		fmtStat(m.SharpeAnnual), fmtStat(m.SortinoAnnual), fmtStat(m.BarsPerYear))
	if len(r.Assumptions) > 0 {
		fmt.Println("--- assumptions / warnings ---")
		for _, a := range r.Assumptions {
			fmt.Println(" *", a)
		}
	}
}

func printSensitivity(rows []backtest.SensRow, s backtest.SensSummary) {
	fmt.Printf("\n=== SENSITIVITY GRID (%d points) ===\n", s.Count)
	fmt.Printf("%-28s %10s %10s %8s %10s\n", "params", "net%", "PF", "trades", "maxDD%")
	for _, r := range rows {
		fmt.Printf("%-28s %10.2f %10s %8d %10.2f\n", r.Label, r.NetRet, fmtStat(r.PF), r.Trades, r.MaxDD)
	}
	fmt.Printf("\nsummary: meanNet=%s medianNet=%s [%s..%s] cvNet=%s\n",
		fmtStat(s.MeanNet), fmtStat(s.MedianNet), fmtStat(s.MinNet), fmtStat(s.MaxNet), fmtStat(s.CVNet))
	fmt.Printf("         meanPF=%s medianPF=%s fracPF>1=%.0f%% fracTrades>=%d=%.0f%%\n",
		fmtStat(s.MeanPF), fmtStat(s.MedianPF), s.FracPFgt1*100, 30, s.FracTradesOk*100)
	fmt.Printf("         baseNet=%s basePF=%s spikeSuspected=%v\n",
		fmtStat(s.BaseNet), fmtStat(s.BasePF), s.Spike)
	fmt.Println("КАК ЧИТАТЬ: низкий fracPF>1 ИЛИ высокий cvNet ИЛИ spike=true => преимущество НЕ УСТОЙЧИВО")
	fmt.Println("к возмущению параметров (вероятно переобучение), независимо от базовой точки.")
}

func execName(m backtest.ExecutionMode) string {
	if m == backtest.SameBarClose {
		return "same-bar-close"
	}
	return "next-bar-open"
}

// fmtStat renders a StatFloat for the console: null->"n/a", inf->"+Inf", -inf->"-Inf",
// else 3 decimals. Replaces the old float64-only fmtNum/isInf/isNaN trio.
func fmtStat(v backtest.StatFloat) string {
	f := v.Float()
	switch {
	case math.IsNaN(f):
		return "n/a"
	case math.IsInf(f, 1):
		return "+Inf"
	case math.IsInf(f, -1):
		return "-Inf"
	default:
		return fmt.Sprintf("%.3f", f)
	}
}

func toStrategy(in []mexc.Kline) []strategy.Candle {
	out := make([]strategy.Candle, len(in))
	for i, k := range in {
		out[i] = strategy.Candle{OpenTime: k.OpenTime, Open: k.Open, High: k.High, Low: k.Low, Close: k.Close, Volume: k.Volume}
	}
	return out
}
func parseT(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
func dumpJSON(path string, v any) {
	f, err := os.Create(path)
	if err != nil {
		log.Printf("out create: %v", err)
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
