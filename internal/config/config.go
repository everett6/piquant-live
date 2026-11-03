// Package config loads and validates the engine's YAML settings. Unknown keys
// are errors: a typo in a production config must fail at startup, not silently
// fall back to a default that looks correct.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"piquant-live/internal/system"
)

// DefaultPath is where the engine looks for its config when no flag is given.
const DefaultPath = "configs/production.yaml"

// Roles are the CPU placement roles understood by the engine.
const (
	RoleAny    = "any"
	RoleIngest = "ingest"
	RoleInfer  = "infer"
	RoleExec   = "exec"
	RoleHouse  = "house"
)

// Config is the root document.
type Config struct {
	Engine  Engine              `yaml:"engine"`
	Feed    Feed                `yaml:"feed"`
	Symbols []Symbol            `yaml:"symbols"`
	Targets map[string][]string `yaml:"targets"` // engine symbol -> venue symbols
	Pins    map[string]int      `yaml:"pins"`    // role -> CPU index, or -1 to leave free
	Storage Storage             `yaml:"storage"`
	Models  Models              `yaml:"models"`
	Exec    Exec                `yaml:"exec"`
	Path    string              `yaml:"-"` // file the config came from
}

// Engine holds process-wide settings.
type Engine struct {
	Mode            string `yaml:"mode"` // dev | paper
	Name            string `yaml:"name"`
	GOMAXPROCS      int    `yaml:"gomaxprocs"`        // 0 = runtime default
	GOMEMLIMITMiB   int    `yaml:"gomemlimit_mib"`    // 0 = no limit
	GCPercent       int    `yaml:"gc_percent"`        // -1 = GC off (GOGC=off)
	RealtimePrio    int    `yaml:"realtime_priority"` // 0 = do not request SCHED_FIFO
	LockThreads     bool   `yaml:"lock_threads"`
	Watchdog        bool   `yaml:"watchdog"`
	WatchdogKickMs  int    `yaml:"watchdog_kick_ms"`
	RingSegments    int    `yaml:"ring_segments"`
	RingSlotCap     int    `yaml:"ring_slot_cap"` // events per segment
	MaxTickEventsPs int    `yaml:"max_tick_events_ps"`
}

// Feed selects and sizes the market data source.
type Feed struct {
	Kind      string   `yaml:"kind"` // synthetic | live
	Venue     string   `yaml:"venue"`
	Durations []string `yaml:"durations"` // live: subscription channels
	RateHz    float64  `yaml:"rate_hz"`   // synthetic: book events per second
	Seed      int64    `yaml:"seed"`
	WsURL     string   `yaml:"ws_url"` // live: overrides the venue default
	File      string   `yaml:"file"`   // synthetic: replay this capture instead of generating
}

// Symbol is one tradable instrument and its tick/lot grid.
type Symbol struct {
	Name     string  `yaml:"name"`
	Venue    string  `yaml:"venue"`
	VenueSym string  `yaml:"venue_symbol"`
	TickSize float64 `yaml:"tick_size"`
	LotSize  float64 `yaml:"lot_size"`
	StartMid float64 `yaml:"start_mid"` // synthetic feed starting price
}

// Storage sizes the persistence writer.
type Storage struct {
	Enabled       bool   `yaml:"enabled"`
	Dir           string `yaml:"dir"`
	FlushInterval string `yaml:"flush_interval"`
	MaxFileBytes  int64  `yaml:"max_file_mb"` // 0 = 64 MiB per file
	KeepFiles     int    `yaml:"keep_files"`  // 0 = keep all
}

// Models sizes the model engines.
type Models struct {
	Enabled       bool    `yaml:"enabled"`
	MaxFeatures   int     `yaml:"max_features"`
	MaxHidden     int     `yaml:"max_hidden"`
	Path          string  `yaml:"path"` // weights file (JSON); optional
	BuyThreshold  float64 `yaml:"buy_threshold"`
	SellThreshold float64 `yaml:"sell_threshold"`
}

