package collect

import (
	"strings"
	"testing"
	"time"
)

func TestCheckTicks(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	ok := TickStatus{UpdatedAt: now, Connected: map[string]bool{"coinbase": true, "kraken": true},
		LastTrade: map[string]int64{"BTC-USD": now.UnixMilli(), "binanceus-BTCUSDT": now.Add(-3 * time.Hour).UnixMilli()},
		TradeGaps: map[string]int64{"BTC-USD": 2}}
	if p := CheckTicks(nil, ok, now); len(p) != 0 {
		t.Fatalf("healthy status flagged: %+v", p)
	}
	bad := ok
	bad.UpdatedAt = now.Add(-10 * time.Minute)
	bad.Connected = map[string]bool{"coinbase": true, "kraken": false}
	bad.LastTrade = map[string]int64{"BTC-USD": now.Add(-20 * time.Minute).UnixMilli()}
	bad.TradeGaps = map[string]int64{"BTC-USD": 5}
	if p := CheckTicks(nil, bad, now); len(p) != 2 { // first check: no disconnect or gap verdicts yet
		t.Fatalf("first check: %+v", p)
	}
	prevDown := ok
	prevDown.Connected = map[string]bool{"coinbase": true, "kraken": false}
	p := CheckTicks(&prevDown, bad, now)
	var keys []string
	for _, x := range p {
		keys = append(keys, x.Key)
	}
	got := strings.Join(keys, ",")
	if got != "ticks-status,venue-kraken,stale-BTC-USD,gap-BTC-USD" {
		t.Fatalf("keys %s", got)
	}
	if !strings.Contains(p[3].Msg, "3 new") {
		t.Fatalf("gap msg %s", p[3].Msg)
	}
}
