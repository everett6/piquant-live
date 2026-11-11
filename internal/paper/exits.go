package paper

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// lossStreakCooldown is how long a symbol must sit flat after its last close before a
// loss streak (and the swing-mode/leakage gates it drives) is cleared.
const lossStreakCooldown = 30 * time.Minute

// winLossEpsilon separates wins and losses from scratch (break-even) round trips.
const winLossEpsilon = 1e-9

// resetPositionLifecycle clears all per-position state when inventory returns to flat,
// so a later entry never inherits a stale peak, trailing stop or fee balance.
func resetPositionLifecycle(pos *Position) {
	pos.Size = 0
	pos.AvgEntryPrice = 0
	pos.PeakPrice = 0
	pos.TrailingStop = 0
	pos.HoldingSec = 0
	pos.EntryFeesUSDT = 0
}

// recordCloseOutcomeLocked classifies a round-trip close as win, loss or scratch and
// updates the loss streak and win rate. roundTripPnL must include entry and exit fees.
// Caller must hold e.mu.
func (e *DryRunEngine) recordCloseOutcomeLocked(symbol string, roundTripPnL float64, evaluateViability bool) {
	sm := e.symMicro[strings.ToUpper(symbol)]
	switch {
	case roundTripPnL > winLossEpsilon:
		e.state.WinningTrades++
		if sm != nil {
			sm.ConsecutiveLosses = 0
		}
	case roundTripPnL < -winLossEpsilon:
		e.state.LosingTrades++
		if sm != nil {
			sm.ConsecutiveLosses++
		}
	default:
		// Scratch: neither a win nor a loss, and does not extend a loss streak.
	}
	if closed := e.state.WinningTrades + e.state.LosingTrades; closed > 0 {
		e.state.WinRate = float64(e.state.WinningTrades) / float64(closed) * 100.0
	}
	if sm == nil {
		return
	}
	sm.LastCloseAt = time.Now()
	sm.RecentPnlWindow = append(sm.RecentPnlWindow, roundTripPnL)
	if len(sm.RecentPnlWindow) > 10 {
		sm.RecentPnlWindow = sm.RecentPnlWindow[1:]
	}
	if !evaluateViability {
		return
	}
	// If tight scalping is leaking money or suffering adverse markouts, stop scalping entirely
	viable, reason := sm.EvaluateScalpingViability(e.tcaTracker)
	if !viable {
		if !sm.ScalpingDisabled {
			sm.ScalpingDisabled = true
			sm.ScalpingDisabledReason = reason
			fmt.Printf("[SELF-IMPROVING] %s: Scalping disabled (%s) -> Transitioned to Patient Swing Liquidity Provision.\n", symbol, reason)
		}
		e.maybeTriggerAutoRecalibrationLocked(symbol)
	} else if sm.ScalpingDisabled && sm.ConsecutiveLosses == 0 && (sm.VpinCalc == nil || sm.VpinCalc.VPIN() < 0.55) {
		sm.ScalpingDisabled = false
		sm.ScalpingDisabledReason = ""
		fmt.Printf("[SELF-IMPROVING] %s: Market conditions normalized -> Resuming standard two-sided GLFT scalping.\n", symbol)
	}
}

// takerExitLocked closes the whole position in symbol immediately by crossing the
// spread (taker fee + size-dependent slippage) and cancels resting quotes.
// Used for stop-loss, trailing-stop, toxic-flow, invalidation and time-stop exits.
// Caller must hold e.mu.
func (e *DryRunEngine) takerExitLocked(symbol string, bestBid, bestAsk, midPrice float64, reason string) {
	pos := e.state.Positions[symbol]
	if pos == nil || math.Abs(pos.Size) < 1e-8 {
		return
	}
	qty := math.Abs(pos.Size)
	long := pos.Size > 0
	side := "SELL"
	ref := bestBid
	if !long {
		side = "BUY"
		ref = bestAsk
	}
	if ref <= 0 || math.IsNaN(ref) || math.IsInf(ref, 0) {
		ref = midPrice
	}
	if ref <= 0 || math.IsNaN(ref) || math.IsInf(ref, 0) {
		return
	}
	slipBps := e.cfg.KylesLambda * math.Sqrt(qty)
	execPrice := ref * (1.0 - slipBps*1e-4)
	if !long {
		execPrice = ref * (1.0 + slipBps*1e-4)
	}

	valUSDT := execPrice * qty
	feeUSDT := valUSDT * (e.cfg.TakerFeeBps * 1e-4)
	realized := qty * (execPrice - pos.AvgEntryPrice)
	if !long {
		realized = qty * (pos.AvgEntryPrice - execPrice)
	}
	net := realized - feeUSDT
	roundTrip := net - pos.EntryFeesUSDT

	if e.tcaTracker != nil {
		sideInt := int8(-1)
		if !long {
			sideInt = 1
		}
		e.tcaTracker.RecordFill(uint64(e.state.TotalTrades), symbol, sideInt, execPrice, midPrice, time.Now().UnixNano())
	}

	e.state.Cash += net
	e.state.RealizedPnL += net
	e.state.TotalTrades++
	pos.RealizedPnL += net
	pos.UnrealizedPnL = 0
	pos.TotalTrades++
	pos.TakerTrades++
	pos.TotalVolumeUSDT += valUSDT
	resetPositionLifecycle(pos)
	e.activeOrders[symbol] = nil
	if sm := e.symMicro[strings.ToUpper(symbol)]; sm != nil {
		sm.InventorySince = time.Time{}
	}
	if e.riskSafeguard != nil {
		e.riskSafeguard.OnFill(net, 0)
	}
	e.recordCloseOutcomeLocked(symbol, roundTrip, true)
	e.updateTaxReservesLocked()

	slippageVsMidBps := 0.0
	if midPrice > 0 {
		slippageVsMidBps = math.Abs(execPrice-midPrice) / midPrice * 1e4
	}
	e.appendTradeRecord(TradeRecord{
		Timestamp:   time.Now().UTC(),
		Symbol:      symbol,
		Side:        side,
		Role:        "TAKER",
		Price:       execPrice,
		Size:        qty,
		ValueUSDT:   valUSDT,
		FeeUSDT:     feeUSDT,
		SlippageBps: slippageVsMidBps,
		PnLRealized: net,
		Strategy:    "EXIT_" + reason,
		DataSource:  e.dataSourceLocked(),
	})
}

