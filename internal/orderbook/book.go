// Package orderbook maintains an L1..L20 price-time book from the ingest event
// stream, entirely in fixed-point int64. It never allocates after construction
// and never takes a lock: one goroutine owns a Book.
//
// Sequence handling is strict. The agent records the last exchange sequence and
// flags a gap on ANY out-of-order message (sequence <= last), not only on a
// jump, because that is what an aggregator needs for correct reconstruction.
// The engine then marks the book stale, cancels resting orders and requests a
// fresh snapshot.
package orderbook

import (
	"piquant-live/internal/event"
)

// Status is the reconstruction state of a book.
type Status uint8

const (
	// StatusUninit has never seen a snapshot.
	StatusUninit Status = iota
	// StatusSyncing has a snapshot and is buffering deltas until the stream
	// reaches the snapshot's sequence.
	StatusSyncing
	// StatusLive is the normal operating state.
	StatusLive
	// StatusStale was disrupted by a sequence gap and must resync from a
	// snapshot.
	StatusStale
)

func (s Status) String() string {
	switch s {
	case StatusSyncing:
		return "syncing"
	case StatusLive:
		return "live"
	case StatusStale:
		return "stale"
	default:
		return "uninit"
	}
}

// Side indexes the two arrays of the book.
type Side uint8

// Book sides.
const (
	Bids Side = 0
	Asks Side = 1
)

// Levels is one side of the book, best level at index 0.
type Levels struct {
	P [event.MaxLevels]int64
	Q [event.MaxLevels]int64
	N uint8
}

// Best returns the best price and size, or zeroes when the side is empty.
func (l *Levels) Best() (price, qty int64) {
	if l.N == 0 {
		return 0, 0
	}
	return l.P[0], l.Q[0]
}

// Book is one instrument's L1..L20 state plus its sequence audit.
type Book struct {
	Symbol    uint16
	Venue     uint16
	Levels    [2]Levels
	Ready     bool
	Anchored  bool // a snapshot or depth event established the sequence base
	LastSeq   uint64
	Status    Status
	Gaps      uint64 // sequence violations seen
	Updates   uint64 // accepted updates
	Snapshots uint64

	snapSeq uint64 // sequence of the snapshot being applied (syncing)

	// Last accepted trade, for mark-to-market and prints.
	LastTrade event.TradeEvent
	TradeTime int64
	Trades    uint64
}

// Reset clears the book and puts it back in StatusUninit.
func (b *Book) Reset() {
	*b = Book{Symbol: b.Symbol, Venue: b.Venue, Status: StatusStale}
}

// Apply applies an envelope to the book. It returns false when the envelope was
// rejected as a sequence violation (the book is then StatusStale).
func (b *Book) Apply(e *event.Envelope) bool {
	switch e.Kind {
	case event.KindSnapshot:
		b.applySnapshot(&e.Snap)
		return true
	case event.KindDepth:
		return b.applyDepth(&e.Depth)
	case event.KindTrade:
		b.LastTrade = e.Trade
		b.TradeTime = e.SendTime
		b.Trades++
		return true
	default:
		return true
	}
}

func (b *Book) applySnapshot(s *event.SnapshotEvent) {
	b.Levels[Bids].N = 0
	b.Levels[Asks].N = 0
	for i := 0; i < int(s.NBids); i++ {
		if s.Bids[i].Qty > 0 {
			b.Levels[Bids].P[b.Levels[Bids].N] = s.Bids[i].Price
			b.Levels[Bids].Q[b.Levels[Bids].N] = s.Bids[i].Qty
			b.Levels[Bids].N++
		}
	}
	for i := 0; i < int(s.NAsks); i++ {
		if s.Asks[i].Qty > 0 {
			b.Levels[Asks].P[b.Levels[Asks].N] = s.Asks[i].Price
			b.Levels[Asks].Q[b.Levels[Asks].N] = s.Asks[i].Qty
			b.Levels[Asks].N++
		}
	}
	b.Ready = b.Levels[Bids].N > 0 && b.Levels[Asks].N > 0
	b.LastSeq = s.Sequence
	b.snapSeq = s.Sequence
	b.Anchored = true
	b.Snapshots++
	if b.Ready {
		b.Status = StatusLive
	} else {
		b.Status = StatusUninit
	}
}

