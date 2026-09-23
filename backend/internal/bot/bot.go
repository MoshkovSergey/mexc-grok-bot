// Package bot implements a rule-based trading engine with risk controls, hot-reloadable
// runtime settings, pluggable LOCAL signal sources ("sma" | "cts"), and a non-trading
// structural-liquidity overlay (Smart Money / POC port, CC BY-NC-SA 4.0, see strategy).
//
// Default mode: paper trading. Live trading only when ENABLE_LIVE_TRADING=true (env-only).
// Long-only on spot: a CTS SHORT means "exit the long position", never "open a short".
// The Smart Money overlay is OBSERVABILITY ONLY: it never triggers orders and never
// changes risk limits.
package bot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/backtest"
	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/config"
	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/mexc"
	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/strategy"
)

var ValidIntervals = []string{
	"1m", "3m", "5m", "15m", "30m",
	"1h", "2h", "4h", "6h", "8h", "12h",
	"1d", "3d", "1w",
}

var ValidSignalSources = []string{"sma", "cts"}

var (
	ErrLiveSymbolChangeWithPosition = errors.New("нельзя сменить пару при открытой live‑позиции; сначала закройте её вручную")
	ErrNoPriceForAutoClose          = errors.New("нет свежей цены для автозакрытия paper‑позиции перед сменой пары; дождитесь данных или сбросьте состояние")
)

var symbolRe = regexp.MustCompile(`^[A-Z0-9]{2,20}$`)

const indicatorBarsToShow = 250

type RuntimeSettings struct {
	Symbol              string    `json:"symbol"`
	Interval            string    `json:"interval"`
	FastPeriod          int       `json:"fastPeriod"`
	SlowPeriod          int       `json:"slowPeriod"`
	PollSeconds         int       `json:"pollSeconds"`
	MaxPositionPct      float64   `json:"maxPositionPct"`
	MaxDrawdownPct      float64   `json:"maxDrawdownPct"`
	PaperEquity         float64   `json:"paperEquity"`
	LiveOrderValueLimit float64   `json:"liveOrderValueLimit"`
	SignalSource        string    `json:"signalSource"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

type State struct {
	Mode        string    `json:"mode"`
	Status      string    `json:"status"`
	Cash        float64   `json:"cash"`
	PositionQty float64   `json:"positionQty"`
	EntryPrice  float64   `json:"entryPrice"`
	LastSignal  string    `json:"lastSignal"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type OrderRow struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	Symbol    string    `json:"symbol"`
	Side      string    `json:"side"`
	Qty       float64   `json:"qty"`
	Price     float64   `json:"price"`
	Status    string    `json:"status"`
	Note      string    `json:"note"`
}

type SnapshotRow struct {
	ID            int64     `json:"id"`
	CreatedAt     time.Time `json:"createdAt"`
	Equity        float64   `json:"equity"`
	Cash          float64   `json:"cash"`
	PositionValue float64   `json:"positionValue"`
}

type RiskEventRow struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
}

type Dashboard struct {
	Status             string                     `json:"status"`
	Mode               string                     `json:"mode"`
	Symbol             string                     `json:"symbol"`
	Interval           string                     `json:"interval"`
	SignalSource       string                     `json:"signalSource"`
	Running            bool                       `json:"running"`
	LastUpdate         time.Time                  `json:"lastUpdate"`
	Equity             float64                    `json:"equity"`
	Cash               float64                    `json:"cash"`
	PositionQty        float64                    `json:"positionQty"`
	PositionValue      float64                    `json:"positionValue"`
	EntryPrice         float64                    `json:"entryPrice"`
	LastSignal         string                     `json:"lastSignal"`
	PeakEquity         float64                    `json:"peakEquity"`
	MaxDrawdownPct     float64                    `json:"maxDrawdownPct"`
	CurrentDrawdownPct float64                    `json:"currentDrawdownPct"`
	LiveTradingEnabled bool                       `json:"liveTradingEnabled"`
	Candles            []mexc.Kline               `json:"candles"`
	Orders             []OrderRow                 `json:"orders"`
	Snapshots          []SnapshotRow              `json:"snapshots"`
	RiskEvents         []RiskEventRow             `json:"riskEvents"`
	Indicator          []strategy.BarState        `json:"indicator"`
	IndicatorSummary   *strategy.Result           `json:"indicatorSummary"`
	SmartMoney         *strategy.SmartMoneyResult `json:"smartMoney"`
}

type SettingsResponse struct {
	Settings           RuntimeSettings `json:"settings"`
	LiveTradingEnabled bool            `json:"liveTradingEnabled"`
	ValidIntervals     []string        `json:"validIntervals"`
	ValidSignalSources []string        `json:"validSignalSources"`
}

