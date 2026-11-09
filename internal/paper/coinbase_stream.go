package paper

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"piquant-live/internal/engine"
	"piquant-live/internal/marketdata"
)

// VenueCoinbase operational flag for MarketEvent
const VenueCoinbase uint32 = 1 << 3

// CoinbaseStreamConfig configures the public Coinbase Exchange WebSocket ticker ingestor.
type CoinbaseStreamConfig struct {
	// Endpoint is the public Coinbase WebSocket URL.
	// Default: wss://ws-feed.exchange.coinbase.com (no credentials required)
	Endpoint string

	// Products to track in Coinbase format, e.g. ["BTC-USD", "ETH-USD"]
	Products []string

	// ReconnectBase is the initial backoff duration (default: 500ms)
	ReconnectBase time.Duration

	// ReconnectMax is the max exponential backoff (default: 15s)
	ReconnectMax time.Duration

	// PingInterval is the keepalive ping interval (default: 20s)
	PingInterval time.Duration
}

// DefaultCoinbaseStreamConfig returns institutional defaults for Coinbase Exchange feeds.
func DefaultCoinbaseStreamConfig() CoinbaseStreamConfig {
	return CoinbaseStreamConfig{
		Endpoint:      "wss://ws-feed.exchange.coinbase.com",
		Products:      []string{"BTC-USD", "ETH-USD"},
		ReconnectBase: 500 * time.Millisecond,
		ReconnectMax:  15 * time.Second,
		PingInterval:  20 * time.Second,
	}
}

// CoinbasePricePoint stores a historical price sample for rolling velocity estimation.
type CoinbasePricePoint struct {
	Price float64
	Time  time.Time
}

// CoinbaseSymbolSignal stores real-time institutional price discovery and lead-lag metrics.
type CoinbaseSymbolSignal struct {
	Symbol          string    `json:"symbol"`
	CoinbasePrice   float64   `json:"coinbase_price"`
	BestBid         float64   `json:"best_bid"`
	BestAsk         float64   `json:"best_ask"`
	MidPrice        float64   `json:"mid_price"`
	BasisBps        float64   `json:"basis_bps"`
	Velocity1sBps   float64   `json:"velocity_1s_bps"`
	LeadLagSkewBps  float64   `json:"lead_lag_skew_bps"`
	IsAdverseAsk    bool      `json:"is_adverse_ask"`
	IsAdverseBid    bool      `json:"is_adverse_bid"`
	LastUpdate      time.Time `json:"last_update"`
	TradesCount     uint64    `json:"trades_count"`
	TotalVolumeUSDT float64   `json:"total_volume_usdt"`

	history [16]CoinbasePricePoint
	histIdx int
	histLen int
}

// CoinbaseSignalState reports real-time telemetry of the Coinbase oracle.
type CoinbaseSignalState struct {
	Connected   bool                             `json:"connected"`
	LastEventNs int64                            `json:"last_event_ns"`
	Symbols     map[string]*CoinbaseSymbolSignal `json:"symbols"`
}

// CoinbaseSignalOracle maintains institutional USD price discovery signals from Coinbase
// and provides lead-lag skews to GLFT market making models on Binance/Bybit.
type CoinbaseSignalOracle struct {
	mu        sync.RWMutex
	signals   map[string]*CoinbaseSymbolSignal
	connected atomic.Bool
	lastNs    atomic.Int64
}

// NewCoinbaseSignalOracle creates a new oracle instance for the given symbols.
func NewCoinbaseSignalOracle(symbols []string) *CoinbaseSignalOracle {
	sigs := make(map[string]*CoinbaseSymbolSignal, len(symbols))
	for _, sym := range symbols {
		norm := NormalizeToUniverseSymbol(sym)
		sigs[norm] = &CoinbaseSymbolSignal{
			Symbol: norm,
		}
	}
	return &CoinbaseSignalOracle{
		signals: sigs,
	}
}

// extractBaseCurrency extracts the underlying asset symbol (e.g. BTC from BTC-USD, BTCUSDC, BTCUSDT).
func extractBaseCurrency(s string) string {
	u := strings.ToUpper(s)
	u = strings.ReplaceAll(u, "-", "")
	u = strings.ReplaceAll(u, "_", "")
	for _, quote := range []string{"USDC", "USDT", "USD", "BUSD", "FDUSD"} {
		if strings.HasSuffix(u, quote) {
			return strings.TrimSuffix(u, quote)
		}
	}
	return u
}

