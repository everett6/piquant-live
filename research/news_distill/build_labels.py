"""Build the headline label table for distilling a small news model (LFM3) on the desktop.

One row per headline with every label source we have:
  - teacher labels (CryptoNewsImpact LLM annotations; Jan-v1-4B extractions of our own feeds)
  - market labels: BTC forward log returns after the headline, from our Coinbase 1m candles,
    plus trailing 24h realized vol so returns can be expressed in sigmas.
Split is by time (no shuffling) and katelabs (Feb-Apr 2026, scored at ingestion) is a held-out test.

Run:  "<venv with pandas+pyarrow>/bin/python" -I build_labels.py --dir ~/piquant-collect
"""
import argparse
import glob
import gzip
import json
import os

import numpy as np
import pandas as pd

HORIZONS_MIN = (15, 60, 240)
TRAIN_END = pd.Timestamp("2024-07-01", tz="UTC")  # train < this <= val; katelabs = test


def read_jsonl_gz(paths):
    rows = []
    for p in paths:
        try:
            with gzip.open(p, "rt") as f:
                for line in f:
                    try:
                        rows.append(json.loads(line))
                    except json.JSONDecodeError:
                        pass
        except (EOFError, OSError):  # truncated final gzip member from a crash
            pass
    return rows


def load_candles(d, product="BTC-USD"):
    rows = read_jsonl_gz(sorted(glob.glob(os.path.join(d, "candles", product, "*.jsonl.gz"))))
    if not rows:
        return pd.Series(dtype=float)
    c = pd.DataFrame(rows).drop_duplicates("t").sort_values("t")
    return pd.Series(np.log(c["c"].to_numpy()), index=c["t"].to_numpy())  # unix s -> log close


