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

var defaultCoinbaseHTTPClient = NewOptimizedHTTPClient(10 * time.Second)

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

// CoinbaseSpotClient implements VenueClient and BatchQuoteClient against Coinbase Advanced Trade v3 REST API.
type CoinbaseSpotClient struct {
	BaseURL       string
	HTTPClient    *http.Client
	AllowUnauthed bool

	creds CoinbaseCredentials
	now   func() time.Time

	authMu       sync.RWMutex
	authCached   bool
	cachedSecret string
	isCDP        bool
	ecKey        *ecdsa.PrivateKey
	ecKeyErr     error
	hmacKey      []byte

	specMu sync.RWMutex
	specs  map[string]coinbaseProductSpec
}

var (
	cbReadBufPool = sync.Pool{
		New: func() any {
			return bytes.NewBuffer(make([]byte, 0, 8<<10))
		},
	}
	cbJWTBufPool = sync.Pool{
		New: func() any {
			return bytes.NewBuffer(make([]byte, 0, 512))
		},
	}
)

// NewCoinbaseSpotClient constructs a CoinbaseSpotClient with optional custom baseURL and httpClient.
func NewCoinbaseSpotClient(creds CoinbaseCredentials, baseURL string, httpClient *http.Client) *CoinbaseSpotClient {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultCoinbaseBaseURL
	}
	if httpClient == nil {
		httpClient = NewOptimizedHTTPClient(10 * time.Second)
	}
	creds.APISecret = normalizePEMSecret(creds.APISecret)
	c := &CoinbaseSpotClient{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: httpClient,
		creds:      creds,
		now:        time.Now,
		specs:      make(map[string]coinbaseProductSpec),
	}
	c.initAuthCacheLocked()
	return c
}

func (c *CoinbaseSpotClient) initAuthCacheLocked() {
	sec := normalizePEMSecret(c.creds.APISecret)
	c.cachedSecret = sec
	c.isCDP = strings.Contains(sec, "BEGIN EC PRIVATE KEY") || strings.Contains(sec, "BEGIN PRIVATE KEY")
	c.ecKey = nil
	c.ecKeyErr = nil
	c.hmacKey = nil
	if sec != "" {
		if c.isCDP {
			c.ecKey, c.ecKeyErr = parseECPrivateKey(sec)
		} else {
			c.hmacKey = []byte(sec)
		}
	}
	c.authCached = true
}

