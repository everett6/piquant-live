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

    # Must ignore llama-cli banner and trailing `[ Prompt: ... | Generation: ... ]` bracket stats
    raw_llama_cli = (
        "Loading model...\n"
        "build : b1-e4b620a\n"
        '[\n'
        ' "ema(btc_ret1, 10) * 0.5 - tanh(abs(vol_ratio_5_20 - 1.0))",\n'
        ' "where(btc_vol20_ann < 0.8, ema(btc_sma50_dist, 7), -0.5)"\n'
        ']\n'
        "\n[ Prompt: 15.6 t/s | Generation: 3.2 t/s ]\nExiting...\n"
    )
    parsed3 = AD.parse_llm_expressions(raw_llama_cli)
    assert parsed3 == [
        "ema(btc_ret1, 10) * 0.5 - tanh(abs(vol_ratio_5_20 - 1.0))",
        "where(btc_vol20_ann < 0.8, ema(btc_sma50_dist, 7), -0.5)",
    ]


def test_generate_llamacpp_candidates_cli_and_server(tmp_path, monkeypatch):
    fake_bin = tmp_path / "llama-cli"
    fake_bin.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
    fake_bin.chmod(0o755)
    fake_model = tmp_path / "LFM3-1.7B-QAD-Q4_0.gguf"
    fake_model.write_bytes(b"GGUF")

    captured_cmd: list[str] = []

    class _FakeProc:
        returncode = 0
        stdout = (
            '[\n'
            '  "ema(btc_sma50_dist, 10) * 2.0",\n'
            '  "tanh(btc_ret21 * 3.0) - max(vol_ratio_5_20 - 1.2, 0.0)"\n'
            ']\n'
            "[ Prompt: 20.0 t/s | Generation: 4.5 t/s ]\n"
        )
        stderr = ""

    def _fake_run(cmd, **kwargs):
        captured_cmd.extend(cmd)
        return _FakeProc()

    monkeypatch.setattr(AD.subprocess, "run", _fake_run)

    exprs, note, src = AD.generate_llamacpp_candidates(
        n_candidates=2,
        llama_bin=str(fake_bin),
        llama_model=str(fake_model),
        llama_threads=3,
    )
    assert note is None
    assert src == "llamacpp:LFM3-1.7B-QAD-Q4_0.gguf"
    assert len(exprs) == 2
    assert "--reasoning" in captured_cmd and "off" in captured_cmd
    assert "--no-repack" in captured_cmd
    assert "-t" in captured_cmd and "3" in captured_cmd

    # Also verify OpenAI-compatible llama-server path
    class _FakeResp:
        def __enter__(self):
            return self

        def __exit__(self, *args):
            return False

        def read(self):
            return json.dumps({
                "choices": [{"message": {"content": '["ema(btc_ret5, 8) * 2.0"]'}}]
            }).encode("utf-8")

    monkeypatch.setattr(AD.urllib.request, "urlopen", lambda req, timeout=None: _FakeResp())
    exprs_srv, note_srv, src_srv = AD.generate_llamacpp_candidates(
        n_candidates=1,
        llama_url="http://127.0.0.1:8080",
    )
    assert note_srv is None
    assert src_srv == "llamacpp:server"
    assert exprs_srv == ["ema(btc_ret5, 8) * 2.0"]


