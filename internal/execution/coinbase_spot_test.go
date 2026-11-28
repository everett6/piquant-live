package execution

import (
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
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoadAndSaveCoinbaseCredentials(t *testing.T) {
	t.Setenv("COINBASE_API_KEY", "")
	t.Setenv("COINBASE_API_SECRET", "")
	dir := t.TempDir()
	envPath := filepath.Join(dir, "coinbase.env")

	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))

	creds := CoinbaseCredentials{
		APIKey:    "organizations/org-1/apiKeys/key-1",
		APISecret: pemStr,
	}
	if err := SaveCoinbaseCredentials(envPath, creds); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(envPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 perms, got %v err=%v", info.Mode().Perm(), err)
	}

	loaded := LoadCoinbaseCredentials(envPath)
	if !loaded.Ready() || !loaded.IsCDPKey() {
		t.Fatalf("expected loaded CDP credentials ready, got %+v", loaded)
	}
	if loaded.APIKey != creds.APIKey || strings.TrimSpace(loaded.APISecret) != strings.TrimSpace(pemStr) {
		t.Fatalf("round-trip credential mismatch")
	}

	// Process environment variables override disk values
	t.Setenv("COINBASE_API_KEY", "env-key-override")
	t.Setenv("COINBASE_API_SECRET", "env-hmac-secret")
	overridden := LoadCoinbaseCredentials(envPath)
	if overridden.APIKey != "env-key-override" || overridden.APISecret != "env-hmac-secret" || overridden.IsCDPKey() {
		t.Fatalf("env override mismatch: %+v", overridden)
	}
}

