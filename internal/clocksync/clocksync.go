// Package clocksync measures the local clock against public time servers (SNTP, RFC 4330) and keeps a guard that
// says whether timestamps can be trusted. Owner rule (STRATEGY_SPEC "Clock sync"): halt price-gap entries when the
// clock is off by more than 250 ms, because wrong timestamps would fake a lead-lag signal.
package clocksync

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"
)

// DefaultServers are queried in parallel; the median offset of the good answers is used.
var DefaultServers = []string{"time.google.com", "time.cloudflare.com", "pool.ntp.org"}

// ntpEpochOffset is the seconds from 1900-01-01 (NTP era 0) to 1970-01-01.
const ntpEpochOffset = 2208988800

// Sample is one server's answer. Offset is how far the server's clock is ahead of ours (positive: we are behind).
type Sample struct {
	Server string
	Offset time.Duration
	RTT    time.Duration
}

func toNTP(t time.Time) uint64 {
	sec := uint64(t.Unix() + ntpEpochOffset)
	frac := uint64(t.Nanosecond()) << 32 / 1e9
	return sec<<32 | frac
}

func fromNTP(v uint64) time.Time {
	sec := int64(v>>32) - ntpEpochOffset
	nsec := int64((v & 0xffffffff) * 1e9 >> 32)
	return time.Unix(sec, nsec)
}

// parseReply checks an SNTP reply and computes offset and round trip from the four timestamps.
func parseReply(b []byte, t1, t4 time.Time) (time.Duration, time.Duration, error) {
	if len(b) < 48 {
		return 0, 0, errors.New("short reply")
	}
	mode := b[0] & 0x7
	li := b[0] >> 6
	stratum := b[1]
	if mode != 4 {
		return 0, 0, fmt.Errorf("bad mode %d", mode)
	}
	if li == 3 || stratum == 0 || stratum > 15 {
		return 0, 0, fmt.Errorf("server unsynchronised (li %d, stratum %d)", li, stratum)
	}
	if binary.BigEndian.Uint64(b[24:32]) != toNTP(t1) {
		return 0, 0, errors.New("reply does not match request")
	}
	t2 := fromNTP(binary.BigEndian.Uint64(b[32:40]))
	t3 := fromNTP(binary.BigEndian.Uint64(b[40:48]))
	offset := (t2.Sub(t1) + t3.Sub(t4)) / 2
	rtt := t4.Sub(t1) - t3.Sub(t2)
	if rtt < 0 {
		rtt = 0
	}
	return offset, rtt, nil
}

// Query asks one server ("host" or "host:port"; default port 123) for the time.
func Query(ctx context.Context, server string) (Sample, error) {
	s := Sample{Server: server}
	addr := server
	if _, _, err := net.SplitHostPort(server); err != nil {
		addr = net.JoinHostPort(server, "123")
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return s, err
	}
	defer conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	req := make([]byte, 48)
	req[0] = 0<<6 | 4<<3 | 3 // LI 0, version 4, mode 3 (client)
	t1 := time.Now()
	binary.BigEndian.PutUint64(req[40:48], toNTP(t1)) // echoed back as the originate timestamp
	if _, err := conn.Write(req); err != nil {
		return s, err
	}
	resp := make([]byte, 48)
	n, err := conn.Read(resp)
	t4 := time.Now()
	if err != nil {
		return s, err
	}
	s.Offset, s.RTT, err = parseReply(resp[:n], t1, t4)
	return s, err
}

// Measurement is the result of one check across servers.
type Measurement struct {
	At      time.Time
	Offset  time.Duration // median offset of the good samples
	Samples []Sample
	Err     error // set when no server gave a usable answer
}

// QueryFunc is Query's signature (tests replace it).
type QueryFunc func(ctx context.Context, server string) (Sample, error)

// Measure queries all servers in parallel. A sample whose round trip is so long that it cannot resolve maxErr
// (RTT/2 > maxErr) is discarded.
func Measure(ctx context.Context, servers []string, maxErr time.Duration, q QueryFunc) Measurement {
	if q == nil {
		q = Query
	}
	m := Measurement{At: time.Now()}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var errs []error
	for _, srv := range servers {
		wg.Add(1)
		go func(srv string) {
			defer wg.Done()
			s, err := q(ctx, srv)
			if err == nil && maxErr > 0 && s.RTT/2 > maxErr {
				err = fmt.Errorf("round trip %v too long", s.RTT)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", srv, err))
				return
			}
			m.Samples = append(m.Samples, s)
		}(srv)
	}
	wg.Wait()
	if len(m.Samples) == 0 {
		m.Err = errors.Join(append([]error{errors.New("no time server answered")}, errs...)...)
		return m
	}
	offs := make([]time.Duration, len(m.Samples))
	for i, s := range m.Samples {
		offs[i] = s.Offset
	}
	sort.Slice(offs, func(i, j int) bool { return offs[i] < offs[j] })
	if n := len(offs); n%2 == 1 {
		m.Offset = offs[n/2]
	} else {
		m.Offset = (offs[n/2-1] + offs[n/2]) / 2
	}
	return m
}

// Guard re-measures every Interval. OK is false (fail closed) until a good measurement arrives, while the last
// good measurement is older than three intervals, or while |offset| > Max.
type Guard struct {
	Servers  []string
	Max      time.Duration
	Interval time.Duration
	Query    QueryFunc
	OnCheck  func(Measurement, bool) // optional: called after every check with the new OK state

	mu       sync.RWMutex
	last     Measurement // latest check, good or not
	good     Measurement // latest check with a usable answer
	lastGood time.Time
	now      func() time.Time
}

// NewGuard returns a guard with the owner's 250 ms limit and a 10-minute interval.
func NewGuard(servers []string) *Guard {
	if len(servers) == 0 {
		servers = DefaultServers
	}
	return &Guard{Servers: servers, Max: 250 * time.Millisecond, Interval: 10 * time.Minute}
}

func (g *Guard) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// Check measures once and updates the guard.
func (g *Guard) Check(ctx context.Context) Measurement {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	m := Measure(cctx, g.Servers, g.Max, g.Query)
	g.mu.Lock()
	g.last = m
	if m.Err == nil {
		g.good, g.lastGood = m, g.clock()
	}
	g.mu.Unlock()
	if g.OnCheck != nil {
		g.OnCheck(m, g.OK())
	}
	return m
}

// Run checks now and then every Interval until ctx ends.
func (g *Guard) Run(ctx context.Context) {
	if g.Interval <= 0 {
		g.Interval = 10 * time.Minute
	}
	g.Check(ctx)
	t := time.NewTicker(g.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.Check(ctx)
		}
	}
}

// Status reports whether timestamps are trustworthy now, with a reason when not.
func (g *Guard) Status() (bool, string) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	switch {
	case g.lastGood.IsZero():
		if g.last.Err != nil {
			return false, "clock not verified: " + g.last.Err.Error()
		}
		return false, "clock not verified yet"
	case g.clock().Sub(g.lastGood) > 3*g.Interval:
		return false, fmt.Sprintf("clock last verified %s ago", g.clock().Sub(g.lastGood).Round(time.Second))
	}
	// A failed check within three intervals of a good one keeps the good one's verdict.
	if off := g.good.Offset; off > g.Max || off < -g.Max {
		return false, fmt.Sprintf("clock off by %v (limit %v)", off.Round(time.Millisecond), g.Max)
	}
	return true, ""
}

// OK is Status without the reason.
func (g *Guard) OK() bool {
	ok, _ := g.Status()
	return ok
}

// Last returns the latest measurement.
func (g *Guard) Last() Measurement {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.last
}
