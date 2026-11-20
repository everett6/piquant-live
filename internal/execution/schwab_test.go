package execution

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSchwabAuthorizeAndRefreshRequest(t *testing.T) {
	u := SchwabAuthorizeURL("app123", "https://127.0.0.1:8443/cb")
	if !strings.Contains(u, "https://api.schwabapi.com/v1/oauth/authorize?") || !strings.Contains(u, "client_id=app123") {
		t.Fatalf("unexpected auth URL: %s", u)
	}
	req, err := BuildSchwabRefreshRequest(context.Background(), "", "app123", "sec456", "rtok789")
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != SchwabTokenURL {
		t.Fatalf("unexpected token URL: %s", req.URL.String())
	}
	if !strings.HasPrefix(req.Header.Get("Authorization"), "Basic ") {
		t.Fatalf("expected Basic auth header, got %q", req.Header.Get("Authorization"))
	}
}

func TestFetchSchwabQuotesAndPlanETFRebalance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-xyz" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.Contains(r.URL.RawQuery, "IBIT") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"IBIT": map[string]any{"quote": map[string]any{"bidPrice": 50.00, "askPrice": 50.02, "lastPrice": 50.01}},
			"ETHA": map[string]any{"quote": map[string]any{"bidPrice": 25.00, "askPrice": 25.01, "lastPrice": 25.00}},
			"SGOV": map[string]any{"quote": map[string]any{"bidPrice": 100.39, "askPrice": 100.40, "lastPrice": 100.40}},
		})
	}))
	defer srv.Close()

	quotes, err := FetchSchwabQuotes(context.Background(), srv.Client(), srv.URL, "tok-xyz", []string{"IBIT", "ETHA", "SGOV"})
	if err != nil {
		t.Fatal(err)
	}
	if quotes["IBIT"].SpreadBps <= 0 || quotes["IBIT"].SpreadBps > 5.0 {
		t.Fatalf("unexpected IBIT spread bps: %+v", quotes["IBIT"])
	}

	orders := PlanSchwabETFRebalance(
		10_000.0,
		35.0, 35.0, 0.0,
		map[string]int{"IBIT": 50, "ETHA": 139, "SGOV": 10},
		quotes,
	)
	// Desired IBIT: floor(3500/50.01) = 69 -> +19 shares BUY; ETHA unchanged (floor(3500/25.005)=139); SGOV: 0 - 10 = SELL 10
	if len(orders) != 2 {
		t.Fatalf("expected 2 orders, got %+v", orders)
	}
	if orders[0].Symbol != "IBIT" || orders[0].Side != "BUY" || orders[0].QuantityShares != 19 {
		t.Fatalf("unexpected first order: %+v", orders[0])
	}
	if orders[1].Symbol != "SGOV" || orders[1].Side != "SELL" || orders[1].QuantityShares != 10 {
		t.Fatalf("unexpected second order: %+v", orders[1])
	}
}

func TestSchwabExchangeRefreshAccountsAndPlanPipeline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth/token":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Basic ") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = r.ParseForm()
			gt := r.Form.Get("grant_type")
			if gt == "authorization_code" && r.Form.Get("code") == "auth-code-1" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token":  "acc-from-code",
					"refresh_token": "ref-new-99",
					"token_type":    "Bearer",
					"expires_in":    1800,
				})
				return
			}
			if gt == "refresh_token" && r.Form.Get("refresh_token") == "ref-new-99" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token":  "acc-live-1",
					"refresh_token": "ref-new-99",
					"token_type":    "Bearer",
					"expires_in":    1800,
				})
				return
			}
			w.WriteHeader(http.StatusBadRequest)
		case "/trader/v1/accounts":
			if r.Header.Get("Authorization") != "Bearer acc-live-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"securitiesAccount": map[string]any{
						"accountNumber": "98765432",
						"currentBalances": map[string]any{
							"liquidationValue": 5000.0,
							"cashBalance":      1200.0,
						},
						"positions": []map[string]any{
							{"longQuantity": 10, "instrument": map[string]any{"symbol": "IBIT"}},
							{"longQuantity": 40, "instrument": map[string]any{"symbol": "ETHA"}},
						},
					},
				},
			})
		case "/marketdata/v1/quotes":
			if r.Header.Get("Authorization") != "Bearer acc-live-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"IBIT": map[string]any{"quote": map[string]any{"bidPrice": 50.0, "askPrice": 50.02, "lastPrice": 50.01}},
				"ETHA": map[string]any{"quote": map[string]any{"bidPrice": 25.0, "askPrice": 25.02, "lastPrice": 25.01}},
				"SGOV": map[string]any{"quote": map[string]any{"bidPrice": 100.0, "askPrice": 100.01, "lastPrice": 100.0}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	envFile := filepath.Join(t.TempDir(), "schwab.env")
	creds := SchwabCredentials{
		ClientID:     "my-app",
		ClientSecret: "my-secret",
		RedirectURI:  "https://127.0.0.1:8443/cb",
	}
	tok, err := ExchangeSchwabAuthCode(context.Background(), srv.Client(), srv.URL+"/v1/oauth/token", creds.ClientID, creds.ClientSecret, "auth-code-1", creds.RedirectURI)
	if err != nil || tok.RefreshToken != "ref-new-99" {
		t.Fatalf("exchange failed: tok=%+v err=%v", tok, err)
	}
	creds.RefreshToken = tok.RefreshToken
	if err := SaveSchwabCredentials(envFile, creds); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(envFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 perms on schwab.env, got %v err=%v", info.Mode().Perm(), err)
	}
	loaded := LoadSchwabCredentials(envFile)
	if !loaded.Ready() || loaded.RefreshToken != "ref-new-99" {
		t.Fatalf("loaded credentials mismatch: %+v", loaded)
	}

	plan := BuildSchwabPlanFromTargets(
		context.Background(), srv.Client(), loaded,
		srv.URL+"/v1/oauth/token", srv.URL+"/marketdata/v1", srv.URL+"/trader/v1",
		"Q42_dualspeed_60_40_carry_spot", 30.0, 30.0, 0.0, 40.0, 0,
	)
	if !plan.Connected || plan.AccountHash != "***5432" || plan.NavUSD != 5000.0 {
		t.Fatalf("unexpected connected plan: %+v", plan)
	}
	// At $5000 NAV, 30% IBIT ($1500 / 50.01 = 29 sh vs 10 held => BUY 19)
	// 30% ETHA ($1500 / 25.01 = 59 sh vs 40 held => BUY 19)
	if len(plan.Orders) != 2 || plan.Orders[0].Symbol != "IBIT" || plan.Orders[0].QuantityShares != 19 || plan.Orders[1].Symbol != "ETHA" || plan.Orders[1].QuantityShares != 19 {
		t.Fatalf("unexpected rebalance orders: %+v", plan.Orders)
	}
}
