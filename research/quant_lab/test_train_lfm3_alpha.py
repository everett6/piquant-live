"""Unit tests for LFM3-1.7B alpha LoRA SFT dataset & anti-leakage pipeline (`train_lfm3_alpha.py`)."""
from __future__ import annotations

import json
import os

import pandas as pd
import pytest

import auto_discover as AD
import train_lfm3_alpha as TA


def test_composite_score_formula():
    m_low_dd = {"sharpe": 1.5, "calmar": 1.4, "geo_monthly_pct": 2.5, "mean_dd_pct": 7.0}
    # 1.5 + 0.5*1.4 + 0.3*2.5 - 0.1*max(0, 7.0 - 8.5) = 1.5 + 0.7 + 0.75 = 2.95
    assert TA.compute_composite_score(m_low_dd) == pytest.approx(2.95, abs=1e-6)

    m_high_dd = {"sharpe": 1.5, "calmar": 1.4, "geo_monthly_pct": 2.5, "mean_dd_pct": 11.5}
    # 2.95 - 0.1*(11.5 - 8.5) = 2.65
    assert TA.compute_composite_score(m_high_dd) == pytest.approx(2.65, abs=1e-6)


def test_discovery_temporal_cutoff_no_2025_leakage():
    assert TA.DISCOVERY_START == "2020-01-01"
    assert TA.DISCOVERY_END == "2025-01-01"
    cutoff = pd.Timestamp("2025-01-01", tz="UTC")

    # Synthetic context must stay strictly before 2025-01-01
    syn_ctx = TA._load_synthetic_discovery_context(seed=7)
    assert syn_ctx["btc"].index.max() < cutoff
    assert syn_ctx["eth"].index.max() < cutoff
    assert syn_ctx["features"].index.max() < cutoff

    # If real daily CSVs & macro data exist on this machine, verify real discovery context too
    btc_csv = os.path.expanduser("~/piquant-collect/daily_csv/BTC-USD_1D.csv")
    dtb3_csv = os.path.expanduser("~/piquant-data/macro/DTB3.csv")
    if os.path.isfile(btc_csv) and os.path.isfile(dtb3_csv):
        real_ctx = TA.load_discovery_context()
        assert real_ctx["btc"].index.min() >= pd.Timestamp("2020-01-01", tz="UTC")
        assert real_ctx["btc"].index.max() < cutoff
        assert real_ctx["eth"].index.max() < cutoff
        assert real_ctx["features"].index.max() < cutoff
        assert real_ctx["rate"].index.max() < cutoff
        assert real_ctx["c1_base"].index.max() < cutoff


def test_build_alpha_dataset_prompt_json_and_ast_validity():
    examples, scored_pool = TA.build_alpha_dataset(
        n_pool=36,
        n_examples=24,
        seed=13,
        dry_run_synth=True,
    )
    assert len(scored_pool) >= 24
    assert len(examples) == 24

    # Scored pool must be sorted descending by composite score
    scores = [r["score"] for r in scored_pool]
    assert scores == sorted(scores, reverse=True)

    # Check every SFT example's prompt schema, JSON formatting, and target AST/causality validity
    prompt_types = set()
    validated_cache: set[str] = set()
    for ex in examples:
        prompt_types.add(ex["prompt_type"])
        assert ex["system"] == TA.SYSTEM_PROMPT
        assert ex["discovery_window"] == ["2020-01-01", "2025-01-01"]
        assert "Allowed feature names ONLY:" in ex["prompt"]
        assert "Allowed functions ONLY:" in ex["prompt"]
        assert "Return ONLY a JSON array" in ex["prompt"]

        # Response must be strict JSON array of strings AND parseable by auto_discover.parse_llm_expressions
        parsed_json = json.loads(ex["response"])
        assert isinstance(parsed_json, list)
        assert len(parsed_json) == ex["n_candidates"]
        assert len(set(parsed_json)) == len(parsed_json), "Target expressions must be distinct"
        assert AD.parse_llm_expressions(ex["response"]) == parsed_json

        for expr in parsed_json:
            assert isinstance(expr, str) and len(expr) < 160
            if expr not in validated_cache:
                AD.SafeAlphaEvaluator.validate(expr)
                ok, msg = AD.check_causality_sandbox(expr, role="overlay")
                assert ok, f"Causality sandbox failed for {expr}: {msg}"
                validated_cache.add(expr)

    # Should include both rejection_feedback and regime-targeted examples
    assert "rejection_feedback" in prompt_types
    assert any(pt.startswith("regime_") for pt in prompt_types)
