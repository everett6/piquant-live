package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"piquant-live/internal/execution"
	"piquant-live/internal/quant/guardrails"
	"piquant-live/internal/trend"
)

func setupExecuteTestFixtures(t *testing.T) (trendDir, dataDir, envPath, alertEnv string) {
	t.Helper()
	t.Setenv("COINBASE_API_KEY", "")
	t.Setenv("COINBASE_API_SECRET", "")
	t.Setenv(execution.LiveArmEnvVar, "")

	root := t.TempDir()
	trendDir = filepath.Join(root, "trend")
	dataDir = filepath.Join(root, "data")
	envPath = filepath.Join(root, "missing_coinbase.env")
	alertEnv = filepath.Join(root, "missing_alert.env")

	st := trend.NewState("2026-11-11")
	t0, _ := time.Parse("2006-01-02", "2026-09-20")
	seq := make([]trend.Day, 0, 60)
	for i := 0; i < 60; i++ {
		seq = append(seq, trend.Day{
			Date:  t0.AddDate(0, 0, i).Format("2006-01-02"),
			Close: 100 + float64(i),
		})
	}
	st.Seed(seq[:51])
	st.SeedETH(seq[:51])
	st.Advance(seq[51:])
	st.AdvanceETH(seq[51:])
	if err := st.Save(filepath.Join(trendDir, "trend_state.json")); err != nil {
		t.Fatalf("save trend state: %v", err)
	}
	return trendDir, dataDir, envPath, alertEnv
}

