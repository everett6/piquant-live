package execution

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"piquant-live/internal/paper"
	"piquant-live/internal/quant/guardrails"
)

const (
	// LiveArmConfirmToken must be passed in RouterConfig.LiveArmToken (and --confirm-live) to allow live orders.
	LiveArmConfirmToken = "ARM_LIVE_EXECUTION"
	// LiveArmEnvVar must be set to LiveArmEnvValue in the process environment to allow live orders.
	LiveArmEnvVar = "PIQUANT_LIVE_ARM"
	// LiveArmEnvValue is the required value of PIQUANT_LIVE_ARM for live order submission.
	LiveArmEnvValue = "YES"
)

var (
	// ErrLiveNotArmed indicates live order submission was attempted without both confirmation token and env var.
	ErrLiveNotArmed = errors.New("execution: live trading is not armed (requires LiveArmToken=ARM_LIVE_EXECUTION and PIQUANT_LIVE_ARM=YES)")
	// ErrStaleQuote indicates the venue top-of-book quote is missing, invalid, or older than MaxQuoteAge.
	ErrStaleQuote = errors.New("execution: market quote is stale or invalid")
	// ErrDailyTurnoverCap indicates submitting the order would exceed MaxDailyNotionalUSD.
	ErrDailyTurnoverCap = errors.New("execution: daily notional turnover cap exceeded")
	// ErrBelowMinOrderUSD indicates the order notional is smaller than MinOrderUSD.
	ErrBelowMinOrderUSD = errors.New("execution: order notional below minimum USD threshold")
	// ErrKillLocked indicates a persistent KILL_LOCK file prevents new risk-increasing orders.
	ErrKillLocked = errors.New("execution: trading halted by persistent KILL_LOCK")
	// ErrReconcileMismatch indicates actual venue balances diverge from expected balances beyond tolerance.
	ErrReconcileMismatch = errors.New("execution: venue balance reconciliation mismatch")
	// ErrHighLatency indicates venue ping latency exceeds the configured threshold or all probes failed.
	ErrHighLatency = errors.New("execution: venue ping latency exceeds threshold")
	// ErrOrderNotFound indicates the venue reported no matching order for the requested ID.
	ErrOrderNotFound = errors.New("execution: order not found")
)

// Quote holds top-of-book inside bid/ask/mid prices and quote timestamp from a trading venue.
type Quote struct {
	Symbol    string    `json:"symbol"`
	Bid       float64   `json:"bid"`
	Ask       float64   `json:"ask"`
	Mid       float64   `json:"mid"`
	Timestamp time.Time `json:"timestamp"`
}

// LimitOrderRequest represents a strictly passive limit order submitted to a VenueClient.
type LimitOrderRequest struct {
	ClientOrderID string  `json:"client_order_id"`
	Symbol        string  `json:"symbol"`
	Side          string  `json:"side"`
	Price         float64 `json:"price"`
	Quantity      float64 `json:"quantity"`
	PostOnly      bool    `json:"post_only"`
}

// OrderReceipt describes an acknowledged or simulated limit order.
type OrderReceipt struct {
	OrderID       string    `json:"order_id"`
	ClientOrderID string    `json:"client_order_id"`
	Symbol        string    `json:"symbol"`
	Side          string    `json:"side"`
	Price         float64   `json:"price"`
	Quantity      float64   `json:"quantity"`
	PostOnly      bool      `json:"post_only"`
	Status        string    `json:"status"`
	FilledQty     float64   `json:"filled_qty"`
	DryRun        bool      `json:"dry_run"`
	SubmittedAt   time.Time `json:"submitted_at"`
}

// VenueClient abstracts a spot/exchange execution adapter (live or mock).
type VenueClient interface {
	Name() string
	GetBalances(ctx context.Context) (map[string]float64, error)
	GetQuote(ctx context.Context, symbol string) (Quote, error)
	PlaceLimitOrder(ctx context.Context, req LimitOrderRequest) (OrderReceipt, error)
	CancelOrder(ctx context.Context, orderID string) error
	GetOrder(ctx context.Context, orderID string) (OrderReceipt, error)
}

// ClientOrderLookup is an optional VenueClient capability for looking up an order
// by its client_order_id (used during crash recovery when server OrderID is unknown).
type ClientOrderLookup interface {
	GetOrderByClientOrderID(ctx context.Context, symbol, clientOrderID string) (OrderReceipt, error)
}

// SubmitOrderOptions configures optional per-order identity overrides for SubmitTargetOrderWithOptions.
type SubmitOrderOptions struct {
	// ClientOrderID explicitly sets the client order ID used for venue submission and idempotency.
	ClientOrderID string
	// IntentKey disambiguates distinct same-day, same-size orders (e.g. "clip-2" or "reentry-1")
	// while preserving deterministic idempotency when retried with the same IntentKey.
	IntentKey string
}

// RouterConfig configures LiveRouter execution safety limits, idempotency, and guardrails.
// Start from DefaultRouterConfig() so safe defaults (DryRun=true, MaxQuoteAge=120s, etc.) are applied.
type RouterConfig struct {
	DryRun              bool
	LiveArmToken        string
	KillLockStatePath   string
	MaxQuoteAge         time.Duration
	MaxDailyNotionalUSD float64
	PostOnlyOffsetBps   float64
	MinOrderUSD         float64
	OrderTTL            time.Duration
	OrderPollInterval   time.Duration
	JournalPath         string
	ReconcileTolerance  float64
	FeeHurdle           guardrails.FeeHurdleSpec
	OnAlert             func(key, subject, body string)
}

