"""Unit tests for mft_lft_fund.py and OpenRouter alpha discovery in auto_discover.py."""
from __future__ import annotations

import json
from unittest.mock import patch

import auto_discover as AD
import mft_lft_fund as MLF


def test_openrouter_missing_key_graceful(monkeypatch):
    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)
    exprs, note = AD.generate_openrouter_candidates(n_candidates=3, api_key="")
    assert exprs == []
    assert note == "OPENROUTER_API_KEY not set"


def test_openrouter_parses_mocked_response(monkeypatch):
    class _FakeResp:
        def __enter__(self):
            return self

        def __exit__(self, *args):
            return False

        def read(self):
            return json.dumps({
                "choices": [{
                    "message": {
                        "content": "tanh(ema(btc_sma50_dist, 10) * 3.0)\nema(btc_signed_flow5, 5) - 0.5 * abs(pair_spread_z20)"
                    }
                }]
            }).encode("utf-8")

    with patch("urllib.request.urlopen", return_value=_FakeResp()):
        exprs, note = AD.generate_openrouter_candidates(n_candidates=4, api_key="sk-or-test")
    assert note is None
    assert len(exprs) == 2


def test_mft_lft_fund_backtest_runs():
    res = MLF.run_fund_backtest()
    assert res["fund_1x_spot"]["sharpe"] > 1.8
    assert res["fund_1p8x_borrow_adj"]["cagr_pct"] > 20.0
