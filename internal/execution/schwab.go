package execution

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

// SchwabRebalanceOrder is a deterministic whole-share preview targeting Safe-QD + Carry ETF proxies.
type SchwabRebalanceOrder struct {
	Symbol         string  `json:"symbol"`
	Side           string  `json:"side"` // BUY or SELL
	QuantityShares int     `json:"quantity_shares"`
	LimitPrice     float64 `json:"limit_price"`
	EstNotionalUSD float64 `json:"est_notional_usd"`
	OrderType      string  `json:"order_type"`
	Instruction    string  `json:"instruction"`
}

// SchwabTokenResponse holds OAuth2 tokens returned by Schwab's `/v1/oauth/token` endpoint.
type SchwabTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// SchwabAccountPositions holds read-only liquidation value and whole-share ETF balances from `/trader/v1/accounts`.
type SchwabAccountPositions struct {
	AccountHash      string         `json:"account_hash,omitempty"`
	LiquidationValue float64        `json:"liquidation_value"`
	CashBalance      float64        `json:"cash_balance"`
	SharesBySymbol   map[string]int `json:"shares_by_symbol"`
}

// SchwabAuthorizeURL returns the OAuth2 consent URL for the updated Schwab Developer API.
func SchwabAuthorizeURL(clientID, redirectURI string) string {
	v := url.Values{}
	v.Set("client_id", strings.TrimSpace(clientID))
	v.Set("redirect_uri", strings.TrimSpace(redirectURI))
	return SchwabAuthURL + "?" + v.Encode()
}

func buildSchwabTokenRequest(ctx context.Context, tokenURL, clientID, clientSecret string, form url.Values) (*http.Request, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return nil, fmt.Errorf("schwab: client_id and client_secret are required")
	}
	if tokenURL == "" {
		tokenURL = SchwabTokenURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	basic := base64.StdEncoding.EncodeToString([]byte(clientID + ":" + clientSecret))
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req, nil
}