type Bot struct {
	cfg *config.Config
	db  *sql.DB
	mxc *mexc.Client

	mu          sync.Mutex
	running     bool
	state       State
	settings    RuntimeSettings
	peakEquity  float64
	lastUpdate  time.Time
	lastCandles []mexc.Kline
	lastIndic   *strategy.Result
	lastSM      *strategy.SmartMoneyResult
}

func New(cfg *config.Config, database *sql.DB, client *mexc.Client) *Bot {
	b := &Bot{cfg: cfg, db: database, mxc: client}
	ctx := context.Background()
	if err := b.loadSettings(ctx); err != nil {
		log.Printf("load settings failed: %v; falling back to env defaults", err)
		b.settings = settingsFromEnv(cfg)
		b.persistSettings(ctx, b.settings)
	}
	b.loadState(ctx)
	return b
}

func settingsFromEnv(cfg *config.Config) RuntimeSettings {
	return RuntimeSettings{
		Symbol: cfg.Symbol, Interval: cfg.Interval,
		FastPeriod: cfg.FastPeriod, SlowPeriod: cfg.SlowPeriod,
		PollSeconds:    cfg.PollSeconds,
		MaxPositionPct: cfg.MaxPositionPct, MaxDrawdownPct: cfg.MaxDrawdownPct,
		PaperEquity: cfg.PaperEquity, LiveOrderValueLimit: cfg.LiveOrderValueLimit,
		SignalSource: "sma", UpdatedAt: time.Now(),
	}
}

func (b *Bot) Run(ctx context.Context) error {
	if err := b.mxc.SyncTime(ctx); err != nil {
		log.Printf("mexc time sync failed: %v", err)
	}
	if err := b.poll(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("initial poll error: %v", err)
	}
	for {
		s := b.getSettingsCopy()
		sleep := time.Duration(s.PollSeconds) * time.Second
		if sleep < 5*time.Second {
			sleep = 5 * time.Second
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			if err := b.poll(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("poll error: %v", err)
			}
		}
	}
}

func (b *Bot) Start(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state.Status == "risk_stopped" {
		return errors.New("бот остановлен риск‑менеджером; сбросьте bot_state вручную или переразверните с новым пиком")
	}
	b.running = true
	b.state.Status = "running"
	b.state.Mode = b.mode()
	b.persistStateLocked()
	return nil
}

func (b *Bot) Stop(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.running = false
	b.state.Status = "stopped"
	b.persistStateLocked()
	return nil
}

func (b *Bot) GetSettings() SettingsResponse {
	s := b.getSettingsCopy()
	return SettingsResponse{
		Settings: s, LiveTradingEnabled: b.cfg.EnableLiveTrading,
		ValidIntervals:     append([]string(nil), ValidIntervals...),
		ValidSignalSources: append([]string(nil), ValidSignalSources...),
	}
}

func (b *Bot) UpdateSettings(ctx context.Context, incoming RuntimeSettings) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := validateRuntime(incoming); err != nil {
		return err
	}
	old := b.settings
	if incoming.Symbol != old.Symbol {
		if b.state.Mode == "live" && b.state.PositionQty > 1e-12 {
			return ErrLiveSymbolChangeWithPosition
		}
		if b.state.PositionQty > 1e-12 {
			price, ok := b.lastClosePriceLocked(old.Symbol, old.Interval)
			if !ok {
				return ErrNoPriceForAutoClose
			}
			b.state.Cash += b.state.PositionQty * price
			b.recordOrderLocked(old.Symbol, "SELL", b.state.PositionQty, price, "filled", "автозакрытие при смене пары")
			b.state.PositionQty = 0
			b.state.EntryPrice = 0
			b.peakEquity = b.state.Cash
			b.state.LastSignal = "RESET_SYMBOL"
		}
		b.lastCandles = nil
		b.lastIndic = nil
		b.lastSM = nil
	} else if incoming.Interval != old.Interval {
		b.lastCandles = nil
		b.lastIndic = nil
		b.lastSM = nil
	}
	incoming.UpdatedAt = time.Now()
	b.settings = incoming
	b.persistSettingsLocked()
	b.persistStateLocked()
	return nil
}

func (b *Bot) Status() map[string]any {
	b.mu.Lock()
	s := b.settings
	state := b.state
	running := b.running
	lastUpdate := b.lastUpdate
	peak := b.peakEquity
	price := 0.0
	if len(b.lastCandles) > 0 {
		price = b.lastCandles[len(b.lastCandles)-1].Close
	}
	b.mu.Unlock()
	equity := state.Cash + state.PositionQty*price
	drawdown := 0.0
	if peak > 0 {
		drawdown = (peak - equity) / peak
	}
	return map[string]any{
		"status": state.Status, "mode": state.Mode, "running": running,
		"symbol": s.Symbol, "interval": s.Interval, "signalSource": s.SignalSource,
		"equity": equity, "cash": state.Cash, "positionQty": state.PositionQty,
		"entryPrice": state.EntryPrice, "lastSignal": state.LastSignal,
		"peakEquity": peak, "currentDrawdownPct": drawdown, "maxDrawdownPct": s.MaxDrawdownPct,
		"liveTradingEnabled": b.cfg.EnableLiveTrading, "lastUpdate": lastUpdate,
	}
}