func newMockCoinbaseQuoteServer(t *testing.T, quoteAge time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/brokerage/best_bid_ask":
			sym := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("product_ids")))
			bid, ask := "59990.00", "60010.00"
			if sym == "ETH-USD" {
				bid, ask = "2999.00", "3001.00"
			}
			ts := time.Now().UTC().Add(-quoteAge).Format(time.RFC3339Nano)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pricebooks": []map[string]any{
					{
						"product_id": sym,
						"bids":       []map[string]string{{"price": bid, "size": "1.0"}},
						"asks":       []map[string]string{{"price": ask, "size": "1.0"}},
						"time":       ts,
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func newMockPingServer(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"iso":"2026-11-20T00:00:00Z"}`))
	}))
}

func TestExecuteAbortsOnHighPingLatency(t *testing.T) {
	ctx := context.Background()
	trendDir, dataDir, envPath, alertEnv := setupExecuteTestFixtures(t)
	cbSrv := newMockCoinbaseQuoteServer(t, 0)
	defer cbSrv.Close()
	slowPingSrv := newMockPingServer(t, 25*time.Millisecond)
	defer slowPingSrv.Close()

	opts, err := parseExecuteOptions([]string{
		"--dir", trendDir,
		"--carry-dir", t.TempDir(),
		"--dtb3", filepath.Join(t.TempDir(), "missing.csv"),
		"--data", dataDir,
		"--env", envPath,
		"--alert-env", alertEnv,
		"--ping-probe=true",
		"--max-ping-ms", "5",
	})
	if err != nil {
		t.Fatalf("parseExecuteOptions failed: %v", err)
	}
	opts.coinbaseBaseURL = cbSrv.URL
	opts.pingEndpoints = []execution.ExchangeEndpoint{{Venue: "coinbase", URL: slowPingSrv.URL}}

	report, err := executeWithOptions(ctx, opts)
	if !errors.Is(err, execution.ErrHighLatency) {
		t.Fatalf("expected ErrHighLatency when ping > --max-ping-ms, got err=%v", err)
	}
	if report.PingPlan == nil {
		t.Fatalf("expected PingPlan attached to ExecuteReport even on abort")
	}
	if report.PingPlan.BestHTTPMedianMs <= 5.0 {
		t.Fatalf("expected measured ping median > 5ms, got %.2fms", report.PingPlan.BestHTTPMedianMs)
	}

	// All probes failed (closed server) must also abort with ErrHighLatency.
	deadSrv := newMockPingServer(t, 0)
	deadURL := deadSrv.URL
	deadSrv.Close()
	opts.maxPingMs = 1500.0
	opts.pingEndpoints = []execution.ExchangeEndpoint{{Venue: "coinbase", URL: deadURL}}

	report2, err2 := executeWithOptions(ctx, opts)
	if !errors.Is(err2, execution.ErrHighLatency) {
		t.Fatalf("expected ErrHighLatency when all probes fail, got err=%v", err2)
	}
	if report2.PingPlan == nil {
		t.Fatalf("expected PingPlan attached when all probes fail")
	}
}

func TestExecutePersistentHighWaterMarkDrawdownKillSwitch(t *testing.T) {
	ctx := context.Background()
	trendDir, dataDir, envPath, alertEnv := setupExecuteTestFixtures(t)
	cbSrv := newMockCoinbaseQuoteServer(t, 0)
	defer cbSrv.Close()
	fastPingSrv := newMockPingServer(t, 2*time.Millisecond)
	defer fastPingSrv.Close()

	carryDir := t.TempDir()
	dtb3Path := filepath.Join(t.TempDir(), "missing.csv")

	// Run 1: $1000 NAV establishes persistent high-water mark in <data>/paper/exec_hwm.json.
	opts1, err := parseExecuteOptions([]string{
		"--dir", trendDir,
		"--carry-dir", carryDir,
		"--dtb3", dtb3Path,
		"--data", dataDir,
		"--env", envPath,
		"--alert-env", alertEnv,
		"--nav", "1000",
		"--max-drawdown-pct", "15",
	})
	if err != nil {
		t.Fatalf("parseExecuteOptions run1 failed: %v", err)
	}
	opts1.coinbaseBaseURL = cbSrv.URL
	opts1.pingEndpoints = []execution.ExchangeEndpoint{{Venue: "coinbase", URL: fastPingSrv.URL}}

	rep1, err1 := executeWithOptions(ctx, opts1)
	if err1 != nil {
		t.Fatalf("run 1 failed unexpectedly: %v", err1)
	}
	if !rep1.SimulatedBalances || rep1.PeakNavUSD != 1000.0 || rep1.DrawdownPct != 0.0 {
		t.Fatalf("unexpected run 1 HWM metrics: sim=%v peak=%.2f dd=%.2f", rep1.SimulatedBalances, rep1.PeakNavUSD, rep1.DrawdownPct)
	}
	if len(rep1.Results) == 0 {
		t.Fatalf("expected run 1 to submit dry-run rebalance orders, plans=%+v", rep1.Plans)
	}
	wantHWMPath := filepath.Join(dataDir, "paper", "exec_hwm.json")
	if rep1.HWMPath != wantHWMPath {
		t.Fatalf("got HWMPath %q, want %q", rep1.HWMPath, wantHWMPath)
	}

	// Run 2: NAV drops to $800 (20% drawdown > 15% cutoff) -> trips PreTradeGuard kill switch on non-reduce-only rebalance.
	opts2 := opts1
	opts2.navUSD = 800.0

	rep2, err2 := executeWithOptions(ctx, opts2)
	if !errors.Is(err2, guardrails.ErrKillSwitchTripped) {
		t.Fatalf("expected ErrKillSwitchTripped on 20%% drawdown > 15%% limit, got err=%v", err2)
	}
	if rep2.PeakNavUSD != 1000.0 {
		t.Fatalf("expected persisted PeakNavUSD 1000.0, got %.2f", rep2.PeakNavUSD)
	}
	if rep2.DrawdownPct != 20.0 {
		t.Fatalf("expected DrawdownPct 20.0, got %.2f", rep2.DrawdownPct)
	}
	if len(rep2.Results) != 0 {
		t.Fatalf("expected 0 submitted orders when kill switch tripped, got %d", len(rep2.Results))
	}

	persisted, err := execution.LoadHighWaterMark(wantHWMPath)
	if err != nil {
		t.Fatalf("LoadHighWaterMark failed: %v", err)
	}
	if persisted.PeakNavUSD != 1000.0 || persisted.LastNavUSD != 800.0 {
		t.Fatalf("unexpected persisted exec_hwm.json: %+v", persisted)
	}
}

func TestExecuteFeeHurdleRejectsInsufficientEdge(t *testing.T) {
	ctx := context.Background()
	trendDir, dataDir, envPath, alertEnv := setupExecuteTestFixtures(t)
	cbSrv := newMockCoinbaseQuoteServer(t, 0)
	defer cbSrv.Close()
	fastPingSrv := newMockPingServer(t, 2*time.Millisecond)
	defer fastPingSrv.Close()

	opts, err := parseExecuteOptions([]string{
		"--dir", trendDir,
		"--carry-dir", t.TempDir(),
		"--dtb3", filepath.Join(t.TempDir(), "missing.csv"),
		"--data", dataDir,
		"--env", envPath,
		"--alert-env", alertEnv,
		"--round-trip-fee-bps", "80",
		"--expected-edge-bps", "25",
	})
	if err != nil {
		t.Fatalf("parseExecuteOptions failed: %v", err)
	}
	opts.coinbaseBaseURL = cbSrv.URL
	opts.pingEndpoints = []execution.ExchangeEndpoint{{Venue: "coinbase", URL: fastPingSrv.URL}}

	report, err := executeWithOptions(ctx, opts)
	if !errors.Is(err, guardrails.ErrInsufficientNetEdge) {
		t.Fatalf("expected ErrInsufficientNetEdge when 80 bps fee > 25 bps edge, got err=%v", err)
	}
	if len(report.Results) != 0 {
		t.Fatalf("expected 0 results on fee hurdle rejection, got %d", len(report.Results))
	}
	if len(report.Errors) == 0 {
		t.Fatalf("expected Errors populated in ExecuteReport")
	}
}

func TestExecutePingPlanCapsQuoteAgeAndRespectsFlag(t *testing.T) {
	ctx := context.Background()
	trendDir, dataDir, envPath, alertEnv := setupExecuteTestFixtures(t)
	fastPingSrv := newMockPingServer(t, 2*time.Millisecond)
	defer fastPingSrv.Close()

	// 1. Default --max-quote-age is 2m, but PingPlan tightens effective MaxQuoteAge to 30s -> 45s quote is stale.
	stale45sSrv := newMockCoinbaseQuoteServer(t, 45*time.Second)
	defer stale45sSrv.Close()

	opts1, err := parseExecuteOptions([]string{
		"--dir", trendDir,
		"--carry-dir", t.TempDir(),
		"--dtb3", filepath.Join(t.TempDir(), "missing.csv"),
		"--data", dataDir,
		"--env", envPath,
		"--alert-env", alertEnv,
	})
	if err != nil {
		t.Fatalf("parseExecuteOptions failed: %v", err)
	}
	opts1.coinbaseBaseURL = stale45sSrv.URL
	opts1.pingEndpoints = []execution.ExchangeEndpoint{{Venue: "coinbase", URL: fastPingSrv.URL}}

	rep1, err1 := executeWithOptions(ctx, opts1)
	if !errors.Is(err1, execution.ErrStaleQuote) {
		t.Fatalf("expected ErrStaleQuote when 45s quote exceeds PingPlan-capped 30s MaxQuoteAge, got err=%v", err1)
	}
	if rep1.PingPlan == nil {
		t.Fatalf("expected PingPlan attached to ExecuteReport")
	}

	// 2. Explicit tighter --max-quote-age 10s must be respected (15s quote rejected, not widened to 30s).
	stale15sSrv := newMockCoinbaseQuoteServer(t, 15*time.Second)
	defer stale15sSrv.Close()

	opts2, err := parseExecuteOptions([]string{
		"--dir", trendDir,
		"--carry-dir", t.TempDir(),
		"--dtb3", filepath.Join(t.TempDir(), "missing.csv"),
		"--data", t.TempDir(),
		"--env", envPath,
		"--alert-env", alertEnv,
		"--max-quote-age", "10s",
	})
	if err != nil {
		t.Fatalf("parseExecuteOptions failed: %v", err)
	}
	opts2.coinbaseBaseURL = stale15sSrv.URL
	opts2.pingEndpoints = []execution.ExchangeEndpoint{{Venue: "coinbase", URL: fastPingSrv.URL}}

	if _, err2 := executeWithOptions(ctx, opts2); !errors.Is(err2, execution.ErrStaleQuote) {
		t.Fatalf("expected ErrStaleQuote when 15s quote exceeds --max-quote-age 10s, got err=%v", err2)
	}
}

func TestExecuteLiveOrderTTLCancelAndCoinbaseAccounts(t *testing.T) {
	ctx := context.Background()
	trendDir, dataDir, _, alertEnv := setupExecuteTestFixtures(t)
	t.Setenv("COINBASE_API_KEY", "test-hmac-key")
	t.Setenv("COINBASE_API_SECRET", "test-hmac-secret")
	t.Setenv(execution.LiveArmEnvVar, execution.LiveArmEnvValue)

	fastPingSrv := newMockPingServer(t, 2*time.Millisecond)
	defer fastPingSrv.Close()

	var placeCount, getOrderCount, cancelCount atomic.Int64
	cbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/brokerage/accounts":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"accounts": []map[string]any{
					{"currency": "USD", "available_balance": map[string]string{"value": "1000.00", "currency": "USD"}},
					{"currency": "BTC", "available_balance": map[string]string{"value": "0", "currency": "BTC"}},
					{"currency": "ETH", "available_balance": map[string]string{"value": "0", "currency": "ETH"}},
				},
				"has_next": false,
			})
		case r.URL.Path == "/api/v3/brokerage/best_bid_ask":
			sym := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("product_ids")))
			bid, ask := "59990.00", "60010.00"
			if sym == "ETH-USD" {
				bid, ask = "2999.00", "3001.00"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pricebooks": []map[string]any{
					{
						"product_id": sym,
						"bids":       []map[string]string{{"price": bid, "size": "1.0"}},
						"asks":       []map[string]string{{"price": ask, "size": "1.0"}},
						"time":       time.Now().UTC().Format(time.RFC3339Nano),
					},
				},
			})
		case r.URL.Path == "/api/v3/brokerage/orders" && r.Method == http.MethodPost:
			placeCount.Add(1)
			var req struct {
				ClientOrderID string `json:"client_order_id"`
				ProductID     string `json:"product_id"`
				Side          string `json:"side"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"success_response": map[string]string{
					"order_id":        "cb-live-" + req.ProductID,
					"product_id":      req.ProductID,
					"side":            req.Side,
					"client_order_id": req.ClientOrderID,
				},
			})
		case strings.HasPrefix(r.URL.Path, "/api/v3/brokerage/orders/historical/"):
			getOrderCount.Add(1)
			ordID := strings.TrimPrefix(r.URL.Path, "/api/v3/brokerage/orders/historical/")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"order": map[string]any{
					"order_id":    ordID,
					"status":      "OPEN",
					"filled_size": "0",
				},
			})
		case r.URL.Path == "/api/v3/brokerage/orders/batch_cancel" && r.Method == http.MethodPost:
			cancelCount.Add(1)
			var req struct {
				OrderIDs []string `json:"order_ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			var results []map[string]any
			for _, id := range req.OrderIDs {
				results = append(results, map[string]any{"success": true, "order_id": id})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer cbSrv.Close()

	opts, err := parseExecuteOptions([]string{
		"--dry-run=false",
		"--confirm-live", execution.LiveArmConfirmToken,
		"--dir", trendDir,
		"--carry-dir", t.TempDir(),
		"--dtb3", filepath.Join(t.TempDir(), "missing.csv"),
		"--data", dataDir,
		"--env", filepath.Join(t.TempDir(), "missing_coinbase.env"),
		"--alert-env", alertEnv,
		"--nav", "0",
		"--order-ttl", "35ms",
		"--max-daily-usd", "1000",
	})
	if err != nil {
		t.Fatalf("parseExecuteOptions failed: %v", err)
	}
	opts.coinbaseBaseURL = cbSrv.URL
	opts.pingEndpoints = []execution.ExchangeEndpoint{{Venue: "coinbase", URL: fastPingSrv.URL}}
	opts.orderPollInterval = 10 * time.Millisecond

	report, err := executeWithOptions(ctx, opts)
	if err != nil {
		t.Fatalf("executeWithOptions live TTL failed: %v", err)
	}
	if !report.LiveArmed || report.DryRun || report.SimulatedBalances || report.NavUSD != 1000.0 {
		t.Fatalf("unexpected live report header: armed=%v dry=%v sim=%v nav=%.2f",
			report.LiveArmed, report.DryRun, report.SimulatedBalances, report.NavUSD)
	}
	places, gets, cancels := placeCount.Load(), getOrderCount.Load(), cancelCount.Load()
	if places == 0 || cancels != places || gets < places {
		t.Fatalf("unexpected call counts: place=%d getOrder=%d cancel=%d", places, gets, cancels)
	}
	for _, res := range report.Results {
		if !res.CancelledOnTTL || res.Receipt.Status != "CANCELLED" {
			t.Fatalf("expected CancelledOnTTL=true & CANCELLED status, got %+v", res)
		}
	}
}

func TestDefaultTrendDir(t *testing.T) {
	touchTrendState := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "trend_state.json"), []byte("{}"), 0o644); err != nil {
			t.Fatalf("write trend_state.json in %s: %v", dir, err)
		}
	}

	cases := []struct {
		name       string
		hasData    bool
		hasLegacy  bool
		wantLegacy bool
	}{
		{
			name:    "resolves data trend dir when state exists",
			hasData: true,
		},
		{
			name:      "prefers data trend dir over legacy when both exist",
			hasData:   true,
			hasLegacy: true,
		},
		{
			name:       "falls back to legacy home dir only when data state absent",
			hasLegacy:  true,
			wantLegacy: true,
		},
		{
			name: "defaults to data trend dir when neither exists",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			dataDir := filepath.Join(root, "data")
			t.Setenv("HOME", home)
			t.Setenv("PIQUANT_DATA_DIR", dataDir)

			dataTrend := filepath.Join(dataDir, "trend")
			legacyTrend := filepath.Join(home, "piquant-trend")
			if tc.hasData {
				touchTrendState(t, dataTrend)
			}
			if tc.hasLegacy {
				touchTrendState(t, legacyTrend)
			}

			want := dataTrend
			if tc.wantLegacy {
				want = legacyTrend
			}
			if got := defaultTrendDir(); got != want {
				t.Fatalf("defaultTrendDir() = %q, want %q", got, want)
			}
			opts, err := parseExecuteOptions(nil)
			if err != nil {
				t.Fatalf("parseExecuteOptions(nil) failed: %v", err)
			}
			if opts.dir != want {
				t.Fatalf("parseExecuteOptions(nil).dir = %q, want %q", opts.dir, want)
			}
		})
	}
}
