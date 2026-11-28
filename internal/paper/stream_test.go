package paper

import (
	"bytes"
	"encoding/json"
	"testing"
)

type fullTradePayload struct {
	EventType string `json:"e"`
	EventTime int64  `json:"E"`
	Symbol    string `json:"s"`
	TradeID   uint64 `json:"t"`
	Price     string `json:"p"`
	Quantity  string `json:"q"`
	TradeTime int64  `json:"T"`
	IsBuyerMM bool   `json:"m"`
	Ignore    bool   `json:"M"`
}

func TestTradePayloadUnmarshal(t *testing.T) {
	payload := []byte(`{"stream":"btcusdt@trade","data":{"e":"trade","E":1793862063463,"s":"BTCUSDT","t":6821515870,"p":"80626.01000000","q":"0.00056000","T":1793862063463,"m":false,"M":true}}`)

	var combined binanceCombinedStreamPayload
	if err := json.Unmarshal(payload, &combined); err != nil {
		t.Fatalf("combined unmarshal failed: %v", err)
	}

	var tr fullTradePayload
	if err := json.Unmarshal(combined.Data, &tr); err != nil {
		t.Fatalf("trade unmarshal failed: %v", err)
	}
	t.Logf("SUCCESS: event=%q time=%d symbol=%q price=%s qty=%s id=%d isBuyerMM=%v",
		tr.EventType, tr.EventTime, tr.Symbol, tr.Price, tr.Quantity, tr.TradeID, tr.IsBuyerMM)
	if tr.TradeID != 6821515870 {
		t.Fatalf("TradeID mismatch: got %d, want 6821515870", tr.TradeID)
	}
	if tr.IsBuyerMM != false {
		t.Fatalf("IsBuyerMM mismatch: got %v, want false", tr.IsBuyerMM)
	}
}

func TestReadWSFrameUnmaskedServerFrames(t *testing.T) {
	// Per RFC 6455, server-to-client data frames are unmasked (MASK bit = 0).
	msg := []byte(`{"type":"ticker"}`)
	frame := append([]byte{0x81, byte(len(msg))}, msg...)
	payload, op, fin, err := readWSFrame(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("readWSFrame failed on unmasked server frame: %v", err)
	}
	if op != 0x1 || !fin || string(payload) != string(msg) {
		t.Fatalf("unexpected frame result: op=%d fin=%v payload=%q", op, fin, string(payload))
	}

	// Fragmented unmasked server message: FIN=0 text followed by FIN=1 continuation.
	f1 := append([]byte{0x01, 5}, []byte("hello")...)
	f2 := append([]byte{0x80, 6}, []byte(" world")...)
	r := bytes.NewReader(append(f1, f2...))
	p1, op1, fin1, err := readWSFrame(r)
	if err != nil || op1 != 0x1 || fin1 || string(p1) != "hello" {
		t.Fatalf("fragment 1 mismatch: op=%d fin=%v p=%q err=%v", op1, fin1, string(p1), err)
	}
	p2, op2, fin2, err := readWSFrame(r)
	if err != nil || op2 != 0x0 || !fin2 || string(p2) != " world" {
		t.Fatalf("fragment 2 mismatch: op=%d fin=%v p=%q err=%v", op2, fin2, string(p2), err)
	}
}

func TestBinanceCandidateEndpointsFailover(t *testing.T) {
	eps, vision := binanceCandidateEndpoints("wss://data-stream.binance.vision/stream?streams=btcusdt@depth20")
	if len(eps) != 2 || eps[1] != "wss://stream.binance.com:9443/stream?streams=btcusdt@depth20" {
		t.Fatalf("unexpected failover endpoints for vision primary: %+v", eps)
	}
	if vision != "" {
		t.Fatalf("expected empty visionFallback when primary is vision, got %q", vision)
	}

	eps2, vision2 := binanceCandidateEndpoints("wss://stream.binance.com:9443/stream?streams=btcusdt@depth20")
	if len(eps2) != 2 || vision2 == "" || eps2[1] != vision2 {
		t.Fatalf("unexpected failover endpoints for com primary: eps=%+v vision=%q", eps2, vision2)
	}
}
