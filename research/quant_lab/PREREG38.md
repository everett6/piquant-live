# Pre-registration 38: ping-adapted bull-market accelerators on `S36_qd_carry_blend` (`<= 40%` Max DD, `769 -> 771`, committed before round-38 reads any real data)

## Why
- **`S36_qd_carry_blend` (`PREREG36` PASS 5/5, trial `767`)** kept `2020–2026` max drawdown to `−27.8%`, `bear −2.4%/mo`, Sharpe `1.285`, Calmar `1.14`, and stress `+0.76%/mo` — well inside the owner's `50%` hard risk limit.
- **The bull-market gap:** By capping the Trend sleeve at `1.0x` (`0.70x` effective weight in `70/30` blend), `S36_qd_carry_blend` earned `+2.64%/mo` overall and `+8.0%/mo` in bull months (vs `R1_1x +14.9%/mo`, `QD_2x +25.1%/mo`), leaving bull compounding short of the owner's `60–80%/yr` (`~4–5%/mo`) growth objective.
- **Suited to the owner's ping (`51–69 ms` TCP RTT from San Jose):** Measured live exchange RTTs (`Coinbase 51.4 ms`, `Binance.US 51.7 ms`, `Kraken 62.6 ms`, `Deribit 68.9 ms`) are too slow to beat `1 ms` colocated HFTs on sub-second sniping, **but have zero latency penalty at daily close (`00:00 UTC`) rebalances**. Patient maker/TWAP slices at daily or weekly cadences execute cleanly over home Ethernet/WiFi without adverse queue-race decay.

## How this differs from closed tests
- **`PREREG29` (16-coin XS momentum) FAILED** because it held an always-on `2x` gross long/short book (including a short leg vulnerable to `+83.6%` alt squeezes and `-11.8%/mo` long bleed during bear markets without a cash/SMA50 exit). **Never repeats long/short alt ranking.**
- **`PREREG12 Q2` FAILED** because it held a static `1/3` `SOL` weight forever (`-81%` DD).
- **`PREREG31–32` (funding/stablecoin gates) FAILED** because non-price gates cut `2x -> 1x` late during pullbacks while remaining unhedged at `-80%` DD.
- **Two novel, strictly long-only, cash-exiting ideas tested here (`2` trials: `769 -> 771`):**
  1. **`Q38_kuramoto_booster` (16-Coin Kuramoto Phase-Sync House-Money Booster):**
     Uses the causal fraction of tracked crypto assets above `SMA50` (`r_t = mean(close_i[t] > SMA50_i[t])` across eligible `16` coins in `S29.COINS`) as a Kuramoto synchronization order parameter. When broad market synchronization holds (`r_t >= 0.70`) AND portfolio drawdown is minimal (`m(dd_t)` near `1.0`), the `70%` `QD` sleeve cap flexes from `1.00x` up to `1.65x`; the moment `dd_t` rises (`10% -> 28%`) or breadth cools (`r_t < 0.70`), exposure collapses back toward `0.40x–1.00x`, sitting in `DTB3 + 30% C1 Carry` whenever `BTC/ETH < SMA50`.
  2. **`Q38_bull_leader_blend` (`40% Safe-QD + 30% Trend-Gated Top-4 Leader Basket + 30% C1 Carry + DTB3`):**
     Replaces `30%` of the static `BTC+ETH` trend sleeve with a **long-only, volatility-inverse weighted Top-4 16-coin Leader Basket**:
     - **Macro Bull Gate:** when `BTC close[t] <= BTC SMA50[t]`, **100% of the Leader sleeve sits in `DTB3` T-bills (`0%` exposure)**.
     - On **Mondays (UTC)** while `BTC > SMA50`: rank all eligible `16` coins (`>= 60` prior closes) where `close_i[t] > SMA50_i[t]` and `score_i[t] = (ret21_i[t]) / sigma21_i[t] > 0`; pick top `K <= 4` coins weighted proportional to `1 / sigma21_i[t]`, scaled by `min(4, len(qualified)) / 4` (unused slots stay in `DTB3`), and scaled by the portfolio's `10%->28%` soft drawdown taper `m(dd_t)`.
     - **Mid-week risk-off guard:** if at any daily close `t` a held coin drops `<= its SMA50` or `BTC <= BTC SMA50`, exit that position immediately into `DTB3` (only re-select on Mondays).

## Rules & Costs (`2` trials: `769 -> 771`, window `2020-01-01..2026-10-31`)
- Signals are warmed up on full candle history prior to `2020-01-01` (`strictly causal: decision at close t -> held over day t+1`).
- **Base cost model (conservative, identical to `PREREG36`):** `us-futures` (`6+1` bps turnover + `0.1 bp/h` funding on all held crypto legs in Trend and Leader sleeves) + `S19.BASE` (`3/7` bps) on `30%` `C1` carry + `DTB3` on all uninvested cash (`max(0, 1 - carry_share - active_crypto_notional)` when net trend+leader weight `< 0.70`).
- **Stress cost model:** `us-futures-stress` (`6+1` bps + `1.0 bp/h` funding) + `S19.STRESS`.
- **Operational Spot/Margin check (descriptive only, not for pass verdict):** charge `us-spot` (`2+1` bps, `0` funding) on portfolio crypto notional up to `1.0 - carry_share = 0.70`, and `us-futures` funding only on excess leverage above `0.70`, reflecting actual US spot + derivatives execution at `51 ms` RTT.

