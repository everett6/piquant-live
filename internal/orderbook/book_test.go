package orderbook

import (
	"testing"

	"piquant-live/internal/event"
)

const px = event.PriceScale

func level(price float64, qty float64) event.Level {
	return event.Level{Price: event.PriceInt(price), Qty: event.QtyInt(qty)}
}

func snapshot(seq uint64, bids, asks []event.Level) *event.Envelope {
	e := &event.Envelope{Sequence: seq, RecvTime: 1, Kind: event.KindSnapshot}
	e.Snap.Sequence = seq
	e.Snap.NBids = uint8(copy(e.Snap.Bids[:], bids))
	e.Snap.NAsks = uint8(copy(e.Snap.Asks[:], asks))
	return e
}

func depth(seq uint64, side event.Side, ups ...event.Level) *event.Envelope {
	e := &event.Envelope{Sequence: seq, RecvTime: 1, Kind: event.KindDepth}
	e.Depth.Sequence = seq
	e.Depth.Side = side
	e.Depth.NUpdates = uint8(copy(e.Depth.Updates[:], ups))
	return e
}

func standardBook(t *testing.T) *Book {
	t.Helper()
	b := &Book{Symbol: 1}
	if !b.Apply(snapshot(100,
		[]event.Level{level(100, 2), level(99.9, 3), level(99.8, 4)},
		[]event.Level{level(100.1, 1), level(100.2, 5)})) {
		t.Fatal("snapshot rejected")
	}
	if b.Status != StatusLive {
		t.Fatalf("status = %s", b.Status)
	}
	return b
}

func TestSnapshotBuildsBook(t *testing.T) {
	b := standardBook(t)
	bp, bq := b.Top(Bids)
	ap, aq := b.Top(Asks)
	if bp != event.PriceInt(100) || bq != event.QtyInt(2) || ap != event.PriceInt(100.1) || aq != event.QtyInt(1) {
		t.Fatalf("top = %d/%d %d/%d", bp, bq, ap, aq)
	}
	if mid, _ := b.Mid(); mid != event.PriceInt(100.05) {
		t.Fatalf("mid = %g", event.PriceFloat(mid))
	}
	if sp := b.Spread(); sp != px/10 {
		t.Fatalf("spread = %d", sp)
	}
}

func TestDepthUpdatesOrdered(t *testing.T) {
	b := standardBook(t)
	// Insert a better bid, change an existing ask, delete a bid.
	if !b.Apply(depth(101, event.SideBuy, level(100.05, 7))) {
		t.Fatal("insert rejected")
	}
	if !b.Apply(depth(102, event.SideSell, level(100.2, 0))) {
		t.Fatal("delete rejected")
	}
	if !b.Apply(depth(103, event.SideBuy, level(99.8, 0), level(99.9, 9))) {
		t.Fatal("batch rejected")
	}
	if p, q := b.Top(Bids); p != event.PriceInt(100.05) || q != event.QtyInt(7) {
		t.Fatalf("bid = %g x %g", event.PriceFloat(p), event.QtyFloat(q))
	}
	if a := b.LevelsOf(Asks); a.N != 1 || a.Q[0] != event.QtyInt(1) {
		t.Fatalf("asks = %+v", a)
	}
	if b.LevelsOf(Bids).N != 3 || b.LevelsOf(Bids).Q[1] != event.QtyInt(2) {
		t.Fatalf("bids = %+v", b.LevelsOf(Bids))
	}
	if b.LastSeq != 103 || b.Updates != 3 || b.Gaps != 0 {
		t.Fatalf("audit = %+v", b)
	}
}

func TestSequenceGapStalesBook(t *testing.T) {
	b := standardBook(t)
	if b.Apply(depth(105, event.SideBuy, level(100, 1))) {
		t.Fatal("gap accepted")
	}
	if b.Status != StatusStale || b.Gaps != 1 {
		t.Fatalf("status = %s gaps = %d", b.Status, b.Gaps)
	}
	if b.Ready {
		t.Fatal("stale book still ready")
	}
	// A duplicate of the last sequence is also a violation.
	b = standardBook(t)
	if b.Apply(depth(100, event.SideBuy, level(100, 1))) || b.Gaps != 1 {
		t.Fatal("duplicate sequence accepted")
	}
}

