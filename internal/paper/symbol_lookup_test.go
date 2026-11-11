package paper

import (
	"testing"

	"piquant-live/internal/engine"
)

func TestSymbolForID_ReverseLookupTracksNewSymbols(t *testing.T) {
	ring := engine.NewSPSCRing(64)
	m := NewMarketStreamIngestor(StreamConfig{Symbols: []string{"btcusdt", "ETHUSDT"}}, ring)
	if got := m.SymbolForID(0); got != "BTCUSDT" {
		t.Fatalf("id 0 = %q", got)
	}
	if got := m.SymbolForID(1); got != "ETHUSDT" {
		t.Fatalf("id 1 = %q", got)
	}
	if got := m.SymbolForID(9); got != "" {
		t.Fatalf("unknown id = %q", got)
	}
	m.handleStreamPayload([]byte(`{"stream":"solusdt@trade","data":{"e":"trade","E":1,"s":"SOLUSDT","t":1,"p":"100.0","q":"1.0","T":1,"m":false,"M":true}}`))
	if got := m.SymbolForID(2); got != "SOLUSDT" {
		t.Fatalf("dynamically added symbol id 2 = %q", got)
	}
}
