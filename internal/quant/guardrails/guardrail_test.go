package guardrails

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

func TestPreTradeGuard_BasicAndFatFinger(t *testing.T) {
	cfg := DefaultGuardrailConfig()
	cfg.Exposure.MaxGrossUSD = 20000.0
	cfg.Exposure.MaxNetUSD = 10000.0
	cfg.Exposure.MaxOrderQtyUSD = 3000.0
	cfg.MaxOrderQty = 0.50
	cfg.FatFingerMaxDeviation = 0.015 // 1.5%

	guard := NewPreTradeGuard(cfg)
	now := time.Now()

	guard.UpdatePortfolioState(10000.0, 10000.0, map[string]float64{"BTCUSDT": 0.0}, map[string]float64{"BTCUSDT": 60000.0}, now)

	// Valid order: buy 0.02 BTC @ 60000 = $1200
	validOrd := OrderRequest{
		Symbol:   "BTCUSDT",
		Side:     "BUY",
		Price:    60000.0,
		Quantity: 0.02,
	}
	if err := guard.ValidateOrder(validOrd, 59990.0, 60010.0, now); err != nil {
		t.Fatalf("expected valid order to pass, got: %v", err)
	}

	// Invalid params (NaN, Inf, <=0)
	if err := guard.ValidateOrder(OrderRequest{Symbol: "BTCUSDT", Side: "BUY", Price: math.NaN(), Quantity: 0.01}, 60000, 60010, now); !errors.Is(err, ErrInvalidOrderParams) {
		t.Errorf("expected ErrInvalidOrderParams for NaN price, got %v", err)
	}
	if err := guard.ValidateOrder(OrderRequest{Symbol: "BTCUSDT", Side: "BUY", Price: 60000, Quantity: -0.01}, 60000, 60010, now); !errors.Is(err, ErrInvalidOrderParams) {
		t.Errorf("expected ErrInvalidOrderParams for negative qty, got %v", err)
	}

	// Fat finger quantity > 0.50 BTC
	fatQtyOrd := OrderRequest{
		Symbol:   "BTCUSDT",
		Side:     "BUY",
		Price:    60000.0,
		Quantity: 0.60,
	}
	if err := guard.ValidateOrder(fatQtyOrd, 59990.0, 60010.0, now); !errors.Is(err, ErrFatFingerQty) {
		t.Errorf("expected ErrFatFingerQty for quantity 0.60, got: %v", err)
	}

	// Fat finger notional USD > $3000
	fatUSDOrd := OrderRequest{
		Symbol:   "BTCUSDT",
		Side:     "BUY",
		Price:    60000.0,
		Quantity: 0.06, // $3,600
	}
	if err := guard.ValidateOrder(fatUSDOrd, 59990.0, 60010.0, now); !errors.Is(err, ErrFatFingerQty) {
		t.Errorf("expected ErrFatFingerQty for notional $3600, got: %v", err)
	}

	// Fat finger price collar: Buy price > +1.5% above ask
	// ask = 60000, 1.5% = +900 => max buy price 60900
	badBuyOrd := OrderRequest{
		Symbol:   "BTCUSDT",
		Side:     "BUY",
		Price:    61500.0, // +2.5%
		Quantity: 0.01,
	}
	if err := guard.ValidateOrder(badBuyOrd, 59990.0, 60000.0, now); !errors.Is(err, ErrFatFingerPrice) {
		t.Errorf("expected ErrFatFingerPrice for buy price 61500, got: %v", err)
	}

	// Fat finger price collar: Sell price < -1.5% below bid
	// bid = 60000, 1.5% = -900 => min sell price 59100
	badSellOrd := OrderRequest{
		Symbol:   "BTCUSDT",
		Side:     "SELL",
		Price:    58500.0, // -2.5%
		Quantity: 0.01,
	}
	if err := guard.ValidateOrder(badSellOrd, 60000.0, 60010.0, now); !errors.Is(err, ErrFatFingerPrice) {
		t.Errorf("expected ErrFatFingerPrice for sell price 58500, got: %v", err)
	}
}