func TestResyncFromSnapshot(t *testing.T) {
	b := standardBook(t)
	b.Apply(depth(110, event.SideBuy, level(100, 1))) // gap
	if b.Status != StatusStale {
		t.Fatal("not stale")
	}
	// Deltas below the new snapshot sequence are covered by it.
	if !b.Apply(snapshot(200, []event.Level{level(101, 1)}, []event.Level{level(102, 1)})) {
		t.Fatal("snapshot rejected")
	}
	if b.Status != StatusLive || b.LastSeq != 200 {
		t.Fatalf("status = %s seq = %d", b.Status, b.LastSeq)
	}
	if b.Apply(depth(199, event.SideBuy, level(101, 2))) {
		t.Fatal("stale delta applied")
	}
	if !b.Apply(depth(201, event.SideBuy, level(101, 2))) {
		t.Fatal("next delta rejected")
	}
}

func TestSnapshotWithNegativeQtyRejected(t *testing.T) {
	b := &Book{}
	e := snapshot(1, []event.Level{level(10, 1), {Price: event.PriceInt(9), Qty: -5}}, []event.Level{level(11, 1)})
	if b.Apply(e) {
		t.Fatal("snapshot with a negative size accepted")
	}
	if b.Ready || b.Gaps != 1 {
		t.Fatalf("book ready=%v gaps=%d after a corrupt snapshot", b.Ready, b.Gaps)
	}
	// A zero-size level is not a corrupt level, it is an empty slot: skip it.
	b = &Book{}
	if !b.Apply(snapshot(1, []event.Level{level(10, 1), level(9, 0)}, []event.Level{level(11, 1)})) {
		t.Fatal("snapshot with a zero-size level rejected")
	}
	if n := b.LevelsOf(Bids).N; n != 1 {
		t.Fatalf("bids = %d levels, want 1", n)
	}
}

func TestCrossedBookStales(t *testing.T) {
	b := standardBook(t)
	// An ask at or below the best bid means a lost message.
	if b.Apply(depth(101, event.SideSell, level(100, 1))) {
		t.Fatal("crossed book accepted")
	}
	if b.Status != StatusStale || b.Gaps != 1 {
		t.Fatalf("status = %s gaps = %d", b.Status, b.Gaps)
	}
}

func TestUnorderedBatchStaysOrdered(t *testing.T) {
	b := standardBook(t)
	// Deeper levels arrive out of order within one batch; they must be placed
	// by rank (no sort pass is needed, but the invariant is the same).
	if !b.Apply(depth(101, event.SideSell, level(100.5, 1), level(100.3, 1), level(100.4, 1))) {
		t.Fatal("batch rejected")
	}
	a := b.LevelsOf(Asks)
	for i := 1; i < int(a.N); i++ {
		if a.P[i] <= a.P[i-1] {
			t.Fatalf("asks not sorted: n=%d p=%v", a.N, a.P[:a.N])
		}
	}
}

func TestMaxLevelsCap(t *testing.T) {
	b := standardBook(t)
	ups := make([]event.Level, 30)
	for i := range ups {
		ups[i] = level(50-float64(i)*0.1, 1)
	}
	b.Apply(depth(101, event.SideBuy, ups...))
	if n := b.LevelsOf(Bids).N; n != event.MaxLevels {
		t.Fatalf("bids = %d levels, want %d", n, event.MaxLevels)
	}
}

func TestEmptySideStales(t *testing.T) {
	b := standardBook(t)
	b.Apply(depth(101, event.SideSell, level(100.1, 0)))
	b.Apply(depth(102, event.SideSell, level(100.2, 0)))
	if b.Status != StatusStale || b.Ready {
		t.Fatalf("empty ask side: status = %s ready = %v", b.Status, b.Ready)
	}
}

func TestNotReadyIgnoresDepth(t *testing.T) {
	b := &Book{}
	if !b.Apply(depth(5, event.SideBuy, level(1, 1))) {
		t.Fatal("depth before snapshot rejected")
	}
	if b.Updates != 0 || b.Status != StatusUninit {
		t.Fatalf("book changed before snapshot: %+v", b)
	}
}

func TestDepthSum(t *testing.T) {
	b := standardBook(t)
	if got := b.DepthSum(Bids, 2); got != event.QtyInt(5) {
		t.Fatalf("DepthSum = %g", event.QtyFloat(got))
	}
	if got := b.DepthSum(Asks, 20); got != event.QtyInt(6) {
		t.Fatalf("DepthSum all = %g", event.QtyFloat(got))
	}
}

func BenchmarkDepthApply(b *testing.B) {
	book := standardBook(&testing.T{})
	e := depth(101, event.SideBuy, level(100, 1))
	var seq uint64 = 100
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		seq++
		e.Depth.Sequence = seq
		e.Sequence = seq
		e.Depth.Updates[0].Qty = int64(i&3 + 1)
		book.Apply(e)
	}
}
