package tax

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestFIFOLotMatchingAndTaxCalculation(t *testing.T) {
	calc := NewCalculator(24.0, 9.3)

	t1 := time.Date(2026, time.February, 10, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, time.February, 15, 14, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, time.March, 1, 16, 0, 0, 0, time.UTC)

	// Buy 0.010 BTC @ $50,000 = $500 basis, fee $0.075
	// Buy 0.010 BTC @ $60,000 = $600 basis, fee $0.090
	// Sell 0.015 BTC @ $70,000 = $1,050 proceeds, fee $0.1575
	// FIFO matching:
	// - 0.010 BTC from Lot 1: Proceeds $700, Basis $500, Fees ($0.075 + $0.105) = $0.18. Net = 700 - 500 - 0.18 = +199.82
	// - 0.005 BTC from Lot 2: Proceeds $350, Basis $300, Fees ($0.045 + $0.0525) = $0.0975. Net = 350 - 300 - 0.0975 = +49.9025
	// Total Net Taxable Gain: ~+249.7225
	trades := []RawTrade{
		{
			Timestamp: t1,
			Symbol:    "BTCUSDT",
			Side:      "BUY",
			Price:     50000.0,
			Size:      0.010,
			FeeUSDT:   0.075,
		},
		{
			Timestamp: t2,
			Symbol:    "BTCUSDT",
			Side:      "BUY",
			Price:     60000.0,
			Size:      0.010,
			FeeUSDT:   0.090,
		},
		{
			Timestamp: t3,
			Symbol:    "BTCUSDT",
			Side:      "SELL",
			Price:     70000.0,
			Size:      0.015,
			FeeUSDT:   0.1575,
		},
	}

	summary := calc.CalculateFromTrades(trades, 2026)
	if summary.TotalDispositions != 2 {
		t.Fatalf("expected 2 dispositions from FIFO split, got %d", summary.TotalDispositions)
	}

	expectedNetGain := (700.0 - 500.0 - (0.075 + 0.105)) + (350.0 - 300.0 - (0.045 + 0.0525))
	if math.Abs(summary.NetTaxableGainUSD-expectedNetGain) > 1e-4 {
		t.Fatalf("expected net taxable gain $%.4f, got $%.4f", expectedNetGain, summary.NetTaxableGainUSD)
	}

	// 24% Fed Tax, 9.3% CA Tax
	expectedFed := expectedNetGain * 0.24
	expectedCA := expectedNetGain * 0.093
	expectedTotal := expectedFed + expectedCA

	if math.Abs(summary.EstimatedFederalTax-expectedFed) > 1e-4 {
		t.Fatalf("expected Fed tax $%.4f, got $%.4f", expectedFed, summary.EstimatedFederalTax)
	}
	if math.Abs(summary.EstimatedStateTax-expectedCA) > 1e-4 {
		t.Fatalf("expected State tax $%.4f, got $%.4f", expectedCA, summary.EstimatedStateTax)
	}
	if math.Abs(summary.TotalTaxReserveUSD-expectedTotal) > 1e-4 {
		t.Fatalf("expected Total tax reserve $%.4f, got $%.4f", expectedTotal, summary.TotalTaxReserveUSD)
	}

	// Verify Q1 placement (March 1 trade is Q1)
	if len(summary.QuarterlySchedule) != 4 {
		t.Fatalf("expected 4 quarters in schedule, got %d", len(summary.QuarterlySchedule))
	}
	q1 := summary.QuarterlySchedule[0]
	if q1.Quarter != "Q1" {
		t.Fatalf("expected Q1, got %s", q1.Quarter)
	}
	if math.Abs(q1.RealizedGain-expectedNetGain) > 1e-4 {
		t.Fatalf("expected Q1 realized gain $%.4f, got $%.4f", expectedNetGain, q1.RealizedGain)
	}
}

func TestForm8949CSVExport(t *testing.T) {
	calc := NewCalculator(24.0, 9.3)
	t1 := time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, time.February, 20, 15, 0, 0, 0, time.UTC)

	trades := []RawTrade{
		{Timestamp: t1, Symbol: "ETHUSDT", Side: "BUY", Price: 2000.0, Size: 1.0, FeeUSDT: 0.30},
		{Timestamp: t2, Symbol: "ETHUSDT", Side: "SELL", Price: 2500.0, Size: 1.0, FeeUSDT: 0.375},
	}
	summary := calc.CalculateFromTrades(trades, 2026)
	csvBytes, err := ExportForm8949CSV(summary)
	if err != nil {
		t.Fatalf("failed to export CSV: %v", err)
	}

	csvStr := string(csvBytes)
	if !strings.Contains(csvStr, "Description of Property") {
		t.Fatalf("missing CSV header")
	}
	if !strings.Contains(csvStr, "1.0000 ETH") {
		t.Fatalf("expected '1.0000 ETH' in CSV, got: %s", csvStr)
	}
	if !strings.Contains(csvStr, "2500.00") || !strings.Contains(csvStr, "2000.00") {
		t.Fatalf("expected proceeds 2500 and cost basis 2000 in CSV: %s", csvStr)
	}
}