func (e *DryRunEngine) dataSourceLocked() string {
	if e.state.DataSource == "" {
		return "live"
	}
	return e.state.DataSource
}

// contractSize returns the futures contract size for symbol, or 0 for spot lots.
func (e *DryRunEngine) contractSize(symbol string) float64 {
	u := strings.ToUpper(symbol)
	if cs, ok := e.cfg.ContractSizes[u]; ok && cs > 0 {
		return cs
	}
	for prefix, cs := range e.cfg.ContractSizes {
		if cs > 0 && strings.HasPrefix(u, prefix) {
			return cs
		}
	}
	return 0
}

// applyFuturesSizing re-sizes a quote for a leveraged futures account: inventory may use
// up to effLev x equity, the clip risks riskPct of equity at the stop, and both are
// rounded down to whole contracts. A single contract is allowed when the risk-sized
// clip is smaller than one contract, as long as one contract risks at most
// maxSingleContractRiskPct of equity and fits the leverage cap. Returns (0, 0) when
// even one contract does not fit.
func (e *DryRunEngine) applyFuturesSizing(symbol string, equity, price, effLev, atrBps float64) (clipQty, maxInvQty float64) {
	const riskPct = 0.015
	const maxSingleContractRiskPct = 0.025
	cs := e.contractSize(symbol)
	if cs <= 0 || equity <= 0 || price <= 0 {
		return 0, 0
	}
	if effLev <= 0 {
		effLev = 1
	}
	if e.cfg.MaxLeverage > 0 && effLev > e.cfg.MaxLeverage {
		effLev = e.cfg.MaxLeverage
	}
	n := float64(len(e.cfg.Symbols))
	if n < 1 {
		n = 1
	}
	stopBps := math.Max(e.cfg.StopLossBps, 1.25*math.Max(atrBps, 0))
	if stopBps <= 0 {
		stopBps = 60
	}
	// Each symbol gets an equal share of the account's leveraged buying power.
	maxInvQty = math.Floor(equity*effLev/n/price/cs+1e-9) * cs
	riskQty := equity * riskPct / (price * stopBps * 1e-4)
	clipQty = math.Floor(math.Min(riskQty, maxInvQty)/cs+1e-9) * cs
	if clipQty < cs {
		oneContractRisk := cs * price * stopBps * 1e-4
		if oneContractRisk <= equity*maxSingleContractRiskPct && cs*price <= equity*effLev {
			clipQty = cs
			if maxInvQty < cs {
				maxInvQty = cs
			}
		} else {
			return 0, 0
		}
	}

	// Account-wide cap: gross notional across all symbols may not exceed MaxLeverage x equity.
	maxLev := e.cfg.MaxLeverage
	if maxLev <= 0 {
		maxLev = effLev
	}
	e.mu.RLock()
	otherGross, ownQty := 0.0, 0.0
	for sym, pos := range e.state.Positions {
		if pos == nil || pos.MarkPrice <= 0 {
			continue
		}
		if strings.EqualFold(sym, symbol) {
			ownQty = math.Abs(pos.Size)
			continue
		}
		otherGross += math.Abs(pos.Size) * pos.MarkPrice
	}
	e.mu.RUnlock()
	room := equity*maxLev - otherGross - ownQty*price
	if room < 0 {
		room = 0
	}
	if roomQty := math.Floor(room/price/cs+1e-9) * cs; clipQty > roomQty {
		clipQty = roomQty
	}
	if capInv := math.Floor((equity*maxLev-otherGross)/price/cs+1e-9) * cs; maxInvQty > capInv {
		maxInvQty = math.Max(capInv, 0)
	}
	if clipQty < cs {
		return 0, math.Max(maxInvQty, 0)
	}
	return clipQty, maxInvQty
}

// accrueFundingLocked charges futures funding on open notional since the last call.
// Funding is charged to the holder regardless of side (a conservative assumption: the
// real rate is signed). It counts against the position's round trip. Caller holds e.mu.
func (e *DryRunEngine) accrueFundingLocked(now time.Time) {
	if e.cfg.FundingBpsPerHour <= 0 {
		e.lastFundingAccrual = now
		return
	}
	if e.lastFundingAccrual.IsZero() || now.Before(e.lastFundingAccrual) {
		e.lastFundingAccrual = now
		return
	}
	dtHours := now.Sub(e.lastFundingAccrual).Hours()
	e.lastFundingAccrual = now
	for _, pos := range e.state.Positions {
		if pos == nil || math.Abs(pos.Size) < 1e-8 || pos.MarkPrice <= 0 {
			continue
		}
		cost := math.Abs(pos.Size) * pos.MarkPrice * e.cfg.FundingBpsPerHour * 1e-4 * dtHours
		e.state.Cash -= cost
		e.state.RealizedPnL -= cost
		pos.RealizedPnL -= cost
		pos.EntryFeesUSDT += cost
	}
}