func (b *Bot) Dashboard(ctx context.Context) (*Dashboard, error) {
	b.mu.Lock()
	s := b.settings
	state := b.state
	running := b.running
	lastUpdate := b.lastUpdate
	peak := b.peakEquity
	candles := append([]mexc.Kline(nil), b.lastCandles...)
	indic := b.lastIndic
	sm := b.lastSM
	b.mu.Unlock()

	if len(candles) == 0 {
		loaded, err := b.loadRecentCandlesFromDB(ctx, s.Symbol, s.Interval, 100)
		if err != nil {
			return nil, err
		}
		candles = loaded
	}
	orders, err := b.queryOrders(ctx, 20)
	if err != nil {
		return nil, err
	}
	snapshots, err := b.querySnapshots(ctx, 50)
	if err != nil {
		return nil, err
	}
	risks, err := b.queryRiskEvents(ctx, 20)
	if err != nil {
		return nil, err
	}

	price := 0.0
	if len(candles) > 0 {
		price = candles[len(candles)-1].Close
	} else {
		price = state.EntryPrice
	}
	positionValue := state.PositionQty * price
	equity := state.Cash + positionValue
	drawdown := 0.0
	if peak > 0 {
		drawdown = (peak - equity) / peak
	}
	mode := state.Mode
	if mode == "" {
		mode = b.mode()
	}

	var indBars []strategy.BarState
	var indSummary *strategy.Result
	if indic != nil {
		indSummary = &strategy.Result{
			Side: indic.Side, LongSignal: indic.LongSignal, ShortSignal: indic.ShortSignal,
			CISDTrend: indic.CISDTrend, LastWickHigh: indic.LastWickHigh, LastWickLow: indic.LastWickLow,
			Progressed: indic.Progressed,
		}
		if len(indic.PerBar) > indicatorBarsToShow {
			indBars = indic.PerBar[len(indic.PerBar)-indicatorBarsToShow:]
		} else {
			indBars = indic.PerBar
		}
	}

	// Trim Smart Money per-bar to the chart window; keep the terminal profile snapshot.
	var smOut *strategy.SmartMoneyResult
	if sm != nil {
		trimmed := *sm
		if len(sm.PerBar) > indicatorBarsToShow {
			trimmed.PerBar = sm.PerBar[len(sm.PerBar)-indicatorBarsToShow:]
		}
		smOut = &trimmed
	}

	return &Dashboard{
		Status: state.Status, Mode: mode, Symbol: s.Symbol, Interval: s.Interval,
		SignalSource: s.SignalSource, Running: running, LastUpdate: lastUpdate,
		Equity: equity, Cash: state.Cash, PositionQty: state.PositionQty,
		PositionValue: positionValue, EntryPrice: state.EntryPrice, LastSignal: state.LastSignal,
		PeakEquity: peak, MaxDrawdownPct: s.MaxDrawdownPct, CurrentDrawdownPct: drawdown,
		LiveTradingEnabled: b.cfg.EnableLiveTrading,
		Candles:            candles, Orders: orders, Snapshots: snapshots, RiskEvents: risks,
		Indicator: indBars, IndicatorSummary: indSummary, SmartMoney: smOut,
	}, nil
}

func (b *Bot) poll(ctx context.Context) error {
	s := b.getSettingsCopy()

	limit := s.SlowPeriod + 30
	if limit < 50 {
		limit = 50
	}
	if s.SignalSource == "cts" && limit < 400 {
		limit = 400
	}
	// Smart Money needs >= LiquidityLen + ATRLen bars to stand up levels/profile.
	if limit < 300 {
		limit = 300
	}
	if limit > 1000 {
		limit = 1000 // MEXC klines hard cap.
	}

	candles, err := b.mxc.GetKlines(ctx, s.Symbol, s.Interval, limit)
	if err != nil {
		return err
	}
	sort.Slice(candles, func(i, j int) bool { return candles[i].OpenTime.Before(candles[j].OpenTime) })
	if err := b.upsertCandles(ctx, s.Symbol, s.Interval, candles); err != nil {
		return err
	}

	b.mu.Lock()
	b.lastCandles = candles
	b.lastUpdate = time.Now()
	running := b.running
	b.mu.Unlock()

	// --- Smart Money overlay (observability only; computed even when stopped) ---
	mainClosed := confirmedOnly(toStrategyCandles(candles), s.Interval)
	if len(mainClosed) > 0 {
		smRes := strategy.ComputeSmartMoney(mainClosed, strategy.DefaultSmartMoneyParams())
		b.mu.Lock()
		b.lastSM = &smRes
		b.mu.Unlock()
	}

	if !running || len(candles) == 0 {
		return nil
	}

	last := candles[len(candles)-1]

	var signal string

	switch s.SignalSource {
	case "cts":
		signal = b.computeCTSSignal(ctx, s, mainClosed)
	default: // "sma"
		signal = computeSignal(candles, s.FastPeriod, s.SlowPeriod)
	}

	if signal != "" {
		b.handleSignal(ctx, signal, last.Close, s)
	}

	b.mu.Lock()
	equity := b.state.Cash + b.state.PositionQty*last.Close
	if equity > b.peakEquity {
		b.peakEquity = equity
	}
	b.recordSnapshotLocked(equity, last.Close)
	b.persistStateLocked()
	b.mu.Unlock()
	return nil
}

