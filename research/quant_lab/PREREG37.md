# Pre-registration 37: quantum-inspired L2 pre-fill defenses (order-book barrier tunneling & 16-coin Ising magnetization) on 50 days of historical L2 books (committed before code reads real data)

## Why
- **Data now on disk (isolated from live Pi `PREREG17/18`):** `~/piquant-collect/historical_l2` holds `51+` consecutive full days of synchronized `1s` top-10 L2 snapshots (`b`, `a`, `upd`) and spot trades for **16 coins** (`BTCUSDT..XLMUSDT`).
- **Prior finding (`PREREG21`):** Naive passive fills lose ~`1 bp` to adverse selection ("getting run over" as resting bid support collapses).
- **Hypothesis:** Using real L2 book physics instead of daily candles, canceling passive bids when either (a) aggressive sell flow exceeds top-3 resting bid depth (**order-book barrier tunneling `QT_barrier`**) or (b) `>= 12/16` coins simultaneously turn ask-heavy (**16-coin Ising spin magnetization `QM_ising`**) avoids toxic fills and turns passive maker quoting net-positive.

## Dataset & Split (`50` consecutive frozen days: `2026-09-25`..`2026-11-13`)
- **Discovery:** `2026-09-25`..`2026-10-19` (`25` days).
- **Holdout (untouched):** `2026-10-20`..`2026-11-13` (`25` days).
- **Traded instruments:** `BTCUSDT` and `ETHUSDT` (pooled), quoting `24/7` (`gate=True`, no daily SMA filter so microstructure defense is tested cleanly across all regimes).
- **Spin universe (`QM_ising`):** all `16` coins (`BTC, ETH, SOL, XRP, DOGE, LINK, ADA, AVAX, LTC, BCH, DOT, ETC, HBAR, SHIB, UNI, XLM`) at exact `1s` causal timestamps (prior snapshot carried forward at most `5s`).

## Rules & Variants (`2` trials: `767 → 769`, fixed parameters, zero search)
Reuses `strategies17.Book` quote/queue/exit engine (`LATENCY_MS=100`, `QUOTE_TTL_MS=60000`, `55→10 bps` TP ladder, `−35→+9 bps` SL/BE ladder, `30 min` time stop):
- **Base filter (`K2_base`, comparator):** quote at `best_bid` when `obi(bid_tot, ask_tot) > 0.25` and `vpin < 0.50`.
- **Classical benchmark (`K3_flow`, comparator):** `K2_base` + `strategies17.defense_hit(ret5s, flow5s)` (`ret5s < -3 bps` or `flow5s < -0.60`).
- **Trial 1 (`QT_barrier`):** Let `V_bid(t) = sum(top 3 bid sizes)` and `K_sell(t) = aggressive sell volume in (t - 5000 ms, t]`. Trigger cancel/skip whenever `K_sell(t) > 0` and `exp(-2 * V_bid(t) / K_sell(t)) > exp(-2)` (equivalently `K_sell(t) > V_bid(t)`). Tracks avoided fills in `shadow_trades`.
- **Trial 2 (`QM_ising`):** Let coin spin `s_i(t) = +1` if `bid_tot_i >= ask_tot_i` else `−1`, and magnetization `M(t) = mean(s_i(t))`. Trigger cancel/skip whenever `M(t) <= -0.50` (`≥ 12/16` coins net ask-heavy simultaneously). Tracks avoided fills in `shadow_trades`.

## Costs
- **`BASE` (primary pass check):** `2.0 bps` maker / `6.0 bps` taker + `1.0 bp` slip (`queue_credit=True`, matching `PREREG17 BASE`).
- **`STRESS`:** `4.0 bps` maker / `6.0 bps` taker + `1.0 bp` slip (`queue_credit=False`).
- **`ZERO_MAKER` (operational US spot check, reported):** `0.0 bps` maker / `2.0 bps` taker + `1.0 bp` slip (`queue_credit=True`).
- Also reported: post-fill mid markouts at `+5s`, `+30s`, `+60s`, `+300s` (bps before fees).

## Pass Rule (`2` trials: `767 → 769`)
A variant (`QT_barrier` or `QM_ising`) **PASSES** under `BASE` only if ALL 4 hold:
1. Holdout `mean_net_bps > K2_base mean_net_bps + 0.5` AND shadow avoided fills earn strictly less than kept fills (`avoided_mean_net < kept_mean_net`).
2. Holdout `n >= 200`, `mean_net_bps > 0`, and `t >= 2.0`.
3. Discovery `n >= 200` and `mean_net_bps > 0`.
4. Holdout `STRESS mean_net_bps > 0`.

