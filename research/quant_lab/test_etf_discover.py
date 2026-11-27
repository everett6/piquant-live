"""Tests for the ETF alpha-discovery path (`etf_discover.py`). Synthetic data only."""
from __future__ import annotations

import json

import numpy as np
import pandas as pd
import pytest

import auto_discover as AD
import etf_discover as ED


def test_universe_expanded_around_qqq():
    assert "QQQ" in ED.ETF_UNIVERSE and len(ED.ETF_UNIVERSE) == len(set(ED.ETF_UNIVERSE))
    for sym in ("SMH", "SOXX", "XLK", "IGV", "HYG", "TLT", "RSP"):
        assert sym in ED.ETF_UNIVERSE
    assert len(ED.ETF_UNIVERSE) >= 25


def test_features_complete_and_causal():
    c = ED.make_synthetic_etf_closes(n=500, seed=1)
    f = ED.build_etf_features(c)
    assert list(f.columns) == list(ED.ETF_FEATURES)
    tail = f.iloc[260:]
    assert tail.notna().all().all()
    # every feature at t must ignore closes after t
    shocked = c.copy()
    shocked.iloc[400:] *= 2.5
    f2 = ED.build_etf_features(shocked)
    pd.testing.assert_frame_equal(f.iloc[:400], f2.iloc[:400])


def test_missing_tickers_give_nan_not_error():
    c = ED.make_synthetic_etf_closes(n=300, seed=2, tickers=("QQQ", "SPY"))
    f = ED.build_etf_features(c)
    assert f["smh_minus_qqq_21d"].isna().all()
    assert f["qqq_minus_spy_21d"].iloc[30:].notna().all()
    with pytest.raises(ValueError):
        ED.build_etf_features(c, target="SMH")


def test_validator_uses_etf_allowlist():
    AD.SafeAlphaEvaluator.validate("tanh(tgt_sma200_dist * 5.0)", allowed=ED.ETF_FEATURE_SET)
    with pytest.raises(AD.UnsafeExpressionError):
        AD.SafeAlphaEvaluator.validate("tanh(btc_ret21)", allowed=ED.ETF_FEATURE_SET)
    with pytest.raises(AD.UnsafeExpressionError):
        AD.SafeAlphaEvaluator.validate("tanh(tgt_sma200_dist)")  # crypto default rejects ETF names


def test_templates_pass_ast_and_causality():
    for expr in ED.ETF_TEMPLATES:
        ok, msg = ED.check_etf_causality(expr)
        assert ok, f"{expr}: {msg}"


def test_causality_gate_catches_constant():
    ok, msg = ED.check_etf_causality("tgt_ret1 * 0.0 + 1.0")
    assert not ok and "constant" in msg


def test_simulate_lag_costs_and_cash():
    idx = pd.bdate_range("2020-01-01", periods=5, tz="UTC")
    close = pd.Series([100.0, 110.0, 110.0, 99.0, 99.0], index=idx)
    sig = pd.Series([1.0, 1.0, 0.0, 0.0, 0.0], index=idx)
    rate = pd.Series(0.0252, index=idx)  # 1 bp/day
    net, turn = ED.simulate_etf(close, sig, "standalone", cost_bps=10.0, rate_annual=rate)
    # day1: held 1.0 bought at close 0 -> +10% minus 10 bp; day2 flat; day3 held 0 -> cash only
    assert net.iloc[0] == 0.0 + 1e-4  # nothing held, earns cash
    assert net.iloc[1] == pytest.approx(0.10 - 0.001)
    assert net.iloc[2] == pytest.approx(0.0)
    assert net.iloc[3] == pytest.approx(1e-4 - 0.001)  # sold: no -10% loss, pays exit cost
    assert turn.sum() == pytest.approx(2.0)


