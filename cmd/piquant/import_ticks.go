package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"piquant-live/internal/backtest"
)

// freeGB returns free disk space at path in GB.
func freeGB(path string) float64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return float64(st.Bavail) * float64(st.Bsize) / 1e9
}

// monthsBetween lists YYYY-MM from..to inclusive.
func monthsBetween(from, to string) ([][2]int, error) {
	a, err := time.Parse("2006-01", from)
	if err != nil {
		return nil, fmt.Errorf("--from: %w", err)
	}
	b, err := time.Parse("2006-01", to)
	if err != nil {
		return nil, fmt.Errorf("--to: %w", err)
	}
	if b.Before(a) {
		return nil, fmt.Errorf("--to is before --from")
	}
	var out [][2]int
	for d := a; !d.After(b); d = d.AddDate(0, 1, 0) {
		out = append(out, [2]int{d.Year(), int(d.Month())})
	}
	return out, nil
}

// runImportTicks downloads real Binance aggTrades archives and converts them to .pqbin partitions:
// piquant import-ticks --symbol BTCUSDT --from 2024-01 --to 2024-03 [--market spot|futures] [--out dir]
func runImportTicks(args []string) error {
	fs := flag.NewFlagSet("import-ticks", flag.ContinueOnError)
	home, _ := os.UserHomeDir()
	symbol := fs.String("symbol", "BTCUSDT", "Binance symbol")
	from := fs.String("from", "", "first month, YYYY-MM")
	to := fs.String("to", "", "last month, YYYY-MM (default: same as --from)")
	market := fs.String("market", "spot", "spot or futures (USDT-margined)")
	out := fs.String("out", filepath.Join(home, "piquant-collect", "pqbin"), "output directory (real partitions are GBs: keep them out of git)")
	keepZip := fs.Bool("keep-zip", false, "keep the downloaded zip after conversion")
	minFree := fs.Float64("min-free-gb", 20, "stop when free disk would drop below this")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" {
		return fmt.Errorf("--from YYYY-MM is required")
	}
	if *to == "" {
		*to = *from
	}
	months, err := monthsBetween(*from, *to)
	if err != nil {
		return err
	}
	base := "https://data.binance.vision/data/spot"
	sub := "spot"
	if *market == "futures" {
		base, sub = "https://data.binance.vision/data/futures/um", "futures"
	} else if *market != "spot" {
		return fmt.Errorf("--market must be spot or futures")
	}
	sym := strings.ToUpper(*symbol)
	dir := filepath.Join(*out, sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	d := backtest.NewTickDownloader(dir)
	d.BaseURL = base
	d.Client = &http.Client{} // archives are hundreds of MB: no overall timeout
	for _, m := range months {
		dst := filepath.Join(dir, fmt.Sprintf("%s-%04d-%02d.pqbin", sym, m[0], m[1]))
		if real, err := backtest.PQBinIsReal(dst); err == nil && real {
			fmt.Printf("[IMPORT] %s already imported\n", filepath.Base(dst))
			continue
		}
		if free := freeGB(dir); free < *minFree+5 {
			return fmt.Errorf("only %.0f GB free; stopping to keep %.0f GB", free, *minFree)
		}
		t0 := time.Now()
		zipPath, err := d.DownloadMonthlyZip(sym, m[0], m[1])
		if err != nil {
			return err
		}
		n, clamped, err := backtest.ConvertZipToPQBinStats(zipPath, dst, sym, m[0], m[1])
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(zipPath), err)
		}
		if !*keepZip {
			_ = os.Remove(zipPath)
		}
		st, _ := os.Stat(dst)
		fmt.Printf("[IMPORT] %s: %d ticks (%d out-of-order timestamps clamped), %.2f GB, %s\n", filepath.Base(dst),
			n, clamped, float64(st.Size())/1e9, time.Since(t0).Round(time.Second))
	}
	return nil
}
