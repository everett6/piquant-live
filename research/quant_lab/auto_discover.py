"""Autonomous Causal Alpha-Discovery & Quarantined Shadow-Queue Daemon.

Supports native C++ `llama.cpp` inference (e.g. Liquid AI `LFM3-1.7B-QAD-Q4_0.gguf`
or fine-tuned `piquant-news-lfm3-v1-Q4_K_M.gguf` via `llama-server` OpenAI API or
direct `llama-cli`), local Ollama, and automatic fallback to a deterministic
symbolic expression generator when local LLMs are unavailable.

Every candidate expression passes 5 strict gates before entering the paper-only
shadow queue (`results/shadow_candidates.json`):
  1. AST safety & complexity gate (zero arbitrary code/IO/imports/attributes).
  2. Synthetic future-shock causality gate (zero lookahead leakage).
  3. Purged & Embargoed 5-fold CV gate (Marcos Lopez de Prado).
  4. Full net-of-fees + stress + spot backtest & Deflated Sharpe Ratio (DSR)
     gate with persistent multiple-testing trial counter (`n_trials`).
  5. Quarantined shadow-only output (`SHADOW_PAPER_ONLY`; never live execution).
"""
from __future__ import annotations

import argparse
import ast
import datetime as dt
import json
import os
import re
import shutil
import subprocess
import time
import urllib.error
import urllib.request
from dataclasses import asdict, dataclass
from typing import Any

import numpy as np
import pandas as pd

import feature_store as FS

HERE = os.path.dirname(os.path.abspath(__file__))
DEFAULT_RESULTS_DIR = os.path.join(HERE, "results")
DEFAULT_STATE_PATH = os.path.join(DEFAULT_RESULTS_DIR, "auto_discover_state.json")
DEFAULT_SHADOW_PATH = os.path.join(DEFAULT_RESULTS_DIR, "shadow_candidates.json")
DEFAULT_LOG_PATH = os.path.join(DEFAULT_RESULTS_DIR, "auto_discover_log.jsonl")

DEFAULT_LLAMA_BIN = os.path.expanduser("~/llama.cpp/build/bin/llama-cli")
DEFAULT_LLAMA_MODEL_CANDIDATES: tuple[str, ...] = (
    os.path.expanduser("~/models/piquant-alpha-lfm3-v2-Q4_K_M.gguf"),
    os.path.expanduser("~/models/piquant-alpha-lfm3-v2/piquant-alpha-lfm3-v2-Q4_K_M.gguf"),
    os.path.expanduser("~/models/piquant-alpha-lfm3-v1-Q4_K_M.gguf"),
    os.path.expanduser("~/models/piquant-alpha-lfm3-v1/piquant-alpha-lfm3-v1-Q4_K_M.gguf"),
    os.path.expanduser("~/models/LFM3-1.7B-QAD-Q4_0.gguf"),
)


def _resolve_default_llama_model(candidates: tuple[str, ...] = DEFAULT_LLAMA_MODEL_CANDIDATES) -> str:
    """Prefer fine-tuned piquant-alpha-lfm3-v1-Q4_K_M.gguf if present, else fall back to base LFM3."""
    for path in candidates:
        expanded = os.path.expanduser(path)
        if os.path.isfile(expanded):
            return expanded
    return os.path.expanduser(candidates[-1])


DEFAULT_LLAMA_MODEL = _resolve_default_llama_model()
DEFAULT_LLAMA_THREADS = 3
DEFAULT_LLAMA_URL = ""

INITIAL_TRIALS = 810
MAX_EXPR_LEN = 300
MAX_AST_NODES = 80

ALLOWED_FEATURES: tuple[str, ...] = (
    "btc_ret1",
    "btc_ret5",
    "btc_ret21",
    "eth_ret5",
    "eth_ret21",
    "btc_sma50_dist",
    "eth_sma50_dist",
    "btc_vol20_ann",
    "eth_vol20_ann",
    "vol_ratio_5_20",
    "btc_vol_z20",
    "btc_signed_flow5",
    "pair_spread_z20",
    "btc_minus_eth_1d",
    "btc_minus_eth_5d",
    "btc_rv_1m_ann",
    "btc_jump_ratio",
    "btc_vpin_1m",
    "eth_vpin_1m",
    "btc_vrp",
    "dvol_z20",
    "btc_fund_z20",
    "fund_spread_be",
    "stable_flow_21d",
    "alt_breadth_20d",
    "qqq_ret21",
    "uup_ret21",
    "micro_tox_score",
    "micro_imbalance_score",
    "news_sent_3d",
    "news_shock_1d",
    "news_vol_7d",
)
ALLOWED_FEATURE_SET = frozenset(ALLOWED_FEATURES)

# Pure causal elementwise / rolling helpers (name -> expected arg count)
FUNC_ARITY: dict[str, int] = {
    "abs": 1,
    "tanh": 1,
    "sign": 1,
    "sigmoid": 1,
    "clip": 3,
    "where": 3,
    "min": 2,
    "max": 2,
    "ema": 2,
    "roll_mean": 2,
}
ALLOWED_FUNC_SET = frozenset(FUNC_ARITY.keys())

# Benchmark hurdles: must beat Q42_dualspeed_60_40_carry without inflating DD
BENCHMARK_GEO_MO_PCT = 2.15
BENCHMARK_SHARPE = 1.363
BENCHMARK_CALMAR = 1.286
BENCHMARK_MAX_DD_PCT = -23.0
BENCHMARK_MEAN_DD_PCT = 10.0
BENCHMARK_DSR = 0.80

# Spot venue benchmark hurdles (Q42_dualspeed_60_40_carry_spot)
SPOT_BENCHMARK_GEO_MO_PCT = 2.15
SPOT_BENCHMARK_SHARPE = 1.483
SPOT_BENCHMARK_CALMAR = 1.460
SPOT_BENCHMARK_MAX_DD_PCT = -20.0
SPOT_BENCHMARK_MEAN_DD_PCT = 9.0
SPOT_BENCHMARK_DSR = 0.80


class UnsafeExpressionError(ValueError):
    """Raised when a candidate expression violates the AST safety allowlist."""


def _sigmoid(x: np.ndarray) -> np.ndarray:
    arr = np.asarray(x, dtype=float)
    return 1.0 / (1.0 + np.exp(-np.clip(arr, -30.0, 30.0)))


def _causal_ema(x: np.ndarray, span: Any) -> np.ndarray:
    s = int(np.clip(round(float(np.asarray(span).ravel()[0])), 2, 120))
    return pd.Series(np.asarray(x, dtype=float)).ewm(span=s, adjust=False).mean().to_numpy(dtype=float)


def _causal_roll_mean(x: np.ndarray, win: Any) -> np.ndarray:
    w = int(np.clip(round(float(np.asarray(win).ravel()[0])), 2, 120))
    return pd.Series(np.asarray(x, dtype=float)).rolling(w, min_periods=1).mean().to_numpy(dtype=float)


