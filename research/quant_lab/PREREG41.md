# Pre-registration 41: 91-day 16-coin L2 multi-hour flow rotation, continuous-time quantum-walk interference, and 15-minute causal L2 smart execution (`775 -> 778`, committed before `run_round41.py` reads historical L2 files)

## Why
- **Dataset just completed (`2026-11-15`):** `~/piquant-collect/historical_l2` now contains **91 consecutive synchronized days (`2026-08-15`..`2026-11-13`)** of `1s` top-10 Bybit L2 snapshots (`book/`) and tick trades (`ticks/`) across **all 16 coins** (`BTC, ETH, SOL, XRP, DOGE, LINK, ADA, AVAX, LTC, BCH, DOT, ETC, HBAR, SHIB, UNI, XLM`).
- **Closed tests not repeated:**
  - `PREREG17/37` proved sub-second `100 ms` passive maker quoting loses `−1.2` to `−1.4 bps` immediate `+5s` post-fill adverse selection (`−9.7 bps` net).
  - `PREREG22` proved `30s–60s` cross-exchange lead-lag taker flow earns `+2.5 bps` gross against `14 bps` RT fees (`−14.4 bps` net).
- **Hypothesis suited to owner's live ping (`51–160 ms` home RTT):**
  1. Instead of competing in sub-second queue races (`5s–60s`), aggregating causal `1s` L2 order-book depth imbalance (`OBI_5`) and signed taker dollar volume (`CVD`) onto **`4h` bars** operates with zero latency penalty while targeting multi-hour directional drift (`> 6–14 bps` RT fees).
  2. Modeling cross-coin L2 flow over the `16`-coin return correlation graph Laplacian via a **continuous-time quantum walk** (`expm(-i tau H)`) tests whether constructive wave interference across correlated crypto L2 inflows predicts `4h` continuation beyond single-coin flow.
  3. On execution (`S36_qd_carry_blend` daily rebalances at `00:00 UTC`), testing whether waiting passively inside a causal **`15-minute` post-close window (`00:00..00:15 UTC`)** for spread compression or favorable top-3 depth saves real implementation-shortfall bps vs crossing immediately at `00:00:01 UTC`.

## Dataset & Exact Split (`91` frozen days: `2026-08-15`..`2026-11-13`)
- **Discovery (`45` days):** `2026-08-15`..`2026-09-28` (`270` × `4h` bars; `720` buy/sell daily execution checks per side).
- **Holdout (`46` days, untouched before one run):** `2026-09-29`..`2026-11-13` (`276` × `4h` bars; `736` daily execution checks per side).
- **Universe (`16` coins):** `BTCUSDT, ETHUSDT, SOLUSDT, XRPUSDT, DOGEUSDT, LINKUSDT, ADAUSDT, AVAXUSDT, LTCUSDT, BCHUSDT, DOTUSDT, ETCUSDT, HBARUSDT, SHIBUSDT, UNIUSDT, XLMUSDT`.
- **Warm-up causality rule:** Rolling indicators (`CVD_z` over `18` bars = `72h`, `EMA_18`, `16x16` correlation matrix over `36` bars = `6d`) are strictly causal (decision at end of `4h` bar `b` uses data `<= b` and earns open-to-open / mid-to-mid over bar `b+1`, or enters after `00:00 UTC`). Warm-up begins within Discovery; `36`-bar initial window (~6 days) is skipped in Discovery metrics consistently across strategies and equal-weight Buy&Hold benchmark.

## Rules & Variants (`3` fixed trials: `775 -> 778`, zero parameter tuning)
1. **`Q41_l2_4h_flow_rotation` (`4h` L2 Accumulation Rotation, long-only):**
   At each `4h` boundary `b`:
   - `obi_4h[i, b]` = mean of `1s` `(bid_top5_qty - ask_top5_qty) / (bid_top5_qty + ask_top5_qty)` over bar `b`.
   - `cvd_ratio[i, b]` = `(buy_usd - sell_usd) / max(1e-9, buy_usd + sell_usd)` over bar `b`; `cvd_z[i, b]` = causal z-score of `cvd_ratio[i, :]` over trailing `18` bars (`72h`, sample std floored at `0.05`).
   - Filter: `obi_4h > +0.08` AND `cvd_z > +0.75` AND `mid_close[b] > EMA_18(mid_close)[b]` AND `mean_spread_bps[b] <= 6.0`.
   - Select top `K <= 3` qualifying coins by `cvd_z`; assign `min(1.0/3, 1.0/3)` per selected coin (so `< 3` qualifiers keeps unused weight in cash earning `0` on `4h` study), rebalanced only at `4h` transitions (charged turnover fee on `|w_{b+1} - w_b|`).