// Exec holds the paper execution and risk settings.
type Exec struct {
	Enabled        bool    `yaml:"enabled"`
	PositionLimit  float64 `yaml:"position_limit"` // base units, absolute
	MaxOrderQty    float64 `yaml:"max_order_qty"`  // base units per order
	TakerFeeBps    float64 `yaml:"taker_fee_bps"`
	MakerFeeBps    float64 `yaml:"maker_fee_bps"`
	SlippageBps    float64 `yaml:"slippage_bps"`
	DailyLossLimit float64 `yaml:"daily_loss_limit"` // quote units, absolute
	KillLatencyMs  int     `yaml:"stale_latency_ms"`
	CooldownMs     int     `yaml:"cooldown_ms"`
}

// Validate checks the config for internal consistency and fills defaults.
func (c *Config) Validate() error {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

	switch c.Engine.Mode {
	case "dev", "paper":
	default:
		add("engine.mode must be dev or paper, got %q", c.Engine.Mode)
	}
	if c.Engine.GOMAXPROCS < 0 {
		add("engine.gomaxprocs must be >= 0")
	}
	if c.Engine.RingSegments == 0 {
		c.Engine.RingSegments = 64
	}
	if c.Engine.RingSlotCap == 0 {
		c.Engine.RingSlotCap = 4096
	}
	if c.Engine.RingSegments&(c.Engine.RingSegments-1) != 0 {
		add("engine.ring_segments must be a power of two, got %d", c.Engine.RingSegments)
	}
	if c.Engine.RingSlotCap&(c.Engine.RingSlotCap-1) != 0 {
		add("engine.ring_slot_cap must be a power of two, got %d", c.Engine.RingSlotCap)
	}
	if c.Engine.WatchdogKickMs == 0 {
		c.Engine.WatchdogKickMs = 500
	}
	switch c.Feed.Kind {
	case "synthetic", "live":
	default:
		add("feed.kind must be synthetic or live, got %q", c.Feed.Kind)
	}
	if c.Feed.Kind == "synthetic" && c.Feed.RateHz <= 0 {
		c.Feed.RateHz = 50
	}
	for role, cpu := range c.Pins {
		switch role {
		case RoleAny, RoleIngest, RoleInfer, RoleExec, RoleHouse:
		default:
			add("pins: unknown role %q", role)
		}
		if cpu >= system.CPUS() && cpu >= 0 {
			add("pins.%s = %d but the host has %d CPUs", role, cpu, system.CPUS())
		}
	}
	if len(c.Symbols) == 0 {
		add("symbols: at least one symbol is required")
	}
	seen := map[uint16]bool{}
	for i := range c.Symbols {
		s := &c.Symbols[i]
		if s.Name == "" {
			add("symbols[%d]: name is required", i)
		}
		if len(s.Name) > 16 {
			add("symbols[%d]: name %q longer than 16 bytes", i, s.Name)
		}
		if seen[SymbolID(s.Name)] {
			add("symbols[%d]: duplicate symbol %q", i, s.Name)
		}
		seen[SymbolID(s.Name)] = true
		if s.VenueSym == "" {
			s.VenueSym = strings.ToUpper(s.Name) + "USDT"
		}
		if s.TickSize <= 0 {
			add("symbols[%d].%s: tick_size must be > 0", i, s.Name)
		}
		if s.LotSize <= 0 {
			s.LotSize = 0.001
		}
		if c.Feed.Kind == "synthetic" && s.StartMid <= 0 {
			add("symbols[%d].%s: start_mid is required for the synthetic feed", i, s.Name)
		}
		if vs := c.Targets[s.Name]; len(vs) == 0 {
			add("targets[%s]: at least one venue symbol is required", s.Name)
		}
	}
	for name := range c.Targets {
		if !seen[SymbolID(name)] {
			add("targets[%s]: no such symbol", name)
		}
	}
	if c.Storage.Enabled {
		if c.Storage.Dir == "" {
			add("storage.dir is required when storage is enabled")
		}
		if c.Storage.FlushInterval == "" {
			c.Storage.FlushInterval = "1s"
		}
		if _, err := time.ParseDuration(c.Storage.FlushInterval); err != nil {
			add("storage.flush_interval: %v", err)
		}
		if c.Storage.MaxFileBytes == 0 {
			c.Storage.MaxFileBytes = 64 << 20
		}
	}
	if c.Models.Enabled {
		if c.Models.MaxFeatures <= 0 || c.Models.MaxFeatures > 256 {
			add("models.max_features must be in 1..256")
		}
		if c.Models.MaxHidden <= 0 || c.Models.MaxHidden > 128 {
			add("models.max_hidden must be in 1..128")
		}
		if c.Models.BuyThreshold == 0 {
			c.Models.BuyThreshold = 0.6
		}
		if c.Models.SellThreshold == 0 {
			c.Models.SellThreshold = 0.6
		}
		if c.Models.BuyThreshold <= 0 || c.Models.BuyThreshold > 1 ||
			c.Models.SellThreshold <= 0 || c.Models.SellThreshold > 1 {
			add("models.buy_threshold and sell_threshold must be in (0,1]")
		}
	}
	if c.Exec.Enabled {
		if c.Exec.PositionLimit <= 0 || c.Exec.MaxOrderQty <= 0 {
			add("exec.position_limit and exec.max_order_qty must be > 0")
		}
		if c.Exec.MaxOrderQty > c.Exec.PositionLimit {
			add("exec.max_order_qty must not exceed exec.position_limit")
		}
		if c.Exec.TakerFeeBps < 0 || c.Exec.MakerFeeBps < 0 || c.Exec.SlippageBps < 0 {
			add("exec fee/slippage parameters must be >= 0")
		}
		if c.Exec.DailyLossLimit <= 0 {
			add("exec.daily_loss_limit must be > 0")
		}
		if c.Exec.CooldownMs == 0 {
			c.Exec.CooldownMs = 250
		}
		if c.Exec.KillLatencyMs == 0 {
			c.Exec.KillLatencyMs = 2000
		}
	}
	if len(errs) > 0 {
		return errors.New("config: " + strings.Join(errs, "; "))
	}
	return nil
}

