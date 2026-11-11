package trend

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func days(start string, closes ...float64) []Day {
	t0, _ := time.Parse("2006-01-02", start)
	out := make([]Day, len(closes))
	for i, c := range closes {
		out[i] = Day{Date: t0.AddDate(0, 0, i).Format("2006-01-02"), Close: c}
	}
	return out
}

func rising(n int, from, step float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = from + step*float64(i)
	}
	return out
}

func TestTrendFilterEntersOnlyAboveSMAAndChargesCosts(t *testing.T) {
	st := NewState("2026-11-11")
	st.Advance(days("2026-01-01", rising(60, 100, 1)...)) // strictly rising: above SMA50 once 50 closes exist
	t1, t2, bh := st.Ledgers["T1_spot_1x"], st.Ledgers["T2_futures_2x"], st.Ledgers["BH"]
	if t1.Pos != 1 || t2.Pos != 2 || bh.Pos != 1 {
		t.Fatalf("positions t1=%v t2=%v bh=%v", t1.Pos, t2.Pos, bh.Pos)
	}
	for i := 0; i < 48; i++ { // before 50 closes the filter is flat
		if t1.Days[i].PosOut != 0 {
			t.Fatalf("day %d entered before SMA warm-up", i)
		}
	}
	entry := t1.Days[48] // 50th close: first decision with a full SMA
	if entry.PosOut != 1 || math.Abs(entry.Ret-(-3e-4)) > 1e-12 {
		t.Fatalf("entry day = %+v (want pos 1, ret = -3 bps entry cost)", entry)
	}
	d := t2.Days[49] // held 2x through a +1/149 day (149 -> 150), funding 2.4 bps x 2
	want := 2*(1.0/149) - 2*0.1*24*1e-4
	if math.Abs(d.Ret-want) > 1e-12 {
		t.Fatalf("T2 held-day ret = %v want %v", d.Ret, want)
	}
}

func TestAdvanceIsIdempotentAndCatchesUp(t *testing.T) {
	all := days("2026-01-01", rising(70, 100, 0.5)...)
	a := NewState("x")
	a.Advance(all[:40])
	a.Advance(all[:55]) // overlap must not double count
	a.Advance(all)
	b := NewState("x")
	b.Advance(all)
	if a.Ledgers["T1_spot_1x"].Equity != b.Ledgers["T1_spot_1x"].Equity || len(a.Ledgers["BH"].Days) != 69 {
		t.Fatalf("catch-up mismatch: %v vs %v, days %d", a.Ledgers["T1_spot_1x"].Equity,
			b.Ledgers["T1_spot_1x"].Equity, len(a.Ledgers["BH"].Days))
	}
}

func TestKillRuleStopsT2(t *testing.T) {
	c := append(rising(60, 100, 1), 110, 100, 90) // -31% day while long 2x: drawdown > 50%
	st := NewState("x")
	st.Advance(days("2026-01-01", c...))
	t2 := st.Ledgers["T2_futures_2x"]
	if !t2.Killed || t2.Pos != 0 || t2.MaxDD < 0.5 {
		t.Fatalf("T2 not killed: %+v", *t2)
	}
}

func TestScoreBeatRateExcludesPartialMonths(t *testing.T) {
	st := NewState("2026-01-10") // registration month (Jan) and the current month (Mar) are excluded
	st.Advance(days("2025-11-01", rising(130, 100, 1)...))
	got := map[string]Summary{}
	for _, s := range st.Score(time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)) {
		got[s.Name] = s
	}
	if got["T1_spot_1x"].Months != 1 || got["T2_futures_2x"].Months != 1 {
		t.Fatalf("months: %+v", got)
	}
	if got["T1_spot_1x"].Beats != 0 { // 1x held all Feb = BH exactly: not a strict beat
		t.Fatalf("T1 beats = %d", got["T1_spot_1x"].Beats)
	}
	if got["T2_futures_2x"].Beats != 1 { // 2x in a rising month beats BH after funding
		t.Fatalf("T2 beats = %d", got["T2_futures_2x"].Beats)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	st := NewState("2026-11-11")
	st.Advance(days("2026-01-01", rising(55, 100, 1)...))
	if err := st.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || got == nil || got.LastDate != st.LastDate || got.Ledgers["T1_spot_1x"].Equity != st.Ledgers["T1_spot_1x"].Equity {
		t.Fatalf("round trip failed: %v %+v", err, got)
	}
	if missing, err := Load(filepath.Join(t.TempDir(), "none.json")); missing != nil || err != nil {
		t.Fatal("missing file should be (nil, nil)")
	}
}

func TestSeedDoesNotBacktest(t *testing.T) {
	st := NewState("2026-03-01")
	all := days("2026-01-01", rising(70, 100, 1)...)
	st.Seed(all[:60]) // history before registration
	if len(st.Ledgers["BH"].Days) != 0 || st.Ledgers["T1_spot_1x"].Equity != 1 {
		t.Fatal("seed must not create ledger entries")
	}
	st.Advance(all) // only the 10 days after the seed count
	if n := len(st.Ledgers["BH"].Days); n != 10 {
		t.Fatalf("forward days = %d, want 10", n)
	}
	if st.Ledgers["T1_spot_1x"].Days[0].PosOut != 1 { // SMA is warm from the seed
		t.Fatal("seeded SMA should allow entry on the first forward day")
	}
}