func (c *CoinbaseSpotClient) cachedSigner() (isCDP bool, apiKey string, ecKey *ecdsa.PrivateKey, hmacKey []byte, err error) {
	c.authMu.RLock()
	if c.authCached && c.cachedSecret == normalizePEMSecret(c.creds.APISecret) {
		isCDP, apiKey, ecKey, hmacKey, err = c.isCDP, c.creds.APIKey, c.ecKey, c.hmacKey, c.ecKeyErr
		c.authMu.RUnlock()
		return isCDP, apiKey, ecKey, hmacKey, err
	}
	c.authMu.RUnlock()

	c.authMu.Lock()
	defer c.authMu.Unlock()
	if !c.authCached || c.cachedSecret != normalizePEMSecret(c.creds.APISecret) {
		c.initAuthCacheLocked()
	}
	return c.isCDP, c.creds.APIKey, c.ecKey, c.hmacKey, c.ecKeyErr
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
	seenCursors := make(map[string]struct{})
	const maxPages = 50
	for page := 0; page < maxPages; page++ {
		if cursor != "" {
			seenCursors[cursor] = struct{}{}
		}
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
		nextCursor := strings.TrimSpace(resp.Cursor)
		if !resp.HasNext || nextCursor == "" {
			break
		}
		if _, seen := seenCursors[nextCursor]; seen {
			break
		}
		cursor = nextCursor
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

// GetQuotes fetches top-of-book quotes for multiple symbols. When authenticated (or AllowUnauthed),
// it batches all product_ids into a single GET /api/v3/brokerage/best_bid_ask call and concurrently
// fetches any missing symbols via GetQuote. When unauthenticated, it fetches each symbol concurrently.
func (c *CoinbaseSpotClient) GetQuotes(ctx context.Context, symbols []string) (map[string]Quote, error) {
	seen := make(map[string]struct{}, len(symbols))
	clean := make([]string, 0, len(symbols))
	for _, raw := range symbols {
		sym := strings.ToUpper(strings.TrimSpace(raw))
		if sym == "" {
			continue
		}
		if _, dup := seen[sym]; dup {
			continue
		}
		seen[sym] = struct{}{}
		clean = append(clean, sym)
	}
	if len(clean) == 0 {
		return map[string]Quote{}, nil
	}
	if len(clean) == 1 {
		q, err := c.GetQuote(ctx, clean[0])
		if err != nil {
			return nil, err
		}
		return map[string]Quote{clean[0]: q}, nil
	}

	out := make(map[string]Quote, len(clean))
	signed := c.creds.Ready()
	if signed || c.AllowUnauthed {
		var pbURL strings.Builder
		pbURL.WriteString("/api/v3/brokerage/best_bid_ask?")
		for i, sym := range clean {
			if i > 0 {
				pbURL.WriteByte('&')
			}
			pbURL.WriteString("product_ids=")
			pbURL.WriteString(url.QueryEscape(sym))
		}
		var resp struct {
			Pricebooks []coinbasePricebook `json:"pricebooks"`
		}
		if err := c.doJSON(ctx, http.MethodGet, pbURL.String(), nil, signed, &resp); err != nil {
			return nil, fmt.Errorf("coinbase best_bid_ask batch: %w", err)
		}
		for _, pb := range resp.Pricebooks {
			pid := strings.ToUpper(strings.TrimSpace(pb.ProductID))
			if pid == "" {
				continue
			}
			q, qerr := pricebookToQuote(pid, pb, "")
			if qerr != nil {
				continue
			}
			if _, wanted := seen[q.Symbol]; wanted {
				out[q.Symbol] = q
			} else if _, wantedPID := seen[pid]; wantedPID {
				out[pid] = q
			}
		}
	}

	// Fetch any symbols not already populated (unauthed path or single-product mock servers).
	missing := make([]string, 0, len(clean))
	for _, sym := range clean {
		if _, ok := out[sym]; !ok {
			missing = append(missing, sym)
		}
	}
	if len(missing) == 0 {
		return out, nil
	}

	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	wg.Add(len(missing))
	for _, sym := range missing {
		go func(s string) {
			defer wg.Done()
			q, err := c.GetQuote(ctx, s)
			mu.Lock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
			} else {
				out[s] = q
			}
			mu.Unlock()
		}(sym)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
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
		spec, err := c.lookupProductSpec(ctx, sym)
		if err != nil {
			return OrderReceipt{}, fmt.Errorf("coinbase place order: %w", err)
		}
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
		if strings.TrimSpace(msg) == "" {
			msg = "unknown failure"
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
	if strings.TrimSpace(orderID) == "" {
		return OrderReceipt{}, errors.New("coinbase place order: empty order_id in success response")
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
		if r.OrderID == orderID || (len(resp.Results) == 1 && strings.TrimSpace(r.OrderID) == "") {
			if !r.Success {
				reason := strings.TrimSpace(r.FailureReason)
				if reason == "" {
					reason = "unknown failure"
				}
				return fmt.Errorf("coinbase cancel %s failed: %s", orderID, reason)
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
		var statusErr *httpStatusError
		if errors.As(err, &statusErr) && statusErr.status == http.StatusNotFound {
			return OrderReceipt{}, fmt.Errorf("coinbase get_order %s: %w: %w", orderID, ErrOrderNotFound, err)
		}
		return OrderReceipt{}, fmt.Errorf("coinbase get_order %s: %w", orderID, err)
	}
	ord := raw.coinbaseHistoricalOrder
	if raw.Order != nil {
		ord = *raw.Order
	}
	return parseCoinbaseHistoricalOrder(ord, orderID), nil
}

// GetOrderByClientOrderID looks up an order by its client_order_id via
// GET /api/v3/brokerage/orders/historical/batch (used when server order_id is not yet known).
func (c *CoinbaseSpotClient) GetOrderByClientOrderID(ctx context.Context, symbol, clientOrderID string) (OrderReceipt, error) {
	if err := c.requireAuth(); err != nil {
		return OrderReceipt{}, err
	}
	clientOrderID = strings.TrimSpace(clientOrderID)
	if clientOrderID == "" {
		return OrderReceipt{}, errors.New("coinbase get_order_by_client_id: client_order_id is required")
	}
	sym := strings.ToUpper(strings.TrimSpace(symbol))

	const maxPages = 50
	cursor := ""
	seenCursors := make(map[string]struct{})
	for page := 0; page < maxPages; page++ {
		if cursor != "" {
			seenCursors[cursor] = struct{}{}
		}
		q := url.Values{}
		q.Set("limit", "100")
		if sym != "" {
			q.Set("product_ids", sym)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		path := "/api/v3/brokerage/orders/historical/batch?" + q.Encode()
		var resp struct {
			Orders  []coinbaseHistoricalOrder `json:"orders"`
			HasNext bool                      `json:"has_next"`
			Cursor  string                    `json:"cursor"`
		}
		if err := c.doJSON(ctx, http.MethodGet, path, nil, true, &resp); err != nil {
			return OrderReceipt{}, fmt.Errorf("coinbase get_order_by_client_id %s: %w", clientOrderID, err)
		}
		for _, ord := range resp.Orders {
			if strings.TrimSpace(ord.ClientOrderID) == clientOrderID {
				return parseCoinbaseHistoricalOrder(ord, ord.OrderID), nil
			}
		}
		nextCursor := strings.TrimSpace(resp.Cursor)
		if !resp.HasNext || nextCursor == "" {
			break
		}
		if _, seen := seenCursors[nextCursor]; seen {
			break
		}
		cursor = nextCursor
	}
	return OrderReceipt{}, fmt.Errorf("coinbase get_order_by_client_id %s: %w", clientOrderID, ErrOrderNotFound)
}

func parseCoinbaseHistoricalOrder(ord coinbaseHistoricalOrder, fallbackOrderID string) OrderReceipt {
	receipt := OrderReceipt{
		OrderID:       ord.OrderID,
		ClientOrderID: ord.ClientOrderID,
		Symbol:        strings.ToUpper(strings.TrimSpace(ord.ProductID)),
		Side:          strings.ToUpper(strings.TrimSpace(ord.Side)),
		Status:        strings.ToUpper(strings.TrimSpace(ord.Status)),
		DryRun:        false,
	}
	if receipt.OrderID == "" {
		receipt.OrderID = fallbackOrderID
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
	return receipt
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

func isIdempotentCoinbaseRequest(method, pathWithQuery string) bool {
	if strings.EqualFold(method, http.MethodGet) {
		return true
	}
	if strings.EqualFold(method, http.MethodPost) {
		pathOnly := strings.SplitN(pathWithQuery, "?", 2)[0]
		return strings.HasSuffix(pathOnly, "/orders/batch_cancel")
	}
	return false
}

func parseRetryAfter(header string, now time.Time) (time.Duration, bool) {
	s := strings.TrimSpace(header)
	if s == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(s); err == nil {
		if secs < 0 {
			secs = 0
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(s); err == nil {
		d := t.Sub(now)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

func (c *CoinbaseSpotClient) doJSON(ctx context.Context, method, pathWithQuery string, bodyObj any, sign bool, out any) error {
	var bodyBytes []byte
	if bodyObj != nil {
		var err error
		bodyBytes, err = json.Marshal(bodyObj)
		if err != nil {
			return err
		}
	}
	fullURL := c.baseURL() + pathWithQuery
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = defaultCoinbaseHTTPClient
	}

	const maxAttempts = 4
	const maxRetryAfter = 15 * time.Second
	retryable := isIdempotentCoinbaseRequest(method, pathWithQuery)

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		statusCode, retryAfter, err := c.doJSONAttempt(ctx, httpClient, method, fullURL, bodyBytes, sign, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable || attempt == maxAttempts-1 || ctx.Err() != nil {
			return lastErr
		}
		isRetryableStatus := statusCode == 0 || statusCode == http.StatusTooManyRequests || (statusCode >= 500 && statusCode <= 599)
		if !isRetryableStatus {
			return lastErr
		}

		delay := 100 * time.Millisecond << uint(attempt)
		if statusCode == http.StatusTooManyRequests {
			if ra, ok := parseRetryAfter(retryAfter, c.clock()); ok {
				delay = ra
			}
		}
		if delay > maxRetryAfter {
			delay = maxRetryAfter
		}
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return lastErr
}

func (c *CoinbaseSpotClient) doJSONAttempt(
	ctx context.Context,
	httpClient *http.Client,
	method, fullURL string,
	bodyBytes []byte,
	sign bool,
	out any,
) (int, string, error) {
	var bodyReader io.Reader
	if bodyBytes != nil {
		bodyReader = bytes.NewReader(bodyBytes)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Accept", "application/json")
	if bodyBytes != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if sign && c.creds.Ready() {
		if err := c.applyAuth(req, bodyBytes, c.clock()); err != nil {
			return 0, "", err
		}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	buf := cbReadBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer func() {
		if buf.Cap() <= 256<<10 {
			buf.Reset()
			cbReadBufPool.Put(buf)
		}
	}()
	if _, err := buf.ReadFrom(io.LimitReader(resp.Body, 4<<20)); err != nil {
		return 0, "", err
	}
	respBytes := buf.Bytes()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, resp.Header.Get("Retry-After"), &httpStatusError{
			status: resp.StatusCode,
			body:   strings.TrimSpace(string(respBytes)),
		}
	}
	if out != nil && len(bytes.TrimSpace(respBytes)) > 0 {
		if err := json.Unmarshal(respBytes, out); err != nil {
			return resp.StatusCode, "", fmt.Errorf("decode response: %w", err)
		}
	}
	return resp.StatusCode, "", nil
}

func (c *CoinbaseSpotClient) applyAuth(req *http.Request, body []byte, now time.Time) error {
	isCDP, apiKey, ecKey, hmacKey, err := c.cachedSigner()
	if err != nil {
		return fmt.Errorf("coinbase jwt sign: %w", err)
	}
	method := strings.ToUpper(req.Method)
	reqPath := req.URL.Path
	if isCDP {
		token, serr := buildCoinbaseCDPJWTWithKey(apiKey, ecKey, method, req.URL.Host, reqPath, now)
		if serr != nil {
			return fmt.Errorf("coinbase jwt sign: %w", serr)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
	return applyCoinbaseHMAC(req, apiKey, hmacKey, method, reqPath, body, now)
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
	return applyCoinbaseHMAC(req, creds.APIKey, []byte(creds.APISecret), method, reqPath, body, now)
}

func applyCoinbaseHMAC(req *http.Request, apiKey string, secret []byte, method, reqPath string, body []byte, now time.Time) error {
	var tsBuf [24]byte
	tsSlice := strconv.AppendInt(tsBuf[:0], now.UTC().Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(tsSlice)
	_, _ = io.WriteString(mac, method)
	_, _ = io.WriteString(mac, reqPath)
	if len(body) > 0 {
		_, _ = mac.Write(body)
	}
	var sumBuf [sha256.Size]byte
	sum := mac.Sum(sumBuf[:0])
	var hexBuf [sha256.Size * 2]byte
	hex.Encode(hexBuf[:], sum)
	req.Header.Set("CB-ACCESS-KEY", apiKey)
	req.Header.Set("CB-ACCESS-SIGN", string(hexBuf[:]))
	req.Header.Set("CB-ACCESS-TIMESTAMP", string(tsSlice))
	return nil
}

func buildCoinbaseCDPJWTWithKey(apiKey string, priv *ecdsa.PrivateKey, method, host, reqPath string, now time.Time) (string, error) {
	if priv == nil {
		return "", errors.New("nil EC private key")
	}
	if priv.Curve != elliptic.P256() {
		return "", fmt.Errorf("unsupported EC curve %s (expected P-256)", priv.Curve.Params().Name)
	}
	var nonceBytes [16]byte
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		return "", err
	}
	var nonceHex [32]byte
	hex.Encode(nonceHex[:], nonceBytes[:])

	jsonBuf := cbJWTBufPool.Get().(*bytes.Buffer)
	tokBuf := cbJWTBufPool.Get().(*bytes.Buffer)
	defer func() {
		if jsonBuf.Cap() <= 8<<10 {
			jsonBuf.Reset()
			cbJWTBufPool.Put(jsonBuf)
		}
		if tokBuf.Cap() <= 8<<10 {
			tokBuf.Reset()
			cbJWTBufPool.Put(tokBuf)
		}
	}()

	// Header JSON -> base64url
	jsonBuf.Reset()
	b := jsonBuf.Bytes()[:0]
	b = append(b, `{"alg":"ES256","kid":`...)
	b = strconv.AppendQuote(b, apiKey)
	b = append(b, `,"nonce":"`...)
	b = append(b, nonceHex[:]...)
	b = append(b, `","typ":"JWT"}`...)

	var encStack [512]byte
	hdrEncLen := base64.RawURLEncoding.EncodedLen(len(b))
	var hdrEnc []byte
	if hdrEncLen <= len(encStack) {
		hdrEnc = encStack[:hdrEncLen]
	} else {
		hdrEnc = make([]byte, hdrEncLen)
	}
	base64.RawURLEncoding.Encode(hdrEnc, b)

	tokBuf.Reset()
	tokBuf.Grow(hdrEncLen + 512)
	tokBuf.Write(hdrEnc)
	tokBuf.WriteByte('.')

	// Claims JSON -> base64url
	issuedAt := now.UTC().Unix()
	var uriBuf [256]byte
	uri := uriBuf[:0]
	uri = append(uri, strings.ToUpper(method)...)
	uri = append(uri, ' ')
	uri = append(uri, host...)
	uri = append(uri, reqPath...)

	b = b[:0]
	b = append(b, `{"iss":"cdp","nbf":`...)
	b = strconv.AppendInt(b, issuedAt, 10)
	b = append(b, `,"exp":`...)
	b = strconv.AppendInt(b, issuedAt+120, 10)
	b = append(b, `,"sub":`...)
	b = strconv.AppendQuote(b, apiKey)
	b = append(b, `,"uri":`...)
	b = strconv.AppendQuote(b, string(uri))
	b = append(b, '}')

	claimsEncLen := base64.RawURLEncoding.EncodedLen(len(b))
	var claimsEnc []byte
	if claimsEncLen <= len(encStack) {
		claimsEnc = encStack[:claimsEncLen]
	} else {
		claimsEnc = make([]byte, claimsEncLen)
	}
	base64.RawURLEncoding.Encode(claimsEnc, b)
	tokBuf.Write(claimsEnc)

	digest := sha256.Sum256(tokBuf.Bytes())
	r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
	if err != nil {
		return "", err
	}
	if r.BitLen() > 256 || s.BitLen() > 256 {
		return "", errors.New("ecdsa signature exceeds 256-bit P-256 size")
	}
	var rawSig [64]byte
	r.FillBytes(rawSig[:32])
	s.FillBytes(rawSig[32:])
	var sigEnc [86]byte // base64.RawURLEncoding.EncodedLen(64) == 86
	base64.RawURLEncoding.Encode(sigEnc[:], rawSig[:])

	tokBuf.WriteByte('.')
	tokBuf.Write(sigEnc[:])
	return tokBuf.String(), nil
}

func buildCoinbaseCDPJWT(apiKey, pemSecret, method, host, reqPath string, now time.Time) (string, error) {
	priv, err := parseECPrivateKey(pemSecret)
	if err != nil {
		return "", err
	}
	return buildCoinbaseCDPJWTWithKey(apiKey, priv, method, host, reqPath, now)
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

func (c *CoinbaseSpotClient) lookupProductSpec(ctx context.Context, sym string) (coinbaseProductSpec, error) {
	c.specMu.RLock()
	if spec, ok := c.specs[sym]; ok {
		c.specMu.RUnlock()
		return spec, nil
	}
	c.specMu.RUnlock()

	var resp struct {
		BaseIncrement  string `json:"base_increment"`
		QuoteIncrement string `json:"quote_increment"`
	}
	path := "/api/v3/brokerage/market/products/" + url.PathEscape(sym)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, false, &resp); err != nil {
		return coinbaseProductSpec{}, fmt.Errorf("coinbase lookup product spec %s: %w", sym, err)
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
	return spec, nil
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
	var stack [64]byte
	b := strconv.AppendFloat(stack[:0], rounded, 'f', maxDecimals, 64)
	if bytes.IndexByte(b, '.') >= 0 {
		for len(b) > 0 && b[len(b)-1] == '0' {
			b = b[:len(b)-1]
		}
		if len(b) > 0 && b[len(b)-1] == '.' {
			b = b[:len(b)-1]
		}
	}
	if len(b) == 0 || (len(b) == 2 && b[0] == '-' && b[1] == '0') {
		return "0"
	}
	return string(b)
}
