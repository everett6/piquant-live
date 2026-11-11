package paper

import (
	"math"
	"testing"
	"time"

	"piquant-live/internal/engine"
)

func newExitTestEngine(t *testing.T, mutate func(*DryRunConfig)) *DryRunEngine {
	t.Helper()
	cfg := DefaultDryRunConfig(t.TempDir())
	cfg.Symbols = []string{"BTCUSDT", "ETHUSDT"}
	if mutate != nil {
		mutate(&cfg)
	}
	eng, err := NewDryRunEngine(cfg, engine.NewSPSCRing(64), nil)
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

func lastFill(t *testing.T, eng *DryRunEngine) TradeRecord {
	t.Helper()
	if len(eng.recentFills) == 0 {
		t.Fatal("no fills recorded")
	}
	return eng.recentFills[len(eng.recentFills)-1]
}

// A break-even close is a scratch: not a loss, and it must not extend a loss streak.
func TestScratchCloseNotCountedAsLoss(t *testing.T) {
	eng := newExitTestEngine(t, func(c *DryRunConfig) { c.ZeroFeeMaker = true })
	eng.mu.Lock()
	defer eng.mu.Unlock()
	eng.executeMakerFillLocked("BTCUSDT", 50000, 0.01, "BUY", 0, "t")
	eng.executeMakerFillLocked("BTCUSDT", 50000, 0.01, "SELL", 0, "t")
	if eng.state.WinningTrades != 0 || eng.state.LosingTrades != 0 {
		t.Fatalf("scratch counted: wins=%d losses=%d", eng.state.WinningTrades, eng.state.LosingTrades)
	}
	if eng.symMicro["BTCUSDT"].ConsecutiveLosses != 0 {
		t.Fatal("scratch extended the loss streak")
	}
}

// A close that clears its own exit fee but not the entry fee is a losing round trip.
func TestEntryFeeCountsAgainstRoundTrip(t *testing.T) {
	eng := newExitTestEngine(t, func(c *DryRunConfig) { c.MakerFeeBps = 1.5 })
	eng.mu.Lock()
	defer eng.mu.Unlock()
	eng.executeMakerFillLocked("ETHUSDT", 2000, 1, "BUY", 0, "t")
	if fee := eng.state.Positions["ETHUSDT"].EntryFeesUSDT; math.Abs(fee-0.30) > 1e-9 {
		t.Fatalf("entry fee not tracked: %v", fee)
	}
	eng.executeMakerFillLocked("ETHUSDT", 2000.5, 1, "SELL", 0, "t") // +0.50 gross, -0.30 exit fee, -0.30 entry fee
	if eng.state.LosingTrades != 1 || eng.state.WinningTrades != 0 {
		t.Fatalf("want 1 loss, got wins=%d losses=%d", eng.state.WinningTrades, eng.state.LosingTrades)
	}
	if eng.state.Positions["ETHUSDT"].EntryFeesUSDT != 0 {
		t.Fatal("entry fees not cleared when flat")
	}
}

// A partial close charges only its share of the entry fee.
func TestPartialCloseProratesEntryFee(t *testing.T) {
	eng := newExitTestEngine(t, func(c *DryRunConfig) { c.MakerFeeBps = 1.5 })
	eng.mu.Lock()
	defer eng.mu.Unlock()
	eng.executeMakerFillLocked("ETHUSDT", 2000, 1, "BUY", 0, "t")
	eng.executeMakerFillLocked("ETHUSDT", 2010, 0.5, "SELL", 0, "t")
	if fee := eng.state.Positions["ETHUSDT"].EntryFeesUSDT; math.Abs(fee-0.15) > 1e-9 {
		t.Fatalf("remaining entry fee = %v, want 0.15", fee)
	}
}

func TestStopLossExitsImmediatelyAsTaker(t *testing.T) {
	eng := newExitTestEngine(t, nil)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	eng.mu.Lock()
	eng.executeMakerFillLocked("BTCUSDT", 50000, 0.01, "BUY", 0, "t")
	eng.mu.Unlock()
	eng.evaluateQuotesAndGating("BTCUSDT", 49600, now) // -80 bps, beyond the 60 bps stop

	eng.mu.RLock()
	defer eng.mu.RUnlock()
	if eng.state.Positions["BTCUSDT"].Size != 0 {
		t.Fatalf("stop did not close position: %+v", eng.state.Positions["BTCUSDT"])
	}
	f := lastFill(t, eng)
	if f.Role != "TAKER" || f.Strategy != "EXIT_STOP_LOSS" || f.Side != "SELL" || f.FeeUSDT <= 0 {
		t.Fatalf("unexpected stop fill: %+v", f)
	}
	if eng.state.LosingTrades != 1 {
		t.Fatalf("stop-out not counted as loss: %d", eng.state.LosingTrades)
	}
}

func TestTimeStopClosesStaleLosingPosition(t *testing.T) {
	eng := newExitTestEngine(t, nil)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	eng.mu.Lock()
	eng.executeMakerFillLocked("BTCUSDT", 50000, 0.01, "BUY", 0, "t")
	eng.symMicro["BTCUSDT"].InventorySince = now.Add(-50 * time.Minute)
	eng.mu.Unlock()
	eng.evaluateQuotesAndGating("BTCUSDT", 49975, now) // -5 bps after 50 min (> 45 min time stop)

	eng.mu.RLock()
	defer eng.mu.RUnlock()
	if f := lastFill(t, eng); f.Strategy != "EXIT_TIME_STOP" || eng.state.Positions["BTCUSDT"].Size != 0 {
		t.Fatalf("expected time-stop exit, last fill %+v", f)
	}
}

func TestLossStreakClearsAfterCooldown(t *testing.T) {
	eng := newExitTestEngine(t, nil)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	eng.mu.Lock()
	sm := eng.symMicro["BTCUSDT"]
	sm.ConsecutiveLosses, sm.ScalpingDisabled = 4, true
	sm.LastCloseAt = now.Add(-31 * time.Minute)
	eng.mu.Unlock()
	eng.evaluateQuotesAndGating("BTCUSDT", 50000, now)

	eng.mu.RLock()
	defer eng.mu.RUnlock()
	if sm.ConsecutiveLosses != 0 || sm.ScalpingDisabled {
		t.Fatalf("loss streak not cleared: losses=%d disabled=%v", sm.ConsecutiveLosses, sm.ScalpingDisabled)
	}
}

func TestEmergencyFlattenResetsLifecycleAndSide(t *testing.T) {
	eng := newExitTestEngine(t, nil)
	eng.mu.Lock()
	defer eng.mu.Unlock()
	eng.executeMakerFillLocked("ETHUSDT", 2000, 0.5, "SELL", 0, "t") // open short
	p := eng.state.Positions["ETHUSDT"]
	p.MarkPrice, p.TrailingStop, p.PeakPrice = 1990, 1995, 1985
	eng.emergencyFlattenLocked("test")
	if p.Size != 0 || p.TrailingStop != 0 || p.PeakPrice != 0 || p.AvgEntryPrice != 0 {
		t.Fatalf("stale lifecycle state after flatten: %+v", p)
	}
	if f := lastFill(t, eng); f.Side != "BUY" {
		t.Fatalf("short cover recorded as %s", f.Side)
	}
}

func TestFuturesSizingContractsAndLeverageCap(t *testing.T) {
	eng := newExitTestEngine(t, func(c *DryRunConfig) {
		c.StopLossBps = 30
		if err := ApplyRegionProfile(c, RegionUSFutures); err != nil {
			t.Fatal(err)
		}
	})
	if eng.cfg.SpotOnly || eng.cfg.MaxLeverage != USFuturesMaxLeverage {
		t.Fatalf("us-futures not leveraged: %+v", eng.cfg)
	}
	clip, maxInv := eng.applyFuturesSizing("BTCUSDT", 200, 73700, 4, 0)
	if clip != 0.01 || maxInv < 0.01 {
		t.Fatalf("BTC nano sizing: clip=%v maxInv=%v, want one 0.01 contract", clip, maxInv)
	}
	clip, _ = eng.applyFuturesSizing("ETHUSDT", 200, 2063, 4, 0)
	if math.Abs(clip-0.1) > 1e-9 {
		t.Fatalf("ETH nano sizing: clip=%v, want 0.1", clip)
	}
	// With $600 already open elsewhere, a $737 BTC contract would exceed 5x on $200.
	eng.mu.Lock()
	eng.state.Positions["ETHUSDT"] = &Position{Symbol: "ETHUSDT", Size: 0.29, MarkPrice: 2063, AvgEntryPrice: 2063}
	eng.mu.Unlock()
	if clip, _ := eng.applyFuturesSizing("BTCUSDT", 200, 73700, 4, 0); clip != 0 {
		t.Fatalf("account leverage cap not enforced: clip=%v", clip)
	}
}

func TestFundingAccrualChargesOpenNotional(t *testing.T) {
	eng := newExitTestEngine(t, func(c *DryRunConfig) { c.FundingBpsPerHour = 1.0 })
	t0 := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	eng.mu.Lock()
	defer eng.mu.Unlock()
	eng.state.Positions["BTCUSDT"] = &Position{Symbol: "BTCUSDT", Size: 0.01, MarkPrice: 50000, AvgEntryPrice: 50000}
	cash := eng.state.Cash
	eng.accrueFundingLocked(t0)
	eng.accrueFundingLocked(t0.Add(time.Hour))
	if got := cash - eng.state.Cash; math.Abs(got-0.05) > 1e-9 {
		t.Fatalf("funding charged %v, want 0.05 ($500 x 1 bps)", got)
	}
}
