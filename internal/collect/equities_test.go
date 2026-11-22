package collect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRefreshEquitiesCSVs(t *testing.T) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	dMinus3 := today.AddDate(0, 0, -3)
	dMinus2 := today.AddDate(0, 0, -2)
	dMinus1 := today.AddDate(0, 0, -1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Include 1 existing date, 2 strictly newer completed dates (1 with null open fallback), and today's incomplete bar.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"chart": map[string]any{
				"result": []map[string]any{
					{
						"timestamp": []int64{dMinus3.Unix(), dMinus2.Unix(), dMinus1.Unix(), today.Add(2 * time.Hour).Unix()},
						"indicators": map[string]any{
							"quote": []map[string]any{
								{
									"open":   []any{100.0, 101.5, nil, 105.0},
									"high":   []any{102.0, 103.0, 104.5, 106.0},
									"low":    []any{99.5, 101.0, 102.0, 104.0},
									"close":  []any{101.0, 102.5, 104.0, 105.5},
									"volume": []any{1000, 1200, 1500, 500},
								},
							},
						},
					},
				},
			},
		})
	}))
	defer srv.Close()

	origBase := YahooChartBase
	YahooChartBase = srv.URL
	defer func() { YahooChartBase = origBase }()

	dir := t.TempDir()
	// Pre-seed SPY with dMinus3 so only dMinus2 and dMinus1 are appended (today's incomplete bar must be skipped).
	seed := "date,open,high,low,close,volume\n" + dMinus3.Format("2006-01-02") + " 00:00:00+00:00,100,102,99.5,101,1000\n"
	if err := os.WriteFile(filepath.Join(dir, "SPY.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	added1, err := RefreshEquitiesCSVs(context.Background(), srv.Client(), dir, []string{"SPY", "GLD"})
	if err != nil {
		t.Fatalf("RefreshEquitiesCSVs failed: %v", err)
	}
	// SPY appends 2 newer completed dates; GLD creates file with all 3 completed dates -> total 5.
	if added1 != 5 {
		t.Fatalf("expected 5 appended rows across SPY(2)+GLD(3), got %d", added1)
	}

	// Second call must be strictly idempotent (0 new rows).
	added2, err := RefreshEquitiesCSVs(context.Background(), srv.Client(), dir, []string{"SPY", "GLD"})
	if err != nil || added2 != 0 {
		t.Fatalf("expected idempotent 0 added rows, got added=%d err=%v", added2, err)
	}

	spyBytes, err := os.ReadFile(filepath.Join(dir, "SPY.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(spyBytes), today.Format("2006-01-02")) {
		t.Fatalf("incomplete current UTC day leaked into SPY.csv:\n%s", string(spyBytes))
	}
}
