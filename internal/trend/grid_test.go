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

func TestTunnelShareMatchesTheFormulaAndTheSleeveUsesIt(t *testing.T) {
	// alternating +2% / -1% days: a rising series with a known, non-zero volatility
	var hist []Day
	p := 100.0
	for i := 0; i < 80; i++ {
		if i%2 == 0 {
			p *= 1.02
		} else {
			p *= 0.99
		}
		hist = append(hist, Day{Date: "d", Close: p})
	}
	c := hist[len(hist)-1].Close
	m, _ := sma(hist, 50)
	var rs []float64
	for i := len(hist) - 30; i < len(hist); i++ {
		rs = append(rs, hist[i].Close/hist[i-1].Close-1)
	}
	mean := 0.0
	for _, r := range rs {
		mean += r / 30
	}
	v := 0.0
	for _, r := range rs {
		v += (r - mean) * (r - mean) / 29
	}
	want := 1 - math.Exp(-2*math.Log(c/m)/math.Sqrt(v))
	got := tunnelShare(hist, c, m)
	if math.Abs(got-want) > 1e-12 || got <= 0 || got >= 1 {
		t.Fatalf("share: got %v want %v", got, want)
	}
	qb := Variant{Lev: 2, SMA: 50, Tunnel: true}
	if _, target := qb.sleeve(0, 0, hist); math.Abs(target-2*want) > 1e-12 {
		t.Fatalf("sleeve target: got %v want %v", target, 2*want)
	}
	if _, target := (Variant{Lev: 2, SMA: 50}).sleeve(0, 0, hist); target != 2 {
		t.Fatalf("P1 must be unchanged, got %v", target)
	}
	if tunnelShare(hist[:30], c, m) != 0 || tunnelShare(hist, m, m) != 0 {
		t.Fatal("no size without 31 closes or at the line")
	}
	// below the SMA the sleeve is flat
	down := append([]Day{}, hist...)
	down[len(down)-1].Close = 1
	if _, target := qb.sleeve(2, 0, down); target != 0 {
		t.Fatalf("below the SMA: got %v", target)
	}
}

func TestCoherenceShareRampsWithTrendAgeAndResets(t *testing.T) {
	var hist []Day
	for i := 0; i < 60; i++ { // flat, then a rise: the close first exceeds its SMA50 on the first rising day
		hist = append(hist, Day{Date: "d", Close: 100})
	}
	p := 100.0
	for age := 1; age <= 12; age++ {
		p *= 1.01
		hist = append(hist, Day{Date: "d", Close: p})
		want := 1 - math.Exp(-float64(age)/5)
		if got := coherenceShare(hist, 50); math.Abs(got-want) > 1e-12 {
			t.Fatalf("age %d: got %v want %v", age, got, want)
		}
	}
	qd := Variant{Lev: 2, SMA: 50, Coherence: true}
	if _, target := qd.sleeve(0, 0, hist); math.Abs(target-2*(1-math.Exp(-12.0/5))) > 1e-12 {
		t.Fatalf("sleeve target %v", target)
	}
	hist = append(hist, Day{Date: "d", Close: 1}) // one close below the SMA resets the age
	if coherenceShare(hist, 50) != 0 {
		t.Fatal("below the SMA the share is 0")
	}
	if _, target := qd.sleeve(2, 0, hist); target != 0 {
		t.Fatalf("below the SMA the sleeve is flat, got %v", target)
	}
	if coherenceShare(hist[:40], 50) != 0 {
		t.Fatal("no SMA, no size")
	}
	// a long trend saturates at full size and the 200-close cap does not matter
	long := make([]Day, 0, 400)
	q := 100.0
	for i := 0; i < 400; i++ {
		q *= 1.002
		long = append(long, Day{Date: "d", Close: q})
	}
	if got := coherenceShare(long, 50); got != 1 {
		t.Fatalf("long trend: got %v", got)
	}
}
