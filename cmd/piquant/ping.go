package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"time"

	"piquant-live/internal/execution"
)

func runPing(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ping", flag.ContinueOnError)
	samples := fs.Int("samples", 3, "number of fresh TCP+HTTP probes per venue")
	timeout := fs.Duration("timeout", 4*time.Second, "per-request timeout")
	jsonOut := fs.Bool("json", false, "print JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	venues := execution.ProbeEndpoints(ctx, execution.DefaultEndpoints, *samples, *timeout)
	plan := execution.PlanForLatency(venues, time.Now().UTC())
	if *jsonOut {
		b, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	fmt.Printf("PiQuant Ping & Latency Execution Plan (%s)\n", plan.MeasuredAtUTC)
	for _, v := range plan.Venues {
		if v.Successes == 0 {
			fmt.Printf("  %-11s FAILED (%s)\n", v.Venue, v.LastError)
			continue
		}
		fmt.Printf("  %-11s TCP median=%6.2fms | HTTP median=%6.2fms | p95=%6.2fms | status=%d (%d/%d)\n",
			v.Venue, v.TCPMedianMs, v.HTTPMedianMs, v.HTTPP95Ms, v.StatusCode, v.Successes, v.Samples)
	}
	fmt.Printf("\nBest venue: %s (%.2fms HTTP median, %.2fms cancel exposure)\n",
		plan.BestVenue, plan.BestHTTPMedianMs, plan.CancelExposureMs)
	fmt.Printf("Sub-second HFT L2 viable: %v\n", plan.SubSecondMakerViable)
	fmt.Printf("Recommended cadence:      %s\n", plan.RecommendedCadence)
	fmt.Printf("Execution mode:           %s (slices=%d every %dms, maker TTL=%dms, stale guard=%dms)\n",
		plan.PrimaryExecutionMode, plan.TWAPSliceCount, plan.TWAPSliceIntervalMs, plan.PostOnlyMakerTTLMs, plan.StaleBookGuardMs)
	fmt.Printf("Guidance: %s\n", plan.Guidance)
	return nil
}
