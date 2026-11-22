# PREREG57: Synthesis of HRP + Hurst/Conformal Gate (`Subagent A`) & AdaHedge + Yang-Zhang/Kalman (`Subagent B`)

## Hypothesis (Round 2 Research Synthesis)
Combining:
1. **Within-Sleeve Hierarchical Risk Parity (`hrp_hurst_alpha.hrp_weights`)** across Crypto (`BTC+ETH+SOL` + `C1` carry) and US Equities/Macro (`12` tickers + `3%` buffer),
2. **Cross-Sleeve Parameter-Free AdaHedge (`kalman_yz_alpha.adahedge_weights`)** + **Yang-Zhang Gap-Vol / Kalman Velocity Crash Brake (`kalman_yz_regime_scale`)**, and
3. **Causal Hurst Variogram + Von Neumann Entropy + Pre-2024 Frozen Split-Conformal Conviction Gate (`conformal_conviction_gate`, `< 2024-01-01` calibration cutoff)**,
will strictly lift `1x` Spot CAGR above `Q56_hybrid_1x` (`+10.93%`) while keeping full Max DD below `baseline_mft_lft_1x` (`-6.92%`), pushing Calmar `>= 3.00`, full Sharpe `> 2.15`, and `2024–2026` OOS Sharpe `>= 2.00`.

## Pre-Registered Pass Gates (`Q57_synth_1x` at `n_trials = 900`)
1. `cagr_gt_q56`: `Q57_synth_1x` full CAGR `>= 14.0%`
2. `max_dd_lt_baseline`: `Q57_synth_1x` full Max DD `< 6.5%`
3. `sharpe_and_dsr`: `Q57_synth_1x` full Sharpe `> 2.15` AND `DSR(900) >= 0.98`
4. `calmar_ge_3p0`: `Q57_synth_1x` full Calmar `>= 3.00`
5. `oos_2024_2026_sharpe_ge_2p0`: `Q57_synth_1x` `2024–2026` OOS Sharpe `>= 2.00`
