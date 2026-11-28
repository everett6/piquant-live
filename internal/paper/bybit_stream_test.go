//go:build quarantine

// Quarantined 2026-11-11 (owner spec): Bybit is not a US venue and nothing uses this ingestor.
// Build or test it with -tags quarantine.

package paper

import (
	"bufio"
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"piquant-live/internal/engine"
	"piquant-live/internal/marketdata"
)

var (
	bybitSampleSnapshot = []byte(`{"topic":"orderbook.50.BTCUSDT","type":"snapshot","ts":1672304484978,"data":{"s":"BTCUSDT","b":[["65000.00","1.500"],["64999.50","2.100"],["64999.00","3.400"]],"a":[["65000.50","0.850"],["65001.00","1.200"],["65001.50","2.500"]],"u":12345678,"seq":123456789},"cts":1672304484976}`)
	bybitSampleDelta    = []byte(`{"topic":"orderbook.50.BTCUSDT","type":"delta","ts":1672304485000,"data":{"s":"BTCUSDT","b":[["65000.00","0.500"],["64999.00","0"]],"a":[["65000.50","0"],["65002.00","1.800"]],"u":12345679,"seq":123456790},"cts":1672304484998}`)
	bybitSampleTrades   = []byte(`{"topic":"publicTrade.BTCUSDT","type":"snapshot","ts":1672304484978,"data":[{"T":1672304484800,"s":"BTCUSDT","S":"Buy","v":"0.05","p":"65000.50","L":"PlusTick","i":"20f34a","BT":false},{"T":1672304484850,"s":"BTCUSDT","S":"Sell","v":"0.12","p":"65000.00","L":"MinusTick","i":"20f34b","BT":false}]}`)
)

func TestBybitFastParseOrderbook(t *testing.T) {
	var delta marketdata.OrderBookDelta
	var isSnapshot bool

	// Snapshot
	ok := FastParseBybitOrderbook(bybitSampleSnapshot, &delta, &isSnapshot)
	if !ok {
		t.Fatalf("expected snapshot parsing to succeed")
	}
	if !isSnapshot {
		t.Fatalf("expected isSnapshot to be true")
	}
	if delta.FinalUpdateID() != 12345678 {
		t.Fatalf("expected update ID 12345678, got %d", delta.FinalUpdateID())
	}
	if delta.NumBids != 3 || delta.NumAsks != 3 {
		t.Fatalf("expected 3 bids and 3 asks, got %d bids and %d asks", delta.NumBids, delta.NumAsks)
	}
	if delta.Bids[0].Price != 65000.00 || delta.Bids[0].Size != 1.500 {
		t.Fatalf("unexpected top bid: %v", delta.Bids[0])
	}
	if delta.Asks[0].Price != 65000.50 || delta.Asks[0].Size != 0.850 {
		t.Fatalf("unexpected top ask: %v", delta.Asks[0])
	}

	// Delta
	ok = FastParseBybitOrderbook(bybitSampleDelta, &delta, &isSnapshot)
	if !ok {
		t.Fatalf("expected delta parsing to succeed")
	}
	if isSnapshot {
		t.Fatalf("expected isSnapshot to be false")
	}
	if delta.FinalUpdateID() != 12345679 {
		t.Fatalf("expected update ID 12345679, got %d", delta.FinalUpdateID())
	}
}

func TestBybitFastParseTrades(t *testing.T) {
	tradesCount := 0
	FastParseBybitTrades(bybitSampleTrades, func(price, qty float64, side uint8, tradeTime int64) {
		tradesCount++
		if tradesCount == 1 {
			if price != 65000.50 || qty != 0.05 || side != 1 || tradeTime != 1672304484800 {
				t.Fatalf("unexpected trade 1: p=%f, q=%f, side=%d, time=%d", price, qty, side, tradeTime)
			}
		} else if tradesCount == 2 {
			if price != 65000.00 || qty != 0.12 || side != 2 || tradeTime != 1672304484850 {
				t.Fatalf("unexpected trade 2: p=%f, q=%f, side=%d, time=%d", price, qty, side, tradeTime)
			}
		}
	})

	if tradesCount != 2 {
		t.Fatalf("expected 2 trades, got %d", tradesCount)
	}
}

func TestBybitFastExtractSymbol(t *testing.T) {
	sym1 := FastExtractBybitSymbol(bybitSampleSnapshot)
	if sym1 != "BTCUSDT" {
		t.Fatalf("expected BTCUSDT, got %s", sym1)
	}

	ethTrade := []byte(`{"topic":"publicTrade.ETHUSDT","data":[{"s":"ETHUSDT","p":"3500.0"}]}`)
	sym2 := FastExtractBybitSymbol(ethTrade)
	if sym2 != "ETHUSDT" {
		t.Fatalf("expected ETHUSDT, got %s", sym2)
	}
}

