package execution

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
