package features

import (
	"math"
	"testing"

	"piquant-live/internal/event"
	"piquant-live/internal/orderbook"
)

func lv(p, q float64) event.Level {
	return event.Level{Price: event.PriceInt(p), Qty: event.QtyInt(q)}
}

func snap(seq uint64, t int64, bids, asks []event.Level) *event.Envelope {
	e := &event.Envelope{Sequence: seq, RecvTime: t, Kind: event.KindSnapshot}
	e.Snap.Sequence = seq
	e.Snap.NBids = uint8(copy(e.Snap.Bids[:], bids))
	e.Snap.NAsks = uint8(copy(e.Snap.Asks[:], asks))
	return e
}

func dep(seq uint64, t int64, side event.Side, ups ...event.Level) *event.Envelope {
	e := &event.Envelope{Sequence: seq, RecvTime: t, Kind: event.KindDepth}
	e.Depth.Sequence = seq
	e.Depth.Side = side
	e.Depth.NUpdates = uint8(copy(e.Depth.Updates[:], ups))
	return e
}

func trade(t int64, side event.Side, qty float64) *event.Envelope {
	e := &event.Envelope{RecvTime: t, Kind: event.KindTrade}
	e.Trade.Side = side
	e.Trade.Qty = event.QtyInt(qty)
	return e
}

type harness struct {
	book *orderbook.Book
	feat *Engine
	seq  uint64
}

// base is an early event time for synthetic harness runs. It must stay well
// below 1e18: nanosecond timestamps of a few times 1e17 leave float64 without
// integer precision above 2^53. base sits on a 100 ms bucket boundary.
const base = int64(100e6)

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{book: &orderbook.Book{}, feat: New(1), seq: 100}
	h.apply(snap(h.next(), base,
		[]event.Level{lv(100, 2), lv(99.9, 3)},
		[]event.Level{lv(100.1, 2), lv(100.2, 3)}))
	if !h.book.Ready {
		t.Fatal("book not ready")
	}
	return h
}

func (h *harness) next() uint64 { h.seq++; return h.seq }

// apply feeds the book first and then the engine: features describe the state
// the event produced, and OFI is measured against the captured pre-update book.
func (h *harness) apply(e *event.Envelope) {
	h.book.Apply(e)
	h.feat.Advance(h.book, e)
}

func (h *harness) extract() []float32 {
	dst := make([]float32, NCore)
	if n := h.feat.Extract(h.book, dst); n != NCore {
		return nil
	}
	return dst
}