// DefaultRouterConfig returns conservative live-ready defaults with DryRun enabled.
func DefaultRouterConfig() RouterConfig {
	return RouterConfig{
		DryRun:             true,
		MaxQuoteAge:        120 * time.Second,
		PostOnlyOffsetBps:  2.0,
		MinOrderUSD:        10.0,
		ReconcileTolerance: 1e-4,
	}
}

// BalanceMismatch records a single asset discrepancy detected during venue reconciliation.
type BalanceMismatch struct {
	Asset    string  `json:"asset"`
	Expected float64 `json:"expected"`
	Actual   float64 `json:"actual"`
	Diff     float64 `json:"diff"`
}

// ReconcileReport summarizes balance verification against expected asset quantities.
type ReconcileReport struct {
	Venue      string             `json:"venue"`
	CheckedAt  time.Time          `json:"checked_at"`
	Matched    bool               `json:"matched"`
	Tolerance  float64            `json:"tolerance"`
	Expected   map[string]float64 `json:"expected"`
	Actual     map[string]float64 `json:"actual"`
	Mismatches []BalanceMismatch  `json:"mismatches,omitempty"`
}

// ExecutionResult describes the outcome of a single SubmitTargetOrder call.
type ExecutionResult struct {
	ClientOrderID    string       `json:"client_order_id"`
	Receipt          OrderReceipt `json:"receipt"`
	DryRun           bool         `json:"dry_run"`
	SkippedDuplicate bool         `json:"skipped_duplicate,omitempty"`
	CancelledOnTTL   bool         `json:"cancelled_on_ttl,omitempty"`
	NotionalUSD      float64      `json:"notional_usd"`
}

// JournalEntry is one line persisted to RouterConfig.JournalPath for idempotent recovery.
type JournalEntry struct {
	Timestamp      time.Time    `json:"timestamp"`
	Venue          string       `json:"venue"`
	ClientOrderID  string       `json:"client_order_id"`
	DayBucket      string       `json:"day_bucket"`
	Symbol         string       `json:"symbol"`
	Side           string       `json:"side"`
	Price          float64      `json:"price"`
	Quantity       float64      `json:"quantity"`
	NotionalUSD    float64      `json:"notional_usd"`
	DryRun         bool         `json:"dry_run"`
	CancelledOnTTL bool         `json:"cancelled_on_ttl,omitempty"`
	Receipt        OrderReceipt `json:"receipt"`
}

type journalKey struct {
	id     string
	dryRun bool
}

// LiveRouter orchestrates pre-trade risk guardrails, quote freshness, passive post-only pricing,
// idempotent client order journaling, balance reconciliation, and dual-factor live arming.
type LiveRouter struct {
	cfg   RouterConfig
	guard *guardrails.PreTradeGuard
	venue VenueClient
	now   func() time.Time

	submitMu      sync.Mutex
	mu            sync.Mutex
	seenOrders    map[journalKey]JournalEntry
	dailyBucket   string
	dailyNotional float64
}

// NewLiveRouter constructs a LiveRouter wrapping a PreTradeGuard and VenueClient,
// loading any previously persisted idempotent client order IDs from cfg.JournalPath.
func NewLiveRouter(cfg RouterConfig, guard *guardrails.PreTradeGuard, venue VenueClient) (*LiveRouter, error) {
	if venue == nil {
		return nil, errors.New("execution: VenueClient is required")
	}
	if guard == nil {
		guard = guardrails.NewPreTradeGuard(guardrails.DefaultGuardrailConfig())
	}
	if cfg.MaxQuoteAge <= 0 {
		cfg.MaxQuoteAge = 120 * time.Second
	}
	if cfg.MinOrderUSD <= 0 {
		cfg.MinOrderUSD = 10.0
	}
	if cfg.ReconcileTolerance <= 0 {
		cfg.ReconcileTolerance = 1e-4
	}
	if math.IsNaN(cfg.PostOnlyOffsetBps) || math.IsInf(cfg.PostOnlyOffsetBps, 0) || cfg.PostOnlyOffsetBps < 0 {
		cfg.PostOnlyOffsetBps = 2.0
	}
	if cfg.OrderTTL < 0 {
		cfg.OrderTTL = 0
	}
	if cfg.OrderTTL > 0 && cfg.OrderPollInterval <= 0 {
		cfg.OrderPollInterval = 250 * time.Millisecond
	}

	r := &LiveRouter{
		cfg:         cfg,
		guard:       guard,
		venue:       venue,
		now:         time.Now,
		seenOrders:  make(map[journalKey]JournalEntry),
		dailyBucket: time.Now().UTC().Format("2006-01-02"),
	}
	if cfg.JournalPath != "" {
		if err := r.loadJournal(); err != nil {
			return nil, fmt.Errorf("execution: load journal %s: %w", cfg.JournalPath, err)
		}
	}
	return r, nil
}

