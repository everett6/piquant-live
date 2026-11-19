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
