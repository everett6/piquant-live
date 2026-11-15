package execution

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// SchwabAuthURL is the OAuth2 authorization endpoint for the updated Charles Schwab Developer API.
	SchwabAuthURL = "https://api.schwabapi.com/v1/oauth/authorize"
	// SchwabTokenURL is the OAuth2 token/refresh endpoint replacing legacy TD Ameritrade endpoints.
	SchwabTokenURL = "https://api.schwabapi.com/v1/oauth/token"
	// SchwabMarketDataBase is the v1 Market Data API base URL.
	SchwabMarketDataBase = "https://api.schwabapi.com/marketdata/v1"
	// SchwabTraderBase is the v1 Trader API base URL.
	SchwabTraderBase = "https://api.schwabapi.com/trader/v1"
)

// SchwabQuote holds top-of-book NBBO and mark prices for a US-listed ETF proxy (e.g. IBIT, ETHA, SGOV).
type SchwabQuote struct {
	Symbol    string  `json:"symbol"`
	BidPrice  float64 `json:"bid_price"`
	AskPrice  float64 `json:"ask_price"`
	LastPrice float64 `json:"last_price"`
	SpreadBps float64 `json:"spread_bps"`
}

// SchwabRebalanceOrder is a deterministic whole-share preview targeting S36_qd_carry_blend ETF proxies.
type SchwabRebalanceOrder struct {
	Symbol         string  `json:"symbol"`
	Side           string  `json:"side"` // BUY or SELL
	QuantityShares int     `json:"quantity_shares"`
	LimitPrice     float64 `json:"limit_price"`
	EstNotionalUSD float64 `json:"est_notional_usd"`
	OrderType      string  `json:"order_type"`
	Instruction    string  `json:"instruction"`
}

// SchwabAuthorizeURL returns the OAuth2 consent URL for the updated Schwab Developer API.
func SchwabAuthorizeURL(clientID, redirectURI string) string {
	v := url.Values{}
	v.Set("client_id", strings.TrimSpace(clientID))
	v.Set("redirect_uri", strings.TrimSpace(redirectURI))
	return SchwabAuthURL + "?" + v.Encode()
}

// BuildSchwabRefreshRequest constructs an authenticated OAuth2 refresh_token request for Schwab v1.
func BuildSchwabRefreshRequest(ctx context.Context, tokenURL, clientID, clientSecret, refreshToken string) (*http.Request, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" || strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("schwab: client_id, client_secret, and refresh_token are required")
	}
	if tokenURL == "" {
		tokenURL = SchwabTokenURL
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	basic := base64.StdEncoding.EncodeToString([]byte(clientID + ":" + clientSecret))
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req, nil
}

// FetchSchwabQuotes fetches real-time quotes via `/marketdata/v1/quotes` using a valid OAuth2 access token.
func FetchSchwabQuotes(ctx context.Context, client *http.Client, baseURL, accessToken string, symbols []string) (map[string]SchwabQuote, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("schwab: access token is required")
	}
	if len(symbols) == 0 {
		return map[string]SchwabQuote{}, nil
	}
	if baseURL == "" {
		baseURL = SchwabMarketDataBase
	}
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	u := strings.TrimRight(baseURL, "/") + "/quotes?symbols=" + url.QueryEscape(strings.Join(symbols, ",")) + "&fields=quote"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("schwab quotes HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var raw map[string]struct {
		Quote struct {
			BidPrice  float64 `json:"bidPrice"`
			AskPrice  float64 `json:"askPrice"`
			LastPrice float64 `json:"lastPrice"`
		} `json:"quote"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("schwab quotes decode: %w", err)
	}
	out := make(map[string]SchwabQuote, len(raw))
	for sym, item := range raw {
		q := SchwabQuote{
			Symbol:    sym,
			BidPrice:  item.Quote.BidPrice,
			AskPrice:  item.Quote.AskPrice,
			LastPrice: item.Quote.LastPrice,
		}
		if q.BidPrice > 0 && q.AskPrice >= q.BidPrice {
			mid := 0.5 * (q.BidPrice + q.AskPrice)
			q.SpreadBps = math.Round((q.AskPrice-q.BidPrice)/mid*1e6) / 100.0
		}
		out[sym] = q
	}
	return out, nil
}

// PlanSchwabETFRebalance maps S36_qd_carry_blend portfolio target weights (BTC%, ETH%, idle Cash%)
// into passive limit order previews on commission-free US ETFs (`IBIT`, `ETHA`, `SGOV`).
// It never submits orders and enforces a minimum $25 order notional filter to avoid tiny churn.
func PlanSchwabETFRebalance(
	navUSD float64,
	targetBTCPct, targetETHPct, idleCashPct float64,
	heldShares map[string]int,
	quotes map[string]SchwabQuote,
) []SchwabRebalanceOrder {
	if navUSD <= 0 {
		return nil
	}
	targets := []struct {
		sym string
		pct float64
	}{
		{"IBIT", targetBTCPct},
		{"ETHA", targetETHPct},
		{"SGOV", idleCashPct},
	}
	var orders []SchwabRebalanceOrder
	for _, t := range targets {
		q, ok := quotes[t.sym]
		if !ok {
			continue
		}
		mid := q.LastPrice
		if q.BidPrice > 0 && q.AskPrice > 0 {
			mid = 0.5 * (q.BidPrice + q.AskPrice)
		}
		if mid <= 0 {
			continue
		}
		desiredShares := int(math.Floor((navUSD * (t.pct / 100.0)) / mid))
		curShares := 0
		if heldShares != nil {
			curShares = heldShares[t.sym]
		}
		diff := desiredShares - curShares
		if diff == 0 {
			continue
		}
		side := "BUY"
		qty := diff
		limitPx := q.BidPrice
		if limitPx <= 0 {
			limitPx = mid
		}
		if diff < 0 {
			side = "SELL"
			qty = -diff
			if q.AskPrice > 0 {
				limitPx = q.AskPrice
			}
		}
		notional := float64(qty) * limitPx
		if notional < 25.0 {
			continue
		}
		orders = append(orders, SchwabRebalanceOrder{
			Symbol:         t.sym,
			Side:           side,
			QuantityShares: qty,
			LimitPrice:     math.Round(limitPx*100) / 100,
			EstNotionalUSD: math.Round(notional*100) / 100,
			OrderType:      "LIMIT",
			Instruction:    side,
		})
	}
	return orders
}
