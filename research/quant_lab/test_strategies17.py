"""Synthetic tests for strategies17 (PREREG17). No real tick data is touched."""
import numpy as np
import pandas as pd

import strategies17 as S


def books(ts, bid=100.0, bid_sz=1.0, bid_tot=10.0, ask_tot=5.0, mids=None):
    n = len(ts)
    return pd.DataFrame({"t": ts, "bid_px": [bid] * n, "bid_sz": [bid_sz] * n, "bid_tot": [bid_tot] * n,
                         "ask_tot": [ask_tot] * n, "mid": mids if mids is not None else [bid + 0.01] * n})


def trades(rows):
    return pd.DataFrame(rows, columns=["t", "p", "s", "side"])


ALWAYS = lambda t: True  # noqa: E731
NEVER = lambda t: False  # noqa: E731


def test_ladders():
    assert S.target_bps(0) == 55 and S.target_bps(10) == 30 and S.target_bps(25) == 10 and S.target_bps(29) == 10
    assert S.stop_bps(0) == -35 and S.stop_bps(10) == -25
    assert abs(S.stop_bps(25) - S.BE_BPS) < 1e-9 and S.stop_bps(29) == S.BE_BPS


def test_signals():
    assert np.allclose(S.obi(np.array([3.0, 0]), np.array([1.0, 0])), [0.5, 0])
    bt = np.array([0, 1000, 5000, 6000])
    r = S.ret5s_bps(bt, np.array([100, 100, 100, 99]))
    assert np.isnan(r[0]) and abs(r[3] - (-100)) < 1e-9  # 6000 vs snapshot at 1000
    f = S.flow5s(np.array([5000]), np.array([1000, 2000, 3000]), np.array([1.0, 1.0, 2.0]),
                 np.array([True, False, False]))
    assert abs(f[0] - (-0.5)) < 1e-9


def test_vpin_buckets_split_trades():
    # bucket volume 1; one trade of 2.5 buy then 0.5 sell -> buckets [1b], [1b], [0.5b+0.5s]
    t = np.array([1000, 2000])
    v = S.vpin_at(np.array([500, 3000]), t, np.array([2.5, 0.5]), np.array([True, False]),
                  {"1970-01-01": 1.0}, n=3)
    assert np.isnan(v[0]) and abs(v[1] - (1 + 1 + 0) / 3) < 1e-9


def test_queue_fill_then_target_win():
    b = books([0, 1000, 2000, 3000])
    tr = trades([[50, 100.0, 5.0, "s"],      # before the quote is live: ignored
                 [500, 100.0, 0.6, "s"],     # queue 1.0 -> 0.4
                 [600, 100.0, 0.5, "s"],     # filled at 100
                 [1500, 100.6, 0.1, "b"]])   # +60 bps >= 55 target: maker exit at +55
    out = S.simulate("X", tr, b, "K1", S.BASE, ALWAYS, NEVER)
    assert len(out["trades"]) == 1
    x = out["trades"][0]
    want = S.target_bps((1500 - 600) / 60_000)  # target has decayed slightly by the exit
    assert x["reason"] == "target" and abs(x["gross_bps"] - want) < 1e-6 and abs(x["net_bps"] - (want - 4)) < 1e-6


def test_stress_needs_trade_through():
    b = books([0, 1000])
    tr = trades([[500, 100.0, 5.0, "s"]])
    assert S.simulate("X", tr, b, "K1", S.STRESS, ALWAYS, NEVER)["trades"] == []


def test_stop_and_breakeven_lock():
    b = books([0, 1000, 120_000])
    tr = trades([[500, 99.0, 1.0, "s"],          # trade-through fill at 100
                 [60_000, 100.2, 0.1, "b"],      # +20 bps peak -> lock armed
                 [61_000, 100.05, 0.1, "s"]])    # +5 bps <= +9 lock stop -> taker exit
    x = S.simulate("X", tr, b, "K1", S.BASE, ALWAYS, NEVER)["trades"][0]
    assert x["reason"] == "breakeven"
    assert abs(x["net_bps"] - (5 - 1 - 2 - 6)) < 0.05  # 5 bps minus slippage and fees: a small loss, not -35
    tr2 = trades([[500, 99.0, 1.0, "s"], [60_000, 99.6, 0.1, "s"]])  # -40 bps: plain stop
    y = S.simulate("X", tr2, b, "K1", S.BASE, ALWAYS, NEVER)["trades"][0]
    assert y["reason"] == "stop" and y["net_bps"] < -40


