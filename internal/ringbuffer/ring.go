// Package ringbuffer provides lock-free single-producer/single-consumer
// queues with cache-line isolation between the producer and consumer cursors.
//
// The queues are the only hand-off between the pinned ingestion core and the
// pinned inference core. They never allocate after construction, never block
// and never take a lock, so they are safe to use from a goroutine that holds
// its OS thread.
//
// Two primitives live here:
//
//   - SPSC, a classic fixed-capacity, power-of-two ring for single-element and
//     bulk hand-offs between exactly one producer and one consumer.
//   - Ring (broadcast.go), a segmented multi-consumer log: one producer
//     publishes a stream once and independent consumers follow it with their
//     own cursors. The order book, the feature engine and the persistence
//     writer each read the same ingest stream without copying it.
package ringbuffer

import (
	"errors"
	"math/bits"
	"runtime"
	"sync/atomic"
)

// CacheLineSize is the L1/L2 line size on both the x86_64 development host and
// the Cortex-A76 used in production (Apple-style 128-byte pairing is not used
// by either, so 64 is the right isolation granularity).
const CacheLineSize = 64

// ErrTooManyCursors is returned by Ring.NewCursor when more live cursors are
// open than the ring supports.
var ErrTooManyCursors = errors.New("ringbuffer: too many live cursors")

// cursor is a single monotonic counter padded to a full cache line so that the
// producer cursor and the consumer cursor never share a line.
type cursor struct {
	seq atomic.Uint64
	_   [CacheLineSize - 8]byte
}

// count is a padded statistics counter.
type count struct {
	v atomic.Uint64
	_ [CacheLineSize - 8]byte
}

// SPSC is a bounded, lock-free, single-producer/single-consumer queue.
//
// One goroutine may call the Push methods and one goroutine may call the Pop
// methods; any other use is a data race. The zero value is not usable; call New.
type SPSC[T any] struct {
	_    [CacheLineSize]byte // keep the slice header off the producer's line
	buf  []T
	mask uint64
	_    [CacheLineSize - 32]byte
	// head is owned by the producer, tail by the consumer. Each lives on its
	// own cache line, with a trailing pad so neighbouring heap objects cannot
	// share the consumer's line either.
	head cursor
	tail cursor
	_    [CacheLineSize]byte

	pushed  count
	popped  count
	dropped count
}

// New allocates a queue holding a power-of-two number of elements, at least
// size. size <= 0 selects the default of 1024. New allocates once, up front.
func New[T any](size int) *SPSC[T] {
	if size <= 0 {
		size = 1024
	}
	if size&(size-1) != 0 {
		size = 1 << bits.Len(uint(size))
	}
	r := &SPSC[T]{
		buf:  make([]T, size),
		mask: uint64(size - 1),
	}
	return r
}

// Cap returns the queue capacity in elements.
func (r *SPSC[T]) Cap() int { return len(r.buf) }

// Len returns the number of elements currently queued. The value is a hint: it
// is exact for the producer and consumer, but stale for any other goroutine.
func (r *SPSC[T]) Len() int {
	h := r.head.seq.Load()
	t := r.tail.seq.Load()
	if h < t {
		return 0
	}
	return int(h - t)
}

// TryPush appends v. It returns false when the queue is full; the element is
// never silently dropped from a caller's point of view, but it is counted.
func (r *SPSC[T]) TryPush(v T) bool {
	// Load the consumer cursor with acquire semantics so that a slot freed by
	// the consumer is not reused before its read completes.
	t := r.tail.seq.Load()
	h := r.head.seq.Load()
	if h-t >= uint64(len(r.buf)) {
		r.dropped.v.Add(1)
		return false
	}
	r.buf[h&r.mask] = v
	// Release the element to the consumer.
	r.head.seq.Store(h + 1)
	r.pushed.v.Add(1)
	return true
}

// PushSpin retries TryPush up to spins times, yielding periodically. A negative
// spin count means spin until the element is queued. It exists for bootstrap and
// shutdown paths; the hot path uses TryPush.
func (r *SPSC[T]) PushSpin(v T, spins int) bool {
	for i := 0; spins < 0 || i < spins; i++ {
		if r.TryPush(v) {
			return true
		}
		if i&15 == 15 {
			runtime.Gosched()
		}
	}
	return false
}

