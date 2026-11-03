package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func load(t *testing.T, path string) *Config {
	t.Helper()
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
	}
	return c
}

// Both shipped profiles must load on the dev host. The production profile is
// written for a 4-core Pi, and its pin values fit any host with >= 4 CPUs.
func TestShippedConfigsLoad(t *testing.T) {
	for _, name := range []string{"dev.yaml", "production.yaml"} {
		path := filepath.Join("..", "..", "configs", name)
		c := load(t, path)
		if c.Engine.RingSegments&(c.Engine.RingSegments-1) != 0 {
			t.Fatalf("%s: ring segments not power of two", name)
		}
		t.Logf("%s: %s", name, c)
	}
}

func TestProductionPlacement(t *testing.T) {
	c := load(t, filepath.Join("..", "..", "configs", "production.yaml"))
	if c.CPUFor(RoleIngest) != 2 || c.CPUFor(RoleExec) != 3 {
		t.Fatalf("pins = %v", c.Pins)
	}
	if c.Engine.Mode != "paper" || c.Feed.Kind != "live" {
		t.Fatalf("production must be paper/live, got %s/%s", c.Engine.Mode, c.Feed.Kind)
	}
	if c.Storage.KeepFiles <= 0 {
		t.Fatalf("production storage retention is unbounded (keep_files=%d)", c.Storage.KeepFiles)
	}
	if strings.HasPrefix(c.Storage.Dir, "/mnt/nvme") {
		t.Fatalf("storage dir %q assumes an NVMe mount; the default profile must run on microSD", c.Storage.Dir)
	}
	if c.Engine.GCPercent == -1 {
		t.Fatal("production must not disable the garbage collector without measured evidence")
	}
}

func TestDefaultsFilled(t *testing.T) {
	c, err := Decode(strings.NewReader(`
engine: {mode: dev}
feed: {kind: synthetic}
symbols:
  - {name: BTC, tick_size: 0.1, start_mid: 100}
targets: {BTC: [binance:BTCUSDT]}
`), "inline")
	if err != nil {
		t.Fatal(err)
	}
	if c.Engine.RingSegments != 64 || c.Engine.RingSlotCap != 4096 || c.Feed.RateHz != 50 {
		t.Fatalf("defaults not filled: %+v %+v", c.Engine, c.Feed)
	}
	if c.Symbols[0].VenueSym != "BTCUSDT" || c.Symbols[0].LotSize != 0.001 {
		t.Fatalf("symbol defaults not filled: %+v", c.Symbols[0])
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	_, err := Decode(strings.NewReader("engine: {mode: dev, bogus: 1}\n"), "inline")
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("err = %v, want unknown-field error", err)
	}
}

func TestValidationErrors(t *testing.T) {
	base := `
engine: {mode: dev}
feed: {kind: synthetic}
symbols:
  - {name: BTC, tick_size: 0.1, start_mid: 100}
targets: {BTC: [binance:BTCUSDT]}
exec: {enabled: true, position_limit: 1, max_order_qty: 2, daily_loss_limit: 10}
`
	_, err := Decode(strings.NewReader(base), "inline")
	if err == nil || !strings.Contains(err.Error(), "max_order_qty") {
		t.Fatalf("err = %v, want max_order_qty error", err)
	}
	if _, err := Decode(strings.NewReader(strings.Replace(base, "enabled: true, position_limit: 1, max_order_qty: 2", "enabled: true, position_limit: 2, max_order_qty: 1", 1)), "inline"); err != nil {
		t.Fatalf("valid exec rejected: %v", err)
	}
	if _, err := Decode(strings.NewReader("engine: {mode: real}\n"), "inline"); err == nil {
		t.Fatal("bad mode accepted")
	}
}

func TestSymbolIDsStable(t *testing.T) {
	if SymbolID("BTC") == SymbolID("ETH") {
		t.Fatal("symbol hash collision")
	}
	if SymbolID("BTC") != SymbolID("BTC") {
		t.Fatal("symbol hash unstable")
	}
	if VenueID("BINANCE") != 1 || VenueID("binance") != 1 {
		t.Fatal("venue ids unstable")
	}
	c := &Config{Symbols: []Symbol{{Name: "BTC"}}}
	if _, ok := c.SymbolByID(SymbolID("BTC")); !ok {
		t.Fatal("SymbolByID miss")
	}
	if _, ok := c.SymbolByID(SymbolID("XRP")); ok {
		t.Fatal("SymbolByID hit for unknown")
	}
}

func TestDuration(t *testing.T) {
	if got := Duration("", 3*time.Second); got != 3*time.Second {
		t.Fatalf("Duration empty = %v", got)
	}
	if got := Duration("250ms", time.Second); got != 250*time.Millisecond {
		t.Fatalf("Duration = %v", got)
	}
	if _, err := os.Stat(filepath.Join("..", "..", "configs", "dev.yaml")); err != nil {
		t.Fatal(err)
	}
}
