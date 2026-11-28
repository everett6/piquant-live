import numpy as np

import decay_cusum as DC


def test_threshold_matches_siegmund_target():
    for k in (0.01, 0.04, 0.2):
        h = DC.solve_threshold(k)
        assert abs(DC.siegmund_arl0(k, h) - DC.ARL0_DAYS) / DC.ARL0_DAYS < 1e-6


def test_reference_too_short_or_flat_gives_none():
    assert DC.reference_params(np.full(179, 0.001)) is None
    assert DC.reference_params(np.zeros(500)) is None
    assert DC.cusum_decay(np.zeros(500), np.ones(10)) is None


def test_cusum_steps_by_hand():
    p = {"mu0": 0.001, "sigma": 0.01, "k": 0.05, "h": 1.0}
    # z = clip((0.001 - r)/0.01, -4, 4) - 0.05
    path = DC.cusum_path(np.array([-0.009, 0.011, -0.1, 0.0]), p)
    assert np.allclose(path, [0.95, 0.0, 3.95, 3.95 + 0.1 - 0.05])


def test_mean_drop_alarms_and_steady_does_not():
    rng = np.random.default_rng(1)
    ref = 0.002 + 0.01 * rng.standard_normal(1095)
    good = DC.cusum_decay(ref, 0.001 + 0.01 * rng.standard_normal(200))
    bad = DC.cusum_decay(ref, -0.004 + 0.01 * rng.standard_normal(400))
    assert good is not None and bad is not None
    assert bad["alarm"] and bad["alarm_day"] is not None
    assert good["s"] < good["h"]
    assert DC.reference_params(ref)["ref_days"] == 1095


def test_no_edge_reference_uses_k_floor():
    rng = np.random.default_rng(2)
    p = DC.reference_params(-0.001 + 0.01 * rng.standard_normal(400))
    assert p["k"] == DC.K_FLOOR


def test_health_rule():
    c = {"alarm": False, "s": 0.0, "h": 10.0}
    assert DC.classify_health_cusum(5, -2.0, c) == "WARMUP"
    assert DC.classify_health_cusum(5, -16.0, c) == "KILLED"
    assert DC.classify_health_cusum(5, -2.0, {**c, "alarm": True}) == "KILLED"
    assert DC.classify_health_cusum(20, -2.0, {**c, "s": 5.0}) == "DECAYING"
    assert DC.classify_health_cusum(20, -2.0, c) == "HEALTHY"


def test_max_drawdown_by_hand():
    assert np.allclose(DC.max_drawdown_pct(np.array([0.1, -0.5, 0.2])), [-50.0])
    assert np.allclose(DC.max_drawdown_pct(np.array([[0.1, 0.1], [-0.2, 0.0]])), [0.0, -20.0])


def test_dd_limit_scales_with_vol_and_is_clamped_and_deterministic():
    rng = np.random.default_rng(3)
    z = rng.standard_normal(1095)
    calm, mid, wild = (0.0005 + s * z for s in (0.002, 0.009, 0.05))
    assert DC.dd_limit_pct(calm) == -10.0
    assert DC.dd_limit_pct(wild) == -35.0
    m = DC.dd_limit_pct(mid)
    assert -35.0 < m < -10.0 and m == DC.dd_limit_pct(mid)
    assert DC.dd_limit_pct(np.zeros(100)) is None


def test_health_rule_v2():
    c = {"alarm": False, "s": 0.0, "h": 10.0}
    assert DC.classify_health_v2(5, -12.0, c, -20.0) == "WARMUP"
    assert DC.classify_health_v2(5, -21.0, c, -20.0) == "KILLED"
    assert DC.classify_health_v2(20, -2.0, {**c, "alarm": True}, -20.0) == "KILLED"
    assert DC.classify_health_v2(20, -2.0, {**c, "s": 6.0}, -20.0) == "DECAYING"
    assert DC.classify_health_v2(20, -19.0, c, -20.0) == "HEALTHY"
