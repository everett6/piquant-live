// Package execution provides ping/latency profiling and latency-adapted order execution planning.
// It only performs read-only probes against public venue time endpoints and never connects to user accounts
// or submits live orders.
package execution

import (
	"context"
	"crypto/tls"
	"fmt"
	"math"
	"net/http"
	"net/http/httptrace"
	"sort"
	"time"
)

// ExchangeEndpoint identifies a public venue health/time URL safe for unauthenticated latency probing.
type ExchangeEndpoint struct {
	Venue string `json:"venue"`
	URL   string `json:"url"`
}

// DefaultEndpoints are the US-accessible venues relevant to PiQuant research and carry/spot execution.
var DefaultEndpoints = []ExchangeEndpoint{
	{Venue: "coinbase", URL: "https://api.exchange.coinbase.com/time"},
	{Venue: "binance_us", URL: "https://api.binance.us/api/v3/time"},
	{Venue: "kraken", URL: "https://api.kraken.com/0/public/Time"},
	{Venue: "deribit", URL: "https://www.deribit.com/api/v2/public/get_time"},
}

// VenueLatency summarizes measured network timing for one venue.
type VenueLatency struct {
	Venue        string  `json:"venue"`
	URL          string  `json:"url"`
	Samples      int     `json:"samples"`
	Successes    int     `json:"successes"`
	TCPMedianMs  float64 `json:"tcp_median_ms"`
	HTTPMedianMs float64 `json:"http_median_ms"`
	HTTPP95Ms    float64 `json:"http_p95_ms"`
	StatusCode   int     `json:"status_code"`
	LastError    string  `json:"last_error,omitempty"`
}

// PingExecutionPlan translates measured network latency into concrete strategy and execution guardrails.
type PingExecutionPlan struct {
	MeasuredAtUTC         string         `json:"measured_at_utc"`
	BestVenue             string         `json:"best_venue"`
	BestHTTPMedianMs      float64        `json:"best_http_median_ms"`
	WorstHTTPP95Ms        float64        `json:"worst_http_p95_ms"`
	CancelExposureMs      float64        `json:"cancel_exposure_ms"`
	SubSecondMakerViable  bool           `json:"sub_second_maker_viable"`
	RecommendedCadence    string         `json:"recommended_cadence"`
	StaleBookGuardMs      int            `json:"stale_book_guard_ms"`
	PostOnlyMakerTTLMs    int            `json:"post_only_maker_ttl_ms"`
	TWAPSliceCount        int            `json:"twap_slice_count"`
	TWAPSliceIntervalMs   int            `json:"twap_slice_interval_ms"`
	PrimaryExecutionMode  string         `json:"primary_execution_mode"`
	Guidance              string         `json:"guidance"`
	Venues                []VenueLatency `json:"venues"`
}

// ProbeEndpoints measures TCP connect and full HTTP round-trip latency for each endpoint.
func ProbeEndpoints(ctx context.Context, endpoints []ExchangeEndpoint, samples int, timeout time.Duration) []VenueLatency {
	if samples <= 0 {
		samples = 3
	}
	if timeout <= 0 {
		timeout = 4 * time.Second
	}
	out := make([]VenueLatency, 0, len(endpoints))
	for _, ep := range endpoints {
		out = append(out, probeOne(ctx, ep, samples, timeout))
	}
	return out
}

func probeOne(ctx context.Context, ep ExchangeEndpoint, samples int, timeout time.Duration) VenueLatency {
	res := VenueLatency{Venue: ep.Venue, URL: ep.URL, Samples: samples}
	tcpSamples := make([]float64, 0, samples)
	httpSamples := make([]float64, 0, samples)

	for i := 0; i < samples; i++ {
		if ctx.Err() != nil {
			res.LastError = ctx.Err().Error()
			break
		}
		var tcpStart, httpStart time.Time
		var tcpMs float64
		var sawTCP bool

		reqCtx, cancel := context.WithTimeout(ctx, timeout)
		trace := &httptrace.ClientTrace{
			ConnectStart: func(_, _ string) {
				tcpStart = time.Now()
			},
			ConnectDone: func(_, _ string, err error) {
				if err == nil && !tcpStart.IsZero() {
					tcpMs = float64(time.Since(tcpStart).Microseconds()) / 1000.0
					sawTCP = true
				}
			},
		}
		req, err := http.NewRequestWithContext(httptrace.WithClientTrace(reqCtx, trace), http.MethodGet, ep.URL, nil)
		if err != nil {
			cancel()
			res.LastError = err.Error()
			continue
		}
		tr := &http.Transport{
			DisableKeepAlives: true,
			TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		}
		client := &http.Client{Transport: tr, Timeout: timeout}
		httpStart = time.Now()
		resp, err := client.Do(req)
		elapsedMs := float64(time.Since(httpStart).Microseconds()) / 1000.0
		tr.CloseIdleConnections()
		cancel()
		if err != nil {
			res.LastError = err.Error()
			continue
		}
		_ = resp.Body.Close()
		res.StatusCode = resp.StatusCode
		res.Successes++
		httpSamples = append(httpSamples, elapsedMs)
		if sawTCP {
			tcpSamples = append(tcpSamples, tcpMs)
		}
	}

	res.TCPMedianMs = percentile(tcpSamples, 0.50)
	res.HTTPMedianMs = percentile(httpSamples, 0.50)
	res.HTTPP95Ms = percentile(httpSamples, 0.95)
	return res
}