class SafeAlphaEvaluator:
    """Strict AST-validated evaluator for causal alpha expressions."""

    ALLOWED_NODES = (
        ast.Expression,
        ast.BinOp,
        ast.UnaryOp,
        ast.Compare,
        ast.Call,
        ast.Name,
        ast.Constant,
        ast.Load,
        ast.Add,
        ast.Sub,
        ast.Mult,
        ast.Div,
        ast.BitAnd,
        ast.BitOr,
        ast.UAdd,
        ast.USub,
        ast.Invert,
        ast.Lt,
        ast.LtE,
        ast.Gt,
        ast.GtE,
    )

    @classmethod
    def validate(cls, expr: str) -> ast.Expression:
        if not isinstance(expr, str):
            raise UnsafeExpressionError("expression must be a string")
        cleaned = expr.strip()
        if not cleaned:
            raise UnsafeExpressionError("expression is empty")
        if len(cleaned) > MAX_EXPR_LEN:
            raise UnsafeExpressionError(f"expression exceeds {MAX_EXPR_LEN} chars")

        try:
            tree = ast.parse(cleaned, mode="eval")
        except SyntaxError as exc:
            raise UnsafeExpressionError(f"syntax error: {exc.msg}") from exc

        nodes = list(ast.walk(tree))
        if len(nodes) > MAX_AST_NODES:
            raise UnsafeExpressionError(f"expression exceeds {MAX_AST_NODES} AST nodes")

        used_features = 0
        for node in nodes:
            if not isinstance(node, cls.ALLOWED_NODES):
                raise UnsafeExpressionError(f"disallowed AST node: {type(node).__name__}")
            if isinstance(node, ast.Constant):
                if not isinstance(node.value, (int, float)) or isinstance(node.value, bool):
                    raise UnsafeExpressionError("only numeric constants are allowed")
                if not np.isfinite(float(node.value)):
                    raise UnsafeExpressionError("non-finite constant")
            elif isinstance(node, ast.Name):
                if node.id in ALLOWED_FEATURE_SET:
                    used_features += 1
                elif node.id not in ALLOWED_FUNC_SET:
                    raise UnsafeExpressionError(f"unknown identifier: {node.id}")
            elif isinstance(node, ast.Call):
                if not isinstance(node.func, ast.Name) or node.func.id not in ALLOWED_FUNC_SET:
                    raise UnsafeExpressionError("only allowlisted function calls are permitted")
                if node.keywords:
                    raise UnsafeExpressionError("keyword arguments are not allowed")
                expected = FUNC_ARITY[node.func.id]
                if len(node.args) != expected:
                    raise UnsafeExpressionError(
                        f"function {node.func.id} expects {expected} args, got {len(node.args)}"
                    )
                if node.func.id in ("ema", "roll_mean"):
                    win_arg = node.args[1]
                    if not isinstance(win_arg, ast.Constant) or not isinstance(win_arg.value, (int, float)):
                        raise UnsafeExpressionError(f"{node.func.id} window must be a numeric constant")
            elif isinstance(node, ast.Compare):
                if len(node.ops) != 1 or len(node.comparators) != 1:
                    raise UnsafeExpressionError("chained comparisons are not allowed")

        if used_features == 0:
            raise UnsafeExpressionError("expression must reference at least one allowed feature")
        return tree

    @classmethod
    def _eval_node(cls, node: ast.AST, env: dict[str, np.ndarray], n: int) -> np.ndarray:
        if isinstance(node, ast.Expression):
            return cls._eval_node(node.body, env, n)
        if isinstance(node, ast.Constant):
            return np.full(n, float(node.value), dtype=float)
        if isinstance(node, ast.Name):
            return env[node.id]
        if isinstance(node, ast.UnaryOp):
            val = cls._eval_node(node.operand, env, n)
            if isinstance(node.op, ast.UAdd):
                return +val
            if isinstance(node.op, ast.USub):
                return -val
            if isinstance(node.op, ast.Invert):
                return (~(val > 0.5)).astype(float)
            raise UnsafeExpressionError(f"unsupported unary op: {type(node.op).__name__}")
        if isinstance(node, ast.BinOp):
            left = cls._eval_node(node.left, env, n)
            right = cls._eval_node(node.right, env, n)
            if isinstance(node.op, ast.Add):
                return left + right
            if isinstance(node.op, ast.Sub):
                return left - right
            if isinstance(node.op, ast.Mult):
                return left * right
            if isinstance(node.op, ast.Div):
                return np.where(np.abs(right) > 1e-12, left / np.where(np.abs(right) > 1e-12, right, 1.0), 0.0)
            if isinstance(node.op, ast.BitAnd):
                return ((left > 0.5) & (right > 0.5)).astype(float)
            if isinstance(node.op, ast.BitOr):
                return ((left > 0.5) | (right > 0.5)).astype(float)
            raise UnsafeExpressionError(f"unsupported binary op: {type(node.op).__name__}")
        if isinstance(node, ast.Compare):
            left = cls._eval_node(node.left, env, n)
            right = cls._eval_node(node.comparators[0], env, n)
            op = node.ops[0]
            if isinstance(op, ast.Lt):
                return (left < right).astype(float)
            if isinstance(op, ast.LtE):
                return (left <= right).astype(float)
            if isinstance(op, ast.Gt):
                return (left > right).astype(float)
            if isinstance(op, ast.GtE):
                return (left >= right).astype(float)
            raise UnsafeExpressionError(f"unsupported compare op: {type(op).__name__}")
        if isinstance(node, ast.Call):
            fname = node.func.id  # type: ignore[union-attr]
            if fname == "ema":
                x = cls._eval_node(node.args[0], env, n)
                span = float(node.args[1].value)  # type: ignore[union-attr]
                return _causal_ema(x, span)
            if fname == "roll_mean":
                x = cls._eval_node(node.args[0], env, n)
                win = float(node.args[1].value)  # type: ignore[union-attr]
                return _causal_roll_mean(x, win)
            args = [cls._eval_node(a, env, n) for a in node.args]
            if fname == "abs":
                return np.abs(args[0])
            if fname == "tanh":
                return np.tanh(np.clip(args[0], -30.0, 30.0))
            if fname == "sign":
                return np.sign(args[0])
            if fname == "sigmoid":
                return _sigmoid(args[0])
            if fname == "clip":
                return np.clip(args[0], args[1], args[2])
            if fname == "where":
                return np.where(args[0] > 0.5, args[1], args[2])
            if fname == "min":
                return np.minimum(args[0], args[1])
            if fname == "max":
                return np.maximum(args[0], args[1])
            raise UnsafeExpressionError(f"unsupported function: {fname}")
        raise UnsafeExpressionError(f"unsupported node: {type(node).__name__}")

    @classmethod
    def evaluate(cls, expr: str, features: pd.DataFrame, role: str = "overlay") -> pd.Series:
        tree = cls.validate(expr)
        n = len(features)
        env: dict[str, np.ndarray] = {}
        for col in ALLOWED_FEATURES:
            if col in features.columns:
                arr = features[col].to_numpy(dtype=float)
                env[col] = np.nan_to_num(arr, nan=0.0, posinf=0.0, neginf=0.0)
            else:
                env[col] = np.zeros(n, dtype=float)

        raw = cls._eval_node(tree, env, n)
        raw = np.nan_to_num(raw, nan=0.0, posinf=0.0, neginf=0.0)

        if role == "overlay":
            # Smooth bounded multiplier in [0.40, 1.25] applied to base trend sleeve
            out = 0.40 + 0.85 * _sigmoid(raw)
        elif role == "standalone":
            # Long-only target exposure in [0.0, 1.0]
            out = np.clip(_sigmoid(raw), 0.0, 1.0)
        else:
            raise ValueError(f"unknown role: {role}")

        return pd.Series(out, index=features.index, dtype=float)


def _make_synthetic_ohlcv(n: int = 320, seed: int = 12345) -> tuple[pd.DataFrame, pd.DataFrame]:
    rng = np.random.default_rng(seed)
    idx = pd.date_range("2022-01-01", periods=n, freq="1D", tz="UTC")

    def _gen(start_price: float, drift: float, vol: float) -> pd.DataFrame:
        rets = rng.normal(drift, vol, size=n)
        close = start_price * np.exp(np.cumsum(rets))
        open_ = close * np.exp(rng.normal(0.0, vol * 0.3, size=n))
        high = np.maximum(open_, close) * (1.0 + np.abs(rng.normal(0.0, vol * 0.5, size=n)))
        low = np.minimum(open_, close) * (1.0 - np.abs(rng.normal(0.0, vol * 0.5, size=n)))
        vol_col = rng.lognormal(mean=10.0, sigma=0.4, size=n)
        return pd.DataFrame({"o": open_, "h": high, "l": low, "c": close, "v": vol_col}, index=idx)

    return _gen(30000.0, 0.0007, 0.025), _gen(2000.0, 0.0005, 0.032)


def check_causality_sandbox(expr: str, role: str = "overlay", shock_bar: int = 160) -> tuple[bool, str]:
    """Gate 2: Future-shock test on synthetic candles. Perturbing t >= shock_bar
    must produce zero change in signal values for t < shock_bar."""
    btc1, eth1 = _make_synthetic_ohlcv(n=320, seed=12345)
    btc2, eth2 = btc1.copy(), eth1.copy()
    btc2.iloc[shock_bar:] = btc2.iloc[shock_bar:] * 3.5
    eth2.iloc[shock_bar:] = eth2.iloc[shock_bar:] * 0.25

    # Generate synthetic multimodal features alongside synthetic BTC/ETH and shock t >= shock_bar
    import multimodal_features as MF
    mm_cols = list(MF.MULTIMODAL_COLS) + list(FS.EXTRA_MULTIMODAL_COLS)
    mm1 = pd.DataFrame(0.0, index=btc1.index, columns=mm_cols)
    # Add some random variation so multimodal features are non-constant and testable
    rng_mm = np.random.default_rng(42)
    for col in mm_cols:
        mm1[col] = rng_mm.normal(0.0, 1.0, size=len(btc1))
    mm2 = mm1.copy()
    mm2.iloc[shock_bar:] = mm2.iloc[shock_bar:] * 4.0

    f1 = FS.build_unified_features(btc1, eth1, multimodal=mm1)
    f2 = FS.build_unified_features(btc2, eth2, multimodal=mm2)
    s1 = SafeAlphaEvaluator.evaluate(expr, f1, role=role)
    s2 = SafeAlphaEvaluator.evaluate(expr, f2, role=role)

    if not np.all(np.isfinite(s1.to_numpy())):
        return False, "non-finite signal values on synthetic candles"
    if not np.allclose(s1.iloc[:shock_bar].to_numpy(), s2.iloc[:shock_bar].to_numpy(), atol=1e-12, rtol=1e-12):
        return False, "LOOKAHEAD DETECTED: future shock changed historical signal"
    if float(s1.iloc[60:].std()) < 1e-5:
        return False, "degenerate constant signal (std < 1e-5)"
    return True, "ok"