// DeterministicClientOrderID produces a stable 24-character hex SHA-256 prefix for idempotent retries.
// It hashes only the order intent (venue, dayBucket, symbol, side, qty) so retries or requotes
// with an updated live quote price preserve the same ClientOrderID.
func DeterministicClientOrderID(venue, dayBucket, symbol, side string, qty float64) string {
	normZero := func(v float64) float64 {
		if v == 0 {
			return 0
		}
		return v
	}
	payload := fmt.Sprintf("%s|%s|%s|%s|%.8f",
		strings.ToLower(strings.TrimSpace(venue)),
		strings.TrimSpace(dayBucket),
		strings.ToUpper(strings.TrimSpace(symbol)),
		strings.ToUpper(strings.TrimSpace(side)),
		normZero(qty),
	)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])[:24]
}

// DeterministicClientOrderIDWithKey produces a stable 24-character hex SHA-256 prefix
// scoped by an optional intentKey (e.g. clip index or re-entry sequence).
// When intentKey is empty, it returns the same ID as DeterministicClientOrderID.
func DeterministicClientOrderIDWithKey(venue, dayBucket, symbol, side string, qty float64, intentKey string) string {
	key := strings.TrimSpace(intentKey)
	if key == "" {
		return DeterministicClientOrderID(venue, dayBucket, symbol, side, qty)
	}
	normZero := func(v float64) float64 {
		if v == 0 {
			return 0
		}
		return v
	}
	payload := fmt.Sprintf("%s|%s|%s|%s|%.8f|%s",
		strings.ToLower(strings.TrimSpace(venue)),
		strings.TrimSpace(dayBucket),
		strings.ToUpper(strings.TrimSpace(symbol)),
		strings.ToUpper(strings.TrimSpace(side)),
		normZero(qty),
		key,
	)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])[:24]
}

// PassiveLimitPrice computes a strictly passive post-only limit price from the inside touch and mid.
// For BUY: min(bid, mid*(1 - offsetBps/1e4)); for SELL: max(ask, mid*(1 + offsetBps/1e4)).
func PassiveLimitPrice(side string, quote Quote, offsetBps float64) float64 {
	mid := quote.Mid
	if mid <= 0 && quote.Bid > 0 && quote.Ask > 0 {
		mid = 0.5 * (quote.Bid + quote.Ask)
	}
	if offsetBps < 0 || math.IsNaN(offsetBps) || math.IsInf(offsetBps, 0) {
		offsetBps = 0
	}
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "BUY":
		return math.Min(quote.Bid, mid*(1.0-offsetBps/1e4))
	case "SELL":
		return math.Max(quote.Ask, mid*(1.0+offsetBps/1e4))
	default:
		return 0
	}
}

// Config returns a copy of the router's effective configuration.
func (r *LiveRouter) Config() RouterConfig {
	return r.cfg
}

// Guard returns the underlying PreTradeGuard instance.
func (r *LiveRouter) Guard() *guardrails.PreTradeGuard {
	return r.guard
}

// DailyNotionalUSD returns the cumulative order notional submitted or simulated in the current UTC day.
func (r *LiveRouter) DailyNotionalUSD() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	bucket := r.clock().UTC().Format("2006-01-02")
	if r.dailyBucket != bucket {
		return 0
	}
	return r.dailyNotional
}

