"""Synthetic tests for round-10 code (python test_strategies10.py)."""
import numpy as np
import pandas as pd

import strategies10 as S10


def _day(prices, day="2024-03-05"):
    idx = pd.date_range(day, periods=len(prices), freq="min", tz="UTC")
    c = pd.Series(prices, index=idx, dtype=float)
    o = c.shift(1).fillna(c.iloc[0])
    return pd.DataFrame({"o": o, "h": np.maximum(o, c), "l": np.minimum(o, c), "c": c, "v": 1.0})


def test_session_table_and_selection():
    px = np.r_[np.linspace(100, 101, 480), np.full(960, 101.0)]
    frames = [_day(px * (1 + 0.001 * k), day=str(pd.Timestamp("2024-01-01") + pd.Timedelta(days=k))[:10]) for k in range(10)]
    df = pd.concat(frames)
    tab = S10.session_table(df, np.ones(len(df), int))
    assert set(tab["sess"]) == {0, 1, 2, 3} and len(tab) == 40
    asia = tab[tab["sess"] == 0]["raw"]
    assert (asia > 0.0099).all()
    tab.loc[tab["sess"] == 0, "raw"] += np.linspace(0, 1e-4, 10)  # give Asia variance so t is finite
    cells = S10.t1_select(tab)
    assert cells.get((0, 1)) == 1 and (1, 1) not in cells
    tr = S10.t1_trades(tab, cells)
    assert len(tr) == 10 and (tr["net"] < tr["gross"]).all()


def test_t2_long_breakout_and_stop():
    px = np.full(1440, 100.0)
    px[810:870] = np.r_[np.full(30, 100.5), np.full(30, 99.5)]  # range 99.5..100.5
    px[900] = 101.0        # close above the range high at 15:00 -> enter at 15:01 open
    px[901:1000] = 101.0
    px[1000] = 99.0        # falls through the 99.5 stop
    df = _day(px)
    tr = S10.t2_trades(df, np.ones(len(df), int))
    assert len(tr) == 1 and tr.iloc[0].reason == "SL" and tr.iloc[0].side == 1
    tr_dn = S10.t2_trades(df, -np.ones(len(df), int))
    assert len(tr_dn) == 1 and tr_dn.iloc[0].side == -1  # px[1000] closes below the range low


if __name__ == "__main__":
    for k, f in list(globals().items()):
        if k.startswith("test_"):
            f()
            print("ok", k)
