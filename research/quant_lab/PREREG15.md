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