## Pass rule (`2` fixed trials: `769 -> 771`, evaluated against `S36_qd_carry_blend` rebuilt in the same run)
A variant PASSES only if ALL FIVE hold under `base` costs over `2020-01-01..2026-10-31`:
1. `max_dd_pct >= -40.0%` (strictly `>= 10` pts inside the `50%` risk limit).
2. `geo_monthly_pct > S36_qd_carry_blend geo_monthly_pct` **AND** `bull_mean_monthly_pct >= S36_qd_carry_blend bull_mean_monthly_pct + 2.0`.
3. `sharpe > S36_qd_carry_blend sharpe`.
4. `bear_mean_monthly_pct >= S36_qd_carry_blend bear_mean_monthly_pct - 0.50`.
5. `A_2025_26 geo_monthly_pct > 0` **AND** `stress geo_monthly_pct > 0`.

## Prediction (written before reading real data)
- `Q38_kuramoto_booster` (`1.65x` cap when `r_t >= 0.70`): because it remains `100%` `BTC+ETH` with `0.40–1.00x` fallback, `bear` and `2022` stay very close to `S36_qd_carry_blend` (`~-2.6%` vs `-2.4%`) while bull months jump from `+8.0%` into `+11.5%–+14.5%/mo`, pushing overall geo return to `~+3.5%–+4.2%/mo` with Max DD around `−31%..−37%`. Strong candidate (`~1 in 2`) to pass all 5 checks if `0.1 bp/h` futures funding on `1.65x` in bull cycles doesn't excessively clip Sharpe.
- `Q38_bull_leader_blend`: altcoin dispersion in 2020–21 and 2023–25 rallies could lift bull capture strongly, **but** individual altcoins (`DOGE/SHIB/SOL/AVAX`) carry higher single-day gap reversals on Monday-to-Monday holds even with `SMA50` stop exits, risking slightly worse bear/whipsaw drag and `0.1 bp/h` futures funding; `~1 in 3` chance to beat `S36_qd_carry_blend` on Sharpe.
- One run only after synthetic tests pass.

## Result (run once, 2026-11-14): Both FAIL strict check 2 (`bull >= S36 + 2.0%`), though `Q38_bull_leader_blend` BEATS `S36_qd_carry_blend` on geo return, Sharpe, DSR, flat, bull, and stress costs

Window `2020-01-01` to `2026-10-31` (`82` complete months: `26` bear, `18` flat, `38` bull).

| Variant (`us-futures` + `S19.BASE`) | %/mo | max DD | Sharpe | Calmar | DSR | worst mo | worst day | 2022 %/mo | A (25–26) | bear / flat / bull | stress %/mo | result |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---|---:|---|
| `S36_qd_carry_blend` (comparator) | +2.64 | −27.8% | 1.285 | 1.14 | 0.572 | −12.41% | −11.34% (2021-01-21) | −1.35 | +1.04 | −2.4 / +0.0 / +8.0 | +0.76 | – |
| `Q38_kuramoto_booster` | +2.37 | −35.5% | 0.952 | 0.80 | 0.249 | −14.62% | −18.73% (2021-01-21) | −1.44 | +0.53 | −2.7 / −0.5 / +8.0 | +0.30 | **FAIL** (checks 2, 3) |
| **`Q38_bull_leader_blend`** | **+2.85** | **−30.0%** | **1.377** | **1.14** | **0.663** | −12.30% | −11.17% (2021-01-21) | −1.57 | +0.20 | −2.7 / **+0.2** / **+8.7** | **+1.06** | **FAIL** (check 2b: `+8.7 < +10.0`) |

Operational check (`us-spot` `2+1 bps, 0 funding` up to `0.70` NAV + `us-futures` on margin > `0.70`):
- **`Q38_bull_leader_blend_spot`:** **`+3.14%/mo`**, max DD **`−29.6%`**, Sharpe **`1.493`**, Calmar `1.28`, `DSR 0.766`, bear/flat/bull `−2.6 / +0.4 / +9.3%`, `2022 −1.54%/mo`, `A +0.46%/mo`.
- **`Q38_kuramoto_booster_spot_margin`:** `+2.71%/mo`, max DD `−34.9%`, Sharpe `1.048`, Calmar `0.93`, bull `+8.7%`.

**Signal diagnostics:** breadth `r_t >= 0.70` held on `36.1%` of days (`mean r_t = 0.468`); the `BTC`-gated Top-4 inverse-vol Leader Basket was active on `47.9%` of days with `mean_gross = 0.439`.

**Reading:**
- **Breadth-chasing leverage boosting (`Q38_kuramoto_booster`) fails cleanly:** raising leverage to `1.65x` once `>= 70%` of tracked coins are already above `SMA50` adds exposure late in rallies, amplifying `2021-01-21` single-day loss (`-11.34% -> -18.73%`), driving `mean_taper_mult` down (`0.787 -> 0.637` in base, `0.461` in stress), and failing both return and Sharpe.
- **Long-only trend-gated Leader Rotation (`Q38_bull_leader_blend`) works structurally:** unlike `PREREG29`'s always-on long/short alt ranking, gating `16` coins by `BTC > SMA50` + `coin > SMA50` + `DTB3` + weekly Monday rebalance + mid-week SMA stop-outs passed 4/5 checks, **beat `S36_qd_carry_blend` on full return (`+2.85%` vs `+2.64%`), Sharpe (`1.377` vs `1.285`), DSR (`0.663` vs `0.572`), flat (`+0.2%` vs `+0.0%`), bull (`+8.7%` vs `+8.0%`), and stress return (`+1.06%` vs `+0.76%`)** while keeping max DD at `−30.0%`. On US Spot (`Q38_bull_leader_blend_spot`), it reached **`+3.14%/mo`, Sharpe `1.493`, DSR `0.766`, DD `−29.6%`**. It failed only the pre-registered `+2.0%/mo` bull margin check (`+8.7%` vs `+10.0%`), so it stays research-only (`PREREG38` closed, trials `769 -> 771`).

