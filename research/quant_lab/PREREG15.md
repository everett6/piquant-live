# Pre-registration 15: PREREG14's rules re-run on data with one bad print removed (committed before it runs)

## Why
PREREG14 failed, and the diagnosis found a data error, not a market event:
- The Coinbase BTC 1m file has a bar at $0.06 on 2017-04-15 23:02 UTC (o=h=l=c, 0.78 BTC; BTC was ~$1,175).
- The S1 stop filled there (−96.5% in one day), and S2/S3 were pushed through their floor into a permanent
  cash lock.

**This is not a variant.** S1, S2, S3, every parameter and the pass rule are exactly PREREG14's. Only the input
data changes. It is disclosed as motivated by the PREREG14 result and charged as 3 new trials.

## Data rule (fixed now, before running)
Drop every 1m bar where o = h = l = c and the price is below 50% of the prior daily close.
- In the current files this removes exactly one bar: BTC 2017-04-15 23:02.
- The two ETH bars of the real 2017-06-21 GDAX flash crash (low $0.10 and $133, opens near $300) are kept.
  They were real fills on that venue.
- Daily OHLC is rebuilt from the cleaned 1m data.

## Variants, costs, periods, pass rule
- Identical to PREREG14: S1 (P1 + intraday vol stop), S2 (S1 + Grossman–Zhou cap, α = 0.70), S3 (S2 with the
  20/50/100/200 ensemble).
- Pass rule unchanged:
  - max DD no worse than −35% at both cost levels;
  - full return ≥ +2.5%/month;
  - > 0 in at least 4 of the 5 periods;
  - stress return > 0.
- Trials: 720 + 3 = 723.

**If one passes:** as in PREREG14, it is added to the trend ledger as a paper variant.

**If none pass:** these rules are closed. In particular:
- no floor-reset or rolling-peak fix for the cash lock;
- no tighter stop cap.

## Prediction (written now)
- **S1:** max DD still worse than −35%. The 12% cap at 2x allows −24% days (Dec 2017), so it fails.
- **S2/S3:** the cash lock can still happen on a real gap. I guess a drawdown near −30% to −36% and a return
  of 1–3%/month.
- I expect at most one of them to pass, more likely none.

## Result (run once, 2026-11-11): all three FAIL. No ledger change. These rules are closed.

| Variant | Full %/mo | Max DD | Stress DD | Stress %/mo | Worst day | Periods > 0 |
|---|---|---|---|---|---|---|
| P1 (comparator) | +8.50 | −84% | −98% | +1.18 | −32.5% | 5/5 |
| S1 stop | +4.73 | −91% | −99% | −2.35 | −24.3% | 5/5 |
| S2 stop + GZ | +0.77 | **−30%** | **−30%** | −0.25 | −21.4% | 1/5 |
| S3 stop + GZ + ensemble | +0.97 | **−30%** | **−30%** | −0.19 | −21.4% | 1/5 |

**What this shows:**
- **Grossman–Zhou sizing does bound the drawdown.** S2/S3 stopped at exactly −30% under both cost levels.
- **But with an all-time peak it freezes the fund.** Equity reached the floor in the 2018 bear. The cap then
  stayed ≈ 0, and C, B, discovery and A all earned 0.00%. A drawdown limit that is never reset means stopping
  trading after the first deep bear.
- **The intraday vol stop hurts crypto trend.**
  - Against P1: return 8.50% → 4.73%/month, drawdown −84% → −91%.
  - There were 318 stop-outs, and most of those days recovered by the close.
  - Kaminski & Lo's condition (serial correlation inside the stop window) does not hold here.
- **The ensemble signal** helped slightly over SMA50 inside the GZ frame (+0.97 vs +0.77%/month). It was not
  tested on its own.

**Frontier so far (full 2016–26, us-futures costs):**

| Config | Max DD | Return |
|---|---|---|
| GZ at 30% | −30% | ~1%/mo, frozen after 2018 |
| 1x | −57% | 5.2%/mo |
| 1.5x | −73% | 7.1%/mo |
| 2x | −84% | 8.5%/mo |

No tested rule keeps the drawdown under 40% and still earns.

Trials charged: 723.
