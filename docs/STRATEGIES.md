# Novel and under-used strategies for PIQuant-Live

Research notes, 2026-11-02. Ranked for this engine specifically: public L2
(L1–L20) plus trades over WebSockets, millisecond-scale decisions from a
Raspberry Pi 5, paper money, no paid data and no colocation. The engine's edge
is *event-driven book state and order-flow features*, not wire latency: WAN
latency to any venue dwarfs anything the Pi can save, so every strategy below
expects to hold positions from a second to minutes.

Sources were checked by fetching each abstract or page. Where only the abstract
was reachable (not the numbers), that is noted.

## Ranked shortlist

| # | Strategy | Inputs | Realistic edge and fee hurdle | Compute | Evidence | Fit |
|---|----------|--------|-------------------------------|---------|----------|-----|
| 1 | Order Flow Imbalance (OFI), multi-level, exponentially weighted | Top-N L2 changes | Predicts a 1–10 s mid move; a filter for entries, not standalone alpha. Hurdle: ~20 bp round trip at retail taker tiers | ns–µs | High (Cont–Kukanov–Stoikov; multi-level OFI) | Excellent: event-driven, no clock sync |
| 2 | Queue-reactive + Avellaneda–Stoikov / GLFT maker with inventory skew | L1/L2 queues, prints, own fills | Spread capture minus adverse selection; Binance maker 7.5–10 bp, Kraken maker 40 bp (unviable small) | µs math, fill state machine | Med–High model / low crypto PnL evidence | Good with a fill simulator and self-trade prevention |
| 3 | Short-horizon classifier (1–10 s, triple barrier, purged CV) on L2 features | OFI + microprice deviation + queue + trade signs | Real but thin after fees; use maker entries or a coarser horizon | Ridge/logistic/tiny MLP: µs on A76 | Med (leakage-free methodology strong, net alpha weak) | Good if features stay at ~30–100 |
| 4 | Microprice / book pressure | Best bid/ask size | Related to OFI, nearly free | ns | High | Excellent baseline |
| 5 | Meta-labeling (López de Prado) on top of 1, 3 or 4 | Primary rule output + features | Does not create alpha; cuts false positives and trade count, the main fee lever | µs | Med–High method | Excellent overlay |
| 6 | Liquidation-cascade / stop-run detection from the tape | Trade sizes vs rolling distribution, book withdrawal | Event-driven, a few bursts per day; needs risk gating | µs | Med | Good |
| 7 | Hawkes-process event-intensity forecasting | LOB event stream | Predicts return sign via event clustering | Light | Med | Interesting |
| 8 | Cross-venue lead-lag (Binance → Coinbase/Kraken) | Two synchronized streams | Price discovery is real but the gap (~1 tick, ~1 bp) is below retail fees (10–80 bp); professionals close it first | Low | Med (predictive) / low (tradable) | Measure as a feature; do not trade as a standalone strategy |
| 9 | Funding-rate / perp-spot basis carry | Funding rate, spot and perp | Fee drag dominates at retail; not second-scale | Trivial | Med | A separate, slower book |
| 10 | Deep LOB models (DeepLOB, transformers, untuned RL) | Long L2 history | Accuracy gains over simple models are small and overfit-prone | Heavy for the Pi | Low–Med | Not recommended |

## Details for the top strategies

### 1. Order Flow Imbalance

