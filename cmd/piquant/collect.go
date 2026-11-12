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
	ticks := fs.Bool("ticks", true, "record every trade and 1s top-10 order-book snapshots (ticks/, book/)")
	tickVenues := fs.String("tick-venues", "coinbase,binanceus,kraken", "venues for tick recording")
	tickBases := fs.String("tick-bases", "BTC,ETH,SOL", "coins for tick recording on every venue")
	backupHost := fs.String("backup-host", "", "ssh target for the nightly Pi state backup (empty = off)")
	backupRemote := fs.String("backup-remote-dir", "piquant-data", "Pi data dir, relative to its home")
	backupPaths := fs.String("backup-paths", "paper,trend,config,reports,macro", "Pi subdirs to back up")
	backupDest := fs.String("backup-dest", filepath.Join(home, "piquant-backups", "pi"), "local backup folder")
	backupKeep := fs.Int("backup-keep", 30, "nightly archives to keep")
	backupHour := fs.Int("backup-hour", 3, "local hour after which the nightly backup runs")
	backupGit := fs.String("backup-git-dir", "", "clone of a PRIVATE repo to push Pi ledgers to (empty = off)")
	clockF := addClockFlags(fs)
	gdeltSpacing := fs.Duration("gdelt-spacing", 20*time.Second, "delay between GDELT requests (their limit is ~1 per 5s, enforced strictly)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	collect.GDELTSpacing = *gdeltSpacing
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
				retryUntilDone(ctx, func() error {
					return collect.BackfillCoinbaseCandles(ctx, client, *dir, prod, now.AddDate(0, 0, -*candleDays), now,
						func(s string) { col.SetBackfillStatus(job, s) })
				}, func(err error) { col.SetBackfillStatus(job, "error, retrying in 5m: "+err.Error()) })
			}
		}()
		go func() {
			queries := []struct{ label, q string }{
				{"crypto", "(bitcoin OR crypto OR ethereum)"},
				{"macro", `("federal reserve" OR inflation OR CPI OR tariffs)`},
			}
			for _, q := range queries { // sequential: GDELT allows one request per 5s per client
				job := "gdelt_" + q.label
				// Windows that fail (throttling, network down) are retried on the next pass.
				for pass := 0; pass < 6 && ctx.Err() == nil; pass++ {
					retryUntilDone(ctx, func() error {
						return collect.BackfillGDELT(ctx, client, *dir, q.label, q.q, now.AddDate(0, 0, -*gdeltDays), now,
							3*time.Hour, func(s string) { col.SetBackfillStatus(job, s) })
					}, func(err error) { col.SetBackfillStatus(job, "error, retrying in 5m: "+err.Error()) })
				}
			}
		}()
	}
	if *ticks {
		tc := collect.TickConfig{Dir: *dir, MinFreeGB: *minFree}
		venues := strings.Split(*tickVenues, ",")
		for _, b := range strings.Split(*tickBases, ",") {
			b = strings.ToUpper(strings.TrimSpace(b))
			for _, v := range venues {
				switch strings.TrimSpace(v) {
				case "coinbase":
					tc.Products = append(tc.Products, b+"-USD")
				case "binanceus":
					tc.BinanceUS = append(tc.BinanceUS, b+"USDT")
				case "kraken":
					tc.Kraken = append(tc.Kraken, b+"/USD")
				}
			}
		}
		rec, err := collect.NewTickRecorder(tc)
		if err != nil {
			return err
		}
		tickDone := make(chan struct{})
		go func() { rec.Run(ctx); close(tickDone) }()
		defer func() { <-tickDone }() // let the recorder flush its buffers on shutdown
	}
	if *backupHost != "" {
		bc := collect.BackupConfig{Host: *backupHost, RemoteDir: *backupRemote, Dest: *backupDest,
			Keep: *backupKeep, Hour: *backupHour, GitDir: *backupGit}
		for _, p := range strings.Split(*backupPaths, ",") {
			if p = strings.TrimSpace(p); p != "" {
				bc.Paths = append(bc.Paths, p)
			}
		}
		go collect.RunBackups(ctx, bc, func(f string, a ...any) { fmt.Printf(f+"\n", a...) })
	}
	startClockGuard(ctx, clockF, filepath.Join(*dir, "clock_offsets.jsonl"))
	fmt.Printf("[COLLECT] writing to %s (symbols %v, coinbase %v); status in status.json\n", *dir, symList, cbProds)
	col.Run(ctx)
	fmt.Println("[COLLECT] stopped, buffers flushed")
	return nil
}

// retryUntilDone reruns fn every 5 minutes until it succeeds or ctx ends, so backfills
// survive network outages without a restart (completed days/windows are skipped on rerun).
func retryUntilDone(ctx context.Context, fn func() error, onErr func(error)) {
	for ctx.Err() == nil {
		err := fn()
		if err == nil || ctx.Err() != nil {
			return
		}
		onErr(err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Minute):
		}
	}
}

// runExtract turns stored headlines into structured signals with a local Ollama model.
func runExtract(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("extract", flag.ExitOnError)
	home, _ := os.UserHomeDir()
	dir := fs.String("dir", filepath.Join(home, "piquant-collect"), "collector data directory")
	model := fs.String("model", "hf.co/janhq/Jan-v1-4B-GGUF:Q4_K_M", "Ollama model")
	endpoint := fs.String("endpoint", "http://127.0.0.1:11434", "Ollama base URL")
	every := fs.Duration("every", 10*time.Minute, "re-scan interval for new headlines")
	once := fs.Bool("once", false, "process the current backlog once and exit")
	limit := fs.Int("limit", 0, "max headlines per pass (0 = all)")
	input := fs.String("input", "", "score headlines from this JSONL file (id,title,at,src) instead of the collector store; implies --once")
	outSub := fs.String("out", "teacher", "output subdirectory of --dir when --input is set")
	if err := fs.Parse(args); err != nil {
		return err
	}
	x := &collect.Extractor{Endpoint: *endpoint, Model: *model, Client: &http.Client{Timeout: 5 * time.Minute}}
	progress := func(s string) { fmt.Println("[EXTRACT]", s) }
	if *input != "" {
		n, err := collect.RunExtractionFile(ctx, x, *input, filepath.Join(*dir, *outSub), *limit, progress)
		fmt.Printf("[EXTRACT] file done: %d headlines (err=%v)\n", n, err)
		return err
	}
	for {
		n, err := collect.RunExtraction(ctx, x, *dir, *limit, progress)
		fmt.Printf("[EXTRACT] pass done: %d headlines (err=%v)\n", n, err)
		if *once || ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*every):
		}
	}
}