// Reconcile compares venue available balances against expectedQty.
// Any discrepancy above ReconcileTolerance locks trading, trips the kill switch, fires OnAlert,
// and returns the ReconcileReport along with ErrReconcileMismatch.
func (r *LiveRouter) Reconcile(ctx context.Context, expectedQty map[string]float64) (ReconcileReport, error) {
	r.submitMu.Lock()
	defer r.submitMu.Unlock()
	now := r.clock().UTC()
	report := ReconcileReport{
		Venue:     r.venue.Name(),
		CheckedAt: now,
		Tolerance: r.cfg.ReconcileTolerance,
		Expected:  make(map[string]float64, len(expectedQty)),
		Actual:    make(map[string]float64),
	}
	for k, v := range expectedQty {
		report.Expected[k] = v
	}

	balances, err := r.venue.GetBalances(ctx)
	if err != nil {
		return report, fmt.Errorf("reconcile %s: fetch balances: %w", r.venue.Name(), err)
	}
	upperBalances := make(map[string]float64, len(balances))
	for k, v := range balances {
		report.Actual[k] = v
		upperBalances[strings.ToUpper(strings.TrimSpace(k))] = v
	}

	keys := make([]string, 0, len(expectedQty))
	for k := range expectedQty {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, asset := range keys {
		exp := expectedQty[asset]
		act, ok := balances[asset]
		if !ok {
			act = upperBalances[strings.ToUpper(strings.TrimSpace(asset))]
		}
		diff := math.Abs(act - exp)
		if math.IsNaN(exp) || math.IsInf(exp, 0) || math.IsNaN(act) || math.IsInf(act, 0) || diff > r.cfg.ReconcileTolerance {
			report.Mismatches = append(report.Mismatches, BalanceMismatch{
				Asset:    asset,
				Expected: exp,
				Actual:   act,
				Diff:     diff,
			})
		}
	}

	if len(report.Mismatches) > 0 {
		report.Matched = false
		var parts []string
		for _, m := range report.Mismatches {
			parts = append(parts, fmt.Sprintf("%s expected=%.8f actual=%.8f diff=%.8f", m.Asset, m.Expected, m.Actual, m.Diff))
		}
		details := strings.Join(parts, "; ")
		reason := fmt.Sprintf("balance reconciliation mismatch on %s: %s", r.venue.Name(), details)
		r.guard.LockTrading(true)
		r.guard.TripKillSwitch(reason)
		if r.cfg.KillLockStatePath != "" {
			_ = paper.WriteKillLock(r.cfg.KillLockStatePath, reason, now)
		}
		if r.cfg.OnAlert != nil {
			r.cfg.OnAlert(
				"reconcile",
				"Balance reconciliation mismatch on "+r.venue.Name(),
				fmt.Sprintf("Trading locked and kill switch tripped at %s.\n\nDiscrepancies:\n%s", now.Format(time.RFC3339), details),
			)
		}
		return report, fmt.Errorf("%w: %s", ErrReconcileMismatch, details)
	}

	report.Matched = true
	return report, nil
}

// SubmitTargetOrder validates kill lock, quote staleness, passive pricing, order minimums,
// daily turnover cap, fee hurdle, and PreTradeGuard rules before journaling and either
// simulating (DryRun=true) or submitting (DryRun=false and dual-armed) a post-only limit order.
func (r *LiveRouter) SubmitTargetOrder(
	ctx context.Context,
	symbol, side string,
	qty float64,
	expectedEdgeBps float64,
	isReduceOnly bool,
) (ExecutionResult, error) {
	return r.SubmitTargetOrderWithOptions(ctx, symbol, side, qty, expectedEdgeBps, isReduceOnly, SubmitOrderOptions{})
}

// SubmitTargetOrderWithOptions behaves like SubmitTargetOrder while allowing an explicit
// ClientOrderID or IntentKey to disambiguate same-day, same-size distinct orders.
func (r *LiveRouter) SubmitTargetOrderWithOptions(
	ctx context.Context,
	symbol, side string,
	qty float64,
	expectedEdgeBps float64,
	isReduceOnly bool,
	opts SubmitOrderOptions,
) (ExecutionResult, error) {
	r.submitMu.Lock()
	defer r.submitMu.Unlock()

	sym := strings.ToUpper(strings.TrimSpace(symbol))
	sideUpper := strings.ToUpper(strings.TrimSpace(side))
	if sym == "" || (sideUpper != "BUY" && sideUpper != "SELL") || !isFinitePositive(qty) ||
		math.IsNaN(expectedEdgeBps) || math.IsInf(expectedEdgeBps, 0) {
		return ExecutionResult{}, guardrails.ErrInvalidOrderParams
	}

	if !r.cfg.DryRun && !r.isLiveArmed() {
		return ExecutionResult{}, ErrLiveNotArmed
	}

	// 1. Check persistent KILL_LOCK and in-memory PreTradeGuard lock/kill-switch before any risk-increasing order.
	if r.cfg.KillLockStatePath != "" && !isReduceOnly {
		kl, err := paper.ReadKillLock(r.cfg.KillLockStatePath)
		if err == nil {
			return ExecutionResult{}, fmt.Errorf("%w: %s (trigger=%s)", ErrKillLocked, kl.Reason, kl.Trigger)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return ExecutionResult{}, fmt.Errorf("execution: read kill lock %s: %w", r.cfg.KillLockStatePath, err)
		}
	}
	if tripped, reason, locked, _, _, _ := r.guard.Status(); locked {
		return ExecutionResult{}, guardrails.ErrTradingLocked
	} else if tripped && !isReduceOnly {
		return ExecutionResult{}, fmt.Errorf("%w: %s", guardrails.ErrKillSwitchTripped, reason)
	}

	// 2. Fetch and validate venue quote freshness.
	quote, err := r.venue.GetQuote(ctx, sym)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("execution: get quote %s: %w", sym, err)
	}
	if !isFinitePositive(quote.Bid) || !isFinitePositive(quote.Ask) || quote.Ask < quote.Bid {
		return ExecutionResult{}, fmt.Errorf("%w: invalid bid=%.8f ask=%.8f for %s", ErrStaleQuote, quote.Bid, quote.Ask, sym)
	}
	if quote.Mid == 0 {
		quote.Mid = 0.5 * (quote.Bid + quote.Ask)
	}
	if !isFinitePositive(quote.Mid) {
		return ExecutionResult{}, fmt.Errorf("%w: invalid mid=%.8f for %s", ErrStaleQuote, quote.Mid, sym)
	}
	now := r.clock().UTC()
	if quote.Timestamp.IsZero() {
		return ExecutionResult{}, fmt.Errorf("%w: missing quote timestamp for %s", ErrStaleQuote, sym)
	}
	age := now.Sub(quote.Timestamp)
	if age > r.cfg.MaxQuoteAge {
		return ExecutionResult{}, fmt.Errorf("%w: %s quote age %s > max %s", ErrStaleQuote, sym, age.Round(time.Millisecond), r.cfg.MaxQuoteAge)
	}

	// 3. Compute strictly passive post-only limit price.
	price := PassiveLimitPrice(sideUpper, quote, r.cfg.PostOnlyOffsetBps)
	if !isFinitePositive(price) {
		return ExecutionResult{}, guardrails.ErrInvalidOrderParams
	}

	// 4. Deterministic client order ID + idempotency check (intent-based, independent of live quote price).
	dayBucket := now.Format("2006-01-02")
	clientOrderID := strings.TrimSpace(opts.ClientOrderID)
	if clientOrderID == "" {
		clientOrderID = DeterministicClientOrderIDWithKey(r.venue.Name(), dayBucket, sym, sideUpper, qty, opts.IntentKey)
	}

	r.mu.Lock()
	if r.dailyBucket != dayBucket {
		r.dailyBucket = dayBucket
		r.dailyNotional = 0
	}
	if prev, exists := r.seenOrders[journalKey{id: clientOrderID, dryRun: r.cfg.DryRun}]; exists {
		r.mu.Unlock()
		if !r.cfg.DryRun && strings.EqualFold(prev.Receipt.Status, "PENDING") {
			var (
				latest OrderReceipt
				gerr   error
			)
			if prev.Receipt.OrderID != "" {
				latest, gerr = r.venue.GetOrder(ctx, prev.Receipt.OrderID)
			} else if lookup, ok := r.venue.(ClientOrderLookup); ok {
				latest, gerr = lookup.GetOrderByClientOrderID(ctx, sym, clientOrderID)
			} else {
				latest, gerr = r.venue.GetOrder(ctx, clientOrderID)
			}
			if gerr != nil {
				if !isOrderNotFoundError(gerr) {
					return ExecutionResult{
						ClientOrderID:    clientOrderID,
						Receipt:          prev.Receipt,
						DryRun:           false,
						SkippedDuplicate: true,
						CancelledOnTTL:   prev.CancelledOnTTL,
						NotionalUSD:      prev.NotionalUSD,
					}, fmt.Errorf("execution: reconcile pending order %s: %w", clientOrderID, gerr)
				}
			} else {
				merged := mergeOrderReceipt(prev.Receipt, latest)
				statusChanged := !strings.EqualFold(merged.Status, "PENDING") && strings.TrimSpace(merged.Status) != ""
				if statusChanged || (prev.Receipt.OrderID == "" && merged.OrderID != "") {
					updated := prev
					updated.Receipt = merged
					if strings.EqualFold(merged.Status, "CANCELLED") {
						updated.CancelledOnTTL = true
						updated.NotionalUSD = merged.FilledQty * prev.Price
					} else if isTerminalNonFillStatus(merged.Status) {
						updated.NotionalUSD = merged.FilledQty * prev.Price
					}
					_ = r.recordEntry(updated)
					if statusChanged {
						return ExecutionResult{
							ClientOrderID:    clientOrderID,
							Receipt:          merged,
							DryRun:           false,
							SkippedDuplicate: true,
							CancelledOnTTL:   updated.CancelledOnTTL,
							NotionalUSD:      updated.NotionalUSD,
						}, nil
					}
					prev = updated
				}
			}
		}
		return ExecutionResult{
			ClientOrderID:    clientOrderID,
			Receipt:          prev.Receipt,
			DryRun:           r.cfg.DryRun,
			SkippedDuplicate: true,
			CancelledOnTTL:   prev.CancelledOnTTL,
			NotionalUSD:      prev.NotionalUSD,
		}, nil
	}
	currentDaily := r.dailyNotional
	r.mu.Unlock()

	// 5. Enforce MinOrderUSD and daily notional turnover cap.
	notionalUSD := qty * price
	if math.IsNaN(notionalUSD) || math.IsInf(notionalUSD, 0) || notionalUSD <= 0 {
		return ExecutionResult{}, guardrails.ErrInvalidOrderParams
	}
	if r.cfg.MinOrderUSD > 0 && notionalUSD < r.cfg.MinOrderUSD {
		return ExecutionResult{}, fmt.Errorf("%w: notional $%.2f < min $%.2f", ErrBelowMinOrderUSD, notionalUSD, r.cfg.MinOrderUSD)
	}
	if !isReduceOnly && r.cfg.MaxDailyNotionalUSD > 0 && currentDaily+notionalUSD > r.cfg.MaxDailyNotionalUSD {
		return ExecutionResult{}, fmt.Errorf("%w: projected daily notional $%.2f > cap $%.2f", ErrDailyTurnoverCap, currentDaily+notionalUSD, r.cfg.MaxDailyNotionalUSD)
	}

	// 6. Enforce fee hurdle and PreTradeGuard risk checks.
	spec := r.cfg.FeeHurdle
	spec.ExpectedEdgeBps = expectedEdgeBps
	if err := guardrails.CheckFeeHurdle(spec, isReduceOnly); err != nil {
		return ExecutionResult{}, err
	}
	if err := r.guard.ValidateOrder(guardrails.OrderRequest{
		Symbol:       sym,
		Side:         sideUpper,
		Price:        price,
		Quantity:     qty,
		IsReduceOnly: isReduceOnly,
	}, quote.Bid, quote.Ask, now); err != nil {
		return ExecutionResult{}, err
	}

	// 7. Dry-run path: never invokes VenueClient.PlaceLimitOrder.
	if r.cfg.DryRun {
		receipt := OrderReceipt{
			OrderID:       "dryrun-" + clientOrderID,
			ClientOrderID: clientOrderID,
			Symbol:        sym,
			Side:          sideUpper,
			Price:         price,
			Quantity:      qty,
			PostOnly:      true,
			Status:        "DRY_RUN",
			FilledQty:     0,
			DryRun:        true,
			SubmittedAt:   now,
		}
		entry := JournalEntry{
			Timestamp:     now,
			Venue:         r.venue.Name(),
			ClientOrderID: clientOrderID,
			DayBucket:     dayBucket,
			Symbol:        sym,
			Side:          sideUpper,
			Price:         price,
			Quantity:      qty,
			NotionalUSD:   notionalUSD,
			DryRun:        true,
			Receipt:       receipt,
		}
		if err := r.recordEntry(entry); err != nil {
			return ExecutionResult{}, err
		}
		return ExecutionResult{
			ClientOrderID: clientOrderID,
			Receipt:       receipt,
			DryRun:        true,
			NotionalUSD:   notionalUSD,
		}, nil
	}

	// 8. Live path: defense-in-depth arm check, persist PENDING journal entry before PlaceLimitOrder,
	// then submit post-only limit order so crashes/timeouts cannot cause duplicate orders on retry.
	if !r.isLiveArmed() {
		return ExecutionResult{}, ErrLiveNotArmed
	}
	pendingEntry := JournalEntry{
		Timestamp:     now,
		Venue:         r.venue.Name(),
		ClientOrderID: clientOrderID,
		DayBucket:     dayBucket,
		Symbol:        sym,
		Side:          sideUpper,
		Price:         price,
		Quantity:      qty,
		NotionalUSD:   notionalUSD,
		DryRun:        false,
		Receipt: OrderReceipt{
			ClientOrderID: clientOrderID,
			Symbol:        sym,
			Side:          sideUpper,
			Price:         price,
			Quantity:      qty,
			PostOnly:      true,
			Status:        "PENDING",
			DryRun:        false,
			SubmittedAt:   now,
		},
	}
	if err := r.recordEntry(pendingEntry); err != nil {
		return ExecutionResult{}, err
	}
	receipt, err := r.venue.PlaceLimitOrder(ctx, LimitOrderRequest{
		ClientOrderID: clientOrderID,
		Symbol:        sym,
		Side:          sideUpper,
		Price:         price,
		Quantity:      qty,
		PostOnly:      true,
	})
	if err != nil {
		rejectedEntry := pendingEntry
		rejectedEntry.NotionalUSD = 0
		rejectedEntry.Receipt.Status = "REJECTED"
		_ = r.recordEntry(rejectedEntry)
		r.mu.Lock()
		key := journalKey{id: clientOrderID, dryRun: false}
		if prev, exists := r.seenOrders[key]; exists {
			if prev.DayBucket == r.dailyBucket && !math.IsNaN(prev.NotionalUSD) && !math.IsInf(prev.NotionalUSD, 0) {
				r.dailyNotional -= prev.NotionalUSD
			}
			delete(r.seenOrders, key)
		}
		r.mu.Unlock()
		return ExecutionResult{}, err
	}
	if receipt.ClientOrderID == "" {
		receipt.ClientOrderID = clientOrderID
	}
	receipt.DryRun = false
	if receipt.SubmittedAt.IsZero() {
		receipt.SubmittedAt = now
	}
	cancelledOnTTL := false
	if r.cfg.OrderTTL > 0 && receipt.OrderID != "" && !isOrderFilled(receipt, qty) {
		var ttlErr error
		receipt, cancelledOnTTL, ttlErr = r.waitForFillOrCancelTTL(ctx, receipt, qty)
		if ttlErr != nil {
			ttlNotional := notionalUSD
			if cancelledOnTTL || strings.EqualFold(receipt.Status, "CANCELLED") || isTerminalNonFillStatus(receipt.Status) {
				ttlNotional = receipt.FilledQty * price
			}
			_ = r.recordEntry(JournalEntry{
				Timestamp:      now,
				Venue:          r.venue.Name(),
				ClientOrderID:  clientOrderID,
				DayBucket:      dayBucket,
				Symbol:         sym,
				Side:           sideUpper,
				Price:          price,
				Quantity:       qty,
				NotionalUSD:    ttlNotional,
				DryRun:         false,
				CancelledOnTTL: cancelledOnTTL,
				Receipt:        receipt,
			})
			return ExecutionResult{
				ClientOrderID:  clientOrderID,
				Receipt:        receipt,
				DryRun:         false,
				CancelledOnTTL: cancelledOnTTL,
				NotionalUSD:    ttlNotional,
			}, ttlErr
		}
	}
	recordedNotional := notionalUSD
	if cancelledOnTTL || strings.EqualFold(receipt.Status, "CANCELLED") || isTerminalNonFillStatus(receipt.Status) {
		recordedNotional = receipt.FilledQty * price
	}
	entry := JournalEntry{
		Timestamp:      now,
		Venue:          r.venue.Name(),
		ClientOrderID:  clientOrderID,
		DayBucket:      dayBucket,
		Symbol:         sym,
		Side:           sideUpper,
		Price:          price,
		Quantity:       qty,
		NotionalUSD:    recordedNotional,
		DryRun:         false,
		CancelledOnTTL: cancelledOnTTL,
		Receipt:        receipt,
	}
	if err := r.recordEntry(entry); err != nil {
		return ExecutionResult{}, err
	}
	return ExecutionResult{
		ClientOrderID:  clientOrderID,
		Receipt:        receipt,
		DryRun:         false,
		CancelledOnTTL: cancelledOnTTL,
		NotionalUSD:    recordedNotional,
	}, nil
}

