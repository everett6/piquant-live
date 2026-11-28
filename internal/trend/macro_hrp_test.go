package trend

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestHRPWeightsNParity12x60(t *testing.T) {
	const nAssets, nBars = 12, 60
	rets := make([][]float64, nAssets)
	for j := 0; j < nAssets; j++ {
		rets[j] = make([]float64, nBars)
	}
	for b := 0; b < nBars; b++ {
		f := float64(b)
		for j := 0; j < nAssets; j++ {
			phase := float64(j%4) * 0.65
			rets[j][b] = 0.012*math.Sin(f*(0.25+0.07*float64(j%4))+phase) + 0.004*math.Cos(f*(0.31+0.05*float64(j)))
		}
	}

	got := hrpWeightsN(rets)
	want := []float64{
		0.134773324543, 0.177224386349, 0.052297622041, 0.032497816267,
		0.053839747858, 0.072968657705, 0.131515889429, 0.099695631779,
		0.051930064484, 0.072248093691, 0.069882729006, 0.051126036849,
	}
	var sum float64
	for i := range want {
		sum += got[i]
		if math.Abs(got[i]-want[i]) > 1e-7 {
			t.Fatalf("hrpWeightsN[%d] = %.12f, want %.12f", i, got[i], want[i])
		}
	}
	if math.Abs(sum-1.0) > 1e-12 {
		t.Fatalf("hrpWeightsN sum = %v, want 1.0", sum)
	}

	// Zero-variance degenerate column handling.
	degen := make([][]float64, 3)
	degen[0] = rets[0]
	degen[1] = make([]float64, nBars)
	degen[2] = rets[2]
	dw := hrpWeightsN(degen)
	if dw[1] != 0 || math.Abs(dw[0]+dw[2]-1.0) > 1e-12 {
		t.Fatalf("degenerate hrpWeightsN = %v, want zero on col 1", dw)
	}
}

func TestMacroHRPSleeveAndFullFundOnRealData(t *testing.T) {
	eqDir := DefaultEquitiesDir()
	if info, err := os.Stat(eqDir); err != nil || !info.IsDir() {
		t.Skipf("equities dir %s absent: %v", eqDir, err)
	}
	series, err := LoadEquityCSVs(eqDir, MacroEquityTickers)
	if err != nil {
		t.Fatalf("LoadEquityCSVs: %v", err)
	}
	if len(series) < 6 {
		t.Skipf("insufficient equity CSVs loaded: %d", len(series))
	}
	home, _ := os.UserHomeDir()
	rates, err := LoadDTB3Rates(filepath.Join(home, "piquant-data", "macro", "DTB3.csv"))
	if err != nil {
		t.Fatalf("LoadDTB3Rates: %v", err)
	}

	macroSum, ok := SimulateMacroHRPSleeve(series, rates)
	if !ok {
		t.Fatal("SimulateMacroHRPSleeve returned ok=false")
	}
	if macroSum.Days < 200 || macroSum.Equity <= 1.0 || math.IsNaN(macroSum.Sharpe) {
		t.Fatalf("unexpected macro summary: %+v", macroSum)
	}
	t.Logf("[GO MACRO HRP] Tickers=%d Days=%d (%s) | Eq=%.4f CAGR=%.2f%% MaxDD=%.2f%% Sharpe=%.3f Calmar=%.3f | Invested=%.2f%% IdleSGOV=%.2f%%",
		macroSum.TickersLoaded, macroSum.Days, macroSum.LastDate,
		macroSum.Equity, macroSum.CAGR, macroSum.MaxDD, macroSum.Sharpe, macroSum.Calmar,
		macroSum.InvestedPct, macroSum.IdleSGOVPct,
	)

	// Reconstruct real L2 crypto state if available and compute combined Q57_Full_Fund_1x over Oct-Nov 2026!
	dates, summaries := discoverCollectedL2Series(t)
	seedLen := 51
	st := NewState(dates[seedLen-1])
	seqB := make([]Day, 0, len(dates))
	seqE := make([]Day, 0, len(dates))
	seqS := make([]Day, 0, len(dates))
	for _, d := range dates {
		seqB = append(seqB, summaries["BTC"][d].Day)
		seqE = append(seqE, summaries["ETH"][d].Day)
		seqS = append(seqS, summaries["SOL"][d].Day)
	}
	st.Seed(seqB[:seedLen])
	st.SeedETH(seqE[:seedLen])
	st.SeedSOL(seqS[:seedLen])
	st.Advance(seqB[seedLen:])
	st.AdvanceETH(seqE[seedLen:])
	st.AdvanceSOL(seqS[seedLen:])

	full, okF := ComputeQ57FullFund(st, nil, rates, eqDir, nil)
	if !okF || full.Name != "Q57_full_multi_asset_fund_1x" {
		t.Fatalf("ComputeQ57FullFund failed: ok=%v full=%+v", okF, full)
	}
	totAlloc := full.CryptoBTCPortPct + full.CryptoETHPortPct + full.CryptoSOLPortPct + full.CarrySleevePct + full.MacroInvestedPct + full.IdleSGOVPct
	if totAlloc < 99.0 || totAlloc > 101.0 {
		t.Fatalf("full fund target allocation sum = %.2f%%, want ~100%%", totAlloc)
	}

	// Evaluate real combined L2 Crypto Q57 + Go Macro HRP over the forward L2 dates (2026-10-05..last macro date).
	macroByDate := make(map[string]float64, len(macroSum.DailyDates))
	for i, dt := range macroSum.DailyDates {
		macroByDate[dt] = macroSum.DailyRets[i]
	}
	l57 := st.Ledgers["Q57_hrp_hurst_conformal_spot_1x"]
	combEq, combPeak, combMaxDD := 1.0, 1.0, 0.0
	wC := full.CryptoSleeveWeightPct / 100.0
	wM := full.MacroSleeveWeightPct / 100.0
	for _, d := range l57.Days {
		if d.Date > macroSum.LastDate {
			break
		}
		mR := macroByDate[d.Date] // 0 on weekends/holidays
		rTot := wC*d.Ret + wM*mR
		combEq *= 1.0 + rTot
		if combEq > combPeak {
			combPeak = combEq
		}
		if dd := 1.0 - combEq/combPeak; dd > combMaxDD {
			combMaxDD = dd
		}
	}
	t.Logf("[GO Q57 FULL FUND] Weights(Crypto=%.1f%% Macro=%.1f%%) | ActiveInvested=%.2f%% IdleSGOV=%.2f%% | CombinedL2(%s..%s) Eq=%.4f MaxDD=%.2f%%",
		full.CryptoSleeveWeightPct, full.MacroSleeveWeightPct, full.MacroInvestedPct, full.IdleSGOVPct,
		l57.Days[0].Date, macroSum.LastDate, combEq, combMaxDD*100.0,
	)
	for _, mt := range full.MacroTargets {
		if mt.TargetPct > 0 {
			t.Logf("  Active Target: %-5s %5.2f%% (%s | close=$%.2f mom=%+.3f vol=%.1f%% trend=%.2f)",
				mt.Symbol, mt.TargetPct, mt.Role, mt.LastClose, mt.Momentum, mt.Vol20Pct, mt.TrendPos)
		}
	}
}

