"""Synthetic tests for strategies18 (PREREG18). No real data."""
import numpy as np
import pandas as pd

import strategies18 as S

T0 = 1_800_000_000_000  # a whole minute in ms


def book(mids, t0=T0, step=1000, half_spread_bps=1.0):
    mids = np.asarray(mids, dtype=float)
    t = t0 + step * np.arange(len(mids), dtype=np.int64)
    h = mids * half_spread_bps * 1e-4
    return pd.DataFrame({"t": t, "bid_px": mids - h, "ask_px": mids + h, "mid": mids})


def never(_t):
    return False


def test_move_1s_and_staleness():
    b = book([100, 100, 100.1])
    mv = S.move_1s_bps(b["t"].to_numpy(), b["mid"].to_numpy(), b["t"].to_numpy())
    assert np.isnan(mv[0]) and mv[1] == 0 and abs(mv[2] - 10) < 1e-6
    t = np.array([T0, T0 + 4000], dtype=np.int64)  # previous snapshot 4 s old: stale
    assert np.isnan(S.move_1s_bps(t, np.array([100.0, 101.0]), t)[1])


def test_lead_signal_needs_lag_under_half():
    lead = book([100, 100, 100.1, 100.1, 100.0])        # +10 bps at index 2, -10 bps at index 4
    flat = book([100] * 5)
    t, d = S.lead_signals(lead["t"].to_numpy(), lead["mid"].to_numpy(), flat["t"].to_numpy(),
                          flat["mid"].to_numpy(), long_only=False)
    assert t.tolist() == [T0 + 2000, T0 + 4000] and d.tolist() == [1, -1]
    t, d = S.lead_signals(lead["t"].to_numpy(), lead["mid"].to_numpy(), flat["t"].to_numpy(),
                          flat["mid"].to_numpy(), long_only=True)
    assert t.tolist() == [T0 + 2000] and d.tolist() == [1]
    follows = book([100, 100, 100.06, 100.06, 100.0])   # lagger already moved 6 bps (> half of 10)
    t, _ = S.lead_signals(lead["t"].to_numpy(), lead["mid"].to_numpy(), follows["t"].to_numpy(),
                          follows["mid"].to_numpy(), long_only=True)
    assert len(t) == 0
    small = book([100, 100, 100.04])                    # 4 bps: below the 5 bps threshold
    t, _ = S.lead_signals(small["t"].to_numpy(), small["mid"].to_numpy(), flat["t"].to_numpy(),
                          flat["mid"].to_numpy(), long_only=False)
    assert len(t) == 0


def test_simulate_pays_spread_and_fees_long_and_short():
    mids = [100.0] * 40
    mids[31:] = [100.5] * 9                             # +50 bps by the exit snapshot
    b = book(mids)
    sig = np.array([T0], dtype=np.int64)
    tr = S.simulate("BTC", sig, np.array([1]), b, S.LAG_HOLD_MS, S.Costs("x", 7.0), never)
    assert len(tr) == 1
    x = tr[0]
    assert x["entry_t"] == T0 + 1000 and x["exit_t"] == T0 + 31000   # first snapshots >= t + 200 / t + 30200
    want = (100.5 * (1 - 1e-4) / (100.0 * (1 + 1e-4)) - 1) * 1e4
    assert abs(x["net_bps"] - (want - 14)) < 1e-9
    assert abs(x["gross_bps"] - 50) < 1e-9 and abs(x["spread_bps"] - (50 - want)) < 1e-9
    sh = S.simulate("BTC", sig, np.array([-1]), b, S.LAG_HOLD_MS, S.Costs("x", 7.0), never)[0]
    entry_bid, exit_ask = 100.0 * (1 - 1e-4), 100.5 * (1 + 1e-4)
    assert abs(sh["net_bps"] - ((entry_bid - exit_ask) / entry_bid * 1e4 - 14)) < 1e-9
    assert sh["net_bps"] < -50


def test_one_trade_at_a_time_exclusions_and_gaps():
    b = book([100.0] * 120)
    sig = T0 + np.array([0, 10_000, 31_000, 40_000], dtype=np.int64)
    d = np.ones(4, dtype=int)
    tr = S.simulate("BTC", sig, d, b, S.LAG_HOLD_MS, S.Costs("x", 7.0), never)
    assert [x["signal_t"] - T0 for x in tr] == [0, 31_000]           # 10 s and 40 s fall inside open trades
    tr = S.simulate("BTC", sig, d, b, S.LAG_HOLD_MS, S.Costs("x", 7.0), lambda t: t == T0)
    assert [x["signal_t"] - T0 for x in tr] == [10_000]  # first excluded; 31 s and 40 s fall inside the next trade
    gap = b[(b["t"] <= T0) | (b["t"] > T0 + 8000)]                   # no snapshot for 8 s after the signal
    assert S.simulate("BTC", sig[:1], d[:1], gap, S.LAG_HOLD_MS, S.Costs("x", 7.0), never) == []
    late = T0 + np.array([100_000], dtype=np.int64)                  # exit would be after the data ends
    assert S.simulate("BTC", late, d[:1], b, S.LAG_HOLD_MS, S.Costs("x", 7.0), never) == []


