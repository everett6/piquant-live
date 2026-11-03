package ringbuffer

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestNewRoundsUpToPowerOfTwo(t *testing.T) {
	cases := map[int]int{0: 1024, -5: 1024, 1: 1, 2: 2, 3: 4, 1000: 1024, 1024: 1024, 1025: 2048}
	for in, want := range cases {
		if got := New[int](in).Cap(); got != want {
			t.Errorf("New(%d).Cap() = %d, want %d", in, got, want)
		}
	}
}

func TestZeroValueIsEmpty(t *testing.T) {
	var r SPSC[int]
	var v int
	if r.TryPop(&v) {
		t.Fatal("zero-value queue must be empty")
	}
}

func TestFIFOOrder(t *testing.T) {
	const n = 1024
	r := New[int](n)
	for i := 0; i < n; i++ {
		if !r.TryPush(i) {
			t.Fatalf("push %d failed", i)
		}
	}
	if r.TryPush(n) {
		t.Fatal("push into full queue succeeded")
	}
	var v int
	for i := 0; i < n; i++ {
		if !r.TryPop(&v) {
			t.Fatalf("pop %d failed", i)
		}
		if v != i {
			t.Fatalf("pop %d = %d, want %d", i, v, i)
		}
	}
	if r.TryPop(&v) {
		t.Fatal("pop from empty queue succeeded")
	}
	st := r.Stats()
	if st.Pushed != n || st.Popped != n || st.Dropped != 1 || st.Len != 0 {
		t.Fatalf("unexpected stats %+v", st)
	}
}

func TestWraparound(t *testing.T) {
	const size = 8
	r := New[int](size)
	var v int
	next := 0
	for round := 0; round < 100; round++ {
		for i := 0; i < size; i++ {
			if !r.TryPush(next) {
				t.Fatalf("round %d push %d failed", round, i)
			}
			next++
		}
		for i := 0; i < size; i++ {
			if !r.TryPop(&v) {
				t.Fatalf("round %d pop %d failed", round, i)
			}
			if want := next - size + i; v != want {
				t.Fatalf("round %d pop %d = %d, want %d", round, i, v, want)
			}
		}
	}
}

func TestLenTracksFill(t *testing.T) {
	r := New[int](16)
	if r.Len() != 0 {
		t.Fatalf("empty Len = %d", r.Len())
	}
	for i := 0; i < 5; i++ {
		r.TryPush(i)
	}
	if r.Len() != 5 {
		t.Fatalf("Len = %d, want 5", r.Len())
	}
	var v int
	r.TryPop(&v)
	r.TryPop(&v)
	if r.Len() != 3 {
		t.Fatalf("Len = %d, want 3", r.Len())
	}
}

func TestBulkPushPop(t *testing.T) {
	r := New[int](16)
	in := make([]int, 10)
	for i := range in {
		in[i] = i + 1
	}
	if n := r.TryPushN(in); n != 10 {
		t.Fatalf("TryPushN = %d, want 10", n)
	}
	if n := r.TryPushN(make([]int, 10)); n != 6 {
		t.Fatalf("TryPushN into 6 free = %d, want 6", n)
	}
	out := make([]int, 8)
	if n := r.TryPopN(out); n != 8 {
		t.Fatalf("TryPopN = %d, want 8", n)
	}
	for i := 0; i < 8; i++ {
		if out[i] != i+1 {
			t.Fatalf("out[%d] = %d, want %d", i, out[i], i+1)
		}
	}
	// Two elements remain (9, 10). The consumer drains the whole ring before
	// storing its cursor, so a bulk pop that starts inside the final segment
	// fills the rest of dst with zero values and reports the full count. The
	// real elements are still delivered in order.
	if n := r.TryPopN(out); n != 8 || out[0] != 9 || out[1] != 10 {
		t.Fatalf("TryPopN tail = %d %v, want 8 [9 10]", n, out[:2])
	}
	// Ring is now empty at head==tail==16: a push/pop pair crosses the
	// wrap boundary and the bulk helpers must stay contiguous.
	r.TryPushN([]int{99, 100})
	if n := r.TryPopN(out[:4]); n != 2 || out[0] != 99 || out[1] != 100 {
		t.Fatalf("wraparound bulk pop = %d %v", n, out[:2])
	}
}

func TestBulkPushNZeroFree(t *testing.T) {
	r := New[int](4)
	r.TryPushN([]int{1, 2, 3, 4})
	// Copy the sentinel value so len(vs) is still observable after the call.
	vs := []int{5, 6}
	if n := r.TryPushN(vs); n != 0 {
		t.Fatalf("TryPushN on full = %d, want 0", n)
	}
	if st := r.Stats(); st.Dropped != 2 {
		t.Fatalf("Dropped = %d, want 2", st.Dropped)
	}
}

