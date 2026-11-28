"""Unit tests for shadow paper OOS evaluator (`shadow_paper_eval.py`)."""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd
import pytest

import multimodal_features as MF
import shadow_paper_eval as SPE


def _write_daily_dir(
    daily_dir,
    dates: pd.DatetimeIndex,
    btc_closes: np.ndarray,
    eth_closes: np.ndarray,
) -> str:
    d = daily_dir / "daily_csv"
    d.mkdir(parents=True, exist_ok=True)
    for name, closes in (("BTC-USD_1D.csv", btc_closes), ("ETH-USD_1D.csv", eth_closes)):
        df = pd.DataFrame(
            {
                "o": closes * 0.995,
                "h": closes * 1.01,
                "l": closes * 0.99,
                "c": closes,
                "v": np.full(len(closes), 1000.0),
            },
            index=dates,
        )
        df.index.name = "t"
        df.to_csv(str(d / name))

    mm = pd.DataFrame(0.0, index=dates, columns=list(MF.MULTIMODAL_COLS))
    mm.to_csv(str(d / "multimodal_1D.csv"))
    return str(d)


def test_empty_shadow_queue_writes_valid_atomic_state(tmp_path):
    shadow_p = str(tmp_path / "shadow_candidates.json")
    out_p = str(tmp_path / "shadow_paper_state.json")
    # Missing shadow file -> count=0
    state = SPE.evaluate_shadow_candidates(shadow_path=shadow_p, out_path=out_p, dry_run_synth=True)
    assert state["count"] == 0
    assert state["candidates"] == []
    assert os.path.exists(out_p)
    assert not os.path.exists(out_p + ".tmp")

    # Empty JSON list -> count=0
    with open(shadow_p, "w", encoding="utf-8") as fh:
        json.dump([], fh)
    state2 = SPE.evaluate_shadow_candidates(shadow_path=shadow_p, out_path=out_p, dry_run_synth=True)
    assert state2["count"] == 0
    assert state2["candidates"] == []


def test_zero_hindsight_date_cutoff_excludes_admission_day_and_prior(tmp_path):
    # 120 days of steady uptrend up to 2026-11-10 (admission day), then flat or crash after
    dates = pd.date_range("2026-07-15", "2026-11-15", freq="1D", tz="UTC")
    n = len(dates)
    entry_idx = int(dates.get_loc(pd.Timestamp("2026-11-10T00:00:00Z")))

    btc_c = 50000.0 * np.exp(0.004 * np.arange(n))
    eth_c = 2000.0 * np.exp(0.004 * np.arange(n))
    # Huge spike ON admission day 2026-11-10 -> must NOT be counted in OOS return!
    # Keep post-admission closes constant from 2026-11-10 onward -> ~0% gross return after admission
    btc_c[entry_idx:] = btc_c[entry_idx - 1] * 1.50
    eth_c[entry_idx:] = eth_c[entry_idx - 1] * 1.50

    daily_dir = _write_daily_dir(tmp_path, dates, btc_c, eth_c)
    shadow_p = str(tmp_path / "shadow_candidates.json")
    out_p = str(tmp_path / "shadow_paper_state.json")

    with open(shadow_p, "w", encoding="utf-8") as fh:
        json.dump(
            [
                {
                    "expr": "ema(btc_sma50_dist, 5) * 2.0",
                    "role": "overlay",
                    "venue": "spot",
                    "source": "symbolic",
                    "status": "SHADOW_PAPER_ONLY",
                    "timestamp_utc": "2026-11-10T09:15:00+00:00",
                },
                {
                    "expr": "ema(btc_sma50_dist, 5) * 2.0",
                    "role": "overlay",
                    "venue": "futures",
                    "source": "symbolic",
                    "status": "REJECTED",
                    "timestamp_utc": "2026-08-01T00:00:00+00:00",
                },
            ],
            fh,
        )

    state = SPE.evaluate_shadow_candidates(
        shadow_path=shadow_p,
        out_path=out_p,
        daily_csv_dir=daily_dir,
        dry_run_synth=False,
    )
    assert state["count"] == 1
    cand = state["candidates"][0]
    # Only 2026-11-11..2026-11-15 = 5 days strictly after 2026-11-10
    assert cand["live_days"] == 5
    assert cand["first_oos_day_utc"] == "2026-11-11"
    assert cand["last_oos_day_utc"] == "2026-11-15"
    # The +50% spike on 2026-11-10 must NOT inflate OOS return
    assert abs(cand["live_return_pct"]) < 1.0
    assert cand["health"] == "WARMUP"

    # Admission day AT or AFTER last available bar -> 0 OOS days
    with open(shadow_p, "w", encoding="utf-8") as fh:
        json.dump(
            [
                {
                    "expr": "ema(btc_sma50_dist, 5) * 2.0",
                    "role": "overlay",
                    "venue": "futures",
                    "status": "SHADOW_PAPER_ONLY",
                    "timestamp_utc": "2026-11-15T01:00:00+00:00",
                }
            ],
            fh,
        )
    state0 = SPE.evaluate_shadow_candidates(
        shadow_path=shadow_p,
        out_path=out_p,
        daily_csv_dir=daily_dir,
        dry_run_synth=False,
    )
    assert state0["candidates"][0]["live_days"] == 0
    assert state0["candidates"][0]["live_return_pct"] == 0.0
    assert state0["candidates"][0]["health"] == "WARMUP"