func TestBybitStreamIngestorPushToRing(t *testing.T) {
	ring := engine.NewSPSCRing(1024)
	cfg := DefaultBybitStreamConfig()
	ingestor := NewBybitStreamIngestor(cfg, ring)

	// Feed snapshot payload
	ingestor.HandleStreamPayload(bybitSampleSnapshot)

	// Verify event landed in SPSC Ring
	ev, ok := ring.Pop()
	if !ok {
		t.Fatalf("expected event in ring buffer")
	}
	if ev.Kind != 2 { // Snapshot
		t.Fatalf("expected event kind 2 (Snapshot), got %d", ev.Kind)
	}
	expectedMid := (65000.00 + 65000.50) * 0.5
	if ev.Price != expectedMid {
		t.Fatalf("expected mid price %f, got %f", expectedMid, ev.Price)
	}
	if ev.Flags&VenueBybit == 0 {
		t.Fatalf("expected VenueBybit flag set, got %d", ev.Flags)
	}

	// Feed trade payload
	ingestor.HandleStreamPayload(bybitSampleTrades)

	// Check trade 1
	tr1, ok := ring.Pop()
	if !ok || tr1.Kind != 0 || tr1.Price != 65000.50 || tr1.Side != 1 {
		t.Fatalf("unexpected trade 1 in ring: %v, ok=%v", tr1, ok)
	}
	// Check trade 2
	tr2, ok := ring.Pop()
	if !ok || tr2.Kind != 0 || tr2.Price != 65000.00 || tr2.Side != 2 {
		t.Fatalf("unexpected trade 2 in ring: %v, ok=%v", tr2, ok)
	}

	// Verify L2 book clone
	book, exists := ingestor.GetL2Book("BTCUSDT")
	if !exists || book == nil {
		t.Fatalf("expected BTCUSDT book to exist")
	}
	mid, hasMid := book.MidPrice()
	if !hasMid || mid != expectedMid {
		t.Fatalf("expected book mid %f, got %f", expectedMid, mid)
	}
}

func TestDualVenueIngestorCreation(t *testing.T) {
	ring := engine.NewSPSCRing(1024)
	binanceCfg := DefaultStreamConfig()
	bybitCfg := DefaultBybitStreamConfig()

	dual := NewDualVenueIngestor(binanceCfg, bybitCfg, ring)
	if dual.Binance() == nil || dual.Bybit() == nil {
		t.Fatalf("expected non-nil child ingestors")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start and immediate stop
	dual.Start(ctx)
	time.Sleep(10 * time.Millisecond)
	dual.Stop()
}

func BenchmarkBybitFastParseOrderbook(b *testing.B) {
	var delta marketdata.OrderBookDelta
	var isSnapshot bool

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		FastParseBybitOrderbook(bybitSampleSnapshot, &delta, &isSnapshot)
	}
}

func BenchmarkBybitFastParseTrades(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		FastParseBybitTrades(bybitSampleTrades, func(p, q float64, s uint8, t int64) {})
	}
}

func BenchmarkBybitFastExtractSymbol(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = FastExtractBybitSymbol(bybitSampleSnapshot)
	}
}

func TestBybitMarketFeedInterfaceAndCopyL2Book(t *testing.T) {
	ring := engine.NewSPSCRing(1024)
	ingestor := NewBybitStreamIngestor(DefaultBybitStreamConfig(), ring)

	var feed MarketFeed = ingestor
	if feed.IsFallback() {
		t.Fatalf("expected IsFallback() == false for BybitStreamIngestor")
	}

	ingestor.HandleStreamPayload(bybitSampleSnapshot)
	total, depths, _, _, _, _, lastNs, lastRealNs := feed.Stats()
	if total != 1 || depths != 1 || lastNs == 0 || lastRealNs == 0 {
		t.Fatalf("unexpected Stats(): total=%d depths=%d lastNs=%d lastRealNs=%d", total, depths, lastNs, lastRealNs)
	}

	var dst marketdata.L2OrderBook
	if !feed.CopyL2Book("BTCUSDT", &dst) {
		t.Fatalf("expected CopyL2Book to succeed")
	}
	mid, ok := dst.MidPrice()
	if !ok || mid != 65000.25 {
		t.Fatalf("expected mid=65000.25, got mid=%v ok=%v", mid, ok)
	}
}

func writeTestWSFrame(c net.Conn, fin bool, op byte, payload []byte) error {
	b0 := op & 0x0F
	if fin {
		b0 |= 0x80
	}
	var hdr [10]byte
	hdr[0] = b0
	n := len(payload)
	var hdrLen int
	if n <= 125 {
		hdr[1] = byte(n)
		hdrLen = 2
	} else {
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(n))
		hdrLen = 4
	}
	if _, err := c.Write(hdr[:hdrLen]); err != nil {
		return err
	}
	_, err := c.Write(payload)
	return err
}

func TestBybitStreamFragmentedFrame(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	srvErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			srvErr <- err
			return
		}
		defer conn.Close()

		br := bufio.NewReader(conn)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				srvErr <- err
				return
			}
			if line == "\r\n" || line == "\n" {
				break
			}
		}
		if _, err := conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")); err != nil {
			srvErr <- err
			return
		}
		// Consume client subscribe frame
		if _, _, _, err := readWSFrame(br); err != nil {
			srvErr <- err
			return
		}
		mid := len(bybitSampleSnapshot) / 2
		if err := writeTestWSFrame(conn, false, 0x1, bybitSampleSnapshot[:mid]); err != nil {
			srvErr <- err
			return
		}
		if err := writeTestWSFrame(conn, true, 0x0, bybitSampleSnapshot[mid:]); err != nil {
			srvErr <- err
			return
		}
		_ = writeTestWSFrame(conn, true, 0x8, nil)
		srvErr <- nil
	}()

	ring := engine.NewSPSCRing(1024)
	cfg := DefaultBybitStreamConfig()
	cfg.Endpoint = "ws://" + ln.Addr().String() + "/v5/public/linear"
	cfg.PingInterval = time.Minute
	ingestor := NewBybitStreamIngestor(cfg, ring)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = ingestor.connectAndStream(ctx)
	if err := <-srvErr; err != nil {
		t.Fatalf("mock ws server error: %v", err)
	}

	ev, ok := ring.Pop()
	if !ok {
		t.Fatalf("expected reassembled fragmented snapshot event in ring")
	}
	if ev.Kind != 2 || ev.Price != 65000.25 {
		t.Fatalf("unexpected event: %+v", ev)
	}
}