// computeCTSSignal pulls the HTF candles, persists them, drops forming bars on BOTH
// series (non-repaint), runs the local CTS port, caches the result for the chart, and
// maps the composite Side to the engine's BUY/SELL vocabulary (long-only spot:
// SHORT=exit).
func (b *Bot) computeCTSSignal(ctx context.Context, s RuntimeSettings, mainClosed []strategy.Candle) string {
	p := strategy.DefaultParams()

	htfLimit := p.HTFLen + 60
	if htfLimit > 1000 {
		htfLimit = 1000 // MEXC klines hard cap.
	}

	htf, err := b.mxc.GetKlines(ctx, s.Symbol, p.HTFTimeframe, htfLimit)
	if err != nil {
		log.Printf("cts htf klines failed: %v", err)
		return ""
	}

	sort.Slice(htf, func(i, j int) bool {
		return htf[i].OpenTime.Before(htf[j].OpenTime)
	})

	// Persist HTF so the offline backtest can replay CTS without a network fetch.
	if err := b.upsertCandles(ctx, s.Symbol, p.HTFTimeframe, htf); err != nil {
		log.Printf("cts htf upsert failed: %v", err)
	}

	htfClosed := confirmedOnly(toStrategyCandles(htf), p.HTFTimeframe)

	if len(mainClosed) == 0 {
		return ""
	}

	res := strategy.ComputeCTS(mainClosed, htfClosed, p)

	b.mu.Lock()
	b.lastIndic = &res
	b.mu.Unlock()

	if !res.Progressed {
		// Not enough warmed history to trust signals yet; do not act, but keep rendering.
		return ""
	}

	switch res.Side {
	case "LONG":
		return "BUY"
	case "SHORT":
		return "SELL"
	}

	return ""
}

func toStrategyCandles(in []mexc.Kline) []strategy.Candle {
	out := make([]strategy.Candle, len(in))
	for i, k := range in {
		out[i] = strategy.Candle{OpenTime: k.OpenTime, Open: k.Open, High: k.High, Low: k.Low, Close: k.Close, Volume: k.Volume}
	}
	return out
}

func confirmedOnly(in []strategy.Candle, interval string) []strategy.Candle {
	dur, ok := strategy.IntervalSeconds(interval)
	if !ok {
		return in
	}
	now := time.Now()
	last := -1
	for i := range in {
		if in[i].OpenTime.Add(dur).After(now) {
			break
		}
		last = i
	}
	if last < 0 {
		return nil
	}
	return in[:last+1]
}

func (b *Bot) handleSignal(ctx context.Context, signal string, price float64, s RuntimeSettings) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.running || price <= 0 {
		return
	}
	equity := b.state.Cash + b.state.PositionQty*price
	if equity > b.peakEquity {
		b.peakEquity = equity
	}
	drawdown := 0.0
	if b.peakEquity > 0 {
		drawdown = (b.peakEquity - equity) / b.peakEquity
	}
	if drawdown >= s.MaxDrawdownPct {
		b.running = false
		b.state.Status = "risk_stopped"
		b.state.LastSignal = "RISK_STOP"
		b.recordRiskLocked("max_drawdown",
			fmt.Sprintf("просадка %.6f >= лимит %.6f; equity=%.8f peak=%.8f", drawdown, s.MaxDrawdownPct, equity, b.peakEquity))
		b.persistStateLocked()
		return
	}
	if b.cfg.EnableLiveTrading {
		b.executeLiveLocked(ctx, signal, price, equity, s)
	} else {
		b.executePaperLocked(signal, price, equity, s)
	}
	b.persistStateLocked()
}

