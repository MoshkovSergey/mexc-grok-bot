// Package backtest provides an OFFLINE, cost-aware backtest engine that replays the
// SAME signal logic as the live engine (SMA crossover and the local CTS-CISD port)
// over historical candles stored in PostgreSQL, with explicit fees and slippage,
// next-bar execution, a max-drawdown kill-switch identical to the live one, and a
// parameter-sensitivity grid to distinguish reproducible edge from overfitting.
//
// This package is READ-ONLY with respect to trading: it never places orders and never
// mutates bot_state. It is a research instrument.
package backtest

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/mexc"
)

// FeeModel holds the per-side cost assumptions actually used in a run.
type FeeModel struct {
	TakerFeeBps float64 `json:"takerFeeBps"`
	SlippageBps float64 `json:"slippageBps"`
	// Sources documents where each number came from, so a reader can judge optimism.
	TakerSource   string `json:"takerSource"`   // "cli" | "env" | "zero-default"
	SlippageSource string `json:"slippageSource"`
}

// ResolveFees builds the fee model from explicit CLI values, then ENV, then a loud
// zero-default. It deliberately does NOT invent a "typical" rate: an unset rate means
// zero fees and an explicit optimistic-assumption flag, forcing the operator to set a
// verified number after checking the MEXC fee schedule and VIP tier.
func ResolveFees(cliTakerBps, cliSlippageBps float64) FeeModel {
	fm := FeeModel{}

	if cliTakerBps > 0 {
		fm.TakerFeeBps, fm.TakerSource = cliTakerBps, "cli"
	} else if v, ok := envFloat("BACKTEST_TAKER_FEE_BPS"); ok && v > 0 {
		fm.TakerFeeBps, fm.TakerSource = v, "env"
	} else {
		fm.TakerFeeBps, fm.TakerSource = 0, "zero-default"
	}

	if cliSlippageBps > 0 {
		fm.SlippageBps, fm.SlippageSource = cliSlippageBps, "cli"
	} else if v, ok := envFloat("BACKTEST_SLIPPAGE_BPS"); ok && v > 0 {
		fm.SlippageBps, fm.SlippageSource = v, "env"
	} else {
		fm.SlippageBps, fm.SlippageSource = 0, "zero-default"
	}
	return fm
}

// Assumptions returns human-readable warnings about optimistic defaults. These MUST be
// surfaced in every result so nobody mistakes a zero-cost run for a realistic one.
func (fm FeeModel) Assumptions() []string {
	var out []string
	if fm.TakerSource == "zero-default" {
		out = append(out, "Использована НУЛЕВАЯ комиссия тейкера — результаты ОПТИМИСТИЧНЫ. Задайте --taker-bps или BACKTEST_TAKER_FEE_BPS после сверки с fee‑schedule MEXC и вашим VIP‑уровнем.")
	}
	if fm.SlippageSource == "zero-default" {
		out = append(out, "Использован НУЛЕВОЙ слиппедж/спред‑пенальти — результаты ОПТИМИСТИЧНЫ. На тонких книгах (например, низколиквидные пары на 1m) реальное рыночное исполнение может быть заметно хуже цены бара.")
	}
	return out
}

// CrossCheckAccount optionally fetches raw commission fields from /api/v3/account for a
// HUMAN to verify the scale. It is NOT used in the calculation, because the numeric
// scale of maker/taker fields in MEXC's Binance-compatible API is ambiguous and must be
// confirmed against the official docs / fee page rather than guessed by multiplying.
func CrossCheckAccount(ctx context.Context, client *mexc.Client) []string {
	if client == nil {
		return nil
	}
	acc, err := client.GetAccount(ctx)
	if err != nil {
		return []string{"перекрёстная проверка по account недоступна: " + err.Error()}
	}
	return []string{
		"сырые комиссии из account (ПРОВЕРЬТЕ МАСШТАБ по документации/fee‑странице MEXC и VIP‑уровню, прежде чем доверять): " +
			"makerCommission=" + strconv.FormatInt(acc.MakerCommission, 10) +
			" takerCommission=" + strconv.FormatInt(acc.TakerCommission, 10) +
			" — эти целые числа НЕ применяются автоматически; задайте --taker-bps явно.",
	}
}

func envFloat(key string) (float64, bool) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}