// UpdateTick records a new ticker tick from Coinbase.
func (o *CoinbaseSignalOracle) UpdateTick(normSymbol string, price, bestBid, bestAsk, size float64, isBuy bool, now time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()

	base := extractBaseCurrency(normSymbol)
	keySet := make(map[string]struct{}, 4)
	keySet[normSymbol] = struct{}{}
	if base != normSymbol && base != "" {
		keySet[base] = struct{}{}
	}
	// Also update any registered universe symbol that shares this base asset
	for registeredSym := range o.signals {
		if extractBaseCurrency(registeredSym) == base {
			keySet[registeredSym] = struct{}{}
		}
	}

	for k := range keySet {
		sig, ok := o.signals[k]
		if !ok {
			sig = &CoinbaseSymbolSignal{Symbol: k}
			o.signals[k] = sig
		}

		sig.CoinbasePrice = price
		sig.BestBid = bestBid
		sig.BestAsk = bestAsk
		if bestBid > 0 && bestAsk > 0 {
			sig.MidPrice = (bestBid + bestAsk) / 2.0
		} else if price > 0 {
			sig.MidPrice = price
		}
		sig.LastUpdate = now
		sig.TradesCount++
		if size > 0 && price > 0 {
			sig.TotalVolumeUSDT += size * price
		}

		// Update circular history buffer
		idx := sig.histIdx
		sig.history[idx] = CoinbasePricePoint{Price: sig.MidPrice, Time: now}
		sig.histIdx = (idx + 1) % len(sig.history)
		if sig.histLen < len(sig.history) {
			sig.histLen++
		}

		// Compute rolling velocity over ~1 second window
		var oldest *CoinbasePricePoint
		cutoff := now.Add(-1500 * time.Millisecond)
		for i := 0; i < sig.histLen; i++ {
			pIdx := (sig.histIdx - 1 - i + len(sig.history)) % len(sig.history)
			pt := &sig.history[pIdx]
			if pt.Time.Before(cutoff) {
				break
			}
			oldest = pt
		}

		if oldest != nil && !oldest.Time.Equal(now) {
			dt := now.Sub(oldest.Time).Seconds()
			if dt >= 0.20 && oldest.Price > 0 {
				retBps := (sig.MidPrice - oldest.Price) / oldest.Price * 1e4
				sig.Velocity1sBps = retBps / dt
			}
		}
	}

	o.lastNs.Store(now.UnixNano())
}