def evaluate_purged_cv(
    btc: pd.DataFrame,
    eth: pd.DataFrame,
    signal: pd.Series,
    base_pos_b: pd.Series | None,
    base_pos_e: pd.Series | None,
    role: str = "overlay",
    taker_bps: float = 5.0,
    slip_bps: float = 10.0,
    funding_bps_h: float = 0.0,
    n_splits: int = 5,
    purge_bars: int = 5,
    embargo_bars: int = 2,
) -> dict[str, Any]:
    """Gate 3: Out-of-sample Purged & Embargoed K-Fold CV net of fees/slippage."""
    idx = btc.index
    rb = btc["c"].pct_change().fillna(0.0).to_numpy(dtype=float)
    re = eth["c"].reindex(idx).ffill().pct_change().fillna(0.0).to_numpy(dtype=float)
    sig = signal.reindex(idx).fillna(1.0 if role == "overlay" else 0.0).to_numpy(dtype=float)

    if role == "overlay" and base_pos_b is not None and base_pos_e is not None:
        wb = 0.5 * base_pos_b.reindex(idx).fillna(0.0).to_numpy(dtype=float) * sig
        we = 0.5 * base_pos_e.reindex(idx).fillna(0.0).to_numpy(dtype=float) * sig
    else:
        wb = 0.5 * sig
        we = 0.5 * sig

    # Strict 1-bar execution lag: weight decided at close t earns return at t+1
    held_wb = np.roll(wb, 1)
    held_we = np.roll(we, 1)
    held_wb[0] = 0.0
    held_we[0] = 0.0

    prev_wb = np.roll(held_wb, 1)
    prev_we = np.roll(held_we, 1)
    prev_wb[0] = 0.0
    prev_we[0] = 0.0

    cost_rate = (taker_bps + slip_bps) * 1e-4
    fund_rate = funding_bps_h * 24.0 * 1e-4
    turn = np.abs(held_wb - prev_wb) + np.abs(held_we - prev_we)
    gross = np.abs(held_wb) + np.abs(held_we)
    net_ret = held_wb * rb + held_we * re - turn * cost_rate - gross * fund_rate

    fold_sharpes: list[float] = []
    for _train_idx, test_idx in FS.purged_embargoed_splits(
        len(idx), n_splits=n_splits, purge_bars=purge_bars, embargo_bars=embargo_bars
    ):
        r = net_ret[test_idx]
        std = float(np.std(r, ddof=1)) if len(r) > 1 else 0.0
        sh = float((np.mean(r) / std) * np.sqrt(365.0)) if std > 1e-12 else 0.0
        fold_sharpes.append(round(sh, 4))

    median_sh = float(np.median(fold_sharpes)) if fold_sharpes else 0.0
    pos_folds = int(sum(1 for s in fold_sharpes if s > 0.0))
    mean_turnover = float(np.mean(turn)) if len(turn) > 0 else 0.0
    passed = (median_sh >= 0.50) and (pos_folds >= 3)
    return {
        "fold_sharpes": fold_sharpes,
        "median_fold_sharpe": round(median_sh, 4),
        "positive_folds": pos_folds,
        "mean_daily_turnover": round(mean_turnover, 5),
        "passed": passed,
    }


def probe_venue_latency_ms(url: str = "https://api.exchange.coinbase.com/time", timeout_s: float = 3.0) -> float | None:
    """Measure median HTTP RTT (ms) to Coinbase Exchange so discovery knows its latency regime."""
    samples: list[float] = []
    for _ in range(2):
        req = urllib.request.Request(url, headers={"User-Agent": "PiQuant-AutoDiscover/1.0"}, method="GET")
        t0 = time.perf_counter()
        try:
            with urllib.request.urlopen(req, timeout=timeout_s) as resp:
                resp.read(64)
            samples.append((time.perf_counter() - t0) * 1000.0)
        except (urllib.error.URLError, TimeoutError, OSError):
            continue
    if not samples:
        return None
    return round(float(np.median(samples)), 2)


def build_latency_profile(latency_ms: float | None, venue: str = "spot") -> dict[str, Any]:
    """Translate measured venue RTT into competitive horizon & turnover constraints."""
    bench_name = "Q42_dualspeed_60_40_carry_spot" if venue == "spot" else "Q42_dualspeed_60_40_carry"
    if latency_ms is None or latency_ms > 25.0:
        return {
            "measured_rtt_ms": latency_ms,
            "regime": "high_rtt_daily_swing",
            "compete_against": f"{bench_name} (daily/6h patient post-only Spot+Carry; NEVER sub-second HFT)",
            "max_daily_turnover": 0.35,
            "min_hold_days": 3.0,
        }
    return {
        "measured_rtt_ms": latency_ms,
        "regime": "low_rtt_intraday_or_daily",
        "compete_against": f"{bench_name} + 6h/hourly post-only maker",
        "max_daily_turnover": 0.65,
        "min_hold_days": 1.5,
    }


def _sanitize_expr(expr: str) -> str:
    """Strip trailing feedback annotations (' -> ...', '(reason: ...)') and surrounding quotes/backticks."""
    if not isinstance(expr, str):
        return ""
    s = expr.strip().strip("`\"'")
    if s.startswith("expr="):
        s = s[len("expr="):].strip().strip("`\"'")
    s = re.sub(r"\s*->.*$", "", s)
    s = re.sub(r"\s*\(reason:.*$", "", s)
    return s.strip().strip("`\"'").strip()


def parse_llm_expressions(text: str) -> list[str]:
    """Extract candidate expression strings from LLM JSON or line-based output.

    Uses `json.JSONDecoder.raw_decode` scanning for `[` so trailing CLI banners
    or stats like `[ Prompt: 15.6 t/s | Generation: 3.2 t/s ]` never break JSON
    array extraction.
    """
    if not text:
        return []
    candidates: list[str] = []
    cleaned = text.strip()

    decoder = json.JSONDecoder()
    pos = 0
    while True:
        idx = cleaned.find("[", pos)
        if idx == -1:
            break
        try:
            parsed, end = decoder.raw_decode(cleaned, idx)
            if isinstance(parsed, list):
                extracted: list[str] = []
                for item in parsed:
                    if isinstance(item, str) and item.strip():
                        extracted.append(item.strip())
                    elif isinstance(item, dict) and isinstance(item.get("expr"), str):
                        extracted.append(item["expr"].strip())
                if extracted:
                    candidates.extend(extracted)
                    break
            pos = max(idx + 1, end)
        except json.JSONDecodeError:
            # Recover complete quoted elements if a JSON array was truncated at max_tokens
            if cleaned[idx : idx + 2] in ('["', '[\n', '[ '):
                recovered = [m.strip() for m in re.findall(r'"([^"\\]{6,280})"', cleaned[idx:]) if m.strip()]
                if recovered:
                    candidates.extend(recovered)
                    break
            pos = idx + 1

    if not candidates:
        skip_prefixes = (
            "#",
            "```",
            "Loading model",
            "build ",
            "model ",
            "ftype ",
            "modalities ",
            "using custom",
            "available commands",
            "/exit",
            "/regen",
            "/clear",
            "/read",
            "/glob",
            "Exiting",
            "[ Prompt:",
            "[Start thinking]",
            "> ",
            "▄",
            "█",
            "▀",
        )
        for line in cleaned.splitlines():
            s = line.strip()
            if not s or any(s.startswith(pfx) for pfx in skip_prefixes):
                continue
            s = re.sub(r"^[\-\*\d\.\)\s]+", "", s).strip()
            s = s.strip("`\"',")
            if s:
                candidates.append(s)

    seen: set[str] = set()
    out: list[str] = []
    for raw_expr in candidates:
        expr = _sanitize_expr(raw_expr)
        if expr and expr not in seen:
            seen.add(expr)
            out.append(expr)
    return out