func TestCoinbaseSpotHMACAuthAndAllEndpoints(t *testing.T) {
	const apiKey = "hmac-key-123"
	const apiSecret = "hmac-secret-xyz"
	var sawPostOnly bool
	var sawBatchCancel bool
	var sawPagination bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		// Verify HMAC-SHA256 headers
		gotKey := r.Header.Get("CB-ACCESS-KEY")
		gotTS := r.Header.Get("CB-ACCESS-TIMESTAMP")
		gotSig := r.Header.Get("CB-ACCESS-SIGN")
		if gotKey != apiKey || gotTS == "" || gotSig == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		prehash := gotTS + r.Method + r.URL.Path + string(body)
		mac := hmac.New(sha256.New, []byte(apiSecret))
		_, _ = mac.Write([]byte(prehash))
		wantSig := hex.EncodeToString(mac.Sum(nil))
		if gotSig != wantSig {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("bad signature"))
			return
		}

		switch r.URL.Path {
		case "/api/v3/brokerage/accounts":
			cursor := r.URL.Query().Get("cursor")
			if cursor == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"accounts": []map[string]any{
						{"currency": "USD", "available_balance": map[string]string{"value": "1250.50", "currency": "USD"}},
						{"currency": "BTC", "available_balance": map[string]string{"value": "0.025", "currency": "BTC"}},
					},
					"has_next": true,
					"cursor":   "page2",
				})
				return
			}
			sawPagination = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"accounts": []map[string]any{
					{"currency": "ETH", "available_balance": map[string]string{"value": "0.5", "currency": "ETH"}},
				},
				"has_next": false,
			})

		case "/api/v3/brokerage/best_bid_ask":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pricebooks": []map[string]any{
					{
						"product_id": "BTC-USD",
						"bids":       []map[string]string{{"price": "62000.00", "size": "1.2"}},
						"asks":       []map[string]string{{"price": "62002.00", "size": "0.8"}},
						"time":       "2026-11-20T00:05:00Z",
					},
				},
			})

		case "/api/v3/brokerage/orders":
			var reqPayload map[string]any
			if err := json.Unmarshal(body, &reqPayload); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			oc, _ := reqPayload["order_configuration"].(map[string]any)
			gtc, _ := oc["limit_limit_gtc"].(map[string]any)
			if gtc == nil || gtc["post_only"] != true {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("missing post_only=true"))
				return
			}
			if _, hasMarket := oc["market_market_ioc"]; hasMarket {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			sawPostOnly = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"success_response": map[string]string{
					"order_id":        "cb-ord-999",
					"product_id":      "BTC-USD",
					"side":            "BUY",
					"client_order_id": "cid-1",
				},
			})

		case "/api/v3/brokerage/orders/batch_cancel":
			var reqPayload struct {
				OrderIDs []string `json:"order_ids"`
			}
			_ = json.Unmarshal(body, &reqPayload)
			if len(reqPayload.OrderIDs) != 1 || reqPayload.OrderIDs[0] != "cb-ord-999" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			sawBatchCancel = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{
					{"success": true, "failure_reason": "", "order_id": "cb-ord-999"},
				},
			})

		case "/api/v3/brokerage/orders/historical/cb-ord-999":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"order": map[string]any{
					"order_id":        "cb-ord-999",
					"client_order_id": "cid-1",
					"product_id":      "BTC-USD",
					"side":            "BUY",
					"status":          "FILLED",
					"filled_size":     "0.01",
					"created_time":    "2026-11-20T00:05:01Z",
					"order_configuration": map[string]any{
						"limit_limit_gtc": map[string]any{
							"base_size":   "0.01",
							"limit_price": "62000",
							"post_only":   true,
						},
					},
				},
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := NewCoinbaseSpotClient(CoinbaseCredentials{APIKey: apiKey, APISecret: apiSecret}, srv.URL, srv.Client())
	ctx := context.Background()

	bal, err := client.GetBalances(ctx)
	if err != nil {
		t.Fatalf("GetBalances failed: %v", err)
	}
	if !sawPagination || bal["USD"] != 1250.50 || bal["BTC"] != 0.025 || bal["ETH"] != 0.5 {
		t.Fatalf("unexpected balances (pagination=%v): %+v", sawPagination, bal)
	}

	q, err := client.GetQuote(ctx, "BTC-USD")
	if err != nil {
		t.Fatalf("GetQuote failed: %v", err)
	}
	if q.Bid != 62000 || q.Ask != 62002 || q.Mid != 62001 || q.Timestamp.IsZero() {
		t.Fatalf("unexpected quote: %+v", q)
	}

	// Even if PostOnly is omitted/false in caller request, CoinbaseSpotClient enforces post_only: true
	rec, err := client.PlaceLimitOrder(ctx, LimitOrderRequest{
		ClientOrderID: "cid-1",
		Symbol:        "BTC-USD",
		Side:          "BUY",
		Price:         62000.0,
		Quantity:      0.01,
		PostOnly:      false,
	})
	if err != nil {
		t.Fatalf("PlaceLimitOrder failed: %v", err)
	}
	if !sawPostOnly || !rec.PostOnly || rec.OrderID != "cb-ord-999" {
		t.Fatalf("unexpected placed order receipt: %+v", rec)
	}

	got, err := client.GetOrder(ctx, "cb-ord-999")
	if err != nil {
		t.Fatalf("GetOrder failed: %v", err)
	}
	if got.Status != "FILLED" || got.FilledQty != 0.01 || got.Price != 62000 || !got.PostOnly {
		t.Fatalf("unexpected historical order: %+v", got)
	}

	if err := client.CancelOrder(ctx, "cb-ord-999"); err != nil {
		t.Fatalf("CancelOrder failed: %v", err)
	}
	if !sawBatchCancel {
		t.Fatalf("expected batch_cancel to be invoked")
	}
}

func TestCoinbaseSpotCDPJWTEcdsaAuth(t *testing.T) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8Bytes}))
	const apiKey = "organizations/org-test/apiKeys/key-test"

	var verifiedURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		tok := strings.TrimPrefix(auth, "Bearer ")
		parts := strings.Split(tok, ".")
		if len(parts) != 3 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		hdrBytes, err1 := base64.RawURLEncoding.DecodeString(parts[0])
		claimsBytes, err2 := base64.RawURLEncoding.DecodeString(parts[1])
		sigBytes, err3 := base64.RawURLEncoding.DecodeString(parts[2])
		if err1 != nil || err2 != nil || err3 != nil || len(sigBytes) != 64 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		var hdr map[string]any
		var claims map[string]any
		_ = json.Unmarshal(hdrBytes, &hdr)
		_ = json.Unmarshal(claimsBytes, &claims)
		if hdr["alg"] != "ES256" || hdr["kid"] != apiKey || hdr["nonce"] == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if claims["iss"] != "cdp" || claims["sub"] != apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		rSig := new(big.Int).SetBytes(sigBytes[:32])
		sSig := new(big.Int).SetBytes(sigBytes[32:])
		if !ecdsa.Verify(&ecKey.PublicKey, digest[:], rSig, sSig) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("invalid ES256 signature"))
			return
		}
		verifiedURI, _ = claims["uri"].(string)

		_ = json.NewEncoder(w).Encode(map[string]any{
			"accounts": []map[string]any{
				{"currency": "USD", "available_balance": map[string]string{"value": "500.00", "currency": "USD"}},
			},
		})
	}))
	defer srv.Close()

	client := NewCoinbaseSpotClient(CoinbaseCredentials{APIKey: apiKey, APISecret: pemKey}, srv.URL, srv.Client())
	bal, err := client.GetBalances(context.Background())
	if err != nil {
		t.Fatalf("CDP JWT GetBalances failed: %v", err)
	}
	if bal["USD"] != 500.0 {
		t.Fatalf("unexpected balance: %+v", bal)
	}
	if !strings.HasPrefix(verifiedURI, "GET ") || !strings.HasSuffix(verifiedURI, "/api/v3/brokerage/accounts") {
		t.Fatalf("unexpected JWT uri claim: %q", verifiedURI)
	}
}

