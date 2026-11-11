package orchestrator

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestLatencyAdaptiveOrchestrator_Transitions(t *testing.T) {
	cfg := DefaultOrchestratorConfig()
	cfg.LowLatencyThresholdMs = 15.0
	cfg.HighLatencyThresholdMs = 150.0
	cfg.HighFundingThresholdAPR = 12.0
	cfg.LatencyAlpha = 0.50 // Fast EWMA for test

	orch := NewLatencyAdaptiveOrchestrator(cfg)
	now := time.Now()

	// Initial default is Medium
	rep := orch.Report(now)
	if rep.CurrentMode != ModeMediumLatency {
		t.Fatalf("expected initial ModeMediumLatency, got %v", rep.CurrentMode)
	}

	// 1. Shift to Low Latency (5ms ping, 2% funding)
	for i := 0; i < 10; i++ {
		orch.RecordLatencyUpdate(5*time.Millisecond, 2.0, now.Add(time.Duration(i)*time.Second))
	}
	rep = orch.Report(now.Add(10 * time.Second))
	if rep.CurrentMode != ModeLowLatency {
		t.Fatalf("expected ModeLowLatency for 5ms ping, got %v", rep.CurrentMode)
	}
	if rep.Weights.PassiveMM != 0.70 {
		t.Errorf("expected PassiveMM weight 0.70, got %f", rep.Weights.PassiveMM)
	}

	// 2. Shift back to Medium Latency (80ms ping, 3% funding - California typical)
	for i := 0; i < 10; i++ {
		orch.RecordLatencyUpdate(80*time.Millisecond, 3.0, now.Add(time.Duration(10+i)*time.Second))
	}
	rep = orch.Report(now.Add(20 * time.Second))
	if rep.CurrentMode != ModeMediumLatency {
		t.Fatalf("expected ModeMediumLatency for 80ms ping, got %v", rep.CurrentMode)
	}
	if rep.Weights.OBIMomentum != 0.50 {
		t.Errorf("expected OBIMomentum weight 0.50, got %f", rep.Weights.OBIMomentum)
	}

	// 3. Shift to High Latency (220ms ping, 2% funding)
	for i := 0; i < 10; i++ {
		orch.RecordLatencyUpdate(220*time.Millisecond, 2.0, now.Add(time.Duration(20+i)*time.Second))
	}
	rep = orch.Report(now.Add(30 * time.Second))
	if rep.CurrentMode != ModeHighLatency {
		t.Fatalf("expected ModeHighLatency for 220ms ping, got %v", rep.CurrentMode)
	}
	if rep.Weights.FundingCarry != 0.55 {
		t.Errorf("expected FundingCarry weight 0.55, got %f", rep.Weights.FundingCarry)
	}

	// 4. Test High Funding Yield Override:
	// Even with medium latency (50ms ping), high funding APR (18.0%) must trigger ModeHighLatency carry!
	orch.RecordLatencyUpdate(50*time.Millisecond, 18.0, now.Add(35*time.Second))
	rep = orch.Report(now.Add(35 * time.Second))
	if rep.CurrentMode != ModeHighLatency {
		t.Fatalf("expected ModeHighLatency triggered by 18%% funding APR, got %v", rep.CurrentMode)
	}
}

func TestLatencyAdaptiveOrchestrator_BlendAlpha(t *testing.T) {
	cfg := DefaultOrchestratorConfig()
	cfg.LatencyAlpha = 1.0 // Instant for direct test
	orch := NewLatencyAdaptiveOrchestrator(cfg)
	now := time.Now()

	// In Low Latency mode: PassiveMM (0.70), OBIMomentum (0.20), FundingCarry (0.05), Trend (0.05)
	orch.RecordLatencyUpdate(8*time.Millisecond, 1.0, now)

	score, horizon := orch.BlendAlpha(1.0, 0.5, 0.0, 0.0)
	// Expected: 0.70*1.0 + 0.20*0.5 + 0.05*0 + 0.05*0 = 0.80
	if math.Abs(score-0.80) > 1e-4 {
		t.Errorf("expected blended score 0.80, got %f", score)
	}
	if horizon == "" {
		t.Errorf("expected non-empty horizon")
	}

	// In Medium Latency mode: PassiveMM (0.20), OBIMomentum (0.50), FundingCarry (0.15), Trend (0.15)
	orch.RecordLatencyUpdate(90*time.Millisecond, 1.0, now.Add(time.Second))
	scoreMed, _ := orch.BlendAlpha(0.0, 1.0, 0.0, 1.0)
	// Expected: 0.20*0 + 0.50*1.0 + 0.15*0 + 0.15*1.0 = 0.65
	if math.Abs(scoreMed-0.65) > 1e-4 {
		t.Errorf("expected blended score 0.65, got %f", scoreMed)
	}
}

func TestLatencyAdaptiveOrchestrator_ConcurrentSafety(t *testing.T) {
	cfg := DefaultOrchestratorConfig()
	orch := NewLatencyAdaptiveOrchestrator(cfg)

	var wg sync.WaitGroup
	workers := 8
	iterations := 100

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				lat := time.Duration(10+i*2) * time.Millisecond
				funding := float64(i % 15)
				now := time.Now()
				orch.RecordLatencyUpdate(lat, funding, now)
				_, _ = orch.BlendAlpha(0.5, -0.2, 0.1, 0.8)
				_ = orch.Report(now)
			}
		}(w)
	}

	wg.Wait()
}

func TestHole40_OrchestratorNaNProtection(t *testing.T) {
	badCfg := OrchestratorConfig{
		LowLatencyThresholdMs:   math.NaN(),
		HighLatencyThresholdMs:  math.NaN(),
		HighFundingThresholdAPR: math.NaN(),
		LatencyAlpha:            math.NaN(),
	}
	orch := NewLatencyAdaptiveOrchestrator(badCfg)
	if orch.cfg.LowLatencyThresholdMs <= 0 || orch.cfg.HighLatencyThresholdMs <= 0 || orch.cfg.LatencyAlpha <= 0 {
		t.Fatalf("expected valid default config from NaN initialization, got %+v", orch.cfg)
	}

	mode := orch.RecordLatencyUpdate(10*time.Millisecond, math.NaN(), time.Now())
	if mode == "" {
		t.Fatalf("RecordLatencyUpdate returned empty mode")
	}

	rep := orch.Report(time.Now())
	if math.IsNaN(rep.SmoothedLatencyMs) || math.IsNaN(rep.FundingAPR) {
		t.Fatalf("Report contained NaN metrics: %+v", rep)
	}
}

