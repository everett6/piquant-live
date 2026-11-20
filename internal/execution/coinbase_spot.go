package execution

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultCoinbaseBaseURL is the production Coinbase Advanced Trade v3 REST base URL.
const DefaultCoinbaseBaseURL = "https://api.coinbase.com"

// CoinbaseCredentials holds Coinbase Advanced Trade v3 API credentials (either CDP ES256 key or legacy HMAC key).
type CoinbaseCredentials struct {
	APIKey    string
	APISecret string
}

// Ready reports whether both API key and secret are non-empty.
func (c CoinbaseCredentials) Ready() bool {
	return strings.TrimSpace(c.APIKey) != "" && strings.TrimSpace(c.APISecret) != ""
}

// IsCDPKey reports whether APISecret contains a PEM-encoded EC/PKCS8 private key for CDP ES256 JWT auth.
func (c CoinbaseCredentials) IsCDPKey() bool {
	s := normalizePEMSecret(c.APISecret)
	return strings.Contains(s, "BEGIN EC PRIVATE KEY") || strings.Contains(s, "BEGIN PRIVATE KEY")
}

// DefaultCoinbaseEnvPath returns ~/.config/piquant/coinbase.env.
func DefaultCoinbaseEnvPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "piquant", "coinbase.env")
}

// LoadCoinbaseCredentials reads COINBASE_API_KEY and COINBASE_API_SECRET from process env first,
// falling back to envPath (typically ~/.config/piquant/coinbase.env).
func LoadCoinbaseCredentials(envPath string) CoinbaseCredentials {
	if envPath == "" {
		envPath = DefaultCoinbaseEnvPath()
	}
	fileVals := parseEnvFile(envPath)
	pick := func(key string) string {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
		return strings.TrimSpace(fileVals[key])
	}
	return CoinbaseCredentials{
		APIKey:    pick("COINBASE_API_KEY"),
		APISecret: normalizePEMSecret(pick("COINBASE_API_SECRET")),
	}
}

// SaveCoinbaseCredentials persists credentials to envPath with strict 0600 file permissions.
func SaveCoinbaseCredentials(envPath string, c CoinbaseCredentials) error {
	if envPath == "" {
		envPath = DefaultCoinbaseEnvPath()
	}
	if envPath == "" {
		return errors.New("coinbase: cannot determine config path")
	}
	if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
		return err
	}
	cleanSecret := strings.ReplaceAll(strings.TrimSpace(c.APISecret), "\r\n", "\n")
	content := fmt.Sprintf("COINBASE_API_KEY=%s\nCOINBASE_API_SECRET=%q\n",
		strings.TrimSpace(c.APIKey), cleanSecret)
	return os.WriteFile(envPath, []byte(content), 0o600)
}

