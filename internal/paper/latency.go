package paper

import (
	"context"
	"net"
	"net/url"
	"sort"
	"sync"
	"time"
)

// LatencySnapshot is the measured network latency to the trading venue.
type LatencySnapshot struct {
	Host        string    `json:"host"`
	LastMs      float64   `json:"last_ms"`
	P50Ms       float64   `json:"p50_ms"`
	P90Ms       float64   `json:"p90_ms"`
	Samples     int       `json:"samples"`
	Failures    int       `json:"consecutive_failures"`
	UpdatedAt   time.Time `json:"updated_at"`
	EffectiveMs float64   `json:"effective_order_latency_ms"` // what the engine uses for order/cancel timing
	OrderHost   string    `json:"order_host,omitempty"`
	OrderP90Ms  float64   `json:"order_p90_ms,omitempty"`
	Mode        string    `json:"strategy_mode,omitempty"`    // latency regime chosen by the orchestrator
}

// latencyWindow is the number of recent probes kept for percentiles.
const latencyWindow = 20

// failedProbeMs is the latency assumed when the venue cannot be reached.
const failedProbeMs = 500.0

// LatencyMonitor measures round-trip latency to the venue by timing TCP handshakes
// (one handshake = one network round trip) at a fixed interval.
type LatencyMonitor struct {
	addr     string
	interval time.Duration
	probe    func(ctx context.Context, addr string) (time.Duration, error)

	mu       sync.Mutex
	samples  []float64
	last     float64
	failures int
	updated  time.Time
}

// NewLatencyMonitor builds a monitor for the host of a ws(s)/http(s) endpoint URL.
func NewLatencyMonitor(endpoint string, interval time.Duration) *LatencyMonitor {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &LatencyMonitor{addr: endpointAddr(endpoint), interval: interval, probe: tcpHandshake}
}

func endpointAddr(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return endpoint
	}
	if u.Port() != "" {
		return u.Host
	}
	switch u.Scheme {
	case "ws", "http":
		return net.JoinHostPort(u.Hostname(), "80")
	default:
		return net.JoinHostPort(u.Hostname(), "443")
	}
}

func tcpHandshake(ctx context.Context, addr string) (time.Duration, error) {
	d := net.Dialer{Timeout: 3 * time.Second}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return 0, err
	}
	elapsed := time.Since(start)
	_ = conn.Close()
	return elapsed, nil
}

// Start probes immediately and then every interval until ctx is done.
func (m *LatencyMonitor) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(m.interval)
		defer t.Stop()
		for {
			m.probeOnce(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (m *LatencyMonitor) probeOnce(ctx context.Context) {
	d, err := m.probe(ctx, m.addr)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		m.failures++
		m.mu.Unlock()
		if m.failuresNow() >= 3 {
			m.Record(failedProbeMs) // venue unreachable: treat as high latency
		}
		return
	}
	m.mu.Lock()
	m.failures = 0
	m.mu.Unlock()
	m.Record(float64(d.Microseconds()) / 1000.0)
}

func (m *LatencyMonitor) failuresNow() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.failures
}

// Record adds one latency sample in milliseconds.
func (m *LatencyMonitor) Record(ms float64) {
	if ms <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.samples = append(m.samples, ms)
	if len(m.samples) > latencyWindow {
		m.samples = m.samples[len(m.samples)-latencyWindow:]
	}
	m.last = ms
	m.updated = time.Now()
}

// Snapshot returns current latency statistics.
func (m *LatencyMonitor) Snapshot() LatencySnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := LatencySnapshot{Host: m.addr, LastMs: m.last, Samples: len(m.samples), Failures: m.failures, UpdatedAt: m.updated}
	if len(m.samples) == 0 {
		return s
	}
	sorted := append([]float64(nil), m.samples...)
	sort.Float64s(sorted)
	s.P50Ms = percentile(sorted, 0.50)
	s.P90Ms = percentile(sorted, 0.90)
	return s
}

func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(q*float64(len(sorted)-1) + 0.5)
	return sorted[idx]
}

// orderProcessingMs is exchange matching + local processing added on top of network RTT.
const orderProcessingMs = 5.0

// effectiveOrderLatency returns the reaction latency the simulator should use: market data
// travels one way from the feed host and the order travels one way to the order host, so
// it is half of each measured p90 RTT plus processing. With only one monitor, that
// monitor's full RTT is used. Falls back to the configured value without measurements.
func (e *DryRunEngine) effectiveOrderLatency() time.Duration {
	feedMs, orderMs := 0.0, 0.0
	if e.latencyMon != nil {
		if s := e.latencyMon.Snapshot(); s.Samples > 0 {
			feedMs = s.P90Ms
		}
	}
	if e.orderLatMon != nil {
		if s := e.orderLatMon.Snapshot(); s.Samples > 0 {
			orderMs = s.P90Ms
		}
	}
	switch {
	case feedMs > 0 && orderMs > 0:
		return time.Duration((feedMs/2 + orderMs/2 + orderProcessingMs) * float64(time.Millisecond))
	case feedMs > 0:
		return time.Duration((feedMs + orderProcessingMs) * float64(time.Millisecond))
	case orderMs > 0:
		return time.Duration((orderMs + orderProcessingMs) * float64(time.Millisecond))
	}
	if e.cfg.OrderLatency > 0 {
		return e.cfg.OrderLatency
	}
	return 120 * time.Millisecond
}

// maxCombinedConvictionAdd caps latency-regime plus goal-controller threshold tightening.
const maxCombinedConvictionAdd = 0.10

// SetLatencyMonitor attaches a live venue latency monitor. Call it before Start:
// the hot path reads the monitor pointer without the engine lock.
func (e *DryRunEngine) SetLatencyMonitor(m *LatencyMonitor) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.latencyMon = m
}

// SetOrderLatencyMonitor attaches a probe of the order-entry host (call before Start).
func (e *DryRunEngine) SetOrderLatencyMonitor(m *LatencyMonitor) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.orderLatMon = m
}

// latencyProfile adapts entries to the latency regime: the slower our orders reach the
// venue, the less we compete at the touch (HFT) and the more we rely on slower,
// higher-conviction signals with wider entries and targets (MFT/LFT).
type latencyProfile struct {
	EntryOffsetMult float64
	ConvictionAdd   float64
	TargetMult      float64
}

func profileForMode(mode string) latencyProfile {
	switch mode {
	case "LOW_LATENCY_MAKER": // HFT: queue-reactive quoting at the touch
		return latencyProfile{EntryOffsetMult: 1.0, ConvictionAdd: 0, TargetMult: 1.0}
	case "HIGH_LATENCY_FUNDING_CARRY": // LFT: stale quotes get picked off; trade only strong, slow signals
		return latencyProfile{EntryOffsetMult: 2.0, ConvictionAdd: 0.06, TargetMult: 1.25}
	default: // MFT: multi-second to multi-minute signals
		return latencyProfile{EntryOffsetMult: 1.3, ConvictionAdd: 0.02, TargetMult: 1.0}
	}
}