func TestPreTradeGuard_ExposureLimits(t *testing.T) {
	cfg := DefaultGuardrailConfig()
	cfg.Exposure.MaxGrossUSD = 10000.0
	cfg.Exposure.MaxNetUSD = 6000.0
	cfg.Exposure.MaxConcentrationPct = 80.0
	cfg.Exposure.MaxOrderQtyUSD = 10000.0
	cfg.MaxOrderQty = 1.0

	guard := NewPreTradeGuard(cfg)
	now := time.Now()

	// Initial portfolio: equity = $5000, holding 0.08 BTC ($4,800)
	positions := map[string]float64{"BTCUSDT": 0.08}
	marks := map[string]float64{"BTCUSDT": 60000.0}
	guard.UpdatePortfolioState(5000.0, 5000.0, positions, marks, now)

	// Attempt to buy another 0.04 BTC ($2,400) -> New net = $7,200 > MaxNetUSD ($6,000)
	overNetOrd := OrderRequest{
		Symbol:   "BTCUSDT",
		Side:     "BUY",
		Price:    60000.0,
		Quantity: 0.04,
	}
	if err := guard.ValidateOrder(overNetOrd, 59990.0, 60010.0, now); !errors.Is(err, ErrNetExposureLimit) {
		t.Errorf("expected ErrNetExposureLimit, got: %v", err)
	}

	// Concentration check: equity = $5000, 80% = $4000.
	// Holding 0.08 BTC is already $4,800 (96% concentration). Any opening buy should fail concentration.
	smallBuy := OrderRequest{
		Symbol:   "BTCUSDT",
		Side:     "BUY",
		Price:    60000.0,
		Quantity: 0.01,
	}
	if err := guard.ValidateOrder(smallBuy, 59990.0, 60010.0, now); !errors.Is(err, ErrConcentrationLimit) {
		t.Errorf("expected ErrConcentrationLimit, got: %v", err)
	}

	// Reduce-only sell order should pass even when exposure/concentration limits are exceeded!
	reduceSell := OrderRequest{
		Symbol:       "BTCUSDT",
		Side:         "SELL",
		Price:        60000.0,
		Quantity:     0.04,
		IsReduceOnly: true,
	}
	if err := guard.ValidateOrder(reduceSell, 59990.0, 60010.0, now); err != nil {
		t.Errorf("expected reduce-only order to pass freely, got: %v", err)
	}
}

func TestPreTradeGuard_KillSwitch(t *testing.T) {
	cfg := DefaultGuardrailConfig()
	cfg.MaxDailyDrawdownPct = 2.0 // -2.0% drawdown limit
	guard := NewPreTradeGuard(cfg)
	now := time.Now()

	// Peak equity = 10,000
	guard.UpdatePortfolioState(10000.0, 10000.0, nil, nil, now)

	isTripped, _, _, peak, curr, _ := guard.Status()
	if isTripped || peak != 10000.0 || curr != 10000.0 {
		t.Fatalf("unexpected state: tripped=%v, peak=%f, curr=%f", isTripped, peak, curr)
	}

	// Equity drops to 9,850 (-1.5% drawdown) -> below 2.0%, no trip
	guard.UpdatePortfolioState(9850.0, 10000.0, nil, nil, now.Add(time.Minute))
	isTripped, _, _, _, _, _ = guard.Status()
	if isTripped {
		t.Fatalf("kill switch should NOT trip at -1.5%% drawdown")
	}

	// Equity drops to 9,790 (-2.1% drawdown) with open long position 0.01 BTC -> trips!
	openPositions := map[string]float64{"BTCUSDT": 0.01}
	marks := map[string]float64{"BTCUSDT": 60000.0}
	guard.UpdatePortfolioState(9790.0, 10000.0, openPositions, marks, now.Add(2*time.Minute))
	isTripped, reason, _, _, _, _ := guard.Status()
	if !isTripped {
		t.Fatalf("kill switch SHOULD trip at -2.1%% drawdown")
	}
	t.Logf("Kill switch tripped with reason: %s", reason)

	// Opening order should be blocked
	openOrd := OrderRequest{
		Symbol:   "BTCUSDT",
		Side:     "BUY",
		Price:    60000.0,
		Quantity: 0.01,
	}
	if err := guard.ValidateOrder(openOrd, 59990.0, 60010.0, now.Add(3*time.Minute)); !errors.Is(err, ErrKillSwitchTripped) {
		t.Errorf("expected ErrKillSwitchTripped, got: %v", err)
	}

	// Pseudo reduce-only order that exceeds open position (e.g. SELL 0.05 when holding 0.01) must be rejected
	fakeReduceOrd := OrderRequest{
		Symbol:       "BTCUSDT",
		Side:         "SELL",
		Price:        60000.0,
		Quantity:     0.05,
		IsReduceOnly: true,
	}
	if err := guard.ValidateOrder(fakeReduceOrd, 59990.0, 60010.0, now.Add(3*time.Minute)); !errors.Is(err, ErrInvalidReduceOnly) {
		t.Errorf("expected ErrInvalidReduceOnly for oversize reduce-only order, got: %v", err)
	}

	// Authentic reduce-only order (closing exact 0.01 BTC position) should be permitted to flatten positions
	closeOrd := OrderRequest{
		Symbol:       "BTCUSDT",
		Side:         "SELL",
		Price:        60000.0,
		Quantity:     0.01,
		IsReduceOnly: true,
	}
	if err := guard.ValidateOrder(closeOrd, 59990.0, 60010.0, now.Add(3*time.Minute)); err != nil {
		t.Errorf("expected reduce-only order to be allowed during kill switch, got: %v", err)
	}

	// Admin resets kill switch
	guard.ResetKillSwitch()
	isTripped, _, _, _, _, _ = guard.Status()
	if isTripped {
		t.Fatalf("kill switch should be cleared after reset")
	}

	// Order now allowed
	if err := guard.ValidateOrder(openOrd, 59990.0, 60010.0, now.Add(4*time.Minute)); err != nil {
		t.Errorf("expected order to pass after kill switch reset, got: %v", err)
	}
}

