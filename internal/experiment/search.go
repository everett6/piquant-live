package experiment

import (
	"context"
	"fmt"
	"math"
	"time"

	"piquant-live/internal/config"
	"piquant-live/internal/models/common"
	"piquant-live/internal/models/mlofi"
)

// SearchTrial represents one evaluation in a parameter search.
type SearchTrial struct {
	TrialID    int            `json:"trial_id"`
	Params     map[string]any `json:"params"`
	FeatureSet string         `json:"feature_set"`
	Lambda     float64        `json:"lambda"`
	ValMAE     float64        `json:"val_mae"`
	ValRMSE    float64        `json:"val_rmse"`
	ValDirAcc  float64        `json:"val_dir_acc"`
	ValRho     float64        `json:"val_rho"`
	DurationMs int64          `json:"duration_ms"`
}

// ParameterSearch holds a hyperparameter exploration experiment.
type ParameterSearch struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Model       string         `json:"model"`
	Dataset     string         `json:"dataset"`
	Horizon     common.Horizon `json:"horizon"`
	Symbol      string         `json:"symbol"`
	State       State          `json:"state"`
	Progress    float64        `json:"progress"`
	Trials      []SearchTrial  `json:"trials"`
	BestTrial   *SearchTrial   `json:"best_trial,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	StartedAt   time.Time      `json:"started_at,omitempty"`
	FinishedAt  time.Time      `json:"finished_at,omitempty"`
	DurationMs  int64          `json:"duration_ms"`
	TestMetrics *Metrics       `json:"test_metrics,omitempty"` // Computed ONCE on the best trial
	Error       string         `json:"error,omitempty"`
}

// SearchSpace defines the parameter grid.
type SearchSpace struct {
	FeatureSets []string  `json:"feature_sets"`
	Lambdas     []float64 `json:"lambdas"`
}

// DefaultSearchSpace provides safe, standard hyperparameter defaults.
func DefaultSearchSpace() SearchSpace {
	return SearchSpace{
		FeatureSets: []string{string(mlofi.SetL1), string(mlofi.SetL5), string(mlofi.SetL20), string(mlofi.SetMulti)},
		Lambdas:     []float64{1e-4, 1e-3, 1e-2, 1e-1, 1.0},
	}
}

// RunSearch executes a parameter search across the search space.
// It rigorously selects the best configuration based ONLY on validation performance.
// Held-out test performance is evaluated exclusively on the selected candidate.
func RunSearch(ctx context.Context, ps *ParameterSearch, space SearchSpace, cfg *config.Config, onProgress func(pct float64)) error {
	ps.StartedAt = time.Now()
	ps.State = StateRunning
	ps.Progress = 0.0

	if len(space.FeatureSets) == 0 {
		space.FeatureSets = []string{string(mlofi.SetL5), string(mlofi.SetMulti)}
	}
	if len(space.Lambdas) == 0 {
		space.Lambdas = []float64{1e-3, 1e-2, 1e-1}
	}

	totalCombinations := len(space.FeatureSets) * len(space.Lambdas)
	trialIdx := 0

	var bestTrial *SearchTrial
	bestScore := math.MaxFloat64 // Lower Val MAE is better

	for _, fset := range space.FeatureSets {
		for _, lam := range space.Lambdas {
			if ctx.Err() != nil {
				ps.State = StateCancelled
				ps.Error = "search cancelled by user"
				return ctx.Err()
			}

			trialIdx++
			trialStart := time.Now()

			// Create trial experiment
			exp := &Experiment{
				ID:         fmt.Sprintf("%s-t%d", ps.ID, trialIdx),
				Name:       fmt.Sprintf("%s trial %d", ps.Name, trialIdx),
				Model:      ps.Model,
				FeatureSet: fset,
				Dataset:    ps.Dataset,
				Horizon:    ps.Horizon,
				Symbol:     ps.Symbol,
				Params:     map[string]any{"lambda": lam},
				Split:      DefaultSplit(ps.Horizon),
			}

			err := Run(ctx, exp, cfg, nil)
			trialDur := time.Since(trialStart).Milliseconds()
			if err != nil {
				continue // Skip failed trial
			}

			trial := SearchTrial{
				TrialID:    trialIdx,
				Params:     map[string]any{"lambda": lam, "feature_set": fset},
				FeatureSet: fset,
				Lambda:     lam,
				ValMAE:     exp.ValMetrics.MAE,
				ValRMSE:    exp.ValMetrics.RMSE,
				ValDirAcc:  exp.ValMetrics.DirectionalAccuracy,
				ValRho:     exp.ValMetrics.RankCorrelation,
				DurationMs: trialDur,
			}
			ps.Trials = append(ps.Trials, trial)

			// Selection criterion: validation MAE (held-out test is NOT touched)
			if trial.ValMAE > 0 && trial.ValMAE < bestScore {
				bestScore = trial.ValMAE
				currentTrial := trial
				bestTrial = &currentTrial
			}

			ps.Progress = float64(trialIdx) / float64(totalCombinations)
			if onProgress != nil {
				onProgress(ps.Progress)
			}
		}
	}

	if bestTrial == nil {
		ps.State = StateFailed
		ps.Error = "no trials succeeded"
		return fmt.Errorf("search failed: %s", ps.Error)
	}

	ps.BestTrial = bestTrial

	// Phase 2: Final single evaluation of the selected best configuration on held-out test data
	finalExp := &Experiment{
		ID:         fmt.Sprintf("%s-best", ps.ID),
		Name:       fmt.Sprintf("%s Best Model", ps.Name),
		Model:      ps.Model,
		FeatureSet: bestTrial.FeatureSet,
		Dataset:    ps.Dataset,
		Horizon:    ps.Horizon,
		Symbol:     ps.Symbol,
		Params:     map[string]any{"lambda": bestTrial.Lambda},
		Split:      DefaultSplit(ps.Horizon),
	}
	if err := Run(ctx, finalExp, cfg, nil); err == nil {
		ps.TestMetrics = &finalExp.TestMetrics
	}

	ps.FinishedAt = time.Now()
	ps.DurationMs = ps.FinishedAt.Sub(ps.StartedAt).Milliseconds()
	ps.State = StateCompleted
	ps.Progress = 1.0

	return nil
}
