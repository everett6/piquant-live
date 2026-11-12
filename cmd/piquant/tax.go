package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"piquant-live/internal/tax"
	"piquant-live/internal/trend"
)

type taxFlags struct {
	dataDir   string
	taxYear   int
	fedRate   float64
	stateRate float64
	taxRate   float64
	exportCSV string
	printCSV  bool
	printJSON bool
	printLot  bool
	trend     bool
	trendDir  string
	trendCap  float64
}

func addTaxFlags(fs *flag.FlagSet) *taxFlags {
	f := &taxFlags{}
	fs.StringVar(&f.dataDir, "data", "", "data directory where paper journals live")
	fs.IntVar(&f.taxYear, "year", time.Now().Year(), "tax year to report (default current year, 0 for all-time)")
	fs.Float64Var(&f.fedRate, "fed-rate", 24.0, "estimated US federal marginal tax rate percentage (default: 24.0%)")
	fs.Float64Var(&f.stateRate, "state-rate", 9.3, "estimated California state tax rate percentage (default: 9.3% CA FTB)")
	fs.Float64Var(&f.taxRate, "tax-rate", 0, "combined flat tax rate override (e.g. 35.0 to set combined federal+state)")
	fs.StringVar(&f.exportCSV, "export", "", "file path to export IRS Form 8949 CSV (e.g. form8949_2026.csv)")
	fs.BoolVar(&f.printCSV, "csv", false, "output raw IRS Form 8949 CSV to standard output")
	fs.BoolVar(&f.printJSON, "json", false, "output structured tax summary in JSON format")
	fs.BoolVar(&f.printLot, "table", false, "print detailed Form 8949 trade-by-trade lot disposition table")
	fs.BoolVar(&f.trend, "trend", false, "report the trend ledgers (P1, R3, D2, ...) instead of the engine journal")
	fs.StringVar(&f.trendDir, "trend-dir", "", "trend ledger directory (default <data dir>/trend)")
	fs.Float64Var(&f.trendCap, "trend-capital", 200, "dollars per trend variant at equity 1 (a variant's own capital wins)")
	return f
}

func runTax(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tax", flag.ContinueOnError)
	f := addTaxFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	dDir := f.dataDir
	if dDir == "" {
		dDir = defaultDataDir()
	}

	if f.trend {
		dir := f.trendDir
		if dir == "" {
			dir = filepath.Join(dDir, "trend")
		}
		return runTrendTax(f, filepath.Join(dir, "trend_state.json"))
	}

	// Locate journal file
	journalPath := filepath.Join(dDir, "paper", "dryrun_journal.jsonl")
	if _, err := os.Stat(journalPath); os.IsNotExist(err) {
		// Also check root data dir fallback
		fallbackPath := filepath.Join("data", "paper", "dryrun_journal.jsonl")
		if _, err2 := os.Stat(fallbackPath); err2 == nil {
			journalPath = fallbackPath
		} else {
			return fmt.Errorf("no trade journal found at %s. Run 'piquant dry-run' first to generate trading records", journalPath)
		}
	}

	fedRate := f.fedRate
	stateRate := f.stateRate
	if f.taxRate > 0 {
		// Split combined rate: standard ~72% Fed, 28% State proportion
		fedRate = f.taxRate * (24.0 / 33.3)
		stateRate = f.taxRate - fedRate
	}

	calc := tax.NewCalculator(fedRate, stateRate)
	summary, err := calc.CalculateFromJournalFile(journalPath, f.taxYear)
	if err != nil {
		return fmt.Errorf("failed to process tax records from journal: %w", err)
	}

	// 1. Export CSV file if requested
	if f.exportCSV != "" {
		csvBytes, err := tax.ExportForm8949CSV(summary)
		if err != nil {
			return fmt.Errorf("failed to generate Form 8949 CSV: %w", err)
		}
		if err := os.WriteFile(f.exportCSV, csvBytes, 0o644); err != nil {
			return fmt.Errorf("failed to write Form 8949 CSV to %s: %w", f.exportCSV, err)
		}
		fmt.Printf("Successfully exported IRS Form 8949 CSV to: %s\n", f.exportCSV)
		return nil
	}

	// 2. Output CSV to stdout if requested
	if f.printCSV {
		csvBytes, err := tax.ExportForm8949CSV(summary)
		if err != nil {
			return err
		}
		os.Stdout.Write(csvBytes)
		return nil
	}

	// 3. Output JSON if requested
	if f.printJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(summary)
	}

	// 4. Default: Print full terminal report
	fmt.Print(tax.FormatTerminalReport(summary))

	if f.printLot {
		fmt.Print(tax.FormatDispositionsTable(summary))
	}

	return nil
}

// runTrendTax writes the trend ledgers' closed positions (Form 8949 for spot variants, Form 6781 Section 1256
// for futures variants) as CSV, or prints totals.
func runTrendTax(f *taxFlags, statePath string) error {
	st, err := trend.Load(statePath)
	if err != nil {
		return fmt.Errorf("trend ledger %s: %w", statePath, err)
	}
	rows, open, skipped := st.TaxRows(f.trendCap, f.taxYear)
	switch {
	case f.exportCSV != "":
		fh, err := os.Create(f.exportCSV)
		if err != nil {
			return err
		}
		if err := trend.WriteTaxCSV(fh, rows); err != nil {
			fh.Close()
			return err
		}
		if err := fh.Close(); err != nil {
			return err
		}
		fmt.Printf("Wrote %d trend ledger rows to %s\n", len(rows), f.exportCSV)
	case f.printCSV:
		return trend.WriteTaxCSV(os.Stdout, rows)
	}
	year := "all years"
	if f.taxYear != 0 {
		year = fmt.Sprint(f.taxYear)
	}
	fmt.Printf("Trend ledgers, %s, $%.0f per variant unless set: %s\n", year, f.trendCap, trend.Totals(rows))
	fmt.Printf("Open positions (not taxable until closed; futures are marked at year end): %d\n", len(open))
	for _, o := range open {
		fmt.Printf("  %-26s %s qty %.6f basis $%.2f since %s\n", o.Variant, o.Coin, o.Quantity, o.CostUSD, o.Opened)
	}
	if skipped > 0 {
		fmt.Printf("Skipped %d ledger days recorded before per-coin detail existed\n", skipped)
	}
	fmt.Println("Paper record only: fills at daily closes, futures treated as Section 1256 contracts. Check with a tax professional.")
	return nil
}