## Prediction (written before reading real L2 files)
- `K2_base` on `BASE` (`2 bps` maker / `7 bps` taker stop) will likely sit slightly negative (`−0.5` to `−2.0 bps/trade`) due to adverse selection and stop-out spread costs, though positive on gross or `ZERO_MAKER` (`0/3 bps`).
- `QT_barrier` (`K_sell > V_bid`) will cut toxic stop-outs and beat `K2_base` on `shadow_trades` vs kept trades (`> +0.5 bps` improvement), though whether `STRESS` (`no queue credit`, `4/7 bps`) clears `> 0` is a close toss-up (`~1 in 3`).
- `QM_ising` (`M <= -0.50`) measures slower macro cross-coin skew; it will fire less often and catch market-wide drops, likely helping gross markouts modestly (`~1 in 4` to pass `STRESS > 0`).
- One run only.

## Result (run once, 2026-11-14): BOTH FAIL (`QT_barrier` and `QM_ising`); passive `100 ms` L2 quoting loses `−2.0` to `−2.5 bps` gross to adverse selection before fees

Evaluated across `50` consecutive frozen days (`2026-09-25..2026-11-13`, `16` coins in `~/piquant-collect/historical_l2`):
- **Discovery (`2026-09-25..2026-10-19`, `25` days):**
  - `K2_base`: `n=4305`, gross `−1.98 bps`, `BASE` net **`−9.21 bps`** (`t = −29.89`, win `19.0%`), `ZERO_MAKER` `−3.59 bps`, `STRESS` `−11.60 bps`; `+5s` markout `−1.21` (`BTC`) / `−1.33 bps` (`ETH`).
  - `K3_flow`: `n=4142`, gross `−2.01 bps`, `BASE` net `−9.25 bps`, cancels `10,974`, avoided `n=1020` net `−9.49 bps`.
  - `QT_barrier`: `n=4130`, gross `−1.89 bps`, `BASE` net `−9.12 bps`, `ZERO_MAKER` `−3.51 bps`, `STRESS` `−11.51 bps`, cancels `434`, avoided `n=79` net `−7.70 bps`.
  - `QM_ising`: `n=4299`, gross `−1.95 bps`, `BASE` net `−9.19 bps`, `ZERO_MAKER` `−3.57 bps`, `STRESS` `−11.57 bps`, cancels `1,390`, avoided `n=89` net `−9.60 bps`.
- **Holdout (`2026-10-20..2026-11-13`, `25` days):**
  - `K2_base`: `n=5637`, gross `−2.33 bps`, `BASE` net **`−9.65 bps`** (`t = −32.95`, win `17.0%`), `ZERO_MAKER` `−3.99 bps`, `STRESS` `−12.00 bps`; `+5s/+300s` markouts `−1.25 / −1.14` (`BTC`) and `−1.38 / −1.75 bps` (`ETH`).
  - `K3_flow`: `n=5417`, gross `−2.51 bps`, `BASE` net `−9.84 bps`, cancels `8,113`, avoided `n=944` net `−9.54 bps`.
  - **`QT_barrier` (FAIL, 1/6 checks):** `n=5208`, gross `−2.45 bps`, `BASE` net **`−9.76 bps`** (`t = −32.79`, win `17.3%`), `ZERO_MAKER` `−4.10 bps`, `STRESS` `−12.11 bps`, cancels `630`, avoided `n=129` net `−9.70 bps`; `+5s/+300s` markouts `−1.29 / −2.01` (`BTC`), `−1.35 / −1.86` (`ETH`).
  - **`QM_ising` (FAIL, 1/6 checks):** `n=5631`, gross `−2.34 bps`, `BASE` net **`−9.66 bps`** (`t = −32.97`, win `16.9%`), `ZERO_MAKER` `−4.00 bps`, `STRESS` `−12.01 bps`, cancels `747`, avoided `n=51` net `−5.37 bps` (`ZERO_MAKER` avoided `+0.12 bps`); `+5s/+300s` markouts `−1.27 / −1.13` (`BTC`), `−1.41 / −1.68` (`ETH`).

**Reading:**
- Real 1s top-10 Bybit L2 books prove why passive maker quoting with `100 ms` reaction latency cannot beat adverse selection at sub-second horizons: every filled quote experiences `−1.2` to `−1.4 bps` immediate `+5s` mid-price markout decay (`~−2.0` to `−2.5 bps` gross per trade, `−3.6` to `−4.2 bps` even at zero maker fees, and `−9.2` to `−9.8 bps` at retail fees).
- Neither `QT_barrier`, `QM_ising`, nor `K3_flow` rescued Holdout adverse selection (`avoided` trades were slightly *better* than kept trades on Holdout across all three defenses). Sub-second L2 pre-fill maker defenses are closed for home ping (`~181 ms` HTTPS RTT, `641 ms` cancel exposure). Trials charged: `767 -> 769`.