func TestFastDualCoherenceParity(t *testing.T) {
	const n = 300
	arr := make([]Day, n)
	px := 100.0
	for i := 0; i < n; i++ {
		switch {
		case i < 80:
			px = 100.0 + 0.4*float64(i)
		case i < 140:
			px = 132.0
		case i < 200:
			px = 132.0 - 0.5*float64(i-140)
		default:
			px = 102.0 + 8.0*math.Sin(float64(i)*0.22) + 0.25*float64(i-200)
		}
		arr[i] = Day{Date: "2026-01-01", Close: px}
	}
	got := make([]float64, n)
	fastDualCoherenceSeries(arr, got)
	for k := 0; k < n; k++ {
		sub := arr[:k+1]
		want := 0.5 * (coherenceShare(sub, 50) + r3CoherenceShare(sub, 50, 20))
		if math.Abs(got[k]-want) > 1e-12 {
			t.Fatalf("bar %d: fastDualCoherenceSeries=%0.15f want=%0.15f", k, got[k], want)
		}
	}
}

func BenchmarkHRPWeights12x60(b *testing.B) {
	const nAssets, nBars = 12, 60
	rets := make([][]float64, nAssets)
	for j := 0; j < nAssets; j++ {
		rets[j] = make([]float64, nBars)
	}
	for t := 0; t < nBars; t++ {
		f := float64(t)
		for j := 0; j < nAssets; j++ {
			rets[j][t] = 0.01*math.Sin(f*(0.2+0.05*float64(j))) + 0.003*math.Cos(f*0.3)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := hrpWeightsN(rets)
		if len(w) != nAssets {
			b.Fatal("bad len")
		}
	}
}

func BenchmarkHRPWeights12x60Into(b *testing.B) {
	const nAssets, nBars = 12, 60
	rets := make([][]float64, nAssets)
	for j := 0; j < nAssets; j++ {
		rets[j] = make([]float64, nBars)
	}
	for t := 0; t < nBars; t++ {
		f := float64(t)
		for j := 0; j < nAssets; j++ {
			rets[j][t] = 0.01*math.Sin(f*(0.2+0.05*float64(j))) + 0.003*math.Cos(f*0.3)
		}
	}
	out := make([]float64, nAssets)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hrpWeightsNInto(rets, out)
		if out[0] <= 0 {
			b.Fatal("bad weight")
		}
	}
}

func BenchmarkLoadEquityCSVs(b *testing.B) {
	eqDir := DefaultEquitiesDir()
	if info, err := os.Stat(eqDir); err != nil || !info.IsDir() {
		b.Skipf("equities dir %s absent: %v", eqDir, err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		series, err := LoadEquityCSVs(eqDir, MacroEquityTickers)
		if err != nil || len(series) == 0 {
			b.Fatalf("LoadEquityCSVs: %v len=%d", err, len(series))
		}
	}
}

func BenchmarkSimulateMacroHRPSleeve(b *testing.B) {
	eqDir := DefaultEquitiesDir()
	if info, err := os.Stat(eqDir); err != nil || !info.IsDir() {
		b.Skipf("equities dir %s absent: %v", eqDir, err)
	}
	series, err := LoadEquityCSVs(eqDir, MacroEquityTickers)
	if err != nil || len(series) < 6 {
		b.Skipf("insufficient equity CSVs: err=%v len=%d", err, len(series))
	}
	home, _ := os.UserHomeDir()
	rates, err := LoadDTB3Rates(filepath.Join(home, "piquant-data", "macro", "DTB3.csv"))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sum, ok := SimulateMacroHRPSleeve(series, rates)
		if !ok || sum.Days == 0 {
			b.Fatal("SimulateMacroHRPSleeve failed")
		}
	}
}
