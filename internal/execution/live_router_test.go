package execution

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"piquant-live/internal/paper"
	"piquant-live/internal/quant/guardrails"
)

type mockVenueClient struct {
	mu             sync.Mutex
	name           string
	balances       map[string]float64
	balancesErr    error
	quotes         map[string]Quote
	quoteErr       error
	placeErr       error
	placeStatus    string
	placeCalls     int
	cancelCalls    int
	getOrderCalls  int
	onGetOrder     func(calls int, rec OrderReceipt) OrderReceipt
	placedRequests []LimitOrderRequest
	orders         map[string]OrderReceipt
}

func newMockVenue() *mockVenueClient {
	return &mockVenueClient{
		name:     "mock-spot",
		balances: map[string]float64{"USD": 5000.0, "BTC": 0.0, "ETH": 0.0},
		quotes: map[string]Quote{
			"BTC-USD": {
				Symbol:    "BTC-USD",
				Bid:       59990.0,
				Ask:       60010.0,
				Mid:       60000.0,
				Timestamp: time.Now().UTC(),
			},
			"ETH-USD": {
				Symbol:    "ETH-USD",
				Bid:       2999.0,
				Ask:       3001.0,
				Mid:       3000.0,
				Timestamp: time.Now().UTC(),
			},
		},
		orders: make(map[string]OrderReceipt),
	}
}

func (m *mockVenueClient) Name() string {
	return m.name
}

func (m *mockVenueClient) GetBalances(context.Context) (map[string]float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.balancesErr != nil {
		return nil, m.balancesErr
	}
	cp := make(map[string]float64, len(m.balances))
	for k, v := range m.balances {
		cp[k] = v
	}
	return cp, nil
}

func (m *mockVenueClient) GetQuote(_ context.Context, symbol string) (Quote, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.quoteErr != nil {
		return Quote{}, m.quoteErr
	}
	q, ok := m.quotes[strings.ToUpper(strings.TrimSpace(symbol))]
	if !ok {
		return Quote{}, errors.New("quote not found")
	}
	return q, nil
}

func (m *mockVenueClient) PlaceLimitOrder(_ context.Context, req LimitOrderRequest) (OrderReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.placeCalls++
	m.placedRequests = append(m.placedRequests, req)
	if m.placeErr != nil {
		return OrderReceipt{}, m.placeErr
	}
	status := "OPEN"
	if m.placeStatus != "" {
		status = m.placeStatus
	}
	rec := OrderReceipt{
		OrderID:       "ord-" + req.ClientOrderID,
		ClientOrderID: req.ClientOrderID,
		Symbol:        req.Symbol,
		Side:          req.Side,
		Price:         req.Price,
		Quantity:      req.Quantity,
		PostOnly:      req.PostOnly,
		Status:        status,
		DryRun:        false,
		SubmittedAt:   time.Now().UTC(),
	}
	m.orders[rec.OrderID] = rec
	return rec, nil
}

func (m *mockVenueClient) CancelOrder(_ context.Context, orderID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelCalls++
	delete(m.orders, orderID)
	return nil
}

func (m *mockVenueClient) GetOrder(_ context.Context, orderID string) (OrderReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getOrderCalls++
	rec, ok := m.orders[orderID]
	if !ok {
		return OrderReceipt{}, errors.New("order not found")
	}
	if m.onGetOrder != nil {
		rec = m.onGetOrder(m.getOrderCalls, rec)
		m.orders[orderID] = rec
	}
	return rec, nil
}

func testGuard() *guardrails.PreTradeGuard {
	cfg := guardrails.DefaultGuardrailConfig()
	cfg.Exposure.MaxGrossUSD = 50000
	cfg.Exposure.MaxNetUSD = 50000
	cfg.Exposure.MaxConcentrationPct = 95
	cfg.Exposure.MaxOrderQtyUSD = 10000
	cfg.MaxOrderQty = 100
	g := guardrails.NewPreTradeGuard(cfg)
	g.UpdatePortfolioState(5000, 5000, map[string]float64{"BTC-USD": 0}, map[string]float64{"BTC-USD": 60000}, time.Now().UTC())
	return g
}

func TestLiveRouterDryRunNeverSubmitsToVenue(t *testing.T) {
	venue := newMockVenue()
	cfg := DefaultRouterConfig()
	cfg.JournalPath = filepath.Join(t.TempDir(), "journal.jsonl")
	cfg.MaxDailyNotionalUSD = 2000

	r, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}

	res, err := r.SubmitTargetOrder(context.Background(), "BTC-USD", "BUY", 0.01, 25.0, false)
	if err != nil {
		t.Fatalf("dry-run order failed: %v", err)
	}
	if venue.placeCalls != 0 {
		t.Fatalf("dry-run must never call PlaceLimitOrder, got %d calls", venue.placeCalls)
	}
	if !res.DryRun || !res.Receipt.DryRun || res.Receipt.Status != "DRY_RUN" || !res.Receipt.PostOnly {
		t.Fatalf("unexpected dry-run receipt: %+v", res)
	}
	wantPrice := 59988.0 // min(59990, 60000*(1 - 2/10000))
	if math.Abs(res.Receipt.Price-wantPrice) > 1e-6 {
		t.Fatalf("got price %.6f, want %.6f", res.Receipt.Price, wantPrice)
	}
	if res.Receipt.OrderID != "dryrun-"+res.ClientOrderID {
		t.Fatalf("unexpected dryrun order id: %s", res.Receipt.OrderID)
	}

	// CancelOrder / GetOrder in dry-run do not hit the live venue.
	gotOrder, err := r.GetOrder(context.Background(), res.Receipt.OrderID)
	if err != nil || gotOrder.OrderID != res.Receipt.OrderID {
		t.Fatalf("dry-run GetOrder failed: %+v err=%v", gotOrder, err)
	}
	if err := r.CancelOrder(context.Background(), res.Receipt.OrderID); err != nil {
		t.Fatalf("dry-run CancelOrder failed: %v", err)
	}
	if venue.cancelCalls != 0 {
		t.Fatalf("dry-run cancel should not call venue, got %d", venue.cancelCalls)
	}
}

