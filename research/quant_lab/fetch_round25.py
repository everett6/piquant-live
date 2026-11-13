"""PREREG25 data: Coinbase daily candles for the fixed altcoin list -> ~/piquant-data/alts/<COIN>.csv (date,close).
Public endpoint, no key. Stdlib only."""
from __future__ import annotations

import csv
import datetime as dt
import json
import os
import time
import urllib.request

from strategies25 import COINS

OUT = os.path.expanduser("~/piquant-data/alts")
URL = "https://api.exchange.coinbase.com/products/{p}/candles?granularity=86400&start={a}&end={b}"


def get(url: str):
    for attempt in range(5):
        try:
            req = urllib.request.Request(url, headers={"User-Agent": "research-script"})
            with urllib.request.urlopen(req, timeout=60) as r:
                return json.loads(r.read())
        except Exception:  # noqa: BLE001
            time.sleep(2 * (attempt + 1))
    raise RuntimeError(f"failed: {url}")


def main():
    os.makedirs(OUT, exist_ok=True)
    today = dt.datetime.now(dt.timezone.utc).replace(hour=0, minute=0, second=0, microsecond=0)
    for coin in COINS:
        rows: dict[str, float] = {}
        a = dt.datetime(2016, 1, 1, tzinfo=dt.timezone.utc)
        while a < today:
            b = min(a + dt.timedelta(days=299), today - dt.timedelta(days=1))
            for t, _lo, _hi, _op, close, _vol in get(URL.format(p=f"{coin}-USD", a=a.isoformat(), b=b.isoformat())):
                d = dt.datetime.fromtimestamp(t, dt.timezone.utc)
                if d < today:  # completed UTC days only
                    rows[d.date().isoformat()] = float(close)
            a = b + dt.timedelta(days=1)
            time.sleep(0.25)
        with open(os.path.join(OUT, f"{coin}.csv"), "w", newline="") as f:
            w = csv.writer(f)
            w.writerow(["date", "close"])
            w.writerows(sorted(rows.items()))
        ks = sorted(rows)
        print(f"{coin}: {len(ks)} days {ks[0] if ks else '-'}..{ks[-1] if ks else '-'}", flush=True)
    with open(os.path.join(OUT, "SOURCES.json"), "w") as f:
        json.dump({"fetched_utc": dt.datetime.now(dt.timezone.utc).isoformat(),
                   "source": "Coinbase Exchange public candles, granularity 86400"}, f, indent=1)


if __name__ == "__main__":
    main()