func near(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func TestImbalanceAndMicroprice(t *testing.T) {
	h := newHarness(t)
	f := h.extract()
	if f == nil {
		t.Fatal("extract returned nothing")
	}
	// best bid 2 vs ask 2 -> 0; l5 uses both levels: 5 vs 5 -> 0.
	if f[0] != 0 || f[1] != 0 || f[15] != 0 {
		t.Fatalf("imbalance = %v %v %v", f[0], f[1], f[15])
	}
	// microprice with equal sizes is the mid: deviation 0; spread 0.1 on 100.05.
	if !near(float64(f[3]), 0, 1e-6) {
		t.Fatalf("micro_dev = %v", f[3])
	}
	if !near(float64(f[4]), 0.1/100.05*1e4, 0.01) {
		t.Fatalf("spread_bps = %v", f[4])
	}
}

func TestMicropriceDeviationSign(t *testing.T) {
	h := newHarness(t)
	// Heavy bid at the top pushes the microprice toward the ask.
	tm := int64(1.1e9)
	h.apply(dep(h.next(), tm, event.SideBuy, lv(100, 9)))
	f := h.extract()
	if f[3] <= 0 || f[3] >= 0.5 {
		t.Fatalf("micro_dev = %v, want (0, 0.5)", f[3])
	}
	if !near(float64(f[0]), 7.0/11.0, 1e-5) {
		t.Fatalf("imb_l1 = %v", f[0])
	}
}

func TestOFITerm(t *testing.T) {
	h := newHarness(t)
	// Bid qty +1 at the same price: OFI = +1.
	h.apply(dep(h.next(), base+100e6, event.SideBuy, lv(100, 3)))
	// Ask qty -0.5 at the same price: supply removed from the ask => +0.5.
	h.apply(dep(h.next(), base+200e6, event.SideSell, lv(100.1, 1.5)))
	// Ask size at the second level increases by 2, contributing -2.
	h.apply(dep(h.next(), base+300e6, event.SideSell, lv(100.2, 5)))
	f := h.extract()
	// The second ask price already exists in the snapshot, so this is +1 +0.5 -2.
	if f[5] != -0.5 {
		t.Fatalf("ofi_1s = %v, want -0.5", f[5])
	}
	if math.IsNaN(float64(f[11])) {
		t.Fatal("nan return")
	}
	if f[15] != float32(1.0/3.0) {
		t.Fatalf("queue ratio = %v", f[15])
	}
}

// A window must cover the time it claims at any event rate. The old fixed
// 512-event ring held 5 s at 100 events/s but only 46 ms at 20 kHz; the bucket
// windows hold the whole period.
func TestOFIWindowsCoverTheWindowAtHighRate(t *testing.T) {
	h := newHarness(t)
	const n = 5000 // 1 ms apart => 5 s of events
	for i := 1; i <= n; i++ {
		h.apply(dep(h.next(), base+int64(i)*1e6, event.SideBuy, lv(100, float64(i+2))))
	}
	f := h.extract()
	// The window may miss the bucket straddling its left edge and nothing else:
	// at most bucketNs/1ms = 50 samples here.
	slack := float64(bucketNs / 1e6)
	if !near(float64(f[6]), n, slack) || float64(f[6]) > n {
		t.Fatalf("ofi_5s = %v over 5 s of 1 ms updates, want %d less at most %v", f[6], n, slack)
	}
	if !near(float64(f[7]), n, slack) {
		t.Fatalf("ofi_30s = %v, want %d less at most %v", f[7], n, slack)
	}
	// The last second of a 1 ms stream holds 1000 updates.
	if !near(float64(f[5]), 1000, slack) {
		t.Fatalf("ofi_1s = %v, want 1000 less at most %v", f[5], slack)
	}
}

func TestOFIWindowsExpireInEventTime(t *testing.T) {
	h := newHarness(t)
	h.apply(dep(h.next(), base+100e6, event.SideBuy, lv(100, 5)))
	if f := h.extract(); !near(float64(f[5]), 3, 1e-6) {
		t.Fatalf("ofi_1s = %v, want 3", f[5])
	}
	// Two seconds later (event time) the 1 s window is empty but 5 s is not.
	h.apply(dep(h.next(), base+2e9, event.SideBuy, lv(100, 5)))
	f := h.extract()
	if f[5] != 0 {
		t.Fatalf("ofi_1s after expiry = %v", f[5])
	}
	if !near(float64(f[6]), 3, 1e-6) {
		t.Fatalf("ofi_5s = %v", f[6])
	}
	// Thirty seconds later the short windows are empty; the 30 s window still
	// holds the sample at the base (its bucket start is exactly t-30 s).
	h.apply(dep(h.next(), base+30e9, event.SideBuy, lv(100, 5)))
	f = h.extract()
	if f[5] != 0 || f[6] != 0 {
		t.Fatalf("short windows not expired: %v %v", f[5], f[6])
	}
	// One more bucket and the base sample leaves the 30 s window too.
	h.apply(dep(h.next(), base+31e9, event.SideBuy, lv(100, 5)))
	if f = h.extract(); !near(float64(f[7]), 0, 1e-6) {
		t.Fatalf("ofi_30s after 31 s = %v, want 0", f[7])
	}
}

func TestReturnsAndVolatility(t *testing.T) {
	h := newHarness(t)
	// Walk the book up and down every 0.5 s: the returns must have both signs.
	signs := map[bool]int{}
	for i := 1; i <= 24; i++ {
		tm := base + int64(i)*500e6
		off := 0.01 * float64(i%3)
		oldOff := 0.01 * float64((i-1)%3)
		oldBid, oldAsk := 100+oldOff, 100.1+oldOff
		h.apply(dep(h.next(), tm, event.SideBuy,
			lv(100+off, 2), lv(99.9, 3), lv(oldBid, 0)))
		h.apply(dep(h.next(), tm, event.SideSell,
			lv(100.1+off, 2), lv(100.2, 3), lv(oldAsk, 0)))
		if f := h.extract(); f[11] != 0 {
			signs[f[11] > 0]++
		}
	}
	f := h.extract()
	if f[10] <= 0 {
		t.Fatalf("vol = %v, want > 0", f[10])
	}
	if signs[true] == 0 || signs[false] == 0 {
		t.Fatalf("1 s returns never changed sign: %v", signs)
	}
}

func TestTradeFeatures(t *testing.T) {
	h := newHarness(t)
	h.apply(trade(base+100e6, event.SideBuy, 1.5))
	h.apply(trade(base+200e6, event.SideSell, 0.5))
	h.apply(trade(base+1.9e9, event.SideSell, 10)) // only the newest trade is inside 1 s
	f := h.extract()
	if !near(float64(f[13]), 1.5-0.5-10, 1e-6) {
		t.Fatalf("trade_imb_5s = %v", f[13])
	}
	if f[14] != 1 {
		t.Fatalf("trade_cnt_1s = %v, want 1", f[14])
	}
}

func TestWindowsAcrossBucketWrap(t *testing.T) {
	// 600 buckets = 60 s of events, more than the 512-bucket ring: the ring
	// wraps and the windows must still be exact.
	h := newHarness(t)
	const n = 600
	for i := 1; i <= n; i++ {
		h.apply(dep(h.next(), base+int64(i)*100e6, event.SideBuy, lv(100, float64(i+2))))
	}
	f := h.extract()
	if !near(float64(f[5]), 10, 1e-3) {
		t.Fatalf("ofi_1s = %v, want the last 10 one-per-100 ms updates", f[5])
	}
	if !near(float64(f[6]), 50, 1e-3) {
		t.Fatalf("ofi_5s = %v, want 50", f[6])
	}
	if !near(float64(f[7]), 300, 1e-3) {
		t.Fatalf("ofi_30s = %v, want 300", f[7])
	}

	tr := newHarness(t)
	for i := 1; i <= n; i++ {
		tr.apply(trade(base+int64(i)*100e6, event.SideBuy, 1))
	}
	f = tr.extract()
	if !near(float64(f[13]), 50, 1e-3) || f[14] != 10 {
		t.Fatalf("trade windows after wrap = %v %v, want 50 10", f[13], f[14])
	}
	tr.apply(trade(base+1000e9, event.SideBuy, 1))
	if f = tr.extract(); f[13] != 1 || f[14] != 1 {
		t.Fatalf("trade windows after expiry = %v %v, want 1 1", f[13], f[14])
	}
}

// A long idle gap must not leave stale flow in the windows, and must not walk
// the whole bucket ring to expire it.
func TestIdleGapClearsWindows(t *testing.T) {
	h := newHarness(t)
	h.apply(dep(h.next(), base+100e6, event.SideBuy, lv(100, 9)))
	h.apply(trade(base+200e6, event.SideBuy, 4))
	h.apply(dep(h.next(), base+10*3600*1e9, event.SideBuy, lv(100, 9)))
	f := h.extract()
	if f[5] != 0 || f[6] != 0 || f[7] != 0 || f[13] != 0 || f[14] != 0 || f[10] != 0 {
		t.Fatalf("windows survived a one-hour gap: %v %v %v %v %v %v",
			f[5], f[6], f[7], f[13], f[14], f[10])
	}
}

func TestSnapshotResetsWindows(t *testing.T) {
	h := newHarness(t)
	h.apply(dep(h.next(), int64(1.1e9), event.SideBuy, lv(100, 5)))
	h.apply(snap(h.next(), int64(1.2e9), []event.Level{lv(100, 2)}, []event.Level{lv(100.1, 2)}))
	f := h.extract()
	if f[5] != 0 || f[7] != 0 || f[14] != 0 {
		t.Fatalf("windows survived a snapshot: %v %v %v", f[5], f[7], f[14])
	}
}

// A backwards timestamp is counted and folded into the current bucket rather
// than rewinding the windows.
func TestBackwardsTimestampIsCounted(t *testing.T) {
	h := newHarness(t)
	h.apply(trade(base+2e9, event.SideBuy, 1))
	h.apply(trade(base+1e9, event.SideSell, 1))
	if got := h.feat.TimeReversals(); got != 1 {
		t.Fatalf("TimeReversals = %d, want 1", got)
	}
	if f := h.extract(); f[13] != 0 || f[14] != 2 {
		t.Fatalf("trade_imb/cnt = %v/%v, want 0/2", f[13], f[14])
	}
}

func TestNotReadyExtract(t *testing.T) {
	e := New(1)
	b := &orderbook.Book{}
	dst := make([]float32, NCore)
	if n := e.Extract(b, dst); n != 0 {
		t.Fatal("extract on unready book")
	}
	if n := e.Extract(b, make([]float32, 3)); n != 0 {
		t.Fatal("extract into small dst")
	}
}

// Features of one vector describe one book state: Extract reads the book it is
// given, not a copy stashed by the previous Advance.
func TestExtractUsesTheGivenBook(t *testing.T) {
	h := newHarness(t)
	h.apply(dep(h.next(), base+100e6, event.SideBuy, lv(100, 4)))
	if f := h.extract(); f[0] != float32(2.0/6.0) {
		t.Fatalf("imb_l1 = %v, want 2/6", f[0])
	}
	// Change the book without an event: the level features must follow it.
	h.book.Apply(dep(h.next(), base+200e6, event.SideBuy, lv(100, 8)))
	if f := h.extract(); f[0] != float32(6.0/10.0) {
		t.Fatalf("imb_l1 = %v, want 6/10 after the book changed", f[0])
	}
}

func TestReset(t *testing.T) {
	h := newHarness(t)
	h.apply(dep(h.next(), int64(1.1e9), event.SideBuy, lv(100, 5)))
	h.feat.Reset()
	if h.feat.started || h.feat.Symbol() != 1 {
		t.Fatalf("reset left state: started=%v sym=%d", h.feat.started, h.feat.Symbol())
	}
}

func TestRollingVolatilityMatchesWindow(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 300; i++ {
		tm := base + int64(i)*200e6
		level := i % 7
		off := 0.01 * float64(level)
		bids := []event.Level{lv(100+off, 2), lv(99.9, 3)}
		asks := []event.Level{lv(100.1+off, 2), lv(100.2, 3)}
		for j := 0; j <= 6; j++ {
			if j != level {
				bids = append(bids, lv(100+0.01*float64(j), 0))
				asks = append(asks, lv(100.1+0.01*float64(j), 0))
			}
		}
		h.apply(dep(h.next(), tm, event.SideBuy, bids...))
		h.apply(dep(h.next(), tm, event.SideSell, asks...))
	}
	e := h.feat
	// The incremental 30 s window must equal a direct recomputation over the
	// buckets that are still inside it.
	var n, sum, sq float64
	for s := e.c30; s <= e.headSeq; s++ {
		b := &e.b[s&bktMask]
		if b.start != s*bucketNs {
			continue
		}
		n += float64(b.retN)
		sum += b.retSum
		sq += b.retSq
	}
	if n < 2 {
		t.Fatalf("volatility samples retained = %v, want at least 2", n)
	}
	m2 := sq - sum*sum/n
	if m2 < 0 {
		m2 = 0
	}
	want := math.Sqrt(m2 / (n - 1))
	if !near(e.volatility(), want, 1e-9) {
		t.Fatalf("rolling volatility = %g, want %g", e.volatility(), want)
	}
	if f := h.extract(); !near(float64(f[10]), want, 1e-3) {
		t.Fatalf("vol_30s_bps = %v, want %g", f[10], want)
	}
}

func BenchmarkAdvanceAndExtract(b *testing.B) {
	h := &harness{book: &orderbook.Book{}, feat: New(1), seq: 1}
	h.apply(snap(1, 1e9, []event.Level{lv(100, 2), lv(99.9, 3)}, []event.Level{lv(100.1, 2), lv(100.2, 3)}))
	e := dep(10, 1e9, event.SideBuy, lv(100, 1))
	dst := make([]float32, NCore)
	var seq uint64 = 1
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		seq++
		e.Depth.Sequence = seq
		e.RecvTime = 1e9 + int64(i)*1e6
		h.apply(e)
		h.feat.Extract(h.book, dst)
	}
}

func BenchmarkExtractOnly(b *testing.B) {
	h := &harness{book: &orderbook.Book{}, feat: New(1), seq: 1}
	h.apply(snap(1, 1e9, []event.Level{lv(100, 2), lv(99.9, 3), lv(99.8, 1)}, []event.Level{lv(100.1, 2), lv(100.2, 3)}))
	dst := make([]float32, NCore)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.feat.Extract(h.book, dst)
	}
}
