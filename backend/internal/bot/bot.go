// Package bot implements a simple rule-based trading engine with risk controls.
//
// Default strategy: SMA fast/slow crossover.
// Default mode: paper trading.
//
// Live trading is possible only when ENABLE_LIVE_TRADING=true. It is still a
// simplified implementation and does not handle all exchange precision/fill
// edge cases. Treat live mode as high-risk.
package bot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/config"
	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/mexc"
)

// State is the persistent bot portfolio/state.
type State struct {
	Mode        string    `json:"mode"`
	Status      string    `json:"status"`
	Cash        float64   `json:"cash"`
	PositionQty float64   `json:"positionQty"`
	EntryPrice  float64   `json:"entryPrice"`
	LastSignal  string    `json:"lastSignal"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// OrderRow is a persisted order record.
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

// SnapshotRow is an equity snapshot.
type SnapshotRow struct {
	ID            int64     `json:"id"`
	CreatedAt     time.Time `json:"createdAt"`
	Equity        float64   `json:"equity"`
	Cash          float64   `json:"cash"`
	PositionValue float64   `json:"positionValue"`
}

// RiskEventRow is a risk manager event.
type RiskEventRow struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
}

// Dashboard is the payload consumed by the React frontend.
type Dashboard struct {
	Status             string       `json:"status"`
	Mode               string       `json:"mode"`
	Symbol             string       `json:"symbol"`
	Interval           string       `json:"interval"`
	Running            bool         `json:"running"`
	LastUpdate         time.Time    `json:"lastUpdate"`
	Equity             float64      `json:"equity"`
	Cash               float64      `json:"cash"`
	PositionQty        float64      `json:"positionQty"`
	PositionValue      float64      `json:"positionValue"`
	EntryPrice         float64      `json:"entryPrice"`
	LastSignal         string       `json:"lastSignal"`
	PeakEquity         float64      `json:"peakEquity"`
	MaxDrawdownPct     float64      `json:"maxDrawdownPct"`
	CurrentDrawdownPct float64      `json:"currentDrawdownPct"`
	LiveTradingEnabled bool         `json:"liveTradingEnabled"`
	Candles            []mexc.Kline `json:"candles"`
	Orders             []OrderRow   `json:"orders"`
	Snapshots          []SnapshotRow `json:"snapshots"`
	RiskEvents         []RiskEventRow `json:"riskEvents"`
}

// Bot is the main trading engine.
type Bot struct {
	cfg *config.Config
	db  *sql.DB
	mxc *mexc.Client

	mu           sync.Mutex
	running      bool
	state        State
	peakEquity   float64
	lastUpdate   time.Time
	lastCandles  []mexc.Kline
}

// New creates a bot and loads persistent state.
func New(cfg *config.Config, database *sql.DB, client *mexc.Client) *Bot {
	b := &Bot{
		cfg: cfg,
		db:  database,
		mxc: client,
	}
	b.loadState(context.Background())
	return b
}

// Run starts the polling loop. It respects context cancellation.
func (b *Bot) Run(ctx context.Context) error {
	// Best-effort time sync for signed requests. Public data can still work if this fails.
	if err := b.mxc.SyncTime(ctx); err != nil {
		log.Printf("mexc time sync failed: %v", err)
	}

	ticker := time.NewTicker(time.Duration(b.cfg.PollSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := b.poll(ctx); err != nil {
				log.Printf("poll error: %v", err)
			}
		}
	}
}

// Start enables trading actions. It does not bypass risk stops.
func (b *Bot) Start(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state.Status == "risk_stopped" {
		return errors.New("bot is stopped by risk manager; reset bot_state manually or redeploy with new peak")
	}

	b.running = true
	b.state.Status = "running"
	b.state.Mode = b.mode()
	b.persistStateLocked()
	return nil
}

// Stop disables trading actions but keeps market data polling.
func (b *Bot) Stop(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.running = false
	b.state.Status = "stopped"
	b.persistStateLocked()
	return nil
}

// Status returns a lightweight status object.
func (b *Bot) Status() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()

	price := 0.0
	if len(b.lastCandles) > 0 {
		price = b.lastCandles[len(b.lastCandles)-1].Close
	}

	equity := b.state.Cash + b.state.PositionQty*price
	drawdown := 0.0
	if b.peakEquity > 0 {
		drawdown = (b.peakEquity - equity) / b.peakEquity
	}

	return map[string]any{
		"status":             b.state.Status,
		"mode":               b.state.Mode,
		"running":            b.running,
		"symbol":             b.cfg.Symbol,
		"interval":           b.cfg.Interval,
		"equity":             equity,
		"cash":               b.state.Cash,
		"positionQty":        b.state.PositionQty,
		"entryPrice":         b.state.EntryPrice,
		"lastSignal":         b.state.LastSignal,
		"peakEquity":         b.peakEquity,
		"currentDrawdownPct": drawdown,
		"maxDrawdownPct":     b.cfg.MaxDrawdownPct,
		"liveTradingEnabled": b.cfg.EnableLiveTrading,
		"lastUpdate":         b.lastUpdate,
	}
}

// Dashboard returns full UI payload.
func (b *Bot) Dashboard(ctx context.Context) (*Dashboard, error) {
	b.mu.Lock()
	state := b.state
	running := b.running
	lastUpdate := b.lastUpdate
	peak := b.peakEquity
	candles := append([]mexc.Kline(nil), b.lastCandles...)
	b.mu.Unlock()

	if len(candles) == 0 {
		loaded, err := b.loadRecentCandlesFromDB(ctx, 100)
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

	return &Dashboard{
		Status:             state.Status,
		Mode:               mode,
		Symbol:             b.cfg.Symbol,
		Interval:           b.cfg.Interval,
		Running:            running,
		LastUpdate:         lastUpdate,
		Equity:             equity,
		Cash:               state.Cash,
		PositionQty:        state.PositionQty,
		PositionValue:      positionValue,
		EntryPrice:         state.EntryPrice,
		LastSignal:         state.LastSignal,
		PeakEquity:         peak,
		MaxDrawdownPct:     b.cfg.MaxDrawdownPct,
		CurrentDrawdownPct: drawdown,
		LiveTradingEnabled: b.cfg.EnableLiveTrading,
		Candles:            candles,
		Orders:             orders,
		Snapshots:          snapshots,
		RiskEvents:         risks,
	}, nil
}

func (b *Bot) poll(ctx context.Context) error {
	limit := b.cfg.SlowPeriod + 30
	if limit < 50 {
		limit = 50
	}

	candles, err := b.mxc.GetKlines(ctx, b.cfg.Symbol, b.cfg.Interval, limit)
	if err != nil {
		return err
	}

	sort.Slice(candles, func(i, j int) bool {
		return candles[i].OpenTime.Before(candles[j].OpenTime)
	})

	if err := b.upsertCandles(ctx, candles); err != nil {
		return err
	}

	b.mu.Lock()
	b.lastCandles = candles
	b.lastUpdate = time.Now()
	running := b.running
	b.mu.Unlock()

	if !running || len(candles) == 0 {
		return nil
	}

	last := candles[len(candles)-1]
	signal := computeSignal(candles, b.cfg.FastPeriod, b.cfg.SlowPeriod)

	if signal != "" {
		b.handleSignal(ctx, signal, last.Close)
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

func (b *Bot) handleSignal(ctx context.Context, signal string, price float64) {
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

	if drawdown >= b.cfg.MaxDrawdownPct {
		b.running = false
		b.state.Status = "risk_stopped"
		b.state.LastSignal = "RISK_STOP"
		b.recordRiskLocked(
			"max_drawdown",
			fmt.Sprintf("drawdown %.4f >= limit %.4f; equity=%.8f peak=%.8f", drawdown, b.cfg.MaxDrawdownPct, equity, b.peakEquity),
		)
		b.persistStateLocked()
		return
	}

	if b.cfg.EnableLiveTrading {
		b.executeLiveLocked(ctx, signal, price, equity)
	} else {
		b.executePaperLocked(signal, price, equity)
	}

	b.persistStateLocked()
}

func (b *Bot) executePaperLocked(signal string, price float64, equity float64) {
	const eps = 1e-12

	switch signal {
	case "BUY":
		targetValue := equity * b.cfg.MaxPositionPct
		currentValue := b.state.PositionQty * price
		deltaValue := targetValue - currentValue

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

		b.recordOrderLocked("BUY", qty, price, "filled", "paper SMA cross")

	case "SELL":
		if b.state.PositionQty <= eps {
			return
		}

		qty := b.state.PositionQty
		proceeds := qty * price

		b.state.Cash += proceeds
		b.state.PositionQty = 0
		b.state.EntryPrice = 0
		b.state.LastSignal = "SELL"

		b.recordOrderLocked("SELL", qty, price, "filled", "paper SMA cross")
	}
}

func (b *Bot) executeLiveLocked(ctx context.Context, signal string, price float64, equity float64) {
	const eps = 1e-12

	switch signal {
	case "BUY":
		targetValue := equity * b.cfg.MaxPositionPct
		currentValue := b.state.PositionQty * price
		deltaValue := targetValue - currentValue
		orderValue := math.Min(deltaValue, b.cfg.LiveOrderValueLimit)

		if orderValue <= eps {
			return
		}

		resp, err := b.mxc.PlaceMarketBuyQuote(ctx, b.cfg.Symbol, orderValue)
		if err != nil {
			b.recordRiskLocked("live_order_error", fmt.Sprintf("BUY: %v", err))
			return
		}

		// Approximate accounting. Real fill quantity must be reconciled via order query.
		approxQty := orderValue / price
		newPosition := b.state.PositionQty + approxQty
		newEntry := ((b.state.EntryPrice * b.state.PositionQty) + (price * approxQty)) / newPosition

		b.state.Cash -= orderValue
		b.state.PositionQty = newPosition
		b.state.EntryPrice = newEntry
		b.state.LastSignal = "BUY"

		b.recordOrderLocked(
			"BUY",
			approxQty,
			price,
			resp.Status,
			fmt.Sprintf("live market buy orderId=%d quoteQty=%.8f", resp.OrderID, orderValue),
		)

	case "SELL":
		if b.state.PositionQty <= eps {
			return
		}

		qty := b.state.PositionQty
		resp, err := b.mxc.PlaceMarketSellBase(ctx, b.cfg.Symbol, qty)
		if err != nil {
			b.recordRiskLocked("live_order_error", fmt.Sprintf("SELL: %v", err))
			return
		}

		proceeds := qty * price
		b.state.Cash += proceeds
		b.state.PositionQty = 0
		b.state.EntryPrice = 0
		b.state.LastSignal = "SELL"

		b.recordOrderLocked(
			"SELL",
			qty,
			price,
			resp.Status,
			fmt.Sprintf("live market sell orderId=%d baseQty=%.8f", resp.OrderID, qty),
		)
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

	prevCloses := closes[:len(closes)-1]
	prevFast := sma(prevCloses, fastPeriod)
	prevSlow := sma(prevCloses, slowPeriod)

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

func (b *Bot) loadState(ctx context.Context) {
	var s State

	err := b.db.QueryRowContext(ctx,
		`SELECT mode, status, cash, position_qty, entry_price, last_signal, updated_at
		 FROM bot_state WHERE id = 1`,
	).Scan(
		&s.Mode,
		&s.Status,
		&s.Cash,
		&s.PositionQty,
		&s.EntryPrice,
		&s.LastSignal,
		&s.UpdatedAt,
	)

	if errors.Is(err, sql.ErrNoRows) {
		s = State{
			Mode:        b.mode(),
			Status:      "stopped",
			Cash:        b.cfg.PaperEquity,
			PositionQty: 0,
			EntryPrice:  0,
			LastSignal:  "none",
			UpdatedAt:   time.Now(),
		}
		b.insertInitialState(ctx, s)
	} else if err != nil {
		log.Printf("load bot state failed: %v; using default paper state", err)
		s = State{
			Mode:        b.mode(),
			Status:      "stopped",
			Cash:        b.cfg.PaperEquity,
			PositionQty: 0,
			EntryPrice:  0,
			LastSignal:  "none",
			UpdatedAt:   time.Now(),
		}
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
		 VALUES (1, $1, $2, $3, $4, $5, $6)
		 ON CONFLICT (id) DO NOTHING`,
		s.Mode, s.Status, s.Cash, s.PositionQty, s.EntryPrice, s.LastSignal,
	)
	if err != nil {
		log.Printf("insert initial bot state failed: %v", err)
	}
}

