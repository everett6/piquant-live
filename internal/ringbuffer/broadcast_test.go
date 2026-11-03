package ringbuffer

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRingCapacityRoundsUp(t *testing.T) {
	r := NewRing[int](4, 100)
	if got, want := r.Capacity(), 4*128; got != want {
		t.Fatalf("Capacity = %d, want %d", got, want)
	}
	if r.Published() != 0 {
		t.Fatalf("Published = %d", r.Published())
	}
}

func TestRingSingleCursorStream(t *testing.T) {
	r := NewRing[uint64](2, 4)
	c, err := r.NewCursor(true)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var v uint64
	for i := uint64(0); i < 20; i++ {
		if _, ok := r.Publish(i); !ok {
			t.Fatalf("publish %d failed", i)
		}
		p, ok := c.TryPopPos(&v)
		if !ok || p != i || v != i {
			t.Fatalf("pop = %d %d %v, want %d", p, v, ok, i)
		}
	}
	if _, ok := c.TryPopPos(&v); ok {
		t.Fatal("pop on caught-up cursor succeeded")
	}
}

func TestRingCursorsIndependent(t *testing.T) {
	r := NewRing[int](4, 8)
	a, _ := r.NewCursor(true)
	b, _ := r.NewCursor(true)
	defer a.Close()
	defer b.Close()
	for i := 0; i < 20; i++ {
		r.Publish(i)
	}
	var v int
	for i := 0; i < 5; i++ {
		a.TryPopPos(&v)
	}
	if a.Pos() != 5 || b.Pos() != 0 {
		t.Fatalf("positions %d %d, want 5 0", a.Pos(), b.Pos())
	}
	for i := 5; i < 20; i++ {
		p, ok := a.TryPopPos(&v)
		if !ok || p != uint64(i) || v != i {
			t.Fatalf("a pop %d = %d %v", i, v, ok)
		}
	}
	for i := 0; i < 20; i++ {
		_, ok := b.TryPopPos(&v)
		if !ok || v != i {
			t.Fatalf("b pop %d = %d %v", i, v, ok)
		}
	}
}

func TestRingBackPressure(t *testing.T) {
	r := NewRing[int](2, 4) // capacity 8
	c, _ := r.NewCursor(true)
	defer c.Close()
	for i := 0; i < 8; i++ {
		if _, ok := r.TryPublish(i); !ok {
			t.Fatalf("publish %d refused with room", i)
		}
	}
	// One full ring ahead of an unread live cursor: refused, not overwritten.
	if _, ok := r.TryPublish(8); ok {
		t.Fatal("publish overwrote unread data")
	}
	var v int
	c.TryPopPos(&v)
	if _, ok := r.TryPublish(8); !ok {
		t.Fatal("publish refused after consumer advanced")
	}
	if p, ok := c.TryPopPos(&v); !ok || p != 1 || v != 1 {
		t.Fatalf("first slot = %d %d %v, want 1", p, v, ok)
	}
}

func TestRingPublishBlocksUntilRead(t *testing.T) {
	r := NewRing[int](2, 2) // capacity 4
	c, _ := r.NewCursor(true)
	defer c.Close()
	var v int
	// Keep the producer exactly one ring ahead until the consumer catches up;
	// Publish must block, not overwrite, and return the right positions.
	for i := 0; i < 5000; i++ {
		p, ok := r.Publish(i)
		if !ok || p != uint64(i) {
			t.Fatalf("publish %d = %d %v", i, p, ok)
		}
		if p, ok := c.TryPopPos(&v); !ok || p != uint64(i) || v != i {
			t.Fatalf("pop %d = %d %d %v", i, p, v, ok)
		}
	}
}

func TestRingPublishWaitsForStalledConsumer(t *testing.T) {
	r := NewRing[int](2, 2) // capacity 4
	c, _ := r.NewCursor(true)
	defer c.Close()
	for i := 0; i < 4; i++ {
		r.Publish(i)
	}
	done := make(chan uint64, 1)
	go func() {
		p, _ := r.Publish(99)
		done <- p
	}()
	select {
	case <-done:
		t.Fatal("Publish did not wait for the stalled consumer")
	case <-time.After(20 * time.Millisecond):
	}
	var v int
	if _, ok := c.TryPopPos(&v); !ok || v != 0 {
		t.Fatalf("pop = %d %v", v, ok)
	}
	select {
	case p := <-done:
		if p != 4 {
			t.Fatalf("unblocked at position %d, want 4", p)
		}
	case <-time.After(time.Second):
		t.Fatal("Publish still blocked after the consumer advanced")
	}
}

