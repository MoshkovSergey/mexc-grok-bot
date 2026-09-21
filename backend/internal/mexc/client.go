// Package mexc implements a minimal MEXC Spot REST client.
//
// Official reference:
// https://mexcdevelop.github.io/apidocs/spot_v3_en/
//
// This client is intentionally small. Before live trading, verify:
// - endpoint paths;
// - required parameters;
// - precision filters;
// - rate limits;
// - order status semantics;
// - market buy/sell parameter differences.
package mexc

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client is a MEXC REST API client.
type Client struct {
	baseURL string
	apiKey  string
	secret  string

	httpClient *http.Client
	timeOffset time.Duration
}

// NewClient creates a MEXC client.
func NewClient(baseURL, apiKey, secret string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		secret:  secret,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// Kline represents a normalized OHLCV candle.
type Kline struct {
	OpenTime time.Time `json:"openTime"`
	Open     float64   `json:"open"`
	High     float64   `json:"high"`
	Low      float64   `json:"low"`
	Close    float64   `json:"close"`
	Volume   float64   `json:"volume"`
}

// rawKline accepts both numeric and string JSON values.
//
// MEXC kline responses may contain numbers and/or strings depending on endpoint/version.
// Using json.RawMessage allows us to parse both formats safely.
type rawKline []json.RawMessage

// Balance represents one asset balance from /api/v3/account.
type Balance struct {
	Asset  string `json:"asset"`
	Free   string `json:"free"`
	Locked string `json:"locked"`
}

// Account represents the spot account response.
type Account struct {
	MakerCommission  int64     `json:"makerCommission"`
	TakerCommission  int64     `json:"takerCommission"`
	BuyerCommission  int64     `json:"buyerCommission"`
	SellerCommission int64     `json:"sellerCommission"`
	CanTrade         bool      `json:"canTrade"`
	CanWithdraw      bool      `json:"canWithdraw"`
	CanDeposit       bool      `json:"canDeposit"`
	UpdateTime       int64     `json:"updateTime"`
	Balances         []Balance `json:"balances"`
}

// OrderResponse represents a subset of order placement response.
type OrderResponse struct {
	Symbol      string `json:"symbol"`
	OrderID     int64  `json:"orderId"`
	OrigQty     string `json:"origQty"`
	ExecutedQty string `json:"executedQty"`
	Status      string `json:"status"`
	Side        string `json:"side"`
	Type        string `json:"type"`
	Time        int64  `json:"time"`
}

// SyncTime fetches MEXC server time and stores local offset.
// Private signed requests require accurate timestamps.
func (c *Client) SyncTime(ctx context.Context) error {
	var resp struct {
		ServerTime int64 `json:"serverTime"`
	}

	data, err := c.do(ctx, http.MethodGet, "/api/v3/time", nil, false, nil)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("decode server time: %w", err)
	}

	if resp.ServerTime <= 0 {
		return fmt.Errorf("invalid server time: %d", resp.ServerTime)
	}

	// time.Until(t) is equivalent to t.Sub(time.Now()), but idiomatic.
	c.timeOffset = time.Until(time.UnixMilli(resp.ServerTime))

	return nil
}

// GetKlines fetches public candlestick data.
//
// Endpoint:
// GET /api/v3/klines
//
// Example parameters:
// symbol=BTCUSDT
// interval=1m
// limit=100
func (c *Client) GetKlines(ctx context.Context, symbol, interval string, limit int) ([]Kline, error) {
	q := url.Values{}
	q.Set("symbol", symbol)
	q.Set("interval", interval)
	q.Set("limit", strconv.Itoa(limit))

	data, err := c.do(ctx, http.MethodGet, "/api/v3/klines", q, false, nil)
	if err != nil {
		return nil, err
	}

	rawCandles, err := decodeKlines(data)
	if err != nil {
		return nil, err
	}

	out := make([]Kline, 0, len(rawCandles))

	for i, raw := range rawCandles {
		// We need at least:
		// 0: open time
		// 1: open
		// 2: high
		// 3: low
		// 4: close
		// 5: volume
		if len(raw) < 6 {
			return nil, fmt.Errorf("kline %d too short: expected at least 6 fields, got %d", i, len(raw))
		}

		openTimeMS, err := parseInt64(raw[0])
		if err != nil {
			return nil, fmt.Errorf("kline %d openTime: %w", i, err)
		}

		open, err := parseFloat(raw[1])
		if err != nil {
			return nil, fmt.Errorf("kline %d open: %w", i, err)
		}

		high, err := parseFloat(raw[2])
		if err != nil {
			return nil, fmt.Errorf("kline %d high: %w", i, err)
		}

		low, err := parseFloat(raw[3])
		if err != nil {
			return nil, fmt.Errorf("kline %d low: %w", i, err)
		}

		closePrice, err := parseFloat(raw[4])
		if err != nil {
			return nil, fmt.Errorf("kline %d close: %w", i, err)
		}

		volume, err := parseFloat(raw[5])
		if err != nil {
			return nil, fmt.Errorf("kline %d volume: %w", i, err)
		}

		out = append(out, Kline{
			OpenTime: time.UnixMilli(openTimeMS),
			Open:     open,
			High:     high,
			Low:      low,
			Close:    closePrice,
			Volume:   volume,
		})
	}

	return out, nil
}

// GetAccount fetches private spot account balances.
func (c *Client) GetAccount(ctx context.Context) (*Account, error) {
	data, err := c.do(ctx, http.MethodGet, "/api/v3/account", url.Values{}, true, nil)
	if err != nil {
		return nil, err
	}

	var acc Account
	if err := json.Unmarshal(data, &acc); err != nil {
		return nil, fmt.Errorf("decode account: %w", err)
	}

	return &acc, nil
}

