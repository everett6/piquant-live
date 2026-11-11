package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"piquant-live/internal/collect"
	"piquant-live/internal/engine"
	"piquant-live/internal/paper"
)

// runCollect records live prices + news to disk and backfills history, for offline research.
func runCollect(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("collect", flag.ExitOnError)
	home, _ := os.UserHomeDir()
	dir := fs.String("dir", filepath.Join(home, "piquant-collect"), "data directory")
	symbols := fs.String("symbols", "BTCUSDT,ETHUSDT", "Binance.US symbols")
	candleDays := fs.Int("backfill-days", 365, "days of Coinbase 1m candles to backfill")
	gdeltDays := fs.Int("gdelt-days", 90, "days of GDELT headlines to backfill")
	noBackfill := fs.Bool("no-backfill", false, "skip historical backfill")
	minFree := fs.Float64("min-free-gb", 20, "pause writing below this much free disk")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	symList := strings.Split(*symbols, ",")
	cbProds := []string{}
	for i := range symList {
		symList[i] = strings.TrimSpace(strings.ToUpper(symList[i]))
		for _, base := range []string{"BTC", "ETH", "SOL"} {
			if strings.HasPrefix(symList[i], base) {
				cbProds = append(cbProds, base+"-USD")
			}
		}
	}

	ring := engine.NewSPSCRing(8192)
	streamCfg := paper.DefaultStreamConfig()
	streamCfg.Symbols = symList
	streamCfg.Endpoint = paper.BuildBinanceUSStreamURL(symList)
	ing := paper.NewMarketStreamIngestor(streamCfg, ring)
	ing.Start(ctx)
	defer ing.Stop()

	cbCfg := paper.DefaultCoinbaseStreamConfig()
	cbCfg.Products = cbProds
	oracle := paper.NewCoinbaseSignalOracle(symList)
	cbIng := paper.NewCoinbaseStreamIngestor(cbCfg, nil, oracle)
	cbIng.Start(ctx)
	defer cbIng.Stop()

	col, err := collect.New(collect.Config{Dir: *dir, Symbols: symList, MinFreeGB: *minFree}, ing, ring, oracle)
	if err != nil {
		return err
	}
	if !*noBackfill {
		client := &http.Client{Timeout: 60 * time.Second}
		now := time.Now().UTC()
		go func() {
			for _, prod := range cbProds {
				job := "candles_" + prod
				err := collect.BackfillCoinbaseCandles(ctx, client, *dir, prod, now.AddDate(0, 0, -*candleDays), now,
					func(s string) { col.SetBackfillStatus(job, s) })
				if err != nil && ctx.Err() == nil {
					col.SetBackfillStatus(job, "error: "+err.Error())
				}
			}
		}()
		go func() {
			queries := []struct{ label, q string }{
				{"crypto", "(bitcoin OR crypto OR ethereum)"},
				{"macro", `("federal reserve" OR inflation OR CPI OR tariffs)`},
			}
			for _, q := range queries { // sequential: GDELT allows one request per 5s per client
				job := "gdelt_" + q.label
				err := collect.BackfillGDELT(ctx, client, *dir, q.label, q.q, now.AddDate(0, 0, -*gdeltDays), now,
					3*time.Hour, func(s string) { col.SetBackfillStatus(job, s) })
				if err != nil && ctx.Err() == nil {
					col.SetBackfillStatus(job, "error: "+err.Error())
				}
			}
		}()
	}
	fmt.Printf("[COLLECT] writing to %s (symbols %v, coinbase %v); status in status.json\n", *dir, symList, cbProds)
	col.Run(ctx)
	fmt.Println("[COLLECT] stopped, buffers flushed")
	return nil
}
