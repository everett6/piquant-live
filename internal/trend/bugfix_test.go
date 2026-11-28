package trend

import (
	"math"
	"testing"
	"time"
)

func TestParseEquityCSVReaderPropagatesError(t *testing.T) {
	malformed := []byte("date,open,high,low,close\n2026-01-01,100,101,99,\"unclosed\n")
	if _, err := parseEquityCSVReader(malformed); err == nil {
		t.Fatal("expected CSV parse error, got nil")
	}
}

func TestComputeQ57FullFundWithMacroRequiresBlend(t *testing.T) {
	macro := MacroSleeveSummary{Days: 10, TickersLoaded: 1}
	if _, ok := ComputeQ57FullFundWithMacro(nil, nil, nil, macro, nil); ok {
		t.Fatal("expected ok=false when st is nil")
	}
	emptyState := &State{Ledgers: map[string]*Ledger{}}
	if _, ok := ComputeQ57FullFundWithMacro(emptyState, nil, nil, macro, nil); ok {
		t.Fatal("expected ok=false when SelectBlend fails")
	}
}

func TestRejectNaNAndInfPrices(t *testing.T) {
	nan := math.NaN()
	posInf := math.Inf(1)
	negInf := math.Inf(-1)

	st := NewState("2026-01-01")
	st.Seed([]Day{
		{Date: "2026-01-01", Close: 100},
		{Date: "2026-01-02", Close: nan},
		{Date: "2026-01-03", Close: posInf},
		{Date: "2026-01-04", Close: negInf},
	})
	if len(st.History) != 1 || st.LastDate != "2026-01-01" {
		t.Fatalf("Seed accepted invalid prices: len=%d last=%s", len(st.History), st.LastDate)
	}

	st.SeedETH([]Day{{Date: "2026-01-01", Close: 10}, {Date: "2026-01-02", Close: posInf}})
	if len(st.ETHHistory) != 1 || st.ETHLast != "2026-01-01" {
		t.Fatalf("SeedETH accepted invalid prices: len=%d last=%s", len(st.ETHHistory), st.ETHLast)
	}

	st.SeedSOL([]Day{{Date: "2026-01-01", Close: 5}, {Date: "2026-01-02", Close: nan}})
	if len(st.SOLHistory) != 1 || st.SOLLast != "2026-01-01" {
		t.Fatalf("SeedSOL accepted invalid prices: len=%d last=%s", len(st.SOLHistory), st.SOLLast)
	}

	if n := st.Advance([]Day{{Date: "2026-01-05", Close: posInf}, {Date: "2026-01-06", Close: nan}}); n != 0 {
		t.Fatalf("Advance accepted invalid prices: n=%d", n)
	}
	if n := st.AdvanceETH([]Day{{Date: "2026-01-05", Close: posInf}}); n != 0 {
		t.Fatalf("AdvanceETH accepted invalid prices: n=%d", n)
	}
	if n := st.AdvanceSOL([]Day{{Date: "2026-01-05", Close: nan}}); n != 0 {
		t.Fatalf("AdvanceSOL accepted invalid prices: n=%d", n)
	}

	st.Backfill(
		[]Day{{Date: "2025-12-30", Close: posInf}, {Date: "2025-12-31", Close: 99}},
		[]Day{{Date: "2025-12-30", Close: nan}, {Date: "2025-12-31", Close: 9}},
	)
	if len(st.History) != 2 || st.History[0].Date != "2025-12-31" {
		t.Fatalf("prependOlder accepted invalid BTC price: %+v", st.History)
	}
	if len(st.ETHHistory) != 2 || st.ETHHistory[0].Date != "2025-12-31" {
		t.Fatalf("prependOlder accepted invalid ETH price: %+v", st.ETHHistory)
	}

	bars := mergeBars(nil, []Bar{
		{T: 0, Close: 100},
		{T: gridSec, Close: posInf},
		{T: 2 * gridSec, Close: nan},
	})
	if len(bars) != 1 || bars[0].T != 0 {
		t.Fatalf("mergeBars accepted invalid prices: %+v", bars)
	}
}

