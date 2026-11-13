import numpy as np
import pandas as pd

import strategies25 as S

IDX = pd.date_range("2020-01-01", periods=200, freq="D", tz="UTC")


def test_basket_needs_60_earlier_closes_and_equal_weights():
    c = pd.DataFrame({"A": 1.0, "B": np.nan}, index=IDX)
    c.loc[IDX[100]:, "B"] = 2.0
    w = S.basket_weights(c)
    assert (w["A"].iloc[:60] == 0).all() and w["A"].iloc[60] == 1.0
    assert w.loc[IDX[159], "B"] == 0 and w.loc[IDX[160], "B"] == 0.5 and w.loc[IDX[160], "A"] == 0.5
    assert np.allclose(w.iloc[60:].sum(axis=1), 1.0)


def test_missing_day_drops_coin_and_returns_zero():
    c = pd.DataFrame({"A": np.linspace(1, 2, 200), "B": 1.0}, index=IDX)
    c.iloc[150, 0] = np.nan
    w = S.basket_weights(c)
    assert w.iloc[150]["A"] == 0 and w.iloc[150]["B"] == 1.0
    r = S.asset_returns(c)
    assert r.iloc[150]["A"] == 0 and r.iloc[151]["A"] == 0


def test_downtrend_gate():
    up = pd.Series(np.linspace(100, 200, 200), index=IDX)
    assert not S.btc_downtrend(up).iloc[60:].any()
    assert S.btc_downtrend(up[::-1].set_axis(IDX)).iloc[60:].all()
    assert not S.btc_downtrend(up).iloc[:49].any()  # no SMA yet -> no gate


def test_pair_pnl_timing_and_no_lookahead():
    btc = pd.Series(100 * 1.01 ** np.arange(200), index=IDX)        # BTC +1%/day
    alts = pd.DataFrame({"A": 50 * 0.98 ** np.arange(200)}, index=IDX)  # alt -2%/day
    gate = pd.Series(True, index=IDX)
    tot, contrib, pos = S.pair_returns(btc, alts, gate, 0.0, 0.0, 0.0)
    assert (tot.iloc[:61] == 0).all()                    # first position decided at close 60, earns day 61
    assert np.allclose(tot.iloc[62:], 0.01 + 0.02)
    btc2 = btc.copy()
    btc2.iloc[150:] *= 3
    tot2, _, _ = S.pair_returns(btc2, alts, gate, 0.0, 0.0, 0.0)
    assert np.allclose(tot.iloc[:150], tot2.iloc[:150])


def test_costs_and_funding():
    btc = pd.Series(100.0, index=IDX)
    alts = pd.DataFrame({"A": 1.0, "B": 1.0}, index=IDX)
    gate = pd.Series(False, index=IDX)
    gate.iloc[100:110] = True
    tot, _, _ = S.pair_returns(btc, alts, gate, 6.0, 1.0, 0.1)
    # enter at day 101 (turnover 2: BTC 1 + basket 1), exit at day 111 (turnover 2); funding on gross 2 for 10 days
    assert np.isclose(tot.sum(), -(4 * 7e-4) - 10 * 2 * 0.1 * 24e-4)


def test_no_pair_without_basket():
    btc = pd.Series(np.linspace(200, 100, 200), index=IDX)
    alts = pd.DataFrame({"A": np.nan}, index=IDX)
    tot, _, pos = S.pair_returns(btc, alts, pd.Series(True, index=IDX), 6.0, 1.0, 0.1)
    assert (pos["BTC"] == 0).all() and (tot == 0).all()
