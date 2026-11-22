package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"piquant-live/internal/execution"
	"piquant-live/internal/trend"
)

type collectedCoinbaseBook struct {
	ProductID string
	BestBid   float64
	BestAsk   float64
	Mid       float64
	Bids      [][2]float64
	Asks      [][2]float64
	AskDepUSD float64
}

func loadLatestCollectedCoinbaseL2(t *testing.T, product string) collectedCoinbaseBook {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot resolve home directory")
	}
	dir := filepath.Join(home, "piquant-collect", "book", product)
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl.gz"))
	if len(files) == 0 {
		t.Skipf("no collected L2 files in %s", dir)
	}
	sort.Strings(files)

	// Prefer the most recent complete file, falling back to earlier files if truncated.
	for i := len(files) - 1; i >= 0; i-- {
		f, err := os.Open(files[i])
		if err != nil {
			continue
		}
		zr, zerr := gzip.NewReader(f)
		if zerr != nil {
			_ = f.Close()
			continue
		}
		sc := bufio.NewScanner(zr)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		var lastLine []byte
		for sc.Scan() {
			b := sc.Bytes()
			if len(b) > 0 && b[0] == '{' {
				lastLine = append(lastLine[:0], b...)
			}
		}
		_ = zr.Close()
		_ = f.Close()
		if len(lastLine) == 0 {
			continue
		}
		var raw struct {
			T int64        `json:"t"`
			B [][2]float64 `json:"b"`
			A [][2]float64 `json:"a"`
		}
		if jerr := json.Unmarshal(lastLine, &raw); jerr == nil && len(raw.B) > 0 && len(raw.A) > 0 {
			bid := raw.B[0][0]
			ask := raw.A[0][0]
			if bid > 0 && ask >= bid {
				var askUSD float64
				for _, lvl := range raw.A {
					askUSD += lvl[0] * lvl[1]
				}
				return collectedCoinbaseBook{
					ProductID: product,
					BestBid:   bid,
					BestAsk:   ask,
					Mid:       0.5 * (bid + ask),
					Bids:      raw.B,
					Asks:      raw.A,
					AskDepUSD: askUSD,
				}
			}
		}
	}
	t.Skipf("could not parse any collected L2 snapshot in %s", dir)
	return collectedCoinbaseBook{}
}

func toCBLevels(lvls [][2]float64) []map[string]string {
	out := make([]map[string]string, 0, len(lvls))
	for _, l := range lvls {
		out = append(out, map[string]string{
			"price": strconv.FormatFloat(l[0], 'f', -1, 64),
			"size":  strconv.FormatFloat(l[1], 'f', -1, 64),
		})
	}
	return out
}

