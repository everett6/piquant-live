"""Unit tests for PREREG57 synthesis (zero pyarrow, strict pre-2024 causality & invariants)."""
from __future__ import annotations

import subprocess
import sys

import run_round57 as R57


def test_round57_no_pyarrow_imported():
    code = (
        "import sys; sys.modules['pyarrow'] = None; "
        "sys.path.insert(0, 'research/quant_lab'); "
        "import run_round57 as R57; "
        "assert hasattr(R57, 'build_q57_series')"
    )
    res = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True)
    assert res.returncode == 0, res.stderr


def test_round57_builds_and_preserves_pre2024_cutoff():
    q57 = R57.build_q57_series()
    assert q57["gate_diag"]["calibration_cutoff"] == "2024-01-01"
    assert q57["gate_diag"]["n_cal_total"] > 500
    assert 0.2 <= q57["mean_ada_crypto_weight"] <= 0.8