func TestLiveRouterDualArmingRequired(t *testing.T) {
	ctx := context.Background()

	// Case 1: Missing both LiveArmToken and PIQUANT_LIVE_ARM
	venue := newMockVenue()
	cfg := DefaultRouterConfig()
	cfg.DryRun = false
	t.Setenv(LiveArmEnvVar, "")
	r, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false); !errors.Is(err, ErrLiveNotArmed) {
		t.Fatalf("expected ErrLiveNotArmed with empty token/env, got %v", err)
	}

	// Case 2: Token set, env missing
	cfg.LiveArmToken = LiveArmConfirmToken
	r, err = NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false); !errors.Is(err, ErrLiveNotArmed) {
		t.Fatalf("expected ErrLiveNotArmed when env missing, got %v", err)
	}

	// Case 3: Env set, token wrong
	t.Setenv(LiveArmEnvVar, LiveArmEnvValue)
	cfg.LiveArmToken = "WRONG_TOKEN"
	r, err = NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false); !errors.Is(err, ErrLiveNotArmed) {
		t.Fatalf("expected ErrLiveNotArmed when token wrong, got %v", err)
	}
	if venue.placeCalls != 0 {
		t.Fatalf("unarmed router called PlaceLimitOrder %d times", venue.placeCalls)
	}

	// Case 4: Both token and env properly armed -> places post-only live order
	cfg.LiveArmToken = LiveArmConfirmToken
	cfg.JournalPath = filepath.Join(t.TempDir(), "live_journal.jsonl")
	r, err = NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
	if err != nil {
		t.Fatalf("armed live order failed: %v", err)
	}
	if venue.placeCalls != 1 {
		t.Fatalf("expected 1 PlaceLimitOrder call, got %d", venue.placeCalls)
	}
	if !venue.placedRequests[0].PostOnly {
		t.Fatalf("live order must always be PostOnly")
	}
	if res.DryRun || res.Receipt.DryRun || res.Receipt.OrderID != "ord-"+res.ClientOrderID {
		t.Fatalf("unexpected live receipt: %+v", res)
	}
}

func TestPassiveLimitPrice(t *testing.T) {
	qNarrow := Quote{Bid: 9999.0, Ask: 10001.0, Mid: 10000.0}
	if got := PassiveLimitPrice("BUY", qNarrow, 0); got != 9999.0 {
		t.Fatalf("BUY offset=0 want 9999, got %.4f", got)
	}
	if got := PassiveLimitPrice("SELL", qNarrow, 0); got != 10001.0 {
		t.Fatalf("SELL offset=0 want 10001, got %.4f", got)
	}
	if got := PassiveLimitPrice("BUY", qNarrow, 2.0); math.Abs(got-9998.0) > 1e-8 {
		t.Fatalf("BUY offset=2bps want 9998, got %.6f", got)
	}
	if got := PassiveLimitPrice("SELL", qNarrow, 2.0); math.Abs(got-10002.0) > 1e-8 {
		t.Fatalf("SELL offset=2bps want 10002, got %.6f", got)
	}

	// Wide spread: capped at bid/ask so we never cross inside touch
	qWide := Quote{Bid: 9990.0, Ask: 10010.0, Mid: 10000.0}
	if got := PassiveLimitPrice("BUY", qWide, 2.0); got != 9990.0 {
		t.Fatalf("wide BUY want bid 9990, got %.4f", got)
	}
	if got := PassiveLimitPrice("SELL", qWide, 2.0); got != 10010.0 {
		t.Fatalf("wide SELL want ask 10010, got %.4f", got)
	}
}

func TestLiveRouterIdempotencyAcrossRestarts(t *testing.T) {
	ctx := context.Background()
	journal := filepath.Join(t.TempDir(), "order_journal.jsonl")
	venue := newMockVenue()
	t.Setenv(LiveArmEnvVar, LiveArmEnvValue)

	cfg := DefaultRouterConfig()
	cfg.DryRun = false
	cfg.LiveArmToken = LiveArmConfirmToken
	cfg.JournalPath = journal
	cfg.MaxDailyNotionalUSD = 600.0 // tight cap to verify duplicate retries are not blocked by cap

	fixedNow := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	venue.quotes["BTC-USD"] = Quote{Symbol: "BTC-USD", Bid: 59990, Ask: 60010, Mid: 60000, Timestamp: fixedNow}

	r1, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	r1.now = func() time.Time { return fixedNow }

	res1, err := r1.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
	if err != nil {
		t.Fatalf("first live order failed: %v", err)
	}
	if res1.SkippedDuplicate || venue.placeCalls != 1 {
		t.Fatalf("unexpected first result: skipped=%v calls=%d", res1.SkippedDuplicate, venue.placeCalls)
	}

	// Retry in same router
	res2, err := r1.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
	if err != nil || !res2.SkippedDuplicate || res2.ClientOrderID != res1.ClientOrderID {
		t.Fatalf("expected duplicate skip in-memory, got res=%+v err=%v", res2, err)
	}
	if venue.placeCalls != 1 {
		t.Fatalf("duplicate retry submitted extra venue order: calls=%d", venue.placeCalls)
	}

	// Simulate process restart with new router loading JournalPath
	r2, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	r2.now = func() time.Time { return fixedNow.Add(5 * time.Second) }

	res3, err := r2.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
	if err != nil || !res3.SkippedDuplicate || res3.ClientOrderID != res1.ClientOrderID {
		t.Fatalf("expected duplicate skip after restart, got res=%+v err=%v", res3, err)
	}
	if venue.placeCalls != 1 {
		t.Fatalf("restarted router placed duplicate order: calls=%d", venue.placeCalls)
	}
}

