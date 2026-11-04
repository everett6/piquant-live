// Package experiment manages quantitative research experiments, model evaluation,
// parameter searches, and reproducibility guarantees for PiQuant.
package experiment

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"piquant-live/internal/models/common"
)

// State is the lifecycle state of an experiment or parameter search.
type State string

const (
	StateQueued      State = "queued"
	StateRunning     State = "running"
	StateCompleted   State = "completed"
	StateFailed      State = "failed"
	StateCancelled   State = "cancelled"
	StateInterrupted State = "interrupted"
)

// Metrics holds out-of-sample statistical evaluation results.
// Evaluator measures statistical forecasting quality, strictly separated from
// real-world trading profitability.
type Metrics struct {
	Samples             int                `json:"samples"`
	MAE                 float64            `json:"mae"`                  // Mean Absolute Error (bps)
	RMSE                float64            `json:"rmse"`                 // Root Mean Squared Error (bps)
	DirectionalAccuracy float64            `json:"directional_accuracy"` // Hit rate [0, 1]
	RankCorrelation     float64            `json:"rank_correlation"`     // Spearman's rho [-1, 1]
	BrierScore          float64            `json:"brier_score"`          // Probability calibration [0, 1]
	MeanForecast        float64            `json:"mean_forecast"`        // Mean predicted move (bps)
	MeanRealized        float64            `json:"mean_realized"`        // Mean actual move (bps)
	Baselines           map[string]float64 `json:"baselines"`            // Baseline MAE: zero, last-return, mean
}

// SplitConfig defines chronological, non-overlapping dataset partitions.
// An embargo gap is enforced between partitions to prevent label lookahead leakage.
type SplitConfig struct {
	TrainPct      float64       `json:"train_pct"`      // e.g. 0.60 (60%)
	ValPct        float64       `json:"val_pct"`        // e.g. 0.20 (20%)
	TestPct       float64       `json:"test_pct"`       // e.g. 0.20 (20%)
	EmbargoWindow time.Duration `json:"embargo_window"` // Gap between splits >= forecast horizon
}

// DefaultSplit returns the standard safe 60/20/20 train/val/test split with 1m embargo.
func DefaultSplit(h common.Horizon) SplitConfig {
	embargo := h.Duration()
	if embargo < time.Minute {
		embargo = time.Minute
	}
	return SplitConfig{
		TrainPct:      0.60,
		ValPct:        0.20,
		TestPct:       0.20,
		EmbargoWindow: embargo,
	}
}

// Validate checks split proportions.
func (s SplitConfig) Validate() error {
	sum := s.TrainPct + s.ValPct + s.TestPct
	if math.Abs(sum-1.0) > 1e-4 {
		return fmt.Errorf("split percentages must sum to 1.0 (got %.3f)", sum)
	}
	if s.TrainPct < 0.20 {
		return errors.New("training split must be at least 20%")
	}
	if s.ValPct < 0.10 {
		return errors.New("validation split must be at least 10%")
	}
	if s.TestPct < 0.10 {
		return errors.New("held-out test split must be at least 10%")
	}
	return nil
}

// Experiment defines a single historical model evaluation job.
type Experiment struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Model       string         `json:"model"`       // e.g. "mlofi_ridge", "mlofi_logistic"
	FeatureSet  string         `json:"feature_set"` // e.g. "L1", "L5", "L20", "multi"
	Dataset     string         `json:"dataset"`     // path or name of dataset
	Horizon     common.Horizon `json:"horizon"`     // H30s, H1m, H5m
	Symbol      string         `json:"symbol"`      // e.g. "BTCUSDT"
	Params      map[string]any `json:"params"`      // hyperparameters (lambda, etc.)
	Split       SplitConfig    `json:"split"`
	State       State          `json:"state"`
	Progress    float64        `json:"progress"` // 0.0 to 1.0
	Stage       string         `json:"stage"`    // "replay", "feature_gen", "train", "val", "test"
	CreatedAt   time.Time      `json:"created_at"`
	StartedAt   time.Time      `json:"started_at,omitempty"`
	FinishedAt  time.Time      `json:"finished_at,omitempty"`
	DurationMs  int64          `json:"duration_ms"`
	WorkerNode  string         `json:"worker_node"` // "pi5" or "pi3b"

	// Results: Train, Validation, and Test metrics are strictly segregated.
	TrainMetrics Metrics `json:"train_metrics"`
	ValMetrics   Metrics `json:"val_metrics"`
	TestMetrics  Metrics `json:"test_metrics"`

	// GeneralizationDiagnostic warns when validation or test metrics diverge significantly from train.
	OverfitWarning string `json:"overfit_warning,omitempty"`
	Error          string `json:"error,omitempty"`
}

