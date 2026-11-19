# Pre-registration 49: plain buy-and-hold stocks (SPY, QQQ) as a 20% sleeve beside blend A, rebalanced monthly (committed before any round-49 code reads real data)

Asked for by the owner on 2026-11-18 ("Yes, test it") after PREREG48 reported, without judging it, that holding
SPY or QQQ as a 20% sleeve gave blend A a higher Sharpe (1.47–1.49 vs 1.36).

**Honest status of the evidence.** The 2020-01 .. 2026-10 numbers have already been seen (daily-rebalanced, PREREG48),
so that window is a confirmation, not a discovery. This test adds two things that have not been seen: (a) a
realistic monthly rebalance with costs, and (b) an earlier window, 2016-08-01 .. 2019-12-31, that includes crypto's
2018 bear market (−80%) and stocks' late-2018 fall (−20%). Funding data does not exist before 2020, so in that window
blend A's 40% carry sleeve earns T-bills instead ("blend A0"); everything else is unchanged.

## Trials (2, 810 → 812)

- **H1:** 80% blend A + 20% SPY, held.
- **H2:** 80% blend A + 20% QQQ, held.

Portfolio rebalanced to 80/20 on the first day of each month; in between the weights drift. Each rebalance pays
cost × the amount moved on both sides: base 3 bps, stress 8 bps. The ETF earns its dividend-adjusted return on trading
days and 0 on weekends and holidays. Blend A is rebuilt as in PREREG46–48 (base and stress costs).

## Pass rules (all must hold)

1. Sharpe above the blend's alone, in both windows (main 2020-01 .. 2026-10 with blend A; early 2016-08 .. 2019-12
   with blend A0);
2. return ≥ the blend's alone − 0.15 %/mo, in both windows;
3. max drawdown no more than 1 point deeper than the blend's alone, in both windows;
4. stress (main window) ≥ blend A stress − 0.15 %/mo;
5. recent period (from 2025-07-01) return > 0.

Reported, not judged: correlation of each ETF with the blend per window, worst month, return in BTC bear months,
number of rebalances, and the same with 10% and 30% sleeves (for sizing, not for choosing).

## Predictions

H1 (SPY): likely passes; SPY's drawdowns did not line up with crypto's except 2022, and PREREG48's daily version
trimmed blend A's drawdown. About 3 in 5. H2 (QQQ): PREREG48 already showed a deeper drawdown (−22.1% vs −19.8%) in the
main window, so check 3 probably fails. About 1 in 4.

No variants: no other ETFs, weights, or rebalance schedules will be judged under this family.

## Result (run once, 2026-11-18; results/round49.json)

Both fail. H1 (SPY) fails only check 2; H2 (QQQ) fails checks 2 and 3.

| | Main 2020-01..2026-10: %/mo | Max DD | Sharpe | Early 2016-08..2019-12: %/mo | Max DD | Sharpe |
|---|---|---|---|---|---|---|
| Blend alone (A / A0) | +2.12 | −19.8% | 1.36 | +5.27 | −19.6% | 2.40 |
| 80/20 with SPY (H1) | +1.96 | −19.4% | 1.45 | +4.56 | −15.7% | 2.50 |
| 80/20 with QQQ (H2) | +2.03 | −22.4% | 1.47 | +4.65 | −15.0% | 2.54 |

- Sharpe rose in both windows for both ETFs (check 1 passes), and stress and recent returns improved.
- Check 2 fails: SPY cost 0.16 %/mo in the main window (limit 0.15) and 0.71 %/mo in the early window, where blend A0
  made +5.27 %/mo and any 20% sleeve earning ~1 %/mo was bound to cost ~0.8. In hindsight the fixed 0.15 limit was
  not achievable in the early window by any ordinary asset; it is recorded as written.
- QQQ deepened the main-window drawdown to −22.4% (check 3), as predicted. In the early window both ETFs cut the
  drawdown by 4–5 points.
- Correlation with the blend: 0.14–0.15 (main), 0.01–0.02 (early). Worst month −6.5% → −5.9% (main, SPY), −11.3% →
  −8.7% (early, SPY).

Reading (not a pass): SPY is a genuine diversifier for this system (better Sharpe, smaller worst month, shallower
drawdown in both windows), and its price is giving up some return: about 0.16 %/mo now, more when crypto is booming.
No variants will be run.
