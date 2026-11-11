"""Tests for run_round4 adoption logic (python test_round4.py)."""
import run_round4 as R


def h(win, net, ret):
    return {"win_rate_pct": win, "mean_net_bps": net, "total_return_pct": ret}


def test_adopt_requires_win_everywhere_and_two_of_three():
    g1 = {k: h(58, -5, -10) for k in R.HOLDOUTS}
    live = {k: h(44, -6, -12) for k in R.HOLDOUTS}
    assert R.adopt(g1, live)["adopt"]
    g1["holdout_C"] = h(54, -5, -10)
    assert not R.adopt(g1, live)["adopt"]  # H-win fails on C


def test_adopt_rejects_worse_expectancy():
    g1 = {k: h(60, -8, -20) for k in R.HOLDOUTS}
    live = {k: h(44, -6, -12) for k in R.HOLDOUTS}
    a = R.adopt(g1, live)
    assert a["h_win"] and not a["adopt"]


def test_grid():
    assert len(R.G1_GRID) == 14 and all(s / (t + s) >= 0.58 for t, s in R.PAIRS)


if __name__ == "__main__":
    for k, f in list(globals().items()):
        if k.startswith("test_"):
            f()
            print("ok", k)