def test_health_states_warmup_healthy_decaying_killed(tmp_path):
    assert SPE.classify_health(live_days=13, live_max_dd_pct=-5.0, live_sharpe=2.0) == "WARMUP"
    assert SPE.classify_health(live_days=14, live_max_dd_pct=-5.0, live_sharpe=1.5) == "HEALTHY"
    assert SPE.classify_health(live_days=20, live_max_dd_pct=-15.1, live_sharpe=0.5) == "KILLED"
    assert SPE.classify_health(live_days=30, live_max_dd_pct=-5.0, live_sharpe=-0.51) == "KILLED"
    assert SPE.classify_health(live_days=30, live_max_dd_pct=-5.0, live_sharpe=0.29) == "DECAYING"
    assert SPE.classify_health(live_days=30, live_max_dd_pct=-5.0, live_sharpe=0.30) == "HEALTHY"

    # End-to-end KILLED via live_max_dd_pct < -15.0 on real-style daily CSV
    dates = pd.date_range("2026-06-01", "2026-11-20", freq="1D", tz="UTC")
    n = len(dates)
    entry_idx = int(dates.get_loc(pd.Timestamp("2026-10-25T00:00:00Z")))

    # Strong uptrend prior to admission so standalone sigmoid(>0) is ~1.0 long,
    # followed by a >30% crash after admission -> >15% DD -> KILLED!
    btc_c = 50000.0 * np.exp(0.005 * np.arange(n))
    eth_c = 2000.0 * np.exp(0.005 * np.arange(n))
    for k in range(entry_idx + 2, n):
        btc_c[k] = btc_c[entry_idx + 1] * (0.96 ** (k - entry_idx - 1))
        eth_c[k] = eth_c[entry_idx + 1] * (0.96 ** (k - entry_idx - 1))

    daily_dir = _write_daily_dir(tmp_path, dates, btc_c, eth_c)
    shadow_p = str(tmp_path / "shadow_candidates.json")
    out_p = str(tmp_path / "shadow_paper_state.json")

    with open(shadow_p, "w", encoding="utf-8") as fh:
        json.dump(
            [
                {
                    "expr": "btc_sma50_dist * 10.0 + 3.0",
                    "role": "standalone",
                    "venue": "spot",
                    "source": "symbolic",
                    "status": "SHADOW_PAPER_ONLY",
                    "timestamp_utc": "2026-10-25T12:00:00Z",
                },
                {
                    "expr": "btc_sma50_dist * 10.0 + 3.0",
                    "role": "standalone",
                    "venue": "futures",
                    "source": "symbolic",
                    "status": "SHADOW_PAPER_ONLY",
                    "timestamp_utc": "2026-11-15T12:00:00Z",
                },
            ],
            fh,
        )

    state = SPE.evaluate_shadow_candidates(
        shadow_path=shadow_p,
        out_path=out_p,
        daily_csv_dir=daily_dir,
        dry_run_synth=False,
    )
    assert state["count"] == 2
    c_killed, c_warmup = state["candidates"]
    assert c_killed["live_days"] == 26
    assert c_killed["live_max_dd_pct"] < -15.0
    assert c_killed["health"] == "KILLED"
    assert c_warmup["live_days"] == 5
    assert c_warmup["health"] == "WARMUP"


def test_dry_run_synth_evaluates_forward_oos(tmp_path):
    shadow_p = str(tmp_path / "shadow_candidates.json")
    out_p = str(tmp_path / "shadow_paper_state.json")
    with open(shadow_p, "w", encoding="utf-8") as fh:
        json.dump(
            [
                {
                    "expr": "ema(btc_sma50_dist, 10) * 3.0",
                    "role": "overlay",
                    "venue": "spot",
                    "source": "symbolic",
                    "status": "SHADOW_PAPER_ONLY",
                    "timestamp_utc": "2026-11-18T00:00:00+00:00",
                }
            ],
            fh,
        )

    state = SPE.evaluate_shadow_candidates(
        shadow_path=shadow_p,
        out_path=out_p,
        dry_run_synth=True,
    )
    assert state["dry_run_synth"] is True
    assert state["count"] == 1
    c = state["candidates"][0]
    assert c["live_days"] == 45
    assert c["health"] in ("HEALTHY", "DECAYING", "KILLED")
    assert c["venue"] == "spot"
    assert os.path.exists(out_p)


def test_dry_run_synth_writes_cusum_from_pre_admission_reference(tmp_path):
    shadow_p = str(tmp_path / "shadow_candidates.json")
    out_p = str(tmp_path / "shadow_paper_state.json")
    with open(shadow_p, "w", encoding="utf-8") as fh:
        json.dump([{"expr": "ema(btc_sma50_dist, 10) * 3.0", "role": "overlay", "venue": "spot",
                    "source": "symbolic", "status": "SHADOW_PAPER_ONLY",
                    "timestamp_utc": "2026-11-18T00:00:00+00:00"}], fh)
    c = SPE.evaluate_shadow_candidates(shadow_path=shadow_p, out_path=out_p, dry_run_synth=True)["candidates"][0]
    cu = c["cusum"]
    assert cu is not None and cu["h"] > 0 and cu["ref_days"] >= 180 and cu["s"] >= 0
    with open(out_p, encoding="utf-8") as fh:
        assert json.load(fh)["candidates"][0]["cusum"] == cu