func TestLiveRouterDryRunJournalDoesNotBlockLive(t *testing.T) {
	ctx := context.Background()
	journal := filepath.Join(t.TempDir(), "order_journal.jsonl")
	venue := newMockVenue()
	fixedNow := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	venue.quotes["BTC-USD"] = Quote{Symbol: "BTC-USD", Bid: 59990, Ask: 60010, Mid: 60000, Timestamp: fixedNow}

	dryCfg := DefaultRouterConfig()
	dryCfg.DryRun = true
	dryCfg.JournalPath = journal
	rDry, err := NewLiveRouter(dryCfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	rDry.now = func() time.Time { return fixedNow }
	if _, err := rDry.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false); err != nil {
		t.Fatal(err)
	}

	// Switching to armed live mode on same journal must still submit the live order.
	t.Setenv(LiveArmEnvVar, LiveArmEnvValue)
	liveCfg := DefaultRouterConfig()
	liveCfg.DryRun = false
	liveCfg.LiveArmToken = LiveArmConfirmToken
	liveCfg.JournalPath = journal
	rLive, err := NewLiveRouter(liveCfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	rLive.now = func() time.Time { return fixedNow }
	res, err := rLive.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
	if err != nil {
		t.Fatalf("live order blocked by dry-run journal: %v", err)
	}
	if res.SkippedDuplicate || venue.placeCalls != 1 {
		t.Fatalf("expected live order to submit once, got skipped=%v calls=%d", res.SkippedDuplicate, venue.placeCalls)
	}
}

func TestLiveRouterStaleAndInvalidQuotes(t *testing.T) {
	ctx := context.Background()
	venue := newMockVenue()
	now := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)

	cfg := DefaultRouterConfig()
	cfg.MaxQuoteAge = 30 * time.Second
	r, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return now }

	// Stale quote (>30s old)
	venue.quotes["BTC-USD"] = Quote{Symbol: "BTC-USD", Bid: 59990, Ask: 60010, Mid: 60000, Timestamp: now.Add(-31 * time.Second)}
	if _, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false); !errors.Is(err, ErrStaleQuote) {
		t.Fatalf("expected ErrStaleQuote, got %v", err)
	}

	// Crossed quote
	venue.quotes["BTC-USD"] = Quote{Symbol: "BTC-USD", Bid: 60020, Ask: 60010, Mid: 60015, Timestamp: now}
	if _, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false); !errors.Is(err, ErrStaleQuote) {
		t.Fatalf("expected ErrStaleQuote on crossed book, got %v", err)
	}

	// Quote with Mid == 0 automatically computes (Bid+Ask)/2
	venue.quotes["BTC-USD"] = Quote{Symbol: "BTC-USD", Bid: 59990, Ask: 60010, Mid: 0, Timestamp: now}
	res, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
	if err != nil {
		t.Fatalf("expected mid fallback to succeed, got %v", err)
	}
	if math.Abs(res.Receipt.Price-59988.0) > 1e-6 {
		t.Fatalf("unexpected passive price from computed mid: %.4f", res.Receipt.Price)
	}
}

func TestLiveRouterMinOrderAndDailyTurnoverCap(t *testing.T) {
	ctx := context.Background()
	venue := newMockVenue()
	journal := filepath.Join(t.TempDir(), "cap_journal.jsonl")
	now := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
	venue.quotes["ETH-USD"] = Quote{Symbol: "ETH-USD", Bid: 3000, Ask: 3002, Mid: 3001, Timestamp: now}

	cfg := DefaultRouterConfig()
	cfg.MinOrderUSD = 25.0
	cfg.MaxDailyNotionalUSD = 150.0
	cfg.PostOnlyOffsetBps = 0
	cfg.JournalPath = journal

	r, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return now }

	// Below MinOrderUSD (0.005 * 3000 = $15 < $25)
	if _, err := r.SubmitTargetOrder(ctx, "ETH-USD", "BUY", 0.005, 25.0, false); !errors.Is(err, ErrBelowMinOrderUSD) {
		t.Fatalf("expected ErrBelowMinOrderUSD, got %v", err)
	}

	// First valid order: 0.03 * 3000 = $90
	if _, err := r.SubmitTargetOrder(ctx, "ETH-USD", "BUY", 0.03, 25.0, false); err != nil {
		t.Fatalf("first ETH order failed: %v", err)
	}

	// Second order exceeding cap: 0.025 * 3000 = $75 -> $90+$75 = $165 > $150
	if _, err := r.SubmitTargetOrder(ctx, "ETH-USD", "BUY", 0.025, 25.0, false); !errors.Is(err, ErrDailyTurnoverCap) {
		t.Fatalf("expected ErrDailyTurnoverCap, got %v", err)
	}

	// Restart router: daily notional is restored from journal, small $45 order passes
	r2, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	r2.now = func() time.Time { return now.Add(time.Minute) }
	if math.Abs(r2.DailyNotionalUSD()-90.0) > 1e-6 {
		t.Fatalf("expected restored daily notional 90.0, got %.2f", r2.DailyNotionalUSD())
	}
	if _, err := r2.SubmitTargetOrder(ctx, "ETH-USD", "BUY", 0.015, 25.0, false); err != nil {
		t.Fatalf("expected $45 order within $150 cap to pass, got %v", err)
	}
}

func TestLiveRouterKillLockRespectsReduceOnly(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "dryrun_state.json")
	lockPath := paper.KillLockPath(stateFile)

	klBytes, _ := json.Marshal(paper.KillLock{
		Reason:  "daily drawdown breach",
		Trigger: "circuit_breaker",
		At:      time.Now().UTC(),
	})
	if err := os.WriteFile(lockPath, klBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	venue := newMockVenue()
	g := testGuard()
	g.UpdatePortfolioState(5000, 5000, map[string]float64{"BTC-USD": 0.05}, map[string]float64{"BTC-USD": 60000}, time.Now().UTC())

	cfg := DefaultRouterConfig()
	cfg.KillLockStatePath = stateFile
	r, err := NewLiveRouter(cfg, g, venue)
	if err != nil {
		t.Fatal(err)
	}

	// Risk-increasing BUY blocked by KILL_LOCK
	if _, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false); !errors.Is(err, ErrKillLocked) {
		t.Fatalf("expected ErrKillLocked on opening order, got %v", err)
	}

	// Reduce-only SELL closing open position is permitted
	res, err := r.SubmitTargetOrder(ctx, "BTC-USD", "SELL", 0.02, 0.0, true)
	if err != nil {
		t.Fatalf("expected reduce-only exit to bypass KILL_LOCK, got %v", err)
	}
	if res.Receipt.Side != "SELL" {
		t.Fatalf("unexpected reduce-only receipt: %+v", res)
	}
}