func TestPeakZeroGuardsAndGridETHFundingHours(t *testing.T) {
	// Guard l.Peak > 0 in step, stepPair, stepTri, stepGrid, EmergencyCheckAll
	hist := []Day{{Date: "2026-01-01", Close: 100}, {Date: "2026-01-02", Close: 100}}
	for _, fn := range []func(*Ledger){
		func(l *Ledger) { l.step("2026-01-02", 0, hist) },
		func(l *Ledger) { l.stepPair("2026-01-02", 0, 0, hist, hist) },
		func(l *Ledger) { l.stepTri("2026-01-02", 0, 0, 0, hist, hist, hist) },
		func(l *Ledger) {
			l.stepGrid("2026-01-02T00", 6, 6, 0, 0, []Bar{{T: 0, Close: 100}}, []Bar{{T: 0, Close: 10}})
		},
	} {
		l := &Ledger{Variant: Variant{Name: "test", Lev: 1, Pair: true, TriAsset: true}}
		fn(l)
		if math.IsNaN(l.MaxDD) || math.IsInf(l.MaxDD, 0) {
			t.Fatalf("MaxDD became NaN/Inf when Peak==0: %v", l.MaxDD)
		}
	}

	// EmergencyCheckAll with Peak==0 and Equity==0
	st := &State{Ledgers: map[string]*Ledger{
		"zero": {
			Variant: Variant{Name: "zero", Lev: 1},
			Pos:     1,
			Days:    []DayRec{{Date: "2026-01-01", CloseB: 100}},
		},
	}}
	_ = st.EmergencyCheckAll(50, 0, 0, time.Now())
	if math.IsNaN(st.Ledgers["zero"].MaxDD) {
		t.Fatal("EmergencyCheckAll produced NaN MaxDD when Peak==0")
	}

	// AdvanceGrid uses ETH bar interval for ETH funding hours
	stGrid := &State{Ledgers: map[string]*Ledger{
		"grid": {
			Variant: Variant{Name: "grid", Pair: true, Clocks: true, Lev: 1, FundingBpsH: 10},
			Equity:  1,
			Peak:    1,
			Pos:     0,
			PosAlt:  1, // only ETH is held so funding isolates ETH hours
		},
	}}
	t0 := int64(1700006400) // divisible by 6*3600
	t0 -= t0 % gridSec
	stGrid.AdvanceGrid(
		[]Bar{{T: t0, Close: 100}},
		[]Bar{{T: t0, Close: 10}},
	)
	// Next step: BTC has 6h bar (t0+6h), ETH skipped t0+6h and jumped 12h (t0+12h)
	n := stGrid.AdvanceGrid(
		[]Bar{{T: t0 + gridSec, Close: 100}, {T: t0 + 2*gridSec, Close: 100}},
		[]Bar{{T: t0 + 2*gridSec, Close: 10}},
	)
	if n != 1 {
		t.Fatalf("expected 1 grid step, got %d", n)
	}
	// ETH held pos=1 over 12h at 10 bps/h = 0.012, weighted 0.5 -> -0.006
	gotRet := stGrid.Ledgers["grid"].Days[0].Ret
	wantETHFund := -0.5 * (10 * 12 * 1e-4)
	if math.Abs(gotRet-wantETHFund) > 1e-12 {
		t.Fatalf("ETH funding return = %.8f, want %.8f (12h ETH interval)", gotRet, wantETHFund)
	}
}

func TestTaxGuardsAndEmergencyDate(t *testing.T) {
	if !heldOverYear("2025-01-01", "2026-01-05T15:04-emergency") {
		t.Fatal("expected heldOverYear=true for emergency-stop date > 1 year after open")
	}
	if heldOverYear("2025-01-01", "2025-06-05T15:04-emergency") {
		t.Fatal("expected heldOverYear=false for emergency-stop date < 1 year after open")
	}

	l := &Ledger{
		Variant: Variant{Name: "tax_test", FundingBpsH: 1},
		Days: []DayRec{
			{Date: "2026-01-01", Ret: -1.0, Equity: 1.0, PosB: 1, CloseB: 100},
			{Date: "2026-01-02", Ret: 0, Equity: 1.0, PosB: 0, CloseB: 100},
		},
	}
	rows, _, _ := l.TaxRows(1000)
	if len(rows) != 1 {
		t.Fatalf("expected 1 closed tax row, got %d", len(rows))
	}
	if math.IsNaN(rows[0].FundingUSD) || math.IsInf(rows[0].FundingUSD, 0) {
		t.Fatalf("FundingUSD is NaN/Inf when 1+Ret==0: %v", rows[0].FundingUSD)
	}
}
