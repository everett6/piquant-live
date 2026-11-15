package trend

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"piquant-live/internal/carry"
	"piquant-live/internal/execution"
)

func TestSoftTaperReducesAndNeverLocks(t *testing.T) {
	if got := softTaper(0.05, 0.10, 0.28, 0.40); got != 1.0 {
		t.Fatalf("below start want 1.0, got %v", got)
	}
	if got := softTaper(0.19, 0.10, 0.28, 0.40); math.Abs(got-0.70) > 1e-12 {
		t.Fatalf("midpoint want 0.70, got %v", got)
	}
	if got := softTaper(0.45, 0.10, 0.28, 0.40); got != 0.40 {
		t.Fatalf("floor want 0.40, got %v", got)
	}
}

func TestS36VariantsAdvanceAndTaperOnDrawdown(t *testing.T) {
	st := pairState(t)
	// First 40 rising days so SMA50 lags well below price, then a -16% pullback
	// that stays above SMA50 (so QD remains long) while crossing TaperStart=0.10.
	seqBTC := append(rising(40, 160, 2), 200, 242)
	seqETH := append(rising(40, 80, 1), 100, 121)
	st.Advance(days("2024-01-02", seqBTC...))
	st.AdvanceETH(days("2024-01-02", seqETH...))

	lSpot := st.Ledgers["S36_safe_qd_spot_1x"]
	lFut := st.Ledgers["S36_safe_qd_fut_1x"]
	lQD := st.Ledgers["QD_btc_eth_coherence_2x"]
	if lSpot == nil || lFut == nil || lQD == nil {
		t.Fatal("missing S36 or QD variants in ensureLedgers")
	}
	if len(lSpot.Days) != len(seqBTC) {
		t.Fatalf("expected %d recorded days, got %d", len(seqBTC), len(lSpot.Days))
	}
	// On the drop day (index 40), dd > 0.10 so soft taper cuts target below untapered 1x QD (0.5 * 2x QD), yet > 0.
	dropIdx := 40
	dropDay := lSpot.Days[dropIdx]
	untapered1x := 0.5 * lQD.Days[dropIdx].PosOut
	if !(dropDay.PosOut > 0 && dropDay.PosOut < untapered1x-1e-6) {
		t.Fatalf("expected tapered non-zero pos < untapered1x (%v) on drop day, got %v", untapered1x, dropDay.PosOut)
	}
	// Spot pays 0 funding so spot equity > futures equity over same days.
	if lSpot.Equity <= lFut.Equity {
		t.Fatalf("expected spot equity (%v) > futures equity (%v)", lSpot.Equity, lFut.Equity)
	}
}

func TestComputeS36BlendWithCarryAndDTB3(t *testing.T) {
	st := pairState(t)
	st.Advance(days("2024-01-02", rising(5, 160, 1)...))
	st.AdvanceETH(days("2024-01-02", rising(5, 80, 0.5)...))

	cState := &carry.State{
		Days: []carry.DayRec{
			{Date: "2024-01-02", Ret: 0.001},
			{Date: "2024-01-03", Ret: 0.001},
		},
	}
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "DTB3.csv")
	if err := os.WriteFile(csvPath, []byte("date,rate\n2024-01-02,5.0\n2024-01-03,5.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rates, err := LoadDTB3Rates(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	plan := execution.PlanForLatency([]execution.VenueLatency{
		{Venue: "coinbase", Samples: 3, Successes: 3, HTTPMedianMs: 180, HTTPP95Ms: 203},
	}, time.Unix(1700000000, 0))

	b, ok := ComputeS36Blend(st, cState, rates, "S36_safe_qd_spot_1x", "S36_qd_carry_blend_spot", &plan)
	if !ok {
		t.Fatal("expected ComputeS36Blend to succeed")
	}
	if b.Days != 5 || b.CarryMatchedDays != 2 || b.LatestDTB3Pct != 5.0 {
		t.Fatalf("unexpected blend summary: %+v", b)
	}
	if b.CarrySleevePct != 30.0 || b.TargetBTCPortPct <= 0 || b.TargetETHPortPct <= 0 {
		t.Fatalf("unexpected weights: %+v", b)
	}

	// Day-0 initialized state before any forward closes still returns valid baseline targets.
	stEmpty := pairState(t)
	b0, ok0 := ComputeS36Blend(stEmpty, nil, rates, "S36_safe_qd_spot_1x", "S36_qd_carry_blend_spot", &plan)
	if !ok0 || b0.Days != 0 || b0.Equity != 1.0 || b0.IdleTrendCashPct != 70.0 || b0.LatestDTB3Pct != 5.0 {
		t.Fatalf("unexpected day-0 blend summary: ok=%v %+v", ok0, b0)
	}
}
