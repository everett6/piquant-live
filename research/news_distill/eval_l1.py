"""PREREG4 L1: score 4,000 fresh headlines with piquant-news-lfm3-v1 and test whether direction/magnitude
carry information about BTC returns at 60/240/1440 minutes. Run once. Writes results/l1.json.

  python eval_l1.py --dir ~/piquant-collect --model ~/models/piquant-news-lfm3-v1/merged
"""
import argparse
import glob
import hashlib
import json
import math
import os
import sys
import time

import numpy as np
import pandas as pd

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import build_labels as BL  # noqa: E402

HORIZONS = (60, 240, 1440)
N_SAMPLE, SEED, MIN_ABS_DIR, COST_BPS = 4000, 11, 0.5, 14.0
ALPHA = 0.01 / 6


def norm_sf(z):
    return 0.5 * math.erfc(z / math.sqrt(2))


def spearman(a: pd.Series, b: pd.Series) -> dict:
    m = a.notna() & b.notna()
    n = int(m.sum())
    if n < 30:
        return {"n": n, "rho": float("nan"), "p_two_sided": float("nan")}
    r = float(a[m].rank().corr(b[m].rank()))
    t = r * math.sqrt((n - 2) / max(1e-12, 1 - r * r))
    return {"n": n, "rho": r, "p_two_sided": 2 * norm_sf(abs(t))}


def sample_rows(d: str) -> pd.DataFrame:
    lab = pd.read_parquet(os.path.join(d, "train", "labels.parquet"))
    lab["id"] = [hashlib.sha1(k.encode()).hexdigest()[:20] for k in lab["title_key"]]
    teacher_ids = set()
    for p in glob.glob(os.path.join(d, "teacher", "*.jsonl.gz")):
        teacher_ids |= {r.get("id") for r in BL.read_jsonl_gz([p])}
    pool = lab[(lab["split"] == "val") & lab["source"].isin(["cryptonewsimpact", "coindesk"])
               & lab["ret_60m_bps"].notna() & ~lab["id"].isin(teacher_ids)]
    pool = pool.drop_duplicates("title_key")
    print(f"pool={len(pool)} (teacher-labelled excluded: {len(teacher_ids)})", flush=True)
    return pool.sample(min(N_SAMPLE, len(pool)), random_state=SEED).reset_index(drop=True)


def add_1440(d: str, s: pd.DataFrame) -> pd.DataFrame:
    logc = BL.load_candles(d)
    BL.HORIZONS_MIN = (1440,)
    m = BL.market_labels(s["ts"], logc)
    s["ret_1440m_bps"] = m["ret_1440m_bps"].to_numpy()
    return s


def verdict(res: dict) -> dict:
    sig_dir = [h for h in HORIZONS if res["direction"][str(h)]["rho"] >= 0.05
               and res["direction"][str(h)]["p_two_sided"] < ALPHA and res["tradability_net_bps"][str(h)] > 0]
    sig_mag = [h for h in HORIZONS if res["magnitude"][str(h)]["rho"] >= 0.05
               and res["magnitude"][str(h)]["p_two_sided"] < ALPHA]
    if sig_dir:
        v = "applicable_signal"
    elif sig_mag:
        v = "applicable_risk_filter"
    else:
        v = "scrap"
    return {"verdict": v, "direction_horizons": sig_dir, "magnitude_horizons": sig_mag}


def score(s: pd.DataFrame, pred: list) -> dict:
    s = s.copy()
    s["dir"] = [float(p.get("direction")) if isinstance(p, dict) and isinstance(p.get("direction"), (int, float))
                else np.nan for p in pred]
    s["mag"] = [float(p.get("magnitude")) if isinstance(p, dict) and isinstance(p.get("magnitude"), (int, float))
                else np.nan for p in pred]
    res = {"n": len(s), "valid_json": float(np.mean([isinstance(p, dict) for p in pred])) if pred else 0.0,
           "direction": {}, "magnitude": {}, "tradability_net_bps": {}, "tradability_n": {},
           "reference_cni": {}}
    strong = s["dir"].abs() >= MIN_ABS_DIR
    for h in HORIZONS:
        r = s[f"ret_{h}m_bps"]
        res["direction"][str(h)] = spearman(s["dir"], r)
        res["magnitude"][str(h)] = spearman(s["mag"], r.abs())
        sel = strong & r.notna()
        res["tradability_n"][str(h)] = int(sel.sum())
        res["tradability_net_bps"][str(h)] = (float((np.sign(s.loc[sel, "dir"]) * r[sel]).mean() - COST_BPS)
                                              if sel.any() else float("nan"))
        res["reference_cni"][str(h)] = {"direction": spearman(s["teacher_direction"], r),
                                        "magnitude": spearman(s["teacher_magnitude"], r.abs())}
    res.update(verdict(res))
    return res


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", default=os.path.expanduser("~/piquant-collect"))
    ap.add_argument("--model", default=os.path.expanduser("~/models/piquant-news-lfm3-v1/merged"))
    a = ap.parse_args()
    t0 = time.time()
    s = add_1440(a.dir, sample_rows(a.dir))
    import torch  # noqa: F401  (GPU only needed past this point)
    from transformers import AutoModelForCausalLM, AutoTokenizer
    import train_lfm3 as TL
    tok = AutoTokenizer.from_pretrained(a.model, trust_remote_code=True)
    model = AutoModelForCausalLM.from_pretrained(a.model, dtype=torch.bfloat16, trust_remote_code=True,
                                                 device_map="cuda")
    pred = TL.predict(model, tok, s["title"].tolist(), batch=16)
    res = score(s, pred)
    res["elapsed_s"] = round(time.time() - t0, 1)
    os.makedirs(os.path.join(HERE, "results"), exist_ok=True)
    with open(os.path.join(HERE, "results", "l1.json"), "w") as fh:
        json.dump(res, fh, indent=1)
    print(json.dumps(res, indent=1))


if __name__ == "__main__":
    main()