// EvaluateAgainstBinance evaluates the Coinbase lead-lag signal given Binance's current mid price.
func (o *CoinbaseSignalOracle) EvaluateAgainstBinance(normSymbol string, binanceMid float64, now time.Time) CoinbaseSymbolSignal {
	if math.IsNaN(binanceMid) || math.IsInf(binanceMid, 0) || binanceMid <= 0 {
		return CoinbaseSymbolSignal{Symbol: normSymbol}
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	sig, ok := o.signals[normSymbol]
	if !ok || sig.MidPrice <= 0 {
		// Multi-quote fallback: try base currency or equivalent quote pair
		base := extractBaseCurrency(normSymbol)
		for _, cand := range []string{base, base + "USD", base + "USDT", base + "USDC"} {
			if s, found := o.signals[cand]; found && s.MidPrice > 0 {
				sig = s
				break
			}
		}
	}
	if sig == nil || sig.MidPrice <= 0 {
		return CoinbaseSymbolSignal{Symbol: normSymbol}
	}

	// Stale signal protection: if no updates for > 5 seconds, clear skew
	if now.Sub(sig.LastUpdate) > 5*time.Second {
		sig.BasisBps = 0
		sig.Velocity1sBps = 0
		sig.LeadLagSkewBps = 0
		sig.IsAdverseAsk = false
		sig.IsAdverseBid = false
		return *sig
	}

	// Compute Basis in basis points: (CoinbaseMid - BinanceMid) / BinanceMid * 10,000
	sig.BasisBps = (sig.MidPrice - binanceMid) / binanceMid * 1e4

	// Lead-Lag Skew Calculation:
	// When Coinbase leads upwards (positive basis and positive velocity),
	// shift GLFT reservation price upward (positive skew).
	// When Coinbase leads downwards, shift reservation downward.
	var rawSkew float64
	if (sig.BasisBps > 0 && sig.Velocity1sBps > 0) || (sig.BasisBps < 0 && sig.Velocity1sBps < 0) {
		// Concordant momentum: strong lead-lag conviction
		rawSkew = 0.40*sig.BasisBps + 0.30*sig.Velocity1sBps
	} else {
		// Discordant: damp toward zero
		rawSkew = 0.20 * sig.BasisBps
	}

	// Clamp skew to [-2.5, +2.5] bps
	if rawSkew > 2.5 {
		rawSkew = 2.5
	} else if rawSkew < -2.5 {
		rawSkew = -2.5
	}
	sig.LeadLagSkewBps = rawSkew

	// Detect toxic adverse flow from US institutional aggressive buying/selling
	sig.IsAdverseAsk = sig.Velocity1sBps > 3.0 || sig.BasisBps > 5.0  // Surging UP: shorting is dangerous
	sig.IsAdverseBid = sig.Velocity1sBps < -3.0 || sig.BasisBps < -5.0 // Dumping DOWN: buying is dangerous

	return *sig
}

// Snapshot returns a thread-safe snapshot of the entire Coinbase signal oracle.
func (o *CoinbaseSignalOracle) Snapshot() CoinbaseSignalState {
	o.mu.RLock()
	defer o.mu.RUnlock()

	res := CoinbaseSignalState{
		Connected:   o.connected.Load(),
		LastEventNs: o.lastNs.Load(),
		Symbols:     make(map[string]*CoinbaseSymbolSignal, len(o.signals)),
	}
	for k, v := range o.signals {
		cp := *v
		res.Symbols[k] = &cp
	}
	return res
}

// IsConnected returns whether the Coinbase feed is actively receiving frames.
func (o *CoinbaseSignalOracle) IsConnected() bool {
	return o.connected.Load()
}

// CoinbaseStreamIngestor manages the real-time WebSocket connection to Coinbase Exchange.
type CoinbaseStreamIngestor struct {
	cfg    CoinbaseStreamConfig
	ring   *engine.SPSCRing
	oracle *CoinbaseSignalOracle

	stats StreamStats

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewCoinbaseStreamIngestor instantiates the Coinbase WebSocket ingestor.
func NewCoinbaseStreamIngestor(cfg CoinbaseStreamConfig, ring *engine.SPSCRing, oracle *CoinbaseSignalOracle) *CoinbaseStreamIngestor {
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultCoinbaseStreamConfig().Endpoint
	}
	if len(cfg.Products) == 0 {
		cfg.Products = []string{"BTC-USD", "ETH-USD"}
	}
	if cfg.ReconnectBase <= 0 {
		cfg.ReconnectBase = 500 * time.Millisecond
	}
	if cfg.ReconnectMax <= 0 {
		cfg.ReconnectMax = 15 * time.Second
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = 20 * time.Second
	}

	if oracle == nil {
		oracle = NewCoinbaseSignalOracle([]string{"BTCUSDT", "ETHUSDT"})
	}

	return &CoinbaseStreamIngestor{
		cfg:    cfg,
		ring:   ring,
		oracle: oracle,
	}
}

// Oracle returns the underlying CoinbaseSignalOracle.
func (c *CoinbaseStreamIngestor) Oracle() *CoinbaseSignalOracle {
	return c.oracle
}

// Start launches the background connection loop.
func (c *CoinbaseStreamIngestor) Start(ctx context.Context) {
	cCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	c.wg.Add(1)
	go c.runLoop(cCtx)
}

// Stop shuts down the ingestor.
func (c *CoinbaseStreamIngestor) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
}

// IsConnected returns true if connected.
func (c *CoinbaseStreamIngestor) IsConnected() bool {
	return c.oracle.IsConnected()
}

// Stats returns connection metrics.
func (c *CoinbaseStreamIngestor) Stats() (total, depths, trades, gaps, reconnects, fallbacks uint64, lastNs int64) {
	return c.stats.TotalEvents.Load(),
		c.stats.DepthUpdates.Load(),
		c.stats.Trades.Load(),
		c.stats.GapsDetected.Load(),
		c.stats.Reconnects.Load(),
		c.stats.FallbackTicks.Load(),
		c.stats.LastEventNs.Load()
}