def _build_alpha_prompt(
    n_candidates: int,
    recent_feedback: list[str] | None = None,
    latency_profile: dict[str, Any] | None = None,
) -> str:
    fb_block = ""
    if recent_feedback:
        fb_block = "\nRecent rejected attempts (avoid repeating these failure modes):\n- " + "\n- ".join(
            recent_feedback[-5:]
        )

    lat_block = ""
    if latency_profile:
        rtt = latency_profile.get("measured_rtt_ms")
        rtt_txt = f"{rtt:.1f}ms" if isinstance(rtt, (int, float)) else "home-broadband (>25ms)"
        lat_block = (
            f"\nExecution latency context: measured venue RTT={rtt_txt}; compete strictly against "
            f"{latency_profile.get('compete_against')} with daily turnover <= {latency_profile.get('max_daily_turnover')} "
            f"(avg hold >= {latency_profile.get('min_hold_days')} days)."
        )

    return (
        f"Generate exactly {n_candidates} distinct quantitative crypto daily alpha overlay expressions "
        f"as a JSON array of strings.\n"
        f"Allowed feature names ONLY: {', '.join(ALLOWED_FEATURES)}.\n"
        f"Allowed functions ONLY: abs(x), tanh(x), sign(x), sigmoid(x), clip(x,lo,hi), "
        f"where(cond,a,b), min(a,b), max(a,b), ema(x,span), roll_mean(x,win).\n"
        f"Allowed operators: + - * / < <= > >= & | ~.\n"
        f"Example valid expressions:\n"
        f"- ema(btc_sma50_dist, 10) * 3.0 - max(vol_ratio_5_20 - 1.2, 0.0) * 1.5\n"
        f"- tanh(ema(btc_ret21, 5) * 4.0) - clip(btc_vol20_ann - 0.75, 0.0, 1.0) * 2.0\n"
        f"Goal: low-turnover, fee-efficient daily signal that scales risk up in calm uptrends and down "
        f"in vol spikes or choppy regimes. Keep expressions concise (<160 chars).\n"
        f"At least half of the returned expressions should incorporate orthogonal multi-modal features "
        f"(btc_vrp, dvol_z20, btc_fund_z20, fund_spread_be, stable_flow_21d, alt_breadth_20d, "
        f"btc_rv_1m_ann, btc_jump_ratio, btc_vpin_1m, qqq_ret21, uup_ret21) alongside trend/spread features."
        f"{lat_block}{fb_block}\n"
        f"Return ONLY a JSON array of {n_candidates} expression strings, nothing else."
    )


def generate_llamacpp_candidates(
    n_candidates: int,
    llama_bin: str = DEFAULT_LLAMA_BIN,
    llama_model: str = DEFAULT_LLAMA_MODEL,
    llama_threads: int = DEFAULT_LLAMA_THREADS,
    llama_url: str = DEFAULT_LLAMA_URL,
    recent_feedback: list[str] | None = None,
    latency_profile: dict[str, Any] | None = None,
    timeout_s: float = 180.0,
    max_tokens: int = 512,
    temperature: float = 0.2,
) -> tuple[list[str], str | None, str]:
    """Generate causal alpha expressions via native C++ `llama.cpp`.

    Tries OpenAI-compatible `llama-server` (`/v1/chat/completions`) when
    `llama_url` is provided, otherwise invokes `llama-cli` directly with
    Pi 5-safe thread/memory flags (`-t 3`, `--no-repack`, `--reasoning off`).

    Returns `(expressions, error_note, source_tag)`.
    """
    prompt = _build_alpha_prompt(n_candidates, recent_feedback=recent_feedback, latency_profile=latency_profile)
    sys_prompt = (
        "You are a quantitative crypto researcher. Output ONLY a valid JSON array "
        "of expression strings using ONLY the allowed features and functions."
    )

    server_err: str | None = None
    if llama_url:
        payload = json.dumps({
            "messages": [
                {"role": "system", "content": sys_prompt},
                {"role": "user", "content": prompt},
            ],
            "temperature": temperature,
            "max_tokens": max_tokens,
        }).encode("utf-8")
        url = llama_url.rstrip("/") + "/v1/chat/completions"
        req = urllib.request.Request(url, data=payload, headers={"Content-Type": "application/json"}, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=timeout_s) as resp:
                body = json.loads(resp.read().decode("utf-8"))
            choices = body.get("choices") or []
            raw_text = ""
            if choices and isinstance(choices[0], dict):
                msg = choices[0].get("message") or {}
                raw_text = str(msg.get("content") or "")
            exprs = parse_llm_expressions(raw_text)
            if exprs:
                return exprs[:n_candidates], None, "llamacpp:server"
            server_err = "llama-server returned no parseable expressions"
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError, OSError) as exc:
            server_err = f"llama-server unavailable ({exc})"

    # Direct native C++ `llama-cli` invocation
    bin_path = os.path.expanduser(llama_bin) if llama_bin else ""
    if bin_path and not os.path.isfile(bin_path):
        found = shutil.which(bin_path)
        bin_path = found or ""
    model_path = os.path.expanduser(llama_model) if llama_model else ""

    if not bin_path or not os.access(bin_path, os.X_OK):
        note = f"llama-cli unavailable ({llama_bin})"
        if server_err:
            note = f"{server_err}; {note}"
        return [], note, "llamacpp"
    if not model_path or not os.path.isfile(model_path):
        note = f"llama GGUF model not found ({llama_model})"
        if server_err:
            note = f"{server_err}; {note}"
        return [], note, "llamacpp"

    threads = max(1, int(llama_threads))
    n_predict = max(64, int(max_tokens), int(n_candidates) * 72)
    cmd = [
        bin_path,
        "-m", model_path,
        "-t", str(threads),
        "-c", "1536",
        "-n", str(n_predict),
        "--temp", str(temperature),
        "--top-k", "50",
        "--repeat-penalty", "1.1",
        "--reasoning", "off",
        "--no-repack",
        "-st",
        "--no-display-prompt",
        "--simple-io",
        "-sys", sys_prompt,
        "-p", prompt,
    ]
    model_tag = f"llamacpp:{os.path.basename(model_path)}"
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout_s, check=False)
    except (subprocess.TimeoutExpired, OSError) as exc:
        return [], f"llama-cli execution failed ({exc})", model_tag

    if proc.returncode != 0:
        err_tail = (proc.stderr or proc.stdout or "").strip().splitlines()
        tail_msg = err_tail[-1] if err_tail else f"exit={proc.returncode}"
        return [], f"llama-cli exited {proc.returncode}: {tail_msg}", model_tag

    exprs = parse_llm_expressions(proc.stdout)
    if not exprs:
        return [], "llama-cli returned no parseable expressions", model_tag
    return exprs[:n_candidates], None, model_tag


def generate_ollama_candidates(
    n_candidates: int,
    ollama_url: str = "http://127.0.0.1:11434",
    model: str = "lfm2.5",
    recent_feedback: list[str] | None = None,
    latency_profile: dict[str, Any] | None = None,
    timeout_s: float = 25.0,
) -> tuple[list[str], str | None]:
    """Query local Ollama `/api/generate` for novel causal alpha expressions."""
    prompt = _build_alpha_prompt(n_candidates, recent_feedback=recent_feedback, latency_profile=latency_profile)

    payload = json.dumps({
        "model": model,
        "prompt": prompt,
        "stream": False,
        "options": {"temperature": 0.4},
    }).encode("utf-8")

    url = ollama_url.rstrip("/") + "/api/generate"
    req = urllib.request.Request(url, data=payload, headers={"Content-Type": "application/json"}, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=timeout_s) as resp:
            body = json.loads(resp.read().decode("utf-8"))
        raw_text = body.get("response", "")
        exprs = parse_llm_expressions(raw_text)
        return exprs[:n_candidates], None
    except (urllib.error.URLError, TimeoutError, json.JSONDecodeError, OSError) as exc:
        return [], f"ollama unavailable ({exc})"


DEFAULT_OPENROUTER_URL = "https://openrouter.ai/api/v1/chat/completions"
DEFAULT_OPENROUTER_MODEL = "openrouter/free"
DEFAULT_OPENROUTER_ENV_PATH = os.path.expanduser("~/.config/piquant/openrouter.env")


def _load_openrouter_key(api_key: str | None = None, env_path: str | None = None) -> str:
    """Resolve OpenRouter API key from explicit arg, env var, or chmod-600 openrouter.env."""
    if api_key and api_key.strip():
        return api_key.strip()
    env_val = os.environ.get("OPENROUTER_API_KEY", "").strip()
    if env_val:
        return env_val
    path = os.path.expanduser(env_path or DEFAULT_OPENROUTER_ENV_PATH)
    if os.path.exists(path):
        try:
            with open(path, "r", encoding="utf-8") as fh:
                for raw_line in fh:
                    line = raw_line.strip()
                    if not line or line.startswith("#") or "=" not in line:
                        continue
                    k, v = line.split("=", 1)
                    if k.strip() == "OPENROUTER_API_KEY":
                        return v.strip().strip('"').strip("'")
        except OSError:
            return ""
    return ""


