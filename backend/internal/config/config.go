// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config contains all backend settings.
type Config struct {
	Port        string
	DatabaseURL string

	// MEXC settings. Verify endpoints and parameters against official docs:
	// https://mexcdevelop.github.io/apidocs/spot_v3_en/
	MexcBaseURL   string
	MexcAPIKey    string
	MexcAPISecret string

	// Market settings (env = defaults only; runtime truth is bot_settings in DB).
	Symbol   string
	Interval string

	// Strategy settings (defaults for first DB row creation).
	FastPeriod int
	SlowPeriod int

	// Polling interval in seconds (default; runtime value is in bot_settings).
	PollSeconds int

	// Paper trading starting equity in quote currency.
	PaperEquity float64

	// Risk controls.
	MaxPositionPct      float64
	MaxDrawdownPct      float64
	EnableLiveTrading   bool
	LiveOrderValueLimit float64
}

// Load reads and validates configuration.
func Load() (*Config, error) {
	cfg := &Config{
		Port:        getEnv("PORT", "8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),

		MexcBaseURL:   strings.TrimRight(getEnv("MEXC_BASE_URL", "https://api.mexc.com"), "/"),
		MexcAPIKey:    os.Getenv("MEXC_API_KEY"),
		MexcAPISecret: os.Getenv("MEXC_API_SECRET"),

		Symbol:   strings.ToUpper(getEnv("SYMBOL", "BTCUSDT")),
		Interval: getEnv("INTERVAL", "1m"),
	}

	var err error
	cfg.FastPeriod, err = getEnvInt("FAST_PERIOD", 9)
	if err != nil {
		return nil, fmt.Errorf("FAST_PERIOD: %w", err)
	}
	cfg.SlowPeriod, err = getEnvInt("SLOW_PERIOD", 21)
	if err != nil {
		return nil, fmt.Errorf("SLOW_PERIOD: %w", err)
	}
	cfg.PollSeconds, err = getEnvInt("POLL_SECONDS", 15)
	if err != nil {
		return nil, fmt.Errorf("POLL_SECONDS: %w", err)
	}
	cfg.PaperEquity, err = getEnvFloat("PAPER_EQUITY", 10000)
	if err != nil {
		return nil, fmt.Errorf("PAPER_EQUITY: %w", err)
	}
	cfg.MaxPositionPct, err = getEnvFloat("MAX_POSITION_PCT", 0.20)
	if err != nil {
		return nil, fmt.Errorf("MAX_POSITION_PCT: %w", err)
	}
	cfg.MaxDrawdownPct, err = getEnvFloat("MAX_DRAWDOWN_PCT", 0.05)
	if err != nil {
		return nil, fmt.Errorf("MAX_DRAWDOWN_PCT: %w", err)
	}
	cfg.EnableLiveTrading, err = getEnvBool("ENABLE_LIVE_TRADING", false)
	if err != nil {
		return nil, fmt.Errorf("ENABLE_LIVE_TRADING: %w", err)
	}
	cfg.LiveOrderValueLimit, err = getEnvFloat("LIVE_ORDER_VALUE_LIMIT", 50)
	if err != nil {
		return nil, fmt.Errorf("LIVE_ORDER_VALUE_LIMIT: %w", err)
	}

	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.FastPeriod <= 0 || cfg.SlowPeriod <= 0 || cfg.FastPeriod >= cfg.SlowPeriod {
		return nil, fmt.Errorf("strategy periods must satisfy 0 < FAST_PERIOD < SLOW_PERIOD")
	}
	if cfg.PollSeconds < 5 {
		cfg.PollSeconds = 5
	}
	if cfg.PaperEquity <= 0 {
		return nil, fmt.Errorf("PAPER_EQUITY must be positive")
	}
	if cfg.MaxPositionPct <= 0 || cfg.MaxPositionPct > 1 {
		return nil, fmt.Errorf("MAX_POSITION_PCT must be in (0, 1]")
	}
	if cfg.MaxDrawdownPct <= 0 || cfg.MaxDrawdownPct > 1 {
		return nil, fmt.Errorf("MAX_DRAWDOWN_PCT must be in (0, 1]")
	}
	if cfg.EnableLiveTrading {
		if cfg.MexcAPIKey == "" || cfg.MexcAPISecret == "" {
			return nil, fmt.Errorf("MEXC_API_KEY and MEXC_API_SECRET are required when ENABLE_LIVE_TRADING=true")
		}
		if cfg.LiveOrderValueLimit <= 0 {
			return nil, fmt.Errorf("LIVE_ORDER_VALUE_LIMIT must be positive when live trading is enabled")
		}
	}
	return cfg, nil
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
func getEnvInt(key string, defaultValue int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", key, v, err)
	}
	return n, nil
}
func getEnvFloat(key string, defaultValue float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", key, v, err)
	}
	return f, nil
}
func getEnvBool(key string, defaultValue bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s=%q: %w", key, v, err)
	}
	return b, nil
}