func isTerminalNonFillStatus(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "CANCELED", "EXPIRED", "FAILED", "REJECTED":
		return true
	default:
		return false
	}
}

func (r *LiveRouter) waitForFillOrCancelTTL(ctx context.Context, receipt OrderReceipt, targetQty float64) (OrderReceipt, bool, error) {
	pollInterval := r.cfg.OrderPollInterval
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	timer := time.NewTimer(r.cfg.OrderTTL)
	defer timer.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	cancelAndRefresh := func() OrderReceipt {
		cancelCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if cerr := r.CancelOrder(cancelCtx, receipt.OrderID); cerr == nil {
			if receipt.Status == "" || strings.EqualFold(receipt.Status, "OPEN") || strings.EqualFold(receipt.Status, "NEW") {
				receipt.Status = "CANCELLED"
			}
		}
		if latest, gerr := r.venue.GetOrder(cancelCtx, receipt.OrderID); gerr == nil {
			receipt = mergeOrderReceipt(receipt, latest)
		}
		return receipt
	}

	for {
		if ctx.Err() != nil {
			return cancelAndRefresh(), false, ctx.Err()
		}
		select {
		case <-ctx.Done():
			return cancelAndRefresh(), false, ctx.Err()
		case <-timer.C:
			if ctx.Err() != nil {
				return cancelAndRefresh(), false, ctx.Err()
			}
			if latest, gerr := r.venue.GetOrder(ctx, receipt.OrderID); gerr == nil {
				receipt = mergeOrderReceipt(receipt, latest)
				if isOrderFilled(receipt, targetQty) {
					return receipt, false, nil
				}
			}
			if cerr := r.CancelOrder(ctx, receipt.OrderID); cerr != nil {
				if latest, gerr := r.venue.GetOrder(ctx, receipt.OrderID); gerr == nil {
					receipt = mergeOrderReceipt(receipt, latest)
					if isOrderFilled(receipt, targetQty) {
						return receipt, false, nil
					}
				}
				return receipt, false, fmt.Errorf("execution: cancel order %s on TTL expiry: %w", receipt.OrderID, cerr)
			}
			if latest, gerr := r.venue.GetOrder(ctx, receipt.OrderID); gerr == nil {
				receipt = mergeOrderReceipt(receipt, latest)
				if isOrderFilled(receipt, targetQty) {
					return receipt, false, nil
				}
			}
			if receipt.Status == "" || strings.EqualFold(receipt.Status, "OPEN") || strings.EqualFold(receipt.Status, "NEW") {
				receipt.Status = "CANCELLED"
			}
			return receipt, true, nil
		case <-ticker.C:
			latest, gerr := r.venue.GetOrder(ctx, receipt.OrderID)
			if gerr != nil {
				continue
			}
			receipt = mergeOrderReceipt(receipt, latest)
			if isOrderFilled(receipt, targetQty) {
				return receipt, false, nil
			}
		}
	}
}

