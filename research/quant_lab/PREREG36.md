# Pre-registration 36: risk-capped QD coherence trend (`≤ 40%` max DD) + T-bills + always-on C1 funding carry (committed before round-36 code reads real data)

## Why
- **`QD_btc_eth_coherence_2x` (`PREREG35` pass, trial `764`)** is now the live paper trend champion (`+8.45%/mo`, Sharpe `1.525`, bear `−5.3%/mo`), but its `−76.5%` max drawdown exceeds the owner's `50%` hard risk limit.
- **`C1` funding carry (`PREREG19` pass)** earns `+5–8%/yr` (`+5.06%/yr` stress, `−1%` DD, uncorrelated `~0`). `PREREG33` found `C1` earns `17–22%/yr` during trend-on days vs `4%/yr` on idle days (so idle-only carry failed), leaving an **always-on `Trend + C1 Carry` blend** (`README.md` Open Ideas) untested.
- **`DTB3` T-bill yield (`PREREG29/33`)** adds free risk-free carry on idle cash sleeves (`+0.12%/mo` on `P1`), and `QD`'s slow coherence ramp holds slightly more cash than `P1`.
- **Goal:** Keep max drawdown safely `≤ 40%` (at least `10` percentage points below the owner's `50%` risk tolerance), raise Sharpe above `QD_2x`, and test cleanly whether a dedicated always-on `70% Safe-QD + 30% C1 Carry` blend beats `100% Safe-QD + T-bills` without leverage creep (`total notional ≤ 1.0x NAV`).

## Rule (`2` trials: `765 → 767`, window `2020-01-01..2026-10-31`)
- **Base trend signal (`QD_1x`):** `S35.coherence_positions(daily, lev=1.0, n=50, tau=5.0)` for `BTC` and `ETH` in equal halves (`0.5` each), warmed up on full pre-2020 candles so `SMA50` and `trend_age` are live on `2020-01-01`.
- **Non-locking soft drawdown taper:** At each close `t`, from realized portfolio equity `E_t` and running peak `P_t = max_{s<=t} E_s` (`dd_t = 1 - E_t / P_t`):
  - `m(dd_t) = 1.0` for `dd_t <= 0.10`
  - linear decay from `1.0` to `0.40` for `0.10 < dd_t < 0.28`
  - `m(dd_t) = 0.40` floor for `dd_t >= 0.28` (never locks at `0%` cash).
  Effective trend target for day `t+1` is `pos_t = QD_1x[t] * m(dd_t)`.
- **Idle cash T-bills:** Each day `t+1`, uninvested capital within the trend budget `max(0, trend_share * (1 - 0.5 * (pos_btc[t] + pos_eth[t])))` accrues `DTB3[t] / 100 / 365` (lagged 1 day).
- **Two fixed variants (`2` trials: `765 → 767`):**
  1. **`S36_safe_qd`:** `100%` `Safe-QD` (`trend_share = 1.00`, `carry_share = 0.00`) + `DTB3` on idle cash.
  2. **`S36_qd_carry_blend`:** `70%` `Safe-QD` (`trend_share = 0.70`) + **`30%` always-on `C1` funding carry (`carry_share = 0.30`)** (`S19.coin_returns(..., "C1")` across `BTC`/`ETH`) + `DTB3` on idle trend cash.

## Comparators and costs
- **Comparators (same window `2020-01-01..2026-10-31`, not charged as trials):**
  - `QD_2x` (`S35.coherence_positions(..., lev=2.0)`)
  - `R1_1x_plus_bills` (`1x` `SMA50` BTC+ETH + `DTB3` on idle sleeves)
  - `C1_carry_alone` (`100%` `C1` carry)
- **Costs:** `base` (`us-futures` `6+1` bps + `0.1 bp/h` funding on trend; `S19.BASE` `3/7` bps on carry) and `stress` (`us-futures-stress` `1.0 bp/h` funding; `S19.STRESS` `6/14` bps + `0.75` funding haircut on carry). Also report `S36_safe_qd` under `us-spot` (`2+1` bps, `0` funding).
- **Split window:** `2020-01-01..2026-10-31` (`82` months), plus `2022` and `A (2025-07..2026-10)` and bear/flat/bull monthly means.

## Pass rule (`2` trials: `765 → 767`)
- **`S36_safe_qd` PASSES** only if ALL hold (base costs):
  1. `max_dd_pct >= -40.0%`
  2. `sharpe > QD_2x sharpe`
  3. `calmar (geo_mo*12/|maxDD|) > R1_1x_plus_bills calmar`
  4. `A_2025_26 > 0` AND `stress full geo_monthly_pct > 0`
- **`S36_qd_carry_blend` PASSES** only if ALL hold (base costs):
  1. `max_dd_pct >= -35.0%`
  2. `sharpe > max(QD_2x sharpe, S36_safe_qd sharpe)`
  3. `bear_mean_monthly_pct > S36_safe_qd bear_mean_monthly_pct`
  4. `A_2025_26 > 0` AND `stress full geo_monthly_pct > 0`

## Prediction (written before running on real data)
- `S36_safe_qd` (`1.0x` max `QD` + `10%→28%` soft taper to `0.40` floor + `DTB3`) should cut `2020–26` Max DD from `QD_2x`'s likely `~−55%..−76%` down into `−22%..−30%` while preserving Sharpe around `~1.3–1.6` and positive `A_2025_26`.
- `S36_qd_carry_blend` (`70% Safe-QD + 30% C1 + DTB3`) will likely trim Max DD further (to `~−16%..−24%`), improve bear/flat months by `+0.1` to `+0.3%/mo`, and beat `Safe-QD` on Sharpe, while giving up some peak bull compounding (~`0.4–0.7%/mo` lower geo return than `100% Safe-QD`).
- One run only.

## Result (run once, 2026-11-14): `S36_qd_carry_blend` PASSES ALL 5 checks; `S36_safe_qd` FAILS Sharpe check alone

Window `2020-01-01` to `2026-10-31` (`82` complete months: `28` bear, `18` flat, `36` bull).

| Variant (`base` costs) | %/mo | max DD | Sharpe | Calmar | worst mo | worst day | 2022 %/mo | A (25–26) | bear / flat / bull | stress %/mo | result |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---|---:|---|
| `QD_2x` (comp) | +5.60 | −73.1% | 1.208 | 0.92 | −35.05% | −32.48% (2021-01-21) | −6.65 | +2.05 | −9.4 / −1.9 / +25.1 | −0.81 | – |
| `R1_1x_plus_bills` (comp) | +4.04 | −56.3% | 1.284 | 0.86 | −20.04% | −16.24% (2021-01-21) | −4.30 | +2.21 | −5.4 / −1.1 / +14.9 | +0.35 | – |
| `C1_carry_alone` (comp) | +0.66 | −0.6% | 9.665 | 13.12 | −0.45% | −0.24% (2020-03-13) | +0.08 | +0.17 | +0.4 / +0.5 / +0.9 | +0.41 | – |
| `S36_safe_qd` | +2.70 | −33.5% | 1.078 | 0.97 | −16.78% | −16.24% (2021-01-21) | −1.43 | +1.08 | −3.1 / −0.5 / +9.1 | +0.60 | **FAIL** (Sharpe `1.078 < 1.208`) |
| **`S36_qd_carry_blend`** | **+2.64** | **−27.8%** | **1.285** | **1.14** | **−12.41%** | **−11.34% (2021-01-21)** | **−1.35** | **+1.04** | **−2.4 / +0.0 / +8.0** | **+0.76** | **PASS (5/5)** |

Operational spot check (`us-spot`: `2+1` bps, `0` funding): `S36_safe_qd_spot` scored **`+3.20%/mo`**, max DD **`−32.7%`**, Sharpe **`1.224`**, Calmar `1.18`, `2022 −1.42%/mo`, `A +1.58%/mo`, `DSR 0.509` (bear/flat/bull `−3.0 / −0.1 / +10.1%`).

**Reading:**
- Adding **always-on `30% C1` funding carry** to **`70% Safe-QD + DTB3`** solved `PREREG33`'s flaw: instead of sitting in carry only during low-funding flat markets (`4%/yr`), the carry sleeve remained invested through `QD`'s bull runs (`+0.9%/mo` mean in bull months) with `-0.6%` standalone DD.
- That carry cushion cut `Safe-QD` drawdowns (`−33.5% → −27.8%`, staying `22.2` pts clear of the `50%` risk ceiling) so the taper stayed lightly engaged (`mean mult = 0.845` vs `0.693`, never hitting the `0.40` floor), lifted **Sharpe (`1.078 → 1.285`)** and **Calmar (`0.97 → 1.14`)**, turned flat months to `+0.0%`, cut `2022` to `−1.35%/mo`, and beat every trend comparator under stress costs (`+0.76%/mo` vs `QD_2x −0.81%/mo`), with `DSR = 0.572` across all `767` trials.
- `S36_safe_qd` on futures paid `0.1 bp/h` funding + `7 bps` per side for capped `1x` exposure, dragging Sharpe to `1.078` (failing Check 2); on spot (`0` funding, `3 bps/side`) it reaches Sharpe `1.224` and `+3.20%/mo` (`−32.7%` DD).
- Trials charged: **`767`** (`765 → 767`). Paper-ledger integration of `S36_qd_carry_blend` awaits owner confirmation.