func (c *CoinbaseStreamIngestor) runLoop(ctx context.Context) {
	defer c.wg.Done()

	backoff := c.cfg.ReconnectBase
	for {
		select {
		case <-ctx.Done():
			c.oracle.connected.Store(false)
			return
		default:
		}

		err := c.connectAndStream(ctx)
		c.oracle.connected.Store(false)
		if ctx.Err() != nil {
			return
		}

		c.stats.Reconnects.Add(1)
		if err != nil {
			time.Sleep(backoff)
			backoff = time.Duration(math.Min(float64(backoff*2), float64(c.cfg.ReconnectMax)))
		} else {
			backoff = c.cfg.ReconnectBase
		}
	}
}

func (c *CoinbaseStreamIngestor) connectAndStream(ctx context.Context) error {
	u, err := url.Parse(c.cfg.Endpoint)
	if err != nil {
		return fmt.Errorf("invalid Coinbase URL %s: %w", c.cfg.Endpoint, err)
	}

	host := u.Host
	if !strings.Contains(host, ":") {
		if u.Scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	d := net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 10 * time.Second,
	}
	dialCtx, dialCancel := context.WithTimeout(ctx, 8*time.Second)
	defer dialCancel()

	rawConn, err := d.DialContext(dialCtx, "tcp", host)
	if err != nil {
		return fmt.Errorf("Coinbase dial failed to %s: %w", host, err)
	}

	var conn net.Conn = rawConn
	if u.Scheme == "wss" {
		tlsConn := tls.Client(rawConn, &tls.Config{
			ServerName: strings.Split(u.Host, ":")[0],
		})
		if err := tlsConn.HandshakeContext(dialCtx); err != nil {
			_ = rawConn.Close()
			return fmt.Errorf("Coinbase tls handshake failed to %s: %w", host, err)
		}
		conn = tlsConn
	}
	defer conn.Close()
	defer c.oracle.connected.Store(false)

	stopWatcher := make(chan struct{})
	defer close(stopWatcher)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopWatcher:
		}
	}()

	// RFC 6455 Handshake
	wsKey := make([]byte, 16)
	_, _ = rand.Read(wsKey)
	keyB64 := base64.StdEncoding.EncodeToString(wsKey)

	path := u.Path
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}

	req := fmt.Sprintf("GET %s HTTP/1.1\r\n"+
		"Host: %s\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\n"+
		"Sec-WebSocket-Version: 13\r\n\r\n", path, u.Host, keyB64)

	if _, err := conn.Write([]byte(req)); err != nil {
		return fmt.Errorf("Coinbase handshake write failed: %w", err)
	}

	br := bufio.NewReaderSize(conn, 64*1024)
	respStatus, err := br.ReadString('\n')
	if err != nil {
		return fmt.Errorf("Coinbase handshake read status failed: %w", err)
	}
	if !strings.Contains(respStatus, "101") {
		return fmt.Errorf("unexpected Coinbase handshake response: %s", strings.TrimSpace(respStatus))
	}

	// Consume HTTP headers
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return fmt.Errorf("Coinbase handshake header read failed: %w", err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}

	// Subscribe to ticker channel for products
	quotedProds := make([]string, len(c.cfg.Products))
	for i, p := range c.cfg.Products {
		quotedProds[i] = fmt.Sprintf(`"%s"`, p)
	}
	subMsg := fmt.Sprintf(`{"type":"subscribe","product_ids":[%s],"channels":["ticker"]}`, strings.Join(quotedProds, ","))
	if err := sendWSText(conn, subMsg); err != nil {
		return fmt.Errorf("Coinbase subscription failed: %w", err)
	}

	c.oracle.connected.Store(true)

	// Keepalive ping loop
	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()

	go func() {
		ticker := time.NewTicker(c.cfg.PingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-ticker.C:
				// Coinbase RFC 6455 ping with bounded write deadline
				pingFrame := []byte{0x89, 0x80, 0x11, 0x22, 0x33, 0x44}
				_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
				if _, err := conn.Write(pingFrame); err != nil {
					return
				}
			}
		}
	}()

	// Frame read loop
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		payload, op, err := readWSFrame(br)
		if err != nil {
			return err
		}

		if op == 0x8 {
			return io.EOF
		}
		if op == 0x9 { // Ping from Coinbase
			_ = sendWSPong(conn, payload)
			continue
		}
		if op == 0xA { // Pong in reply to our ping
			continue
		}
		if op != 0x1 && op != 0x2 {
			continue
		}

		c.HandleStreamPayload(payload)
	}
}