func TestExecuteQ57AgainstRealCollectedCoinbaseL2Book(t *testing.T) {
	books := map[string]collectedCoinbaseBook{
		"BTC-USD": loadLatestCollectedCoinbaseL2(t, "BTC-USD"),
		"ETH-USD": loadLatestCollectedCoinbaseL2(t, "ETH-USD"),
		"SOL-USD": loadLatestCollectedCoinbaseL2(t, "SOL-USD"),
	}

	var multiBatchCalls atomic.Int64
	var singleQuoteCalls atomic.Int64
	var publicQuoteCalls atomic.Int64
	var authHeaderSeen atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/time":
			_ = json.NewEncoder(w).Encode(map[string]any{"iso": time.Now().UTC().Format(time.RFC3339Nano)})
		case "/api/v3/brokerage/accounts":
			if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				authHeaderSeen.Add(1)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"accounts": []map[string]any{
					{"currency": "USD", "available_balance": map[string]string{"value": "1000.00", "currency": "USD"}},
					{"currency": "BTC", "available_balance": map[string]string{"value": "0", "currency": "BTC"}},
					{"currency": "ETH", "available_balance": map[string]string{"value": "0", "currency": "ETH"}},
					{"currency": "SOL", "available_balance": map[string]string{"value": "0", "currency": "SOL"}},
				},
				"has_next": false,
			})
		case "/api/v3/brokerage/best_bid_ask":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			pids := r.URL.Query()["product_ids"]
			if len(pids) > 1 {
				multiBatchCalls.Add(1)
			} else {
				singleQuoteCalls.Add(1)
			}
			nowStr := time.Now().UTC().Format(time.RFC3339Nano)
			pricebooks := make([]map[string]any, 0, len(pids))
			for _, pid := range pids {
				b, ok := books[strings.ToUpper(pid)]
				if !ok {
					continue
				}
				pricebooks = append(pricebooks, map[string]any{
					"product_id": b.ProductID,
					"bids":       toCBLevels(b.Bids),
					"asks":       toCBLevels(b.Asks),
					"time":       nowStr,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"pricebooks": pricebooks})
		case "/api/v3/brokerage/market/product_book":
			publicQuoteCalls.Add(1)
			pid := strings.ToUpper(r.URL.Query().Get("product_id"))
			b, ok := books[pid]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pricebook": map[string]any{
					"product_id": b.ProductID,
					"bids":       toCBLevels(b.Bids[:1]),
					"asks":       toCBLevels(b.Asks[:1]),
					"time":       time.Now().UTC().Format(time.RFC3339Nano),
				},
				"mid_market": strconv.FormatFloat(b.Mid, 'f', -1, 64),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// Build a clean 80-day uptrend trend_state terminating at the real collected L2 mid prices.
	root := t.TempDir()
	trendDir := filepath.Join(root, "trend")
	n := 80
	st := trend.NewState("2026-11-11")
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	seqB := make([]trend.Day, 0, n)
	seqE := make([]trend.Day, 0, n)
	seqS := make([]trend.Day, 0, n)
	for i := 0; i < n; i++ {
		scale := 0.80 + 0.20*float64(i)/float64(n-1)
		osc := 1.0 + 0.004*float64((i%5)-2)
		if i == n-1 {
			osc = 1.0
		}
		d := t0.AddDate(0, 0, i).Format("2006-01-02")
		cB := books["BTC-USD"].Mid * scale * osc
		cE := books["ETH-USD"].Mid * scale * osc
		cS := books["SOL-USD"].Mid * scale * osc
		seqB = append(seqB, trend.Day{Date: d, Open: cB, High: cB * 1.01, Low: cB * 0.99, Close: cB})
		seqE = append(seqE, trend.Day{Date: d, Open: cE, High: cE * 1.01, Low: cE * 0.99, Close: cE})
		seqS = append(seqS, trend.Day{Date: d, Open: cS, High: cS * 1.01, Low: cS * 0.99, Close: cS})
	}
	st.Seed(seqB[:51])
	st.SeedETH(seqE[:51])
	st.SeedSOL(seqS[:51])
	st.Advance(seqB[51:])
	st.AdvanceETH(seqE[51:])
	st.AdvanceSOL(seqS[51:])
	if err := st.Save(filepath.Join(trendDir, "trend_state.json")); err != nil {
		t.Fatalf("save trend_state: %v", err)
	}

	// 1. Unauthenticated dry-run against real Coinbase L2 public product_book snapshots.
	t.Setenv("COINBASE_API_KEY", "")
	t.Setenv("COINBASE_API_SECRET", "")
	t.Setenv(execution.LiveArmEnvVar, "")

	optsUnauth, err := parseExecuteOptions([]string{
		"--portfolio", "q57_hrp_hurst",
		"--dir", trendDir,
		"--carry-dir", t.TempDir(),
		"--dtb3", filepath.Join(t.TempDir(), "missing.csv"),
		"--data", filepath.Join(root, "data_unauth"),
		"--env", filepath.Join(root, "missing_cb.env"),
		"--alert-env", filepath.Join(root, "missing_alert.env"),
		"--rebalance-buffer-pct", "3.0",
		"--max-daily-usd", "1000",
		"--max-order-usd", "500",
		"--nav", "1000",
	})
	if err != nil {
		t.Fatalf("parseExecuteOptions unauth: %v", err)
	}
	optsUnauth.coinbaseBaseURL = srv.URL
	optsUnauth.pingEndpoints = []execution.ExchangeEndpoint{{Venue: "coinbase", URL: srv.URL + "/time"}}

	rep1, err := executeWithOptions(context.Background(), optsUnauth)
	if err != nil {
		t.Fatalf("executeWithOptions q57_hrp_hurst unauth failed: %v", err)
	}
	if rep1.BlendName != "Q57_hrp_hurst_conformal_carry_spot" {
		t.Fatalf("BlendName = %q, want Q57_hrp_hurst_conformal_carry_spot", rep1.BlendName)
	}
	if len(rep1.Plans) != 3 {
		t.Fatalf("expected 3 planned legs (BTC, ETH, SOL), got %d", len(rep1.Plans))
	}
	for _, p := range rep1.Plans {
		b := books[p.Symbol]
		if p.SkippedReason != "" {
			t.Fatalf("unexpected skipped leg %s: %s", p.Symbol, p.SkippedReason)
		}
		if p.Side == "BUY" && p.LimitPrice > b.BestBid {
			t.Fatalf("%s passive BUY limit %.4f > real L2 BestBid %.4f", p.Symbol, p.LimitPrice, b.BestBid)
		}
		if p.EstNotionalUSD > b.AskDepUSD {
			t.Fatalf("%s order notional $%.2f exceeds real top-10 L2 depth $%.2f", p.Symbol, p.EstNotionalUSD, b.AskDepUSD)
		}
	}
	if publicQuoteCalls.Load() < 3 {
		t.Fatalf("expected public product_book calls for BTC/ETH/SOL, got %d", publicQuoteCalls.Load())
	}

	// 2. Authenticated CDP ES256 dry-run: must fetch all 3 L2 quotes in ONE multi-symbol batch call
	//    during NAV/sizing planning, plus per-order safety freshness checks inside SubmitTargetOrder.
	multiBatchCalls.Store(0)
	singleQuoteCalls.Store(0)
	publicQuoteCalls.Store(0)
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))
	t.Setenv("COINBASE_API_KEY", "organizations/test-org/apiKeys/test-key")
	t.Setenv("COINBASE_API_SECRET", pemKey)

	optsAuth, err := parseExecuteOptions([]string{
		"--portfolio", "q57_hrp_hurst",
		"--dir", trendDir,
		"--carry-dir", t.TempDir(),
		"--dtb3", filepath.Join(t.TempDir(), "missing.csv"),
		"--data", filepath.Join(root, "data_auth"),
		"--env", filepath.Join(root, "missing_cb.env"),
		"--alert-env", filepath.Join(root, "missing_alert.env"),
		"--rebalance-buffer-pct", "3.0",
		"--max-daily-usd", "1000",
		"--max-order-usd", "500",
		"--nav", "0",
	})
	if err != nil {
		t.Fatalf("parseExecuteOptions auth: %v", err)
	}
	optsAuth.coinbaseBaseURL = srv.URL
	optsAuth.pingEndpoints = []execution.ExchangeEndpoint{{Venue: "coinbase", URL: srv.URL + "/time"}}

	rep2, err := executeWithOptions(context.Background(), optsAuth)
	if err != nil {
		t.Fatalf("executeWithOptions q57_hrp_hurst auth failed: %v", err)
	}
	if multiBatchCalls.Load() != 1 || singleQuoteCalls.Load() != 3 || publicQuoteCalls.Load() != 0 {
		t.Fatalf("expected 1 multi-symbol batch + 3 SubmitTargetOrder safety checks and 0 public calls, got multi=%d single=%d public=%d",
			multiBatchCalls.Load(), singleQuoteCalls.Load(), publicQuoteCalls.Load())
	}
	if authHeaderSeen.Load() == 0 {
		t.Fatalf("expected CDP Bearer auth header on Coinbase accounts call")
	}
	if len(rep2.Results) != 3 {
		t.Fatalf("expected 3 dry-run order results for Q57, got %d", len(rep2.Results))
	}
	t.Logf("[EXECUTE Q57 REAL L2] Blend=%s MultiBatch=%d SafetyChecks=%d AuthSeen=%d BTC=$%.2f ETH=$%.2f SOL=$%.2f",
		rep2.BlendName, multiBatchCalls.Load(), singleQuoteCalls.Load(), authHeaderSeen.Load(),
		books["BTC-USD"].Mid, books["ETH-USD"].Mid, books["SOL-USD"].Mid,
	)
	_ = fmt.Sprintf("")
	_ = io.Discard
}