func parseEnvFile(path string) map[string]string {
	vals := map[string]string{}
	if path == "" {
		return vals
	}
	f, err := os.Open(path)
	if err != nil {
		return vals
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	var curKey string
	var curQuote byte
	var curVal strings.Builder

	for sc.Scan() {
		raw := sc.Text()
		if curKey != "" {
			curVal.WriteByte('\n')
			trimmedRight := strings.TrimRight(raw, " \t\r")
			if strings.HasSuffix(trimmedRight, string(curQuote)) && !strings.HasSuffix(trimmedRight, `\`+string(curQuote)) {
				curVal.WriteString(strings.TrimSuffix(trimmedRight, string(curQuote)))
				vals[curKey] = curVal.String()
				curKey = ""
				curQuote = 0
				curVal.Reset()
			} else {
				curVal.WriteString(raw)
			}
			continue
		}

		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(k), "export "))
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
			q := v[0]
			if strings.HasSuffix(v[1:], string(q)) {
				vals[k] = v[1 : len(v)-1]
			} else {
				curKey = k
				curQuote = q
				curVal.WriteString(v[1:])
			}
			continue
		}
		vals[k] = v
	}
	return vals
}

func normalizePEMSecret(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, `\n`, "\n")
	return s
}

type coinbaseProductSpec struct {
	baseIncrement  float64
	quoteIncrement float64
}

// CoinbaseSpotClient implements VenueClient against Coinbase Advanced Trade v3 REST API.
type CoinbaseSpotClient struct {
	BaseURL       string
	HTTPClient    *http.Client
	AllowUnauthed bool

	creds CoinbaseCredentials
	now   func() time.Time

	specMu sync.Mutex
	specs  map[string]coinbaseProductSpec
}

// NewCoinbaseSpotClient constructs a CoinbaseSpotClient with optional custom baseURL and httpClient.
func NewCoinbaseSpotClient(creds CoinbaseCredentials, baseURL string, httpClient *http.Client) *CoinbaseSpotClient {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultCoinbaseBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	creds.APISecret = normalizePEMSecret(creds.APISecret)
	return &CoinbaseSpotClient{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: httpClient,
		creds:      creds,
		now:        time.Now,
		specs:      make(map[string]coinbaseProductSpec),
	}
}

// Name returns the canonical venue identifier.
func (c *CoinbaseSpotClient) Name() string {
	return "coinbase-spot"
}

// GetBalances queries GET /api/v3/brokerage/accounts and returns available balances keyed by currency.
func (c *CoinbaseSpotClient) GetBalances(ctx context.Context) (map[string]float64, error) {
	if err := c.requireAuth(); err != nil {
		return nil, err
	}
	balances := make(map[string]float64)
	cursor := ""
	for {
		path := "/api/v3/brokerage/accounts?limit=250"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var resp struct {
			Accounts []struct {
				Currency         string `json:"currency"`
				AvailableBalance struct {
					Value    string `json:"value"`
					Currency string `json:"currency"`
				} `json:"available_balance"`
			} `json:"accounts"`
			HasNext bool   `json:"has_next"`
			Cursor  string `json:"cursor"`
		}
		if err := c.doJSON(ctx, http.MethodGet, path, nil, true, &resp); err != nil {
			return nil, fmt.Errorf("coinbase accounts: %w", err)
		}
		for _, acct := range resp.Accounts {
			ccy := strings.ToUpper(strings.TrimSpace(acct.Currency))
			if ccy == "" {
				ccy = strings.ToUpper(strings.TrimSpace(acct.AvailableBalance.Currency))
			}
			if ccy == "" {
				continue
			}
			val, err := strconv.ParseFloat(strings.TrimSpace(acct.AvailableBalance.Value), 64)
			if err != nil || math.IsNaN(val) || math.IsInf(val, 0) {
				continue
			}
			balances[ccy] += val
		}
		if !resp.HasNext || strings.TrimSpace(resp.Cursor) == "" {
			break
		}
		cursor = resp.Cursor
	}
	return balances, nil
}

type coinbasePriceLevel struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

type coinbasePricebook struct {
	ProductID string               `json:"product_id"`
	Bids      []coinbasePriceLevel `json:"bids"`
	Asks      []coinbasePriceLevel `json:"asks"`
	Time      string               `json:"time"`
}

// GetQuote retrieves top-of-book bid/ask/mid for symbol (e.g., "BTC-USD").
// When credentials are configured (or AllowUnauthed is true), it queries /api/v3/brokerage/best_bid_ask.
// When unauthenticated against production api.coinbase.com, it uses the public /api/v3/brokerage/market/product_book endpoint.
func (c *CoinbaseSpotClient) GetQuote(ctx context.Context, symbol string) (Quote, error) {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	if sym == "" {
		return Quote{}, errors.New("coinbase quote: symbol is required")
	}

	if c.creds.Ready() || c.AllowUnauthed {
		return c.fetchBestBidAsk(ctx, sym, c.creds.Ready())
	}

	// Unauthenticated dry-run / public market data path.
	if c.baseURL() == DefaultCoinbaseBaseURL {
		return c.fetchPublicProductBook(ctx, sym)
	}
	// Custom test servers may expose either best_bid_ask or market/product_book without auth.
	q, err := c.fetchBestBidAsk(ctx, sym, false)
	if err == nil {
		return q, nil
	}
	var httpErr *httpStatusError
	if errors.As(err, &httpErr) && (httpErr.status == http.StatusNotFound || httpErr.status == http.StatusUnauthorized) {
		if pubQ, pubErr := c.fetchPublicProductBook(ctx, sym); pubErr == nil {
			return pubQ, nil
		}
	}
	return Quote{}, err
}

func (c *CoinbaseSpotClient) fetchBestBidAsk(ctx context.Context, sym string, signed bool) (Quote, error) {
	path := "/api/v3/brokerage/best_bid_ask?product_ids=" + url.QueryEscape(sym)
	var resp struct {
		Pricebooks []coinbasePricebook `json:"pricebooks"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, signed, &resp); err != nil {
		return Quote{}, fmt.Errorf("coinbase best_bid_ask %s: %w", sym, err)
	}
	for _, pb := range resp.Pricebooks {
		if strings.EqualFold(pb.ProductID, sym) || len(resp.Pricebooks) == 1 {
			return pricebookToQuote(sym, pb, "")
		}
	}
	return Quote{}, fmt.Errorf("coinbase best_bid_ask: no pricebook returned for %s", sym)
}

func (c *CoinbaseSpotClient) fetchPublicProductBook(ctx context.Context, sym string) (Quote, error) {
	path := "/api/v3/brokerage/market/product_book?product_id=" + url.QueryEscape(sym) + "&limit=1"
	var resp struct {
		Pricebook coinbasePricebook `json:"pricebook"`
		MidMarket string            `json:"mid_market"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, false, &resp); err != nil {
		return Quote{}, fmt.Errorf("coinbase product_book %s: %w", sym, err)
	}
	return pricebookToQuote(sym, resp.Pricebook, resp.MidMarket)
}

func pricebookToQuote(sym string, pb coinbasePricebook, midStr string) (Quote, error) {
	if len(pb.Bids) == 0 || len(pb.Asks) == 0 {
		return Quote{}, fmt.Errorf("coinbase quote %s: empty bid/ask book", sym)
	}
	bid, err := strconv.ParseFloat(strings.TrimSpace(pb.Bids[0].Price), 64)
	if err != nil || !isFinitePositive(bid) {
		return Quote{}, fmt.Errorf("coinbase quote %s: invalid bid %q", sym, pb.Bids[0].Price)
	}
	ask, err := strconv.ParseFloat(strings.TrimSpace(pb.Asks[0].Price), 64)
	if err != nil || !isFinitePositive(ask) || ask < bid {
		return Quote{}, fmt.Errorf("coinbase quote %s: invalid ask %q (bid=%.8f)", sym, pb.Asks[0].Price, bid)
	}
	mid := 0.5 * (bid + ask)
	if m, merr := strconv.ParseFloat(strings.TrimSpace(midStr), 64); merr == nil && isFinitePositive(m) {
		mid = m
	}
	var ts time.Time
	if strings.TrimSpace(pb.Time) != "" {
		if parsed, perr := time.Parse(time.RFC3339Nano, strings.TrimSpace(pb.Time)); perr == nil {
			ts = parsed.UTC()
		} else if parsed, perr := time.Parse(time.RFC3339, strings.TrimSpace(pb.Time)); perr == nil {
			ts = parsed.UTC()
		}
	}
	outSym := sym
	if strings.TrimSpace(pb.ProductID) != "" {
		outSym = strings.ToUpper(strings.TrimSpace(pb.ProductID))
	}
	return Quote{
		Symbol:    outSym,
		Bid:       bid,
		Ask:       ask,
		Mid:       mid,
		Timestamp: ts,
	}, nil
}

type coinbaseOrderConfig struct {
	LimitLimitGTC *coinbaseLimitGTC `json:"limit_limit_gtc,omitempty"`
}

type coinbaseLimitGTC struct {
	BaseSize   string `json:"base_size"`
	LimitPrice string `json:"limit_price"`
	PostOnly   bool   `json:"post_only"`
}

// PlaceLimitOrder submits a strictly post-only GTC limit order via POST /api/v3/brokerage/orders.
// Market orders are never permitted and post_only is always forced to true.
func (c *CoinbaseSpotClient) PlaceLimitOrder(ctx context.Context, req LimitOrderRequest) (OrderReceipt, error) {
	if err := c.requireAuth(); err != nil {
		return OrderReceipt{}, err
	}
	sym := strings.ToUpper(strings.TrimSpace(req.Symbol))
	side := strings.ToUpper(strings.TrimSpace(req.Side))
	if sym == "" || (side != "BUY" && side != "SELL") || !isFinitePositive(req.Price) || !isFinitePositive(req.Quantity) {
		return OrderReceipt{}, fmt.Errorf("coinbase order: invalid order parameters (symbol=%q side=%q price=%v qty=%v)", req.Symbol, req.Side, req.Price, req.Quantity)
	}

	price := req.Price
	qty := req.Quantity
	if c.baseURL() == DefaultCoinbaseBaseURL {
		spec := c.lookupProductSpec(ctx, sym)
		if spec.quoteIncrement > 0 {
			price = roundPricePassive(price, spec.quoteIncrement, side)
		}
		if spec.baseIncrement > 0 {
			qty = floorToStep(qty, spec.baseIncrement)
		}
		if !isFinitePositive(price) || !isFinitePositive(qty) {
			return OrderReceipt{}, fmt.Errorf("coinbase order: rounded price/qty non-positive for %s", sym)
		}
	}

	payload := struct {
		ClientOrderID      string              `json:"client_order_id"`
		ProductID          string              `json:"product_id"`
		Side               string              `json:"side"`
		OrderConfiguration coinbaseOrderConfig `json:"order_configuration"`
	}{
		ClientOrderID: req.ClientOrderID,
		ProductID:     sym,
		Side:          side,
		OrderConfiguration: coinbaseOrderConfig{
			LimitLimitGTC: &coinbaseLimitGTC{
				BaseSize:   formatDecimal(qty, 8),
				LimitPrice: formatDecimal(price, 8),
				PostOnly:   true, // Hard safety invariant: always maker post-only, never market.
			},
		},
	}

	var resp struct {
		Success         bool   `json:"success"`
		FailureReason   string `json:"failure_reason"`
		OrderID         string `json:"order_id"`
		SuccessResponse *struct {
			OrderID       string `json:"order_id"`
			ProductID     string `json:"product_id"`
			Side          string `json:"side"`
			ClientOrderID string `json:"client_order_id"`
		} `json:"success_response"`
		ErrorResponse *struct {
			Error                string `json:"error"`
			Message              string `json:"message"`
			ErrorDetails         string `json:"error_details"`
			PreviewFailureReason string `json:"preview_failure_reason"`
		} `json:"error_response"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v3/brokerage/orders", payload, true, &resp); err != nil {
		return OrderReceipt{}, fmt.Errorf("coinbase place order: %w", err)
	}
	if !resp.Success {
		msg := resp.FailureReason
		if resp.ErrorResponse != nil {
			for _, candidate := range []string{resp.ErrorResponse.Message, resp.ErrorResponse.ErrorDetails, resp.ErrorResponse.Error, resp.ErrorResponse.PreviewFailureReason} {
				if strings.TrimSpace(candidate) != "" {
					msg = candidate
					break
				}
			}
		}
		return OrderReceipt{}, fmt.Errorf("coinbase place order rejected: %s", msg)
	}

	orderID := resp.OrderID
	clientOrderID := req.ClientOrderID
	if resp.SuccessResponse != nil {
		if strings.TrimSpace(resp.SuccessResponse.OrderID) != "" {
			orderID = resp.SuccessResponse.OrderID
		}
		if strings.TrimSpace(resp.SuccessResponse.ClientOrderID) != "" {
			clientOrderID = resp.SuccessResponse.ClientOrderID
		}
	}
	return OrderReceipt{
		OrderID:       orderID,
		ClientOrderID: clientOrderID,
		Symbol:        sym,
		Side:          side,
		Price:         price,
		Quantity:      qty,
		PostOnly:      true,
		Status:        "OPEN",
		FilledQty:     0,
		DryRun:        false,
		SubmittedAt:   c.clock().UTC(),
	}, nil
}

// CancelOrder cancels an open order via POST /api/v3/brokerage/orders/batch_cancel.
func (c *CoinbaseSpotClient) CancelOrder(ctx context.Context, orderID string) error {
	if err := c.requireAuth(); err != nil {
		return err
	}
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return errors.New("coinbase cancel: order_id is required")
	}
	payload := struct {
		OrderIDs []string `json:"order_ids"`
	}{
		OrderIDs: []string{orderID},
	}
	var resp struct {
		Results []struct {
			Success       bool   `json:"success"`
			FailureReason string `json:"failure_reason"`
			OrderID       string `json:"order_id"`
		} `json:"results"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v3/brokerage/orders/batch_cancel", payload, true, &resp); err != nil {
		return fmt.Errorf("coinbase batch_cancel %s: %w", orderID, err)
	}
	for _, r := range resp.Results {
		if r.OrderID == orderID || len(resp.Results) == 1 {
			if !r.Success {
				return fmt.Errorf("coinbase cancel %s failed: %s", orderID, r.FailureReason)
			}
			return nil
		}
	}
	return fmt.Errorf("coinbase cancel %s: no cancel result returned", orderID)
}

type coinbaseHistoricalOrder struct {
	OrderID            string              `json:"order_id"`
	ClientOrderID      string              `json:"client_order_id"`
	ProductID          string              `json:"product_id"`
	Side               string              `json:"side"`
	Status             string              `json:"status"`
	FilledSize         string              `json:"filled_size"`
	AverageFilledPrice string              `json:"average_filled_price"`
	CreatedTime        string              `json:"created_time"`
	OrderConfiguration coinbaseOrderConfig `json:"order_configuration"`
}

// GetOrder fetches order status via GET /api/v3/brokerage/orders/historical/{id}.
func (c *CoinbaseSpotClient) GetOrder(ctx context.Context, orderID string) (OrderReceipt, error) {
	if err := c.requireAuth(); err != nil {
		return OrderReceipt{}, err
	}
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return OrderReceipt{}, errors.New("coinbase get_order: order_id is required")
	}
	path := "/api/v3/brokerage/orders/historical/" + url.PathEscape(orderID)
	var raw struct {
		Order *coinbaseHistoricalOrder `json:"order"`
		coinbaseHistoricalOrder
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, true, &raw); err != nil {
		return OrderReceipt{}, fmt.Errorf("coinbase get_order %s: %w", orderID, err)
	}
	ord := raw.coinbaseHistoricalOrder
	if raw.Order != nil {
		ord = *raw.Order
	}
	receipt := OrderReceipt{
		OrderID:       ord.OrderID,
		ClientOrderID: ord.ClientOrderID,
		Symbol:        strings.ToUpper(strings.TrimSpace(ord.ProductID)),
		Side:          strings.ToUpper(strings.TrimSpace(ord.Side)),
		Status:        strings.ToUpper(strings.TrimSpace(ord.Status)),
		DryRun:        false,
	}
	if receipt.OrderID == "" {
		receipt.OrderID = orderID
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(ord.FilledSize), 64); err == nil && !math.IsNaN(v) {
		receipt.FilledQty = v
	}
	if cfg := ord.OrderConfiguration.LimitLimitGTC; cfg != nil {
		receipt.PostOnly = cfg.PostOnly
		if v, err := strconv.ParseFloat(strings.TrimSpace(cfg.LimitPrice), 64); err == nil && !math.IsNaN(v) {
			receipt.Price = v
		}
		if v, err := strconv.ParseFloat(strings.TrimSpace(cfg.BaseSize), 64); err == nil && !math.IsNaN(v) {
			receipt.Quantity = v
		}
	}
	if receipt.Price == 0 {
		if v, err := strconv.ParseFloat(strings.TrimSpace(ord.AverageFilledPrice), 64); err == nil && !math.IsNaN(v) {
			receipt.Price = v
		}
	}
	if strings.TrimSpace(ord.CreatedTime) != "" {
		if ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(ord.CreatedTime)); err == nil {
			receipt.SubmittedAt = ts.UTC()
		} else if ts, err := time.Parse(time.RFC3339, strings.TrimSpace(ord.CreatedTime)); err == nil {
			receipt.SubmittedAt = ts.UTC()
		}
	}
	return receipt, nil
}

func (c *CoinbaseSpotClient) requireAuth() error {
	if c.creds.Ready() || c.AllowUnauthed {
		return nil
	}
	return fmt.Errorf("coinbase: API credentials not configured (set COINBASE_API_KEY and COINBASE_API_SECRET or %s)", DefaultCoinbaseEnvPath())
}

func (c *CoinbaseSpotClient) baseURL() string {
	b := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if b == "" {
		return DefaultCoinbaseBaseURL
	}
	return b
}

func (c *CoinbaseSpotClient) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

type httpStatusError struct {
	status int
	body   string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.status, e.body)
}

func (c *CoinbaseSpotClient) doJSON(ctx context.Context, method, pathWithQuery string, bodyObj any, sign bool, out any) error {
	var bodyBytes []byte
	var bodyReader io.Reader
	if bodyObj != nil {
		var err error
		bodyBytes, err = json.Marshal(bodyObj)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}
	fullURL := c.baseURL() + pathWithQuery
	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if bodyObj != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if sign && c.creds.Ready() {
		if err := applyCoinbaseAuth(req, c.creds, bodyBytes, c.clock()); err != nil {
			return err
		}
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &httpStatusError{status: resp.StatusCode, body: strings.TrimSpace(string(respBytes))}
	}
	if out != nil && len(bytes.TrimSpace(respBytes)) > 0 {
		if err := json.Unmarshal(respBytes, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func applyCoinbaseAuth(req *http.Request, creds CoinbaseCredentials, body []byte, now time.Time) error {
	method := strings.ToUpper(req.Method)
	reqPath := req.URL.Path
	if creds.IsCDPKey() {
		token, err := buildCoinbaseCDPJWT(creds.APIKey, creds.APISecret, method, req.URL.Host, reqPath, now)
		if err != nil {
			return fmt.Errorf("coinbase jwt sign: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}

	ts := strconv.FormatInt(now.UTC().Unix(), 10)
	prehash := ts + method + reqPath + string(body)
	mac := hmac.New(sha256.New, []byte(creds.APISecret))
	_, _ = mac.Write([]byte(prehash))
	sig := hex.EncodeToString(mac.Sum(nil))
	req.Header.Set("CB-ACCESS-KEY", creds.APIKey)
	req.Header.Set("CB-ACCESS-SIGN", sig)
	req.Header.Set("CB-ACCESS-TIMESTAMP", ts)
	return nil
}

func buildCoinbaseCDPJWT(apiKey, pemSecret, method, host, reqPath string, now time.Time) (string, error) {
	priv, err := parseECPrivateKey(pemSecret)
	if err != nil {
		return "", err
	}
	if priv.Curve != elliptic.P256() {
		return "", fmt.Errorf("unsupported EC curve %s (expected P-256)", priv.Curve.Params().Name)
	}
	var nonceBytes [16]byte
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		return "", err
	}

	headerJSON, err := json.Marshal(struct {
		Alg   string `json:"alg"`
		Kid   string `json:"kid"`
		Nonce string `json:"nonce"`
		Typ   string `json:"typ"`
	}{
		Alg:   "ES256",
		Kid:   apiKey,
		Nonce: hex.EncodeToString(nonceBytes[:]),
		Typ:   "JWT",
	})
	if err != nil {
		return "", err
	}

	issuedAt := now.UTC().Unix()
	claimsJSON, err := json.Marshal(struct {
		Iss string `json:"iss"`
		Nbf int64  `json:"nbf"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
		URI string `json:"uri"`
	}{
		Iss: "cdp",
		Nbf: issuedAt,
		Exp: issuedAt + 120,
		Sub: apiKey,
		URI: fmt.Sprintf("%s %s%s", strings.ToUpper(method), host, reqPath),
	})
	if err != nil {
		return "", err
	}

	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
	if err != nil {
		return "", err
	}
	if r.BitLen() > 256 || s.BitLen() > 256 {
		return "", errors.New("ecdsa signature exceeds 256-bit P-256 size")
	}
	rawSig := make([]byte, 64)
	r.FillBytes(rawSig[:32])
	s.FillBytes(rawSig[32:])

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(rawSig), nil
}

func parseECPrivateKey(pemSecret string) (*ecdsa.PrivateKey, error) {
	normalized := normalizePEMSecret(pemSecret)
	block, _ := pem.Decode([]byte(normalized))
	if block == nil {
		return nil, errors.New("invalid PEM private key")
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	pkcs8Key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	ecKey, ok := pkcs8Key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("PKCS#8 key is not an ECDSA private key")
	}
	return ecKey, nil
}

func (c *CoinbaseSpotClient) lookupProductSpec(ctx context.Context, sym string) coinbaseProductSpec {
	c.specMu.Lock()
	if spec, ok := c.specs[sym]; ok {
		c.specMu.Unlock()
		return spec
	}
	c.specMu.Unlock()

	var resp struct {
		BaseIncrement  string `json:"base_increment"`
		QuoteIncrement string `json:"quote_increment"`
	}
	path := "/api/v3/brokerage/market/products/" + url.PathEscape(sym)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, false, &resp); err != nil {
		return coinbaseProductSpec{}
	}
	spec := coinbaseProductSpec{}
	if v, err := strconv.ParseFloat(strings.TrimSpace(resp.BaseIncrement), 64); err == nil && isFinitePositive(v) {
		spec.baseIncrement = v
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(resp.QuoteIncrement), 64); err == nil && isFinitePositive(v) {
		spec.quoteIncrement = v
	}
	c.specMu.Lock()
	c.specs[sym] = spec
	c.specMu.Unlock()
	return spec
}

func floorToStep(v, step float64) float64 {
	if step <= 0 {
		return v
	}
	return math.Floor(v/step+1e-9) * step
}

func roundPricePassive(price, step float64, side string) float64 {
	if step <= 0 {
		return price
	}
	if strings.EqualFold(side, "SELL") {
		return math.Ceil(price/step-1e-9) * step
	}
	return math.Floor(price/step+1e-9) * step
}

func formatDecimal(v float64, maxDecimals int) string {
	factor := math.Pow10(maxDecimals)
	rounded := math.Round(v*factor) / factor
	s := strconv.FormatFloat(rounded, 'f', maxDecimals, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}