func TestLiveRouterFeeHurdleAndGuardrails(t *testing.T) {
	ctx := context.Background()
	venue := newMockVenue()
	g := testGuard()
	g.UpdatePortfolioState(5000, 5000, map[string]float64{"BTC-USD": 0.05}, map[string]float64{"BTC-USD": 60000}, time.Now().UTC())

	cfg := DefaultRouterConfig()
	cfg.FeeHurdle = guardrails.FeeHurdleSpec{
		RoundTripFeeBps: 10,
		SpreadBps:       2,
		SafetyMarginBps: 5,
		TaxDragFraction: 0.333,
	}
	r, err := NewLiveRouter(cfg, g, venue)
	if err != nil {
		t.Fatal(err)
	}

	// Insufficient edge (12 < 17 bps) -> ErrInsufficientNetEdge
	if _, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 12.0, false); !errors.Is(err, guardrails.ErrInsufficientNetEdge) {
		t.Fatalf("expected ErrInsufficientNetEdge, got %v", err)
	}

	// Reduce-only passes even with 0 expected edge
	if _, err := r.SubmitTargetOrder(ctx, "BTC-USD", "SELL", 0.01, 0.0, true); err != nil {
		t.Fatalf("reduce-only should clear fee hurdle, got %v", err)
	}

	// Oversize notional (> MaxOrderQtyUSD 10000) -> ErrFatFingerQty
	if _, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.25, 25.0, false); !errors.Is(err, guardrails.ErrFatFingerQty) {
		t.Fatalf("expected ErrFatFingerQty, got %v", err)
	}
}

func TestLiveRouterReconcileMatchAndMismatch(t *testing.T) {
	ctx := context.Background()
	venue := newMockVenue()
	venue.balances = map[string]float64{"USD": 1000.0, "BTC": 0.125, "ETH": 1.5}

	var alerts []string
	cfg := DefaultRouterConfig()
	cfg.ReconcileTolerance = 1e-4
	cfg.OnAlert = func(key, subject, body string) {
		alerts = append(alerts, key+":"+subject)
	}

	r, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}

	// Match within tolerance
	rep, err := r.Reconcile(ctx, map[string]float64{"USD": 1000.0, "BTC": 0.12505, "ETH": 1.5})
	if err != nil || !rep.Matched || len(rep.Mismatches) != 0 {
		t.Fatalf("expected reconcile match, got rep=%+v err=%v", rep, err)
	}
	if len(alerts) != 0 {
		t.Fatalf("unexpected alert on matching reconcile: %v", alerts)
	}

	// Mismatch trips kill switch, locks trading, fires alert, and blocks new orders
	rep, err = r.Reconcile(ctx, map[string]float64{"USD": 1000.0, "BTC": 0.20, "ETH": 1.5})
	if !errors.Is(err, ErrReconcileMismatch) {
		t.Fatalf("expected ErrReconcileMismatch, got %v", err)
	}
	if rep.Matched || len(rep.Mismatches) != 1 || rep.Mismatches[0].Asset != "BTC" {
		t.Fatalf("unexpected mismatch report: %+v", rep)
	}
	tripped, _, locked, _, _, _ := r.Guard().Status()
	if !tripped || !locked {
		t.Fatalf("expected guard tripped=%v and locked=%v", tripped, locked)
	}
	if len(alerts) != 1 || !strings.HasPrefix(alerts[0], "reconcile:") {
		t.Fatalf("expected reconcile alert, got %v", alerts)
	}
	if _, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false); !errors.Is(err, guardrails.ErrTradingLocked) {
		t.Fatalf("expected ErrTradingLocked after reconcile mismatch, got %v", err)
	}
}

func TestDeterministicClientOrderID(t *testing.T) {
	id1 := DeterministicClientOrderID("coinbase-spot", "2026-11-20", "btc-usd", "buy", 0.01)
	id2 := DeterministicClientOrderID("COINBASE-SPOT", "2026-11-20", "BTC-USD", "BUY", 0.01)
	id3 := DeterministicClientOrderID("coinbase-spot", "2026-11-20", "BTC-USD", "BUY", 0.02)
	if len(id1) != 24 {
		t.Fatalf("expected 24 char hex id, got %d (%q)", len(id1), id1)
	}
	if id1 != id2 {
		t.Fatalf("expected normalized deterministic id equality: %s vs %s", id1, id2)
	}
	if id1 == id3 {
		t.Fatalf("different quantities produced identical client order id: %s", id1)
	}
}

func TestLiveRouterIntentStableClientOrderIDAcrossQuoteChanges(t *testing.T) {
	ctx := context.Background()
	t.Setenv(LiveArmEnvVar, LiveArmEnvValue)
	journal := filepath.Join(t.TempDir(), "intent_journal.jsonl")
	fixedNow := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	cfg := DefaultRouterConfig()
	cfg.DryRun = false
	cfg.LiveArmToken = LiveArmConfirmToken
	cfg.JournalPath = journal

	venue := newMockVenue()
	venue.quotes["BTC-USD"] = Quote{Symbol: "BTC-USD", Bid: 59990, Ask: 60010, Mid: 60000, Timestamp: fixedNow}

	r, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return fixedNow }

	res1, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
	if err != nil {
		t.Fatalf("first submit failed: %v", err)
	}

	// Quote moves before retry/requote of the same intent: ClientOrderID must stay identical and deduplicate.
	venue.quotes["BTC-USD"] = Quote{Symbol: "BTC-USD", Bid: 60990, Ask: 61010, Mid: 61000, Timestamp: fixedNow.Add(5 * time.Second)}
	res2, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
	if err != nil {
		t.Fatalf("second submit failed: %v", err)
	}
	if !res2.SkippedDuplicate || res2.ClientOrderID != res1.ClientOrderID {
		t.Fatalf("expected duplicate skip with identical intent ClientOrderID across quote change, got res1=%+v res2=%+v", res1, res2)
	}
	if venue.placeCalls != 1 {
		t.Fatalf("expected 1 PlaceLimitOrder call, got %d", venue.placeCalls)
	}
}

