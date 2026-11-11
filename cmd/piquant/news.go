package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"piquant-live/internal/macro"
)

func runNews(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("news", flag.ContinueOnError)
	dataDir := fs.String("data", "", "data directory where news cache lives")
	syncURL := fs.String("sync-url", "", "optional custom URL for news feed ingestion")
	ingestTitle := fs.String("ingest", "", "ingest a breaking raw headline, e.g. --ingest='Solana Bridge Exploit: $50M Stolen'")
	ingestSummary := fs.String("summary", "", "optional summary for ingested news item")
	ingestSource := fs.String("source", "Manual CLI", "source label for ingested news item")
	limit := fs.Int("limit", 10, "number of recent news items to display")
	watch := fs.Bool("watch", false, "live update news ticker every 2 seconds")

	if err := fs.Parse(args); err != nil {
		return err
	}

	dDir := *dataDir
	if dDir == "" {
		dDir = defaultDataDir()
	}

	pipeline := macro.NewNewsPipeline(dDir, *syncURL)

	// If manual ingest requested:
	if strings.TrimSpace(*ingestTitle) != "" {
		now := time.Now().UTC()
		item := pipeline.IngestRawHeadline(*ingestTitle, *ingestSummary, *ingestSource, "", now)
		fmt.Printf("\n[SUCCESS] News Ingested & Analyzed:\n")
		fmt.Printf("  ID:               %s\n", item.ID)
		fmt.Printf("  Timestamp:        %s\n", item.Timestamp.Format(time.RFC3339))
		fmt.Printf("  Scope:            %s\n", item.Scope)
		fmt.Printf("  Category:         %s\n", item.Category)
		fmt.Printf("  Impact:           %s\n", item.Impact)
		fmt.Printf("  Sentiment Score:  %+.2f\n", item.SentimentScore)
		fmt.Printf("  Urgency Score:    %.2f\n", item.UrgencyScore)
		fmt.Printf("  Confidence Score: %.2f\n", item.ConfidenceScore)
		fmt.Printf("  Is Exploit/Hack:  %v\n", item.IsAdverseExploit)
		if len(item.AffectedSymbols) > 0 {
			fmt.Printf("  Affected Symbols: %s\n", strings.Join(item.AffectedSymbols, ", "))
		} else {
			fmt.Printf("  Affected Symbols: MARKET-WIDE (ALL)\n")
		}

		rep := pipeline.Evaluate(now)
		fmt.Printf("\n[DEFENSE STATE AFTER INGESTION]\n")
		fmt.Printf("  Macro Sentiment:  %+.2f\n", rep.MacroSentiment)
		if len(rep.SymbolBidKills) > 0 {
			var kills []string
			for s, k := range rep.SymbolBidKills {
				if k {
					kills = append(kills, s)
				}
			}
			if len(kills) > 0 {
				fmt.Printf("  BID KILLS ACTIVE: %s (Resting Buy Quotes Blocked!)\n", strings.Join(kills, ", "))
			}
		}
		if len(rep.SpreadMultipliers) > 0 {
			for s, mult := range rep.SpreadMultipliers {
				if mult > 1.0 {
					fmt.Printf("  Spread Multiplier (%s): %.2fx\n", s, mult)
				}
			}
		}
		fmt.Println()
		return nil
	}

	renderNewsDashboard(pipeline, *limit)

	if !*watch {
		return nil
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// ANSI clear screen and re-render
			fmt.Print("\033[H\033[2J")
			renderNewsDashboard(pipeline, *limit)
		}
	}
}

func renderNewsDashboard(pipeline *macro.NewsPipeline, limit int) {
	now := time.Now().UTC()
	rep := pipeline.Evaluate(now)
	recent := pipeline.GetRecentNews(limit)

	fmt.Println("================================================================================")
	fmt.Printf("      PIQUANT MACRO & MICRO NEWS INTELLIGENCE ORACLE  [%s]\n", now.Format("15:04:05 UTC"))
	fmt.Println("================================================================================")

	// Macro State
	macroDesc := "NEUTRAL"
	if rep.MacroSentiment >= 0.35 {
		macroDesc = "BULLISH / DOVISH EXPANSION"
	} else if rep.MacroSentiment <= -0.35 {
		macroDesc = "BEARISH / HAWKISH CONTRACTION"
	}
	fmt.Printf("Macro Bias:       %+.2f (%s)\n", rep.MacroSentiment, macroDesc)

	// Symbol Sentiments
	var symScores []string
	for sym, score := range rep.SymbolSentiments {
		symScores = append(symScores, fmt.Sprintf("%s: %+.2f", sym, score))
	}
	if len(symScores) > 0 {
		fmt.Printf("Symbol Sentiments: %s\n", strings.Join(symScores, " | "))
	} else {
		fmt.Printf("Symbol Sentiments: Calibrated to neutral baseline\n")
	}

	// Active Defenses
	var activeKills []string
	for sym, killed := range rep.SymbolBidKills {
		if killed {
			activeKills = append(activeKills, sym)
		}
	}
	if len(activeKills) > 0 {
		fmt.Printf("\033[1;31m[EXPLOIT SHIELD ACTIVE] BID KILL ENGAGED: %s (No buy orders placed)\033[0m\n", strings.Join(activeKills, ", "))
	} else {
		fmt.Printf("Exploit Defenses:  All order books clear (Normal Quoting)\n")
	}

	var activeSpreads []string
	for sym, mult := range rep.SpreadMultipliers {
		if mult > 1.0 {
			activeSpreads = append(activeSpreads, fmt.Sprintf("%s=%.2fx", sym, mult))
		}
	}
	if len(activeSpreads) > 0 {
		fmt.Printf("Spread Multipliers: %s\n", strings.Join(activeSpreads, ", "))
	}

	fmt.Println("--------------------------------------------------------------------------------")
	fmt.Printf("RECENT BREAKING NEWS & MACRO EVENTS (%d items)\n", len(recent))
	fmt.Println("--------------------------------------------------------------------------------")

	for i, it := range recent {
		age := now.Sub(it.Timestamp).Round(time.Minute)
		ageStr := fmt.Sprintf("%s ago", age)
		if age < time.Minute {
			ageStr = "just now"
		}

		impactTag := fmt.Sprintf("[%s]", it.Impact)
		scopeTag := fmt.Sprintf("[%s]", it.Scope)

		symStr := "ALL"
		if len(it.AffectedSymbols) > 0 {
			symStr = strings.Join(it.AffectedSymbols, ",")
		}

		fmt.Printf("%2d. %-10s %-8s %-10s Sent:%+0.2f  Sym:%-8s  %s\n",
			i+1, ageStr, scopeTag, impactTag, it.SentimentScore, symStr, it.Title)
		if it.Summary != "" {
			fmt.Printf("    -> %s\n", it.Summary)
		}
	}
	fmt.Println("================================================================================")
}
