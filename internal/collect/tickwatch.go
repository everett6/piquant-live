package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// TickProblem is one issue found in ticks_status.json; Key groups repeats for alert cooldowns.
type TickProblem struct {
	Key, Msg string
}

// tradeStaleLimit is how long a stream may go without a trade before it counts as stalled. Binance.US trades are
// sparse (a few per hour), so only the busy streams are checked.
var tradeStaleLimit = map[string]time.Duration{
	"BTC-USD":        15 * time.Minute,
	"ETH-USD":        15 * time.Minute,
	"kraken-BTC-USD": 30 * time.Minute,
}

// CheckTicks compares the latest status with the previous one (nil on the first check).
func CheckTicks(prev *TickStatus, cur TickStatus, now time.Time) []TickProblem {
	var out []TickProblem
	if age := now.Sub(cur.UpdatedAt); age > 5*time.Minute {
		out = append(out, TickProblem{"ticks-status", fmt.Sprintf("tick status not updated for %s (recorder stuck?)",
			age.Round(time.Second))})
	}
	if cur.Paused {
		out = append(out, TickProblem{"ticks-paused", "tick writing paused (free disk below the guard)"})
	}
	venues := make([]string, 0, len(cur.Connected))
	for v := range cur.Connected {
		venues = append(venues, v)
	}
	sort.Strings(venues)
	for _, v := range venues {
		// alert only when it was also down at the previous check (~5 min), so quick reconnects stay quiet
		if !cur.Connected[v] && prev != nil && !prev.Connected[v] {
			out = append(out, TickProblem{"venue-" + v, v + " websocket disconnected for 5+ minutes: " + cur.LastError[v]})
		}
	}
	keys := make([]string, 0, len(tradeStaleLimit))
	for k := range tradeStaleLimit {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if ms, ok := cur.LastTrade[k]; ok && ms > 0 {
			if age := now.Sub(time.UnixMilli(ms)); age > tradeStaleLimit[k] {
				out = append(out, TickProblem{"stale-" + k, fmt.Sprintf("%s: no trade for %s", k, age.Round(time.Second))})
			}
		}
	}
	if prev != nil {
		gk := make([]string, 0, len(cur.TradeGaps))
		for k := range cur.TradeGaps {
			gk = append(gk, k)
		}
		sort.Strings(gk)
		for _, k := range gk {
			if d := cur.TradeGaps[k] - prev.TradeGaps[k]; d > 0 {
				out = append(out, TickProblem{"gap-" + k, fmt.Sprintf("%s: %d new trade-ID gaps (missing trades; "+
					"PREREG17 drops those minutes)", k, d)})
			}
		}
	}
	return out
}

// WatchTicks reads <dir>/ticks_status.json every 5 minutes and reports problems.
func WatchTicks(ctx context.Context, dir string, report func(TickProblem)) {
	var prev *TickStatus
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		b, err := os.ReadFile(filepath.Join(dir, "ticks_status.json"))
		if err != nil {
			report(TickProblem{"ticks-status", "cannot read ticks_status.json: " + err.Error()})
			continue
		}
		var cur TickStatus
		if err := json.Unmarshal(b, &cur); err != nil {
			continue // caught mid-rename; next check
		}
		for _, p := range CheckTicks(prev, cur, time.Now()) {
			report(p)
		}
		prev = &cur
	}
}
