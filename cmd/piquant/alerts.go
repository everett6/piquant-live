package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"piquant-live/internal/alert"
	"piquant-live/internal/paper"
	"piquant-live/internal/trend"
)

func addAlertFlag(fs *flag.FlagSet) *string {
	return fs.String("alert-env", alert.DefaultEnvPath(), "env file with the Gmail alert settings (missing = log only)")
}

// feedStale decides whether the market feed needs an alert: no event for 5 minutes, or none at all 10 minutes
// after start.
func feedStale(lastNs int64, started, now time.Time) (bool, string) {
	if lastNs <= 0 {
		if now.Sub(started) > 10*time.Minute {
			return true, "no market data received since start " + started.UTC().Format(time.RFC3339)
		}
		return false, ""
	}
	age := now.Sub(time.Unix(0, lastNs))
	if age > 5*time.Minute {
		return true, fmt.Sprintf("last market event %s ago", age.Round(time.Second))
	}
	return false, ""
}

// dailySummary is the body of the daily email.
func dailySummary(st paper.DryRunState, scores []trend.Summary, now time.Time) string {
	var b strings.Builder
	dd := 0.0
	if st.PeakEquity > 0 {
		dd = (1 - st.Equity/st.PeakEquity) * 100
	}
	fmt.Fprintf(&b, "Daily summary %s (paper money only)\n\nEngine: equity $%.2f, peak $%.2f, drop from peak %.1f%%, "+
		"halted %v, fills %d\n", now.UTC().Format("2006-01-02"), st.Equity, st.PeakEquity, dd, st.CircuitHalted,
		st.TotalTrades)
	if st.CircuitHalted {
		fmt.Fprintf(&b, "Halt reason: %s\n", st.CircuitHaltReason)
	}
	if len(scores) > 0 {
		b.WriteString("\nTrend ledgers (equity 1.00 = start):\n")
		for _, s := range scores {
			fmt.Fprintf(&b, "  %-26s equity %.4f  max drop %.1f%%  position %.2f", s.Name, s.Equity, s.MaxDD*100,
				s.CurrentPos)
			if s.Killed {
				b.WriteString("  KILLED")
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// watchEngine sends feed-stall alerts and a daily summary after 00:15 UTC; it also routes kill locks to email.
func watchEngine(ctx context.Context, eng *paper.DryRunEngine, al *alert.Alerter, trendDir string) {
	paper.KillHook = func(kl paper.KillLock) {
		al.Send("kill", "KILL SWITCH: engine halted and locked", fmt.Sprintf(
			"Reason: %s\nTrigger: %s\nAt: %s\nEquity $%.2f, peak $%.2f\n\nThe engine stays halted until you run "+
				"'piquant unlock' with the service stopped.", kl.Reason, kl.Trigger, kl.At.Format(time.RFC3339),
			kl.Equity, kl.PeakEquity))
	}
	started := time.Now()
	lastDaily := ""
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now().UTC()
		_, _, _, _, _, _, lastNs := eng.StreamStats()
		if stale, why := feedStale(lastNs, started, now); stale {
			al.Send("feed", "Market feed stalled", why+"\n\nPositions are kept; the engine resumes when data returns.")
		}
		day := now.Format("2006-01-02")
		if now.Hour() == 0 && now.Minute() >= 15 && day != lastDaily {
			lastDaily = day
			var scores []trend.Summary
			if trendDir != "" {
				if ts, err := trend.Load(filepath.Join(trendDir, "trend_state.json")); err == nil {
					scores = ts.Score(now)
				}
			}
			al.Send("daily-"+day, "Daily summary "+day, dailySummary(eng.State(), scores, now))
		}
	}
}

// runAlertTest sends one test email synchronously: piquant alert-test [--alert-env path].
func runAlertTest(args []string) error {
	fs := flag.NewFlagSet("alert-test", flag.ContinueOnError)
	env := addAlertFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	host, _ := os.Hostname()
	al := alert.New(*env, host)
	if err := al.SendNow("Test alert", "If you can read this, PiQuant email alerts work on "+host+"."); err != nil {
		return fmt.Errorf("test email failed: %w", err)
	}
	fmt.Println("Test email sent.")
	return nil
}
