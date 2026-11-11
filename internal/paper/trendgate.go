package paper

import (
	"sync"
	"time"
)

// TrendGate restricts new medium-frequency entries to the direction of the daily trend filter (PREREG6:
// long when the last completed UTC daily BTC close is above its SMA50, flat otherwise). Exits are never gated.
// The regime is pushed in by the caller (cmd wires a Coinbase daily-candle poller), so this package does not
// depend on the network code.
type TrendGate struct {
	mu      sync.RWMutex
	up      bool
	known   bool
	updated time.Time
	close   float64
	sma     float64
	maxAge  time.Duration
}

// TrendGateState is the dashboard/checkpoint view of the gate.
type TrendGateState struct {
	Regime    string    `json:"regime"` // UP, DOWN or UNKNOWN
	Close     float64   `json:"last_daily_close"`
	SMA       float64   `json:"sma50"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewTrendGate returns a gate that treats a regime older than maxAge as unknown (no new entries).
func NewTrendGate(maxAge time.Duration) *TrendGate {
	if maxAge <= 0 {
		maxAge = 48 * time.Hour
	}
	return &TrendGate{maxAge: maxAge}
}

// Set records the latest regime.
func (g *TrendGate) Set(up bool, close, sma float64, at time.Time) {
	g.mu.Lock()
	g.up, g.known, g.close, g.sma, g.updated = up, true, close, sma, at
	g.mu.Unlock()
}

func (g *TrendGate) fresh(now time.Time) bool {
	return g.known && now.Sub(g.updated) <= g.maxAge
}

// AllowLongEntry reports whether opening or adding to a long is allowed: only in a fresh UP regime.
func (g *TrendGate) AllowLongEntry(now time.Time) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.fresh(now) && g.up
}

// AllowShortEntry is always false: the tested trend filter is long-or-flat.
func (g *TrendGate) AllowShortEntry(time.Time) bool { return false }

// Snapshot returns the gate state for checkpoints and the dashboard.
func (g *TrendGate) Snapshot(now time.Time) TrendGateState {
	g.mu.RLock()
	defer g.mu.RUnlock()
	s := TrendGateState{Regime: "UNKNOWN", Close: g.close, SMA: g.sma, UpdatedAt: g.updated}
	if g.fresh(now) {
		s.Regime = "DOWN"
		if g.up {
			s.Regime = "UP"
		}
	}
	return s
}