func isOrderFilled(rec OrderReceipt, targetQty float64) bool {
	return strings.EqualFold(strings.TrimSpace(rec.Status), "FILLED") ||
		(targetQty > 0 && rec.FilledQty >= targetQty)
}

func isOrderNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrOrderNotFound) {
		return true
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) && statusErr.status == 404 {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "order not found")
}

func mergeOrderReceipt(base, latest OrderReceipt) OrderReceipt {
	if latest.OrderID == "" {
		latest.OrderID = base.OrderID
	}
	if latest.ClientOrderID == "" {
		latest.ClientOrderID = base.ClientOrderID
	}
	if latest.Symbol == "" {
		latest.Symbol = base.Symbol
	}
	if latest.Side == "" {
		latest.Side = base.Side
	}
	if latest.Price <= 0 {
		latest.Price = base.Price
	}
	if latest.Quantity <= 0 {
		latest.Quantity = base.Quantity
	}
	if latest.FilledQty < base.FilledQty {
		latest.FilledQty = base.FilledQty
	}
	if !latest.PostOnly {
		latest.PostOnly = base.PostOnly
	}
	if strings.TrimSpace(latest.Status) == "" {
		latest.Status = base.Status
	}
	if latest.SubmittedAt.IsZero() {
		latest.SubmittedAt = base.SubmittedAt
	}
	latest.DryRun = false
	return latest
}

