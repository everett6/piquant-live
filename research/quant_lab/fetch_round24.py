"""PREREG24 data: SPY/TLT/GLD/UUP adjusted daily closes (Yahoo chart API; Stooq fallback) and FRED DTB3.
Writes ~/piquant-data/macro/<SYM>.csv (date,adj) and DTB3.csv (date,rate). Stdlib only."""
from __future__ import annotations

import csv
import datetime as dt
import io
import json
import os
import sys
import urllib.request

OUT = os.path.expanduser("~/piquant-data/macro")
UA = {"User-Agent": "Mozilla/5.0 (research script)"}
START = int(dt.datetime(2014, 1, 1, tzinfo=dt.timezone.utc).timestamp())


def get(url: str) -> bytes:
    with urllib.request.urlopen(urllib.request.Request(url, headers=UA), timeout=60) as r:
        return r.read()


def yahoo(sym: str) -> list[tuple[str, float]]:
    end = int(dt.datetime.now(dt.timezone.utc).timestamp())
    j = json.loads(get(f"https://query1.finance.yahoo.com/v8/finance/chart/{sym}?period1={START}&period2={end}"
                       "&interval=1d&events=div%2Csplit"))["chart"]["result"][0]
    ts, adj = j["timestamp"], j["indicators"]["adjclose"][0]["adjclose"]
    tz = dt.timezone(dt.timedelta(seconds=j["meta"].get("gmtoffset", -18000)))
    return [(dt.datetime.fromtimestamp(t, tz).date().isoformat(), a) for t, a in zip(ts, adj) if a is not None]


def stooq(sym: str) -> list[tuple[str, float]]:
    rows = csv.DictReader(io.StringIO(get(f"https://stooq.com/q/d/l/?s={sym.lower()}.us&i=d").decode()))
    return [(r["Date"], float(r["Close"])) for r in rows if r.get("Date", "") >= "2014-01-01"]


def main():
    os.makedirs(OUT, exist_ok=True)
    src = {}
    for sym in ("SPY", "TLT", "GLD", "UUP"):
        try:
            rows, src[sym] = yahoo(sym), "yahoo"
        except Exception as e:  # noqa: BLE001
            print(f"{sym}: yahoo failed ({e}); trying stooq", file=sys.stderr)
            rows, src[sym] = stooq(sym), "stooq"
        with open(os.path.join(OUT, f"{sym}.csv"), "w", newline="") as f:
            w = csv.writer(f)
            w.writerow(["date", "adj"])
            w.writerows(rows)
        print(f"{sym}: {len(rows)} days {rows[0][0]}..{rows[-1][0]} ({src[sym]})")
    rows = [r for r in csv.DictReader(io.StringIO(get(
        "https://fred.stlouisfed.org/graph/fredgraph.csv?id=DTB3&cosd=2014-01-01").decode()))]
    key = [k for k in rows[0] if k.lower() != "observation_date" and k.upper() != "DATE"][0]
    datek = [k for k in rows[0] if k != key][0]
    good = [(r[datek], float(r[key])) for r in rows if r[key] not in ("", ".")]
    with open(os.path.join(OUT, "DTB3.csv"), "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["date", "rate"])
        w.writerows(good)
    print(f"DTB3: {len(good)} days {good[0][0]}..{good[-1][0]}")
    with open(os.path.join(OUT, "SOURCES.json"), "w") as f:
        json.dump({"fetched_utc": dt.datetime.now(dt.timezone.utc).isoformat(), "etf_source": src,
                   "rf": "FRED DTB3"}, f, indent=1)


if __name__ == "__main__":
    main()