func (b *Book) applyDepth(d *event.DepthEvent) bool {
	if !b.Ready {
		// No snapshot yet: only snapshots can start a book.
		return true
	}
	if b.Status == StatusSyncing && d.Sequence <= b.snapSeq {
		return true // already covered by the snapshot
	}
	b.Anchored = true
	if d.Sequence != b.LastSeq+1 {
		b.fail(d.Sequence)
		return false
	}
	var side Side
	switch d.Side {
	case event.SideBuy:
		side = Bids
	case event.SideSell:
		side = Asks
	default:
		b.fail(d.Sequence)
		return false
	}
	L := &b.Levels[side]
	// Updates may arrive after trades on the same price; apply them all. If
	// the book is not strictly ordered afterwards, rebuild it in place.
	if !applyLevels(L, d.Updates[:d.NUpdates], side == Bids) {
		sortLevels(L, side == Bids)
	}
	if L.N == 0 || b.Levels[Bids].N == 0 || b.Levels[Asks].N == 0 {
		b.Ready = false
		b.Status = StatusStale
		b.LastSeq = d.Sequence
		return false
	}
	if b.Levels[Bids].P[0] >= b.Levels[Asks].P[0] {
		// Crossed book: a lost message. Resync.
		b.Ready = false
		b.Status = StatusStale
		b.LastSeq = d.Sequence
		b.Gaps++
		return false
	}
	b.LastSeq = d.Sequence
	if b.Status == StatusSyncing {
		b.Status = StatusLive
	}
	b.Updates++
	return true
}

func (b *Book) fail(seq uint64) {
	b.Gaps++
	b.Status = StatusStale
	b.Ready = false
	if seq > b.LastSeq {
		b.LastSeq = seq
	}
}

// applyLevels applies absolute quantity updates (qty 0 removes a level). It
// returns false when the side is no longer strictly price-ordered.
func applyLevels(L *Levels, ups []event.Level, descending bool) bool {
	for i := range ups {
		lv := &ups[i]
		idx := -1
		for j := 0; j < int(L.N); j++ {
			if L.P[j] == lv.Price {
				idx = j
				break
			}
		}
		switch {
		case lv.Qty == 0:
			if idx >= 0 {
				copy(L.P[idx:], L.P[idx+1:L.N])
				copy(L.Q[idx:], L.Q[idx+1:L.N])
				L.N--
			}
		case idx >= 0:
			L.Q[idx] = lv.Qty
		default:
			if int(L.N) == event.MaxLevels {
				continue // deeper than L20
			}
			L.P[L.N] = lv.Price
			L.Q[L.N] = lv.Qty
			L.N++
			if !ordered(L, descending) {
				return false
			}
		}
	}
	return true
}

func ordered(L *Levels, descending bool) bool {
	for i := 1; i < int(L.N); i++ {
		if descending {
			if L.P[i] >= L.P[i-1] {
				return false
			}
		} else if L.P[i] <= L.P[i-1] {
			return false
		}
	}
	return true
}

// sortLevels insertion-sorts one side; at most 20 levels.
func sortLevels(L *Levels, descending bool) {
	for i := 1; i < int(L.N); i++ {
		p, q := L.P[i], L.Q[i]
		j := i - 1
		for j >= 0 {
			if descending {
				if L.P[j] > p {
					break
				}
			} else if L.P[j] < p {
				break
			}
			L.P[j+1] = L.P[j]
			L.Q[j+1] = L.Q[j]
			j--
		}
		L.P[j+1] = p
		L.Q[j+1] = q
	}
}

// Top returns the best level of a side.
func (b *Book) Top(s Side) (price, qty int64) { return b.Levels[s].Best() }

// Mid returns the mid price in fixed point, and false if the book is not
// ready.
func (b *Book) Mid() (int64, bool) {
	if !b.Ready {
		return 0, false
	}
	bp, _ := b.Levels[Bids].Best()
	ap, _ := b.Levels[Asks].Best()
	return (bp + ap) / 2, true
}

// Spread returns the ask minus bid, or 0 when not ready.
func (b *Book) Spread() int64 {
	if !b.Ready {
		return 0
	}
	return b.Levels[Asks].P[0] - b.Levels[Bids].P[0]
}

// DepthSum returns the sum of quantities over the top n levels of a side.
func (b *Book) DepthSum(s Side, n int) int64 {
	L := &b.Levels[s]
	if n > int(L.N) {
		n = int(L.N)
	}
	var sum int64
	for i := 0; i < n; i++ {
		sum += L.Q[i]
	}
	return sum
}

// Levels returns the side's level array.
func (b *Book) LevelsOf(s Side) *Levels { return &b.Levels[s] }