func TestCoinbaseSpotUnauthedPublicQuoteAndPrivateErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/brokerage/market/product_book":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pricebook": map[string]any{
					"product_id": "ETH-USD",
					"bids":       []map[string]string{{"price": "3100.10", "size": "2.0"}},
					"asks":       []map[string]string{{"price": "3100.30", "size": "1.5"}},
					"time":       time.Now().UTC().Format(time.RFC3339Nano),
				},
				"mid_market": "3100.20",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// Empty credentials: private endpoints fail immediately, public product_book quote succeeds.
	client := NewCoinbaseSpotClient(CoinbaseCredentials{}, srv.URL, srv.Client())
	ctx := context.Background()

	if _, err := client.GetBalances(ctx); err == nil {
		t.Fatal("expected GetBalances without credentials to fail")
	}
	if _, err := client.PlaceLimitOrder(ctx, LimitOrderRequest{Symbol: "ETH-USD", Side: "BUY", Price: 3100, Quantity: 0.1, PostOnly: true}); err == nil {
		t.Fatal("expected PlaceLimitOrder without credentials to fail")
	}
	if err := client.CancelOrder(ctx, "o1"); err == nil {
		t.Fatal("expected CancelOrder without credentials to fail")
	}
	if _, err := client.GetOrder(ctx, "o1"); err == nil {
		t.Fatal("expected GetOrder without credentials to fail")
	}

	q, err := client.GetQuote(ctx, "ETH-USD")
	if err != nil {
		t.Fatalf("public GetQuote failed: %v", err)
	}
	if q.Bid != 3100.10 || q.Ask != 3100.30 || q.Mid != 3100.20 {
		t.Fatalf("unexpected public quote: %+v", q)
	}
}

func TestCoinbaseSpotRejectedOrderAndCancelFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/brokerage/orders":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success":        false,
				"failure_reason": "UNKNOWN_FAILURE_REASON",
				"error_response": map[string]string{
					"error":   "INSUFFICIENT_FUND",
					"message": "Insufficient funds in USD account",
				},
			})
		case "/api/v3/brokerage/orders/batch_cancel":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{
					{"success": false, "failure_reason": "UNKNOWN_ORDER", "order_id": "ord-missing"},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := NewCoinbaseSpotClient(CoinbaseCredentials{APIKey: "k", APISecret: "s"}, srv.URL, srv.Client())
	ctx := context.Background()

	if _, err := client.PlaceLimitOrder(ctx, LimitOrderRequest{
		ClientOrderID: "c1",
		Symbol:        "BTC-USD",
		Side:          "BUY",
		Price:         60000,
		Quantity:      0.01,
		PostOnly:      true,
	}); err == nil || !strings.Contains(err.Error(), "Insufficient funds") {
		t.Fatalf("expected descriptive rejection error, got %v", err)
	}

	if err := client.CancelOrder(ctx, "ord-missing"); err == nil || !strings.Contains(err.Error(), "UNKNOWN_ORDER") {
		t.Fatalf("expected descriptive cancel error, got %v", err)
	}
}

