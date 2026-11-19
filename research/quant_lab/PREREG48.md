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

## Result (run once, 2026-11-18; results/round48.json)

Both fail on check 2 only (return gave up more than 0.10 %/mo); checks 1, 3, 4, 5 and 6 pass.

| | %/mo | Max DD | Sharpe | Recent | Stress |
|---|---|---|---|---|---|
| Blend A | +2.12 | −19.8% | 1.36 | +0.56 | +0.71 |
| 80% A + 20% SPY trend (E1) | +1.83 | −17.7% | 1.41 | +0.48 | +0.69 |
| 80% A + 20% QQQ trend (E2) | +1.89 | −17.6% | 1.44 | +0.54 | +0.74 |
| 80% A + 20% SPY buy-and-hold (not judged) | +1.98 | −19.2% | 1.47 | +0.73 | — |
| 80% A + 20% QQQ buy-and-hold (not judged) | +2.06 | −22.1% | 1.49 | +0.76 | — |

- The trend rule does its job on stocks as a drawdown cutter: SPY −33.7% → −11.7%, QQQ −35.1% → −15.3% (2020-01 on);
  long history SPY −55% → −33% (from 1993), QQQ −83% → −34% (from 1999). It costs half the return: SPY +15%/yr →
  +7%/yr here and +10.8% → +3.3%/yr since 1993; QQQ +19% → +10% here and +10.6% → +5.6% since 1999. Long about 55% of
  days, about 13 trades a year.
- As a sleeve it trims blend A's worst drawdown by about 2 points and lifts Sharpe slightly, but costs 0.23–0.29 %/mo.
- Plain buy-and-hold stocks as a 20% sleeve gave a higher Sharpe (1.47–1.49) than either trend sleeve, at most of
  blend A's return; QQQ deepened the drawdown, SPY trimmed it slightly. The correlation with blend A was low
  (0.18–0.20), so most of the gain comes from owning something different, not from the trend rule.

No variants of the trend-on-stocks family will be run.

Trial count note: another session charged 3 trials (its PREREG45 ML test, 802 → 805) between this pre-registration and
its run, so this test's 2 trials take the counter 805 → 807 (the runner's `N_TRIALS = 804` is only a label here; no
check depends on it).