func doSchwabTokenRequest(ctx context.Context, client *http.Client, tokenURL, clientID, clientSecret string, form url.Values) (SchwabTokenResponse, error) {
	if client == nil {
		client = NewOptimizedHTTPClient(10 * time.Second)
	}
	req, err := buildSchwabTokenRequest(ctx, tokenURL, clientID, clientSecret, form)
	if err != nil {
		return SchwabTokenResponse{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return SchwabTokenResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return SchwabTokenResponse{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return SchwabTokenResponse{}, fmt.Errorf("schwab token HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out SchwabTokenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return SchwabTokenResponse{}, fmt.Errorf("schwab token decode: %w", err)
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return SchwabTokenResponse{}, fmt.Errorf("schwab token response missing access_token")
	}
	return out, nil
}

// BuildSchwabRefreshRequest constructs an authenticated OAuth2 refresh_token request for Schwab v1.
func BuildSchwabRefreshRequest(ctx context.Context, tokenURL, clientID, clientSecret, refreshToken string) (*http.Request, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("schwab: client_id, client_secret, and refresh_token are required")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	return buildSchwabTokenRequest(ctx, tokenURL, clientID, clientSecret, form)
}

// ExchangeSchwabAuthCode exchanges an OAuth2 authorization code from Schwab's consent redirect
// for access and refresh tokens via `/v1/oauth/token`.
func ExchangeSchwabAuthCode(ctx context.Context, client *http.Client, tokenURL, clientID, clientSecret, code, redirectURI string) (SchwabTokenResponse, error) {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(redirectURI) == "" {
		return SchwabTokenResponse{}, fmt.Errorf("schwab: authorization code and redirect_uri are required")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", strings.TrimSpace(code))
	form.Set("redirect_uri", strings.TrimSpace(redirectURI))
	return doSchwabTokenRequest(ctx, client, tokenURL, clientID, clientSecret, form)
}

// RefreshSchwabToken refreshes an OAuth2 access token using a Schwab v1 refresh token.
func RefreshSchwabToken(ctx context.Context, client *http.Client, tokenURL, clientID, clientSecret, refreshToken string) (SchwabTokenResponse, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return SchwabTokenResponse{}, fmt.Errorf("schwab: refresh_token is required")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", strings.TrimSpace(refreshToken))
	return doSchwabTokenRequest(ctx, client, tokenURL, clientID, clientSecret, form)
}

// FetchSchwabAccountPositions fetches read-only balances and equity/ETF share counts from `/trader/v1/accounts?fields=positions`.
func FetchSchwabAccountPositions(ctx context.Context, client *http.Client, traderBaseURL, accessToken string) (SchwabAccountPositions, error) {
	if strings.TrimSpace(accessToken) == "" {
		return SchwabAccountPositions{}, fmt.Errorf("schwab: access token is required")
	}
	if traderBaseURL == "" {
		traderBaseURL = SchwabTraderBase
	}
	if client == nil {
		client = NewOptimizedHTTPClient(8 * time.Second)
	}
	u := strings.TrimRight(traderBaseURL, "/") + "/accounts?fields=positions"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return SchwabAccountPositions{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return SchwabAccountPositions{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return SchwabAccountPositions{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return SchwabAccountPositions{}, fmt.Errorf("schwab accounts HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var accounts []struct {
		SecuritiesAccount struct {
			AccountNumber string `json:"accountNumber"`
			Positions     []struct {
				LongQuantity  float64 `json:"longQuantity"`
				ShortQuantity float64 `json:"shortQuantity"`
				Instrument    struct {
					Symbol string `json:"symbol"`
				} `json:"instrument"`
			} `json:"positions"`
			CurrentBalances struct {
				LiquidationValue float64 `json:"liquidationValue"`
				CashBalance      float64 `json:"cashBalance"`
			} `json:"currentBalances"`
		} `json:"securitiesAccount"`
	}
	if err := json.Unmarshal(body, &accounts); err != nil {
		return SchwabAccountPositions{}, fmt.Errorf("schwab accounts decode: %w", err)
	}
	out := SchwabAccountPositions{SharesBySymbol: map[string]int{}}
	for _, a := range accounts {
		sa := a.SecuritiesAccount
		out.LiquidationValue += sa.CurrentBalances.LiquidationValue
		out.CashBalance += sa.CurrentBalances.CashBalance
		if out.AccountHash == "" && sa.AccountNumber != "" && len(sa.AccountNumber) >= 4 {
			out.AccountHash = "***" + sa.AccountNumber[len(sa.AccountNumber)-4:]
		}
		for _, p := range sa.Positions {
			sym := strings.ToUpper(strings.TrimSpace(p.Instrument.Symbol))
			if sym == "" {
				continue
			}
			out.SharesBySymbol[sym] += int(math.Round(p.LongQuantity - p.ShortQuantity))
		}
	}
	return out, nil
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
		client = NewOptimizedHTTPClient(8 * time.Second)
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

// SchwabCredentials holds Charles Schwab Developer API v1 OAuth credentials loaded from env or disk.
type SchwabCredentials struct {
	ClientID     string
	ClientSecret string
	RefreshToken string
	RedirectURI  string
}

// Ready reports whether client ID, secret, and refresh token are all populated.
func (c SchwabCredentials) Ready() bool {
	return strings.TrimSpace(c.ClientID) != "" &&
		strings.TrimSpace(c.ClientSecret) != "" &&
		strings.TrimSpace(c.RefreshToken) != ""
}

// DefaultSchwabEnvPath returns ~/.config/piquant/schwab.env.
func DefaultSchwabEnvPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "piquant", "schwab.env")
}

// LoadSchwabCredentials resolves SCHWAB_APP_KEY, SCHWAB_APP_SECRET, SCHWAB_REFRESH_TOKEN, and
// SCHWAB_REDIRECT_URI from process environment first, falling back to envPath (typically ~/.config/piquant/schwab.env).
func LoadSchwabCredentials(envPath string) SchwabCredentials {
	fileVals := map[string]string{}
	if envPath == "" {
		envPath = DefaultSchwabEnvPath()
	}
	if envPath != "" {
		if f, err := os.Open(envPath); err == nil {
			defer f.Close()
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				k, v, ok := strings.Cut(line, "=")
				if !ok {
					continue
				}
				v = strings.TrimSpace(v)
				v = strings.Trim(v, `"'`)
				fileVals[strings.TrimSpace(k)] = v
			}
		}
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				return v
			}
		}
		for _, k := range keys {
			if v := strings.TrimSpace(fileVals[k]); v != "" {
				return v
			}
		}
		return ""
	}
	redir := pick("SCHWAB_REDIRECT_URI")
	if redir == "" {
		redir = "https://127.0.0.1:8443/cb"
	}
	return SchwabCredentials{
		ClientID:     pick("SCHWAB_APP_KEY", "SCHWAB_CLIENT_ID"),
		ClientSecret: pick("SCHWAB_APP_SECRET", "SCHWAB_CLIENT_SECRET"),
		RefreshToken: pick("SCHWAB_REFRESH_TOKEN"),
		RedirectURI:  redir,
	}
}

// SaveSchwabCredentials writes credentials with strict 0600 file permissions (never commits secrets).
func SaveSchwabCredentials(envPath string, c SchwabCredentials) error {
	if envPath == "" {
		envPath = DefaultSchwabEnvPath()
	}
	if envPath == "" {
		return fmt.Errorf("schwab: cannot determine config path")
	}
	if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
		return err
	}
	content := fmt.Sprintf("SCHWAB_APP_KEY=%s\nSCHWAB_APP_SECRET=%s\nSCHWAB_REFRESH_TOKEN=%s\nSCHWAB_REDIRECT_URI=%s\n",
		c.ClientID, c.ClientSecret, c.RefreshToken, c.RedirectURI)
	return os.WriteFile(envPath, []byte(content), 0o600)
}

// SchwabETFAllocation describes the target dollar and percentage allocation for one US ETF proxy.
type SchwabETFAllocation struct {
	Symbol        string  `json:"symbol"`
	Role          string  `json:"role"`
	TargetPct     float64 `json:"target_pct"`
	TargetUSD     float64 `json:"target_usd"`
	CurrentShares int     `json:"current_shares"`
}

// SchwabPortfolioPlan summarizes a read-only Schwab ETF rebalance preview (never submits live orders).
type SchwabPortfolioPlan struct {
	Portfolio          string                 `json:"portfolio"`
	NavUSD             float64                `json:"nav_usd"`
	Connected          bool                   `json:"schwab_connected"`
	AccountHash        string                 `json:"account_hash,omitempty"`
	AccountValueUSD    float64                `json:"account_value_usd,omitempty"`
	Quotes             map[string]SchwabQuote `json:"quotes,omitempty"`
	Allocations        []SchwabETFAllocation  `json:"allocations"`
	CarrySleevePct     float64                `json:"carry_sleeve_pct"`
	CarrySleeveUSD     float64                `json:"carry_sleeve_usd"`
	Orders             []SchwabRebalanceOrder `json:"orders,omitempty"`
	AuthURL            string                 `json:"auth_url,omitempty"`
	Error              string                 `json:"error,omitempty"`
	ExecutionPrinciple string                 `json:"execution_principle"`
}

// BuildSchwabPlanFromTargets computes target ETF dollar weights (`IBIT`, `ETHA`, `SGOV`) and, when
// Schwab v1 credentials are configured, refreshes an OAuth2 access token to fetch read-only positions
// and live NBBO quotes in order to size passive whole-share limit previews.
func BuildSchwabPlanFromTargets(
	ctx context.Context,
	client *http.Client,
	creds SchwabCredentials,
	tokenURL, marketDataBase, traderBase string,
	portfolioName string,
	btcPct, ethPct, idleCashPct, carryPct, navUSD float64,
) SchwabPortfolioPlan {
	autoNav := navUSD <= 0
	if navUSD <= 0 {
		navUSD = 1000.0
	}
	roundUSD := func(v float64) float64 { return math.Round(v*100) / 100 }
	plan := SchwabPortfolioPlan{
		Portfolio:          portfolioName,
		NavUSD:             roundUSD(navUSD),
		CarrySleevePct:     roundUSD(carryPct),
		CarrySleeveUSD:     roundUSD(navUSD * carryPct / 100.0),
		ExecutionPrinciple: "Read-only preview using updated Schwab v1 OAuth2/MarketData/Trader API; never places live orders.",
	}
	if strings.TrimSpace(creds.ClientID) != "" {
		plan.AuthURL = SchwabAuthorizeURL(creds.ClientID, creds.RedirectURI)
	}

	var held map[string]int
	if creds.Ready() {
		tok, err := RefreshSchwabToken(ctx, client, tokenURL, creds.ClientID, creds.ClientSecret, creds.RefreshToken)
		if err != nil {
			plan.Error = err.Error()
		} else {
			plan.Connected = true
			if acct, aerr := FetchSchwabAccountPositions(ctx, client, traderBase, tok.AccessToken); aerr == nil {
				plan.AccountHash = acct.AccountHash
				plan.AccountValueUSD = roundUSD(acct.LiquidationValue)
				held = acct.SharesBySymbol
				if autoNav && acct.LiquidationValue > 0 {
					plan.NavUSD = roundUSD(acct.LiquidationValue)
					plan.CarrySleeveUSD = roundUSD(plan.NavUSD * carryPct / 100.0)
				}
			} else if plan.Error == "" {
				plan.Error = aerr.Error()
			}
			if quotes, qerr := FetchSchwabQuotes(ctx, client, marketDataBase, tok.AccessToken, []string{"IBIT", "ETHA", "SGOV"}); qerr == nil {
				plan.Quotes = quotes
				plan.Orders = PlanSchwabETFRebalance(plan.NavUSD, btcPct, ethPct, idleCashPct, held, quotes)
			} else if plan.Error == "" {
				plan.Error = qerr.Error()
			}
		}
	}

	plan.Allocations = []SchwabETFAllocation{
		{Symbol: "IBIT", Role: "BTC Spot Proxy", TargetPct: roundUSD(btcPct), TargetUSD: roundUSD(plan.NavUSD * btcPct / 100.0), CurrentShares: held["IBIT"]},
		{Symbol: "ETHA", Role: "ETH Spot Proxy", TargetPct: roundUSD(ethPct), TargetUSD: roundUSD(plan.NavUSD * ethPct / 100.0), CurrentShares: held["ETHA"]},
		{Symbol: "SGOV", Role: "Idle Trend T-Bills (0-3m)", TargetPct: roundUSD(idleCashPct), TargetUSD: roundUSD(plan.NavUSD * idleCashPct / 100.0), CurrentShares: held["SGOV"]},
	}
	return plan
}
