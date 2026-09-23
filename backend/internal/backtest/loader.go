package backtest

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/strategy"
)

// LoadCandles reads confirmed OHLCV history for symbol/interval in [start,end] from
// exchange_candles, ordered ascending. If end is zero it defaults to now and the
// still-forming tail bar (open_time + interval > now) is dropped, so the backtest only
// ever sees closed bars. start zero = no lower bound.
func LoadCandles(ctx context.Context, db *sql.DB, symbol, interval string, start, end time.Time) ([]strategy.Candle, error) {
	dur, ok := strategy.IntervalSeconds(interval)
	if !ok {
		return nil, fmt.Errorf("неизвестный интервал %q", interval)
	}
	if end.IsZero() {
		end = time.Now()
	}
	// Only bars whose close time has elapsed are confirmed.
	confirmCut := end.Add(-dur)

	q := `SELECT open_time, open, high, low, close, volume
	      FROM exchange_candles
	      WHERE symbol=$1 AND timeframe=$2 AND open_time <= $3`
	args := []any{symbol, interval, confirmCut}
	if !start.IsZero() {
		q += ` AND open_time >= $4`
		args = append(args, start)
	}
	q += ` ORDER BY open_time ASC`

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []strategy.Candle
	for rows.Next() {
		var c strategy.Candle
		if err := rows.Scan(&c.OpenTime, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountCandles is a cheap existence probe (used to decide whether HTF history must be
// fetched from the exchange because the DB has none yet).
func CountCandles(ctx context.Context, db *sql.DB, symbol, interval string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM exchange_candles WHERE symbol=$1 AND timeframe=$2`,
		symbol, interval).Scan(&n)
	return n, err
}