func TestCoinbaseSpotBatchGetQuotesAndFallback(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/api/v3/brokerage/best_bid_ask" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		pids := r.URL.Query()["product_ids"]
		nowStr := time.Now().UTC().Format(time.RFC3339Nano)
		pbs := make([]map[string]any, 0, len(pids))
		for _, pid := range pids {
			switch strings.ToUpper(pid) {
			case "BTC-USD":
				pbs = append(pbs, map[string]any{"product_id": "BTC-USD", "bids": []map[string]string{{"price": "60000", "size": "1"}}, "asks": []map[string]string{{"price": "60001", "size": "1"}}, "time": nowStr})
			case "ETH-USD":
				pbs = append(pbs, map[string]any{"product_id": "ETH-USD", "bids": []map[string]string{{"price": "3000", "size": "2"}}, "asks": []map[string]string{{"price": "3001", "size": "2"}}, "time": nowStr})
			case "SOL-USD":
				// Simulate a server that only includes SOL-USD when queried individually as fallback.
				if len(pids) == 1 {
					pbs = append(pbs, map[string]any{"product_id": "SOL-USD", "bids": []map[string]string{{"price": "200", "size": "5"}}, "asks": []map[string]string{{"price": "200.2", "size": "5"}}, "time": nowStr})
				}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"pricebooks": pbs})
	}))
	defer srv.Close()

	c := NewCoinbaseSpotClient(CoinbaseCredentials{APIKey: "k", APISecret: "s"}, srv.URL, srv.Client())
	// 1. BTC+ETH returns in 1 single batch HTTP round-trip.
	qs2, err := c.GetQuotes(context.Background(), []string{"BTC-USD", "ETH-USD", "btc-usd"})
	if err != nil {
		t.Fatalf("GetQuotes 2 failed: %v", err)
	}
	if hits.Load() != 1 || qs2["BTC-USD"].Mid != 60000.5 || qs2["ETH-USD"].Mid != 3000.5 {
		t.Fatalf("unexpected 2-symbol batch result: hits=%d qs=%+v", hits.Load(), qs2)
	}

	// 2. When SOL-USD is omitted from batch response, client falls back cleanly to fetch only SOL-USD.
	hits.Store(0)
	qs3, err := c.GetQuotes(context.Background(), []string{"BTC-USD", "ETH-USD", "SOL-USD"})
	if err != nil {
		t.Fatalf("GetQuotes 3 fallback failed: %v", err)
	}
	if hits.Load() != 2 || qs3["SOL-USD"].Mid != 200.1 {
		t.Fatalf("unexpected 3-symbol fallback result: hits=%d qs=%+v", hits.Load(), qs3)
	}
}

func TestCoinbaseSpotDoJSONRetry429And503(t *testing.T) {
	var getAttempts atomic.Int32
	var cancelAttempts atomic.Int32
	var placeAttempts atomic.Int32
	var exhaustAttempts atomic.Int32
	var cancelBodies []string
	var cancelTimestamps []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/brokerage/accounts":
			n := getAttempts.Add(1)
			if n == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":"unavailable"}`))
				return
			}
			if n == 2 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"rate_limited"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"accounts": []map[string]any{
					{"currency": "USD", "available_balance": map[string]string{"value": "123.45"}},
				},
			})

		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/brokerage/orders/batch_cancel":
			bodyBytes, _ := io.ReadAll(r.Body)
			cancelBodies = append(cancelBodies, string(bodyBytes))
			cancelTimestamps = append(cancelTimestamps, r.Header.Get("CB-ACCESS-TIMESTAMP"))
			n := cancelAttempts.Add(1)
			if n == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":"try again"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{
					{"success": true, "order_id": "ord-1"},
				},
			})

		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/brokerage/orders":
			placeAttempts.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"unavailable"}`))

		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/brokerage/orders/historical/ord-exhaust":
			exhaustAttempts.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"still down"}`))

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	var clockTick atomic.Int64
	clockTick.Store(1700000000)
	c := NewCoinbaseSpotClient(CoinbaseCredentials{
		APIKey:    "hmac-key",
		APISecret: "hmac-secret",
	}, srv.URL, srv.Client())
	c.now = func() time.Time {
		return time.Unix(clockTick.Add(1), 0).UTC()
	}

	// 1. Safe GET retries on 503 and 429 Retry-After and succeeds.
	bals, err := c.GetBalances(context.Background())
	if err != nil {
		t.Fatalf("GetBalances failed after retries: %v", err)
	}
	if getAttempts.Load() != 3 || bals["USD"] != 123.45 {
		t.Fatalf("unexpected GetBalances retry result: attempts=%d bals=%+v", getAttempts.Load(), bals)
	}

	// 2. Idempotent cancel retries on 503, rewinds JSON body, and re-signs auth timestamp.
	if err := c.CancelOrder(context.Background(), "ord-1"); err != nil {
		t.Fatalf("CancelOrder failed after retry: %v", err)
	}
	if cancelAttempts.Load() != 2 || len(cancelBodies) != 2 {
		t.Fatalf("expected 2 cancel attempts, got %d", cancelAttempts.Load())
	}
	if cancelBodies[0] == "" || cancelBodies[0] != cancelBodies[1] {
		t.Fatalf("expected identical rewound cancel body on retry, got %q vs %q", cancelBodies[0], cancelBodies[1])
	}
	if cancelTimestamps[0] == "" || cancelTimestamps[0] == cancelTimestamps[1] {
		t.Fatalf("expected re-signed auth timestamp on retry, got %q vs %q", cancelTimestamps[0], cancelTimestamps[1])
	}

	// 3. Non-idempotent PlaceLimitOrder must NOT retry on 503.
	_, err = c.PlaceLimitOrder(context.Background(), LimitOrderRequest{
		ClientOrderID: "cid-1",
		Symbol:        "BTC-USD",
		Side:          "BUY",
		Price:         60000,
		Quantity:      0.01,
		PostOnly:      true,
	})
	if err == nil {
		t.Fatal("expected PlaceLimitOrder error on 503")
	}
	if placeAttempts.Load() != 1 {
		t.Fatalf("PlaceLimitOrder must not retry on 503, got %d attempts", placeAttempts.Load())
	}

	// 4. Bounded retries stop after maxAttempts.
	if _, err := c.GetOrder(context.Background(), "ord-exhaust"); err == nil {
		t.Fatal("expected GetOrder error after exhausting retries")
	}
	if exhaustAttempts.Load() != 4 {
		t.Fatalf("expected 4 bounded attempts, got %d", exhaustAttempts.Load())
	}
}

