package trend

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"time"
)

// Tax forms for the trend ledgers. Futures variants (FundingBpsH > 0, modelled on US-regulated futures) are
// treated as Section 1256 contracts: gains go on Form 6781, split 60% long-term / 40% short-term, and any
// position open at a year end is marked to market at that year's last close. Spot variants go on Form 8949 with
// FIFO lots. This is a paper record to hand to a tax professional, not tax advice.
const (
	Form8949 = "8949"
	Form6781 = "6781 (Sec. 1256)"
)

// TaxRow is one closed position (or a Section 1256 year-end mark) of one sleeve.
type TaxRow struct {
	Variant     string
	Coin        string
	Form        string
	Opened      string // YYYY-MM-DD (for a 1256 row after a year-end mark: the mark date)
	Closed      string
	Quantity    float64
	CostUSD     float64
	ProceedsUSD float64
	FeesUSD     float64 // entry and exit fees and slippage on the closed quantity
	FundingUSD  float64 // futures funding paid while held
	GainUSD     float64 // proceeds - cost - fees - funding
	Term        string  // SHORT / LONG (8949) or 60/40 (1256)
	Note        string
}

// OpenLot is a position still open after the last ledger day (not taxable yet, except 1256 at year end).
type OpenLot struct {
	Variant, Coin, Opened string
	Quantity, CostUSD     float64
}

type sleeveLot struct {
	pos, qty, cost, fees, funding float64
	opened                        string
}

// TaxRows walks the ledger's days and returns its closed positions. capital is the dollar size of the book at
// equity 1 (the variant's own Capital wins when set). Fills happen at each day's close when a sleeve's position
// changes; the size bought is the sleeve's share of equity at that close. Days recorded before the per-sleeve
// fields existed are skipped (reported in skipped).
func (l *Ledger) TaxRows(capital float64) (rows []TaxRow, open []OpenLot, skipped int) {
	v := l.Variant
	if v.Capital > 0 {
		capital = v.Capital
	}
	futures := v.FundingBpsH > 0
	form, w := Form8949, 1.0
	if futures {
		form = Form6781
	}
	coins := []string{"BTC"}
	if v.Pair {
		coins, w = []string{"BTC", "ETH"}, 0.5
	}
	cost := (v.FeeBps + v.SlipBps) * 1e-4
	fund := v.FundingBpsH * 24 * 1e-4
	lots := make([]sleeveLot, len(coins))
	var lastDate string
	lastClose := make([]float64, len(coins))
	eqPrev := 1.0
	if len(l.Days) > 0 {
		eqPrev = l.Days[0].Equity / (1 + l.Days[0].Ret)
	}

	closeLot := func(k int, frac, px float64, date, note string) {
		lt := &lots[k]
		q := lt.qty * frac
		r := TaxRow{Variant: v.Name, Coin: coins[k], Form: form, Opened: lt.opened, Closed: date, Quantity: q,
			CostUSD: lt.cost * frac, ProceedsUSD: q * px, Note: note}
		exitFee := 0.0
		if note == "" { // a real sale pays fees; a year-end mark does not
			exitFee = q * px * cost
		}
		r.FeesUSD = lt.fees*frac + exitFee
		r.FundingUSD = lt.funding * frac
		r.GainUSD = r.ProceedsUSD - r.CostUSD - r.FeesUSD - r.FundingUSD
		switch {
		case futures:
			r.Term = "60/40"
		case heldOverYear(lt.opened, date):
			r.Term = "LONG"
		default:
			r.Term = "SHORT"
		}
		rows = append(rows, r)
		lt.qty -= q
		lt.cost -= r.CostUSD
		lt.fees -= lt.fees * frac
		lt.funding -= r.FundingUSD
		if lt.qty <= 1e-15 {
			*lt = sleeveLot{}
		}
	}

	for _, d := range l.Days {
		pos := []float64{d.PosB, d.PosE}
		px := []float64{d.CloseB, d.CloseE}
		if px[0] <= 0 || (v.Pair && px[1] <= 0) {
			skipped++
			eqPrev = d.Equity
			continue
		}
		if futures && lastDate != "" && d.Date[:4] != lastDate[:4] {
			// Section 1256: mark each open position to market at the year's last close; the marked value is
			// the new basis for the rest of the holding.
			for k := range coins {
				if lt := lots[k]; lt.qty > 0 {
					closeLot(k, 1, lastClose[k], lastDate, "year-end mark to market")
					lots[k] = sleeveLot{pos: lt.pos, qty: lt.qty, cost: lt.qty * lastClose[k], opened: lastDate}
				}
			}
		}
		for k := range coins {
			lt := &lots[k]
			if lt.qty > 0 {
				lt.funding += lt.pos * w * fund * eqPrev * capital
			}
			target := pos[k]
			if target < 0 {
				target = 0 // trend variants are long or flat
			}
			switch {
			case target < lt.pos && lt.qty > 0:
				closeLot(k, (lt.pos-target)/lt.pos, px[k], d.Date, "")
				lt.pos = target
				if target == 0 {
					*lt = sleeveLot{}
				}
			case target > lt.pos:
				notional := (target - lt.pos) * w * d.Equity * capital
				if lt.qty == 0 {
					lt.opened = d.Date
				}
				lt.qty += notional / px[k]
				lt.cost += notional
				lt.fees += notional * cost
				lt.pos = target
			}
			lastClose[k] = px[k]
		}
		lastDate = d.Date
		eqPrev = d.Equity
	}
	for k, lt := range lots {
		if lt.qty > 0 {
			open = append(open, OpenLot{Variant: v.Name, Coin: coins[k], Opened: lt.opened, Quantity: lt.qty,
				CostUSD: lt.cost})
		}
	}
	return rows, open, skipped
}