// PlaceMarketBuyQuote places a market BUY order using quote quantity.
//
// WARNING:
// MEXC may require different parameters for market buy vs market sell.
// Confirm exact fields in official documentation before enabling live trading.
func (c *Client) PlaceMarketBuyQuote(ctx context.Context, symbol string, quoteQty float64) (*OrderResponse, error) {
	if quoteQty <= 0 {
		return nil, fmt.Errorf("quoteQty must be positive")
	}

	q := url.Values{}
	q.Set("symbol", symbol)
	q.Set("side", "BUY")
	q.Set("type", "MARKET")
	q.Set("quoteOrderQty", formatFloat(quoteQty))

	data, err := c.do(ctx, http.MethodPost, "/api/v3/order", q, true, nil)
	if err != nil {
		return nil, err
	}

	var resp OrderResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decode market buy response: %w", err)
	}

	return &resp, nil
}

// PlaceMarketSellBase places a market SELL order using base quantity.
func (c *Client) PlaceMarketSellBase(ctx context.Context, symbol string, baseQty float64) (*OrderResponse, error) {
	if baseQty <= 0 {
		return nil, fmt.Errorf("baseQty must be positive")
	}

	q := url.Values{}
	q.Set("symbol", symbol)
	q.Set("side", "SELL")
	q.Set("type", "MARKET")
	q.Set("quantity", formatFloat(baseQty))

	data, err := c.do(ctx, http.MethodPost, "/api/v3/order", q, true, nil)
	if err != nil {
		return nil, err
	}

	var resp OrderResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decode market sell response: %w", err)
	}

	return &resp, nil
}

// do performs an HTTP request to MEXC.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, signed bool, body io.Reader) ([]byte, error) {
	if query == nil {
		query = url.Values{}
	}

	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return nil, err
	}

	if signed {
		if c.apiKey == "" || c.secret == "" {
			return nil, fmt.Errorf("MEXC API key and secret are required for signed endpoints")
		}

		query.Set("timestamp", strconv.FormatInt(c.nowMillis(), 10))
		query.Set("recvWindow", "5000")

		// Signature is HMAC-SHA256 over the query string, excluding signature itself.
		qs := query.Encode()
		mac := hmac.New(sha256.New, []byte(c.secret))
		if _, err := mac.Write([]byte(qs)); err != nil {
			return nil, err
		}
		signature := hex.EncodeToString(mac.Sum(nil))
		query.Set("signature", signature)
	}

	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if signed && c.apiKey != "" {
		// MEXC Spot API commonly uses this header for API key authentication.
		req.Header.Set("X-MEXC-APIKEY", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mexc api %s %s: status=%d body=%s", method, path, resp.StatusCode, string(data))
	}

	return data, nil
}

func (c *Client) nowMillis() int64 {
	return time.Now().Add(c.timeOffset).UnixMilli()
}

// decodeKlines parses MEXC kline response.
//
// Expected successful response is usually an array:
// [
//   [openTime, open, high, low, close, volume, ...],
//   ...
// ]
//
// Error responses may be objects:
// {
//   "code": ...,
//   "msg": "..."
// }
func decodeKlines(data []byte) ([]rawKline, error) {
	trimmed := bytes.TrimSpace(data)

	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty response body")
	}

	// If response starts with '{', try to decode as MEXC error object.
	if trimmed[0] == '{' {
		var apiErr struct {
			Code    int    `json:"code"`
			Msg     string `json:"msg"`
			Message string `json:"message"`
		}

		if err := json.Unmarshal(trimmed, &apiErr); err == nil {
			msg := apiErr.Msg
			if msg == "" {
				msg = apiErr.Message
			}

			if apiErr.Code != 0 || msg != "" {
				return nil, fmt.Errorf("mexc api error: code=%d msg=%s", apiErr.Code, msg)
			}
		}
	}

	var raw []rawKline
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return nil, fmt.Errorf("decode klines: %w; raw_prefix=%s", err, prefixForError(trimmed))
	}

	return raw, nil
}

// parseInt64 parses JSON value that may be number or string.
func parseInt64(raw json.RawMessage) (int64, error) {
	raw = bytes.TrimSpace(raw)

	if len(raw) == 0 {
		return 0, fmt.Errorf("empty value")
	}

	if string(raw) == "null" {
		return 0, fmt.Errorf("null value")
	}

	// Try native int64.
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, nil
	}

	// Try string containing integer.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return 0, fmt.Errorf("empty string value")
		}
		return strconv.ParseInt(s, 10, 64)
	}

	// Try float, then truncate. Useful if API returns 1730000000000.0.
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return int64(f), nil
	}

	return 0, fmt.Errorf("cannot parse int64 from %s", string(raw))
}

// parseFloat parses JSON value that may be number or string.
func parseFloat(raw json.RawMessage) (float64, error) {
	raw = bytes.TrimSpace(raw)

	if len(raw) == 0 {
		return 0, fmt.Errorf("empty value")
	}

	if string(raw) == "null" {
		return 0, fmt.Errorf("null value")
	}

	// Try native float64.
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f, nil
	}

	// Try string containing decimal number.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return 0, fmt.Errorf("empty string value")
		}
		return strconv.ParseFloat(s, 64)
	}

	return 0, fmt.Errorf("cannot parse float64 from %s", string(raw))
}

func formatFloat(f float64) string {
	// Avoid scientific notation for API query parameters.
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func prefixForError(data []byte) string {
	const maxLen = 240
	if len(data) <= maxLen {
		return string(data)
	}
	return string(data[:maxLen]) + "..."
}