func TestCoinbaseSpotLookupProductSpecPropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not_found"}`))
	}))
	defer srv.Close()

	c := NewCoinbaseSpotClient(CoinbaseCredentials{}, srv.URL, srv.Client())
	if _, err := c.lookupProductSpec(context.Background(), "UNKNOWN-USD"); err == nil {
		t.Fatal("expected lookupProductSpec to propagate HTTP 404 error")
	}
}

func TestCoinbaseSpotPlaceAndCancelErrorEdgeCases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/brokerage/orders":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
			})
		case "/api/v3/brokerage/orders/batch_cancel":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{
					{"success": true, "order_id": "other-order"},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewCoinbaseSpotClient(CoinbaseCredentials{
		APIKey:    "hmac-key",
		APISecret: "hmac-secret",
	}, srv.URL, srv.Client())

	if _, err := c.PlaceLimitOrder(context.Background(), LimitOrderRequest{
		ClientOrderID: "cid-empty",
		Symbol:        "BTC-USD",
		Side:          "BUY",
		Price:         60000,
		Quantity:      0.01,
		PostOnly:      true,
	}); err == nil {
		t.Fatal("expected error when success response has empty order_id")
	}

	if err := c.CancelOrder(context.Background(), "ord-target"); err == nil {
		t.Fatal("expected error when batch_cancel returns mismatched order_id")
	}

	hc := NewOptimizedHTTPClient(7 * time.Second)
	tr, ok := hc.Transport.(*http.Transport)
	if !ok || tr.ResponseHeaderTimeout != 7*time.Second {
		t.Fatalf("expected ResponseHeaderTimeout=7s, got %+v", hc.Transport)
	}
}

func BenchmarkCoinbaseSpotCDPJWTSign(b *testing.B) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(ecKey)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	c := NewCoinbaseSpotClient(CoinbaseCredentials{
		APIKey:    "organizations/org-bench/apiKeys/key-bench",
		APISecret: pemKey,
	}, "https://api.coinbase.com", nil)
	req, _ := http.NewRequest(http.MethodGet, "https://api.coinbase.com/api/v3/brokerage/accounts", nil)
	now := time.Unix(1795300000, 0).UTC()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := c.applyAuth(req, nil, now); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCoinbaseSpotHMACSign(b *testing.B) {
	c := NewCoinbaseSpotClient(CoinbaseCredentials{
		APIKey:    "hmac-key-bench",
		APISecret: "hmac-secret-bench",
	}, "https://api.coinbase.com", nil)
	req, _ := http.NewRequest(http.MethodPost, "https://api.coinbase.com/api/v3/brokerage/orders", nil)
	body := []byte(`{"client_order_id":"123","product_id":"BTC-USD"}`)
	now := time.Unix(1795300000, 0).UTC()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := c.applyAuth(req, body, now); err != nil {
			b.Fatal(err)
		}
	}
}

