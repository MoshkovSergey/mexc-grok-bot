-- Initial schema for the MEXC Grok-like bot.
-- FLOAT/DOUBLE types are used for MVP simplicity. For production accounting,
-- consider NUMERIC and decimal libraries to avoid floating-point drift.

CREATE TABLE IF NOT EXISTS exchange_candles (
    symbol TEXT NOT NULL,
    timeframe TEXT NOT NULL,
    open_time TIMESTAMPTZ NOT NULL,
    open DOUBLE PRECISION NOT NULL,
    high DOUBLE PRECISION NOT NULL,
    low DOUBLE PRECISION NOT NULL,
    close DOUBLE PRECISION NOT NULL,
    volume DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (symbol, timeframe, open_time)
);

CREATE INDEX IF NOT EXISTS idx_exchange_candles_symbol_timeframe_time
    ON exchange_candles (symbol, timeframe, open_time DESC);

CREATE TABLE IF NOT EXISTS bot_state (
    id SMALLINT PRIMARY KEY,
    mode TEXT NOT NULL DEFAULT 'paper',
    status TEXT NOT NULL DEFAULT 'stopped',
    cash DOUBLE PRECISION NOT NULL DEFAULT 0,
    position_qty DOUBLE PRECISION NOT NULL DEFAULT 0,
    entry_price DOUBLE PRECISION NOT NULL DEFAULT 0,
    last_signal TEXT NOT NULL DEFAULT 'none',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Hot-reloadable trading settings. signal_source selects the trigger layer:
--   'sma' = built-in moving-average crossover (research baseline)
--   'cts' = local Go port of the Pine CTS-CISD indicator (no TradingView dependency)
-- Live trading is NEVER controlled from here (env-only).
CREATE TABLE IF NOT EXISTS bot_settings (
    id SMALLINT PRIMARY KEY,
    symbol TEXT NOT NULL,
    interval TEXT NOT NULL,
    fast_period INT NOT NULL,
    slow_period INT NOT NULL,
    poll_seconds INT NOT NULL,
    max_position_pct DOUBLE PRECISION NOT NULL,
    max_drawdown_pct DOUBLE PRECISION NOT NULL,
    paper_equity DOUBLE PRECISION NOT NULL,
    live_order_value_limit DOUBLE PRECISION NOT NULL,
    signal_source TEXT NOT NULL DEFAULT 'sma',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE bot_settings ADD COLUMN IF NOT EXISTS signal_source TEXT NOT NULL DEFAULT 'sma';

CREATE TABLE IF NOT EXISTS paper_orders (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    symbol TEXT NOT NULL,
    side TEXT NOT NULL,
    qty DOUBLE PRECISION NOT NULL,
    price DOUBLE PRECISION NOT NULL,
    status TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_paper_orders_created_at
    ON paper_orders (created_at DESC);

CREATE TABLE IF NOT EXISTS equity_snapshots (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    equity DOUBLE PRECISION NOT NULL,
    cash DOUBLE PRECISION NOT NULL,
    position_value DOUBLE PRECISION NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_equity_snapshots_created_at
    ON equity_snapshots (created_at DESC);

CREATE TABLE IF NOT EXISTS risk_events (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    type TEXT NOT NULL,
    message TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_risk_events_created_at
    ON risk_events (created_at DESC);