2. **`Q41_l2_quantum_walk` (Continuous-Time Quantum Walk Interference across 16 Coins):**
   At each `4h` boundary `b` (with `>= 36` prior bars):
   - Form `16x16` Pearson correlation matrix `C` of `4h` log returns over trailing `36` bars; keep positive edges `W_ij = max(0, C_ij)` (`W_ii = 0`), symmetric normalized Laplacian `L_norm = I - D^{-1/2} W D^{-1/2}`.
   - Local potential `V_i = -clip(obi_4h[i, b], -1, 1)`; Hamiltonian `H = L_norm + diag(V)`.
   - Propagate signed positive and negative L2 flow independently under unitary Schrödinger evolution `U = expm(-1j * tau * H)` (`tau = 0.60`, fixed a priori): `psi_buy_0 = max(clip(cvd_z, -3, 3), 0)`, `psi_sell_0 = max(-clip(cvd_z, -3, 3), 0)`, `psi_buy_tau = U @ psi_buy_0`, `psi_sell_tau = U @ psi_sell_0`.
   - Net coherent quantum amplitude score: `influx[i] = |psi_buy_tau[i]| - |psi_sell_tau[i]|`.
   - Filter: `influx[i] > 0.50` AND `obi_4h[i, b] > 0.0` AND `mid_close[i, b] > EMA_18(mid_close)[i, b]` AND `mean_spread_bps[i, b] <= 6.0`.
   - Pick top `K <= 3` qualifying coins by `influx[i]`, weight `1/3` each (unfilled slots sit in cash).
