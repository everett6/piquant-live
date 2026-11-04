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

// ContinuousModel is the shared interface implemented by all regression and classification predictors.
type ContinuousModel interface {
	Fit(samples []common.Sample, idx []int) error
	Predict(x *[common.FeatureCount]float64) float64
}

type logisticWrapper struct {
	mod *common.Logistic
}

func (w *logisticWrapper) Fit(samples []common.Sample, idx []int) error {
	binSamples := make([]common.Sample, len(samples))
	for i, s := range samples {
		binSamples[i] = s
		if s.Y > 0 {
			binSamples[i].Y = 1
		} else {
			binSamples[i].Y = 0
		}
	}
	return w.mod.Fit(binSamples, idx)
}

func (w *logisticWrapper) Predict(x *[common.FeatureCount]float64) float64 {
	return w.mod.Logit(x) * 2.0
}

// InstantiateModel creates an unfitted predictor for the specified model family.
func InstantiateModel(modelName string, lambda float64, params map[string]any) ContinuousModel {
	switch modelName {
	case "elastic_net":
		alpha := 0.5
		if a, ok := params["alpha"].(float64); ok && a >= 0 && a <= 1 {
			alpha = a
		}
		return common.NewElasticNet(lambda, alpha)
	case "adaptive_rls":
		ff := 0.99
		if f, ok := params["forgetting_factor"].(float64); ok && f > 0.8 && f <= 1.0 {
			ff = f
		}
		return common.NewAdaptiveRLS(ff)
	case "queue_logistic":
		return &logisticWrapper{mod: common.NewLogistic(lambda)}
	default:
		return &common.Ridge{Lam: lambda}
	}
}

// ExtractSamples deterministically reads the dataset, calculates orderbook features, and computes forward horizon returns.
func ExtractSamples(ctx context.Context, datasetPath string, horizon common.Horizon, cfg *config.Config, onProgress func(pct float64)) ([]common.Sample, error) {
	rep, err := replay.Open(datasetPath, replay.Options{
		Config:   cfg,
		Speed:    0, // max speed
		MaxBooks: 16,
	})
	if err != nil {
		return nil, fmt.Errorf("cannot open dataset %s: %v", datasetPath, err)
	}

	featEng := features.New(0)
	obs := make([]FeatureObservation, 0, 4096)
	featDst := make([]float32, features.NCore)

	count := 0
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
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
			onProgress(math.Min(1.0, float64(count)*0.0002))
		}
	}

	if len(obs) < 50 {
		return nil, fmt.Errorf("insufficient valid book observations (%d found, minimum 50 required)", len(obs))
	}

	horizonNs := horizon.Duration().Nanoseconds()
	if horizonNs <= 0 {
		horizonNs = 30 * time.Second.Nanoseconds()
	}

	samples := make([]common.Sample, 0, len(obs))
	nObs := len(obs)
	targetIdx := 0

	for i := 0; i < nObs; i++ {
		targetTime := obs[i].Timestamp + horizonNs
		for targetIdx < nObs && obs[targetIdx].Timestamp < targetTime {
			targetIdx++
		}
		if targetIdx >= nObs {
			break
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
		return nil, fmt.Errorf("insufficient forward labeled samples (%d found, minimum 30 required)", len(samples))
	}

	return samples, nil
}

// PartitionSamples splits samples chronologically into train, val, and test slices with an embargo buffer.
func PartitionSamples(samples []common.Sample, split SplitConfig) (train, val, test []common.Sample, err error) {
	nTotal := len(samples)
	if nTotal < 30 {
		return nil, nil, nil, fmt.Errorf("insufficient forward labeled samples (%d found, minimum 30 required)", nTotal)
	}

	embargoGap := int(math.Ceil(float64(nTotal) * 0.02))
	if embargoGap < 2 {
		embargoGap = 2
	}

	trainPct := split.TrainPct
	valPct := split.ValPct
	if trainPct <= 0 || valPct <= 0 {
		trainPct = 0.60
		valPct = 0.20
	}

	nTrain := int(float64(nTotal) * trainPct)
	nVal := int(float64(nTotal) * valPct)

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

	return samples[0:trainEnd], samples[valStart:valEnd], samples[testStart:testEnd], nil
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

	samples, err := ExtractSamples(ctx, exp.Dataset, exp.Horizon, cfg, func(pct float64) {
		if onProgress != nil {
			onProgress("replay", 0.05+pct*0.40)
		}
	})
	if err != nil {
		exp.State = StateFailed
		exp.Error = err.Error()
		return err
	}

	exp.Stage = "partitioning"
	exp.Progress = 0.50
	if onProgress != nil {
		onProgress(exp.Stage, exp.Progress)
	}

	trainSamples, valSamples, testSamples, err := PartitionSamples(samples, exp.Split)
	if err != nil {
		exp.State = StateFailed
		exp.Error = err.Error()
		return err
	}

	exp.Stage = "fitting"
	exp.Progress = 0.65
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

	model := InstantiateModel(exp.Model, lambda, exp.Params)
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
