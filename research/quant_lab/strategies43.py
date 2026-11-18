"""Round-43 (PREREG43): Combining PREREG42 Expected-Drawdown control (6%->20% taper, R3 fast-exit,
higher C1 carry share) with PREREG38 BTC-gated Top-4 inverse-volatility Leader Rotation.
Pure functions; zero disk I/O."""
from __future__ import annotations

import pandas as pd

import strategies38 as S38
import strategies42 as S42


def asymmetric_dualspeed_positions(
    qd_pos: pd.Series,
    r3_pos: pd.Series,
    qd_fraction: float,
) -> pd.Series:
    """Convex combination qd_fraction * qd_pos + (1 - qd_fraction) * r3_pos, strictly causal."""
    if not (0.0 <= qd_fraction <= 1.0):
        raise ValueError(f"qd_fraction must be in [0, 1], got {qd_fraction}")
    q = qd_pos.fillna(0.0).astype(float)
    r = r3_pos.reindex(q.index).fillna(0.0).astype(float)
    return qd_fraction * q + (1.0 - qd_fraction) * r


def evaluate_prereg43_variant(
    btc: pd.DataFrame,
    eth: pd.DataFrame,
    pos_b_base: pd.Series,
    pos_e_base: pd.Series,
    leader_closes: pd.DataFrame,
    leader_w: pd.DataFrame,
    carry_ret: pd.Series,
    rate_pct: pd.Series,
    trend_share: float,
    leader_share: float,
    carry_share: float,
    taker_bps: float,
    slip_bps: float,
    funding_bps_h: float,
    spot_threshold: float | None = None,
    dd_start: float = 0.06,
    dd_full: float = 0.20,
    dd_floor: float = 0.40,
) -> tuple[pd.Series, pd.Series, dict[str, float]]:
    """Run causal multi-sleeve simulation and attach PREREG42 drawdown_diagnostics."""
    ret, mult, _ = S38.simulate_multi_sleeve_portfolio(
        btc=btc,
        eth=eth,
        pos_b_base=pos_b_base,
        pos_e_base=pos_e_base,
        leader_closes=leader_closes,
        leader_w=leader_w,
        carry_ret=carry_ret,
        rate_pct=rate_pct,
        trend_share=trend_share,
        leader_share=leader_share,
        carry_share=carry_share,
        taker_bps=taker_bps,
        slip_bps=slip_bps,
        funding_bps_h=funding_bps_h,
        spot_threshold=spot_threshold,
        use_taper=True,
        dd_start=dd_start,
        dd_full=dd_full,
        dd_floor=dd_floor,
    )
    diag = S42.drawdown_diagnostics(ret)
    return ret, mult, diag