func heldOverYear(opened, closed string) bool {
	a, err1 := time.Parse("2006-01-02", opened)
	b, err2 := time.Parse("2006-01-02", closed)
	return err1 == nil && err2 == nil && b.After(a.AddDate(1, 0, 0))
}

// TaxRows returns every ledger's rows closed in year (0 = all years), sorted by close date then variant.
func (st *State) TaxRows(capital float64, year int) (rows []TaxRow, open []OpenLot, skipped int) {
	for _, name := range sortedKeys(st.Ledgers) {
		r, o, s := st.Ledgers[name].TaxRows(capital)
		open = append(open, o...)
		skipped += s
		for _, x := range r {
			if year == 0 || x.Closed[:4] == strconv.Itoa(year) {
				rows = append(rows, x)
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Closed != rows[j].Closed {
			return rows[i].Closed < rows[j].Closed
		}
		return rows[i].Variant < rows[j].Variant
	})
	return rows, open, skipped
}

// WriteTaxCSV writes the rows with a header.
func WriteTaxCSV(w io.Writer, rows []TaxRow) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"variant", "coin", "form", "date_opened", "date_closed", "quantity", "cost_basis_usd",
		"proceeds_usd", "fees_usd", "funding_usd", "gain_usd", "term", "note"})
	f2 := func(x float64) string { return strconv.FormatFloat(round(x, 2), 'f', 2, 64) }
	for _, r := range rows {
		_ = cw.Write([]string{r.Variant, r.Coin, r.Form, r.Opened, r.Closed,
			strconv.FormatFloat(r.Quantity, 'f', 8, 64), f2(r.CostUSD), f2(r.ProceedsUSD), f2(r.FeesUSD),
			f2(r.FundingUSD), f2(r.GainUSD), r.Term, r.Note})
	}
	cw.Flush()
	return cw.Error()
}

// TaxTotals sums gains by form and term: 8949 SHORT and LONG; 6781 split 60% long / 40% short.
type TaxTotals struct {
	Rows                         int
	Short8949, Long8949, Net1256 float64
}

func (t TaxTotals) String() string {
	return fmt.Sprintf("%d rows | Form 8949 short-term %+.2f, long-term %+.2f | Form 6781 net %+.2f "+
		"(60%% long %+.2f, 40%% short %+.2f)", t.Rows, t.Short8949, t.Long8949, t.Net1256, 0.6*t.Net1256,
		0.4*t.Net1256)
}

// Totals adds up rows.
func Totals(rows []TaxRow) TaxTotals {
	t := TaxTotals{Rows: len(rows)}
	for _, r := range rows {
		switch {
		case r.Form == Form6781:
			t.Net1256 += r.GainUSD
		case r.Term == "LONG":
			t.Long8949 += r.GainUSD
		default:
			t.Short8949 += r.GainUSD
		}
	}
	return t
}

func round(x float64, n int) float64 {
	p := math.Pow(10, float64(n))
	return math.Round(x*p) / p
}