3. **`Q41_l2_smart_exec` (Causal `15-min` L2 Execution Timing for `S36` Daily Rebalances):**
   Evaluated independently on both **Buy** (`+1`) and **Sell** (`-1`) notional rebalances across all `16` coins at every `00:00 UTC` daily close (`91` days × `16` coins):
   - **Baseline (Market-on-Close):** cross spread at the first `1s` snapshot `t_0` in `[00:00:00, 00:00:10]`: pay `ask_0(t_0)` for buys, receive `bid_0(t_0)` for sells. Shortfall vs `mid(t_0)` = `(ask_0(t_0) - mid(t_0)) / mid(t_0)` (Buy) or `(mid(t_0) - bid_0(t_0)) / mid(t_0)` (Sell) (always `> 0`, half-spread).
   - **L2 Smart Exec (Passive Patient Limit in `[00:00:00, 00:15:00]`, fallback cross at `00:15:00`):**
     Compute trailing rolling `1h` median spread `spread_med_1h` cleanly from `[23:00:00, 23:59:59]` prior day (passed seamlessly across days; on `2026-08-15` day 0 where prior day isn't loaded, skip that first boundary cleanly).
     Track `4` equal `25%` post-only slices (`S36` ping-safe schedule). A slice at `p_target` (`bid_0` at slice entry for Buy; `ask_0` for Sell) fills **passively** if inside its `225s` slice window (4 slices = `900s`) a causal `1s` snapshot shows:
     `(spread_bps <= 0.85 * spread_med_1h OR favorable_obi3 >= 0.10)` AND subsequent trade print crosses `<= p_target` (Buy) / `>= p_target` (Sell) (`+100 ms` queue guard). Otherwise that slice crosses the market at the slice end (`ask_0` / `bid_0`).
   - Report mean **implementation shortfall (`bps` vs `mid(t_0)`)**, **bps saved vs baseline**, fill rate `%`, and paired t-stat on Discovery and Holdout (both Pooled `16` coins and `BTC+ETH` specifically).

## Cost Models (`Q41_l2_4h_flow_rotation` & `Q41_l2_quantum_walk`)
- **`US_SPOT` (primary operational check):** `2 bps` fee + `1 bp` slip (`3 bps` per one-way unit turnover = `6 bps` round-trip), `0` funding.
- **`US_FUT` (base fee stress comparison):** `6 bps` fee + `1 bp` slip (`7 bps` per one-way unit turnover = `14 bps` RT) + `0.1 bp/h` futures funding (`0.4 bps` per `4h` bar held).

## Pass Rules (`3` fixed trials: `775 -> 778`)
- **Directional `4h` strategies (`Q41_l2_4h_flow_rotation` & `Q41_l2_quantum_walk`) PASS under `US_SPOT` only if ALL 4 hold:**
  1. Holdout cumulative return > `0%` AND beats `16-coin Equal-Weight Buy&Hold` on Holdout Sharpe (`Sharpe > BH_Sharpe`).
  2. Holdout annualized Sharpe `>= 1.0` AND Holdout max drawdown `>= -12.0%`.
  3. Discovery annualized Sharpe `>= 1.0` AND Discovery return > `0%`.
  4. Holdout `US_FUT` return > `0%`.
- **Execution strategy (`Q41_l2_smart_exec`) PASSES only if ALL 3 hold:**
  1. Holdout mean implementation shortfall strictly `<` Baseline shortfall (`bps_saved > 0.25 bps` on `16`-coin pooled rebalances with `t >= 3.0`).
  2. Discovery `bps_saved > 0.25 bps` with `t >= 3.0`.
  3. Pooled `BTC+ETH` Holdout `bps_saved > 0.10 bps` (no adverse-selection slippage explosion vs immediate `00:00:01` cross).

## Prediction (written before running on real data)
- **`Q41_l2_4h_flow_rotation` & `Q41_l2_quantum_walk`:** `4h` holding horizons cut `PREREG22`'s `14 bps` turnover drag dramatically, but `91` days (`Aug–Nov 2026`) is a short sample heavily influenced by macro BTC trend (`Nov` `-10%` dip). `Q41_l2_quantum_walk` has a fair chance (`~1 in 3`) of beating `Buy&Hold` on Holdout Sharpe and max drawdown (`cash exit`), while clearing `Sharpe >= 1.0` in BOTH Discovery and Holdout under `US_SPOT` is roughly `~1 in 4`.
- **`Q41_l2_smart_exec`:** At `51–160 ms` RTT over a `15-minute` (`4x` slice) post-close window with trade-through confirmation, `BTC+ETH` L2 top-of-book spreads tighten quickly after `00:00:05 UTC` funding/rollover volatility settles. Waiting patiently with favorable OBI3 tilt should save `~0.4–1.5 bps` across all 16 coins (`~0.2–0.5 bps` on `BTC+ETH` where spread is 1 tick, larger on alts) cleanly (`~3 in 4` chance to PASS).
- One run only after synthetic unit tests pass.

## Result (run once, 2026-11-15, `172.4s`): ALL 3 FAIL (`PREREG41` closed, trials `775 -> 778`)

Evaluated across `91` frozen days (`2026-08-15..2026-11-13`, `546 × 4h` bars, `16` coins in `~/piquant-collect/historical_l2`):
- **Discovery (`2026-08-15..2026-09-28`, `45d`, bull leg `EW_BuyHold +36.01%`, Sharpe `4.895`):**
  - `Q41_l2_4h_flow_rotation`: `US_SPOT` **`+11.57%`**, DD `−6.49%`, Sharpe **`3.906`**, `87/233` active bars (`US_FUT` `+8.31%`, Sharpe `2.882`).
  - `Q41_l2_quantum_walk`: `US_SPOT` **`+12.89%`**, DD `−9.69%`, Sharpe **`3.294`**, `129/233` active bars (`US_FUT` `+6.68%`, Sharpe `1.842`).
- **Holdout (`2026-09-29..2026-11-13`, `46d`, bear drop `EW_BuyHold −31.12%`, DD `−32.93%`, Sharpe `−4.671`):**
  - `Q41_l2_4h_flow_rotation` (**FAIL** checks `1, 2, 4`): `US_SPOT` **`−10.73%`**, DD `−10.73%`, Sharpe `−6.519`, `49/276` active bars (`US_FUT` `−12.23%`).
  - `Q41_l2_quantum_walk` (**FAIL** checks `1, 2, 4`): `US_SPOT` **`−18.30%`**, DD `−18.51%`, Sharpe `−6.496`, `78/276` active bars (`US_FUT` `−20.54%`).
- **`Q41_l2_smart_exec` (`00:00:00..00:15:00` 4x post-only slices vs `00:00:01` MOC, **FAIL** checks `1, 2, 3`):**
  - **Discovery `16`-coin pooled (`n=1,408`):** MOC `1.178 bps` vs Smart `1.602 bps` (`−0.425 bps` saved, `t = −0.665`, `81.8%` passive fill).
  - **Holdout `16`-coin pooled (`n=1,472`):** MOC `1.013 bps` vs Smart `1.249 bps` (`−0.236 bps` saved, `t = −0.484`, `81.5%` passive fill).
  - **Holdout `BTC+ETH` (`n=184`):** MOC `0.023 bps` vs Smart `0.408 bps` (`−0.385 bps` saved, `t = −0.570`, `90.4%` passive fill).

**Reading:**
1. **`4h` L2 flow & quantum-walk strategies fail out-of-sample bear defense:** Both earned `+11.6%` to `+12.9%` (`Sharpe > 3.2`) in Discovery's bull leg and cut Holdout drawdown cleanly vs buy-and-hold (`−10.7%` and `−18.5%` vs `−32.9%`), **but a `3-day` `EMA_18` (`4h`) gate enters dead-cat L2 rallies inside a `-31%` bear regime** — confirming why daily `SMA50` (`S36_qd_carry_blend`) remains structurally superior.
2. **Tight L2 books punish `15m` passive execution drift:** On `BTC+ETH`, the `00:00 UTC` top-of-book half-spread is only `0.014–0.023 bps`, while waiting 15 minutes for passive queue fills incurs `~0.41 bps` implementation shortfall from adverse selection and chase risk on unfilled slices. Passive limit slicing only makes sense when an exchange's maker-vs-taker fee discount exceeds `~1–2 bps` (e.g., US Spot taker vs maker tiers), not to reduce `1`-tick mid-price drift. `PREREG41` closed (`775 -> 778`).