// CancelOrder cancels an order on the venue (or returns nil for dry-run simulated IDs).
func (r *LiveRouter) CancelOrder(ctx context.Context, orderID string) error {
	if r.cfg.DryRun || strings.HasPrefix(orderID, "dryrun-") {
		return nil
	}
	return r.venue.CancelOrder(ctx, orderID)
}

// GetOrder retrieves an order status from the local dry-run journal or from the venue.
func (r *LiveRouter) GetOrder(ctx context.Context, orderID string) (OrderReceipt, error) {
	if strings.HasPrefix(orderID, "dryrun-") {
		r.mu.Lock()
		defer r.mu.Unlock()
		cid := strings.TrimPrefix(orderID, "dryrun-")
		if entry, ok := r.seenOrders[journalKey{id: cid, dryRun: true}]; ok {
			return entry.Receipt, nil
		}
	}
	return r.venue.GetOrder(ctx, orderID)
}

func (r *LiveRouter) isLiveArmed() bool {
	return r.cfg.LiveArmToken == LiveArmConfirmToken && os.Getenv(LiveArmEnvVar) == LiveArmEnvValue
}

func (r *LiveRouter) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *LiveRouter) loadJournal() error {
	f, err := os.Open(r.cfg.JournalPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	today := r.clock().UTC().Format("2006-01-02")
	r.dailyBucket = today

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var entry JournalEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return fmt.Errorf("line %d: %w", lineNo, err)
		}
		if entry.ClientOrderID == "" {
			continue
		}
		key := journalKey{id: entry.ClientOrderID, dryRun: entry.DryRun}
		if strings.EqualFold(entry.Receipt.Status, "REJECTED") && entry.Receipt.OrderID == "" && entry.Receipt.FilledQty == 0 {
			delete(r.seenOrders, key)
			continue
		}
		r.seenOrders[key] = entry
	}
	if err := sc.Err(); err != nil {
		return err
	}
	for _, entry := range r.seenOrders {
		if entry.DryRun == r.cfg.DryRun && entry.DayBucket == today && !math.IsNaN(entry.NotionalUSD) && !math.IsInf(entry.NotionalUSD, 0) {
			r.dailyNotional += entry.NotionalUSD
		}
	}
	return nil
}

