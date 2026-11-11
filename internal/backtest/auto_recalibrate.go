package backtest

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrNoTickData is returned when no tick partitions exist for the symbol, so no
// recalibration can be run. Callers must treat this as "nothing changed".
var ErrNoTickData = errors.New("no tick partitions found; recalibration skipped")

// RunAutonomousRecalibration executes a parallel grid search over recent tick
// partitions and persists the winning configuration for hot-reloading by the live
// engine. It writes nothing and returns an error when it has no data to search on.
func RunAutonomousRecalibration(ctx context.Context, symbol string, currentCapital float64, dataDir string) error {
	if currentCapital <= 0 {
		currentCapital = 200.00
	}
	if symbol == "" {
		symbol = "BTCUSDT"
	}
	sym := strings.ToUpper(symbol)

	// Search candidate data directories for binary tick partitions
	candidates := []string{"data/historical/ticks", "../../data/historical/ticks"}
	if dataDir != "" {
		candidates = append([]string{filepath.Join(dataDir, "ticks"), dataDir}, candidates...)
	}

	var foundFiles []string
	for _, dir := range candidates {
		matches, err := filepath.Glob(filepath.Join(dir, fmt.Sprintf("%s-*.pqbin", sym)))
		if err == nil && len(matches) > 0 {
			foundFiles = matches
			break
		}
	}
	if len(foundFiles) == 0 {
		return fmt.Errorf("%s: %w", sym, ErrNoTickData)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Use at most the latest 3 months for responsive calibration
	if len(foundFiles) > 3 {
		foundFiles = foundFiles[len(foundFiles)-3:]
	}
	partitions, err := LoadMonthlyPartitions(foundFiles)
	if err != nil {
		return fmt.Errorf("%s: load tick partitions: %w", sym, err)
	}
	if len(partitions) == 0 {
		return fmt.Errorf("%s: %w", sym, ErrNoTickData)
	}

	baseCfg := TickSimConfig{
		Symbol:             sym,
		InitialCapital:     currentCapital,
		MakerFeeBps:        1.50,
		TakerFeeBps:        4.0,
		SimulatedLatencyMs: 10.0,
		CancelLatencyMs:    5.0,
		BaseSpreadBps:      1.50,
		TargetProfitBps:    12.5,
		StopLossBps:        60.0,
		EnableMacroShield:  true,
	}
	leaderboard := RunParallelGridSearch(partitions, DefaultGridSearchCandidates(baseCfg))
	if len(leaderboard) == 0 {
		return fmt.Errorf("%s: grid search produced no candidates", sym)
	}
	if err := ctx.Err(); err != nil {
		return err // timed out: do not publish a result the caller has given up on
	}

	outPath := "data/reports/optimal_params.json"
	if dataDir != "" {
		outPath = filepath.Join(dataDir, "config", "optimal_params.json")
	}
	if err := SaveOptimalParams(outPath, leaderboard[0]); err != nil {
		return fmt.Errorf("%s: save params: %w", sym, err)
	}
	return nil
}