func TestCalculateFromJournalReader(t *testing.T) {
	calc := NewCalculator(24.0, 9.3)
	var buf bytes.Buffer

	r1 := RawTrade{
		Timestamp: time.Date(2026, time.April, 1, 10, 0, 0, 0, time.UTC),
		Symbol:    "BTCUSDC",
		Side:      "BUY",
		Price:     75000.0,
		Size:      0.001,
		FeeUSDT:   0.0,
	}
	r2 := RawTrade{
		Timestamp: time.Date(2026, time.April, 2, 10, 0, 0, 0, time.UTC),
		Symbol:    "BTCUSDC",
		Side:      "SELL",
		Price:     75150.0,
		Size:      0.001,
		FeeUSDT:   0.0,
	}

	b1, _ := json.Marshal(r1)
	b2, _ := json.Marshal(r2)
	buf.Write(append(b1, '\n'))
	buf.Write(append(b2, '\n'))

	summary, err := calc.CalculateFromReader(&buf, 2026)
	if err != nil {
		t.Fatalf("failed to calculate from reader: %v", err)
	}
	if summary.TotalDispositions != 1 {
		t.Fatalf("expected 1 disposition, got %d", summary.TotalDispositions)
	}
	expectedGain := 0.15 // $75.15 - $75.00
	if math.Abs(summary.NetTaxableGainUSD-expectedGain) > 1e-4 {
		t.Fatalf("expected gain $0.15, got $%.4f", summary.NetTaxableGainUSD)
	}

	// Verify terminal report formatting does not panic
	report := FormatTerminalReport(summary)
	if !strings.Contains(report, "TOTAL TAX RESERVE TO SET ASIDE") {
		t.Fatalf("terminal report missing expected tax reserve header")
	}
	table := FormatDispositionsTable(summary)
	if !strings.Contains(table, "IRS FORM 8949") {
		t.Fatalf("table missing expected 8949 header")
	}
}

func TestLongTermHoldingPeriodClassification(t *testing.T) {
	calc := NewCalculator(20.0, 9.0)
	tBuy := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	tSell := time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC) // > 2 years

	trades := []RawTrade{
		{Timestamp: tBuy, Symbol: "SOLUSDT", Side: "BUY", Price: 100.0, Size: 1.0},
		{Timestamp: tSell, Symbol: "SOLUSDT", Side: "SELL", Price: 200.0, Size: 1.0},
	}
	summary := calc.CalculateFromTrades(trades, 2026)
	if summary.TotalDispositions != 1 {
		t.Fatalf("expected 1 disposition, got %d", summary.TotalDispositions)
	}
	if summary.Dispositions[0].HoldingType != HoldingLongTerm {
		t.Fatalf("expected LONG_TERM holding type, got %s", summary.Dispositions[0].HoldingType)
	}
	if summary.NetLongTermGainUSD != 100.0 {
		t.Fatalf("expected long-term gain 100, got %f", summary.NetLongTermGainUSD)
	}
}

func TestFormatMoney_NaNSafety(t *testing.T) {
	// Must not panic on NaN or Inf
	s1 := formatMoney(math.NaN())
	if s1 != "0.00" {
		t.Fatalf("expected '0.00' for NaN, got %s", s1)
	}
	s2 := formatMoney(math.Inf(1))
	if s2 != "0.00" {
		t.Fatalf("expected '0.00' for +Inf, got %s", s2)
	}
	s3 := formatMoney(1234567.89)
	if s3 != "1,234,567.89" {
		t.Fatalf("expected formatted money '1,234,567.89', got %s", s3)
	}
}