func TestCoinbaseSpotGetOrder404AndGetOrderByClientOrderID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/brokerage/orders/historical/cid-only":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_found"}`))
		case "/api/v3/brokerage/orders/historical/batch":
			if got := r.URL.Query().Get("product_ids"); got != "BTC-USD" {
				t.Errorf("expected product_ids=BTC-USD, got %q", got)
			}
			cursor := r.URL.Query().Get("cursor")
			if cursor == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"orders": []map[string]any{
						{
							"order_id":        "cb-other-1",
							"client_order_id": "cid-other",
							"product_id":      "BTC-USD",
							"side":            "BUY",
							"status":          "OPEN",
						},
					},
					"has_next": true,
					"cursor":   "page-2",
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"orders": []map[string]any{
					{
						"order_id":        "cb-srv-42",
						"client_order_id": "cid-target",
						"product_id":      "BTC-USD",
						"side":            "BUY",
						"status":          "FILLED",
						"filled_size":     "0.02",
						"order_configuration": map[string]any{
							"limit_limit_gtc": map[string]any{
								"base_size":   "0.02",
								"limit_price": "61000.00",
								"post_only":   true,
							},
						},
					},
				},
				"has_next": false,
				"cursor":   "",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewCoinbaseSpotClient(CoinbaseCredentials{
		APIKey:    "hmac-key-test",
		APISecret: "hmac-secret-test",
	}, srv.URL, srv.Client())
	ctx := context.Background()

	if _, err := c.GetOrder(ctx, "cid-only"); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("expected ErrOrderNotFound on 404 from GetOrder, got %v", err)
	}

	got, err := c.GetOrderByClientOrderID(ctx, "btc-usd", "cid-target")
	if err != nil {
		t.Fatalf("GetOrderByClientOrderID failed: %v", err)
	}
	if got.OrderID != "cb-srv-42" || got.ClientOrderID != "cid-target" || got.Status != "FILLED" || got.FilledQty != 0.02 || !got.PostOnly {
		t.Fatalf("unexpected receipt from GetOrderByClientOrderID: %+v", got)
	}

	if _, err := c.GetOrderByClientOrderID(ctx, "BTC-USD", "cid-missing"); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("expected ErrOrderNotFound for missing client_order_id, got %v", err)
	}
}

func TestCoinbaseSpotPaginationCursorCycleAndMaxPages(t *testing.T) {
	var acctCalls, ordCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/brokerage/accounts":
			acctCalls++
			cursor := r.URL.Query().Get("cursor")
			nextCursor := "c1"
			if cursor == "c1" {
				nextCursor = "c1" // cycle back to same cursor
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"accounts": []map[string]any{
					{"currency": "USD", "available_balance": map[string]string{"value": "100.0", "currency": "USD"}},
				},
				"has_next": true,
				"cursor":   nextCursor,
			})
		case "/api/v3/brokerage/orders/historical/batch":
			ordCalls++
			cursor := r.URL.Query().Get("cursor")
			nextCursor := "p1"
			if cursor == "p1" {
				nextCursor = "p1" // cycle back to same cursor
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"orders":   []any{},
				"has_next": true,
				"cursor":   nextCursor,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewCoinbaseSpotClient(CoinbaseCredentials{
		APIKey:    "hmac-key-test",
		APISecret: "hmac-secret-test",
	}, srv.URL, srv.Client())
	ctx := context.Background()

	bal, err := c.GetBalances(ctx)
	if err != nil {
		t.Fatalf("GetBalances failed: %v", err)
	}
	if acctCalls != 2 {
		t.Fatalf("expected GetBalances to stop after cursor cycle (2 calls), got %d", acctCalls)
	}
	if bal["USD"] != 200.0 {
		t.Fatalf("expected USD balance 200.0, got %.2f", bal["USD"])
	}

	if _, err := c.GetOrderByClientOrderID(ctx, "BTC-USD", "missing"); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("expected ErrOrderNotFound, got %v", err)
	}
	if ordCalls != 2 {
		t.Fatalf("expected GetOrderByClientOrderID to stop after cursor cycle (2 calls), got %d", ordCalls)
	}
}