def test_lead_profile():
    lag = book([100.0, 100.0, 100.02, 100.02, 100.02, 100.02, 100.05] + [100.05] * 30)
    sig_t = np.array([T0 + 1000], dtype=np.int64)
    p = S.lead_profile(sig_t, np.array([1]), lag["t"].to_numpy(), lag["mid"].to_numpy(), never)
    assert p["signals"] == 1
    assert abs(p["1s"]["mean_bps"] - 2) < 1e-6 and abs(p["5s"]["mean_bps"] - 5) < 1e-6
    assert abs(p["30s"]["mean_bps"] - 5) < 1e-6
    p = S.lead_profile(sig_t, np.array([-1]), lag["t"].to_numpy(), lag["mid"].to_numpy(), never)
    assert abs(p["1s"]["mean_bps"] + 2) < 1e-6
    assert S.lead_profile(sig_t, np.array([1]), lag["t"].to_numpy(), lag["mid"].to_numpy(),
                          lambda t: True)["signals"] == 0


def flow_tape():
    """Two UTC days of one size-1 trade per minute, alternating buy and sell."""
    day = pd.Timestamp("2027-01-01", tz="UTC")
    d0 = int(day.timestamp() * 1000)
    t = d0 + 60_000 * np.arange(2880, dtype=np.int64) + 30_000
    s = np.ones(2880)
    buy = (np.arange(2880) % 2 == 0)
    return d0, t, s, buy


def test_flow_signals_threshold_and_volume_floor():
    d0, t, s, buy = flow_tape()
    # Every minute has a single size-1 trade: |flow60| = 1 and volume 1 = the median, so every minute signals.
    m, d = S.flow_signals(t, s, buy)
    assert len(m) == 2880 and m[0] == d0 + 60_000 and d[0] == 1 and d[1] == -1
    # Thin minutes on day 2 (volume 0.5 < day-1 median 1) do not signal; day 1 uses its own median.
    s2 = s.copy()
    s2[1440:] = 0.5
    m2, _ = S.flow_signals(t, s2, buy)
    assert len(m2) == 1440 and m2[-1] == d0 + 1440 * 60_000
    # A balanced minute (buy 1, sell 1) has flow 0 and does not signal; 3:1 gives exactly 0.50 and does.
    tt = np.array([d0 + 1000, d0 + 2000, d0 + 61_000, d0 + 62_000], dtype=np.int64)
    ss = np.array([1.0, 1.0, 3.0, 1.0])
    bb = np.array([True, False, True, False])
    m3, d3 = S.flow_signals(tt, ss, bb)
    assert m3.tolist() == [d0 + 120_000] and d3.tolist() == [1]
    assert S.minute_volume_floor(tt, ss)["2027-01-01"] == 0.0  # most minutes of the day are empty


def test_summarize_and_pass():
    good = [{"net_bps": 3.0 + (i % 2), "gross_bps": 20.0, "spread_bps": 2.0} for i in range(200)]
    st = S.summarize(good, days=10)
    assert st["n"] == 200 and abs(st["mean_net_bps"] - 3.5) < 1e-9 and st["t"] > 2
    assert st["win_rate"] == 1.0 and st["trades_per_day"] == 20 and abs(st["spread_share_of_gross"] - 0.1) < 1e-9
    assert S.passes(st, st, st)
    assert not S.passes(S.summarize(good[:199], 10), st, st)           # fewer than 200 holdout trades
    bad = S.summarize([{"net_bps": -1.0, "gross_bps": 1.0, "spread_bps": 1.0}] * 5, 1)
    assert not S.passes(st, bad, st) and not S.passes(st, st, bad) and not S.passes(st, st, {"n": 0})
    assert S.summarize([], 1) == {"n": 0}
    assert S.COSTS["L1"][0].side_bps == 40 and S.COSTS["L1"][1].side_bps == 45
    assert S.COSTS["L2"][0].side_bps == 7 and S.COSTS["F1"][1].side_bps == 9 and S.LONG_ONLY["L1"]
