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
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
