package main

import (
	"strings"
	"testing"
	"time"

	"piquant-live/internal/paper"
	"piquant-live/internal/trend"
)

func TestFeedStale(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	if s, _ := feedStale(0, now.Add(-5*time.Minute), now); s {
		t.Fatal("no data 5 min after start is still warm-up")
	}
	if s, _ := feedStale(0, now.Add(-11*time.Minute), now); !s {
		t.Fatal("no data 11 min after start should alert")
	}
	if s, _ := feedStale(now.Add(-time.Minute).UnixNano(), now.Add(-time.Hour), now); s {
		t.Fatal("fresh feed flagged")
	}
	if s, why := feedStale(now.Add(-6*time.Minute).UnixNano(), now.Add(-time.Hour), now); !s || why == "" {
		t.Fatal("6-minute-old feed should alert")
	}
}

func TestDailySummary(t *testing.T) {
	st := paper.DryRunState{Equity: 180, PeakEquity: 200, CircuitHalted: true, CircuitHaltReason: "drawdown"}
	body := dailySummary(st, []trend.Summary{{Name: "P1_btc_eth_2x", Equity: 1.02, MaxDD: 0.05, CurrentPos: 1}},
		time.Date(2026, 11, 12, 0, 15, 0, 0, time.UTC))
	for _, want := range []string{"2026-11-12", "drop from peak 10.0%", "Halt reason: drawdown", "P1_btc_eth_2x"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in\n%s", want, body)
		}
	}
}
