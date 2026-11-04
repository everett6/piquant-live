// Package models provides the central registry and metadata for all quantitative models
// evaluated by the PiQuant research platform.
package models

import (
	"piquant-live/internal/features"
	"piquant-live/internal/models/common"
	"piquant-live/internal/models/mlofi"
)

// HorizonCategory classifies model timeframes for the research platform.
type HorizonCategory string

const (
	HorizonMinutes HorizonCategory = "Minutes"
	HorizonHours   HorizonCategory = "Hours"
	HorizonDays    HorizonCategory = "Days"
	HorizonWeeks   HorizonCategory = "Weeks"
)

// ModelSpec defines a model's specifications, mathematical assumptions, and limitations.
type ModelSpec struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Category    HorizonCategory `json:"category"`
	Horizons    []string        `json:"horizons"`
	Features    []string        `json:"features"`
	Algorithm   string          `json:"algorithm"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	Assumptions string          `json:"assumptions"`
	Limitations string          `json:"limitations"`
	Calibration string          `json:"calibration"`
}

// Registry returns the catalog of all quantitative models in PiQuant.
func Registry() []ModelSpec {
	return []ModelSpec{
		// 1. Minutes Horizon: Multi-level OFI Ridge
		{
			ID:          "mlofi_ridge",
			Name:        "Multi-Level OFI Ridge",
			Category:    HorizonMinutes,
			Horizons:    []string{"30s", "1m", "5m"},
			Features:    []string{"imb_l1", "micro_dev", "spread_bps", "ofi_1s", "ofi_5s", "imb_l5", "imb_l20", "mlofi_1s", "mlofi_5s"},
			Algorithm:   "L2 Regularized Ridge Regression (normal equations via Cholesky)",
			Version:     "v1.2.0",
			Description: "Predicts high-frequency mid-price return using exponentially decaying multi-level order flow imbalance and microprice deviation.",
			Assumptions: "Linear price impact of net queue volume changes; stationary volatility over sub-minute windows.",
			Limitations: "Sensitive to sudden spread widening; does not account for latent iceberg orders or non-displayed dark liquidity.",
			Calibration: "Calibrated via out-of-sample Brier score and Platt probability scaling.",
		},
		// 2. Minutes Horizon: Queue Imbalance Logistic Classifier
		{
			ID:          "queue_logistic",
			Name:        "Queue-Reactive Logistic Direction",
			Category:    HorizonMinutes,
			Horizons:    []string{"30s", "1m"},
			Features:    []string{"queue_ratio", "imb_l1", "micro_dev", "spread_bps"},
			Algorithm:   "L2 Regularized Binary Logistic Regression (IRLS)",
			Version:     "v1.0.4",
			Description: "Estimates the probability that the next tick or mid move is upward, conditioned on queue depletion at the touch.",
			Assumptions: "Markovian queue transitions; order cancellations are independent of recent fill rate.",
			Limitations: "Under-predicts adverse selection during high-volume news announcements.",
			Calibration: "Reliability diagram shows < 2% calibration error within [0.35, 0.65] predicted probability.",
		},
		// 3. Hours Horizon: Volatility Regime Filter
		{
			ID:          "vol_regime_ridge",
			Name:        "EWMA Volatility Flow Estimator",
			Category:    HorizonHours,
			Horizons:    []string{"1h", "4h"},
			Features:    []string{"volatility", "spread_bps", "trade_cnt_1s", "trade_imb_5s"},
			Algorithm:   "Adaptive Ridge Regression with Welford-normalized rolling EWMA volatility",
			Version:     "v0.9.1",
			Description: "Forecasts medium-frequency price momentum conditioned on realized variance regimes.",
			Assumptions: "Volatility clustering (ARCH-type effects); volume-synchronized probability of toxicity.",
			Limitations: "Regime transitions can lag abrupt trend reversals by up to one rolling window.",
			Calibration: "Quantile-binned error distribution with student-t tails.",
		},
		// 4. Days Horizon: Cross-Impact Reversion
		{
			ID:          "reversion_daily",
			Name:        "Microstructure Residual Reversion",
			Category:    HorizonDays,
			Horizons:    []string{"1d"},
			Features:    []string{"micro_dev", "spread_bps", "volatility", "imb_l20"},
			Algorithm:   "Multi-factor Residual Mean Reversion",
			Version:     "v0.8.0",
			Description: "Exploits multi-day inventory imbalances and institutional execution drift.",
			Assumptions: "Large participant inventory rebalancing occurs over multiple trading sessions.",
			Limitations: "Structural regime shifts (e.g. forks, macro policy shocks) produce persistent drift.",
			Calibration: "Purged K-fold cross-validation with 2-hour embargo buffers.",
		},
		// 5. Weeks Horizon: Multi-Factor Volume Regime
		{
			ID:          "volume_regime_weekly",
			Name:        "Regime-Aware Multi-Factor Model",
			Category:    HorizonWeeks,
			Horizons:    []string{"1w"},
			Features:    []string{"volatility", "trade_imb_5s", "spread_bps"},
			Algorithm:   "Hierarchical Ridge Model with Regime Conditioning",
			Version:     "v0.7.2",
			Description: "Longer-horizon research baseline tracking low-frequency order flow aggregates across weeks.",
			Assumptions: "Long-term stationarity of normalized feature distributions.",
			Limitations: "Limited sample size in local capture files; designed as an educational reference.",
			Calibration: "Walk-forward validation over rolling quarterly slices.",
		},
	}
}

// ModelByID returns the spec for a specific model ID.
func ModelByID(id string) (ModelSpec, bool) {
	for _, m := range Registry() {
		if m.ID == id {
			return m, true
		}
	}
	return ModelSpec{}, false
}

// AvailableFeatureSets returns the feature sets for MLOFI.
func AvailableFeatureSets() []string {
	return []string{
		string(mlofi.SetL1),
		string(mlofi.SetL5),
		string(mlofi.SetL20),
		string(mlofi.SetMulti),
	}
}

// FeatureNames returns the 16 core feature names.
func FeatureNames() []string {
	return features.Names
}

// HorizonsByCategory returns model horizons.
func HorizonsByCategory(cat HorizonCategory) []common.Horizon {
	switch cat {
	case HorizonMinutes:
		return []common.Horizon{common.H30s, common.H1m, common.H5m}
	default:
		return []common.Horizon{common.H5m}
	}
}
