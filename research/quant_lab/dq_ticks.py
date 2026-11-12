"""Data-quality check of the recorded ticks and books for one UTC day (gaps only; no signals). Usage: dq_ticks.py YYYY-MM-DD"""
import gzip, json, os, sys
import numpy as np
C = os.path.expanduser("~/piquant-collect")
day = sys.argv[1]
def rows(kind, p):
    f = os.path.join(C, kind, p, day + ".jsonl.gz")
    with gzip.open(f, "rt") as fh:
        for line in fh:
            if line.strip():
                try: yield json.loads(line)
                except json.JSONDecodeError: yield None
for p in sorted(os.listdir(os.path.join(C, "ticks"))):
    r = [x for x in rows("ticks", p)]
    bad = sum(x is None for x in r); r = [x for x in r if x]
    t = np.array([x["t"] for x in r], np.int64); ids = np.array([x["id"] for x in r], np.int64)
    o = np.argsort(ids, kind="stable"); ids_s = np.unique(ids)
    d = np.diff(ids_s)
    gaps = d[d > 1]
    tg = np.diff(np.sort(t))
    span_h = (t.max() - t.min()) / 3.6e6
    print(f"TICKS {p:20s} n={len(r):8d} dup={len(ids)-len(ids_s):5d} badjson={bad} span={span_h:5.1f}h "
          f"id_gaps={len(gaps):5d} missing_ids={int((gaps-1).sum()):8d} max_time_gap={tg.max()/1000 if len(tg) else 0:7.1f}s "
          f"gaps>60s={int((tg>60000).sum())} out_of_order_t={int((np.diff(t)<0).sum())}")
for p in sorted(os.listdir(os.path.join(C, "book"))):
    r = [x for x in rows("book", p) if x]
    t = np.sort(np.array([x["t"] for x in r], np.int64))
    empty = sum(1 for x in r if not x.get("b") or not x.get("a"))
    crossed = sum(1 for x in r if x.get("b") and x.get("a") and x["b"][0][0] >= x["a"][0][0])
    g = np.diff(t)
    big = g[g > 5000]
    print(f"BOOK  {p:20s} n={len(r):7d} span={(t[-1]-t[0])/3.6e6:5.1f}h median_dt={np.median(g)/1000:4.2f}s "
          f"gaps>5s={len(big):4d} lost={big.sum()/60000:6.1f}min max_gap={g.max()/1000:7.1f}s empty={empty} crossed={crossed}")