func TestLiveRouterPersistsPendingBeforePlaceLimitOrder(t *testing.T) {
	ctx := context.Background()
	t.Setenv(LiveArmEnvVar, LiveArmEnvValue)
	journal := filepath.Join(t.TempDir(), "pending_journal.jsonl")
	fixedNow := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	cfg := DefaultRouterConfig()
	cfg.DryRun = false
	cfg.LiveArmToken = LiveArmConfirmToken
	cfg.JournalPath = journal

	venue := newMockVenue()
	venue.quotes["BTC-USD"] = Quote{Symbol: "BTC-USD", Bid: 59990, Ask: 60010, Mid: 60000, Timestamp: fixedNow}
	venue.placeErr = errors.New("simulated network timeout")

	r1, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	r1.now = func() time.Time { return fixedNow }

	if _, err := r1.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false); err == nil {
		t.Fatal("expected PlaceLimitOrder error")
	}

	raw, err := os.ReadFile(journal)
	if err != nil {
		t.Fatalf("expected PENDING journal file to be persisted before PlaceLimitOrder: %v", err)
	}
	if !strings.Contains(string(raw), `"status":"PENDING"`) {
		t.Fatalf("expected PENDING status in journal, got: %s", string(raw))
	}
	if !strings.Contains(string(raw), `"status":"REJECTED"`) {
		t.Fatalf("expected REJECTED status in journal after PlaceLimitOrder error, got: %s", string(raw))
	}

	// Simulate process crash mid-PlaceLimitOrder (only the PENDING line persisted before crash);
	// retry after restart must skip rather than place a duplicate order.
	firstLine := strings.SplitN(strings.TrimSpace(string(raw)), "\n", 2)[0] + "\n"
	if err := os.WriteFile(journal, []byte(firstLine), 0o600); err != nil {
		t.Fatal(err)
	}
	venue.placeErr = nil
	r2, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	r2.now = func() time.Time { return fixedNow.Add(10 * time.Second) }

	res2, err := r2.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
	if err != nil {
		t.Fatalf("retry after restart failed: %v", err)
	}
	if !res2.SkippedDuplicate || res2.Receipt.Status != "PENDING" {
		t.Fatalf("expected SkippedDuplicate with PENDING receipt when GetOrder fails, got %+v", res2)
	}
	if venue.placeCalls != 1 {
		t.Fatalf("expected no duplicate PlaceLimitOrder call after restart, got %d", venue.placeCalls)
	}
}

func TestLiveRouterPendingReconcilesWithGetOrderOnRetry(t *testing.T) {
	ctx := context.Background()
	t.Setenv(LiveArmEnvVar, LiveArmEnvValue)
	journal := filepath.Join(t.TempDir(), "pending_reconcile.jsonl")
	fixedNow := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	cfg := DefaultRouterConfig()
	cfg.DryRun = false
	cfg.LiveArmToken = LiveArmConfirmToken
	cfg.JournalPath = journal

	venue := newMockVenue()
	venue.quotes["BTC-USD"] = Quote{Symbol: "BTC-USD", Bid: 59990, Ask: 60010, Mid: 60000, Timestamp: fixedNow}
	venue.placeErr = errors.New("timeout after send")

	r1, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	r1.now = func() time.Time { return fixedNow }
	if _, err := r1.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false); err == nil {
		t.Fatal("expected initial timeout error")
	}

	// Simulate process crash before PlaceLimitOrder returned (leaving PENDING as latest journal entry).
	raw, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	firstLine := strings.SplitN(strings.TrimSpace(string(raw)), "\n", 2)[0] + "\n"
	if err := os.WriteFile(journal, []byte(firstLine), 0o600); err != nil {
		t.Fatal(err)
	}

	// On restart, venue actually accepted and filled the order.
	cid := DeterministicClientOrderID(venue.Name(), "2026-11-20", "BTC-USD", "BUY", 0.01)
	venue.mu.Lock()
	venue.placeErr = nil
	venue.orders[cid] = OrderReceipt{
		OrderID:       "venue-recovered-1",
		ClientOrderID: cid,
		Symbol:        "BTC-USD",
		Side:          "BUY",
		Price:         59988,
		Quantity:      0.01,
		PostOnly:      true,
		Status:        "FILLED",
		FilledQty:     0.01,
		SubmittedAt:   fixedNow,
	}
	venue.mu.Unlock()

	r2, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	r2.now = func() time.Time { return fixedNow.Add(5 * time.Second) }
	res, err := r2.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
	if err != nil {
		t.Fatalf("expected reconciled duplicate skip, got err=%v", err)
	}
	if !res.SkippedDuplicate || res.Receipt.Status != "FILLED" || res.Receipt.OrderID != "venue-recovered-1" {
		t.Fatalf("expected SkippedDuplicate with reconciled FILLED receipt, got %+v", res)
	}
	if venue.placeCalls != 1 {
		t.Fatalf("expected no second PlaceLimitOrder call, got %d", venue.placeCalls)
	}
}

func TestLiveRouterPartialFillOnTTLCancelRecordsPartialNotional(t *testing.T) {
	ctx := context.Background()
	t.Setenv(LiveArmEnvVar, LiveArmEnvValue)
	journal := filepath.Join(t.TempDir(), "partial_ttl.jsonl")
	fixedNow := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	venue := newMockVenue()
	venue.quotes["BTC-USD"] = Quote{Symbol: "BTC-USD", Bid: 59990, Ask: 60010, Mid: 60000, Timestamp: fixedNow}
	venue.onGetOrder = func(_ int, rec OrderReceipt) OrderReceipt {
		rec.Status = "OPEN"
		rec.FilledQty = 0.004
		return rec
	}

	cfg := DefaultRouterConfig()
	cfg.DryRun = false
	cfg.LiveArmToken = LiveArmConfirmToken
	cfg.JournalPath = journal
	cfg.OrderTTL = 35 * time.Millisecond
	cfg.OrderPollInterval = 10 * time.Millisecond

	r, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return fixedNow }

	res, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.CancelledOnTTL || res.Receipt.Status != "CANCELLED" || res.Receipt.FilledQty != 0.004 {
		t.Fatalf("unexpected partial-fill TTL result: %+v", res)
	}
	wantNotional := 0.004 * res.Receipt.Price
	if math.Abs(res.NotionalUSD-wantNotional) > 1e-6 || math.Abs(r.DailyNotionalUSD()-wantNotional) > 1e-6 {
		t.Fatalf("expected partial notional %.6f, got res=%.6f daily=%.6f", wantNotional, res.NotionalUSD, r.DailyNotionalUSD())
	}
}

