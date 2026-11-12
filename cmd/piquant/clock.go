package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"piquant-live/internal/clocksync"
)

type clockFlags struct {
	servers string
	maxMs   int
}

func addClockFlags(fs *flag.FlagSet) *clockFlags {
	f := &clockFlags{}
	fs.StringVar(&f.servers, "clock-servers", strings.Join(clocksync.DefaultServers, ","),
		"SNTP servers for the clock guard (empty = guard off)")
	fs.IntVar(&f.maxMs, "clock-max-ms", 250, "clock guard limit: block new entries when the clock is off by more")
	return f
}

// clockRecord is one line of the offset log, so tick analyses can drop windows with a bad clock.
type clockRecord struct {
	At       time.Time `json:"at"`
	OffsetMs float64   `json:"offset_ms"`
	OK       bool      `json:"ok"`
	Servers  int       `json:"servers"`
	Err      string    `json:"err,omitempty"`
}

// startClockGuard starts the guard in the background (nil when disabled). logPath, if set, gets one JSON line per
// check. State changes are printed.
func startClockGuard(ctx context.Context, f *clockFlags, logPath string) *clocksync.Guard {
	var servers []string
	for _, s := range strings.Split(f.servers, ",") {
		if s = strings.TrimSpace(s); s != "" {
			servers = append(servers, s)
		}
	}
	if len(servers) == 0 {
		fmt.Println("[CLOCK] clock guard off")
		return nil
	}
	g := clocksync.NewGuard(servers)
	g.Max = time.Duration(f.maxMs) * time.Millisecond
	prev := -1
	g.OnCheck = func(m clocksync.Measurement, ok bool) {
		state := 0
		if ok {
			state = 1
		}
		if state != prev {
			if ok {
				fmt.Printf("[CLOCK] clock OK: offset %v from %d servers (limit %v)\n",
					m.Offset.Round(time.Millisecond), len(m.Samples), g.Max)
			} else {
				_, why := g.Status()
				fmt.Printf("[CLOCK] clock NOT OK: %s\n", why)
			}
			prev = state
		}
		if logPath == "" {
			return
		}
		rec := clockRecord{At: m.At.UTC(), OffsetMs: float64(m.Offset.Microseconds()) / 1000, OK: ok,
			Servers: len(m.Samples)}
		if m.Err != nil {
			rec.Err = m.Err.Error()
		}
		b, _ := json.Marshal(rec)
		if fh, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			_, _ = fh.Write(append(b, '\n'))
			_ = fh.Close()
		}
	}
	go g.Run(ctx)
	return g
}
