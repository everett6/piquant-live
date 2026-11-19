"""Unit tests for autonomous causal alpha-discovery daemon (`auto_discover.py`)."""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd
import pytest

import auto_discover as AD
import feature_store as FS


def test_safe_alpha_evaluator_accepts_valid_expressions():
    btc, eth = AD._make_synthetic_ohlcv(n=120, seed=7)
    feats = FS.build_unified_features(btc, eth)
    exprs = [
        "ema(btc_sma50_dist, 10) * 3.0 - max(vol_ratio_5_20 - 1.2, 0.0)",
        "where(btc_vol20_ann < 0.80, tanh(btc_ret21 * 3.0), -0.5)",
        "clip(roll_mean(btc_signed_flow5, 10) + abs(pair_spread_z20), -2.0, 2.0)",
        "sigmoid(btc_minus_eth_5d) * sign(eth_sma50_dist)",
    ]
    for expr in exprs:
        sig_ov = AD.SafeAlphaEvaluator.evaluate(expr, feats, role="overlay")
        assert len(sig_ov) == len(feats)
        assert np.all(np.isfinite(sig_ov.to_numpy()))
        assert sig_ov.min() >= 0.40 - 1e-9
        assert sig_ov.max() <= 1.25 + 1e-9

        sig_st = AD.SafeAlphaEvaluator.evaluate(expr, feats, role="standalone")
        assert sig_st.min() >= 0.0 - 1e-9
        assert sig_st.max() <= 1.0 + 1e-9


@pytest.mark.parametrize(
    "bad_expr",
    [
        "__import__('os').system('echo pwned')",
        "eval('btc_ret1')",
        "btc_ret1.shift(-1)",
        "open('/etc/passwd')",
        "unknown_feature + 1.0",
        "1.0 + 2.0",  # no feature referenced
        "ema(btc_ret1, btc_ret5)",  # non-constant window
        "abs(btc_ret1, 2)",  # wrong arity
        "(lambda x: x)(btc_ret1)",
        "[x for x in btc_ret1]",
        "btc_ret1 < eth_ret5 < btc_ret21",  # chained compare
    ],
)
def test_safe_alpha_evaluator_rejects_unsafe_asts(bad_expr: str):
    with pytest.raises(AD.UnsafeExpressionError):
        AD.SafeAlphaEvaluator.validate(bad_expr)


def test_causality_sandbox_passes_valid_and_rejects_constant():
    ok, msg = AD.check_causality_sandbox("ema(btc_sma50_dist, 10) * 3.0", role="overlay")
    assert ok, msg

    # Constant output via clip(x, 0, 0) must be rejected as degenerate
    ok_const, msg_const = AD.check_causality_sandbox("clip(btc_ret1, 0.0, 0.0)", role="overlay")
    assert not ok_const
    assert "degenerate" in msg_const


def test_parse_llm_expressions_json_and_lines():
    raw_json = 'Here are candidates:\n["ema(btc_ret21, 5)", "tanh(btc_sma50_dist * 3.0)"]'
    parsed = AD.parse_llm_expressions(raw_json)
    assert parsed == ["ema(btc_ret21, 5)", "tanh(btc_sma50_dist * 3.0)"]

    raw_lines = "1. `ema(btc_ret5, 10)`\n2. `tanh(eth_sma50_dist)`\n"
    parsed2 = AD.parse_llm_expressions(raw_lines)
    assert parsed2 == ["ema(btc_ret5, 10)", "tanh(eth_sma50_dist)"]


def test_ollama_fallback_and_dry_run_synth_end_to_end(tmp_path):
    state_p = str(tmp_path / "state.json")
    shadow_p = str(tmp_path / "shadow.json")
    log_p = str(tmp_path / "log.jsonl")

    # Point Ollama URL at an unreachable port to verify graceful fallback to symbolic
    summary = AD.run_discovery(
        n_candidates=4,
        mode="auto",
        ollama_url="http://127.0.0.1:19999",
        model="lfm2.5",
        role="overlay",
        seed=42,
        dry_run_synth=True,
        state_path=state_p,
        shadow_path=shadow_p,
        log_path=log_p,
    )
    assert summary["evaluated"] == 4
    assert summary["ollama_note"] is not None
    assert summary["n_trials_total"] == AD.INITIAL_TRIALS + 4
    assert os.path.exists(state_p)
    assert os.path.exists(shadow_p)
    assert os.path.exists(log_p)

    with open(shadow_p, "r", encoding="utf-8") as fh:
        shadow_data = json.load(fh)
    assert isinstance(shadow_data, list)
    for item in shadow_data:
        assert item["status"] == "SHADOW_PAPER_ONLY"
        assert item["accepted"] is True

    with open(log_p, "r", encoding="utf-8") as fh:
        log_lines = [json.loads(line) for line in fh if line.strip()]
    assert len(log_lines) == 4
