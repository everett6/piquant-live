package orderbook

import (
	"testing"

	"piquant-live/internal/event"
)

// Regression: a batch whose updates are not in price order used to append the
// out-of-order level, notice the side was unsorted, and skip every remaining
// update in the batch. Updates are now applied all-or-nothing.
func TestBatchAppliesEveryUpdateInOrder(t *testing.T) {
	b := standardBook(t)
	// Asks start at 100.1 x1, 100.2 x5. Insert a deeper ask out of order and
	// delete the touch in the same batch: both must take effect.
	if !b.Apply(depth(101, event.SideSell, level(100.5, 1), level(100.1, 0))) {
		t.Fatal("batch rejected")
	}
	a := b.LevelsOf(Asks)
	if a.N != 2 || a.P[0] != event.PriceInt(100.2) || a.P[1] != event.PriceInt(100.5) {
		t.Fatalf("asks = %v, want [100.2 100.5]", priceSlice(a))
	}
}

// Regression: an insert better than the deepest level was dropped whenever the
// side was already full, so the touch could silently stop reflecting updates.
func TestBestLevelInsertEvictsWorstWhenFull(t *testing.T) {
	b := &Book{}
	bids := make([]event.Level, 0, event.MaxLevels)
	for i := 0; i < event.MaxLevels; i++ {
		bids = append(bids, level(100-float64(i)*0.1, 1))
	}
	if !b.Apply(snapshot(1, bids, []event.Level{level(101, 1)})) {
		t.Fatal("full snapshot rejected")
	}
	if !b.Apply(depth(2, event.SideBuy, level(100.05, 3))) {
		t.Fatal("insert into a full side rejected")
	}
	v := b.LevelsOf(Bids)
	if v.N != event.MaxLevels {
		t.Fatalf("bids = %d levels, want %d", v.N, event.MaxLevels)
	}
	if v.P[0] != event.PriceInt(100.05) || v.Q[0] != event.QtyInt(3) {
		t.Fatalf("top = %v x %v, want 100.05 x 3", event.PriceFloat(v.P[0]), event.QtyFloat(v.Q[0]))
	}
	// The worst level of the 20 was 98.1 (100 - 19*0.1); it made room.
	if v.P[v.N-1] != event.PriceInt(98.2) {
		t.Fatalf("worst bid = %v, want 98.2", event.PriceFloat(v.P[v.N-1]))
	}
}

// Regression: a level deeper than L20 on a full side is dropped, and the book
// must stay consistent (still ordered, sequence still accepted).
func TestDeeperLevelIgnoredWhenFull(t *testing.T) {
	b := &Book{}
	bids := make([]event.Level, 0, event.MaxLevels)
	for i := 0; i < event.MaxLevels; i++ {
		bids = append(bids, level(100-float64(i)*0.1, 1))
	}
	b.Apply(snapshot(1, bids, []event.Level{level(101, 1)}))
	if !b.Apply(depth(2, event.SideBuy, level(99.0, 4))) {
		t.Fatal("deep insert rejected")
	}
	v := b.LevelsOf(Bids)
	if v.N != event.MaxLevels || v.P[v.N-1] != event.PriceInt(98.1) {
		t.Fatalf("deep insert changed the window: n=%d worst=%v", v.N, event.PriceFloat(v.P[v.N-1]))
	}
}

// Regression: a depth update with a negative size was written straight into the
// book, poisoning every depth-weighted feature summed afterwards.
func TestNegativeDepthQtyRejected(t *testing.T) {
	b := standardBook(t)
	bad := depth(101, event.SideBuy, event.Level{Price: event.PriceInt(100), Qty: -5})
	if b.Apply(bad) {
		t.Fatal("negative size accepted")
	}
	if b.Status != StatusStale || b.Gaps != 1 {
		t.Fatalf("status = %s gaps = %d after a negative size", b.Status, b.Gaps)
	}
	if q := b.LevelsOf(Bids).Q[0]; q != event.QtyInt(2) {
		t.Fatalf("bid qty = %v, want the pre-batch 2", event.QtyFloat(q))
	}
}

