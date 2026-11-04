package experiment

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"piquant-live/internal/config"
	"piquant-live/internal/features"
	"piquant-live/internal/models/common"
	"piquant-live/internal/models/mlofi"
	"piquant-live/internal/replay"
)

// FeatureObservation captures features at one point in time and tracks future mid prices.
type FeatureObservation struct {
	Timestamp int64
	MidPrice  float64
	Features  [common.FeatureCount]float64
}

// Run executes an experiment deterministically and updates its fields.
func Run(ctx context.Context, exp *Experiment, cfg *config.Config, onProgress func(stage string, pct float64)) error {
	exp.StartedAt = time.Now()
	exp.State = StateRunning
	exp.Stage = "replay"
	exp.Progress = 0.05
	if onProgress != nil {
		onProgress(exp.Stage, exp.Progress)
	}

	rep, err := replay.Open(exp.Dataset, replay.Options{
		Config:   cfg,
		Speed:    0, // max speed
		MaxBooks: 16,
	})
	if err != nil {
		exp.State = StateFailed
		exp.Error = fmt.Sprintf("cannot open dataset %s: %v", exp.Dataset, err)
		return err
	}

	featEng := features.New(0)
	var obs []FeatureObservation
	featDst := make([]float32, features.NCore)

	// Phase 1: Replay & Feature generation
	count := 0
	for {
		if ctx.Err() != nil {
			exp.State = StateCancelled
			exp.Error = "experiment cancelled by user"
			return ctx.Err()
		}

		step, err := rep.Next()
		if errors.Is(err, io.EOF) || errors.Is(err, replay.ErrFinished) {
			break
		}
		if err != nil {
			continue // skip corrupted line
		}

		b := step.Book
		if b != nil && b.Ready {
			featEng.Advance(b, &step.Env)
			featEng.Extract(b, featDst)

			// Sample every 5 events or when mid changes
			midInt, ok := b.Mid()
			if ok && midInt > 0 && (count%5 == 0) {
				var f64 [common.FeatureCount]float64
				for i := 0; i < common.FeatureCount && i < len(featDst); i++ {
					f64[i] = float64(featDst[i])
				}
				obs = append(obs, FeatureObservation{
					Timestamp: step.Env.RecvTime,
					MidPrice:  float64(midInt),
					Features:  f64,
				})
			}
		}
		count++
		if count%500 == 0 && onProgress != nil {
			onProgress("replay", math.Min(0.40, 0.05+float64(count)*0.0001))
		}
	}

	if len(obs) < 50 {
		exp.State = StateFailed
		exp.Error = fmt.Sprintf("insufficient valid book observations (%d found, minimum 50 required)", len(obs))
		return errors.New(exp.Error)
	}

	exp.Stage = "labeling"
	exp.Progress = 0.45
	if onProgress != nil {
		onProgress(exp.Stage, exp.Progress)
	}

	// Phase 2: Compute forward mid returns (bps) at horizon H
	horizonNs := exp.Horizon.Duration().Nanoseconds()
	if horizonNs <= 0 {
		horizonNs = 30 * time.Second.Nanoseconds()
	}

	var samples []common.Sample
	nObs := len(obs)
	targetIdx := 0

	for i := 0; i < nObs; i++ {
		targetTime := obs[i].Timestamp + horizonNs
		for targetIdx < nObs && obs[targetIdx].Timestamp < targetTime {
			targetIdx++
		}
		if targetIdx >= nObs {
			break // Remaining observations cannot form a full forward horizon
		}

		futureMid := obs[targetIdx].MidPrice
		currentMid := obs[i].MidPrice
		if currentMid > 0 && futureMid > 0 {
			yBps := common.Bps(currentMid, futureMid)
			samples = append(samples, common.Sample{
				X: obs[i].Features,
				Y: yBps,
			})
		}
	}

	if len(samples) < 30 {
		exp.State = StateFailed
		exp.Error = fmt.Sprintf("insufficient forward labeled samples (%d found)", len(samples))
		return errors.New(exp.Error)
	}

	// Phase 3: Train / Val / Test Partitioning with Embargo Gaps
	embargoGap := int(math.Ceil(float64(len(samples)) * 0.02))
	if embargoGap < 2 {
		embargoGap = 2
	}

	nTotal := len(samples)
	nTrain := int(float64(nTotal) * exp.Split.TrainPct)
	nVal := int(float64(nTotal) * exp.Split.ValPct)

	trainEnd := nTrain
	valStart := trainEnd + embargoGap
	valEnd := valStart + nVal
	testStart := valEnd + embargoGap
	testEnd := nTotal

	if testStart >= testEnd || valStart >= valEnd {
		// Fallback without gap if dataset is compact
		trainEnd = int(float64(nTotal) * 0.60)
		valStart = trainEnd
		valEnd = valStart + int(float64(nTotal)*0.20)
		testStart = valEnd
		testEnd = nTotal
	}

	trainSamples := samples[0:trainEnd]
	valSamples := samples[valStart:valEnd]
	testSamples := samples[testStart:testEnd]

	exp.Stage = "fitting"
	exp.Progress = 0.60
	if onProgress != nil {
		onProgress(exp.Stage, exp.Progress)
	}

	// Parse feature set
	fset := mlofi.FeatureSet(exp.FeatureSet)
	if fset == "" {
		fset = mlofi.SetMulti
	}
	featIdx, err := mlofi.Features(fset)
	if err != nil {
		featIdx = []int{0, 1, 2, 3, 4, 5, 6}
	}

	lambda := 1e-3
	if l, ok := exp.Params["lambda"].(float64); ok && l > 0 {
		lambda = l
	}

	// Fit Ridge model on Train partition ONLY
	model := &common.Ridge{Lam: lambda}
	if err := model.Fit(trainSamples, featIdx); err != nil {
		exp.State = StateFailed
		exp.Error = fmt.Sprintf("model fit failed: %v", err)
		return err
	}

	exp.Stage = "evaluating"
	exp.Progress = 0.85
	if onProgress != nil {
		onProgress(exp.Stage, exp.Progress)
	}

	// Predict function
	evalSplit := func(split []common.Sample) ([]float64, []float64) {
		preds := make([]float64, len(split))
		actuals := make([]float64, len(split))
		for i, s := range split {
			preds[i] = model.Predict(&s.X)
			actuals[i] = s.Y
		}
		return preds, actuals
	}

	trainPred, trainAct := evalSplit(trainSamples)
	valPred, valAct := evalSplit(valSamples)
	testPred, testAct := evalSplit(testSamples)

	exp.TrainMetrics = ComputeMetrics(trainPred, trainAct)
	exp.ValMetrics = ComputeMetrics(valPred, valAct)
	exp.TestMetrics = ComputeMetrics(testPred, testAct)

	exp.OverfitWarning = CheckGeneralization(exp.TrainMetrics, exp.ValMetrics, exp.TestMetrics)

	exp.FinishedAt = time.Now()
	exp.DurationMs = exp.FinishedAt.Sub(exp.StartedAt).Milliseconds()
	exp.State = StateCompleted
	exp.Stage = "completed"
	exp.Progress = 1.0
	if onProgress != nil {
		onProgress(exp.Stage, exp.Progress)
	}

	return nil
}