// HandleStreamPayload processes raw Coinbase WebSocket payload bytes with zero heap allocation.
func (c *CoinbaseStreamIngestor) HandleStreamPayload(payload []byte) {
	now := time.Now()
	nowNs := now.UnixNano()
	c.stats.LastEventNs.Store(nowNs)

	// Quick check for ticker frame
	if !bytes.Contains(payload, []byte(`"type":"ticker"`)) {
		return
	}

	symbol, price, bestBid, bestAsk, lastSize, isBuy, ok := FastParseCoinbaseTicker(payload)
	if !ok || price <= 0 {
		return
	}

	// Update Oracle in memory
	c.oracle.UpdateTick(symbol, price, bestBid, bestAsk, lastSize, isBuy, now)

	c.stats.Trades.Add(1)
	c.stats.TotalEvents.Add(1)

	// Push MarketEvent into ring if available
	if c.ring != nil {
		var side uint8 = 2
		if isBuy {
			side = 1
		}
		var symID uint16
		base := extractBaseCurrency(symbol)
		switch base {
		case "BTC":
			symID = 0
		case "ETH":
			symID = 1
		case "SOL":
			symID = 2
		default:
			symID = 0
		}

		c.ring.Push(engine.MarketEvent{
			Timestamp: nowNs,
			Sequence:  uint64(nowNs),
			Price:     price,
			Size:      lastSize,
			Side:      side,
			Kind:      0, // Trade
			SymbolID:  symID,
			Flags:     VenueCoinbase,
		})
	}
}

// FastParseCoinbaseTicker extracts ticker data from Coinbase JSON with 0 allocations.
func FastParseCoinbaseTicker(payload []byte) (symbol string, price, bestBid, bestAsk, lastSize float64, isBuy bool, ok bool) {
	// 1. Extract product_id
	pIDKey := []byte(`"product_id":`)
	idx := bytes.Index(payload, pIDKey)
	if idx < 0 {
		return "", 0, 0, 0, 0, false, false
	}
	rest := payload[idx+len(pIDKey):]
	for len(rest) > 0 && rest[0] <= ' ' {
		rest = rest[1:]
	}
	if len(rest) == 0 || rest[0] != '"' {
		return "", 0, 0, 0, 0, false, false
	}
	rest = rest[1:]
	end := bytes.IndexByte(rest, '"')
	if end <= 0 {
		return "", 0, 0, 0, 0, false, false
	}
	symbol = NormalizeToUniverseSymbol(string(rest[:end]))

	// 2. Extract price
	price = fastExtractJSONFloat(payload, []byte(`"price":`))
	bestBid = fastExtractJSONFloat(payload, []byte(`"best_bid":`))
	bestAsk = fastExtractJSONFloat(payload, []byte(`"best_ask":`))
	lastSize = fastExtractJSONFloat(payload, []byte(`"last_size":`))

	// 3. Extract side ("buy" vs "sell")
	sideKey := []byte(`"side":`)
	if sIdx := bytes.Index(payload, sideKey); sIdx >= 0 {
		sRest := payload[sIdx+len(sideKey):]
		for len(sRest) > 0 && (sRest[0] <= ' ' || sRest[0] == '"') {
			sRest = sRest[1:]
		}
		if len(sRest) > 0 && (sRest[0] == 'b' || sRest[0] == 'B') {
			isBuy = true
		}
	}

	return symbol, price, bestBid, bestAsk, lastSize, isBuy, true
}

func fastExtractJSONFloat(payload, key []byte) float64 {
	idx := bytes.Index(payload, key)
	if idx < 0 {
		return 0
	}
	rest := payload[idx+len(key):]
	for len(rest) > 0 && (rest[0] <= ' ' || rest[0] == '"') {
		rest = rest[1:]
	}
	end := 0
	for end < len(rest) && rest[end] != '"' && rest[end] != ',' && rest[end] != '}' && rest[end] > ' ' {
		end++
	}
	if end == 0 {
		return 0
	}
	val, _ := marketdata.ParseFastFloat(rest[:end])
	return val
}

// NormalizeToUniverseSymbol converts Coinbase product strings (e.g. BTC-USD) to standard clean keys (e.g. BTCUSD).
func NormalizeToUniverseSymbol(product string) string {
	upper := strings.ToUpper(product)
	upper = strings.ReplaceAll(upper, "-", "")
	upper = strings.ReplaceAll(upper, "_", "")
	return upper
}
