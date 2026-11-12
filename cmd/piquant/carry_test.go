package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"piquant-live/internal/carry"
	"piquant-live/internal/collect"
)

// fakeCarryServer serves November 2026 funding zips (0.01% three times a day) and flat daily candles; December's
// funding file is not published.
func fakeCarryServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "fundingRate-2026-11.zip"):
			var csv strings.Builder
			csv.WriteString("calc_time,funding_interval_hours,last_funding_rate\n")
			for ts := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC); ts.Month() == 11; ts = ts.Add(8 * time.Hour) {
				fmt.Fprintf(&csv, "%d,8,0.0001\n", ts.UnixMilli())
			}
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			f, _ := zw.Create("x.csv")
			_, _ = f.Write([]byte(csv.String()))
			_ = zw.Close()
			_, _ = w.Write(buf.Bytes())
		case strings.Contains(r.URL.Path, "/candles"):
			start, _ := time.Parse(time.RFC3339, r.URL.Query().Get("start"))
			end, _ := time.Parse(time.RFC3339, r.URL.Query().Get("end"))
			var rows [][]float64
			for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
				rows = append(rows, []float64{float64(d.Unix()), 100, 100, 100, 100, 1})
			}
			_ = json.NewEncoder(w).Encode(rows)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestAdvanceCarry(t *testing.T) {
	srv := fakeCarryServer(t)
	defer srv.Close()
	oldF, oldC := carry.FundingBase, collect.CoinbaseCandlesBase
	carry.FundingBase, collect.CoinbaseCandlesBase = srv.URL, srv.URL
	defer func() { carry.FundingBase, collect.CoinbaseCandlesBase = oldF, oldC }()
	path := filepath.Join(t.TempDir(), "carry_state.json")
	ctx := context.Background()

	// mid-November: the ledger opens, but the month is not complete
	st, n, err := advanceCarry(ctx, srv.Client(), path, time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC))
	if err != nil || n != 0 || st == nil || st.LastDate != carryRegistered {
		t.Fatalf("open: %+v %d %v", st, n, err)
	}
	// 2 December: November applies from the 12th (19 days); December is unpublished, so it stops there
	st, n, err = advanceCarry(ctx, srv.Client(), path, time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC))
	if err != nil || n != 19 || st.LastDate != "2026-11-30" {
		t.Fatalf("november: n=%d last=%s err=%v", n, st.LastDate, err)
	}
	if st.Days[0].Ret < 1.9e-4 || st.Days[0].Ret > 2.1e-4 { // 3 x 0.01% on N = E/1.5
		t.Fatalf("day ret %v", st.Days[0].Ret)
	}
	// running again adds nothing (idempotent), also in January while December is still missing
	if _, n, err = advanceCarry(ctx, srv.Client(), path, time.Date(2027, 1, 5, 0, 0, 0, 0, time.UTC)); err != nil || n != 0 {
		t.Fatalf("rerun: n=%d err=%v", n, err)
	}
}