func (r *LiveRouter) recordEntry(entry JournalEntry) error {
	if r.cfg.JournalPath != "" {
		if err := os.MkdirAll(filepath.Dir(r.cfg.JournalPath), 0o755); err != nil {
			return fmt.Errorf("execution: create journal dir: %w", err)
		}
		line, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("execution: marshal journal entry: %w", err)
		}
		f, err := os.OpenFile(r.cfg.JournalPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("execution: open journal %s: %w", r.cfg.JournalPath, err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			_ = f.Close()
			return fmt.Errorf("execution: write journal %s: %w", r.cfg.JournalPath, err)
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return fmt.Errorf("execution: sync journal %s: %w", r.cfg.JournalPath, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("execution: close journal %s: %w", r.cfg.JournalPath, err)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dailyBucket != entry.DayBucket {
		r.dailyBucket = entry.DayBucket
		r.dailyNotional = 0
	}
	key := journalKey{id: entry.ClientOrderID, dryRun: entry.DryRun}
	if prev, exists := r.seenOrders[key]; exists && prev.DayBucket == entry.DayBucket && !math.IsNaN(prev.NotionalUSD) && !math.IsInf(prev.NotionalUSD, 0) {
		r.dailyNotional -= prev.NotionalUSD
	}
	r.seenOrders[key] = entry
	r.dailyNotional += entry.NotionalUSD
	return nil
}

// HighWaterMarkState persists monotonic peak NAV and UTC daily peak NAV across CLI executions.
type HighWaterMarkState struct {
	PeakNavUSD   float64 `json:"peak_nav_usd"`
	LastNavUSD   float64 `json:"last_nav_usd"`
	UpdatedAtUTC string  `json:"updated_at_utc"`
	DailyPeakUSD float64 `json:"daily_peak_usd"`
	DailyDateUTC string  `json:"daily_date_utc"`
}

// LoadHighWaterMark reads persisted high-water mark state from path.
// When path does not exist yet, it returns a zero HighWaterMarkState and nil error.
func LoadHighWaterMark(path string) (HighWaterMarkState, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return HighWaterMarkState{}, errors.New("execution: high-water mark path is required")
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return HighWaterMarkState{}, nil
	}
	if err != nil {
		return HighWaterMarkState{}, fmt.Errorf("execution: read high-water mark %s: %w", path, err)
	}
	var st HighWaterMarkState
	if err := json.Unmarshal(b, &st); err != nil {
		return HighWaterMarkState{}, fmt.Errorf("execution: parse high-water mark %s: %w", path, err)
	}
	return st, nil
}

// UpdateAndSaveHighWaterMark updates monotonic PeakNavUSD and UTC-daily DailyPeakUSD against
// currentNAV and writes the updated HighWaterMarkState atomically via .tmp + os.Rename.
func UpdateAndSaveHighWaterMark(path string, currentNAV float64, now time.Time) (HighWaterMarkState, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return HighWaterMarkState{}, errors.New("execution: high-water mark path is required")
	}
	if !isFinitePositive(currentNAV) {
		return HighWaterMarkState{}, fmt.Errorf("execution: invalid current NAV %.4f for high-water mark", currentNAV)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	prev, err := LoadHighWaterMark(path)
	if err != nil {
		return HighWaterMarkState{}, err
	}

	peak := prev.PeakNavUSD
	if math.IsNaN(peak) || math.IsInf(peak, 0) || peak < currentNAV {
		peak = currentNAV
	}
	today := now.Format("2006-01-02")
	dailyPeak := prev.DailyPeakUSD
	if prev.DailyDateUTC != today || math.IsNaN(dailyPeak) || math.IsInf(dailyPeak, 0) || dailyPeak < currentNAV {
		dailyPeak = currentNAV
	}

	next := HighWaterMarkState{
		PeakNavUSD:   peak,
		LastNavUSD:   currentNAV,
		UpdatedAtUTC: now.Format(time.RFC3339),
		DailyPeakUSD: dailyPeak,
		DailyDateUTC: today,
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return HighWaterMarkState{}, fmt.Errorf("execution: create hwm dir: %w", err)
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return HighWaterMarkState{}, fmt.Errorf("execution: marshal high-water mark: %w", err)
	}
	data = append(data, '\n')

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return HighWaterMarkState{}, fmt.Errorf("execution: write high-water mark tmp %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return HighWaterMarkState{}, fmt.Errorf("execution: write high-water mark tmp %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return HighWaterMarkState{}, fmt.Errorf("execution: sync high-water mark tmp %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return HighWaterMarkState{}, fmt.Errorf("execution: close high-water mark tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return HighWaterMarkState{}, fmt.Errorf("execution: rename high-water mark %s: %w", path, err)
	}
	return next, nil
}

func isFinitePositive(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0
}
