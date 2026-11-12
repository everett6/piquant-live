package trend

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

func near(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s = %.6f, want %.6f", what, got, want)
	}
}

func TestTaxRowsSpotRoundTrip(t *testing.T) {
	l := &Ledger{Variant: Variant{Name: "S", Lev: 1, FeeBps: 10, SMA: 50}, Days: []DayRec{
		{Date: "2026-01-01", PosB: 1, CloseB: 100, Equity: 1},
		{Date: "2026-01-02", PosB: 1, CloseB: 110, Equity: 1.1, Ret: 0.1},
		{Date: "2026-01-03", PosB: 0, CloseB: 120, Equity: 1.2, Ret: 0.09},
	}}
	rows, open, skipped := l.TaxRows(1000)
	if len(rows) != 1 || len(open) != 0 || skipped != 0 {
		t.Fatalf("rows %d open %d skipped %d", len(rows), len(open), skipped)
	}
	r := rows[0]
	if r.Form != Form8949 || r.Term != "SHORT" || r.Opened != "2026-01-01" || r.Closed != "2026-01-03" {
		t.Fatalf("row %+v", r)
	}
	near(t, "qty", r.Quantity, 10)
	near(t, "cost", r.CostUSD, 1000)
	near(t, "proceeds", r.ProceedsUSD, 1200)
	near(t, "fees", r.FeesUSD, 1+1.2)
	near(t, "gain", r.GainUSD, 200-2.2)
}

func TestTaxRowsFuturesYearEndMark(t *testing.T) {
	l := &Ledger{Variant: Variant{Name: "F", Lev: 2, FundingBpsH: 1, SMA: 50, Pair: true}, Days: []DayRec{
		{Date: "2025-12-30", PosB: 2, CloseB: 100, CloseE: 10, Equity: 1},
		{Date: "2025-12-31", PosB: 2, CloseB: 105, CloseE: 10, Equity: 1.1},
		{Date: "2026-01-01", PosB: 0, CloseB: 110, CloseE: 10, Equity: 1.2},
	}}
	st := &State{Ledgers: map[string]*Ledger{"F": l}}
	all, open, _ := st.TaxRows(100, 0)
	if len(all) != 2 || len(open) != 0 {
		t.Fatalf("rows %+v open %+v", all, open)
	}
	m, s := all[0], all[1]
	if m.Form != Form6781 || m.Term != "60/40" || m.Closed != "2025-12-31" || m.Note == "" {
		t.Fatalf("mark row %+v", m)
	}
	near(t, "mark qty", m.Quantity, 1) // 2x * half sleeve * $100 = $100 at $100
	near(t, "mark funding", m.FundingUSD, 2*0.5*0.0024*1*100)
	near(t, "mark gain", m.GainUSD, 5-0.24)
	if s.Opened != "2025-12-31" || s.Closed != "2026-01-01" {
		t.Fatalf("sale row %+v", s)
	}
	near(t, "sale basis", s.CostUSD, 105)
	near(t, "sale gain", s.GainUSD, 5-2*0.5*0.0024*1.1*100)
	y25, _, _ := st.TaxRows(100, 2025)
	if len(y25) != 1 || y25[0].Closed != "2025-12-31" {
		t.Fatalf("2025 filter %+v", y25)
	}
	tot := Totals(all)
	near(t, "1256 net", tot.Net1256, m.GainUSD+s.GainUSD)
	var buf bytes.Buffer
	if err := WriteTaxCSV(&buf, all); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(buf.String(), "\n"); lines != 3 {
		t.Fatalf("csv lines %d", lines)
	}
}

func TestTaxRowsOpenAndOldRecords(t *testing.T) {
	l := &Ledger{Variant: Variant{Name: "O", Lev: 1, SMA: 50, Capital: 50}, Days: []DayRec{
		{Date: "2026-11-01", PosOut: 1, Equity: 1}, // before per-sleeve fields existed
		{Date: "2026-11-02", PosB: 1, CloseB: 25, Equity: 1},
	}}
	rows, open, skipped := l.TaxRows(1000)
	if len(rows) != 0 || skipped != 1 || len(open) != 1 {
		t.Fatalf("rows %d open %d skipped %d", len(rows), len(open), skipped)
	}
	near(t, "open qty uses variant capital", open[0].Quantity, 2)
}

func TestTaxRowsFromRealSteps(t *testing.T) {
	// The ledger's own step() must fill the per-sleeve fields the export reads.
	l := &Ledger{Variant: Variant{Name: "BH", Lev: 1, SMA: 0}, Equity: 1, Peak: 1}
	hist := []Day{{Date: "2026-01-01", Close: 100}}
	l.step("2026-01-01", 0, hist)
	hist = append(hist, Day{Date: "2026-01-02", Close: 110})
	l.step("2026-01-02", 0.1, hist)
	if d := l.Days[1]; d.PosB != 1 || d.CloseB != 110 {
		t.Fatalf("day %+v", d)
	}
	_, open, _ := l.TaxRows(100)
	if len(open) != 1 {
		t.Fatalf("open %+v", open)
	}
	near(t, "BH qty", open[0].Quantity, 1)
}
