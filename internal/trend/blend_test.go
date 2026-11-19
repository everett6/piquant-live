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

func TestQ42ExpectedDrawdownAndDualSpeedBlends(t *testing.T) {
	st := pairState(t)
	// Rising trend then a pullback that drops below SMA20 while staying above SMA50,
	// forcing Q42_r3_fastexit_qd_spot_1x to exit earlier than Q42_safe_qd_spot_1x and generating drawdown > 0.
	seqBTC := append(rising(35, 160, 2), 205, 198, 215, 225)
	seqETH := append(rising(35, 80, 1), 102, 99, 108, 113)
	st.Advance(days("2024-01-02", seqBTC...))
	st.AdvanceETH(days("2024-01-02", seqETH...))

	lQD := st.Ledgers["Q42_safe_qd_spot_1x"]
	lR3 := st.Ledgers["Q42_r3_fastexit_qd_spot_1x"]
	if lQD == nil || lR3 == nil {
		t.Fatal("missing Q42 ledger variants")
	}
	// On pullback day 36 (198), R3 coherence exits while QD remains > 0.
	if lR3.Days[36].PosOut != 0 {
		t.Fatalf("expected Q42 R3 fastexit to exit below SMA20 on day 36, got pos=%v", lR3.Days[36].PosOut)
	}
	if lQD.Days[36].PosOut <= 0 {
		t.Fatalf("expected Q42 Safe-QD to remain long above SMA50 on day 36, got pos=%v", lQD.Days[36].PosOut)
	}

	q42 := ComputeQ42Blends(st, nil, nil, nil)
	if len(q42) != 2 {
		t.Fatalf("expected 2 Q42 blends, got %d", len(q42))
	}
	dual, bal := q42[0], q42[1]
	if dual.Name != "Q42_dualspeed_60_40_carry_spot" || dual.CarrySleevePct != 40.0 {
		t.Fatalf("unexpected dual blend header: %+v", dual)
	}
	if bal.Name != "Q42_balanced_50_50_carry_spot" || bal.CarrySleevePct != 50.0 {
		t.Fatalf("unexpected balanced blend header: %+v", bal)
	}
	for _, b := range q42 {
		if b.MeanDD <= 0 || b.UlcerIndex < b.MeanDD || b.CDaR95 < b.MeanDD || b.MaxUnderwaterDays < 1 || b.MaxDD < b.CDaR95 {
			t.Fatalf("invalid expected drawdown invariants for %s: %+v", b.Name, b)
		}
	}

	// Verify computeCDaR95 matches exact P95 tail mean on a known slice.
	cdar := computeCDaR95([]float64{0, 0.01, 0.02, 0.03, 0.04, 0.05, 0.06, 0.07, 0.08, 0.10})
	if math.Abs(cdar-0.10) > 1e-12 {
		t.Fatalf("expected CDaR95 0.10, got %v", cdar)
	}
}

func TestQ44VolBalancedAndBHHarvesterBlends(t *testing.T) {
	st := pairState(t)
	seqBTC := append(rising(30, 160, 2), 185, 178, 195, 205)
	seqETH := append(rising(30, 80, 1), 92, 88, 98, 104)
	st.Advance(days("2024-01-02", seqBTC...))
	st.AdvanceETH(days("2024-01-02", seqETH...))

	lBH := st.Ledgers["Q44_bh_vol_harvester_spot_1x"]
	lQD := st.Ledgers["Q44_qd_vol_1x_2p5x_spot"]
	if lBH == nil || lQD == nil {
		t.Fatal("missing Q44 ledger variants")
	}
	// On pullback day 31 (178), BH Harvester increases exposure > 1.0x while vol-balanced QD stays in (0, 2.5].
	if lBH.Days[31].PosOut <= 1.0 || lBH.Days[31].PosOut > 1.35 {
		t.Fatalf("expected BH harvester > 1.0 on dip day 31, got %v", lBH.Days[31].PosOut)
	}
	if lQD.Days[29].PosOut <= 1.0 || lQD.Days[29].PosOut > 2.5 {
		t.Fatalf("expected 1x..2.5x vol-balanced QD in (1.0, 2.5] on low-vol uptrend day 29, got %v", lQD.Days[29].PosOut)
	}

	q44 := ComputeQ44Blends(st, nil, nil, nil)
	if len(q44) != 2 {
		t.Fatalf("expected 2 Q44 blends, got %d", len(q44))
	}
	bHarv, ok := SelectBlend(st, nil, nil, "q44_bh_harvester", nil)
	if !ok || bHarv.Name != "Q44_bh_vol_harvester_1x_2p5x_spot" || bHarv.CarrySleevePct != 25.0 {
		t.Fatalf("unexpected q44_bh_harvester blend: ok=%v %+v", ok, bHarv)
	}
}
