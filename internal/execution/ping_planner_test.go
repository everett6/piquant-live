package execution

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPlanForLatencyHomePingProfile(t *testing.T) {
	venues := []VenueLatency{
		{Venue: "coinbase", Samples: 5, Successes: 5, TCPMedianMs: 51.4, HTTPMedianMs: 92.0, HTTPP95Ms: 120.0},
		{Venue: "kraken", Samples: 5, Successes: 5, TCPMedianMs: 62.6, HTTPMedianMs: 110.0, HTTPP95Ms: 145.0},
	}
	plan := PlanForLatency(venues, time.Date(2026, 11, 14, 22, 0, 0, 0, time.UTC))
	if plan.BestVenue != "coinbase" {
		t.Fatalf("expected best venue coinbase, got %s", plan.BestVenue)
	}
	if plan.SubSecondMakerViable {
		t.Fatalf("expected sub-second maker to be marked non-viable at home RTT, got true")
	}
	if plan.PrimaryExecutionMode != "patient_post_only_twap_spot_plus_carry" {
		t.Fatalf("unexpected mode: %s", plan.PrimaryExecutionMode)
	}
	if plan.TWAPSliceCount != 4 || plan.StaleBookGuardMs < 300 {
		t.Fatalf("unexpected execution params: slices=%d stale_guard=%d", plan.TWAPSliceCount, plan.StaleBookGuardMs)
	}
}

func TestPlanForLatencyColocatedProfile(t *testing.T) {
	venues := []VenueLatency{
		{Venue: "tokyo_colo", Samples: 3, Successes: 3, TCPMedianMs: 0.8, HTTPMedianMs: 1.5, HTTPP95Ms: 2.0},
	}
	plan := PlanForLatency(venues, time.Unix(1700000000, 0))
	if !plan.SubSecondMakerViable {
		t.Fatalf("expected colocated RTT to allow sub-second maker")
	}
	if plan.PrimaryExecutionMode != "colocated_l2_maker" {
		t.Fatalf("expected colocated mode, got %s", plan.PrimaryExecutionMode)
	}
}

func TestProbeEndpointsAgainstMockServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"iso":"2026-11-14T22:00:00Z"}`))
	}))
	defer srv.Close()

	res := ProbeEndpoints(context.Background(), []ExchangeEndpoint{{Venue: "mock", URL: srv.URL}}, 3, 2*time.Second)
	if len(res) != 1 || res[0].Successes != 3 || res[0].StatusCode != 200 {
		t.Fatalf("unexpected probe result: %+v", res)
	}
	if res[0].HTTPMedianMs < 3.0 {
		t.Fatalf("expected >=3ms HTTP latency on sleeping mock server, got %.2fms", res[0].HTTPMedianMs)
	}
}
