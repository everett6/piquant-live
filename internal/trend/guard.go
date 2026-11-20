package trend

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Risk guards added 2026-11-18 at the owner's request. They protect the paper ledgers (and any future live book) from
// the two ways a leveraged crypto account is wiped out, without changing any strategy's entry or exit rule.
//
// 1. Margin safety: no sleeve may hold more leverage than survives the worst daily close on record. ETH fell 44.6% in
//    one day (docs/PHASE1_TAIL_RISK_2026-11.md). An isolated futures position at leverage L is liquidated after a move
//    of about 1/L minus the maintenance margin, so 1/L must exceed 44.6% plus a 5-point buffer: L <= 2.0. Only the
//    PREREG44 1x-2.5x ledgers ever asked for more.
// 2. Intraday emergency stop: the daily rules only look at the close. A crash in the middle of the day is checked
//    every minute against each ledger's peak; at a 30% drawdown the ledger is flattened at the current price and the
//    normal rule decides again at the next close. After a stop it fires again only on a further 10-point loss, so a
//    ledger that re-enters while still 30% under its peak is not churned out every minute.

const (
	WorstDailyMove = 0.446 // ETH's worst daily close on record
	MarginBuffer   = 0.05
	EmergencyDD    = 0.30
	EmergencyStep  = 0.10
)

// MaxSafeLev is the most leverage per sleeve that survives WorstDailyMove plus MarginBuffer, rounded down to 0.1.
var MaxSafeLev = math.Floor(10/(WorstDailyMove+MarginBuffer)) / 10

// capMargin limits one sleeve's target leverage to MaxSafeLev and counts the times it bites.
func (l *Ledger) capMargin(target float64) float64 {
	if target > MaxSafeLev {
		l.MarginCaps++
		return MaxSafeLev
	}
	return target
}

// EmergencyCheck marks every ledger with a position to the given prices (BTC, ETH) against the closes of its last
// record. A ledger at or beyond its emergency level is flattened at those prices, paying its exit cost, and gets a
// record dated "<UTC time>-emergency". It returns one line per ledger stopped. Ledgers whose last record has no
// closes (older state) are skipped.
func (st *State) EmergencyCheck(pxB, pxE float64, now time.Time) []string {
	var fired []string
	for _, name := range sortedKeys(st.Ledgers) {
		l := st.Ledgers[name]
		if len(l.Days) == 0 || (l.Pos == 0 && l.PosAlt == 0) || l.Killed || pxB <= 0 {
			continue
		}
		last := l.Days[len(l.Days)-1]
		if last.CloseB <= 0 || strings.HasSuffix(last.Date, "-emergency") {
			continue
		}
		pair := l.Variant.Pair
		move := l.Pos * (pxB/last.CloseB - 1)
		if pair {
			if last.CloseE <= 0 || pxE <= 0 {
				continue
			}
			move = 0.5*l.Pos*(pxB/last.CloseB-1) + 0.5*l.PosAlt*(pxE/last.CloseE-1)
		}
		eq := l.Equity * (1 + move)
		peak := math.Max(l.Peak, eq)
		dd := 1 - eq/peak
		level := l.EmergencyArm
		if level == 0 || l.Equity >= l.Peak {
			level = EmergencyDD // re-armed at the base level once the ledger is back at a peak
		}
		if dd < level {
			continue
		}
		c := (l.Variant.FeeBps + l.Variant.SlipBps) * 1e-4
		exit := math.Abs(l.Pos) * c
		if pair {
			exit = 0.5*math.Abs(l.Pos)*c + 0.5*math.Abs(l.PosAlt)*c
		}
		ret := (1+move)*(1-exit) - 1
		l.Equity *= 1 + ret
		if d := 1 - l.Equity/l.Peak; d > l.MaxDD {
			l.MaxDD = d
		}
		l.Pos, l.PosAlt = 0, 0
		l.EmergencyArm = dd + EmergencyStep
		l.EmergencyStops++
		rec := DayRec{Date: now.UTC().Format("2006-01-02T15:04") + "-emergency", Ret: ret, Equity: l.Equity, CloseB: pxB}
		if pair {
			rec.CloseE = pxE
		}
		l.Days = append(l.Days, rec)
		fired = append(fired, fmt.Sprintf("%s: drawdown %.1f%% >= %.0f%%, flattened at BTC %.2f ETH %.2f (equity %.4f)",
			name, dd*100, level*100, pxB, pxE, l.Equity))
	}
	return fired
}
