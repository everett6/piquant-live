package paper

import (
	"testing"
	"time"

	"piquant-live/internal/engine"
	"piquant-live/internal/quant/orchestrator"
)

func TestQuantHedgeFundOrchestratorAndGuardrailsIntegration(t *testing.T) {
	tempDir := t.TempDir()
	cfg := DefaultDryRunConfig(tempDir)
	cfg.OrderLatency = 100 * time.Millisecond // California fiber latency

	ring := engine.NewSPSCRing(128)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)
	eng, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	now := time.Now()

	// 1. Initial State has QuantOrchestrator initialized
	st := eng.State()
	if st.QuantOrchestrator == nil {
		t.Fatalf("expected QuantOrchestrator report to be populated in State()")
	}
	t.Logf("Initial Orchestrator Mode: %s, Dominant Horizon: %s", st.QuantOrchestrator.CurrentMode, st.QuantOrchestrator.TargetHorizon)

	// 2. Medium-Latency Quoting (100ms): Strategy tag should reflect MEDIUM_LATENCY_OBI_MOMENTUM
	eng.evaluateQuotesAndGating("BTCUSDT", 75000.00, now)
	st = eng.State()
	btcOrders := st.ActiveOrders["BTCUSDT"]
	if len(btcOrders) == 0 {
		t.Fatalf("expected active maker orders placed for BTCUSDT")
	}
	foundStrategy := btcOrders[0].Strategy
	if foundStrategy != string(orchestrator.ModeMediumLatency) {
		t.Errorf("expected strategy tag %s, got %s", orchestrator.ModeMediumLatency, foundStrategy)
	}

	// 3. Low-Latency Environment Transition:
	// Simulate co-location (5ms latency updates)
	eng.cfg.OrderLatency = 5 * time.Millisecond
	for i := 0; i < 40; i++ {
		eng.quantOrch.RecordLatencyUpdate(5*time.Millisecond, 2.0, now.Add(time.Duration(i)*time.Millisecond))
	}
	rep := eng.quantOrch.Report(now.Add(time.Second))
	if rep.CurrentMode != orchestrator.ModeLowLatency {
		t.Fatalf("expected orchestrator mode %s, got %s (latency=%.2f)", orchestrator.ModeLowLatency, rep.CurrentMode, rep.SmoothedLatencyMs)
	}

	// Move price to trigger cancel-and-replace so new order receives new strategy tag
	eng.evaluateQuotesAndGating("BTCUSDT", 75100.00, now.Add(time.Second))
	st = eng.State()
	btcOrders = st.ActiveOrders["BTCUSDT"]
	foundLowLatency := false
	for _, ord := range btcOrders {
		if ord.Strategy == string(orchestrator.ModeLowLatency) {
			foundLowLatency = true
			break
		}
	}
	if !foundLowLatency {
		t.Errorf("expected at least one order with strategy tag %s, got %+v", orchestrator.ModeLowLatency, btcOrders)
	}

	// 4. Pre-Trade Guardrail Kill Switch Verification:
	// Simulate an adverse trailing drawdown of 2.5% (exceeding 2.0% max daily drawdown limit)
	eng.mu.Lock()
	eng.state.PeakEquity = 1000.0
	eng.state.Equity = 970.0 // -3.0% drawdown
	eng.mu.Unlock()

	eng.evaluateQuotesAndGating("BTCUSDT", 75000.00, now.Add(2*time.Second))
	st = eng.State()

	if !st.CircuitHalted {
		t.Fatalf("expected CircuitHalted=true after kill switch trip")
	}
	if len(st.ActiveOrders["BTCUSDT"]) != 0 {
		t.Errorf("expected all active opening orders pulled after kill switch trip, got %d", len(st.ActiveOrders["BTCUSDT"]))
	}
	t.Logf("Circuit Halted Reason: %s", st.CircuitHaltReason)
}