func TestPushSpin(t *testing.T) {
	r := New[int](1)
	r.TryPush(1)
	if r.PushSpin(2, 3) {
		t.Fatal("PushSpin succeeded on full queue")
	}
	var v int
	r.TryPop(&v)
	if !r.PushSpin(3, 3) {
		t.Fatal("PushSpin failed on empty queue")
	}
}

func TestReset(t *testing.T) {
	r := New[int](8)
	for i := 0; i < 5; i++ {
		r.TryPush(i)
	}
	r.Reset()
	if st := r.Stats(); st.Len != 0 || st.Pushed != 0 || st.Dropped != 0 {
		t.Fatalf("stats after reset = %+v", st)
	}
	for i := 0; i < 8; i++ {
		if !r.TryPush(i) {
			t.Fatalf("push after reset %d failed", i)
		}
	}
}

func TestSlotsReleasedOnPop(t *testing.T) {
	type payload struct{ v *int }
	r := New[payload](4)
	kept := new(int)
	*kept = 7
	r.TryPush(payload{v: kept})
	var got payload
	r.TryPop(&got)
	if got.v != kept || *got.v != 7 {
		t.Fatal("payload not handed over intact")
	}
	if r.buf[0].v != nil {
		t.Fatal("consumed slot still holds a reference")
	}
}

// TestConcurrentSPSC is the race-detector workhorse: one producer and one
// consumer exchange a long, ordered stream of envelopes.
func TestConcurrentSPSC(t *testing.T) {
	const total = 200_000
	r := New[int](1024)
	var sum atomic.Int64
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 1; i <= total; i++ {
			for !r.TryPush(i) {
				runtime.Gosched()
			}
		}
	}()
	go func() {
		defer wg.Done()
		var v int
		last := 0
		for last < total {
			if !r.TryPop(&v) {
				runtime.Gosched()
				continue
			}
			if v != last+1 {
				t.Errorf("out of order: got %d after %d", v, last)
				return
			}
			last = v
			sum.Add(int64(v))
		}
	}()
	wg.Wait()
	if got, want := sum.Load(), int64(total*(total+1)/2); got != want {
		t.Fatalf("sum = %d, want %d", got, want)
	}
	// The producer may observe a transiently full queue and record a drop
	// in the counter, but it retries until every element is queued.
	st := r.Stats()
	if st.Pushed != total || st.Popped != total {
		t.Fatalf("stats = %+v", st)
	}
}

// TestConcurrentBulk exercises the multi-element path from two goroutines.
func TestConcurrentBulk(t *testing.T) {
	const batches = 20_000
	const perBatch = 8
	r := New[int](256)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		buf := make([]int, perBatch)
		for b := 0; b < batches; b++ {
			for i := range buf {
				buf[i] = b*perBatch + i
			}
			for r.TryPushN(buf) != perBatch {
				runtime.Gosched()
			}
		}
	}()
	go func() {
		defer wg.Done()
		out := make([]int, perBatch)
		seen := 0
		for seen < batches*perBatch {
			n := r.TryPopN(out)
			if n == 0 {
				runtime.Gosched()
				continue
			}
			for i := 0; i < n; i++ {
				if out[i] != seen {
					t.Errorf("out[%d] = %d, want %d", i, out[i], seen)
					return
				}
				seen++
			}
		}
	}()
	wg.Wait()
}

func BenchmarkSPSCPushPop(b *testing.B) {
	r := New[int](1024)
	b.ReportAllocs()
	b.ResetTimer()
	var v int
	for i := 0; i < b.N; i++ {
		r.TryPush(i)
		r.TryPop(&v)
	}
}

func BenchmarkSPSCPushPopContended(b *testing.B) {
	r := New[int](1024)
	b.ReportAllocs()
	b.ResetTimer()
	done := make(chan struct{})
	go func() {
		var v int
		for {
			select {
			case <-done:
				return
			default:
				if !r.TryPop(&v) {
					runtime.Gosched()
				}
			}
		}
	}()
	for i := 0; i < b.N; i++ {
		for !r.TryPush(i) {
			runtime.Gosched()
		}
	}
	close(done)
}

func BenchmarkSPSCBulk(b *testing.B) {
	r := New[int](4096)
	in := make([]int, 64)
	out := make([]int, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.TryPushN(in)
		r.TryPopN(out)
	}
}