func (b *Bot) persistStateLocked() {
	_, err := b.db.ExecContext(context.Background(),
		`UPDATE bot_state
		 SET mode=$1, status=$2, cash=$3, position_qty=$4, entry_price=$5, last_signal=$6, updated_at=now()
		 WHERE id=1`,
		b.state.Mode,
		b.state.Status,
		b.state.Cash,
		b.state.PositionQty,
		b.state.EntryPrice,
		b.state.LastSignal,
	)
	if err != nil {
		log.Printf("persist bot state failed: %v", err)
	}
}

func (b *Bot) recordOrderLocked(side string, qty, price float64, status, note string) {
	_, err := b.db.ExecContext(context.Background(),
		`INSERT INTO paper_orders (symbol, side, qty, price, status, note)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		b.cfg.Symbol, side, qty, price, status, note,
	)
	if err != nil {
		log.Printf("record order failed: %v", err)
	}
}

func (b *Bot) recordSnapshotLocked(equity, price float64) {
	positionValue := b.state.PositionQty * price
	_, err := b.db.ExecContext(context.Background(),
		`INSERT INTO equity_snapshots (equity, cash, position_value)
		 VALUES ($1, $2, $3)`,
		equity, b.state.Cash, positionValue,
	)
	if err != nil {
		log.Printf("record equity snapshot failed: %v", err)
	}
}

func (b *Bot) recordRiskLocked(eventType, message string) {
	_, err := b.db.ExecContext(context.Background(),
		`INSERT INTO risk_events (type, message) VALUES ($1, $2)`,
		eventType, message,
	)
	if err != nil {
		log.Printf("record risk event failed: %v", err)
	}
}

func (b *Bot) upsertCandles(ctx context.Context, candles []mexc.Kline) error {
	for _, k := range candles {
		_, err := b.db.ExecContext(ctx,
			`INSERT INTO exchange_candles
				(symbol, timeframe, open_time, open, high, low, close, volume)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			 ON CONFLICT (symbol, timeframe, open_time)
			 DO UPDATE SET
				open = EXCLUDED.open,
				high = EXCLUDED.high,
				low = EXCLUDED.low,
				close = EXCLUDED.close,
				volume = EXCLUDED.volume`,
			b.cfg.Symbol,
			b.cfg.Interval,
			k.OpenTime,
			k.Open,
			k.High,
			k.Low,
			k.Close,
			k.Volume,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *Bot) loadRecentCandlesFromDB(ctx context.Context, limit int) ([]mexc.Kline, error) {
	rows, err := b.db.QueryContext(ctx,
		`SELECT open_time, open, high, low, close, volume
		 FROM exchange_candles
		 WHERE symbol=$1 AND timeframe=$2
		 ORDER BY open_time DESC
		 LIMIT $3`,
		b.cfg.Symbol, b.cfg.Interval, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reversed []mexc.Kline
	for rows.Next() {
		var k mexc.Kline
		if err := rows.Scan(&k.OpenTime, &k.Open, &k.High, &k.Low, &k.Close, &k.Volume); err != nil {
			return nil, err
		}
		reversed = append(reversed, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}

	return reversed, nil
}

func (b *Bot) queryOrders(ctx context.Context, limit int) ([]OrderRow, error) {
	rows, err := b.db.QueryContext(ctx,
		`SELECT id, created_at, symbol, side, qty, price, status, note
		 FROM paper_orders
		 ORDER BY id DESC
		 LIMIT $1`,
		limit,
	)
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
		`SELECT id, created_at, equity, cash, position_value
		 FROM equity_snapshots
		 ORDER BY id DESC
		 LIMIT $1`,
		limit,
	)
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
		`SELECT id, created_at, type, message
		 FROM risk_events
		 ORDER BY id DESC
		 LIMIT $1`,
		limit,
	)
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