func TestLiveRouterOrderTTLCancelsUnfilledOrder(t *testing.T) {
	ctx := context.Background()
	venue := newMockVenue()
	t.Setenv(LiveArmEnvVar, LiveArmEnvValue)
	journal := filepath.Join(t.TempDir(), "ttl_cancel_journal.jsonl")

	cfg := DefaultRouterConfig()
	cfg.DryRun = false
	cfg.LiveArmToken = LiveArmConfirmToken
	cfg.JournalPath = journal
	cfg.OrderTTL = 45 * time.Millisecond
	cfg.OrderPollInterval = 10 * time.Millisecond

	r, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}

	res, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
	if err != nil {
		t.Fatalf("expected clean TTL cancel return, got err=%v", err)
	}
	if !res.CancelledOnTTL {
		t.Fatalf("expected CancelledOnTTL=true, got false: %+v", res)
	}
	if res.Receipt.Status != "CANCELLED" {
		t.Fatalf("expected receipt status CANCELLED, got %q", res.Receipt.Status)
	}
	venue.mu.Lock()
	cancelCalls := venue.cancelCalls
	getCalls := venue.getOrderCalls
	venue.mu.Unlock()
	if cancelCalls != 1 {
		t.Fatalf("expected 1 CancelOrder call, got %d", cancelCalls)
	}
	if getCalls < 2 {
		t.Fatalf("expected multiple GetOrder polls before TTL cancel, got %d", getCalls)
	}

	// Verify idempotent retry reflects CancelledOnTTL from journal.
	resDup, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
	if err != nil || !resDup.SkippedDuplicate || !resDup.CancelledOnTTL {
		t.Fatalf("expected duplicate retry to preserve CancelledOnTTL, got %+v err=%v", resDup, err)
	}

	// DryRun with OrderTTL must remain instant (no sleeping/polling).
	dryCfg := DefaultRouterConfig()
	dryCfg.DryRun = true
	dryCfg.OrderTTL = 5 * time.Second
	dryCfg.OrderPollInterval = 10 * time.Millisecond
	rDry, err := NewLiveRouter(dryCfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	dryRes, err := rDry.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
	if err != nil {
		t.Fatalf("dry-run with OrderTTL failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("dry-run slept unexpectedly (%s) with OrderTTL configured", elapsed)
	}
	if dryRes.CancelledOnTTL || dryRes.Receipt.Status != "DRY_RUN" {
		t.Fatalf("unexpected dry-run result: %+v", dryRes)
	}
}

func TestLiveRouterOrderTTLFillsDuringPolling(t *testing.T) {
	ctx := context.Background()
	venue := newMockVenue()
	t.Setenv(LiveArmEnvVar, LiveArmEnvValue)
	venue.onGetOrder = func(calls int, rec OrderReceipt) OrderReceipt {
		if calls >= 2 {
			rec.Status = "FILLED"
			rec.FilledQty = rec.Quantity
		}
		return rec
	}

	cfg := DefaultRouterConfig()
	cfg.DryRun = false
	cfg.LiveArmToken = LiveArmConfirmToken
	cfg.OrderTTL = 500 * time.Millisecond
	cfg.OrderPollInterval = 10 * time.Millisecond

	r, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
	if err != nil {
		t.Fatalf("expected filled order return, got err=%v", err)
	}
	if res.CancelledOnTTL {
		t.Fatalf("expected CancelledOnTTL=false for filled order")
	}
	if res.Receipt.Status != "FILLED" || res.Receipt.FilledQty != 0.01 {
		t.Fatalf("expected FILLED receipt with qty 0.01, got %+v", res.Receipt)
	}
	venue.mu.Lock()
	cancelCalls := venue.cancelCalls
	venue.mu.Unlock()
	if cancelCalls != 0 {
		t.Fatalf("expected 0 CancelOrder calls on filled order, got %d", cancelCalls)
	}
}

func TestLiveRouterOrderTTLRespectsContextCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	venue := newMockVenue()
	t.Setenv(LiveArmEnvVar, LiveArmEnvValue)
	journal := filepath.Join(t.TempDir(), "ttl_ctx_journal.jsonl")

	cfg := DefaultRouterConfig()
	cfg.DryRun = false
	cfg.LiveArmToken = LiveArmConfirmToken
	cfg.JournalPath = journal
	cfg.OrderTTL = 2 * time.Second
	cfg.OrderPollInterval = 10 * time.Millisecond

	r, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("TTL wait ignored ctx cancellation, took %s", elapsed)
	}
	if _, statErr := os.Stat(journal); statErr != nil {
		t.Fatalf("expected placed order to be journaled on ctx cancel: %v", statErr)
	}
}

func TestHighWaterMarkLoadUpdateAndUTCRollover(t *testing.T) {
	dir := t.TempDir()
	hwmPath := filepath.Join(dir, "paper", "exec_hwm.json")

	// Missing file returns zero state cleanly.
	initial, err := LoadHighWaterMark(hwmPath)
	if err != nil {
		t.Fatalf("LoadHighWaterMark missing failed: %v", err)
	}
	if initial.PeakNavUSD != 0 || initial.DailyPeakUSD != 0 {
		t.Fatalf("expected zero initial state, got %+v", initial)
	}

	d1Morning := time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)
	st1, err := UpdateAndSaveHighWaterMark(hwmPath, 1000.0, d1Morning)
	if err != nil {
		t.Fatalf("UpdateAndSaveHighWaterMark #1 failed: %v", err)
	}
	if st1.PeakNavUSD != 1000.0 || st1.LastNavUSD != 1000.0 || st1.DailyPeakUSD != 1000.0 || st1.DailyDateUTC != "2026-11-20" {
		t.Fatalf("unexpected hwm #1: %+v", st1)
	}

	// Higher NAV same day raises both PeakNavUSD and DailyPeakUSD.
	d1Afternoon := time.Date(2026, 11, 20, 16, 0, 0, 0, time.UTC)
	st2, err := UpdateAndSaveHighWaterMark(hwmPath, 1250.0, d1Afternoon)
	if err != nil {
		t.Fatalf("UpdateAndSaveHighWaterMark #2 failed: %v", err)
	}
	if st2.PeakNavUSD != 1250.0 || st2.LastNavUSD != 1250.0 || st2.DailyPeakUSD != 1250.0 || st2.DailyDateUTC != "2026-11-20" {
		t.Fatalf("unexpected hwm #2: %+v", st2)
	}

	// Drawdown same day preserves both PeakNavUSD and DailyPeakUSD.
	d1Evening := time.Date(2026, 11, 20, 23, 45, 0, 0, time.UTC)
	st3, err := UpdateAndSaveHighWaterMark(hwmPath, 1100.0, d1Evening)
	if err != nil {
		t.Fatalf("UpdateAndSaveHighWaterMark #3 failed: %v", err)
	}
	if st3.PeakNavUSD != 1250.0 || st3.LastNavUSD != 1100.0 || st3.DailyPeakUSD != 1250.0 || st3.DailyDateUTC != "2026-11-20" {
		t.Fatalf("unexpected hwm #3: %+v", st3)
	}

	// New UTC day resets DailyPeakUSD to current NAV while keeping monotonic PeakNavUSD.
	d2Morning := time.Date(2026, 11, 21, 0, 5, 0, 0, time.UTC)
	st4, err := UpdateAndSaveHighWaterMark(hwmPath, 1050.0, d2Morning)
	if err != nil {
		t.Fatalf("UpdateAndSaveHighWaterMark #4 failed: %v", err)
	}
	if st4.PeakNavUSD != 1250.0 || st4.LastNavUSD != 1050.0 || st4.DailyPeakUSD != 1050.0 || st4.DailyDateUTC != "2026-11-21" {
		t.Fatalf("unexpected hwm #4: %+v", st4)
	}

	reloaded, err := LoadHighWaterMark(hwmPath)
	if err != nil {
		t.Fatalf("LoadHighWaterMark reload failed: %v", err)
	}
	if reloaded != st4 {
		t.Fatalf("reloaded hwm mismatch: got %+v, want %+v", reloaded, st4)
	}
	if _, err := os.Stat(hwmPath + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no lingering .tmp file, stat err=%v", err)
	}
}