def market_labels(ts, logc):
    """Forward log returns (bps) and trailing 24h 1-minute realized vol for each headline time."""
    out = {f"ret_{h}m_bps": np.full(len(ts), np.nan) for h in HORIZONS_MIN}
    out["vol24h_1m_bps"] = np.full(len(ts), np.nan)
    if logc.empty:
        return pd.DataFrame(out)
    t = logc.index.to_numpy()
    v = logc.to_numpy()
    r1 = np.diff(v, prepend=v[0])
    csum, csq = np.cumsum(r1), np.cumsum(r1 * r1)
    sec = (ts.astype("int64") // 10**9).to_numpy()
    i0 = np.searchsorted(t, sec, side="right") - 1  # last candle at/before the headline: no look-ahead
    ok = (i0 >= 0) & (np.abs(t[np.clip(i0, 0, len(t) - 1)] - sec) <= 120)
    for h in HORIZONS_MIN:
        j = np.searchsorted(t, sec + h * 60, side="right") - 1
        good = ok & (j > i0) & (np.abs(t[np.clip(j, 0, len(t) - 1)] - (sec + h * 60)) <= 120)
        out[f"ret_{h}m_bps"][good] = (v[j[good]] - v[i0[good]]) * 1e4
    k = np.searchsorted(t, sec - 86400, side="left")
    n = i0 - k
    good = ok & (n > 600)
    s1 = csum[i0[good]] - csum[k[good]]
    s2 = csq[i0[good]] - csq[k[good]]
    nn = n[good]
    out["vol24h_1m_bps"][good] = np.sqrt(np.maximum(s2 / nn - (s1 / nn) ** 2, 0)) * 1e4
    return pd.DataFrame(out)


def norm_timeframe(s):
    s = str(s).lower()
    if "short" in s:
        return "hours"
    if "mid" in s or "medium" in s:
        return "days"
    if "long" in s:
        return "weeks"
    return None


def load_sources(d):
    pub = os.path.join(d, "public")
    frames = []
    cni = pd.read_parquet(os.path.join(pub, "cryptonewsimpact", "train.parquet"))
    st = cni["impact_status"].str.lower().str.strip()
    sign = np.select([st == "increase", st == "decrease"], [1.0, -1.0], 0.0)
    frames.append(pd.DataFrame({
        "source": "cryptonewsimpact",
        "title": cni["title"],
        "ts": pd.to_datetime(cni["published_on"], utc=True, errors="coerce"),
        "teacher_direction": sign * cni["impact_score"].fillna(0).clip(0, 10) / 10,
        "teacher_magnitude": cni["impact_score"].fillna(0).clip(0, 10) / 10 * (sign != 0),
        "teacher_horizon": cni["impact_timeframe"].map(norm_timeframe),
        "teacher_expected_pct": cni["expected_change_pct"],
    }))
    cd = pd.read_parquet(os.path.join(pub, "coindesk_2020_2025", "train.parquet"), columns=["published_on", "title"])
    raw = cd["published_on"]
    num = pd.to_numeric(raw, errors="coerce")
    ts = pd.to_datetime(num, unit="s", utc=True) if num.notna().mean() > 0.9 else pd.to_datetime(
        raw, utc=True, errors="coerce", format="mixed")
    frames.append(pd.DataFrame({"source": "coindesk", "title": cd["title"], "ts": ts}))
    kl = pd.read_parquet(os.path.join(pub, "katelabs_holdout", "headlines.parquet"))
    frames.append(pd.DataFrame({
        "source": "katelabs", "title": kl["headline"], "ts": pd.to_datetime(kl["published_at"], utc=True),
        "katelabs_score": kl["score"], "katelabs_type": kl["news_type"],
    }))
    ex = read_jsonl_gz(glob.glob(os.path.join(d, "extract", "*.jsonl.gz")))
    if ex:
        e = pd.DataFrame(ex)
        e = e[e.get("parse_err", pd.Series([""] * len(e))).fillna("") == ""]
        sig = pd.json_normalize(e["signal"]).add_prefix("jan_")
        frames.append(pd.concat([pd.DataFrame({
            "source": "live_" + e["src"].str.replace("/", "_"), "title": e["title"].to_numpy(),
            "ts": pd.to_datetime(e["at"], utc=True, format="ISO8601").to_numpy()}), sig.reset_index(drop=True)], axis=1))
    df = pd.concat(frames, ignore_index=True)
    df["title"] = df["title"].astype(str).str.strip()
    df = df[(df["title"].str.len() > 15) & df["ts"].notna()]
    df["title_key"] = df["title"].str.lower().str.replace(r"[^a-z0-9 ]", "", regex=True).str.slice(0, 120)
    # Same headline in several sources: keep one row, preferring the one carrying teacher labels.
    df["_pri"] = df["teacher_direction"].notna().astype(int) * 2 + df.get("jan_event_type", pd.Series(index=df.index)).notna().astype(int)
    df = df.sort_values(["_pri", "ts"], ascending=[False, True]).drop_duplicates("title_key").drop(columns="_pri")
    return df.sort_values("ts").reset_index(drop=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", default=os.path.expanduser("~/piquant-collect"))
    a = ap.parse_args()
    df = load_sources(a.dir)
    m = market_labels(df["ts"], load_candles(a.dir))
    df = pd.concat([df, m], axis=1)
    df["split"] = np.where(df["source"] == "katelabs", "test",
                           np.where(df["ts"] < TRAIN_END, "train", "val"))
    out = os.path.join(a.dir, "train")
    os.makedirs(out, exist_ok=True)
    df.to_parquet(os.path.join(out, "labels.parquet"), index=False)
    has_mkt = df["ret_60m_bps"].notna()
    print(f"rows={len(df)}  with market label={int(has_mkt.sum())}")
    print(df.groupby(["split", "source"]).size().to_string())
    lab = df[has_mkt & df["teacher_direction"].notna()]
    if len(lab) > 200:
        for h in HORIZONS_MIN:
            c = lab["teacher_direction"].rank().corr(lab[f"ret_{h}m_bps"].rank())  # Spearman without scipy
            print(f"teacher direction vs BTC {h}m return: spearman={c:+.4f} (n={len(lab)})")
        c = lab["teacher_magnitude"].rank().corr(lab["ret_60m_bps"].abs().rank())
        print(f"teacher magnitude vs |60m return|: spearman={c:+.4f}")


if __name__ == "__main__":
    main()
