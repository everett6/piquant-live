package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"piquant-live/internal/collect"
	"piquant-live/internal/trend"
)

func TestSpotPriceReadsTheTicker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/products/BTC-USD/ticker" {
			_, _ = w.Write([]byte(`{"price":"91234.5"}`))
			return
		}
		http.Error(w, "no", http.StatusNotFound)
	}))
	defer srv.Close()
	old := collect.CoinbaseCandlesBase
	collect.CoinbaseCandlesBase = srv.URL
	defer func() { collect.CoinbaseCandlesBase = old }()
	p, err := spotPrice(context.Background(), srv.Client(), "BTC-USD")
	if err != nil || p != 91234.5 {
		t.Fatalf("got %v %v", p, err)
	}
	if _, err := spotPrice(context.Background(), srv.Client(), "ETH-USD"); err == nil {
		t.Fatal("want an error for HTTP 404")
	}
}

func TestEmergencyPassSavesOnlyWhenALedgerStops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trend_state.json")
	st := trend.NewState("2026-01-01")
	for name, l := range st.Ledgers {
		if name != "P1_btc_eth_2x" {
			delete(st.Ledgers, name)
			continue
		}
		l.Pos, l.PosAlt = 2, 2
		l.Days = []trend.DayRec{{Date: "2026-11-18", Equity: 1, CloseB: 100, CloseE: 10}}
	}
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}
	if f := emergencyPass(path, 95, 9.5, time.Now()); len(f) != 0 {
		t.Fatalf("fired at -10%%: %v", f)
	}
	if f := emergencyPass(path, 80, 8, time.Now()); len(f) != 1 {
		t.Fatalf("want one stop at -40%%, got %v", f)
	}
	back, err := trend.Load(path)
	if err != nil || back.Ledgers["P1_btc_eth_2x"].Pos != 0 || back.Ledgers["P1_btc_eth_2x"].EmergencyStops != 1 {
		t.Fatalf("not saved: %v", err)
	}
}