func TestLiveRouterSameDaySameSizeDistinctOrdersWithIntentKeyAndClientOrderID(t *testing.T) {
	ctx := context.Background()
	t.Setenv(LiveArmEnvVar, LiveArmEnvValue)
	journal := filepath.Join(t.TempDir(), "multi_clip_journal.jsonl")
	fixedNow := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)

	base := DeterministicClientOrderID("coinbase-spot", "2026-11-20", "BTC-USD", "BUY", 0.01)
	if got := DeterministicClientOrderIDWithKey("coinbase-spot", "2026-11-20", "BTC-USD", "BUY", 0.01, ""); got != base {
		t.Fatalf("empty intentKey should match DeterministicClientOrderID: got %s want %s", got, base)
	}
	clip1 := DeterministicClientOrderIDWithKey("coinbase-spot", "2026-11-20", "BTC-USD", "BUY", 0.01, "clip-1")
	clip2 := DeterministicClientOrderIDWithKey("coinbase-spot", "2026-11-20", "BTC-USD", "BUY", 0.01, "clip-2")
	if len(clip1) != 24 || clip1 == clip2 || clip1 == base {
		t.Fatalf("expected distinct 24-char hex IDs, got base=%s clip1=%s clip2=%s", base, clip1, clip2)
	}

	cfg := DefaultRouterConfig()
	cfg.DryRun = false
	cfg.LiveArmToken = LiveArmConfirmToken
	cfg.JournalPath = journal

	venue := newMockVenue()
	venue.quotes["BTC-USD"] = Quote{Symbol: "BTC-USD", Bid: 59990, Ask: 60010, Mid: 60000, Timestamp: fixedNow}

	r, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return fixedNow }

	// Clip 1 submits.
	res1, err := r.SubmitTargetOrderWithOptions(ctx, "BTC-USD", "BUY", 0.01, 25.0, false, SubmitOrderOptions{IntentKey: "clip-1"})
	if err != nil {
		t.Fatalf("clip-1 failed: %v", err)
	}
	if res1.SkippedDuplicate {
		t.Fatalf("expected clip-1 not to be skipped")
	}

	// Retry of clip 1 deduplicates.
	res1Retry, err := r.SubmitTargetOrderWithOptions(ctx, "BTC-USD", "BUY", 0.01, 25.0, false, SubmitOrderOptions{IntentKey: "clip-1"})
	if err != nil || !res1Retry.SkippedDuplicate || res1Retry.ClientOrderID != res1.ClientOrderID {
		t.Fatalf("expected clip-1 retry to deduplicate, got res=%+v err=%v", res1Retry, err)
	}

	// Clip 2 (same day, same symbol, same side, same qty) must NOT collide with clip 1.
	res2, err := r.SubmitTargetOrderWithOptions(ctx, "BTC-USD", "BUY", 0.01, 25.0, false, SubmitOrderOptions{IntentKey: "clip-2"})
	if err != nil {
		t.Fatalf("clip-2 failed: %v", err)
	}
	if res2.SkippedDuplicate || res2.ClientOrderID == res1.ClientOrderID {
		t.Fatalf("expected clip-2 to submit with distinct ClientOrderID, got res2=%+v res1=%+v", res2, res1)
	}

	// Explicit ClientOrderID also submits distinctly and deduplicates on retry.
	res3, err := r.SubmitTargetOrderWithOptions(ctx, "BTC-USD", "BUY", 0.01, 25.0, false, SubmitOrderOptions{ClientOrderID: "custom-reentry-1"})
	if err != nil {
		t.Fatalf("explicit ClientOrderID submit failed: %v", err)
	}
	if res3.SkippedDuplicate || res3.ClientOrderID != "custom-reentry-1" {
		t.Fatalf("unexpected explicit ClientOrderID result: %+v", res3)
	}
	res3Retry, err := r.SubmitTargetOrderWithOptions(ctx, "BTC-USD", "BUY", 0.01, 25.0, false, SubmitOrderOptions{ClientOrderID: "custom-reentry-1"})
	if err != nil || !res3Retry.SkippedDuplicate || res3Retry.ClientOrderID != "custom-reentry-1" {
		t.Fatalf("expected explicit ClientOrderID retry to deduplicate, got res=%+v err=%v", res3Retry, err)
	}

	if venue.placeCalls != 3 {
		t.Fatalf("expected 3 PlaceLimitOrder calls (clip-1, clip-2, explicit), got %d", venue.placeCalls)
	}
}

type clientLookupMockVenue struct {
	*mockVenueClient
	lookupByClient map[string]OrderReceipt
	lookupErr      error
	lookupCalls    int
}

func (m *clientLookupMockVenue) GetOrderByClientOrderID(_ context.Context, _ string, clientOrderID string) (OrderReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lookupCalls++
	if m.lookupErr != nil {
		return OrderReceipt{}, m.lookupErr
	}
	rec, ok := m.lookupByClient[clientOrderID]
	if !ok {
		return OrderReceipt{}, ErrOrderNotFound
	}
	return rec, nil
}

