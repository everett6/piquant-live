// Package event defines the fixed-size market data event types that travel
// through the engine hot path.
//
// Every type in this package is a plain value type with no pointers, so an
// event can be copied in and out of a ring buffer slot without touching the
// heap. Prices and quantities are fixed-point int64: floating point is only
// permitted in normalised model inputs (see internal/features).
package event

import (
	"fmt"
	"math"
)

// Kind identifies the payload carried by an Envelope.
type Kind uint8

const (
	KindUnknown Kind = iota
	KindTrade
	KindDepth   // incremental depth update
	KindSnapshot
	KindHeartbeat
)

func (k Kind) String() string {
	switch k {
	case KindTrade:
		return "trade"
	case KindDepth:
		return "depth"
	case KindSnapshot:
		return "snapshot"
	case KindHeartbeat:
		return "heartbeat"
	default:
		return "unknown"
	}
}

// Side is the aggressor side of a trade or the side of a depth update.
type Side uint8

const (
	SideNone Side = iota
	SideBuy
	SideSell
)

func (s Side) String() string {
	switch s {
	case SideBuy:
		return "buy"
	case SideSell:
		return "sell"
	default:
		return "none"
	}
}

const (
	// MaxLevels is the depth maintained per side (L1..L20).
	MaxLevels = 20

	// PriceScale is the number of fixed-point ticks per unit of price, i.e. a
	// price of 1.0 is stored as 1e9. Nine decimals covers every crypto venue
	// tick size in practical use while keeping products well inside int64.
	PriceScale int64 = 1_000_000_000

	// QtyScale is the number of fixed-point units per unit of size (1e8).
	QtyScale int64 = 100_000_000
)

// Level is one price level of a book.
type Level struct {
	Price int64 // fixed point, PriceScale ticks per unit
	Qty   int64 // fixed point, QtyScale units per unit
}

// Valid reports whether the level can be part of a well-formed book.
func (l Level) Valid() bool { return l.Price > 0 && l.Qty >= 0 }

// TradeEvent is a single print. Side is the aggressor.
type TradeEvent struct {
	Price   int64
	Qty     int64
	Side    Side
	TradeID uint64
}

// DepthEvent is an incremental depth update: up to MaxLevels absolute price
// changes on one side. Qty == 0 deletes the level.
type DepthEvent struct {
	Sequence uint64
	Side     Side
	NUpdates uint8
	Updates  [MaxLevels]Level
}

// SnapshotEvent is a full L1..MaxLevels replacement for both sides. Bids are
// expected best-first (descending price), asks best-first (ascending price).
type SnapshotEvent struct {
	Sequence uint64
	NBids    uint8
	NAsks    uint8
	Bids     [MaxLevels]Level
	Asks     [MaxLevels]Level
}

// Envelope is the unit of work that crosses the ingest ring buffer. It is
// ~1 KiB by design: a fixed, predictable copy that never escapes to the heap.
type Envelope struct {
	Sequence uint64 // ingest sequence assigned by the feed handler
	Venue    uint16
	Symbol   uint16
	Kind     Kind
	SendTime int64 // venue timestamp, unix nanoseconds
	RecvTime int64 // local receive timestamp, unix nanoseconds
	Trade    TradeEvent
	Depth    DepthEvent
	Snap     SnapshotEvent
}

// Reset zeroes every payload so the envelope can be reused from a pool. The
// identity fields (Sequence, Venue, Symbol) are left intact by the caller's
// choice; callers that need a blank envelope can assign a zero value.
func (e *Envelope) Reset() {
	e.Kind = KindUnknown
	e.SendTime = 0
	e.RecvTime = 0
	e.Trade = TradeEvent{}
	e.Depth = DepthEvent{}
	e.Snap = SnapshotEvent{}
}

// Latency reports the local receive-to-now delay plus the venue-to-local
// delivery delay. It is intended for diagnostics, not for hot-path decisions.
func (e *Envelope) Latency(now int64) (delivery, age int64) {
	return e.RecvTime - e.SendTime, now - e.RecvTime
}

// Validate performs the cheap structural checks a feed handler relies on
// before a message is admitted to the pipeline.
func (e *Envelope) Validate() error {
	if e.Sequence == 0 {
		return fmt.Errorf("event: zero sequence")
	}
	if e.RecvTime <= 0 {
		return fmt.Errorf("event: invalid recv time %d", e.RecvTime)
	}
	switch e.Kind {
	case KindTrade:
		if e.Trade.Price <= 0 || e.Trade.Qty <= 0 {
			return fmt.Errorf("event: trade with non-positive price/qty (%d/%d)",
				e.Trade.Price, e.Trade.Qty)
		}
		if e.Trade.Side != SideBuy && e.Trade.Side != SideSell {
			return fmt.Errorf("event: trade with invalid side %d", e.Trade.Side)
		}
	case KindDepth:
		if e.Depth.NUpdates == 0 || e.Depth.NUpdates > MaxLevels {
			return fmt.Errorf("event: depth update count %d out of range", e.Depth.NUpdates)
		}
		if e.Depth.Side != SideBuy && e.Depth.Side != SideSell {
			return fmt.Errorf("event: depth update with invalid side %d", e.Depth.Side)
		}
		for i := 0; i < int(e.Depth.NUpdates); i++ {
			if !e.Depth.Updates[i].Valid() {
				return fmt.Errorf("event: invalid depth level %d (%+v)", i, e.Depth.Updates[i])
			}
		}
	case KindSnapshot:
		if e.Snap.NBids == 0 || e.Snap.NAsks == 0 {
			return fmt.Errorf("event: empty snapshot")
		}
		if e.Snap.NBids > MaxLevels || e.Snap.NAsks > MaxLevels {
			return fmt.Errorf("event: snapshot depth out of range")
		}
		for i := 0; i < int(e.Snap.NBids); i++ {
			if !e.Snap.Bids[i].Valid() {
				return fmt.Errorf("event: invalid bid %d", i)
			}
		}
		for i := 0; i < int(e.Snap.NAsks); i++ {
			if !e.Snap.Asks[i].Valid() {
				return fmt.Errorf("event: invalid ask %d", i)
			}
		}
	case KindHeartbeat:
	default:
		return fmt.Errorf("event: unknown kind %d", e.Kind)
	}
	return nil
}

// NBidsInt returns the bid count as an int for range loops.
func (s *SnapshotEvent) NBidsInt() int { return int(s.NBids) }

// NAsksInt returns the ask count as an int for range loops.
func (s *SnapshotEvent) NAsksInt() int { return int(s.NAsks) }

// PriceInt converts a floating point price to fixed point ticks.
func PriceInt(v float64) int64 { return int64(math.Round(v * float64(PriceScale))) }

// QtyInt converts a floating point quantity to fixed point units.
func QtyInt(v float64) int64 { return int64(math.Round(v * float64(QtyScale))) }

// PriceFloat converts fixed point ticks back to a floating point price. Only
// for logging, model inputs and tests; never for book arithmetic.
func PriceFloat(v int64) float64 { return float64(v) / float64(PriceScale) }

// QtyFloat converts fixed point units back to a floating point quantity.
func QtyFloat(v int64) float64 { return float64(v) / float64(QtyScale) }