Cont, Kukanov and Stoikov (*Quantitative Finance* 15(7), 2015,
https://doi.org/10.1080/14697688.2015.1013148). Per event n at level l, with
best bid/ask price P and size q before and after the update:

```
e_n,l =  I(P^b_n >= P^b_{n-1}) * q^b_n  -  I(P^b_n <= P^b_{n-1}) * q^b_{n-1}
       - [I(P^a_n <= P^a_{n-1}) * q^a_n  -  I(P^a_n >= P^a_{n-1}) * q^a_{n-1}]
```

with the convention that a missing previous book makes the change equal to the
current size. OFI over a window is the sum of e_n. The original paper reports an
almost linear OFI-to-price-impact relation (R² around 65% contemporaneously,
out of sample).

The novelty for this engine: **multi-level OFI with exponential decay across
levels**, `MLOFI = Σ_l exp(-β(l-1)) * e_n,l` over levels 1..K, which was
validated on Nasdaq names and improves as levels are added
(https://arxiv.org/abs/1907.06230). Because the engine keeps L1–L20 as integer
arrays, all 20 levels of e_n cost a few hundred integer operations per event.
Suggested starting point: K = 5, β = 0.3, windows of 1 s, 5 s and 30 s.
A per-level OFI vector can also be reduced with the microprice weights below;
that is cheaper and keeps the state-conditioned behaviour reported in
https://arxiv.org/abs/2607.09230 (order flow helps on top of a liquidity-state
model; gains are symbol-dependent).

Failure modes: transient impact reverts within seconds (fade, do not chase);
spoofed and wash-trade flow contaminates OFI
(https://arxiv.org/abs/2411.05803); at 1 s horizons taker fees can exceed the
move.

### 2. Microprice and book pressure (free baseline)

```
microprice = (P^a * q^b + P^b * q^a) / (q^a + q^b)
signal     = (microprice - mid) / spread        in [-0.5, +0.5]
```

The engine computes this on every book update in nanoseconds. A decaying-weight
version over L1..L5 is the natural bridge to multi-level OFI.

### 3. Queue-reactive market making with inventory skew

Huang, Lehalle and Rosenbaum's queue-reactive model
(https://arxiv.org/abs/1312.0563) fits limit, cancel and market arrival
intensities as a function of the best-queue size during constant-mid regimes.
That gives a **fill probability** p(δ) for quoting δ ticks from the mid, which
replaces the exponential fill assumption of Avellaneda–Stoikov. The
reservation price is `r = s - q * γ * σ² * (T - t)` for inventory q; quotes go at
`r ∓ (γσ²(T-t)/2 + (1/γ)·ln(1 + γ/κ))`. The inventory-limited version is
Guéant–Lehalle–Fernandez-Tapia (https://arxiv.org/abs/1105.3115).

For this engine: estimate queue position from events observed since the order
was placed, skew quotes by inventory, simulate fills against the reconstructed
book (no real orders), and enforce self-trade prevention in the simulator.
Fees: Binance maker 7.5–10 bp (https://www.binance.com/en/fee/schedule),
Kraken maker from 40 bp (https://www.kraken.com/features/fee-schedule). The
engine models maker rebates/tiers as configuration, and the default is a
punishing 10 bp to stay honest.

### 4. Short-horizon classifier with leakage-free evaluation

Features: OFI at several windows, microprice deviation, depth slope, spread,
realized volatility, trade-sign runs. Labels: triple barrier (profit target and
stop both scaled by σ) over 1–10 s. Validation: purged K-fold with an embargo
at least as long as the label horizon (López de Prado, *Advances in Financial
Machine Learning*). Evidence that simple models are enough: a 2025
Bybit BTC/USDT study found well-preprocessed baselines match or beat DeepLOB
(https://arxiv.org/abs/2506.05764); a 2026 survey finds no architecture that
delivers persistent, cross-regime, capacity-aware net alpha
(https://arxiv.org/abs/2609.04917). Evaluate net of fees and always compare
against the microprice baseline.

### 5. Meta-labeling

The primary rule (OFI or microprice threshold) gives the side. A small
classifier predicts whether that candidate trade hits target before stop, and
its probability drives a threshold and a size. It is the cheapest way to cut
trade count and fee bleed, and it is validated with the same purged CV.

## Sampling and evaluation discipline

* Every backtest and every paper session reports gross and net PnL at 10 bp
  taker and 7.5 bp maker, plus slippage from walking the reconstructed book.
* No overlapping-label leakage: labels and features are cut at event time, and
  cross-validation purges any training sample whose label window overlaps the
  test window.
* Multiple-testing discipline: a strategy counts as promising only if its
  pre-registered test passes once; variants of failed tests are not run
  (see `docs/PLAN.md`).

## What not to bother with

* Colocated cross-venue latency arbitrage: WAN latency plus retail fees.
* Deep LOB models and untuned RL market making on the Pi: gains over simple
  models are small; fill models in simulators are optimistic.
* Taker-only OFI momentum at 1 s: pays ~20 bp round trip against a few-bp move.
* Kraken market making for a small account at 40 bp maker.
* Funding carry as the core strategy: the Pi adds nothing.
* Rule-mined technical indicators: the multiple-testing problem (Sullivan,
  Timmermann and White) makes most of them false discoveries.