func TestCalculateFromTrades_NaNSanitization(t *testing.T) {
	calc := NewCalculator(24.0, 9.3)
	tNow := time.Now()

	trades := []RawTrade{
		{Timestamp: tNow, Symbol: "BTCUSDT", Side: "BUY", Price: math.NaN(), Size: 1.0},
		{Timestamp: tNow, Symbol: "BTCUSDT", Side: "BUY", Price: 50000.0, Size: math.NaN()},
		{Timestamp: tNow, Symbol: "BTCUSDT", Side: "BUY", Price: -100.0, Size: 1.0},
		{Timestamp: tNow, Symbol: "BTCUSDT", Side: "BUY", Price: 50000.0, Size: 0.01, FeeUSDT: math.NaN()},
		{Timestamp: tNow.Add(time.Minute), Symbol: "BTCUSDT", Side: "SELL", Price: 51000.0, Size: 0.01, FeeUSDT: 0.05},
	}

	summary := calc.CalculateFromTrades(trades, tNow.Year())
	if summary.TotalDispositions != 1 {
		t.Fatalf("expected exactly 1 valid disposition, got %d", summary.TotalDispositions)
	}
	if math.IsNaN(summary.NetTaxableGainUSD) || math.IsInf(summary.NetTaxableGainUSD, 0) {
		t.Fatalf("NetTaxableGainUSD is NaN/Inf: %f", summary.NetTaxableGainUSD)
	}
}

func TestCalculateFromTrades_EmergencyLiquidation(t *testing.T) {
	calc := NewCalculator(24.0, 9.3)
	tNow := time.Now()

	trades := []RawTrade{
		{Timestamp: tNow, Symbol: "BTCUSDT", Side: "BUY", Price: 50000.0, Size: 0.010, FeeUSDT: 0.075},
		{Timestamp: tNow.Add(time.Hour), Symbol: "BTCUSDT", Side: "EMERGENCY_PROTECTIVE_LIQUIDATE", Price: 52000.0, Size: 0.010, FeeUSDT: 0.078},
	}

	summary := calc.CalculateFromTrades(trades, tNow.Year())
	if summary.TotalDispositions != 1 {
		t.Fatalf("expected 1 disposition for emergency liquidation, got %d", summary.TotalDispositions)
	}
	expectedGain := (520.0 - 500.0) - (0.075 + 0.078)
	if math.Abs(summary.NetTaxableGainUSD-expectedGain) > 1e-4 {
		t.Fatalf("expected gain $%.4f, got $%.4f", expectedGain, summary.NetTaxableGainUSD)
	}
}

func TestFIFOEdgeCases(t *testing.T) {
	calc := NewCalculator(24.0, 9.3)
	tNow := time.Now()

	// Test 1: Partial lot matching - sell spans multiple buy lots
	trades := []RawTrade{
		{Timestamp: tNow, Symbol: "BTCUSDT", Side: "BUY", Price: 50000.0, Size: 0.010, FeeUSDT: 0.075},
		{Timestamp: tNow.Add(time.Minute), Symbol: "BTCUSDT", Side: "BUY", Price: 51000.0, Size: 0.010, FeeUSDT: 0.0765},
		{Timestamp: tNow.Add(2 * time.Minute), Symbol: "BTCUSDT", Side: "SELL", Price: 52000.0, Size: 0.015, FeeUSDT: 0.117}, // Sell 0.015, spans both lots
	}
	summary := calc.CalculateFromTrades(trades, tNow.Year())
	if summary.TotalDispositions != 2 {
		t.Fatalf("expected 2 dispositions for partial lot matching, got %d", summary.TotalDispositions)
	}
	// First lot: 0.010 @ 50000, second lot: 0.005 @ 51000
	expectedGain1 := (0.010 * 52000.0) - (0.010 * 50000.0) - (0.010 * (0.075/0.010 + 0.117/0.015))
	expectedGain2 := (0.005 * 52000.0) - (0.005 * 51000.0) - (0.005 * (0.0765/0.010 + 0.117/0.015))
	expectedTotal := expectedGain1 + expectedGain2
	if math.Abs(summary.NetTaxableGainUSD-expectedTotal) > 1e-4 {
		t.Fatalf("partial lot matching: expected gain $%.4f, got $%.4f", expectedTotal, summary.NetTaxableGainUSD)
	}

	// Test 2: Sell exactly matches a lot (no partial)
	trades2 := []RawTrade{
		{Timestamp: tNow, Symbol: "ETHUSDT", Side: "BUY", Price: 3000.0, Size: 1.0, FeeUSDT: 4.5},
		{Timestamp: tNow.Add(time.Hour), Symbol: "ETHUSDT", Side: "SELL", Price: 3100.0, Size: 1.0, FeeUSDT: 4.65},
	}
	summary2 := calc.CalculateFromTrades(trades2, tNow.Year())
	if summary2.TotalDispositions != 1 {
		t.Fatalf("expected 1 disposition for exact match, got %d", summary2.TotalDispositions)
	}
	expectedGain2 = (3100.0 - 3000.0) - (4.5 + 4.65)
	if math.Abs(summary2.NetTaxableGainUSD-expectedGain2) > 1e-4 {
		t.Fatalf("exact match: expected gain $%.4f, got $%.4f", expectedGain2, summary2.NetTaxableGainUSD)
	}

	// Test 3: Multiple sells in same timestamp (order preserved by stable sort)
	trades3 := []RawTrade{
		{Timestamp: tNow, Symbol: "SOLUSDT", Side: "BUY", Price: 100.0, Size: 10.0, FeeUSDT: 0.15},
		{Timestamp: tNow, Symbol: "SOLUSDT", Side: "SELL", Price: 110.0, Size: 5.0, FeeUSDT: 0.0825},  // First sell
		{Timestamp: tNow, Symbol: "SOLUSDT", Side: "SELL", Price: 115.0, Size: 5.0, FeeUSDT: 0.08625}, // Second sell (same timestamp)
	}
	summary3 := calc.CalculateFromTrades(trades3, tNow.Year())
	if summary3.TotalDispositions != 2 {
		t.Fatalf("expected 2 dispositions for same-timestamp sells, got %d", summary3.TotalDispositions)
	}
	// First sell should match first 5 units, second sell matches remaining 5
	if summary3.Dispositions[0].Quantity != 5.0 || summary3.Dispositions[1].Quantity != 5.0 {
		t.Fatalf("unexpected quantities: %v, %v", summary3.Dispositions[0].Quantity, summary3.Dispositions[1].Quantity)
	}
}