def generate_openrouter_candidates(
    n_candidates: int,
    api_key: str | None = None,
    model: str = DEFAULT_OPENROUTER_MODEL,
    openrouter_url: str = DEFAULT_OPENROUTER_URL,
    recent_feedback: list[str] | None = None,
    latency_profile: dict[str, Any] | None = None,
    timeout_s: float = 35.0,
) -> tuple[list[str], str | None]:
    """Query OpenRouter chat-completions (supports `:free` models) for MFT/LFT alpha expressions."""
    key = _load_openrouter_key(api_key=api_key)
    if not key:
        return [], "OPENROUTER_API_KEY not set"

    prompt = _build_alpha_prompt(n_candidates, recent_feedback=recent_feedback, latency_profile=latency_profile)
    payload = json.dumps({
        "model": model,
        "messages": [
            {"role": "system", "content": "You are a quantitative researcher outputting only causal alpha formulas."},
            {"role": "user", "content": prompt},
        ],
        "temperature": 0.4,
    }).encode("utf-8")

    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {key}",
        "HTTP-Referer": "https://github.com/everett6/piquant-live",
        "X-Title": "PiQuant Alpha Discovery",
    }
    req = urllib.request.Request(openrouter_url, data=payload, headers=headers, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=timeout_s) as resp:
            body = json.loads(resp.read().decode("utf-8"))
        choices = body.get("choices") or []
        raw_text = ""
        if choices and isinstance(choices[0], dict):
            msg = choices[0].get("message") or {}
            raw_text = str(msg.get("content") or "")
        exprs = parse_llm_expressions(raw_text)
        return exprs[:n_candidates], None
    except (urllib.error.URLError, TimeoutError, json.JSONDecodeError, OSError, ValueError) as exc:
        return [], f"openrouter unavailable ({exc})"


def generate_symbolic_candidates(n_candidates: int, seed: int = 42) -> list[str]:
    """Deterministic symbolic alpha generator over ALLOWED_FEATURES & ALLOWED_FUNCS."""
    rng = np.random.default_rng(seed)

    curated_templates = [
        "ema(btc_sma50_dist, 10) * 3.0 - max(vol_ratio_5_20 - 1.2, 0.0) * 1.5",
        "tanh(ema(btc_ret21, 5) * 4.0) - clip(btc_vol20_ann - 0.75, 0.0, 1.0) * 2.0",
        "where(vol_ratio_5_20 < 1.35, ema(btc_sma50_dist + eth_sma50_dist, 7) * 2.5, -1.0)",
        "ema(btc_signed_flow5, 10) * 2.0 + tanh(btc_sma50_dist * 4.0) - 0.8 * max(eth_vol20_ann - 0.85, 0.0)",
        "clip(ema(btc_ret21 + eth_ret21, 10) * 2.5, -2.0, 2.0) - 0.6 * abs(pair_spread_z20)",
        "roll_mean(btc_sma50_dist, 14) * 3.5 - 1.2 * max(vol_ratio_5_20 - 1.15, 0.0)",
        "tanh((btc_sma50_dist + 0.5 * eth_sma50_dist) * 3.0) + 0.4 * ema(btc_vol_z20, 10)",
        "where(btc_vol20_ann < 0.80, 0.8 + tanh(btc_ret21 * 3.0), -0.6 * vol_ratio_5_20)",
        "ema(btc_ret5, 10) * 4.0 + ema(btc_sma50_dist, 20) * 2.0 - max(btc_vol20_ann - 0.70, 0.0) * 1.5",
        "tanh(ema(btc_minus_eth_5d, 10) * 3.0) + 1.5 * tanh(btc_sma50_dist * 3.0)",
        "clip(roll_mean(btc_signed_flow5, 15) * 3.0 + ema(btc_ret21, 12) * 3.0, -2.0, 2.0)",
        "where(btc_sma50_dist > 0.0, 1.0 - max(vol_ratio_5_20 - 1.25, 0.0), -0.5 - abs(btc_ret5) * 3.0)",
    ]

    out: list[str] = []
    seen: set[str] = set()
    perm = rng.permutation(len(curated_templates))
    for i in perm:
        expr = curated_templates[int(i)]
        if expr not in seen:
            seen.add(expr)
            out.append(expr)
        if len(out) >= n_candidates:
            return out

    # Synthesize additional random low-turnover combinations if n_candidates > len(curated_templates)
    trend_feats = ("btc_sma50_dist", "eth_sma50_dist", "btc_ret21", "eth_ret21", "btc_signed_flow5")
    risk_feats = ("vol_ratio_5_20", "btc_vol20_ann", "eth_vol20_ann", "pair_spread_z20")
    attempts = 0
    while len(out) < n_candidates and attempts < n_candidates * 10:
        attempts += 1
        tf = str(rng.choice(trend_feats))
        rf = str(rng.choice(risk_feats))
        span = int(rng.choice([5, 10, 15, 21, 30]))
        w1 = round(float(rng.uniform(1.0, 4.0)), 2)
        w2 = round(float(rng.uniform(0.5, 2.0)), 2)
        thr = round(float(rng.uniform(0.6, 1.4)), 2)
        expr = f"tanh(ema({tf}, {span}) * {w1}) - {w2} * max(abs({rf}) - {thr}, 0.0)"
        if expr not in seen:
            seen.add(expr)
            out.append(expr)
    return out


def _load_state(state_path: str) -> dict[str, Any]:
    if os.path.exists(state_path):
        with open(state_path, "r", encoding="utf-8") as fh:
            data = json.load(fh)
        if isinstance(data, dict) and isinstance(data.get("n_trials"), int):
            return data
    return {"n_trials": INITIAL_TRIALS, "runs": 0, "accepted": 0, "rejected": 0}


def _save_json(path: str, payload: Any) -> None:
    os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(payload, fh, indent=2)
    os.replace(tmp, path)


def _append_jsonl(path: str, record: dict[str, Any]) -> None:
    os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)
    with open(path, "a", encoding="utf-8") as fh:
        fh.write(json.dumps(record) + "\n")


