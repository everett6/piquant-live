"""Synthetic tests for strategies22 (PREREG22). No real data, no network."""
import numpy as np
import pandas as pd

import strategies22 as T

DAY = "2030-01-01"
D0 = T.day_start_ms(DAY)


def test_quotes_to_1s_takes_latest_quote_and_drops_bad():
    t = np.array([D0 + 500, D0 + 1500, D0 + 1600, D0 + 3000], dtype=np.int64)
    bid = np.array([100.0, 101.0, 102.0, 105.0])
    ask = np.array([101.0, 102.0, 101.0, 106.0])   # third quote is crossed
    b = T.quotes_to_1s(t, bid, ask, D0)
    assert b["t"].iloc[0] == D0 + 1000              # nothing before the first quote
    assert b["bid_px"].iloc[0] == 100.0
    assert D0 + 2000 not in set(b["t"])             # latest quote (crossed) gives no snapshot
    row = b[b["t"] == D0 + 3000].iloc[0]
    assert row["bid_px"] == 105.0 and row["mid"] == 105.5
    assert len(b) == 86_400 - 2                     # every later second carries the last quote


def test_quote_gap_minutes_includes_edges():
    t = D0 + np.arange(0, T.DAY_MS - 30_000, 30_000, dtype=np.int64)
    t = t[(t < D0 + 600_000) | (t > D0 + 661_000)]   # one gap: 09:30 -> 11:30 (120 s), minutes 9-11
    m0 = D0 // 60_000
    assert T.quote_gap_minutes(t, D0) == {m0 + 9, m0 + 10, m0 + 11}
    late = t[t < D0 + T.DAY_MS - 120_000]            # last quote 2+ min before 24:00
    assert (D0 + T.DAY_MS - 1) // 60_000 in T.quote_gap_minutes(late, D0)


def test_trade_id_gaps_and_clean_trades():
    raw = pd.DataFrame({"t": [D0 + 1000, D0 + 1000, D0 + 2000, D0 + 125_000],
                        "id": [1, 1, 2, 5], "side": ["buy", "buy", "sell", "buy"], "amount": [1, 1, 2, 3]})
    tr = T.clean_trades(raw)
    assert list(tr["id"]) == [1, 2, 5] and list(tr["side"]) == ["b", "s", "b"]
    bad = T.trade_id_gap_minutes(tr["id"].to_numpy(), tr["t"].to_numpy())
    assert bad == {D0 // 60_000, D0 // 60_000 + 1, D0 // 60_000 + 2}


def _walk(lag_ms, seed=1):
    rng = np.random.default_rng(seed)
    t = D0 + np.arange(0, T.DAY_MS, 250, dtype=np.int64)
    mid = 100 * np.exp(np.cumsum(rng.normal(0, 3e-4, len(t))))
    return t, mid


def test_run_day_finds_lead_when_kraken_copies_coinbase():
    t, mid = _walk(0)
    q = {"coinbase": (t, mid * (1 - 1e-5), mid * (1 + 1e-5)),
         "kraken": (t + 2000, mid * (1 - 1e-5), mid * (1 + 1e-5)),
         "binance-us": (t + 500, mid * (1 - 1e-5), mid * (1 + 1e-5))}
    n = 5000
    tr = T.clean_trades(pd.DataFrame({"t": D0 + np.linspace(0, T.DAY_MS - 1, n).astype(np.int64),
                                      "id": np.arange(n), "side": ["buy"] * n, "amount": np.ones(n)}))
    r = T.run_day(DAY, "BTC", q, tr)
    lead = r["leads"]["coinbase->kraken"]
    assert lead["signals"] > 0 and lead["5s"]["mean_bps"] > 2       # Kraken follows
    back = r["leads"]["kraken->coinbase"]
    assert back["5s"]["n"] == 0 or abs(back["5s"]["mean_bps"]) < lead["5s"]["mean_bps"]
    l1 = r["trades"][("L1", "base")]
    assert l1 and np.mean([x["gross_bps"] for x in l1]) > 0
    assert all(x["day"] == DAY for x in l1)
    assert all(x["dir"] == 1 for x in l1)                           # L1 is long only
    assert all(x["exit_t"] < D0 + T.DAY_MS for k in r["trades"] for x in r["trades"][k])


def test_pool_leads_weights_by_count():
    a = {"signals": 2, "1s": {"n": 2, "mean_bps": 1.0}, "5s": {"n": 0, "mean_bps": None},
         "30s": {"n": 2, "mean_bps": 0.0}}
    b = {"signals": 1, "1s": {"n": 1, "mean_bps": 4.0}, "5s": {"n": 1, "mean_bps": 3.0},
         "30s": {"n": 1, "mean_bps": 3.0}}
    p = T.pool_leads([a, b])
    assert p["signals"] == 3 and p["1s"]["mean_bps"] == 2.0 and p["5s"]["mean_bps"] == 3.0


def test_run_day_without_binance_us():
    t, mid = _walk(0)
    q = {"coinbase": (t, mid * (1 - 1e-5), mid * (1 + 1e-5)),
         "kraken": (t + 2000, mid * (1 - 1e-5), mid * (1 + 1e-5))}
    tr = T.clean_trades(pd.DataFrame({"t": [D0 + 1000, D0 + 2000], "id": [1, 2], "side": ["buy", "sell"],
                                      "amount": [1.0, 1.0]}))
    r = T.run_day(DAY, "BTC", q, tr)
    assert set(r["leads"]) == {"coinbase->kraken", "kraken->coinbase"}
    assert r["trades"][("L1", "base")]