func TestRingBlockedPublishUnblocksOnClose(t *testing.T) {
	r := NewRing[int](2, 1) // capacity 2
	c, _ := r.NewCursor(true)
	defer c.Close()
	r.Publish(1)
	r.Publish(2)
	done := make(chan bool, 1)
	go func() {
		_, ok := r.Publish(3)
		done <- ok
	}()
	runtime.Gosched()
	r.Close()
	if ok := <-done; ok {
		t.Fatal("publish returned true after Close")
	}
}

func TestRingCloseAndCursorClose(t *testing.T) {
	r := NewRing[int](2, 2)
	if _, err := r.NewCursor(false); err != nil {
		t.Fatal(err)
	}
	c, _ := r.NewCursor(true)
	c.Close()
	c.Close() // idempotent
	if r.live != 0 {
		t.Fatalf("live = %d after close", r.live)
	}
	// A closed cursor does not throttle: the producer runs a full ring ahead.
	for i := 0; i < 10; i++ {
		if _, ok := r.TryPublish(i); !ok {
			t.Fatalf("publish %d refused with no live cursors", i)
		}
	}
}

func TestRingMaxSubscribers(t *testing.T) {
	r := NewRing[int](2, 2)
	for i := 0; i < maxSubscribers; i++ {
		if _, err := r.NewCursor(true); err != nil {
			t.Fatalf("cursor %d: %v", i, err)
		}
	}
	if _, err := r.NewCursor(true); !errors.Is(err, ErrTooManyCursors) {
		t.Fatalf("err = %v, want ErrTooManyCursors", err)
	}
	if _, err := r.NewCursor(false); err != nil {
		t.Fatalf("non-live cursor refused: %v", err)
	}
}

// TestRingConcurrentProducerConsumers runs one producer against several live
// consumers under the race detector.
func TestRingConcurrentProducerConsumers(t *testing.T) {
	const total = 100_000
	const nConsumers = 3
	r := NewRing[uint64](8, 128)
	var wg sync.WaitGroup
	var sums [nConsumers]atomic.Uint64
	for i := 0; i < nConsumers; i++ {
		c, _ := r.NewCursor(true)
		wg.Add(1)
		go func(c *Cursor[uint64], i int) {
			defer wg.Done()
			defer c.Close()
			var v, sum uint64
			for {
				_, ok := c.TryPopPos(&v)
				if !ok {
					if r.closed.Load() && c.Pos() == r.Published() {
						sums[i].Store(sum)
						return
					}
					runtime.Gosched()
					continue
				}
				if v != c.Pos() {
					t.Errorf("consumer %d: value %d out of order", i, v)
					return
				}
				sum += v
			}
		}(c, i)
	}
	for i := uint64(1); i <= total; i++ {
		r.Publish(i)
	}
	r.Close()
	wg.Wait()
	want := uint64(total * (total + 1) / 2)
	for i := 0; i < nConsumers; i++ {
		if got := sums[i].Load(); got != want {
			t.Fatalf("consumer %d sum = %d, want %d", i, got, want)
		}
	}
}

// TestRingSlotReuseUnderRace forces the producer to lap the ring many times
// while a consumer reads slowly, so every slot is overwritten repeatedly.
func TestRingSlotReuseUnderRace(t *testing.T) {
	const total = 50_000
	r := NewRing[uint64](2, 8) // capacity 16: laps 3000+ times
	c, _ := r.NewCursor(true)
	defer c.Close()
	done := make(chan struct{})
	var bad atomic.Int64
	go func() {
		defer close(done)
		var v uint64
		var next uint64
		for next < total {
			if _, ok := c.TryPopPos(&v); !ok {
				runtime.Gosched()
				continue
			}
			next++
			if v != next {
				bad.Add(1)
			}
		}
	}()
	for i := uint64(1); i <= total; i++ {
		r.Publish(i)
	}
	<-done
	if bad.Load() != 0 {
		t.Fatalf("%d out-of-order reads", bad.Load())
	}
}

func BenchmarkRingPublishPop(b *testing.B) {
	r := NewRing[int](4, 1024)
	c, _ := r.NewCursor(true)
	defer c.Close()
	var v int
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Publish(i)
		c.TryPopPos(&v)
	}
}