func (b *Bot) executePaperLocked(signal string, price float64, equity float64, s RuntimeSettings) {
	const eps = 1e-12
	srcNote := "paper: пересечение SMA"
	if s.SignalSource == "cts" {
		srcNote = "paper: сигнал CTS-CISD"
	}
	switch signal {
	case "BUY":
		targetValue := equity * s.MaxPositionPct
		deltaValue := targetValue - b.state.PositionQty*price
		if deltaValue <= eps {
			return
		}
		qty := deltaValue / price
		cost := qty * price
		if cost > b.state.Cash+eps {
			qty = b.state.Cash / price
			cost = qty * price
		}
		if qty <= eps || cost <= eps {
			return
		}
		newPosition := b.state.PositionQty + qty
		newEntry := ((b.state.EntryPrice * b.state.PositionQty) + (price * qty)) / newPosition
		b.state.Cash -= cost
		b.state.PositionQty = newPosition
		b.state.EntryPrice = newEntry
		b.state.LastSignal = "BUY"
		b.recordOrderLocked(s.Symbol, "BUY", qty, price, "filled", srcNote)
	case "SELL":
		if b.state.PositionQty <= eps {
			return
		}
		qty := b.state.PositionQty
		b.state.Cash += qty * price
		b.state.PositionQty = 0
		b.state.EntryPrice = 0
		b.state.LastSignal = "SELL"
		b.recordOrderLocked(s.Symbol, "SELL", qty, price, "filled", srcNote)
	}
}

func (b *Bot) executeLiveLocked(ctx context.Context, signal string, price float64, equity float64, s RuntimeSettings) {
	const eps = 1e-12
	switch signal {
	case "BUY":
		targetValue := equity * s.MaxPositionPct
		deltaValue := targetValue - b.state.PositionQty*price
		orderValue := math.Min(deltaValue, s.LiveOrderValueLimit)
		if orderValue <= eps {
			return
		}
		resp, err := b.mxc.PlaceMarketBuyQuote(ctx, s.Symbol, orderValue)
		if err != nil {
			b.recordRiskLocked("live_order_error", fmt.Sprintf("BUY (покупка): %v", err))
			return
		}
		approxQty := orderValue / price
		newPosition := b.state.PositionQty + approxQty
		newEntry := ((b.state.EntryPrice * b.state.PositionQty) + (price * approxQty)) / newPosition
		b.state.Cash -= orderValue
		b.state.PositionQty = newPosition
		b.state.EntryPrice = newEntry
		b.state.LastSignal = "BUY"
		b.recordOrderLocked(s.Symbol, "BUY", approxQty, price, resp.Status, fmt.Sprintf("live: рыночная покупка orderId=%d quoteQty=%.8f", resp.OrderID, orderValue))	
		case "SELL":
		if b.state.PositionQty <= eps {
			return
		}
		qty := b.state.PositionQty
		resp, err := b.mxc.PlaceMarketSellBase(ctx, s.Symbol, qty)
		if err != nil {
			b.recordRiskLocked("live_order_error", fmt.Sprintf("SELL (продажа): %v", err))
			return
		}
		b.state.Cash += qty * price
		b.state.PositionQty = 0
		b.state.EntryPrice = 0
		b.state.LastSignal = "SELL"
		b.recordOrderLocked(s.Symbol, "SELL", qty, price, resp.Status, fmt.Sprintf("live: рыночная продажа orderId=%d baseQty=%.8f", resp.OrderID, qty))
	}
}

func computeSignal(candles []mexc.Kline, fastPeriod, slowPeriod int) string {
	if len(candles) < slowPeriod+1 {
		return ""
	}
	closes := make([]float64, len(candles))
	for i, c := range candles {
		closes[i] = c.Close
	}
	currFast := sma(closes, fastPeriod)
	currSlow := sma(closes, slowPeriod)
	prev := closes[:len(closes)-1]
	prevFast := sma(prev, fastPeriod)
	prevSlow := sma(prev, slowPeriod)
	if prevFast == 0 || prevSlow == 0 || currSlow == 0 {
		return ""
	}
	if prevFast <= prevSlow && currFast > currSlow {
		return "BUY"
	}
	if prevFast >= prevSlow && currFast < currSlow {
		return "SELL"
	}
	return ""
}

func sma(values []float64, period int) float64 {
	if period <= 0 || len(values) < period {
		return 0
	}
	sum := 0.0
	for i := len(values) - period; i < len(values); i++ {
		sum += values[i]
	}
	return sum / float64(period)
}

func (b *Bot) mode() string {
	if b.cfg.EnableLiveTrading {
		return "live"
	}
	return "paper"
}