// ComputeMetrics evaluates predicted vs actual basis points changes.
func ComputeMetrics(predicted, actual []float64) Metrics {
	n := len(predicted)
	if n == 0 || len(actual) != n {
		return Metrics{}
	}

	var sumAE, sumSE float64
	var sumPred, sumActual float64
	var correctDirection int
	var brierSum float64

	// Baseline sums
	var baselineZeroAE, baselineZeroSE float64
	var baselineMeanAE, baselineMeanSE float64
	for _, a := range actual {
		sumActual += a
	}
	meanActual := sumActual / float64(n)

	for i := 0; i < n; i++ {
		p := predicted[i]
		a := actual[i]

		err := p - a
		ae := math.Abs(err)
		se := err * err

		sumAE += ae
		sumSE += se
		sumPred += p

		// Directional hit rate
		if (p >= 0 && a >= 0) || (p < 0 && a < 0) {
			correctDirection++
		}

		// Probability calibration proxy: map bps to [0, 1] probability of upward move
		probUp := 1.0 / (1.0 + math.Exp(-p*0.5))
		actualUp := 0.0
		if a > 0 {
			actualUp = 1.0
		}
		brierSum += (probUp - actualUp) * (probUp - actualUp)

		// Baseline: zero
		baselineZeroAE += math.Abs(a)
		baselineZeroSE += a * a

		// Baseline: mean
		baselineMeanAE += math.Abs(meanActual - a)
		baselineMeanSE += (meanActual - a) * (meanActual - a)
	}

	mae := sumAE / float64(n)
	rmse := math.Sqrt(sumSE / float64(n))
	dirAcc := float64(correctDirection) / float64(n)
	brier := brierSum / float64(n)
	rho := spearmanRankCorrelation(predicted, actual)

	return Metrics{
		Samples:             n,
		MAE:                 mae,
		RMSE:                rmse,
		DirectionalAccuracy: dirAcc,
		RankCorrelation:     rho,
		BrierScore:          brier,
		MeanForecast:        sumPred / float64(n),
		MeanRealized:        meanActual,
		Baselines: map[string]float64{
			"zero_mae":  baselineZeroAE / float64(n),
			"zero_rmse": math.Sqrt(baselineZeroSE / float64(n)),
			"mean_mae":  baselineMeanAE / float64(n),
			"mean_rmse": math.Sqrt(baselineMeanSE / float64(n)),
		},
	}
}

// spearmanRankCorrelation calculates the rank correlation between two vectors.
func spearmanRankCorrelation(x, y []float64) float64 {
	n := len(x)
	if n < 2 {
		return 0
	}
	rx := rank(x)
	ry := rank(y)

	var d2 float64
	for i := 0; i < n; i++ {
		d := rx[i] - ry[i]
		d2 += d * d
	}
	return 1.0 - (6.0*d2)/(float64(n)*(float64(n*n)-1.0))
}

type rankItem struct {
	val float64
	idx int
}

func rank(v []float64) []float64 {
	n := len(v)
	items := make([]rankItem, n)
	for i := 0; i < n; i++ {
		items[i] = rankItem{val: v[i], idx: i}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].val < items[j].val
	})
	ranks := make([]float64, n)
	for i := 0; i < n; {
		j := i + 1
		for j < n && items[j].val == items[i].val {
			j++
		}
		avgRank := 1.0 + float64(i+j-1)/2.0
		for k := i; k < j; k++ {
			ranks[items[k].idx] = avgRank
		}
		i = j
	}
	return ranks
}

// CheckGeneralization compares train, validation, and test metrics and produces
// an actionable diagnostic if signs of severe overfitting or failure to generalize occur.
func CheckGeneralization(train, val, test Metrics) string {
	if train.Samples == 0 || val.Samples == 0 {
		return ""
	}
	// If validation MAE is more than 35% worse than training MAE
	if val.MAE > train.MAE*1.35 {
		return fmt.Sprintf("High validation degradation (val MAE %.2f bps vs train MAE %.2f bps). Model may be overfitting training noise.", val.MAE, train.MAE)
	}
	// If directional accuracy drops below random on validation
	if train.DirectionalAccuracy > 0.55 && val.DirectionalAccuracy < 0.49 {
		return fmt.Sprintf("Directional accuracy collapse: train %.1f%% vs val %.1f%%. Signal fails to generalize out of sample.", train.DirectionalAccuracy*100, val.DirectionalAccuracy*100)
	}
	// If rank correlation is negative on validation
	if train.RankCorrelation > 0.10 && val.RankCorrelation < -0.05 {
		return fmt.Sprintf("Negative rank correlation on validation (rho = %.2f). Inverting signal out of sample.", val.RankCorrelation)
	}
	return ""
}
