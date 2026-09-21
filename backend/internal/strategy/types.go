// Package strategy is a local Go port of Pine indicators (CTS-CISD and
// Structural Liquidity & POC Matrix). It is a FAITHFUL port, not a byte-for-byte
// clone of TradingView's ta.* semantics; validate against TradingView on identical
// candles before trusting signals (see README). Non-repaint: callers feed only
// confirmed (closed) candles.
//
// Licensing note: the Smart Money / POC port (smartmoney.go) is an adaptation of
// BigBeluga's work under CC BY-NC-SA 4.0 (non-commercial, share-alike, attribution).
// See the header of smartmoney.go and the README license section.
package strategy

import (
	"strings"
	"time"
)

// Candle is one OHLCV bar. INPUT type: stays plain float64 and is never serialized
// to the API, so it does not need NaN handling.
type Candle struct {
	OpenTime time.Time
	Open     float64
	High     float64
	Low      float64
	Close    float64
	Volume   float64
}

// BarState is the per-bar CTS output serialized to the dashboard. All price /
// indicator fields are NaNFloat so Pine `na` becomes JSON null on the wire.
type BarState struct {
	Time time.Time `json:"time"`

	EMAFast  NaNFloat `json:"emaFast"`
	EMASlow  NaNFloat `json:"emaSlow"`
	RSI      NaNFloat `json:"rsi"`
	ATRPct   NaNFloat `json:"atrPct"`
	VolRatio NaNFloat `json:"volRatio"`
	HTFEma   NaNFloat `json:"htfEma"`

	TSV1      NaNFloat `json:"tsV1"`
	TSV2      NaNFloat `json:"tsV2"`
	TSSymDist NaNFloat `json:"tsSymDist"`

	TSBull    bool `json:"tsBull"`
	TSBear    bool `json:"tsBear"`
	CISDBull  bool `json:"cisdBull"`
	CISDBear  bool `json:"cisdBear"`
	BullSweep bool `json:"bullSweep"`
	BearSweep bool `json:"bearSweep"`

	EMALongOK    bool `json:"emaLongOk"`
	EMAShortOK   bool `json:"emaShortOk"`
	VolumeOK     bool `json:"volumeOk"`
	RSILongOK    bool `json:"rsiLongOk"`
	RSIShortOK   bool `json:"rsiShortOk"`
	ATROK        bool `json:"atrOk"`
	HTFLongOK    bool `json:"htfLongOk"`
	HTFShortOK   bool `json:"htfShortOk"`
	LongFilters  bool `json:"longFilters"`
	ShortFilters bool `json:"shortFilters"`

	LongSignal  bool `json:"longSignal"`
	ShortSignal bool `json:"shortSignal"`
}

// Result is the CTS output over the supplied confirmed candles.
type Result struct {
	PerBar []BarState `json:"perBar"`

	Side        string `json:"side"` // "LONG" | "SHORT" | ""
	LongSignal  bool   `json:"longSignal"`
	ShortSignal bool   `json:"shortSignal"`

	CISDTrend    int      `json:"cisdTrend"`
	LastWickHigh NaNFloat `json:"lastWickHigh"`
	LastWickLow  NaNFloat `json:"lastWickLow"`
	Progressed   bool     `json:"progressed"`
}

// IntervalSeconds maps a MEXC/Pine-style interval string to its duration.
// Returns ok=false for unknown values (caller then treats all bars as confirmed,
// the conservative-but-noisy fallback; prefer fixing the interval string).
func IntervalSeconds(interval string) (time.Duration, bool) {
	switch strings.TrimSpace(strings.ToLower(interval)) {
	case "1m":
		return time.Minute, true
	case "3m":
		return 3 * time.Minute, true
	case "5m":
		return 5 * time.Minute, true
	case "15m":
		return 15 * time.Minute, true
	case "30m":
		return 30 * time.Minute, true
	case "1h":
		return time.Hour, true
	case "2h":
		return 2 * time.Hour, true
	case "4h":
		return 4 * time.Hour, true
	case "6h":
		return 6 * time.Hour, true
	case "8h":
		return 8 * time.Hour, true
	case "12h":
		return 12 * time.Hour, true
	case "1d":
		return 24 * time.Hour, true
	case "3d":
		return 72 * time.Hour, true
	case "1w":
		return 168 * time.Hour, true
	}
	return 0, false
}