# Pre-registration 48: the crypto trend rule on stocks (SPY, QQQ) as a 20% sleeve beside blend A (committed before any round-48 code reads real data)

Asked for by the owner on 2026-11-18: "would it be a good idea to apply this to stocks?" then "Yes, test it". Only
synthetic tests had run when this was committed. QQQ prices are fetched by the runner (free Yahoo data).

## Rule (unchanged from crypto, not tuned)

Blend A's trend core, the dual-speed rule: position = ½ × QD coherence (`strategies35.coherence_positions`, SMA50,
τ = 5, 1x) + ½ × R3 fast exit (`strategies40.r3_fastexit_qd_positions`, 1x), computed on the ETF's dividend-adjusted
daily close, counted in trading days. Long/flat only, never above 1x (no margin).

Sleeve return on each trading day: position from the previous close × the ETF's return + (1 − position) × T-bills
(DTB3, accrued for the calendar days since the previous close) − cost × |position change|. Cost per unit traded:
base 2 bps (spread and slippage, no commission), stress 5 bps. Returns are placed on the trading date; weekends and
holidays are 0 for the sleeve.

## Trials (2, 802 → 804)

- **E1:** dual-speed on SPY (S&P 500).
- **E2:** dual-speed on QQQ (Nasdaq-100).

Each is judged as 80% blend A + 20% sleeve against blend A alone (`Q42_dualspeed_60_40_carry`, rebuilt as in
PREREG46/47), 2020-01-01 .. 2026-11-01, regimes from BTC months, "recent" = from 2025-07-01.

## Pass rules (all must hold)

1. max drawdown at least 1 point better than blend A's;
2. return ≥ blend A − 0.10 %/mo;
3. Sharpe > blend A's;
4. stress return ≥ blend A stress − 0.10 %/mo;
5. recent-period return > 0;
6. the sleeve on its own has a smaller max drawdown than buying and holding the same ETF over the same window (the
   trend rule must do its job on stocks).

## Reported, not judged

80% A + 20% buy-and-hold of each ETF (to separate "stocks help" from "the trend rule helps"); correlation of each
sleeve with A; share of days long; trades per year; and each sleeve vs buy-and-hold over the longest free history
(SPY from 1993, QQQ from 1999), which covers 2000–02 and 2008.

## Predictions (written before running)

Stocks fell with crypto in 2022, and 2020 was a fast V that trend rules whipsaw on. The sleeve should cut the ETF's
drawdown (check 6 likely passes) but give up return against 40%+ crypto bull months, so check 2 or check 1 likely
fails. E1 about 1 in 4, E2 about 1 in 4.

No variants: if both fail, no other ETFs, windows, weights or rule settings will be tried under this family.