func TestPreTradeGuard_RateLimiter(t *testing.T) {
	cfg := DefaultGuardrailConfig()
	cfg.RateLimitRate = 5.0     // 5 tokens/sec
	cfg.RateLimitCapacity = 3.0 // Burst of 3
	guard := NewPreTradeGuard(cfg)
	now := time.Now()
	guard.UpdatePortfolioState(10000.0, 10000.0, nil, nil, now)

	ord := OrderRequest{
		Symbol:   "BTCUSDT",
		Side:     "BUY",
		Price:    60000.0,
		Quantity: 0.01,
	}

	// First 3 orders in rapid succession should pass (consuming burst capacity of 3)
	for i := 0; i < 3; i++ {
		if err := guard.ValidateOrder(ord, 59990.0, 60010.0, now); err != nil {
			t.Fatalf("order %d failed: %v", i, err)
		}
	}

	// 4th order at the exact same instant should be rate limited
	if err := guard.ValidateOrder(ord, 59990.0, 60010.0, now); !errors.Is(err, ErrRateLimitExceeded) {
		t.Errorf("expected ErrRateLimitExceeded on 4th burst order, got: %v", err)
	}

	// After 200ms (1 token refilled at 5/sec), next order passes
	futureTime := now.Add(205 * time.Millisecond)
	if err := guard.ValidateOrder(ord, 59990.0, 60010.0, futureTime); err != nil {
		t.Errorf("expected order after refill to pass, got: %v", err)
	}
}

func TestPreTradeGuard_ConcurrentSafety(t *testing.T) {
	cfg := DefaultGuardrailConfig()
	guard := NewPreTradeGuard(cfg)
	guard.UpdatePortfolioState(50000.0, 50000.0, nil, nil, time.Now())

	var wg sync.WaitGroup
	workers := 10
	ordersPerWorker := 50

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < ordersPerWorker; i++ {
				now := time.Now().Add(time.Duration(i*100) * time.Millisecond)
				ord := OrderRequest{
					Symbol:   "BTCUSDT",
					Side:     "BUY",
					Price:    60000.0,
					Quantity: 0.001,
				}
				_ = guard.ValidateOrder(ord, 59990.0, 60010.0, now)
				if i%10 == 0 {
					guard.UpdatePortfolioState(50000.0+float64(workerID), 50000.0+float64(workerID), nil, nil, now)
				}
			}
		}(w)
	}

	wg.Wait()
}