// A batch whose last update is invalid must not leave the earlier updates
// applied: the book is all-or-nothing per depth message.
func TestRejectedBatchIsNotPartiallyApplied(t *testing.T) {
	b := standardBook(t)
	before := *b.LevelsOf(Bids)
	bad := depth(101, event.SideBuy, level(100, 9), event.Level{Price: event.PriceInt(99.5), Qty: -1})
	if b.Apply(bad) {
		t.Fatal("batch with an invalid level accepted")
	}
	after := *b.LevelsOf(Bids)
	if after != before {
		t.Fatalf("side changed by a rejected batch: %+v -> %+v", before, after)
	}
}

// A crossed or duplicate-price snapshot is a corrupt capture, not a book state.
func TestCorruptSnapshotRejected(t *testing.T) {
	cases := map[string]struct {
		bids, asks []event.Level
	}{
		"crossed":    {[]event.Level{level(101, 1)}, []event.Level{level(100, 1)}},
		"locked":     {[]event.Level{level(100, 1)}, []event.Level{level(100, 1)}},
		"unsorted":   {[]event.Level{level(99, 1), level(100, 1)}, []event.Level{level(101, 1)}},
		"duplicate":  {[]event.Level{level(100, 1), level(100, 2)}, []event.Level{level(101, 1)}},
		"ask sorted": {[]event.Level{level(100, 1)}, []event.Level{level(102, 1), level(101, 1)}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			b := &Book{}
			if b.Apply(snapshot(1, tc.bids, tc.asks)) {
				t.Fatal("corrupt snapshot accepted")
			}
			if b.Ready || b.Status != StatusStale || b.Gaps != 1 {
				t.Fatalf("ready=%v status=%s gaps=%d", b.Ready, b.Status, b.Gaps)
			}
			// The sequence is still anchored at the rejected snapshot so a
			// following delta is judged against it, and the next good snapshot
			// recovers the book.
			if b.LastSeq != 1 {
				t.Fatalf("LastSeq = %d, want the rejected snapshot's sequence", b.LastSeq)
			}
			if !b.Apply(snapshot(5, []event.Level{level(100, 1)}, []event.Level{level(101, 1)})) {
				t.Fatal("recovery snapshot rejected")
			}
			if b.Status != StatusLive || b.Gaps != 1 {
				t.Fatalf("recovery: status=%s gaps=%d", b.Status, b.Gaps)
			}
		})
	}
}

// Inserting a level between two existing ones must land in rank order, and the
// side must stay strictly ordered across many interleaved inserts and deletes.
func TestInterleavedInsertsStayOrdered(t *testing.T) {
	b := &Book{}
	if !b.Apply(snapshot(1,
		[]event.Level{level(100, 1), level(99, 1), level(98, 1)},
		[]event.Level{level(101, 1), level(102, 1), level(103, 1)})) {
		t.Fatal("snapshot rejected")
	}
	updates := [][]event.Level{
		{level(99.5, 2)},
		{level(98.5, 2)},
		{level(99.7, 3)},
		{level(99, 0)},
		{level(100.5, 1)},
		{level(97.5, 1)},
		{level(98.5, 0)},
	}
	for i, ups := range updates {
		if !b.Apply(depth(uint64(2+i), event.SideBuy, ups...)) {
			t.Fatalf("update %d rejected", i)
		}
		v := b.LevelsOf(Bids)
		for j := 1; j < int(v.N); j++ {
			if v.P[j] >= v.P[j-1] {
				t.Fatalf("update %d: bids unordered: %v", i, priceSlice(v))
			}
		}
	}
	want := []float64{100.5, 100, 99.7, 99.5, 98, 97.5}
	v := b.LevelsOf(Bids)
	if int(v.N) != len(want) {
		t.Fatalf("bids = %v, want %v", priceSlice(v), want)
	}
	for i, p := range want {
		if v.P[i] != event.PriceInt(p) {
			t.Fatalf("bids = %v, want %v", priceSlice(v), want)
		}
	}
}

func priceSlice(L *Levels) []float64 {
	out := make([]float64, L.N)
	for i := range out {
		out[i] = event.PriceFloat(L.P[i])
	}
	return out
}
