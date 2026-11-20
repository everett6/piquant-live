package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"piquant-live/internal/collect"
)

// runBackfillGaps fills trade-id gaps in the recorded tick files from free public REST history. Fetched trades go
// to <dir>/ticks_backfill/<stream>/<day>.jsonl.gz (backfill=true); the live ticks/ files are never written.
func runBackfillGaps(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("backfill-gaps", flag.ExitOnError)
	home, _ := os.UserHomeDir()
	dir := fs.String("dir", filepath.Join(home, "piquant-collect"), "recorder directory holding ticks/")
	out := fs.String("out", "", "output directory (default <dir>/ticks_backfill)")
	streams := fs.String("streams", "", "comma-separated stream dirs under ticks/ (default all)")
	from := fs.String("from", "", "first day file to scan, YYYY-MM-DD (default: oldest)")
	to := fs.String("to", "", "last day file to scan, YYYY-MM-DD (default: newest)")
	maxGap := fs.Int64("max-gap", 3_000_000, "skip gaps with more missing trades than this")
	dry := fs.Bool("dry-run", false, "only list the gaps")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := collect.GapFillConfig{Dir: *dir, OutDir: *out, From: *from, To: *to, MaxGap: *maxGap, DryRun: *dry,
		Progress: func(s string) { fmt.Println(s) }}
	if *streams != "" {
		for _, s := range strings.Split(*streams, ",") {
			if s = strings.TrimSpace(s); s != "" {
				cfg.Streams = append(cfg.Streams, s)
			}
		}
	}
	rep, err := collect.BackfillTradeGaps(ctx, cfg)
	if rep != nil {
		names := make([]string, 0, len(rep.Streams))
		for k := range rep.Streams {
			names = append(names, k)
		}
		sort.Strings(names)
		fmt.Printf("%-20s %6s %9s %9s %9s %9s\n", "stream", "gaps", "missing", "filled", "skipped", "remaining")
		for _, k := range names {
			s := rep.Streams[k]
			fmt.Printf("%-20s %6d %9d %9d %9d %9d %s\n", k, s.Gaps, s.Missing, s.Filled, s.Skipped, s.Remaining, s.Error)
		}
	}
	return err
}
