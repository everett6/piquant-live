"""Download DefiLlama's total USD-pegged stablecoin supply, daily (PREREG32). Public API, no key.
Writes ~/piquant-data/stablecoins/total.csv (date, usd) and SOURCES.json."""
from __future__ import annotations

import json
import os
import urllib.request

import pandas as pd

OUT = os.path.expanduser("~/piquant-data/stablecoins")
URL = "https://stablecoins.llama.fi/stablecoincharts/all"


def main():
    os.makedirs(OUT, exist_ok=True)
    req = urllib.request.Request(URL, headers={"User-Agent": "research-client"})
    with urllib.request.urlopen(req, timeout=60) as f:
        rows = json.load(f)
    df = pd.DataFrame({"date": [pd.Timestamp(int(r["date"]), unit="s", tz="UTC").strftime("%Y-%m-%d") for r in rows],
                       "usd": [float(r["totalCirculatingUSD"]["peggedUSD"]) for r in rows]})
    df = df.drop_duplicates("date", keep="last").sort_values("date")
    df.to_csv(os.path.join(OUT, "total.csv"), index=False)
    src = {"source": URL, "field": "totalCirculatingUSD.peggedUSD", "fetched": pd.Timestamp.now("UTC").isoformat(),
           "rows": int(len(df)), "first": df["date"].iloc[0], "last": df["date"].iloc[-1]}
    with open(os.path.join(OUT, "SOURCES.json"), "w") as fh:
        json.dump(src, fh, indent=1)
    print(src)


if __name__ == "__main__":
    main()
