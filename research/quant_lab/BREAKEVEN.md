# Breakeven win rate and why bracket geometry alone cannot create edge

This replaces the earlier one-line claim "(30 + 9) / 65 = 60%". That claim was not derived and used the wrong
denominator. The derivation follows; the empirical check is T0 in PREREG3.md.

## 1. Breakeven win rate (exact, for two outcomes)

A trade ends with net return +W (target) with probability p, or -L (stop) with probability 1 - p.
W and L are measured after all fees and slippage.

    E[net] = p W - (1 - p) L
    E[net] > 0  <=>  p > p* = L / (W + L)                                   (1)

Proof: E[net] is linear in p. It is zero at p* = L / (W + L) and increasing in p because W + L > 0.

**Many outcomes** (time stops, breakeven locks, trailing stops). Let outcome k have probability p_k and net
return R_k. Then

    E[net] = sum_k p_k R_k.

Breakeven is simply E[net] = 0. "Win rate" is no longer sufficient. A 70% win rate loses money if the wins are
+3 bps breakeven-lock exits and the losses are -39 bps stops.

## 2. The live engine's numbers (us-futures profile, internal/paper/dryrun.go and region.go)

- Fees: entry is a maker order, 2 bps.
- Target exit: a maker order at entry x (1 + (2 x 2 + T) bps). T is a net target that starts at 35 bps and
  decays toward the 80% floor, 28 bps. So W = T.
- Stop exit: a taker order once the adverse move exceeds S = 30 bps (scaled up together with T in high vol).
  So L = S + 2 (maker entry) + 6 (taker exit) + 1 (slippage) = 39 bps.

| Target state | Breakeven p* = L / (W + L) |
|---|---|
| Fresh target, W = 35 | 39 / 74 = 52.7% |
| Decayed to floor, W = 28 | 39 / 67 = 58.2% |

These hold only if every maker exit fills at its price with no adverse selection. The breakeven lock adds a
third outcome of about +3 bps, so (1) is an approximation and E[net] = sum_k p_k R_k is the real test.

## 3. Geometry theorem: brackets cannot manufacture edge

Let X_t = log price. Assume X_t is a martingale over the holding window (no predictable drift given the entry
information). Let the exit time tau be "first touch of +T or -S, or max hold H". Tau is bounded by H, so the
optional stopping theorem gives

    E[X_tau - X_0] = 0.                                                     (2)

Every TP/SL/time-stop rule therefore has zero expected gross log return. After costs c > 0, E[net] = -c < 0
for every geometry.

If paths are continuous and H is long enough that the max-hold exit is rare, the exit is +T or -S. Then (2)
forces

    p_gross T - (1 - p_gross) S = 0   =>   p_gross = S / (T + S).          (3)

Widening TP relative to SL lowers the win rate by exactly the amount that keeps the edge at zero.

**With drift.** Edge can only come from drift: the entry signal must predict the direction of the next move.
For Brownian motion with drift mu and variance sigma^2 per unit time, the probability of touching +T before
-S is (gambler's ruin with drift; scale function s(x) = exp(-2 mu x / sigma^2)):

    P_up = (1 - e^{2 mu S / sigma^2}) / (e^{-2 mu T / sigma^2} - e^{2 mu S / sigma^2}),   (4)

This tends to S / (T + S) as mu -> 0.

## 4. Consequences for the wider-TP/SL idea

- On an entry signal with no predictive drift, such as the engine core in round 1 (gross -0.15 bps/trade),
  widening TP cannot help. It only moves the win rate along (3).
- Wider brackets can help only by lowering the cost share c / (T + S) on a signal that has real drift. Trend
  regimes are the one place the lab found discovery drift.

## 5. Caveats (all of them make real results worse than (3))

- Gaps through the stop make L larger than S.
- The pessimistic same-bar rule (stop counted first) biases the simulated win rate down for tight brackets.
- Simple returns carry a +sigma^2/2 convexity term relative to log returns. It is negligible at these
  horizons.
- Max-hold exits break the two-outcome assumption.