// TryPop removes the oldest element into dst. It returns false when the queue
// is empty.
func (r *SPSC[T]) TryPop(dst *T) bool {
	t := r.tail.seq.Load()
	h := r.head.seq.Load()
	if t == h {
		return false
	}
	var zero T
	*dst = r.buf[t&r.mask]
	// Overwrite the slot so the queue never pins an object the consumer is
	// already done with (matters for pointer-bearing payloads).
	r.buf[t&r.mask] = zero
	r.tail.seq.Store(t + 1)
	r.popped.v.Add(1)
	return true
}

// TryPushN appends as many leading elements of vs as fit and returns how many
// were taken. Elements are copied with copy(), so the slice never escapes.
func (r *SPSC[T]) TryPushN(vs []T) int {
	if len(vs) == 0 {
		return 0
	}
	t := r.tail.seq.Load()
	h := r.head.seq.Load()
	free := uint64(len(r.buf)) - (h - t)
	n := uint64(len(vs))
	if n > free {
		n = free
	}
	if n == 0 {
		r.dropped.v.Add(uint64(len(vs)))
		return 0
	}
	start := h & r.mask
	first := uint64(len(r.buf)) - start
	if first > n {
		first = n
	}
	copy(r.buf[start:start+first], vs[:first])
	if n > first {
		copy(r.buf[:n-first], vs[first:n])
	}
	r.head.seq.Store(h + n)
	r.pushed.v.Add(n)
	if n < uint64(len(vs)) {
		r.dropped.v.Add(uint64(len(vs)) - n)
	}
	return int(n)
}

// TryPopN fills dst and returns how many elements were taken: min(queued,
// len(dst)). The consumer drains the whole ring before publishing its cursor,
// so when the queued run crosses the ring boundary in the middle of dst, the
// remainder of dst is zero-filled. The taken elements are still delivered in
// order, but the remainder is a leftover, not valid data.
func (r *SPSC[T]) TryPopN(dst []T) int {
	if len(dst) == 0 {
		return 0
	}
	t := r.tail.seq.Load()
	h := r.head.seq.Load()
	n := h - t
	if n == 0 {
		return 0
	}
	if n > uint64(len(dst)) {
		n = uint64(len(dst))
	}
	start := t & r.mask
	first := uint64(len(r.buf)) - start
	if first > n {
		first = n
	}
	copy(dst[:first], r.buf[start:start+first])
	if n > first {
		copy(dst[first:n], r.buf[:n-first])
	}
	// Release references held by the vacated slots before handing them back.
	var zero T
	for i := uint64(0); i < first; i++ {
		r.buf[start+i] = zero
	}
	for i := uint64(0); i < n-first; i++ {
		r.buf[i] = zero
	}
	r.tail.seq.Store(t + n)
	r.popped.v.Add(n)
	return int(n)
}

// Stats is a point-in-time snapshot of the queue counters.
type Stats struct {
	Capacity uint64
	Len      uint64
	Pushed   uint64
	Popped   uint64
	Dropped  uint64
}

// Stats returns the current counters.
func (r *SPSC[T]) Stats() Stats {
	h := r.head.seq.Load()
	t := r.tail.seq.Load()
	var ln uint64
	if h >= t {
		ln = h - t
	}
	return Stats{
		Capacity: uint64(len(r.buf)),
		Len:      ln,
		Pushed:   r.pushed.v.Load(),
		Popped:   r.popped.v.Load(),
		Dropped:  r.dropped.v.Load(),
	}
}

// Reset empties the queue and zeroes its counters. It must only be called when
// neither producer nor consumer is running.
// Reset on a zero-value queue is a no-op: it is already empty.
func (r *SPSC[T]) Reset() {
	if r.buf == nil {
		return
	}
	var zero T
	for i := range r.buf {
		r.buf[i] = zero
	}
	r.head.seq.Store(0)
	r.tail.seq.Store(0)
	r.pushed.v.Store(0)
	r.popped.v.Store(0)
	r.dropped.v.Store(0)
}