def test_time_stop():
    b = books([0, 1000, 2_000_000])
    tr = trades([[500, 99.0, 1.0, "s"], [31 * 60_000, 100.0, 0.1, "b"]])
    x = S.simulate("X", tr, b, "K1", S.BASE, ALWAYS, NEVER)["trades"][0]
    assert x["reason"] == "time"


def test_gate_exclusion_and_confluence():
    b = books([0, 1000])
    tr = trades([[500, 99.0, 1.0, "s"]])
    assert S.simulate("X", tr, b, "K1", S.BASE, NEVER, NEVER)["quotes"] == 0
    assert S.simulate("X", tr, b, "K1", S.BASE, ALWAYS, ALWAYS)["quotes"] == 0
    vp = np.array([0.3, 0.3])
    assert S.simulate("X", tr, b, "K2", S.BASE, ALWAYS, NEVER, vpin=vp)["quotes"] == 1
    weak = books([0, 1000], bid_tot=5.0, ask_tot=5.0)  # OBI 0 fails > 0.25
    assert S.simulate("X", tr, weak, "K2", S.BASE, ALWAYS, NEVER, vpin=vp)["quotes"] == 0
    assert S.simulate("X", tr, b, "K2", S.BASE, ALWAYS, NEVER, vpin=np.array([0.6, 0.6]))["quotes"] == 0


def test_defense_cancel_avoids_fill_and_shadow_records_it():
    # Quote posted at t=0; at t=1000 the 5 s sell flow is -1 -> K3 cancels (live at 1100). The dump at 2000
    # would have filled K2 and then hit the stop.
    b = books([0, 1000, 3000, 70_000, 200_000])
    tr = trades([[900, 100.5, 0.01, "s"],         # sell flow, above our price: no fill
                 [2000, 99.0, 1.0, "s"],          # trade-through: K2 fills here, K3 already cancelled
                 [60_000, 99.0, 0.1, "s"]])       # -100 bps: stop
    vp = np.full(5, 0.3)
    k2 = S.simulate("X", tr, b, "K2", S.BASE, ALWAYS, NEVER, vpin=vp)
    k3 = S.simulate("X", tr, b, "K3", S.BASE, ALWAYS, NEVER, vpin=vp)
    assert len(k2["trades"]) == 1 and k2["trades"][0]["reason"] == "stop"
    assert k3["defense_cancels"] >= 1
    assert all(x["entry_t"] != 2000 for x in k3["trades"])
    assert len(k3["shadow_trades"]) == 1 and k3["shadow_trades"][0]["net_bps"] < 0


def test_cancel_latency_still_fills():
    b = books([0, 1000, 70_000])
    tr = trades([[900, 100.5, 0.01, "s"], [1050, 99.0, 1.0, "s"]])  # dump 50 ms after the cancel decision
    k3 = S.simulate("X", tr, b, "K3", S.BASE, ALWAYS, NEVER, vpin=np.full(3, 0.3))
    assert k3["defense_cancels"] == 1 and k3["trades"] == [] and k3["quotes"] == 1
    # the fill happened (position open at the end, no exit yet)


def test_summarize_and_pass_rule():
    tr = [{"net_bps": v, "gross_bps": v + 4, "reason": "target"} for v in [10, 0.5, -10, 12]]
    s = S.summarize(tr)
    assert s["n"] == 4 and abs(s["win_rate"] - 0.5) < 1e-9 and abs(s["scratch_rate"] - 0.25) < 1e-9
    good = {"n": 250, "mean_net_bps": 3.0, "t": 2.5}
    assert S.passes(good, {"n": 100, "mean_net_bps": 1.0}, {"n": 250, "mean_net_bps": 0.5})
    assert not S.passes({**good, "n": 150}, {"n": 100, "mean_net_bps": 1.0}, {"n": 250, "mean_net_bps": 0.5})
    assert not S.passes(good, {"n": 100, "mean_net_bps": -1.0}, {"n": 250, "mean_net_bps": 0.5})
