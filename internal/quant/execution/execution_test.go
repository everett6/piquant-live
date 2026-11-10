package execution

import (
	"math"
	"testing"
	"time"
)

func TestTWAPRouter(t *testing.T) {
	router := NewTWAPRouter(10 * time.Second)
	startTime := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

	// Test invalid parameters
	if _, err := router.ScheduleSlices(-1, 60*time.Second, startTime); err == nil {
		t.Errorf("expected error for negative quantity")
	}
	if _, err := router.ScheduleSlices(1.5, 0, startTime); err == nil {
		t.Errorf("expected error for zero duration")
	}

	// Test valid slicing: 1.0 BTC over 60 seconds with 10s interval = 6 slices
	totalQty := 1.0
	slices, err := router.ScheduleSlices(totalQty, 60*time.Second, startTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(slices) != 6 {
		t.Fatalf("expected 6 slices, got %d", len(slices))
	}

	var sumQty float64
	for i, s := range slices {
		if s.Index != i {
			t.Errorf("slice %d had index %d", i, s.Index)
		}
		if s.TotalSlices != 6 {
			t.Errorf("expected TotalSlices 6, got %d", s.TotalSlices)
		}
		expectedTime := startTime.Add(time.Duration(i*10) * time.Second)
		if !s.ScheduledTime.Equal(expectedTime) {
			t.Errorf("slice %d time mismatch: expected %v, got %v", i, expectedTime, s.ScheduledTime)
		}
		sumQty += s.Quantity
	}

	if math.Abs(sumQty-totalQty) > 1e-9 {
		t.Errorf("sum of slices %f != total %f", sumQty, totalQty)
	}
}

func TestVWAPRouter(t *testing.T) {
	profile := DefaultCryptoVWAPProfile()
	router := NewVWAPRouter(profile, 1*time.Hour)

	// 24 hours schedule starting at 00:00 UTC
	startTime := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	totalQty := 10.0

	slices, err := router.ScheduleSlices(totalQty, 24*time.Hour, startTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(slices) != 24 {
		t.Fatalf("expected 24 slices, got %d", len(slices))
	}

	var sumQty float64
	for i, s := range slices {
		sumQty += s.Quantity
		// US-EU overlap hour 14 should have higher quantity than Asian lull hour 4
		if i == 14 && s.Quantity <= slices[4].Quantity {
			t.Errorf("expected US-EU peak (slice 14: %f) > Asian lull (slice 4: %f)", s.Quantity, slices[4].Quantity)
		}
	}

	if math.Abs(sumQty-totalQty) > 1e-9 {
		t.Errorf("sum of slices %f != total %f", sumQty, totalQty)
	}
}

func TestAdaptiveShortfallRouter(t *testing.T) {
	router := NewAdaptiveShortfallRouter(1e-4, 0.05)

	// Zero or negative input
	if res := router.ComputeSchedule(0, 5, 20.0, 100000.0, 0.3); res != nil {
		t.Errorf("expected nil for 0 quantity")
	}
	if res := router.ComputeSchedule(1.0, 0, 20.0, 100000.0, 0.3); res != nil {
		t.Errorf("expected nil for 0 slices")
	}

	// Compare calm execution vs toxic execution
	totalQty := 5.0
	numSlices := 10
	volatility := 50.0 // 50 bps
	depth := 200000.0

	calmSlices := router.ComputeSchedule(totalQty, numSlices, volatility, depth, 0.15)
	toxicSlices := router.ComputeSchedule(totalQty, numSlices, volatility, depth, 0.85)

	if len(calmSlices) != numSlices || len(toxicSlices) != numSlices {
		t.Fatalf("expected %d slices for both", numSlices)
	}

	// Verify exact sum allocation
	var sumCalm, sumToxic float64
	for i := 0; i < numSlices; i++ {
		sumCalm += calmSlices[i].Quantity
		sumToxic += toxicSlices[i].Quantity
	}
	if math.Abs(sumCalm-totalQty) > 1e-8 {
		t.Errorf("calm slices sum mismatch: %f vs %f", sumCalm, totalQty)
	}
	if math.Abs(sumToxic-totalQty) > 1e-8 {
		t.Errorf("toxic slices sum mismatch: %f vs %f", sumToxic, totalQty)
	}

	// In calm markets (low VPIN), the algorithm frontloads more to reduce volatility risk (higher initial slice).
	// In toxic markets (high VPIN), eta is amplified 2.5x, slowing down the pace so initial slice is smaller.
	if calmSlices[0].Quantity <= toxicSlices[0].Quantity {
		t.Errorf("expected calm initial slice (%f) > toxic initial slice (%f)", calmSlices[0].Quantity, toxicSlices[0].Quantity)
	}
}

func TestAdaptiveShortfallRouter_ZeroEtaProtection(t *testing.T) {
	// If TempImpactEta or RiskAversionLambda are 0 or uninitialized, must not panic or return NaNs
	badRouter := &AdaptiveShortfallRouter{}
	slices := badRouter.ComputeSchedule(10.0, 5, 20.0, 50000.0, 0.5)
	if len(slices) != 5 {
		t.Fatalf("expected 5 slices from uninitialized router, got %d", len(slices))
	}
	var sum float64
	for _, s := range slices {
		if math.IsNaN(s.Quantity) || math.IsInf(s.Quantity, 0) {
			t.Fatalf("slice quantity is NaN/Inf")
		}
		sum += s.Quantity
	}
	if math.Abs(sum-10.0) > 1e-8 {
		t.Fatalf("expected sum 10.0, got %f", sum)
	}
}

func TestTWAPRouter_ZeroIntervalProtection(t *testing.T) {
	// Zero interval in raw struct must not panic with integer division by zero
	tw := &TWAPRouter{SliceInterval: 0}
	slices, err := tw.ScheduleSlices(10.0, 10*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slices) == 0 {
		t.Fatalf("expected slices scheduled")
	}
}

