"""Download Deribit DVOL (30-day implied volatility index) daily candles for BTC and ETH (PREREG26).
Public API, no key. Writes ~/piquant-data/dvol/{CUR}.csv (date, close in percent) and SOURCES.json."""
from __future__ import annotations

import json
import os
import time
import urllib.request

import pandas as pd

OUT = os.path.expanduser("~/piquant-data/dvol")
URL = ("https://www.deribit.com/api/v2/public/get_volatility_index_data"
       "?currency={cur}&start_timestamp={a}&end_timestamp={b}&resolution=1D")
START_MS = int(pd.Timestamp("2021-01-01", tz="UTC").timestamp() * 1000)


def fetch(cur: str) -> pd.DataFrame:
    end, rows = int(time.time() * 1000), []
    while True:
        req = urllib.request.Request(URL.format(cur=cur, a=START_MS, b=end), headers={"User-Agent": "research-client"})
        with urllib.request.urlopen(req, timeout=30) as f:
            res = json.load(f)["result"]
        rows += res["data"]
        if not res.get("continuation") or not res["data"]:
            break
        end = int(res["continuation"])
        time.sleep(0.3)
    df = pd.DataFrame(rows, columns=["t", "o", "h", "l", "c"]).drop_duplicates("t").sort_values("t")
    df["date"] = pd.to_datetime(df["t"], unit="ms", utc=True).dt.strftime("%Y-%m-%d")
    return df[["date", "c"]].rename(columns={"c": "close"})


def main():
    os.makedirs(OUT, exist_ok=True)
    src = {"source": "Deribit public/get_volatility_index_data, resolution 1D", "fetched": pd.Timestamp.now("UTC").isoformat()}
    for cur in ("BTC", "ETH"):
        df = fetch(cur)
        df.to_csv(os.path.join(OUT, f"{cur}.csv"), index=False)
        src[cur] = {"rows": int(len(df)), "first": df["date"].iloc[0], "last": df["date"].iloc[-1]}
        print(cur, src[cur])
    json.dump(src, open(os.path.join(OUT, "SOURCES.json"), "w"), indent=1)


if __name__ == "__main__":
    main()