func TestLiveRouterPendingRecoveryUsesClientOrderLookupAndPropagatesErrors(t *testing.T) {
	ctx := context.Background()
	t.Setenv(LiveArmEnvVar, LiveArmEnvValue)
	journal := filepath.Join(t.TempDir(), "pending_client_lookup.jsonl")
	fixedNow := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	cfg := DefaultRouterConfig()
	cfg.DryRun = false
	cfg.LiveArmToken = LiveArmConfirmToken
	cfg.JournalPath = journal

	baseVenue := newMockVenue()
	baseVenue.quotes["BTC-USD"] = Quote{Symbol: "BTC-USD", Bid: 59990, Ask: 60010, Mid: 60000, Timestamp: fixedNow}
	baseVenue.placeErr = errors.New("timeout after send")

	venue := &clientLookupMockVenue{
		mockVenueClient: baseVenue,
		lookupByClient:  make(map[string]OrderReceipt),
	}

	r1, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	r1.now = func() time.Time { return fixedNow }
	if _, err := r1.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false); err == nil {
		t.Fatal("expected initial timeout error")
	}

	// Simulate process crash before PlaceLimitOrder returned (leaving PENDING as latest journal entry).
	raw, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	firstLine := strings.SplitN(strings.TrimSpace(string(raw)), "\n", 2)[0] + "\n"
	if err := os.WriteFile(journal, []byte(firstLine), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. Transient lookup error must be surfaced (not swallowed as stale PENDING with err==nil).
	venue.mu.Lock()
	venue.placeErr = nil
	venue.lookupErr = errors.New("HTTP 500: upstream unavailable")
	venue.mu.Unlock()

	r2, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	r2.now = func() time.Time { return fixedNow.Add(5 * time.Second) }
	if _, err := r2.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false); err == nil {
		t.Fatal("expected error when GetOrderByClientOrderID fails with transient 500")
	}
	if venue.lookupCalls != 1 {
		t.Fatalf("expected GetOrderByClientOrderID to be called once, got %d", venue.lookupCalls)
	}

	// 2. Once GetOrderByClientOrderID succeeds, PENDING reconciles to FILLED with server OrderID.
	cid := DeterministicClientOrderID(venue.Name(), "2026-11-20", "BTC-USD", "BUY", 0.01)
	venue.mu.Lock()
	venue.lookupErr = nil
	venue.lookupByClient[cid] = OrderReceipt{
		OrderID:       "srv-ord-777",
		ClientOrderID: cid,
		Symbol:        "BTC-USD",
		Side:          "BUY",
		Price:         59988,
		Quantity:      0.01,
		PostOnly:      true,
		Status:        "FILLED",
		FilledQty:     0.01,
		SubmittedAt:   fixedNow,
	}
	venue.mu.Unlock()

	res, err := r2.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
	if err != nil {
		t.Fatalf("expected reconciled duplicate skip, got err=%v", err)
	}
	if !res.SkippedDuplicate || res.Receipt.Status != "FILLED" || res.Receipt.OrderID != "srv-ord-777" {
		t.Fatalf("expected reconciled FILLED receipt with srv-ord-777, got %+v", res)
	}
	if venue.placeCalls != 1 {
		t.Fatalf("expected no duplicate PlaceLimitOrder call, got %d", venue.placeCalls)
	}
}

func TestLiveRouterPlaceLimitOrderErrorMarksRejectedAndUnwedges(t *testing.T) {
	ctx := context.Background()
	t.Setenv(LiveArmEnvVar, LiveArmEnvValue)
	journal := filepath.Join(t.TempDir(), "place_err_rejected.jsonl")
	fixedNow := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	cfg := DefaultRouterConfig()
	cfg.DryRun = false
	cfg.LiveArmToken = LiveArmConfirmToken
	cfg.JournalPath = journal

	venue := newMockVenue()
	venue.quotes["BTC-USD"] = Quote{Symbol: "BTC-USD", Bid: 59990, Ask: 60010, Mid: 60000, Timestamp: fixedNow}
	venue.placeErr = errors.New("400 post-only rejected")

	r, err := NewLiveRouter(cfg, testGuard(), venue)
	if err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return fixedNow }

	if _, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false); err == nil {
		t.Fatal("expected PlaceLimitOrder error")
	}
	if got := r.DailyNotionalUSD(); got != 0 {
		t.Fatalf("expected DailyNotionalUSD to be 0 after failed PlaceLimitOrder, got %.2f", got)
	}

	// Retrying with the same clientOrderID in-process must NOT be wedged as SkippedDuplicate.
	venue.placeErr = nil
	res, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
	if err != nil {
		t.Fatalf("expected retry to succeed, got %v", err)
	}
	if res.SkippedDuplicate {
		t.Fatalf("expected retry after PlaceLimitOrder error not to be SkippedDuplicate, got %+v", res)
	}
	if venue.placeCalls != 2 {
		t.Fatalf("expected 2 PlaceLimitOrder calls (failed + retry), got %d", venue.placeCalls)
	}
}

func TestLiveRouterTerminalNonFillStatusesRecomputeNotional(t *testing.T) {
	ctx := context.Background()
	t.Setenv(LiveArmEnvVar, LiveArmEnvValue)
	fixedNow := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	for _, status := range []string{"EXPIRED", "FAILED", "REJECTED"} {
		t.Run("live_"+status, func(t *testing.T) {
			cfg := DefaultRouterConfig()
			cfg.DryRun = false
			cfg.LiveArmToken = LiveArmConfirmToken
			cfg.JournalPath = filepath.Join(t.TempDir(), status+".jsonl")

			venue := newMockVenue()
			venue.quotes["BTC-USD"] = Quote{Symbol: "BTC-USD", Bid: 59990, Ask: 60010, Mid: 60000, Timestamp: fixedNow}
			venue.placeStatus = status

			r, err := NewLiveRouter(cfg, testGuard(), venue)
			if err != nil {
				t.Fatal(err)
			}
			r.now = func() time.Time { return fixedNow }

			res, err := r.SubmitTargetOrder(ctx, "BTC-USD", "BUY", 0.01, 25.0, false)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.NotionalUSD != 0 {
				t.Fatalf("expected NotionalUSD=0 for %s with 0 filledQty, got %.2f", status, res.NotionalUSD)
			}
			if got := r.DailyNotionalUSD(); got != 0 {
				t.Fatalf("expected DailyNotionalUSD=0 for %s, got %.2f", status, got)
			}
		})
	}
}