// PlanForLatency computes execution parameters matched to the user's real round-trip ping.
func PlanForLatency(venues []VenueLatency, now time.Time) PingExecutionPlan {
	plan := PingExecutionPlan{
		MeasuredAtUTC: now.UTC().Format(time.RFC3339),
		Venues:        append([]VenueLatency(nil), venues...),
	}

	bestHTTP := math.Inf(1)
	worstP95 := 0.0
	for _, v := range venues {
		if v.Successes == 0 || v.HTTPMedianMs <= 0 {
			continue
		}
		if v.HTTPMedianMs < bestHTTP {
			bestHTTP = v.HTTPMedianMs
			plan.BestVenue = v.Venue
		}
		if v.HTTPP95Ms > worstP95 {
			worstP95 = v.HTTPP95Ms
		}
	}

	if math.IsInf(bestHTTP, 1) {
		bestHTTP = 250.0
		worstP95 = 350.0
		plan.BestVenue = "offline_fallback"
	} else if worstP95 < bestHTTP {
		worstP95 = bestHTTP
	}

	// Cancel exposure approximates one inbound tick travel + local decision + one cancel round-trip.
	cancelExposure := 1.5*worstP95 + 25.0
	plan.BestHTTPMedianMs = round2(bestHTTP)
	plan.WorstHTTPP95Ms = round2(worstP95)
	plan.CancelExposureMs = round2(cancelExposure)
	plan.SubSecondMakerViable = cancelExposure <= 30.0

	staleGuard := int(math.Max(150.0, math.Ceil(cancelExposure*1.5)))
	plan.StaleBookGuardMs = staleGuard

	switch {
	case plan.SubSecondMakerViable:
		plan.RecommendedCadence = "sub-second / L2 market making viable"
		plan.PostOnlyMakerTTLMs = 5000
		plan.TWAPSliceCount = 1
		plan.TWAPSliceIntervalMs = 0
		plan.PrimaryExecutionMode = "colocated_l2_maker"
		plan.Guidance = "Low colocated RTT (<30ms exposure); L2 queue making and fast pre-fill cancels are latency-competitive."
	case bestHTTP <= 250.0:
		plan.RecommendedCadence = "6h / daily-close & weekly-leader spot+carry"
		plan.PostOnlyMakerTTLMs = 60000
		plan.TWAPSliceCount = 4
		plan.TWAPSliceIntervalMs = int(math.Max(5000.0, math.Ceil(worstP95*20.0)))
		plan.PrimaryExecutionMode = "patient_post_only_twap_spot_plus_carry"
		plan.Guidance = fmt.Sprintf(
			"Home RTT ~%.0fms median (%.0fms cancel exposure): avoid 1ms HFT queue races; execute daily/6h trend & leader rotations via %dx patient post-only slices on US spot (2+1 bps, 0 funding) with %dms TTL.",
			bestHTTP, cancelExposure, plan.TWAPSliceCount, plan.PostOnlyMakerTTLMs,
		)
	default:
		plan.RecommendedCadence = "daily-close & weekly rotation only"
		plan.PostOnlyMakerTTLMs = 120000
		plan.TWAPSliceCount = 6
		plan.TWAPSliceIntervalMs = 15000
		plan.PrimaryExecutionMode = "slow_twap_daily_close"
		plan.Guidance = "High WAN latency (>250ms): widen stale-book guards and restrict execution strictly to daily/weekly post-only TWAP baskets."
	}

	return plan
}

func percentile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := append([]float64(nil), xs...)
	sort.Float64s(cp)
	pos := q * float64(len(cp)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return round2(cp[lo])
	}
	frac := pos - float64(lo)
	return round2(cp[lo]*(1-frac) + cp[hi]*frac)
}

func round2(v float64) float64 {
	return math.Round(v*100.0) / 100.0
}
