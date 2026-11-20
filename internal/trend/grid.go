package trend

import (
	"math"
	"sort"
	"time"
)

// PREREG30 E4: P1's rule decided on four clocks (daily closes at 00, 06, 12 and 18 UTC), a quarter of each sleeve
// per clock, stepped on 6-hour bars. Mirrors research/quant_lab/strategies30.py (clock_held, split_held,
// grid_returns). One difference: a clock's daily close is taken as the close of the 6-hour bar ending at that
// hour, and a tranche is flat when any of its 50 closes is missing; the backtest instead dropped days with fewer
// than four 6-hour bars.

const (
	gridSec = 6 * 3600
	gridCap = 400 // 50 daily closes per clock need about 200 six-hour bars
)

// Clocks are the UTC hours at which a tranche decides.
var Clocks = []int{0, 6, 12, 18}

// Bar is one completed 6-hour candle, labelled by its start.
type Bar struct {
	T     int64   `json:"t"` // start, unix seconds
	Close float64 `json:"close"`
}

func mergeBars(h, bars []Bar) []Bar {
	seen := make(map[int64]bool, len(h))
	for _, b := range h {
		seen[b.T] = true
	}
	for _, b := range bars {
		if b.Close > 0 && b.T%gridSec == 0 && !seen[b.T] {
			seen[b.T] = true
			h = append(h, b)
		}
	}
	sort.Slice(h, func(i, j int) bool { return h[i].T < h[j].T })
	if len(h) > gridCap {
		h = h[len(h)-gridCap:]
	}
	return h
}

// clockTarget is the position held from the close of the last bar in h: the mean over the clocks of lev when that
// clock's latest daily close is above the mean of its last n daily closes, else 0.
func clockTarget(h []Bar, lev float64, n int) float64 {
	if len(h) == 0 || n <= 0 {
		return 0
	}
	px := make(map[int64]float64, len(h))
	for _, b := range h {
		px[b.T] = b.Close
	}
	tc := h[len(h)-1].T + gridSec // the close being decided on
	hour := int(tc % 86400 / 3600)
	sum := 0.0
	for _, k := range Clocks {
		c := tc - int64((hour-k+24)%24)*3600 // this clock's latest close at or before tc
		last, tot, ok := px[c-gridSec], 0.0, true
		for j := 0; j < n; j++ {
			p, found := px[c-gridSec-int64(j)*86400]
			if !found {
				ok = false
				break
			}
			tot += p
		}
		if ok && last > tot/float64(n) {
			sum += lev
		}
	}
	return sum / float64(len(Clocks))
}

func barIndex(h []Bar, t int64) int {
	i := sort.Search(len(h), func(i int) bool { return h[i].T >= t })
	if i < len(h) && h[i].T == t {
		return i
	}
	return -1
}

// AdvanceGrid stores completed 6-hour bars and steps the four-clock variants over every bar newer than GridLast
// that both coins have. The first call only seeds history, so a new ledger starts flat and pays its entry cost on
// its first step. Idempotent: bars at or before GridLast are never applied twice.
func (st *State) AdvanceGrid(btc, eth []Bar) int {
	st.GridB, st.GridE = mergeBars(st.GridB, btc), mergeBars(st.GridE, eth)
	if st.GridLast == 0 {
		for i := len(st.GridB) - 1; i >= 0; i-- {
			if barIndex(st.GridE, st.GridB[i].T) >= 0 {
				st.GridLast = st.GridB[i].T
				break
			}
		}
		return 0
	}
	n := 0
	for bi, b := range st.GridB {
		ei := barIndex(st.GridE, b.T)
		if b.T <= st.GridLast || bi < 1 || ei < 1 {
			continue
		}
		hours := float64(b.T-st.GridB[bi-1].T) / 3600
		barB := b.Close/st.GridB[bi-1].Close - 1
		barE := st.GridE[ei].Close/st.GridE[ei-1].Close - 1
		date := time.Unix(b.T, 0).UTC().Format("2006-01-02T15")
		for _, name := range sortedKeys(st.Ledgers) {
			if l := st.Ledgers[name]; l.Variant.Clocks {
				l.stepGrid(date, hours, barB, barE, st.GridB[:bi+1], st.GridE[:ei+1])
			}
		}
		st.GridLast = b.T
		n++
	}
	return n
}

func (l *Ledger) stepGrid(date string, hours, barB, barE float64, histB, histE []Bar) {
	v := l.Variant
	c := (v.FeeBps + v.SlipBps) * 1e-4
	f := v.FundingBpsH * hours * 1e-4
	tb, te := clockTarget(histB, v.Lev, v.SMA), clockTarget(histE, v.Lev, v.SMA)
	ret := 0.5*(l.Pos*barB-math.Abs(l.Pos)*f) + 0.5*(l.PosAlt*barE-math.Abs(l.PosAlt)*f) -
		0.5*math.Abs(tb-l.Pos)*c - 0.5*math.Abs(te-l.PosAlt)*c
	l.Equity *= 1 + ret
	if l.Equity > l.Peak {
		l.Peak = l.Equity
	}
	if dd := 1 - l.Equity/l.Peak; dd > l.MaxDD {
		l.MaxDD = dd
	}
	l.Pos, l.PosAlt = tb, te
	l.Days = append(l.Days, DayRec{Date: date, Ret: ret, PosOut: 0.5*tb + 0.5*te, Equity: l.Equity,
		PosB: tb, PosE: te, CloseB: histB[len(histB)-1].Close, CloseE: histE[len(histE)-1].Close})
}
