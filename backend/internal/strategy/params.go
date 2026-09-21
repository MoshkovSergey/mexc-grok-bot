package strategy

// Params mirrors the Pine inputs of CTS-CISD with the SAME defaults as the script.
// Editing these is the "tuning surface" of the strategy; see the overfitting risk
// note in README before changing anything based on a single backtest.
type Params struct {
	// Core trend / risk.
	EMAFastLen   int
	EMASlowLen   int
	RSILen       int
	RSIMid       float64
	RSIOverbought float64
	RSIOversold  float64
	VolLen       int
	VolumeMult   float64
	ATRLen       int
	MinATRPct    float64
	MaxATRPct    float64
	HTFTimeframe string // MEXC-style interval string, e.g. "4h" (Pine input.timeframe("240"))
	HTFLen       int

	// Triple Smoothed.
	TSLen     int
	TSsigLen  int
	TSnormLen int
	TSmat     string // EMA | SMA | RMA | WMA
	TSsigMat  string
	SrcType   string // close | open | high | low | hl2 | hlc3 | ohlc4

	// CISD / liquidity.
	SwingLen         int
	ExpiryBars       int
	LiquidityLookback int
	Tolerance        float64
	MaxScan          int
	MaxPending       int

	// Signals / filters.
	ConfirmOnClose bool
	UseEMAFilter   bool
	UseVolumeFilter bool
	UseRSIFilter   bool
	UseATRFIlter   bool
	UseHTFFilter   bool
	SignalMode     string // Any | Confluence | Triple Smoothed only | CISD only
}

// DefaultParams reproduces the Pine script's input defaults exactly.
func DefaultParams() Params {
	return Params{
		EMAFastLen: 21, EMASlowLen: 55,
		RSILen: 14, RSIMid: 50, RSIOverbought: 70, RSIOversold: 30,
		VolLen: 20, VolumeMult: 1.0,
		ATRLen: 14, MinATRPct: 0, MaxATRPct: 8,
		HTFTimeframe: "4h", HTFLen: 200,

		TSLen: 7, TSsigLen: 12, TSnormLen: 70,
		TSmat: "EMA", TSsigMat: "EMA", SrcType: "close",

		SwingLen: 12, ExpiryBars: 100, LiquidityLookback: 10,
		Tolerance: 0.7, MaxScan: 50, MaxPending: 20,

		ConfirmOnClose: true,
		UseEMAFilter: true, UseVolumeFilter: true, UseRSIFilter: true,
		UseATRFIlter: true, UseHTFFilter: true,
		SignalMode: "Confluence",
	}
}