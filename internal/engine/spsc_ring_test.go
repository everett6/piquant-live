package engine

import (
	"sync"
	"testing"
)

func TestSPSCRingPushPop(t *testing.T) {
	ring := NewSPSCRing(4)
	if ring.Cap() != 4 {
		t.Fatalf("expected capacity 4, got %d", ring.Cap())
	}
	if ring.Len() != 0 {
		t.Fatalf("expected initial len 0, got %d", ring.Len())
	}

	// Pop from empty
	if _, ok := ring.Pop(); ok {
		t.Errorf("expected Pop on empty ring to return false")
	}

	// Push 4 items (fill buffer)
	for i := uint64(1); i <= 4; i++ {
		ev := MarketEvent{Sequence: i, Price: 100.0 * float64(i)}
		if !ring.Push(ev) {
			t.Fatalf("expected Push(%d) to succeed", i)
		}
	}

	if ring.Len() != 4 {
		t.Errorf("expected len 4, got %d", ring.Len())
	}

	// Buffer is now full; 5th push must fail
	overflowEv := MarketEvent{Sequence: 5}
	if ring.Push(overflowEv) {
		t.Errorf("expected Push on full ring to return false")
	}

	// Pop first 2 items
	ev1, ok1 := ring.Pop()
	if !ok1 || ev1.Sequence != 1 {
		t.Errorf("expected sequence 1, got %v", ev1)
	}
	ev2, ok2 := ring.Pop()
	if !ok2 || ev2.Sequence != 2 {
		t.Errorf("expected sequence 2, got %v", ev2)
	}

	if ring.Len() != 2 {
		t.Errorf("expected len 2, got %d", ring.Len())
	}

	// Push 2 more items across circular wrap-around
	if !ring.Push(MarketEvent{Sequence: 5}) {
		t.Errorf("expected push 5 to succeed")
	}
	if !ring.Push(MarketEvent{Sequence: 6}) {
		t.Errorf("expected push 6 to succeed")
	}

	// Drain remaining 4 items
	expectedSeqs := []uint64{3, 4, 5, 6}
	for _, expected := range expectedSeqs {
		ev, ok := ring.Pop()
		if !ok || ev.Sequence != expected {
			t.Errorf("expected sequence %d, got ok=%v, seq=%d", expected, ok, ev.Sequence)
		}
	}

	if ring.Len() != 0 {
		t.Errorf("expected len 0 after drain, got %d", ring.Len())
	}
}

func TestSPSCRingConcurrentProducerConsumer(t *testing.T) {
	ring := NewSPSCRing(1024)
	total := 100_000

	var wg sync.WaitGroup
	wg.Add(2)

	// Producer
	go func() {
		defer wg.Done()
		for i := 1; i <= total; i++ {
			ev := MarketEvent{Sequence: uint64(i), Price: float64(i)}
			for !ring.Push(ev) {
				// busy wait / spin on full
			}
		}
	}()

	// Consumer
	go func() {
		defer wg.Done()
		expectedSeq := uint64(1)
		for expectedSeq <= uint64(total) {
			ev, ok := ring.Pop()
			if !ok {
				continue
			}
			if ev.Sequence != expectedSeq {
				t.Errorf("out of order event: expected %d, got %d", expectedSeq, ev.Sequence)
			}
			expectedSeq++
		}
	}()

	wg.Wait()
}

func TestSPSCRingBatchPushPop(t *testing.T) {
	ring := NewSPSCRing(16)

	// Batch push 10 items
	input := make([]MarketEvent, 10)
	for i := range input {
		input[i] = MarketEvent{Sequence: uint64(i + 1), Price: float64(i + 1) * 100}
	}

	written := ring.PushBatch(input)
	if written != 10 {
		t.Fatalf("expected 10 items written, got %d", written)
	}
	if ring.Len() != 10 {
		t.Fatalf("expected ring len 10, got %d", ring.Len())
	}

	// Try writing 10 more (capacity is 16, so only 6 free slots remain)
	more := make([]MarketEvent, 10)
	for i := range more {
		more[i] = MarketEvent{Sequence: uint64(11 + i)}
	}
	writtenMore := ring.PushBatch(more)
	if writtenMore != 6 {
		t.Fatalf("expected 6 items written before full, got %d", writtenMore)
	}
	if ring.Len() != 16 {
		t.Fatalf("expected ring full (16), got %d", ring.Len())
	}

	// Batch pop 8 items
	drain1 := make([]MarketEvent, 8)
	read1 := ring.PopBatch(drain1)
	if read1 != 8 {
		t.Fatalf("expected 8 items popped, got %d", read1)
	}
	for i := 0; i < 8; i++ {
		if drain1[i].Sequence != uint64(i+1) {
			t.Errorf("drain1[%d] seq mismatch: expected %d, got %d", i, i+1, drain1[i].Sequence)
		}
	}
	if ring.Len() != 8 {
		t.Fatalf("expected remaining len 8, got %d", ring.Len())
	}

	// Batch pop remaining items with a larger destination slice
	drain2 := make([]MarketEvent, 16)
	read2 := ring.PopBatch(drain2)
	if read2 != 8 {
		t.Fatalf("expected remaining 8 items popped, got %d", read2)
	}
	expectedRemaining := []uint64{9, 10, 11, 12, 13, 14, 15, 16}
	for i := 0; i < 8; i++ {
		if drain2[i].Sequence != expectedRemaining[i] {
			t.Errorf("drain2[%d] seq mismatch: expected %d, got %d", i, expectedRemaining[i], drain2[i].Sequence)
		}
	}
	if ring.Len() != 0 {
		t.Fatalf("expected empty ring, got %d", ring.Len())
	}
}

func BenchmarkSPSCRingBatchPushPop(b *testing.B) {
	ring := NewSPSCRing(1024)
	batchSize := 32
	pushBatch := make([]MarketEvent, batchSize)
	for i := range pushBatch {
		pushBatch[i] = MarketEvent{Sequence: uint64(i + 1), Price: 75000.0}
	}
	popBatch := make([]MarketEvent, batchSize)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ring.PushBatch(pushBatch)
		ring.PopBatch(popBatch)
	}
}
