package trend

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestMaxSafeLevSurvivesTheWorstDayOnRecord(t *testing.T) {
	if MaxSafeLev != 2.0 {
		t.Fatalf("MaxSafeLev = %v, want 2.0", MaxSafeLev)
	}
	if 1/MaxSafeLev < WorstDailyMove+MarginBuffer-1e-12 {
		t.Fatal("a sleeve at MaxSafeLev would be liquidated by the worst day plus buffer")
	}
	l := &Ledger{}
	if l.capMargin(2.5) != 2.0 || l.capMargin(1.7) != 1.7 || l.MarginCaps != 1 {
		t.Fatalf("capMargin wrong, caps %d", l.MarginCaps)
	}
}

func TestPairStepNeverHoldsMoreThanTheSafeLeverage(t *testing.T) {
	var hist []Day
	p := 100.0
	for i := 0; i < 120; i++ {
		if i%2 == 0 { // calm rise, about 16% annual volatility: the 1x-2.5x rule asks for the full 2.5x
			p *= 1.013
		} else {
			p *= 0.993
		}
		hist = append(hist, Day{Date: "d", Close: p})
	}
	v := Variant{Name: "x", Lev: 2.5, SMA: 50, Pair: true, Coherence: true, VolTarget: 0.80}
	l := &Ledger{Variant: v, Equity: 1, Peak: 1}
	l.stepPair("2026-01-01", 0, 0, hist, hist)
	if l.Pos > MaxSafeLev+1e-12 || l.PosAlt > MaxSafeLev+1e-12 || l.MarginCaps == 0 {
		t.Fatalf("pos %v / %v, caps %d", l.Pos, l.PosAlt, l.MarginCaps)
	}
}

func pairLedger(pos float64) *Ledger {
	return &Ledger{Variant: Variant{Name: "p", Lev: 2, FeeBps: 6, SlipBps: 1, Pair: true}, Equity: 1, Peak: 1,
		Pos: pos, PosAlt: pos, Days: []DayRec{{Date: "2026-11-18", Equity: 1, CloseB: 100, CloseE: 10}}}
}

func TestEmergencyStopFiresAtThirtyPercentAndBooksTheMove(t *testing.T) {
	st := &State{Ledgers: map[string]*Ledger{"p": pairLedger(2)}}
	now := time.Date(2026, 11, 19, 14, 5, 0, 0, time.UTC)
	if f := st.EmergencyCheck(86, 8.6, now); len(f) != 0 { // -14% x 2 = -28%: not yet
		t.Fatalf("fired early: %v", f)
	}
	f := st.EmergencyCheck(84, 8.4, now) // -16% x 2 = -32%
	l := st.Ledgers["p"]
	want := (1 - 0.32) * (1 - 2*7e-4)
	if len(f) != 1 || math.Abs(l.Equity-want) > 1e-12 || l.Pos != 0 || l.PosAlt != 0 || l.EmergencyStops != 1 {
		t.Fatalf("fired %v equity %v pos %v/%v", f, l.Equity, l.Pos, l.PosAlt)
	}
	last := l.Days[len(l.Days)-1]
	if last.Date != "2026-11-19T14:05-emergency" || last.CloseB != 84 || last.CloseE != 8.4 {
		t.Fatalf("record %+v", last)
	}
	if math.Abs(l.EmergencyArm-(0.32+EmergencyStep)) > 1e-12 {
		t.Fatalf("re-arm level %v", l.EmergencyArm)
	}
	if f := st.EmergencyCheck(50, 5, now); len(f) != 0 { // flat now: nothing to stop
		t.Fatal("a flat ledger was stopped")
	}
}

func TestEmergencyReArmsOnlyAfterAFurtherLossOrANewPeak(t *testing.T) {
	l := pairLedger(1)
	l.Peak, l.Equity, l.EmergencyArm = 1, 0.65, 0.45 // stopped earlier at 35%, re-entered at 1x
	st := &State{Ledgers: map[string]*Ledger{"p": l}}
	now := time.Now()
	if f := st.EmergencyCheck(90, 9, now); len(f) != 0 { // -10%: 41.5% below peak, under the 45% re-arm level
		t.Fatalf("churned: %v", f)
	}
	if f := st.EmergencyCheck(80, 8, now); len(f) != 1 { // -20%: 48% below peak
		t.Fatal("did not fire past the re-arm level")
	}
	m := pairLedger(1)
	m.EmergencyArm = 0.45 // back at a peak (equity == peak): the base 30% applies again
	st2 := &State{Ledgers: map[string]*Ledger{"m": m}}
	if f := st2.EmergencyCheck(69, 6.9, now); len(f) != 1 {
		t.Fatal("base level not restored at a new peak")
	}
}

func TestEmergencySkipsSingleLedgersCorrectlyAndOldRecords(t *testing.T) {
	single := &Ledger{Variant: Variant{Name: "s", Lev: 2, FeeBps: 6, SlipBps: 1}, Equity: 1, Peak: 1, Pos: 2,
		Days: []DayRec{{Date: "2026-11-18", CloseB: 100}}}
	old := pairLedger(2)
	old.Days[0].CloseB = 0 // record from before closes were stored
	st := &State{Ledgers: map[string]*Ledger{"s": single, "o": old}}
	if f := st.EmergencyCheck(84, 1, time.Now()); len(f) != 1 || !strings.HasPrefix(f[0], "s:") || old.Pos != 2 {
		t.Fatalf("fired %v, old pos %v", f, old.Pos)
	}
}

func TestEmergencyResetArmOnNewPeak(t *testing.T) {
	l := pairLedger(1)
	l.Peak, l.Equity, l.EmergencyArm = 1.0, 1.0, 0.45
	st := &State{Ledgers: map[string]*Ledger{"p": l}}
	now := time.Date(2026, 11, 19, 14, 5, 0, 0, time.UTC)

	// Small intraday move while at peak: does not fire, but resets EmergencyArm to 0.
	if f := st.EmergencyCheck(95, 9.5, now); len(f) != 0 {
		t.Fatalf("unexpected fire: %v", f)
	}
	if l.EmergencyArm != 0 {
		t.Fatalf("EmergencyArm = %v, want 0 after equity >= peak", l.EmergencyArm)
	}

	// Next close leaves equity slightly below peak; a 31% drawdown must fire at EmergencyDD (30%), not 45%.
	l.Equity = 0.99
	if f := st.EmergencyCheck(69, 6.9, now); len(f) != 1 {
		t.Fatalf("expected emergency stop at 30%% threshold, fired=%v", f)
	}
}
