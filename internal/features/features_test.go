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
// integer precision above 2^53.
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

// apply feeds the engine first (it needs the pre-update book for OFI) and
// then the book.
func (h *harness) apply(e *event.Envelope) {
	h.feat.Advance(h.book, e)
	h.book.Apply(e)
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
	h.feat.compute(h.book, base)
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
	h.apply(&event.Envelope{Sequence: h.next(), RecvTime: tm, Kind: event.KindHeartbeat})
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
	// 30 s covers everything up to the 30 s edge; the -0.1 s sample is inside.
	// Thirty-one seconds later everything has expired.
	h.apply(dep(h.next(), base+30e9, event.SideBuy, lv(100, 5)))
	f = h.extract()
	if f[5] != 0 || f[6] != 0 || f[7] != 3 {
		t.Fatalf("ofi windows not expired: %v %v %v", f[5], f[6], f[7])
	}
}

func TestReturnsAndVolatility(t *testing.T) {
	h := newHarness(t)
	// Walk the book up and down by one cent every event, then measure the
	// volatility of 1 s returns: it must be positive, and the returns must
	// have both signs.
	// The bid walks one cent up and down every 0.5 s: alternate moves keep the
	// book ordered (a walk up must raise both sides).
	signs := map[bool]int{}
	for i := 1; i <= 12; i++ {
		tm := base + int64(i)*500e6
		off := 0.01 * float64(i%3)
		oldOff := 0.01 * float64((i-1)%3)
		oldBid, oldAsk := 100+oldOff, 100.1+oldOff
		h.apply(dep(h.next(), tm, event.SideBuy,
			lv(100+off, 2), lv(99.9, 3), lv(oldBid, 0)))
		h.apply(dep(h.next(), tm, event.SideSell,
			lv(100.1+off, 2), lv(100.2, 3), lv(oldAsk, 0)))
	}
	f := h.extract()
	if !(f[10] > 0) {
		t.Fatalf("vol = %v, want > 0", f[10])
	}
	if f[11] == 0 && math.Abs(float64(f[11])) < 1e-9 {
		t.Logf("ret_1s = %v (mid unchanged over the last second)", f[11])
	}
	for i := 13; i <= 24; i++ {
		tm := base + int64(i)*500e6
		off := 0.01 * float64(i%3)
		oldOff := 0.01 * float64((i-1)%3)
		oldBid, oldAsk := 100+oldOff, 100.1+oldOff
		h.apply(dep(h.next(), tm, event.SideBuy,
			lv(100+off, 2), lv(99.9, 3), lv(oldBid, 0)))
		h.apply(dep(h.next(), tm, event.SideSell,
			lv(100.1+off, 2), lv(100.2, 3), lv(oldAsk, 0)))
		r := h.extract()[11]
		if r != 0 {
			signs[r > 0]++
		}
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

func TestIncrementalWindowsAcrossRingWrap(t *testing.T) {
	const n = ringLen + 88
	depth := newHarness(t)
	for i := 1; i <= n; i++ {
		tm := base + int64(i)*10e6
		depth.apply(dep(depth.next(), tm, event.SideBuy, lv(100, float64(i+2))))
	}
	f := depth.extract()
	if f[5] != 100 || f[6] != 500 || f[7] != ringLen {
		t.Fatalf("OFI windows after ring wrap = %v %v %v, want 100 500 %d",
			f[5], f[6], f[7], ringLen)
	}

	trades := newHarness(t)
	for i := 1; i <= n; i++ {
		tm := base + int64(i)*10e6
		trades.apply(trade(tm, event.SideBuy, 1))
	}
	f = trades.extract()
	if f[13] != 500 || f[14] != 100 {
		t.Fatalf("trade windows after ring wrap = %v %v, want 500 100", f[13], f[14])
	}

	trades.apply(&event.Envelope{Sequence: trades.next(), RecvTime: base + 40e9, Kind: event.KindHeartbeat})
	f = trades.extract()
	if f[13] != 0 || f[14] != 0 {
		t.Fatalf("trade windows after expiry = %v %v, want 0 0", f[13], f[14])
	}
}

func TestSnapshotResetsWindows(t *testing.T) {
	h := newHarness(t)
	h.apply(dep(h.next(), int64(1.1e9), event.SideBuy, lv(100, 5)))
	h.apply(snap(h.next(), int64(1.2e9), []event.Level{lv(100, 2)}, []event.Level{lv(100.1, 2)}))
	f := h.extract()
	if f[5] != 0 || f[7] != 0 || f[14] != 0 {
		t.Fatalf("windows survived a snapshot: %v %v", f[5], f[7])
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

func TestReset(t *testing.T) {
	h := newHarness(t)
	h.apply(dep(h.next(), int64(1.1e9), event.SideBuy, lv(100, 5)))
	h.feat.Reset()
	if h.feat.nCh != 0 || h.feat.nTr != 0 || h.feat.Symbol() != 1 {
		t.Fatalf("reset left state: %+v", h.feat)
	}
}

func TestRollingVolatilityExpires(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 12; i++ {
		tm := base + int64(i)*500e6
		off := 0.01 * float64(i%3)
		oldOff := 0.01 * float64((i-1)%3)
		oldBid, oldAsk := 100+oldOff, 100.1+oldOff
		h.apply(dep(h.next(), tm, event.SideBuy,
			lv(100+off, 2), lv(99.9, 3), lv(oldBid, 0)))
		h.apply(dep(h.next(), tm, event.SideSell,
			lv(100.1+off, 2), lv(100.2, 3), lv(oldAsk, 0)))
	}
	if f := h.extract(); f[10] <= 0 {
		t.Fatalf("volatility = %v (n=%d mean=%g m2=%g), want > 0 before expiry",
			f[10], h.feat.volN, h.feat.volMean, h.feat.volM2)
	}
	h.apply(&event.Envelope{Sequence: h.next(), RecvTime: base + 40e9, Kind: event.KindHeartbeat})
	if f := h.extract(); f[10] != 0 {
		t.Fatalf("volatility after expiry = %v, want 0", f[10])
	}
}

func TestRollingVolatilityMatchesRetainedWindow(t *testing.T) {
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
	tm := base + 300*200e6
	values := make([]float64, 0, ringLen)
	oldest := e.chSeq - uint64(e.nCh)
	for seq := oldest; seq < e.chSeq; seq++ {
		i := seq & (ringLen - 1)
		if e.chHasRet[i] && tm-e.chT[i] < win30s {
			values = append(values, e.chRet[i])
		}
	}
	if uint64(len(values)) != e.volN || len(values) < 2 {
		t.Fatalf("retained volatility samples = %d, want %d and at least 2", len(values), e.volN)
	}
	var mean float64
	for _, v := range values {
		mean += v
	}
	mean /= float64(len(values))
	var m2 float64
	for _, v := range values {
		d := v - mean
		m2 += d * d
	}
	want := math.Sqrt(m2 / float64(len(values)-1))
	if !near(e.volatility(), want, 1e-9) {
		t.Fatalf("rolling volatility = %g, want %g", e.volatility(), want)
	}
}

func BenchmarkExtract(b *testing.B) {
	h := &harness{book: &orderbook.Book{}, feat: New(1), seq: 1}
	h.apply(snap(1, 1e9, []event.Level{lv(100, 2), lv(99.9, 3), lv(99.8, 1)}, []event.Level{lv(100.1, 2), lv(100.2, 3)}))
	dst := make([]float32, NCore)
	var seq uint64 = 1
	var t int64 = 1e9
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		seq++
		t += 1e6
		h.apply(dep(seq, t, event.SideBuy, lv(100+float64(i%2)*0.01, float64(i%7+1))))
		h.feat.Extract(h.book, dst)
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
