import numpy as np
import pandas as pd

import lab
import strategies29 as S

IDX = pd.date_range("2021-01-04", periods=260, freq="D", tz="UTC")  # starts on a Monday
NAMES = S.COINS[:9]


def panel(seed=3, n=260, cols=NAMES):
    rng = np.random.default_rng(seed)
    return pd.DataFrame(100 * np.cumprod(1 + rng.normal(0.001, 0.04, (n, len(cols))), axis=0), index=IDX[:n], columns=cols)


def test_no_lookahead_future_bars_do_not_change_weights():
    c = panel()
    w = S.target_weights(c)
    c2 = c.copy()
    c2.iloc[200:] *= np.linspace(0.2, 3.0, len(NAMES))
    w2 = S.target_weights(c2)
    assert w.iloc[:200].equals(w2.iloc[:200])
    r, r2 = (S.book_returns(x, y, 6, 1, 0.1) for x, y in ((c, w), (c2, w2)))
    assert np.array_equal(r["total"].to_numpy()[:200], r2["total"].to_numpy()[:200])


def test_weights_are_thirds_net_zero_and_change_only_on_mondays():
    w = S.target_weights(panel())
    active = w[(w != 0).any(axis=1)]
    assert len(active) > 100 and np.allclose(active.sum(axis=1), 0) and np.allclose(active.abs().sum(axis=1), 2.0)
    assert ((active > 0).sum(axis=1) == 3).all() and ((active < 0).sum(axis=1) == 3).all()
    changed = (w.diff().abs().sum(axis=1) > 0)
    assert (w.index[changed].dayofweek == 0).all()
    assert (w.iloc[:60] == 0).all().all()  # 60 earlier closes needed


def test_ranks_by_21_day_return():
    c = pd.DataFrame({n: 100 * (1 + 0.001 * (i + 1)) ** np.arange(260) for i, n in enumerate(NAMES)}, index=IDX)
    w = S.target_weights(c).iloc[-1]
    assert (w[NAMES[-3:]] > 0).all() and (w[NAMES[:3]] < 0).all() and (w[NAMES[3:6]] == 0).all()


def test_flat_market_and_too_few_coins_take_no_position():
    flat = pd.DataFrame(100.0, index=IDX, columns=NAMES)
    assert (S.target_weights(flat) == 0).all().all()
    assert (S.book_returns(flat, S.target_weights(flat), 6, 1, 1.0)["total"] == 0).all()
    assert (S.target_weights(panel(cols=NAMES[:5])) == 0).all().all()


def test_missing_coin_is_not_ranked_and_earns_zero():
    c = panel()
    c.loc[IDX[150]:IDX[200], "XRP"] = np.nan
    w = S.target_weights(c)
    held = w.shift(1).fillna(0)
    reb = [d for d in IDX[151:201] if d.dayofweek == 0]
    assert (w.loc[reb, "XRP"] == 0).all()
    r = S.book_returns(c, w, 0, 0, 0)
    assert np.isfinite(r["total"]).all()
    assert w.loc[IDX[201]:IDX[221], "XRP"].eq(0).all()  # needs a price 21 days back before it is ranked again


def test_crash_and_squeeze_hit_one_coin_weight():
    c = panel()
    w = S.target_weights(c)
    t = 180
    held = w.iloc[t - 1]
    longc, shortc = held.idxmax(), held.idxmin()
    for coin, jump, expect in ((longc, 0.70, -0.30 / 3), (shortc, 2.0, -1.0 / 3)):
        c2 = c.copy()
        c2.iloc[t:, c2.columns.get_loc(coin)] *= jump
        base = S.book_returns(c, w, 0, 0, 0)["total"].iloc[t]
        hit = S.book_returns(c2, S.target_weights(c2), 0, 0, 0)["total"].iloc[t]
        assert np.isclose(hit - base, expect * (1 + c[coin].pct_change().iloc[t]), atol=1e-9)


def test_costs_match_lab_position_returns_for_one_coin():
    c = panel()[["BTC"]]
    pos = np.where(np.arange(260) % 14 < 7, 1.0, -0.5)
    w = pd.DataFrame({"BTC": pos}, index=IDX)
    d = pd.DataFrame({"o": c["BTC"], "h": c["BTC"], "l": c["BTC"], "c": c["BTC"], "v": 1.0})
    for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS):
        mine = S.book_returns(c, w, costs.taker_bps, costs.slip_bps, costs.funding_bps_h)["total"]
        assert np.allclose(mine, lab.position_returns(d, pos, costs))


def test_stress_funding_charges_gross_exposure():
    c = panel()
    w = S.target_weights(c)
    b, s = (S.book_returns(c, w, 6, 1, f) for f in (0.1, 1.0))
    held = w.shift(1).fillna(0).abs().sum(axis=1)
    assert np.allclose(b["total"] - s["total"], held * 0.9e-4 * 24)
    assert np.allclose(b["long"] + b["short"] - b["cost"], b["total"])
