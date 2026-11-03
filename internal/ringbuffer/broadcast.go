package ringbuffer

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// Segment is one fixed-size block of the shared log. Slots hold the values
// committed to them; a consumer must have observed the commit cursor move past
// a slot before reading it, and the producer must not wrap back onto a slot
// until every live consumer has passed it.
type Segment[T any] struct {
	Slots []T
}

// subscribed is a live consumer seen by the producer. The producer owns the
// list; cursors register and unregister through the ring's lock.
type subscribed[T any] struct {
	pos  *cursor
	done bool
}

func (s *subscribed[T]) load() uint64 { return s.pos.seq.Load() }

// Ring is a segmented single-producer/multi-consumer log with one backing
// store.
//
// The producer publishes values at monotonically increasing positions; position
// n lives in segment (n>>shift)&(nseg-1), slot n&mask. Each live consumer has
// its own Cursor. The producer back-pressures (or, with TryPublish, refuses) a
// write once the slowest live cursor is one full ring behind, so a live cursor
// never observes data that has been overwritten.
//
// Values are immutable once committed. Consumers should copy out what they
// need before calling Set; nothing in the ring is mutated in place, so several
// readers share the write-once payload without copying it.
//
// The producer must be a single goroutine. Any number of goroutines may create,
// use and close cursors. The zero value is not usable; call New (SPSC) or NewRing (Ring).
type Ring[T any] struct {
	segs   []Segment[T]
	nseg   uint64
	shift  uint
	mask   uint64
	maxLag uint64

	// head is the commit cursor, written only by the producer.
	head cursor
	_    [CacheLineSize]byte

	closed atomic.Bool
	lag    count // write attempts refused due to back-pressure
	_      [CacheLineSize]byte

	mu     sync.Mutex
	subs   []subscribed[T]
	cur    []*Cursor[T]
	live   int
	pushed count
}

// NewRing builds a ring with segs segments of size slots each. Both are rounded up
// to powers of two (minimum 2 and 1 respectively). The whole backing store is
// allocated here, once.
func NewRing[T any](segs, size int) *Ring[T] {
	if segs < 2 {
		segs = 2
	}
	if size < 1 {
		size = 1
	}
	segs = 1 << log2ceil(uint64(segs))
	size = 1 << log2ceil(uint64(size))
	r := &Ring[T]{
		segs:   make([]Segment[T], segs),
		nseg:   uint64(segs),
		shift:  uint(log2ceil(uint64(size))),
		mask:   uint64(size - 1),
		maxLag: uint64(segs) * uint64(size),
		subs:   make([]subscribed[T], 0, maxSubscribers),
		cur:    make([]*Cursor[T], 0, maxSubscribers),
	}
	for i := range r.segs {
		r.segs[i].Slots = make([]T, size)
	}
	return r
}

func log2ceil(v uint64) uint {
	var n uint
	for 1<<n < v {
		n++
	}
	return n
}

// MaxSubscribers is the number of concurrently open cursors a ring supports.
const maxSubscribers = 16

// Capacity returns the total number of slots across all segments.
func (r *Ring[T]) Capacity() int { return len(r.segs) * len(r.segs[0].Slots) }

// Published returns the next position the producer will write.
func (r *Ring[T]) Published() uint64 { return r.head.seq.Load() }

// NewCursor creates a consumer cursor starting at the current stream head.
//
// A live cursor throttles the producer: the producer may not run more than one
// full ring ahead of it, so data it has not read is never overwritten, and a
// live cursor that stops reading stalls the stream. Create a cursor live for
// slow or bursty readers (persistence, model inference).
//
// A non-live cursor does not throttle the producer. It may lag behind by more
// than one ring length and then see overwritten slots; use it only when the
// producer is paced anyway (e.g. a replay harness that publishes only after
// every reader has caught up).
func (r *Ring[T]) NewCursor(live bool) (*Cursor[T], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if live && r.live >= maxSubscribers {
		return nil, ErrTooManyCursors
	}
	c := &Cursor[T]{ring: r, live: live}
	if live {
		r.subs = append(r.subs, subscribed[T]{pos: &c.pos})
		r.cur = append(r.cur, c)
		r.live++
	}
	c.Set(r.head.seq.Load())
	return c, nil
}

// Close releases a live cursor. Consumers must close cursors they no longer
// use; an open live cursor keeps throttling the producer.
func (c *Cursor[T]) Close() {
	if !c.live || !c.closed.CompareAndSwap(false, true) {
		return
	}
	r := c.ring
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.subs {
		if r.subs[i].pos == &c.pos {
			r.subs[i].done = true
		}
	}
	r.live--
}

// Close stops the producer: a blocked Publish returns and further publishes
// are refused. It does not affect readers.
func (r *Ring[T]) Close() { r.closed.Store(true) }

// barrier returns the lowest position that some live consumer has not yet
// passed (the stream head when no consumer is live).
func (r *Ring[T]) barrier() uint64 {
	r.mu.Lock()
	low := r.head.seq.Load()
	for _, s := range r.subs {
		if s.done {
			continue
		}
		if p := s.load(); p < low {
			low = p
		}
	}
	r.mu.Unlock()
	return low
}

// Publish commits v at the next position and returns it and true. It spins,
// yielding the CPU, while a live consumer is a full ring behind; it returns
// false if the ring was closed while waiting.
func (r *Ring[T]) Publish(v T) (uint64, bool) {
	h := r.head.seq.Load()
	for h-r.barrier() >= r.maxLag {
		if r.closed.Load() {
			return h, false
		}
		runtime.Gosched()
	}
	return r.commit(h, v), true
}

// TryPublish commits v at the next position when no live consumer is a full
// ring behind. It never blocks.
func (r *Ring[T]) TryPublish(v T) (uint64, bool) {
	h := r.head.seq.Load()
	if h-r.barrier() >= r.maxLag {
		r.lag.v.Add(1)
		return h, false
	}
	return r.commit(h, v), true
}

func (r *Ring[T]) commit(h uint64, v T) uint64 {
	r.segs[(h>>r.shift)&(r.nseg-1)].Slots[h&r.mask] = v
	r.head.seq.Store(h + 1)
	r.pushed.v.Add(1)
	return h
}

// Cursor is one consumer's position in a Ring.
type Cursor[T any] struct {
	ring   *Ring[T]
	pos    cursor
	live   bool
	closed atomic.Bool
}

// Pos returns the next position this cursor will read.
func (c *Cursor[T]) Pos() uint64 { return c.pos.seq.Load() }

// TryPopPos copies the value at the cursor position into dst and advances,
// returning the position and true. It returns false when caught up or closed.
func (c *Cursor[T]) TryPopPos(dst *T) (uint64, bool) {
	if c.closed.Load() {
		return c.pos.seq.Load(), false
	}
	p := c.pos.seq.Load()
	if p == c.ring.head.seq.Load() {
		return p, false
	}
	*dst = c.ring.segs[(p>>c.ring.shift)&(c.ring.nseg-1)].Slots[p&c.ring.mask]
	c.Set(p + 1)
	return p, true
}

// Set moves the cursor to pos, which must not be beyond a slot already
// committed.
func (c *Cursor[T]) Set(pos uint64) { c.pos.seq.Store(pos) }
