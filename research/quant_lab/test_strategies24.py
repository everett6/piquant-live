import numpy as np
import pandas as pd

import strategies24 as S


def _px(trend, n=400, vol=0.01, seed=0):
    rng = np.random.default_rng(seed)
    idx = pd.bdate_range("2015-01-01", periods=n)
    return pd.Series(100 * np.exp(np.cumsum(trend + vol * rng.standard_normal(n))), index=idx)


def test_weights_sign_size_and_warmup():
    up = _px(0.002)
    w = S.tsmom_weights(up, 0.10)
    assert (w.iloc[:252] == 0).all()
    assert (w.iloc[260:] > 0).all()
    vol = up.pct_change().rolling(60).std().iloc[-1] * np.sqrt(252)
    assert abs(w.iloc[-1] - min(4.0, 0.10 / vol)) < 1e-12
    assert (S.tsmom_weights(_px(-0.002), 0.10).iloc[260:] < 0).all()


def test_cap_at_four():
    calm = _px(0.001, vol=0.0005)
    assert S.tsmom_weights(calm, 0.40).iloc[300:].max() == 4.0


def test_overlay_no_lookahead_and_excess_return():
    px = pd.DataFrame({"A": _px(0.002, seed=1)})
    rf = pd.Series(5.0, index=px.index)
    tot, c = S.overlay_returns(px, rf, 0.10, 0.0)
    w = S.tsmom_weights(px["A"], 0.10)
    d = px.index[300]
    expect = w.shift(1)[d] * (px["A"].pct_change()[d] - 0.05 / 252)
    assert abs(tot[d] - expect) < 1e-12
    # changing a future price must not change earlier returns
    px2 = px.copy()
    px2.iloc[350:, 0] *= 1.5
    tot2, _ = S.overlay_returns(px2, rf, 0.10, 0.0)
    assert np.allclose(tot.iloc[:350], tot2.iloc[:350])


def test_costs_charged_on_turnover():
    px = pd.DataFrame({"A": _px(0.002, seed=2)})
    rf = pd.Series(0.0, index=px.index)
    t0, _ = S.overlay_returns(px, rf, 0.10, 0.0)
    t1, _ = S.overlay_returns(px, rf, 0.10, 2.0)
    held = S.tsmom_weights(px["A"], 0.10).shift(1).fillna(0)
    assert np.isclose((t0 - t1).sum(), held.diff().abs().fillna(held.abs()).sum() * 2e-4)


def test_idle_share_uses_previous_crypto_close():
    days = pd.date_range("2020-01-01", periods=6, freq="D", tz="UTC")
    btc = pd.Series([2, 0, 0, 2, 2, 0], index=days, dtype=float)
    eth = pd.Series([2, 2, 0, 0, 2, 2], index=days, dtype=float)
    etf = pd.DatetimeIndex(days[1:])
    k = S.idle_share(btc, eth, etf)
    assert list(k) == [0.0, 0.5, 1.0, 0.5, 0.0]
    k2 = S.idle_share(btc, eth.iloc[3:], etf)  # ETH missing before day 3 counts as flat
    assert list(k2) == [0.5, 1.0, 1.0, 0.5, 0.0]


def test_target_series_scales_weights():
    px = _px(0.002, seed=3)
    tgt = pd.Series(0.40, index=px.index)
    tgt.iloc[:330] = 0.0
    w = S.tsmom_weights(px, tgt)
    assert (w.iloc[:330] == 0).all() and (w.iloc[330:] > 0).all()


def test_to_crypto_days_weekends_zero():
    etf = pd.Series([0.01, 0.02], index=pd.DatetimeIndex(["2020-01-03", "2020-01-06"]))  # Fri, Mon
    cdays = pd.date_range("2020-01-03", periods=4, freq="D", tz="UTC")
    out = S.to_crypto_days(etf, cdays)
    assert list(out) == [0.01, 0.0, 0.0, 0.02]
