package trend

import (
	"math"
	"testing"
)

const t0 = int64(1_700_006_400) // 2023-11-15 00:00 UTC

func gbars(closes []float64) []Bar {
	out := make([]Bar, len(closes))
	for i, c := range closes {
		out[i] = Bar{T: t0 + int64(i)*gridSec, Close: c}
	}
	return out
}

func gridRise(n int, step float64) []float64 {
	out := make([]float64, n)
	p := 100.0
	for i := range out {
		p *= 1 + step
		out[i] = p
	}
	return out
}

func gridState() *State {
	st := NewState("2023-01-01")
	for name, l := range st.Ledgers {
		if !l.Variant.Clocks {
			delete(st.Ledgers, name)
		}
	}
	return st
}

func TestClockTargetNeedsFiftyClosesPerClock(t *testing.T) {
	up := gbars(gridRise(300, 0.001))
	if got := clockTarget(up, 2, 50); got != 2 {
		t.Fatalf("steady rise: want 2, got %v", got)
	}
	// 199 bars: only the clock that closes on the last bar has 50 daily closes (49*4+1 = 197 bars back); the
	// clocks 6 and 12 hours earlier fit too, the one 18 hours earlier needs 200.
	if got := clockTarget(up[:199], 2, 50); got != 1.5 {
		t.Fatalf("199 bars: want 1.5, got %v", got)
	}
	if got := clockTarget(up[:196], 2, 50); got != 0 {
		t.Fatalf("196 bars: want 0, got %v", got)
	}
	if got := clockTarget(gbars(gridRise(300, -0.001)), 2, 50); got != 0 {
		t.Fatalf("steady fall: want 0, got %v", got)
	}
}

func TestClockTargetEachClockSeesOnlyItsOwnClose(t *testing.T) {
	c := gridRise(300, 0.001)
	c[299] = 1 // crash in the last bar: only the clock closing now sees it
	if got := clockTarget(gbars(c), 2, 50); got != 1.5 {
		t.Fatalf("one clock out: want 1.5, got %v", got)
	}
	c[298] = 1 // two clocks have now closed below
	if got := clockTarget(gbars(c), 2, 50); got != 1.0 {
		t.Fatalf("two clocks out: want 1.0, got %v", got)
	}
}

func TestClockTargetMissingCloseLeavesThatClockFlat(t *testing.T) {
	b := gbars(gridRise(300, 0.001))
	gap := append(append([]Bar{}, b[:250]...), b[251:]...) // bar 250 is one clock's daily close inside the window
	if got := clockTarget(gap, 2, 50); got != 1.5 {
		t.Fatalf("want 1.5, got %v", got)
	}
}

func TestAdvanceGridSeedsThenStepsWithCostsAndIsIdempotent(t *testing.T) {
	st := gridState()
	c := gridRise(302, 0.001)
	b := gbars(c)
	if n := st.AdvanceGrid(b[:300], b[:300]); n != 0 || st.GridLast != b[299].T {
		t.Fatalf("seed: n=%d last=%d", n, st.GridLast)
	}
	l := st.Ledgers["E4_btc_eth_4clock_2x"]
	if l.Equity != 1 || len(l.Days) != 0 {
		t.Fatal("seed must not touch the ledger")
	}
	if n := st.AdvanceGrid(b, b); n != 2 {
		t.Fatalf("want 2 steps, got %d", n)
	}
	// step 1: flat over the bar, enters 2x on both halves: cost 2 * 7 bps
	if d := l.Days[0]; math.Abs(d.Ret-(-2*7e-4)) > 1e-12 || d.PosB != 2 || d.PosE != 2 {
		t.Fatalf("entry step: %+v", d)
	}
	// step 2: 2x over a +0.1% bar less 6 hours of funding at 0.1 bp/h on 2 units
	want := 2*(c[301]/c[300]-1) - 2*0.1*6*1e-4
	if d := l.Days[1]; math.Abs(d.Ret-want) > 1e-12 || d.Date != "2024-01-29T06" {
		t.Fatalf("held step: %+v want %v", d, want)
	}
	eq := l.Equity
	if n := st.AdvanceGrid(b, b); n != 0 || l.Equity != eq || len(l.Days) != 2 {
		t.Fatal("repeat call must not step again")
	}
}

func TestAdvanceGridWaitsForBothCoinsAndLeavesDailyLedgersAlone(t *testing.T) {
	st := NewState("2023-01-01")
	b := gbars(gridRise(302, 0.001))
	st.AdvanceGrid(b[:300], b[:300])
	if n := st.AdvanceGrid(b, b[:300]); n != 0 {
		t.Fatalf("ETH bar missing: want 0 steps, got %d", n)
	}
	if n := st.AdvanceGrid(b, b); n != 2 {
		t.Fatalf("want 2 steps, got %d", n)
	}
	for name, l := range st.Ledgers {
		if !l.Variant.Clocks && len(l.Days) != 0 {
			t.Fatalf("%s was stepped by the grid", name)
		}
	}
	// and the daily pair stepper must not step the four-clock ledger
	days := []Day{{Date: "2024-01-01", Close: 100}, {Date: "2024-01-02", Close: 101}, {Date: "2024-01-03", Close: 102}}
	st.Advance(append([]Day{}, days...))
	st.AdvanceETH(append([]Day{}, days...))
	if got := len(st.Ledgers["E4_btc_eth_4clock_2x"].Days); got != 2 {
		t.Fatalf("daily stepper touched the four-clock ledger: %d records", got)
	}
}