func TestFIFOFallbackNoPriorBuy(t *testing.T) {
	calc := NewCalculator(24.0, 9.3)
	tNow := time.Now()

	// Sell without any prior buy - should use fallback logic
	trades := []RawTrade{
		{Timestamp: tNow, Symbol: "BTCUSDT", Side: "SELL", Price: 50000.0, Size: 0.010, FeeUSDT: 0.075, PnLRealized: 100.0},
	}
	summary := calc.CalculateFromTrades(trades, tNow.Year())
	if summary.TotalDispositions != 1 {
		t.Fatalf("expected 1 disposition for fallback sell, got %d", summary.TotalDispositions)
	}
	// Fallback basis = proceeds - pnlProp - fee
	// pnlProp = 100.0 * (0.010/0.010) = 100.0
	// proceeds = 500.0, fee = 0.075
	// basis = 500.0 - 100.0 - 0.075 = 399.925
	// netGain = 500.0 - 399.925 - 0.075 = 100.0
	expectedGain := 100.0
	if math.Abs(summary.NetTaxableGainUSD-expectedGain) > 1e-4 {
		t.Fatalf("fallback sell: expected gain $%.4f, got $%.4f", expectedGain, summary.NetTaxableGainUSD)
	}
	// Should be short-term (acquired 1 hour before sell)
	if summary.Dispositions[0].HoldingType != HoldingShortTerm {
		t.Fatalf("fallback should be short-term, got %s", summary.Dispositions[0].HoldingType)
	}
}

func TestFIFOZeroFeeHandling(t *testing.T) {
	calc := NewCalculator(24.0, 9.3)
	tNow := time.Now()

	// Trades with zero fees
	trades := []RawTrade{
		{Timestamp: tNow, Symbol: "BTCUSDT", Side: "BUY", Price: 50000.0, Size: 0.010, FeeUSDT: 0.0},
		{Timestamp: tNow.Add(time.Hour), Symbol: "BTCUSDT", Side: "SELL", Price: 51000.0, Size: 0.010, FeeUSDT: 0.0},
	}
	summary := calc.CalculateFromTrades(trades, tNow.Year())
	if summary.TotalDispositions != 1 {
		t.Fatalf("expected 1 disposition, got %d", summary.TotalDispositions)
	}
	expectedGain := 10.0 // (51000 - 50000) * 0.010
	if math.Abs(summary.NetTaxableGainUSD-expectedGain) > 1e-4 {
		t.Fatalf("zero fee: expected gain $%.4f, got $%.4f", expectedGain, summary.NetTaxableGainUSD)
	}
	if summary.TotalFeesUSD != 0.0 {
		t.Fatalf("expected zero total fees, got %f", summary.TotalFeesUSD)
	}
}
