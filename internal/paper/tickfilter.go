package paper

import (
	"math"
	"time"
)

// Bad-print guard. A single impossible tick (e.g. Coinbase's BTC bar at $0.06 on 2017-04-15) would otherwise
// become the mark price, fake a huge drawdown and trip the kill switch at a price that never traded.
const (
	tickJumpMax     = 0.20            // ticks further than this from the last good price are held back
	tickConfirmBand = 0.05            // ...until this many agreeing ticks confirm the new level
	tickConfirmN    = 3               //
	tickStaleAfter  = 5 * time.Minute // after a gap this long, the next tick is accepted as is
)

type tickFilter struct {
	last   float64
	lastAt time.Time
	pend   float64
	pendN  int
}

// accept reports whether p is a usable price, updating the filter.
func (f *tickFilter) accept(p float64, now time.Time) bool {
	if f.last <= 0 || now.Sub(f.lastAt) > tickStaleAfter || math.Abs(p/f.last-1) <= tickJumpMax {
		f.last, f.lastAt, f.pendN = p, now, 0
		return true
	}
	if f.pendN > 0 && math.Abs(p/f.pend-1) <= tickConfirmBand {
		f.pendN++
	} else {
		f.pend, f.pendN = p, 1
	}
	if f.pendN >= tickConfirmN {
		f.last, f.lastAt, f.pendN = p, now, 0
		return true
	}
	return false
}

// tickOKLocked applies the per-symbol filter. Caller MUST hold e.mu.
func (e *DryRunEngine) tickOKLocked(symbol string, price float64, now time.Time) bool {
	if e.tickFilters == nil {
		e.tickFilters = make(map[string]*tickFilter, 4)
	}
	f := e.tickFilters[symbol]
	if f == nil {
		f = &tickFilter{}
		e.tickFilters[symbol] = f
	}
	if f.accept(price, now) {
		return true
	}
	e.state.RejectedTicks++
	return false
}
