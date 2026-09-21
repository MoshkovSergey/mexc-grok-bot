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

// rawKline matches MEXC array-of-arrays kline response.
type rawKline []string

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
		return err
	}

	c.timeOffset = time.UnixMilli(resp.ServerTime).Sub(time.Now())
	return nil
}

// GetKlines fetches public candlestick data.
func (c *Client) GetKlines(ctx context.Context, symbol, interval string, limit int) ([]Kline, error) {
	q := url.Values{}
	q.Set("symbol", symbol)
	q.Set("interval", interval)
	q.Set("limit", strconv.Itoa(limit))

	data, err := c.do(ctx, http.MethodGet, "/api/v3/klines", q, false, nil)
	if err != nil {
		return nil, err
	}

	var raw []rawKline
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	out := make([]Kline, 0, len(raw))
	for _, r := range raw {
		if len(r) < 6 {
			continue
		}

		openTime, err := strconv.ParseInt(r[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse kline openTime: %w", err)
		}

		open, err := strconv.ParseFloat(r[1], 64)
		if err != nil {
			return nil, fmt.Errorf("parse kline open: %w", err)
		}

		high, err := strconv.ParseFloat(r[2], 64)
		if err != nil {
			return nil, fmt.Errorf("parse kline high: %w", err)
		}

		low, err := strconv.ParseFloat(r[3], 64)
		if err != nil {
			return nil, fmt.Errorf("parse kline low: %w", err)
		}

		closePrice, err := strconv.ParseFloat(r[4], 64)
		if err != nil {
			return nil, fmt.Errorf("parse kline close: %w", err)
		}

		volume, err := strconv.ParseFloat(r[5], 64)
		if err != nil {
			return nil, fmt.Errorf("parse kline volume: %w", err)
		}

		out = append(out, Kline{
			OpenTime: time.UnixMilli(openTime),
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
		return nil, err
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
		return nil, err
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
		return nil, err
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

func formatFloat(f float64) string {
	// Avoid scientific notation for API query parameters.
	return strconv.FormatFloat(f, 'f', -1, 64)
}