func validateRuntime(s RuntimeSettings) error {
	s.Symbol = strings.ToUpper(strings.TrimSpace(s.Symbol))
	if !symbolRe.MatchString(s.Symbol) {
		return fmt.Errorf("недопустимый символ %q (ожидается 2–20 символов A-Z0-9)", s.Symbol)
	}
	s.Interval = strings.TrimSpace(s.Interval)
	if !containsString(ValidIntervals, s.Interval) {
		return fmt.Errorf("недопустимый интервал %q (допустимо: %s)", s.Interval, strings.Join(ValidIntervals, ", "))
	}
	s.SignalSource = strings.ToLower(strings.TrimSpace(s.SignalSource))
	if s.SignalSource == "" {
		s.SignalSource = "sma"
	}
	if !containsString(ValidSignalSources, s.SignalSource) {
		return fmt.Errorf("недопустимый signalSource %q (допустимо: %s)", s.SignalSource, strings.Join(ValidSignalSources, ", "))
	}
	if s.FastPeriod <= 0 || s.SlowPeriod <= 0 || s.FastPeriod >= s.SlowPeriod {
		return fmt.Errorf("периоды стратегии должны удовлетворять 0 < fastPeriod < slowPeriod")
	}
	if s.PollSeconds < 5 {
		return fmt.Errorf("pollSeconds должно быть >= 5")
	}
	if s.MaxPositionPct <= 0 || s.MaxPositionPct > 1 {
		return fmt.Errorf("maxPositionPct должно быть в (0, 1]")
	}
	if s.MaxDrawdownPct <= 0 || s.MaxDrawdownPct > 1 {
		return fmt.Errorf("maxDrawdownPct должно быть в (0, 1]")
	}
	if s.PaperEquity <= 0 {
		return fmt.Errorf("paperEquity должно быть положительным")
	}
	if s.LiveOrderValueLimit <= 0 {
		return fmt.Errorf("liveOrderValueLimit должно быть положительным")
	}
	return nil
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (b *Bot) lastClosePriceLocked(symbol, interval string) (float64, bool) {
	if len(b.lastCandles) > 0 {
		return b.lastCandles[len(b.lastCandles)-1].Close, true
	}
	rows, err := b.db.QueryContext(context.Background(),
		`SELECT close FROM exchange_candles WHERE symbol=$1 AND timeframe=$2 ORDER BY open_time DESC LIMIT 1`, symbol, interval)
	if err != nil {
		return 0, false
	}
	defer rows.Close()
	if rows.Next() {
		var c float64
		if err := rows.Scan(&c); err == nil && c > 0 {
			return c, true
		}
	}
	return 0, false
}

func (b *Bot) getSettingsCopy() RuntimeSettings {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.settings
}

func (b *Bot) loadSettings(ctx context.Context) error {
	var s RuntimeSettings
	err := b.db.QueryRowContext(ctx,
		`SELECT symbol, interval, fast_period, slow_period, poll_seconds,
		        max_position_pct, max_drawdown_pct, paper_equity,
		        live_order_value_limit, signal_source, updated_at
		 FROM bot_settings WHERE id = 1`,
	).Scan(&s.Symbol, &s.Interval, &s.FastPeriod, &s.SlowPeriod, &s.PollSeconds,
		&s.MaxPositionPct, &s.MaxDrawdownPct, &s.PaperEquity, &s.LiveOrderValueLimit, &s.SignalSource, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		s = settingsFromEnv(b.cfg)
		b.persistSettings(ctx, s)
		b.mu.Lock()
		b.settings = s
		b.mu.Unlock()
		return nil
	}
	if err != nil {
		return err
	}
	if err := validateRuntime(s); err != nil {
		log.Printf("stored settings invalid (%v); resetting to env defaults", err)
		s = settingsFromEnv(b.cfg)
		b.persistSettings(ctx, s)
	}
	b.mu.Lock()
	b.settings = s
	b.mu.Unlock()
	return nil
}

func (b *Bot) persistSettings(ctx context.Context, s RuntimeSettings) {
	_, err := b.db.ExecContext(ctx,
		`INSERT INTO bot_settings
			(id, symbol, interval, fast_period, slow_period, poll_seconds,
			 max_position_pct, max_drawdown_pct, paper_equity, live_order_value_limit,
			 signal_source, updated_at)
		 VALUES (1,$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now())
		 ON CONFLICT (id) DO UPDATE SET
			symbol=EXCLUDED.symbol, interval=EXCLUDED.interval,
			fast_period=EXCLUDED.fast_period, slow_period=EXCLUDED.slow_period,
			poll_seconds=EXCLUDED.poll_seconds, max_position_pct=EXCLUDED.max_position_pct,
			max_drawdown_pct=EXCLUDED.max_drawdown_pct, paper_equity=EXCLUDED.paper_equity,
			live_order_value_limit=EXCLUDED.live_order_value_limit,
			signal_source=EXCLUDED.signal_source, updated_at=now()`,
		s.Symbol, s.Interval, s.FastPeriod, s.SlowPeriod, s.PollSeconds,
		s.MaxPositionPct, s.MaxDrawdownPct, s.PaperEquity, s.LiveOrderValueLimit,
		strings.ToLower(strings.TrimSpace(s.SignalSource)))
	if err != nil {
		log.Printf("persist settings failed: %v", err)
	}
}

func (b *Bot) persistSettingsLocked() { b.persistSettings(context.Background(), b.settings) }

func (b *Bot) loadState(ctx context.Context) {
	var s State
	err := b.db.QueryRowContext(ctx,
		`SELECT mode, status, cash, position_qty, entry_price, last_signal, updated_at
		 FROM bot_state WHERE id = 1`,
	).Scan(&s.Mode, &s.Status, &s.Cash, &s.PositionQty, &s.EntryPrice, &s.LastSignal, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		s = State{Mode: b.mode(), Status: "stopped", Cash: b.settings.PaperEquity, PositionQty: 0, EntryPrice: 0, LastSignal: "none", UpdatedAt: time.Now()}
		b.insertInitialState(ctx, s)
	} else if err != nil {
		log.Printf("load bot state failed: %v; using default paper state", err)
		s = State{Mode: b.mode(), Status: "stopped", Cash: b.settings.PaperEquity, PositionQty: 0, EntryPrice: 0, LastSignal: "none", UpdatedAt: time.Now()}
		b.insertInitialState(ctx, s)
	}
	if s.Mode == "" {
		s.Mode = b.mode()
	}
	if s.Status == "" {
		s.Status = "stopped"
	}
	if s.LastSignal == "" {
		s.LastSignal = "none"
	}
	b.state = s
	b.peakEquity = s.Cash
	b.running = false
}

func (b *Bot) insertInitialState(ctx context.Context, s State) {
	_, err := b.db.ExecContext(ctx,
		`INSERT INTO bot_state (id, mode, status, cash, position_qty, entry_price, last_signal)
		 VALUES (1,$1,$2,$3,$4,$5,$6) ON CONFLICT (id) DO NOTHING`,
		s.Mode, s.Status, s.Cash, s.PositionQty, s.EntryPrice, s.LastSignal)
	if err != nil {
		log.Printf("insert initial bot state failed: %v", err)
	}
}

func (b *Bot) persistStateLocked() {
	_, err := b.db.ExecContext(context.Background(),
		`UPDATE bot_state SET mode=$1, status=$2, cash=$3, position_qty=$4, entry_price=$5, last_signal=$6, updated_at=now() WHERE id=1`,
		b.state.Mode, b.state.Status, b.state.Cash, b.state.PositionQty, b.state.EntryPrice, b.state.LastSignal)
	if err != nil {
		log.Printf("persist bot state failed: %v", err)
	}
}

func (b *Bot) recordOrderLocked(symbol, side string, qty, price float64, status, note string) {
	_, err := b.db.ExecContext(context.Background(),
		`INSERT INTO paper_orders (symbol, side, qty, price, status, note) VALUES ($1,$2,$3,$4,$5,$6)`,
		symbol, side, qty, price, status, note)
	if err != nil {
		log.Printf("record order failed: %v", err)
	}
}

func (b *Bot) recordSnapshotLocked(equity, price float64) {
	_, err := b.db.ExecContext(context.Background(),
		`INSERT INTO equity_snapshots (equity, cash, position_value) VALUES ($1,$2,$3)`,
		equity, b.state.Cash, b.state.PositionQty*price)
	if err != nil {
		log.Printf("record equity snapshot failed: %v", err)
	}
}

func (b *Bot) recordRiskLocked(eventType, message string) {
	_, err := b.db.ExecContext(context.Background(),
		`INSERT INTO risk_events (type, message) VALUES ($1,$2)`, eventType, message)
	if err != nil {
		log.Printf("record risk event failed: %v", err)
	}
}

func (b *Bot) upsertCandles(ctx context.Context, symbol, interval string, candles []mexc.Kline) error {
	for _, k := range candles {
		_, err := b.db.ExecContext(ctx,
			`INSERT INTO exchange_candles (symbol, timeframe, open_time, open, high, low, close, volume)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			 ON CONFLICT (symbol, timeframe, open_time)
			 DO UPDATE SET open=EXCLUDED.open, high=EXCLUDED.high, low=EXCLUDED.low, close=EXCLUDED.close, volume=EXCLUDED.volume`,
			symbol, interval, k.OpenTime, k.Open, k.High, k.Low, k.Close, k.Volume)
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *Bot) loadRecentCandlesFromDB(ctx context.Context, symbol, interval string, limit int) ([]mexc.Kline, error) {
	rows, err := b.db.QueryContext(ctx,
		`SELECT open_time, open, high, low, close, volume FROM exchange_candles
		 WHERE symbol=$1 AND timeframe=$2 ORDER BY open_time DESC LIMIT $3`, symbol, interval, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rev []mexc.Kline
	for rows.Next() {
		var k mexc.Kline
		if err := rows.Scan(&k.OpenTime, &k.Open, &k.High, &k.Low, &k.Close, &k.Volume); err != nil {
			return nil, err
		}
		rev = append(rev, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, nil
}

func (b *Bot) queryOrders(ctx context.Context, limit int) ([]OrderRow, error) {
	rows, err := b.db.QueryContext(ctx,
		`SELECT id, created_at, symbol, side, qty, price, status, note FROM paper_orders ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]OrderRow, 0, limit)
	for rows.Next() {
		var o OrderRow
		if err := rows.Scan(&o.ID, &o.CreatedAt, &o.Symbol, &o.Side, &o.Qty, &o.Price, &o.Status, &o.Note); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (b *Bot) querySnapshots(ctx context.Context, limit int) ([]SnapshotRow, error) {
	rows, err := b.db.QueryContext(ctx,
		`SELECT id, created_at, equity, cash, position_value FROM equity_snapshots ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SnapshotRow, 0, limit)
	for rows.Next() {
		var s SnapshotRow
		if err := rows.Scan(&s.ID, &s.CreatedAt, &s.Equity, &s.Cash, &s.PositionValue); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (b *Bot) queryRiskEvents(ctx context.Context, limit int) ([]RiskEventRow, error) {
	rows, err := b.db.QueryContext(ctx,
		`SELECT id, created_at, type, message FROM risk_events ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RiskEventRow, 0, limit)
	for rows.Next() {
		var r RiskEventRow
		if err := rows.Scan(&r.ID, &r.CreatedAt, &r.Type, &r.Message); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// BacktestComparison is the payload for GET /api/backtest: one honest run per source
// over the same confirmed history, plus a light SMA sensitivity grid. Heavy CTS
// sensitivity is intentionally CLI-only (offline, no HTTP timeout risk).
type BacktestComparison struct {
	SMA            *backtest.Result     `json:"sma,omitempty"`
	CTS            *backtest.Result     `json:"cts,omitempty"`
	SMASensitivity []backtest.SensRow   `json:"smaSensitivity,omitempty"`
	SMASensSummary backtest.SensSummary `json:"smaSensSummary,omitempty"`
	Assumptions    []string             `json:"assumptions"`
	Window         map[string]string    `json:"window"`
}

// RunBacktest loads confirmed history from the DB and replays both signal sources with
// explicit costs and next-bar fills. It is read-only wrt trading state.
func (b *Bot) RunBacktest(ctx context.Context, symbol, interval, source string, start, end time.Time, takerBps, slipBps, maxPos, maxDD, equity float64, withSMAGrid bool, step, minTrades int) (*BacktestComparison, error) {
	candles, err := backtest.LoadCandles(ctx, b.db, symbol, interval, start, end)
	if err != nil {
		return nil, err
	}
	if len(candles) < 50 {
		return nil, fmt.Errorf("only %d confirmed candles in window; need >=50", len(candles))
	}

	fee := backtest.ResolveFees(takerBps, slipBps)
	base := backtest.Config{
		Interval: interval, FastPeriod: b.settings.FastPeriod, SlowPeriod: b.settings.SlowPeriod,
		MaxPositionPct: maxPos, MaxDrawdownPct: maxDD, StartEquity: equity,
		Fee: fee, Execution: backtest.NextBarOpen,
	}

	out := &BacktestComparison{
		Assumptions: fee.Assumptions(),
		Window:      map[string]string{"symbol": symbol, "interval": interval, "bars": fmt.Sprint(len(candles))},
	}

	if source == "" || source == "sma" || source == "both" {
		c := base
		c.Source = "sma"
		r := backtest.Run(candles, nil, c)
		out.SMA = &r
		out.Assumptions = append(out.Assumptions, r.Assumptions...)
		if withSMAGrid {
			rows, sum := backtest.SensGrid(candles, nil, c, step, minTrades)
			out.SMASensitivity = rows
			out.SMASensSummary = sum
		}
	}
	if source == "" || source == "cts" || source == "both" {
		c := base
		c.Source = "cts"
		p := strategy.DefaultParams()
		c.CTSParams = &p
		// HTF: prefer DB, fallback to a read-only exchange fetch.
		var htf []strategy.Candle
		if n, _ := backtest.CountCandles(ctx, b.db, symbol, p.HTFTimeframe); n > 0 {
			htf, err = backtest.LoadCandles(ctx, b.db, symbol, p.HTFTimeframe, time.Time{}, time.Time{})
			if err != nil {
				return nil, err
			}
		} else if raw, e := b.mxc.GetKlines(ctx, symbol, p.HTFTimeframe, p.HTFLen+60); e == nil {
			sort.Slice(raw, func(i, j int) bool { return raw[i].OpenTime.Before(raw[j].OpenTime) })
			htf = toStrategyCandles(raw)
		}
		r := backtest.Run(candles, htf, c)
		out.CTS = &r
		out.Assumptions = append(out.Assumptions, r.Assumptions...)
	}
	return out, nil
}