def test_llamacpp_and_ollama_fallback_dry_run_synth_end_to_end(tmp_path):
    state_p = str(tmp_path / "state.json")
    shadow_p = str(tmp_path / "shadow.json")
    log_p = str(tmp_path / "log.jsonl")

    # Missing llama-cli binary + unreachable Ollama URL must gracefully fall back to symbolic
    summary = AD.run_discovery(
        n_candidates=4,
        mode="auto",
        llama_bin=str(tmp_path / "missing-llama-cli"),
        llama_model=str(tmp_path / "missing.gguf"),
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
    assert summary["llamacpp_note"] is not None
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


def test_resolve_default_llama_model_prefers_finetuned(tmp_path):
    ft = tmp_path / "piquant-alpha-lfm3-v2-Q4_K_M.gguf"
    base = tmp_path / "LFM3-1.7B-QAD-Q4_0.gguf"
    cands = (str(ft), str(base))

    # Neither exists -> falls back to last candidate (base)
    assert AD._resolve_default_llama_model(cands) == str(base)

    # Only base exists -> selects base
    base.write_bytes(b"GGUF")
    assert AD._resolve_default_llama_model(cands) == str(base)

    # Fine-tuned exists -> prefers fine-tuned over base
    ft.write_bytes(b"GGUF")
    assert AD._resolve_default_llama_model(cands) == str(ft)


def test_sanitize_expr_and_parse_strips_feedback_arrows():
    assert (
        AD._sanitize_expr("ema(btc_sma50_dist, 10) * 3.0 -> FULL_GATE: Geo/mo 1.2% < 2.15%")
        == "ema(btc_sma50_dist, 10) * 3.0"
    )
    assert (
        AD._sanitize_expr("`tanh(btc_vrp * 2.0)` -> AST_GATE: syntax error")
        == "tanh(btc_vrp * 2.0)"
    )
    assert (
        AD._sanitize_expr("clip(dvol_z20, -1.0, 1.0)->PURGED_CV_GATE: median_sharpe=0.1")
        == "clip(dvol_z20, -1.0, 1.0)"
    )

    leaked_json = json.dumps([
        "ema(btc_ret21, 5) -> FULL_GATE: DSR 0.1 < 0.8",
        "ema(btc_ret21, 5)",
        "tanh(btc_vrp * 2.0) -> PURGED_CV_GATE: median_sharpe=0.2",
        "`clip(btc_fund_z20, -1.0, 1.0)` -> AST_GATE: syntax error",
    ])
    parsed = AD.parse_llm_expressions(leaked_json)
    assert parsed == [
        "ema(btc_ret21, 5)",
        "tanh(btc_vrp * 2.0)",
        "clip(btc_fund_z20, -1.0, 1.0)",
    ]


def test_load_recent_feedback_excludes_ast_gate_and_arrows(tmp_path):
    log_p = tmp_path / "log.jsonl"
    records = [
        {"expr": "bad -> syntax", "accepted": False, "reject_reason": "AST_GATE: syntax error: invalid syntax"},
        {"expr": "ema(btc_ret5, 10)", "accepted": False, "reject_reason": "PURGED_CV_GATE: median_sharpe=0.10, pos_folds=1/5"},
        {"expr": "unknown_feat + 1", "accepted": False, "reject_reason": "AST_GATE: unknown identifier: unknown_feat"},
        {"expr": "tanh(btc_vrp * 2.0)", "accepted": False, "reject_reason": "FULL_GATE: DSR 0.4 < 0.8; Geo/mo 1.1% < 2.15%"},
        {"expr": "ema(btc_sma50_dist, 10)", "accepted": True, "reject_reason": ""},
    ]
    log_p.write_text("\n".join(json.dumps(r) for r in records) + "\n", encoding="utf-8")

    fb = AD._load_recent_feedback(str(log_p), limit=5)
    assert len(fb) == 2
    assert all("->" not in item for item in fb)
    assert all("AST_GATE" not in item for item in fb)
    assert fb[0] == 'expr="ema(btc_ret5, 10)" (reason: PURGED_CV_GATE: median_sharpe=0.10, pos_folds=1/5)'
    assert fb[1] == 'expr="tanh(btc_vrp * 2.0)" (reason: FULL_GATE: DSR 0.4 < 0.8; Geo/mo 1.1% < 2.15%)'

    prompt = AD._build_alpha_prompt(4, recent_feedback=fb)
    assert "btc_vrp" in prompt and "stable_flow_21d" in prompt and "qqq_ret21" in prompt
    assert "->" not in prompt


def test_venue_spot_gate_and_shadow_dedup(tmp_path, monkeypatch):
    base_metrics = {
        "deflated_sharpe": 0.90,
        "max_dd_pct": -22.0,
        "mean_dd_pct": 9.5,
        "geo_monthly_pct": 2.30,
        "sharpe": 1.40,
        "calmar": 1.35,
        "A_geo_monthly_pct": 0.50,
    }
    stress_metrics = {"geo_monthly_pct": 0.60}
    spot_pass = {
        "deflated_sharpe": 0.85,
        "max_dd_pct": -18.5,
        "mean_dd_pct": 8.4,
        "geo_monthly_pct": 2.40,
        "sharpe": 1.52,
        "calmar": 1.50,
        "A_geo_monthly_pct": 0.80,
    }
    spot_fail = dict(spot_pass, sharpe=1.42, calmar=1.35)

    # Futures passes on base_metrics, spot checks spot_metrics against SPOT_BENCHMARK_*
    ok_fut, _ = AD._check_full_gates(base_metrics, stress_metrics)
    assert ok_fut
    ok_spot_fail, reason_fail = AD._check_full_gates(base_metrics, stress_metrics, spot=spot_fail, venue="spot")
    assert not ok_spot_fail
    assert "1.483" in reason_fail and "1.46" in reason_fail
    ok_spot_pass, _ = AD._check_full_gates(base_metrics, stress_metrics, spot=spot_pass, venue="spot")
    assert ok_spot_pass

    # End-to-end shadow dedup keyed by (expr, venue, role)
    monkeypatch.setattr(AD, "generate_symbolic_candidates", lambda n_candidates, seed=42: ["ema(btc_sma50_dist, 10) * 3.0"])
    monkeypatch.setattr(
        AD,
        "evaluate_purged_cv",
        lambda *args, **kwargs: {"fold_sharpes": [1.5] * 5, "median_fold_sharpe": 1.5, "positive_folds": 5, "passed": True},
    )
    monkeypatch.setattr(
        AD,
        "_evaluate_synthetic_full",
        lambda *args, **kwargs: (base_metrics, stress_metrics, spot_pass),
    )

    state_p = str(tmp_path / "state.json")
    shadow_p = str(tmp_path / "shadow.json")
    log_p = str(tmp_path / "log.jsonl")

    # Run twice for spot -> only 1 entry in shadow queue, with venue="spot"
    for _ in range(2):
        s1 = AD.run_discovery(
            n_candidates=1, mode="symbolic", role="overlay", venue="spot",
            dry_run_synth=True, state_path=state_p, shadow_path=shadow_p, log_path=log_p,
        )
        assert s1["accepted"] == 1
        assert s1["shadow_queue_len"] == 1

    # Same expr on futures -> distinct (expr, venue, role) key -> appended (len=2)
    s2 = AD.run_discovery(
        n_candidates=1, mode="symbolic", role="overlay", venue="futures",
        dry_run_synth=True, state_path=state_p, shadow_path=shadow_p, log_path=log_p,
    )
    assert s2["accepted"] == 1
    assert s2["shadow_queue_len"] == 2

    with open(shadow_p, "r", encoding="utf-8") as fh:
        q = json.load(fh)
    assert [(item["expr"], item["venue"], item["role"]) for item in q] == [
        ("ema(btc_sma50_dist, 10) * 3.0", "spot", "overlay"),
        ("ema(btc_sma50_dist, 10) * 3.0", "futures", "overlay"),
    ]
    with open(log_p, "r", encoding="utf-8") as fh:
        logged = [json.loads(line) for line in fh if line.strip()]
    assert [r["venue"] for r in logged] == ["spot", "spot", "futures"]
