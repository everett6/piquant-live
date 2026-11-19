# Pre-registration 47: gradient-boosted trees (XGBoost-style) on daily BTC/ETH (committed before any round-47 code reads real data)

Asked for by the owner on 2026-11-18: "how about xgboost models". Only synthetic tests had run when this was
committed.

## Model

scikit-learn `HistGradientBoostingClassifier` (histogram gradient-boosted trees, the same method as LightGBM and
XGBoost's `hist` mode; already installed, nothing downloaded). Fixed settings, chosen before any data was read and never
tuned: `max_depth=3, learning_rate=0.05, max_iter=200, min_samples_leaf=50, l2_regularization=1.0, random_state=0`,
no early stopping.

**Features** (all from closes and volume up to and including day t; one row per coin per day; BTC and ETH rows pooled
into one model with a coin flag):
- log returns over 1, 3, 7, 14, 30, 60 and 120 days;
- realised volatility over 7 and 30 days and their ratio;
- close / SMA50 − 1 and close / SMA200 − 1;
- drawdown from the 90-day high;
- 30-day volume / 180-day volume;
- day of week;
- the other coin's 7- and 30-day log returns.

**Walk-forward, no look-ahead.** Training starts with the first row where every feature exists (2016). The model is
refitted every 63 trading days (about quarterly) on all rows whose label is fully known at the refit date, with a 5-day
embargo (labels cover 5 days). Each fitted model predicts only the next 63 days. The first prediction day is
2020-01-01, so every scored day is out of sample.

## The two strategies (2 trials, 800 → 802)

**X1, direct forecaster.** Label: the coin's 5-day forward return > 0. Position at close t, held over t+1 (as everywhere
in this lab): long 1x if the predicted probability ≥ 0.55, otherwise flat. BTC/ETH halves, US futures costs
(`lab.US_FUTURES`; stress `lab.US_FUTURES_STRESS`).
Compared with the QD coherence trend sleeve at the same 1x long/flat exposure and costs.

**X2, meta-label filter on blend A's trend sleeve.** Blend A = `Q42_dualspeed_60_40_carry` (as in PREREG45/46).
Label: the dual-speed trend position's 5-day forward return (position × coin return) > 0, trained only on days the
position is not zero. When the predicted probability < 0.45, the trend position is halved; otherwise it is unchanged.
The blend is rebuilt with the filtered positions (same carry, taper and costs).

Window 2020-01-01 .. 2026-11-01 (W0/W1 of run_round33). "Recent" = A period from 2025-07-01 (as reported by
`extended_metrics`).

## Pass rules (all must hold; stress = stress costs)

X1:
1. out-of-sample AUC of the 5-day label ≥ 0.53 (pooled BTC+ETH);
2. Sharpe > the QD 1x sleeve's Sharpe;
3. recent-period return > 0;
4. stress return > 0;
5. deflated Sharpe at 802 trials ≥ 0.50.

X2:
1. max drawdown at least 2 points better than blend A's;
2. return ≥ blend A − 0.10 %/mo;
3. Sharpe > blend A's;
4. stress return ≥ blend A stress − 0.10 %/mo;
5. recent-period return > 0.

Reported but not judged: AUC per coin, share of days long (X1) or filtered (X2), feature importances by permutation
on the last fitted model, and turnover.

## Predictions (written before running)

- X1: daily crypto returns are close to unpredictable from their own price history; the trend features the model can
  find are the same ones QD already uses, and the model adds turnover. AUC around 0.51–0.53. Fail, about 1 in 8.
- X2: meta-labelling has the better chance because it only has to spot bad trend days, not forecast prices. Still, the
  trend's losing days are whipsaws that look like winning days when they start. About 1 in 5.

No variants: if either fails, no other settings, thresholds, horizons or features will be tried under this family.