def test_leverage_pays_financing():
    idx = pd.bdate_range("2020-01-01", periods=3, tz="UTC")
    close = pd.Series(100.0, index=idx)
    net, _ = ED.simulate_etf(close, pd.Series(1.25, index=idx), "overlay", 0.0, pd.Series(0.0, index=idx))
    assert net.iloc[2] == pytest.approx(-0.25 * ED.FINANCING_SPREAD / ED.ANNUAL_DAYS)


def test_gates_need_to_beat_buy_and_hold():
    bench = {"sharpe": 0.8, "calmar": 0.5, "max_dd_pct": -30.0}
    good = {"deflated_sharpe": 0.9, "sharpe": 1.0, "calmar": 0.7, "max_dd_pct": -20.0, "recent_geo_monthly_pct": 1.0}
    stress = {"geo_monthly_pct": 0.5}
    cv = {"mean_daily_turnover": 0.02}
    assert ED.check_etf_gates(good, stress, bench, cv) == (True, "PASS")
    ok, why = ED.check_etf_gates({**good, "sharpe": 0.7, "max_dd_pct": -35.0}, stress, bench, {"mean_daily_turnover": 0.3})
    assert not ok and "buy-hold" in why and "deeper" in why and "turnover" in why


def test_symbolic_generator_deterministic_and_valid():
    a = ED.generate_etf_symbolic_candidates(20, seed=3)
    assert a == ED.generate_etf_symbolic_candidates(20, seed=3)
    assert len(a) == len(set(a)) == 20
    for e in a:
        AD.SafeAlphaEvaluator.validate(e, allowed=ED.ETF_FEATURE_SET)


def test_prompt_names_target_and_features():
    p = ED.build_etf_prompt(4, ["expr=\"x\" (reason: y)"], None, target="SMH")
    assert "SMH" in p and "hyg_minus_ief_21d" in p and "btc_" not in p and "reason: y" in p


def test_loader_reads_cache_and_seals_holdout(tmp_path):
    idx = pd.date_range("2024-12-20", periods=20, freq="D", tz="UTC")
    for sym in ("QQQ", "SPY"):
        pd.DataFrame({"date": idx, "adj_close": np.arange(20.0) + 100}).to_csv(tmp_path / f"{sym}.csv", index=False)
    c = ED.load_etf_closes(tickers=("QQQ", "SPY"), cache_dir=str(tmp_path), end="2025-01-01")
    assert c.index.max() < pd.Timestamp("2025-01-01", tz="UTC")
    assert list(c.columns) == ["QQQ", "SPY"]


def test_run_etf_discovery_synthetic_end_to_end(tmp_path, monkeypatch):
    # make the LLM paths return nothing so the symbolic generator fills the batch
    monkeypatch.setattr(AD, "generate_llamacpp_candidates", lambda **k: ([], "off", "llamacpp"))
    monkeypatch.setattr(AD, "generate_ollama_candidates", lambda **k: ([], "off"))
    state = tmp_path / "state.json"
    shadow = tmp_path / "shadow.json"
    log = tmp_path / "log.jsonl"
    state.write_text(json.dumps({"n_trials": 900, "runs": 0, "accepted": 0, "rejected": 0}))
    out = ED.run_etf_discovery(n_candidates=6, dry_run_synth=True, state_path=str(state),
                               shadow_path=str(shadow), log_path=str(log))
    assert out["evaluated"] == 6 and out["venue"] == "etf:QQQ"
    assert all(r["source"] == "symbolic" for r in out["results"])
    assert out["n_trials_total"] >= 900 + 1
    recs = [json.loads(x) for x in log.read_text().splitlines()]
    assert len(recs) == 6 and all(r["asset_class"] == "etf" and r["target"] == "QQQ" for r in recs)
    assert json.loads(state.read_text())["runs"] == 1
    # rejected ETF attempts feed back into the next prompt
    fb = ED._load_etf_feedback(str(log))
    assert all("reason:" in x for x in fb)


def test_cli_rejects_unknown_target(tmp_path):
    with pytest.raises(ValueError):
        ED.run_etf_discovery(target="ARKK", dry_run_synth=True, state_path=str(tmp_path / "s.json"))
