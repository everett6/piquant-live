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
