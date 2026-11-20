# Pre-registration 50: low-frequency blend A (trend decided weekly or monthly instead of daily) (committed before any round-50 code reads real data)

Asked for by the owner on 2026-11-18: "for the crypto implement a low frequency trading, check if it works before
implementing it". Only synthetic tests had run when this was committed. Numbered 50 to stay clear of the numbers
another agent in this repo has been reusing.

## Question

Blend A (`Q42_dualspeed_60_40_carry`) re-decides its trend positions every daily close. Does deciding them only once
a week or once a month keep the return and drawdown while trading far less (lower costs, fewer whipsaws, less to go
wrong on the Pi)? Never tested here: PREREG30/40 tested more clocks, not fewer decisions.

## Trials (2, 812 → 814)

- **L1, weekly:** the dual-speed trend position (½ QD + ½ R3 fast exit, 1x per coin) is read at the Sunday daily close
  (00:00 UTC Monday) and held unchanged for the next 7 days.
- **L2, monthly:** read at the last daily close of each month and held for the whole next month.

Everything else is blend A unchanged: 60% trend / 40% C1 carry / T-bills on idle cash, US futures costs (stress:
`US_FUTURES_STRESS`, `S19.STRESS`), and the 6% → 20% drawdown taper (floor 0.40), which still acts daily because it is
the safety rule. Window 2020-01-01 .. 2026-11-01; regimes from BTC months; recent = from 2025-07-01.

## Pass rules (all must hold; compared with daily blend A)

1. return ≥ blend A − 0.10 %/mo;
2. max drawdown no more than 1 point deeper than blend A's;
3. stress return ≥ blend A's stress return (lower turnover should help when costs are high);
4. recent-period return > 0;
5. trend turnover (sum of |position change| per year, both coins) at most half of daily blend A's.

Reported, not judged: Sharpe, bear/flat/bull months, worst day and worst month, trend changes per year, and the same
two schedules with the decision day shifted (weekly on Wednesday close, monthly on the 15th) to show how much the
result depends on the chosen day.

## Predictions

Crypto trends last weeks to months, so a weekly check should lose little: L1 about 1 in 2. Monthly is too slow for
crypto crashes (March 2020 and May 2021 fell 50% inside three weeks) and the taper only partly covers it: L2 about 1
in 6.

If L1 passes, it is implemented as a Pi paper ledger (`L1_weekly_dualspeed`) beside the daily blend; if both fail,
nothing is implemented and no variants are run.

## Result (run once, 2026-11-18; results/round50.json)

Both fail; nothing is implemented.

| | %/mo | Max DD | Sharpe | Stress | Recent | Trend turnover/yr | Trend changes/yr |
|---|---|---|---|---|---|---|---|
| Daily blend A | +2.12 | −19.8% | 1.36 | +0.71 | +0.56 | 30.2 | 416 |
| L1 weekly (Sunday) | +1.63 | −21.5% | 1.07 | +0.53 | +0.20 | 20.0 | 66 |
| L2 monthly (month-end) | +1.11 | −23.4% | 0.81 | +0.17 | +0.34 | 11.1 | 19 |
| weekly on Wednesday (not judged) | +1.92 | −20.4% | 1.24 | +0.57 | +0.18 | 19.2 | 67 |
| monthly on the 15th (not judged) | +1.16 | −27.8% | 0.79 | +0.12 | −0.21 | 9.1 | 19 |

- L1 fails 4 of 5 (return −0.49 %/mo, DD 1.7 points deeper, stress lower, turnover 66% of daily, not ≤ 50%).
  L2 fails 3 of 5 (return −1.01 %/mo, DD 3.6 points deeper, stress lower).
- Why: the daily blend's edge is the fast exit. Holding a week or a month keeps the position through the first days
  of a fall that the daily rule already left (worst day −7.7% → −9.1 to −14.4%; worst month −6.5% → −7.9 to −16.0%).
  Lower turnover did not help even under stress costs, because most daily "changes" are small ramp steps that cost
  little; the losses come from late exits, not fees.
- The result depends heavily on the chosen day (weekly: Sunday +1.63 vs Wednesday +1.92 %/mo; monthly 15th has a
  −16% month), another sign that low-frequency timing is luck, not edge.

Low-frequency decision schedules for the trend sleeve are closed. The daily blend stays as is.