// CPUFor returns the CPU index for role, or -1 when unset/free.
func (c *Config) CPUFor(role string) int {
	if cpu, ok := c.Pins[role]; ok {
		return cpu
	}
	return -1
}

// SymbolByID returns the symbol for id, if configured.
func (c *Config) SymbolByID(id uint16) (Symbol, bool) {
	for _, s := range c.Symbols {
		if SymbolID(s.Name) == id {
			return s, true
		}
	}
	return Symbol{}, false
}

// SymbolID maps a symbol name to the 16-bit id carried in events (FNV-1a).
func SymbolID(name string) uint16 {
	const (
		offset = 2166136261
		prime  = 16777619
	)
	h := uint32(offset)
	for i := 0; i < len(name); i++ {
		h ^= uint32(name[i])
		h *= prime
	}
	return uint16(h ^ h>>16)
}

// VenueID maps a venue name to the 16-bit id carried in events.
func VenueID(name string) uint16 {
	// Reserved ids keep the common venues stable across renames.
	switch strings.ToLower(name) {
	case "binance":
		return 1
	case "coinbase":
		return 2
	case "kraken":
		return 3
	case "synthetic":
		return 4
	}
	return SymbolID(name)
}

// Load reads, decodes and validates a YAML config file.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Decode(f, path)
}

// Decode parses and validates a config document; name labels errors.
func Decode(r io.Reader, name string) (*Config, error) {
	c := &Config{Path: name}
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if c.Engine.Name == "" {
		c.Engine.Name = "piquant-live"
	}
	return c, nil
}

// Duration parses d, returning def when d is empty.
func Duration(d string, def time.Duration) time.Duration {
	v, err := time.ParseDuration(d)
	if err != nil {
		return def
	}
	return v
}

// String renders the effective settings for the startup log.
func (c *Config) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "mode=%s feed=%s symbols=%d pins=", c.Engine.Mode, c.Feed.Kind, len(c.Symbols))
	for _, r := range []string{RoleIngest, RoleInfer, RoleExec} {
		b.WriteString(r)
		b.WriteByte('=')
		b.WriteString(strconv.Itoa(c.CPUFor(r)))
		b.WriteByte(' ')
	}
	fmt.Fprintf(&b, "ring=%dx%d storage=%v exec=%v models=%v",
		c.Engine.RingSegments, c.Engine.RingSlotCap, c.Storage.Enabled, c.Exec.Enabled, c.Models.Enabled)
	return b.String()
}
