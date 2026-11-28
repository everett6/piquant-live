package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"piquant-live/internal/carry"
	"piquant-live/internal/trend"
)

func TestHandlePaperTrendBlend(t *testing.T) {
	dir := t.TempDir()
	trendDir := filepath.Join(dir, "trend")
	carryDir := filepath.Join(dir, "carry")
	dtb3Path := filepath.Join(dir, "DTB3.csv")

	srv, err := New(Options{
		Token:           testToken,
		PeerToken:       peerToken,
		TrendDir:        trendDir,
		CarryDir:        carryDir,
		DTB3Path:        dtb3Path,
		DisableLivePing: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	authHdr := map[string]string{"Authorization": "Bearer " + peerToken}

	// 1. Unauthenticated -> 401
	if rec := get(t, h, http.MethodGet, "/api/paper/trend-blend", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	// 2. Uninitialized -> 200 status=uninitialized
	rec0 := get(t, h, http.MethodGet, "/api/paper/trend-blend", authHdr, nil)
	if rec0.Code != http.StatusOK {
		t.Fatalf("expected 200 on uninitialized, got %d: %s", rec0.Code, rec0.Body.String())
	}
	var body0 map[string]any
	if err := json.Unmarshal(rec0.Body.Bytes(), &body0); err != nil {
		t.Fatal(err)
	}
	if body0["status"] != "uninitialized" {
		t.Fatalf("expected status=uninitialized, got %v", body0["status"])
	}

	// 3. Seed trend, carry, and DTB3 -> 200 status=ok with S36_qd_carry_blend
	btcHist := make([]trend.Day, 0, 50)
	ethHist := make([]trend.Day, 0, 50)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 50; i++ {
		d := t0.AddDate(0, 0, i).Format("2006-01-02")
		btcHist = append(btcHist, trend.Day{Date: d, Close: 100 + float64(i)})
		ethHist = append(ethHist, trend.Day{Date: d, Close: 50 + float64(i)*0.5})
	}
	st := trend.NewState(btcHist[49].Date)
	st.Seed(btcHist)
	st.SeedETH(ethHist)
	fwdBTC := make([]trend.Day, 0, 5)
	fwdETH := make([]trend.Day, 0, 5)
	for i := 50; i < 55; i++ {
		d := t0.AddDate(0, 0, i).Format("2006-01-02")
		fwdBTC = append(fwdBTC, trend.Day{Date: d, Close: 100 + float64(i)*2})
		fwdETH = append(fwdETH, trend.Day{Date: d, Close: 50 + float64(i)})
	}
	st.Advance(fwdBTC)
	st.AdvanceETH(fwdETH)
	if err := st.Save(filepath.Join(trendDir, "trend_state.json")); err != nil {
		t.Fatal(err)
	}

	cSt := &carry.State{
		Days: []carry.DayRec{
			{Date: fwdBTC[0].Date, Ret: 0.0005},
			{Date: fwdBTC[1].Date, Ret: 0.0005},
		},
	}
	if err := os.MkdirAll(carryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cBytes, _ := json.Marshal(cSt)
	if err := os.WriteFile(filepath.Join(carryDir, "carry_state.json"), cBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dtb3Path, []byte("date,rate\n2026-02-18,4.12\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec1 := get(t, h, http.MethodGet, "/api/paper/trend-blend", authHdr, nil)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec1.Code, rec1.Body.String())
	}
	var res struct {
		Status    string               `json:"status"`
		Scorecard []trend.Summary      `json:"scorecard"`
		Blend     []trend.BlendSummary `json:"s36_qd_carry_blend"`
		Q42Blends []trend.BlendSummary `json:"q42_expected_dd_blends"`
	}
	if err := json.Unmarshal(rec1.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" || len(res.Scorecard) < 12 || len(res.Blend) != 2 || len(res.Q42Blends) != 2 {
		t.Fatalf("unexpected response: %+v", res)
	}
	if res.Blend[0].LatestDTB3Pct != 4.12 || res.Blend[0].CarryMatchedDays != 2 {
		t.Fatalf("unexpected blend metrics: %+v", res.Blend[0])
	}
	if res.Q42Blends[0].Name != "Q42_dualspeed_60_40_carry_spot" || res.Q42Blends[1].Name != "Q42_balanced_50_50_carry_spot" {
		t.Fatalf("unexpected Q42 blend names: %+v", res.Q42Blends)
	}

	// 4. Schwab plan preview endpoint (auth + offline fallback + selector switching)
	if rec := get(t, h, http.MethodGet, "/api/paper/schwab-plan", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on schwab-plan unauth, got %d", rec.Code)
	}
	recSw := get(t, h, http.MethodGet, "/api/paper/schwab-plan?portfolio=q42_dualspeed&nav=2000", authHdr, nil)
	if recSw.Code != http.StatusOK {
		t.Fatalf("expected 200 on schwab-plan, got %d: %s", recSw.Code, recSw.Body.String())
	}
	var swBody struct {
		Status string `json:"status"`
		Schwab struct {
			Portfolio      string  `json:"portfolio"`
			NavUSD         float64 `json:"nav_usd"`
			CarrySleevePct float64 `json:"carry_sleeve_pct"`
			Allocations    []struct {
				Symbol    string  `json:"symbol"`
				TargetUSD float64 `json:"target_usd"`
			} `json:"allocations"`
		} `json:"schwab"`
	}
	if err := json.Unmarshal(recSw.Body.Bytes(), &swBody); err != nil {
		t.Fatal(err)
	}
	if swBody.Status != "ok" || swBody.Schwab.Portfolio != "Q42_dualspeed_60_40_carry_spot" || swBody.Schwab.NavUSD != 2000 || swBody.Schwab.CarrySleevePct != 40.0 || len(swBody.Schwab.Allocations) != 3 {
		t.Fatalf("unexpected schwab-plan payload: %+v", swBody)
	}

	// 5. Alpha-decay self-improving loop endpoint
	recAD := get(t, h, http.MethodGet, "/api/paper/alpha-decay", authHdr, nil)
	if recAD.Code != http.StatusOK {
		t.Fatalf("expected 200 on alpha-decay, got %d: %s", recAD.Code, recAD.Body.String())
	}
	var adBody struct {
		Status string `json:"status"`
		Loop   struct {
			DaysProcessed int                `json:"days_processed"`
			Weights       map[string]float64 `json:"weights"`
		} `json:"self_improving_loop"`
	}
	if err := json.Unmarshal(recAD.Body.Bytes(), &adBody); err != nil {
		t.Fatal(err)
	}
	if adBody.Status != "ok" || adBody.Loop.DaysProcessed != 5 || len(adBody.Loop.Weights) != 4 {
		t.Fatalf("unexpected alpha-decay response: %+v", adBody)
	}
}

func TestTrendMtimeCacheHitAndInvalidation(t *testing.T) {
	dir := t.TempDir()
	trendDir := filepath.Join(dir, "trend")
	carryDir := filepath.Join(dir, "carry")
	eqDir := filepath.Join(dir, "equities")
	dtb3Path := filepath.Join(dir, "DTB3.csv")
	if err := os.MkdirAll(eqDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Build 90 days of synthetic BTC/ETH/SOL + SPY/TLT CSVs for full-fund macro HRP cache test.
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	btcHist := make([]trend.Day, 0, 75)
	ethHist := make([]trend.Day, 0, 75)
	solHist := make([]trend.Day, 0, 75)
	var spyCSV, tltCSV strings.Builder
	spyCSV.WriteString("date,open,high,low,close,volume\n")
	tltCSV.WriteString("date,open,high,low,close,volume\n")
	for i := 0; i < 75; i++ {
		d := t0.AddDate(0, 0, i).Format("2006-01-02")
		cB := 100.0 + float64(i)*1.5
		cE := 50.0 + float64(i)*0.8
		cS := 20.0 + float64(i)*0.4
		btcHist = append(btcHist, trend.Day{Date: d, Open: cB, High: cB * 1.01, Low: cB * 0.99, Close: cB})
		ethHist = append(ethHist, trend.Day{Date: d, Open: cE, High: cE * 1.01, Low: cE * 0.99, Close: cE})
		solHist = append(solHist, trend.Day{Date: d, Open: cS, High: cS * 1.01, Low: cS * 0.99, Close: cS})
		fmt.Fprintf(&spyCSV, "%s,%.2f,%.2f,%.2f,%.2f,1000\n", d, 400+float64(i), 401+float64(i), 399+float64(i), 400+float64(i))
		fmt.Fprintf(&tltCSV, "%s,%.2f,%.2f,%.2f,%.2f,1000\n", d, 100+float64(i)*0.1, 101+float64(i)*0.1, 99+float64(i)*0.1, 100+float64(i)*0.1)
	}
	if err := os.WriteFile(filepath.Join(eqDir, "SPY.csv"), []byte(spyCSV.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(eqDir, "TLT.csv"), []byte(tltCSV.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dtb3Path, []byte("date,rate\n2026-01-01,4.00\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := trend.NewState(btcHist[54].Date)
	st.Seed(btcHist[:55])
	st.SeedETH(ethHist[:55])
	st.SeedSOL(solHist[:55])
	st.Advance(btcHist[55:])
	st.AdvanceETH(ethHist[55:])
	st.AdvanceSOL(solHist[55:])
	tPath := filepath.Join(trendDir, "trend_state.json")
	if err := st.Save(tPath); err != nil {
		t.Fatal(err)
	}

	srv, err := New(Options{
		Token:           testToken,
		PeerToken:       peerToken,
		TrendDir:        trendDir,
		CarryDir:        carryDir,
		DTB3Path:        dtb3Path,
		EquitiesDir:     eqDir,
		DisableLivePing: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	authHdr := map[string]string{"Authorization": "Bearer " + peerToken}

	// First request warms cache (4 misses: trend, carry, dtb3, macroHRP).
	rec1 := get(t, h, http.MethodGet, "/api/paper/schwab-plan?portfolio=q57_full_fund&nav=1000&include_macro=true", authHdr, nil)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request failed: %d: %s", rec1.Code, rec1.Body.String())
	}
	missesAfterFirst := srv.trendCache.Misses()
	hitsAfterFirst := srv.trendCache.Hits()
	if missesAfterFirst == 0 || hitsAfterFirst != 0 {
		t.Fatalf("expected cold misses>0 hits=0, got misses=%d hits=%d", missesAfterFirst, hitsAfterFirst)
	}

	// Second request hits all 4 caches with zero extra misses (include_macro=true to load macro HRP).
	rec2 := get(t, h, http.MethodGet, "/api/paper/trend-blend?include_macro=true", authHdr, nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second request failed: %d: %s", rec2.Code, rec2.Body.String())
	}
	if srv.trendCache.Misses() != missesAfterFirst || srv.trendCache.Hits() < 4 {
		t.Fatalf("expected full cache hit (misses stay %d, hits>=4), got misses=%d hits=%d",
			missesAfterFirst, srv.trendCache.Misses(), srv.trendCache.Hits())
	}

	// Append a new bar to SPY.csv + bump DTB3.csv -> invalidates macroHRP + dtb3 caches immediately.
	nextDate := t0.AddDate(0, 0, 75).Format("2006-01-02")
	spyUpdated := spyCSV.String() + fmt.Sprintf("%s,476.00,477.00,475.00,476.00,1000\n", nextDate)
	if err := os.WriteFile(filepath.Join(eqDir, "SPY.csv"), []byte(spyUpdated), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dtb3Path, []byte("date,rate\n2026-01-01,4.00\n"+nextDate+",5.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec3 := get(t, h, http.MethodGet, "/api/paper/schwab-plan?portfolio=q57_full_fund&nav=1000&include_macro=true", authHdr, nil)
	if rec3.Code != http.StatusOK {
		t.Fatalf("third request failed: %d: %s", rec3.Code, rec3.Body.String())
	}
	if srv.trendCache.Misses() != missesAfterFirst+2 {
		t.Fatalf("expected exactly 2 cache invalidations (dtb3 + macroHRP), got misses=%d (before=%d)",
			srv.trendCache.Misses(), missesAfterFirst)
	}
	var body3 struct {
		Q57FullFund struct {
			LastDate   string `json:"last_date"`
			MacroStats struct {
				Days int `json:"days"`
			} `json:"macro_stats"`
		} `json:"q57_full_fund"`
	}
	if err := json.Unmarshal(rec3.Body.Bytes(), &body3); err != nil {
		t.Fatal(err)
	}
	if body3.Q57FullFund.LastDate != nextDate || body3.Q57FullFund.MacroStats.Days != 76 {
		t.Fatalf("expected invalidated cache to reflect new bar %s (76 days), got %+v", nextDate, body3.Q57FullFund)
	}
}
