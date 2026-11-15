package dashboard

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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
	}
	if err := json.Unmarshal(rec1.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" || len(res.Scorecard) < 10 || len(res.Blend) != 2 {
		t.Fatalf("unexpected response: %+v", res)
	}
	if res.Blend[0].LatestDTB3Pct != 4.12 || res.Blend[0].CarryMatchedDays != 2 {
		t.Fatalf("unexpected blend metrics: %+v", res.Blend[0])
	}
}