def _load_recent_feedback(log_path: str, limit: int = 5) -> list[str]:
    if limit <= 0 or not os.path.exists(log_path):
        return []
    lines: list[str] = []
    with open(log_path, "r", encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                rec = json.loads(line)
                if rec.get("accepted"):
                    continue
                reason = str(rec.get("reject_reason") or "failed").strip()
                if reason.startswith("AST_GATE"):
                    continue
                expr_clean = _sanitize_expr(str(rec.get("expr", "")))
                if not expr_clean:
                    continue
                short_reason = reason.replace("->", ":").strip()
                if len(short_reason) > 160:
                    short_reason = short_reason[:157].rstrip() + "..."
                lines.append(f'expr="{expr_clean}" (reason: {short_reason})')
            except json.JSONDecodeError:
                continue
    return lines[-limit:]


@dataclass
class CandidateResult:
    expr: str
    source: str
    role: str
    n_trials_at_eval: int
    accepted: bool
    reject_reason: str
    venue: str = "futures"
    cv_summary: dict[str, Any] | None = None
    base_metrics: dict[str, Any] | None = None
    stress_metrics: dict[str, Any] | None = None
    spot_metrics: dict[str, Any] | None = None
    timestamp_utc: str = ""


def _evaluate_synthetic_full(
    btc: pd.DataFrame,
    eth: pd.DataFrame,
    signal: pd.Series,
    role: str,
    n_trials: int,
) -> tuple[dict[str, Any], dict[str, Any], dict[str, Any]]:
    """Lightweight synthetic full-gate evaluator for `--dry-run-synth` and unit tests."""
    import lab

    idx = btc.index
    rb = btc["c"].pct_change().fillna(0.0).to_numpy(dtype=float)
    re = eth["c"].reindex(idx).ffill().pct_change().fillna(0.0).to_numpy(dtype=float)
    sig = signal.reindex(idx).fillna(0.0).to_numpy(dtype=float)
    w = np.clip(0.5 * sig, 0.0, 1.0)
    held = np.roll(w, 1)
    held[0] = 0.0
    prev = np.roll(held, 1)
    prev[0] = 0.0
    turn = 2.0 * np.abs(held - prev)

    def _sim(taker_bps: float, slip_bps: float) -> dict[str, Any]:
        cost = (taker_bps + slip_bps) * 1e-4
        r = pd.Series(held * (rb + re) - turn * cost, index=idx)
        eq = (1.0 + r).cumprod()
        dd = eq / eq.cummax() - 1.0
        n_mo = max(1.0, len(r) / 30.4375)
        geo_mo = float(((eq.iloc[-1] ** (1.0 / n_mo)) - 1.0) * 100.0)
        std = float(r.std(ddof=1))
        sharpe = float((r.mean() / std) * np.sqrt(365.0)) if std > 1e-12 else 0.0
        max_dd = float(dd.min() * 100.0)
        mean_dd = float((-dd).mean() * 100.0)
        ann = ((1.0 + geo_mo / 100.0) ** 12 - 1.0) * 100.0
        calmar = float(ann / abs(max_dd)) if abs(max_dd) > 1e-9 else 0.0
        dsr = float(lab.deflated_sharpe(r, n_trials))
        return {
            "geo_monthly_pct": round(geo_mo, 4),
            "annualized_pct": round(ann, 2),
            "sharpe": round(sharpe, 4),
            "calmar": round(calmar, 4),
            "max_dd_pct": round(max_dd, 4),
            "mean_dd_pct": round(mean_dd, 4),
            "A_geo_monthly_pct": round(geo_mo, 4),
            "deflated_sharpe": round(dsr, 4),
        }

    return _sim(5.0, 10.0), _sim(10.0, 20.0), _sim(2.0, 5.0)


def _check_full_gates(
    base: dict[str, Any],
    stress: dict[str, Any],
    spot: dict[str, Any] | None = None,
    venue: str = "futures",
    cv_summary: dict[str, Any] | None = None,
    latency_profile: dict[str, Any] | None = None,
) -> tuple[bool, str]:
    if venue == "futures":
        target = base
        min_dsr = BENCHMARK_DSR
        min_max_dd = BENCHMARK_MAX_DD_PCT
        max_mean_dd = BENCHMARK_MEAN_DD_PCT
        min_geo_mo = BENCHMARK_GEO_MO_PCT
        min_sharpe = BENCHMARK_SHARPE
        min_calmar = BENCHMARK_CALMAR
    elif venue == "spot":
        target = spot if spot is not None else base
        min_dsr = SPOT_BENCHMARK_DSR
        min_max_dd = SPOT_BENCHMARK_MAX_DD_PCT
        max_mean_dd = SPOT_BENCHMARK_MEAN_DD_PCT
        min_geo_mo = SPOT_BENCHMARK_GEO_MO_PCT
        min_sharpe = SPOT_BENCHMARK_SHARPE
        min_calmar = SPOT_BENCHMARK_CALMAR
    else:
        raise ValueError(f"unknown venue: {venue}")

    reasons: list[str] = []
    if float(target.get("deflated_sharpe", 0.0)) < min_dsr:
        reasons.append(f"DSR {target.get('deflated_sharpe')} < {min_dsr}")
    if float(target.get("max_dd_pct", -100.0)) < min_max_dd:
        reasons.append(f"MaxDD {target.get('max_dd_pct')}% < {min_max_dd}%")
    if float(target.get("mean_dd_pct", 100.0)) > max_mean_dd:
        reasons.append(f"MeanDD {target.get('mean_dd_pct')}% > {max_mean_dd}%")
    if float(target.get("geo_monthly_pct", -100.0)) < min_geo_mo:
        reasons.append(f"Geo/mo {target.get('geo_monthly_pct')}% < {min_geo_mo}%")
    if float(target.get("sharpe", 0.0)) <= min_sharpe:
        reasons.append(f"Sharpe {target.get('sharpe')} <= {min_sharpe}")
    if float(target.get("calmar", 0.0)) <= min_calmar:
        reasons.append(f"Calmar {target.get('calmar')} <= {min_calmar}")
    a_rec = target.get("A_geo_monthly_pct", target.get("A_2025_26"))
    if a_rec is None or float(a_rec) <= 0.0:
        reasons.append(f"A_geo_monthly_pct {a_rec} <= 0")
    if float(stress.get("geo_monthly_pct", -100.0)) <= 0.0:
        reasons.append(f"stress geo/mo {stress.get('geo_monthly_pct')} <= 0")
    if cv_summary and latency_profile:
        turn = float(cv_summary.get("mean_daily_turnover", 0.0))
        max_turn = float(latency_profile.get("max_daily_turnover", 0.35))
        if turn > max_turn:
            reasons.append(f"turnover {turn:.4f} > latency cap {max_turn:.2f}")
    if reasons:
        return False, "; ".join(reasons)
    return True, "PASS"


def _load_daily_candles(product: str, lab_mod: Any, daily_csv_dir: str | None = None) -> pd.DataFrame:
    """Load 1D OHLCV candles from a lightweight daily CSV cache (Pi 5 friendly,
    no pyarrow required) if available, otherwise fall back to `lab.load_candles`."""
    candidates: list[str] = []
    if daily_csv_dir:
        candidates.append(os.path.join(os.path.expanduser(daily_csv_dir), f"{product}_1D.csv"))
    candidates.append(os.path.expanduser(f"~/piquant-collect/daily_csv/{product}_1D.csv"))
    for path in candidates:
        if os.path.exists(path):
            df = pd.read_csv(path, index_col=0, parse_dates=True)
            if df.index.tz is None:
                df.index = df.index.tz_localize("UTC")
            else:
                df.index = df.index.tz_convert("UTC")
            return df[["o", "h", "l", "c", "v"]].astype("float64").dropna()
    return lab_mod.resample(lab_mod.load_candles(product=product, since=None), "1D")


def _load_real_market_context(daily_csv_dir: str | None = None) -> dict[str, Any]:
    """Load real 2020-2026 BTC/ETH candles, funding, DTB3, and Q42 base sleeves."""
    import lab
    import strategies5 as S5
    import strategies19 as S19
    import strategies35 as S35
    import strategies36 as S36
    import strategies40 as S40
    import strategies42 as S42
    from run_round7 import cut
    from run_round19 import END, START, load_funding
    from run_round33 import DTB3, W0, W1
    from run_round36 import extended_metrics

    full = {c: _load_daily_candles(f"{c}-USD", lab, daily_csv_dir=daily_csv_dir) for c in ("BTC", "ETH")}
    d = {c: full[c][(full[c].index >= START) & (full[c].index < END)] for c in ("BTC", "ETH")}
    btc_sub, eth_sub = d["BTC"], d["ETH"]
    eth_aligned = eth_sub.reindex(btc_sub.index).ffill()

    # Compute base Q42 dual-speed (50% QD + 50% R3) on full history before slicing
    qd_full = {c: pd.Series(S35.coherence_positions(full[c], lev=1.0), index=full[c].index) for c in ("BTC", "ETH")}
    r3_full = {c: pd.Series(S40.r3_fastexit_qd_positions(full[c], lev=1.0), index=full[c].index) for c in ("BTC", "ETH")}
    dual_1x = {
        c: (0.5 * (qd_full[c] + r3_full[c])).reindex(btc_sub.index).fillna(0.0)
        for c in ("BTC", "ETH")
    }

    # Auto-load multimodal daily CSV and pass into build_unified_features
    import multimodal_features as MF
    multimodal_path = os.path.join(os.path.expanduser(daily_csv_dir or '~/piquant-collect/daily_csv'), 'multimodal_1D.csv')
    mm_df = MF.load_multimodal_csv(multimodal_path)

    # Build unified causal features on full history then slice to avoid edge warm-up distortion
    full_eth_aligned = full["ETH"].reindex(full["BTC"].index).ffill()
    features_full = FS.build_unified_features(full["BTC"], full_eth_aligned, multimodal=mm_df)
    features_sub = features_full.reindex(btc_sub.index)

    f = {c: load_funding(f"{c}USDT") for c in ("BTC", "ETH")}
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"].dt.tz_localize("UTC")).dropna()
    reg = S5.regime_months(S5.complete_months(cut(btc_sub["c"].pct_change().fillna(0), W0, W1)))

    c1_base = S19.portfolio([S19.coin_returns(d[c], f[c], "C1", S19.BASE) for c in ("BTC", "ETH")]).reindex(
        btc_sub.index
    ).fillna(0.0)
    c1_stress = S19.portfolio([S19.coin_returns(d[c], f[c], "C1", S19.STRESS) for c in ("BTC", "ETH")]).reindex(
        btc_sub.index
    ).fillna(0.0)

    return {
        "btc": btc_sub,
        "eth": eth_aligned,
        "features": features_sub,
        "dual_1x": dual_1x,
        "c1_base": c1_base,
        "c1_stress": c1_stress,
        "rate": rate,
        "reg": reg,
        "lab": lab,
        "S36": S36,
        "S42": S42,
        "extended_metrics": extended_metrics,
    }


def _evaluate_real_full(
    ctx: dict[str, Any],
    signal: pd.Series,
    role: str,
    n_trials: int,
) -> tuple[dict[str, Any], dict[str, Any], dict[str, Any]]:
    lab = ctx["lab"]
    S36 = ctx["S36"]
    S42 = ctx["S42"]
    extended_metrics = ctx["extended_metrics"]
    btc_sub = ctx["btc"]
    eth_sub = ctx["eth"]
    reg = ctx["reg"]
    rate = ctx["rate"]

    if role == "overlay":
        pos_b = ctx["dual_1x"]["BTC"] * signal.reindex(btc_sub.index).fillna(1.0)
        pos_e = ctx["dual_1x"]["ETH"] * signal.reindex(btc_sub.index).fillna(1.0)
        trend_share, carry_share = 0.60, 0.40
    else:
        pos_b = signal.reindex(btc_sub.index).fillna(0.0)
        pos_e = signal.reindex(btc_sub.index).fillna(0.0)
        trend_share, carry_share = 0.60, 0.40

    def _run(c1_ret: pd.Series, cost_cfg: Any) -> dict[str, Any]:
        rv, mv, _ = S36.simulate_qd_carry_portfolio(
            btc_sub, eth_sub, pos_b, pos_e, c1_ret, rate,
            trend_share=trend_share, carry_share=carry_share,
            taker_bps=cost_cfg.taker_bps, slip_bps=cost_cfg.slip_bps,
            funding_bps_h=cost_cfg.funding_bps_h,
            use_taper=True, dd_start=0.06, dd_full=0.20, dd_floor=0.40,
        )
        m = extended_metrics(rv, reg)
        m["annualized_pct"] = round(((1.0 + m["geo_monthly_pct"] / 100.0) ** 12 - 1.0) * 100.0, 2)
        m["deflated_sharpe"] = round(float(lab.deflated_sharpe(rv, n_trials)), 4)
        m["mean_taper_mult"] = round(float(mv.mean()), 4)
        m.update(S42.drawdown_diagnostics(rv))
        return m

    base_m = _run(ctx["c1_base"], lab.US_FUTURES)
    stress_m = _run(ctx["c1_stress"], lab.US_FUTURES_STRESS)
    spot_m = _run(ctx["c1_base"], lab.US_SPOT)
    return base_m, stress_m, spot_m


def run_discovery(
    n_candidates: int = 8,
    mode: str = "auto",
    llama_bin: str = DEFAULT_LLAMA_BIN,
    llama_model: str = DEFAULT_LLAMA_MODEL,
    llama_threads: int = DEFAULT_LLAMA_THREADS,
    llama_url: str = DEFAULT_LLAMA_URL,
    ollama_url: str = "http://127.0.0.1:11434",
    openrouter_model: str = DEFAULT_OPENROUTER_MODEL,
    openrouter_url: str = DEFAULT_OPENROUTER_URL,
    model: str = "lfm2.5",
    role: str = "overlay",
    seed: int = 42,
    dry_run_synth: bool = False,
    daily_csv_dir: str | None = None,
    state_path: str = DEFAULT_STATE_PATH,
    shadow_path: str = DEFAULT_SHADOW_PATH,
    log_path: str = DEFAULT_LOG_PATH,
    venue: str = "futures",
    latency_ms: float | None = None,
    probe_latency: bool = True,
) -> dict[str, Any]:
    """Execute one bounded autonomous alpha-discovery round."""
    if venue not in ("futures", "spot"):
        raise ValueError(f"unknown venue: {venue}")
    state = _load_state(state_path)
    recent_fb = _load_recent_feedback(log_path, limit=5)

    measured_ms = latency_ms
    if measured_ms is None and probe_latency and not dry_run_synth:
        measured_ms = probe_venue_latency_ms()
    latency_profile = build_latency_profile(measured_ms, venue=venue)

    # 1. Generate candidate expressions: C++ llama.cpp -> OpenRouter -> Ollama -> deterministic symbolic
    expr_pairs: list[tuple[str, str]] = []
    llamacpp_note: str | None = None
    openrouter_note: str | None = None
    ollama_note: str | None = None

    if mode in ("auto", "llamacpp"):
        llama_exprs, llamacpp_note, llama_src = generate_llamacpp_candidates(
            n_candidates=n_candidates,
            llama_bin=llama_bin,
            llama_model=llama_model,
            llama_threads=llama_threads,
            llama_url=llama_url,
            recent_feedback=recent_fb,
            latency_profile=latency_profile,
        )
        seen_llama: set[str] = set()
        for e in llama_exprs:
            if e not in seen_llama:
                expr_pairs.append((e, llama_src))
                seen_llama.add(e)

    if mode == "openrouter" or (mode == "auto" and not dry_run_synth and len(expr_pairs) < n_candidates and _load_openrouter_key()):
        needed = n_candidates - len(expr_pairs)
        or_exprs, openrouter_note = generate_openrouter_candidates(
            n_candidates=needed,
            model=openrouter_model,
            openrouter_url=openrouter_url,
            recent_feedback=recent_fb,
            latency_profile=latency_profile,
        )
        seen = {e for e, _ in expr_pairs}
        for e in or_exprs:
            if e not in seen:
                expr_pairs.append((e, f"openrouter:{openrouter_model}"))
                seen.add(e)

    if mode == "ollama" or (mode == "auto" and len(expr_pairs) < n_candidates):
        needed = n_candidates - len(expr_pairs)
        ollama_exprs, ollama_note = generate_ollama_candidates(
            n_candidates=needed,
            ollama_url=ollama_url,
            model=model,
            recent_feedback=recent_fb,
            latency_profile=latency_profile,
        )
        seen = {e for e, _ in expr_pairs}
        for e in ollama_exprs:
            if e not in seen:
                expr_pairs.append((e, f"ollama:{model}"))
                seen.add(e)

    if mode == "symbolic" or (mode == "auto" and len(expr_pairs) < n_candidates):
        needed = n_candidates - len(expr_pairs)
        seen = {e for e, _ in expr_pairs}
        for e in generate_symbolic_candidates(n_candidates=needed + len(seen), seed=seed):
            if e not in seen:
                expr_pairs.append((e, "symbolic"))
                seen.add(e)
            if len(expr_pairs) >= n_candidates:
                break

    # 2. Load market context (synthetic or real 2020-2026 candles)
    if dry_run_synth:
        btc_syn, eth_syn = _make_synthetic_ohlcv(n=400, seed=seed + 99)
        features_syn = FS.build_unified_features(btc_syn, eth_syn)
        ctx = None
    else:
        ctx = _load_real_market_context(daily_csv_dir=daily_csv_dir)
        btc_syn = eth_syn = features_syn = None

    # Load existing shadow queue and deduplicate by (expr, venue, role)
    shadow_queue: list[dict[str, Any]] = []
    if os.path.exists(shadow_path):
        try:
            with open(shadow_path, "r", encoding="utf-8") as fh:
                loaded = json.load(fh)
            if isinstance(loaded, list):
                shadow_queue = loaded
        except json.JSONDecodeError:
            shadow_queue = []

    existing_shadow_keys: set[tuple[str, str, str]] = {
        (
            _sanitize_expr(str(item.get("expr", ""))),
            str(item.get("venue", "futures")),
            str(item.get("role", "overlay")),
        )
        for item in shadow_queue
        if isinstance(item, dict) and item.get("expr")
    }

    results: list[CandidateResult] = []
    now_iso = dt.datetime.now(dt.timezone.utc).isoformat()

    for raw_expr, src in expr_pairs:
        expr = _sanitize_expr(raw_expr) or raw_expr
        # Gate 1: AST safety
        try:
            SafeAlphaEvaluator.validate(expr)
        except UnsafeExpressionError as exc:
            res = CandidateResult(
                expr=expr, source=src, role=role, n_trials_at_eval=state["n_trials"],
                accepted=False, reject_reason=f"AST_GATE: {exc}", venue=venue, timestamp_utc=now_iso,
            )
            results.append(res)
            _append_jsonl(log_path, asdict(res))
            state["rejected"] += 1
            continue

        # Gate 2: Synthetic causality future-shock test
        ok_causal, causal_msg = check_causality_sandbox(expr, role=role)
        if not ok_causal:
            res = CandidateResult(
                expr=expr, source=src, role=role, n_trials_at_eval=state["n_trials"],
                accepted=False, reject_reason=f"CAUSALITY_GATE: {causal_msg}", venue=venue, timestamp_utc=now_iso,
            )
            results.append(res)
            _append_jsonl(log_path, asdict(res))
            state["rejected"] += 1
            continue

        # Gate 3: Purged & Embargoed CV
        if dry_run_synth:
            assert btc_syn is not None and eth_syn is not None and features_syn is not None
            sig = SafeAlphaEvaluator.evaluate(expr, features_syn, role=role)
            if venue == "spot":
                cv_res = evaluate_purged_cv(
                    btc_syn, eth_syn, sig, None, None, role=role,
                    taker_bps=2.0, slip_bps=1.0, funding_bps_h=0.0,
                )
            else:
                cv_res = evaluate_purged_cv(btc_syn, eth_syn, sig, None, None, role=role)
        else:
            assert ctx is not None
            sig = SafeAlphaEvaluator.evaluate(expr, ctx["features"], role=role)
            cv_costs = ctx["lab"].US_SPOT if venue == "spot" else ctx["lab"].US_FUTURES
            cv_res = evaluate_purged_cv(
                ctx["btc"], ctx["eth"], sig,
                ctx["dual_1x"]["BTC"], ctx["dual_1x"]["ETH"],
                role=role,
                taker_bps=cv_costs.taker_bps,
                slip_bps=cv_costs.slip_bps,
                funding_bps_h=cv_costs.funding_bps_h,
            )

        # Charge multiple-testing trial counter for every real/CV evaluation
        state["n_trials"] += 1
        cur_trials = state["n_trials"]

        if not cv_res["passed"]:
            res = CandidateResult(
                expr=expr, source=src, role=role, n_trials_at_eval=cur_trials,
                accepted=False,
                reject_reason=(
                    f"PURGED_CV_GATE: median_sharpe={cv_res['median_fold_sharpe']}, "
                    f"pos_folds={cv_res['positive_folds']}/5"
                ),
                venue=venue,
                cv_summary=cv_res,
                timestamp_utc=now_iso,
            )
            results.append(res)
            _append_jsonl(log_path, asdict(res))
            state["rejected"] += 1
            continue

        # Gate 4: Full net-of-fees + stress + spot + DSR gate
        if dry_run_synth:
            assert btc_syn is not None and eth_syn is not None
            base_m, stress_m, spot_m = _evaluate_synthetic_full(btc_syn, eth_syn, sig, role, cur_trials)
        else:
            assert ctx is not None
            base_m, stress_m, spot_m = _evaluate_real_full(ctx, sig, role, cur_trials)

        ok_full, full_reason = _check_full_gates(
            base_m, stress_m, spot=spot_m, venue=venue, cv_summary=cv_res, latency_profile=latency_profile
        )
        res = CandidateResult(
            expr=expr, source=src, role=role, n_trials_at_eval=cur_trials,
            accepted=ok_full,
            reject_reason="" if ok_full else f"FULL_GATE: {full_reason}",
            venue=venue,
            cv_summary=cv_res,
            base_metrics=base_m,
            stress_metrics=stress_m,
            spot_metrics=spot_m,
            timestamp_utc=now_iso,
        )
        results.append(res)
        _append_jsonl(log_path, asdict(res))

        if ok_full:
            state["accepted"] += 1
            shadow_key = (expr, venue, role)
            if shadow_key not in existing_shadow_keys:
                existing_shadow_keys.add(shadow_key)
                entry = asdict(res)
                entry["status"] = "SHADOW_PAPER_ONLY"
                shadow_queue.append(entry)
        else:
            state["rejected"] += 1

    state["runs"] += 1
    state["last_run_utc"] = now_iso
    state["latency_profile"] = latency_profile
    _save_json(state_path, state)
    _save_json(shadow_path, shadow_queue)

    return {
        "mode": mode,
        "venue": venue,
        "latency_profile": latency_profile,
        "llamacpp_note": llamacpp_note,
        "openrouter_note": openrouter_note,
        "ollama_note": ollama_note,
        "dry_run_synth": dry_run_synth,
        "evaluated": len(results),
        "accepted": sum(1 for r in results if r.accepted),
        "rejected": sum(1 for r in results if not r.accepted),
        "n_trials_total": state["n_trials"],
        "shadow_queue_len": len(shadow_queue),
        "results": [asdict(r) for r in results],
    }


def main() -> None:
    p = argparse.ArgumentParser(description="Autonomous causal alpha-discovery daemon (shadow-paper only).")
    p.add_argument("--n-candidates", type=int, default=8)
    p.add_argument("--mode", choices=("auto", "llamacpp", "openrouter", "ollama", "symbolic"), default="auto")
    p.add_argument("--openrouter-model", default=DEFAULT_OPENROUTER_MODEL, help="OpenRouter model slug (supports :free models)")
    p.add_argument("--llama-bin", default=DEFAULT_LLAMA_BIN, help="Path to native llama-cli binary")
    p.add_argument("--llama-model", default=DEFAULT_LLAMA_MODEL, help="Path to GGUF model (e.g. LFM3-1.7B-QAD-Q4_0.gguf)")
    p.add_argument("--llama-threads", type=int, default=DEFAULT_LLAMA_THREADS, help="CPU threads for llama.cpp (default 3)")
    p.add_argument("--llama-url", default=DEFAULT_LLAMA_URL, help="Optional OpenAI-compatible llama-server base URL")
    p.add_argument("--ollama-url", default="http://127.0.0.1:11434")
    p.add_argument("--model", default="lfm2.5")
    p.add_argument("--role", choices=("overlay", "standalone"), default="overlay")
    p.add_argument("--venue", choices=("futures", "spot"), default="futures")
    p.add_argument("--latency-ms", type=float, default=None, help="Override venue RTT in ms (auto-probed if unset)")
    p.add_argument("--no-latency-probe", action="store_true", help="Skip live HTTP latency probe")
    p.add_argument("--seed", type=int, default=42)
    p.add_argument("--dry-run-synth", action="store_true")
    p.add_argument("--daily-csv-dir", default=None)
    p.add_argument("--state-path", default=DEFAULT_STATE_PATH)
    p.add_argument("--shadow-path", default=DEFAULT_SHADOW_PATH)
    p.add_argument("--log-path", default=DEFAULT_LOG_PATH)
    args = p.parse_args()

    summary = run_discovery(
        n_candidates=args.n_candidates,
        mode=args.mode,
        llama_bin=args.llama_bin,
        llama_model=args.llama_model,
        llama_threads=args.llama_threads,
        llama_url=args.llama_url,
        ollama_url=args.ollama_url,
        openrouter_model=args.openrouter_model,
        model=args.model,
        role=args.role,
        seed=args.seed,
        dry_run_synth=args.dry_run_synth,
        daily_csv_dir=args.daily_csv_dir,
        state_path=args.state_path,
        shadow_path=args.shadow_path,
        log_path=args.log_path,
        venue=args.venue,
        latency_ms=args.latency_ms,
        probe_latency=not args.no_latency_probe,
    )
    lp = summary.get("latency_profile") or {}
    print(
        f"auto_discover done | mode={summary['mode']} venue={summary['venue']} synth={summary['dry_run_synth']} "
        f"rtt={lp.get('measured_rtt_ms')}ms regime={lp.get('regime')} "
        f"evaluated={summary['evaluated']} accepted={summary['accepted']} rejected={summary['rejected']} "
        f"n_trials={summary['n_trials_total']} shadow_queue={summary['shadow_queue_len']}"
    )
    if summary["llamacpp_note"]:
        print(f"  llamacpp note: {summary['llamacpp_note']}")
    if summary.get("openrouter_note"):
        print(f"  openrouter note: {summary['openrouter_note']}")
    if summary["ollama_note"]:
        print(f"  ollama note: {summary['ollama_note']} (fell back to symbolic generator)")
    for r in summary["results"]:
        status = "SHADOW_PASS" if r["accepted"] else f"REJECT ({r['reject_reason']})"
        base = r.get("base_metrics") or {}
        extra = ""
        if base:
            extra = (
                f" | geo={float(base.get('geo_monthly_pct', 0.0)):.3f}%/mo "
                f"sharpe={float(base.get('sharpe', 0.0)):.3f} "
                f"maxDD={float(base.get('max_dd_pct', 0.0)):.2f}% "
                f"A25_26={float(base.get('A_geo_monthly_pct', 0.0)):+.2f}% "
                f"DSR={float(base.get('deflated_sharpe', 0.0)):.4f}"
            )
        print(f"  [{r['source']}] {status}{extra}\n    expr: {r['expr']}")


if __name__ == "__main__":
    main()
