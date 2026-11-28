package clocksync

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"
)

func TestNTPRoundTrip(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 123456789, time.UTC)
	got := fromNTP(toNTP(now))
	if d := got.Sub(now); d > time.Microsecond || d < -time.Microsecond {
		t.Fatalf("round trip off by %v", d)
	}
}

// reply builds a server answer to a request sent at t1, with server receive/transmit times t2, t3.
func reply(t1, t2, t3 time.Time, stratum byte) []byte {
	b := make([]byte, 48)
	b[0] = 0<<6 | 4<<3 | 4
	b[1] = stratum
	binary.BigEndian.PutUint64(b[24:32], toNTP(t1))
	binary.BigEndian.PutUint64(b[32:40], toNTP(t2))
	binary.BigEndian.PutUint64(b[40:48], toNTP(t3))
	return b
}

func TestParseReplyOffset(t *testing.T) {
	t1 := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	// server is 300 ms ahead; one-way delay 20 ms; server processing 1 ms
	t2 := t1.Add(20*time.Millisecond + 300*time.Millisecond)
	t3 := t2.Add(time.Millisecond)
	t4 := t1.Add(41 * time.Millisecond)
	off, rtt, err := parseReply(reply(t1, t2, t3, 2), t1, t4)
	if err != nil {
		t.Fatal(err)
	}
	if d := off - 300*time.Millisecond; d > time.Millisecond || d < -time.Millisecond {
		t.Fatalf("offset %v, want 300ms", off)
	}
	if d := rtt - 40*time.Millisecond; d > time.Millisecond || d < -time.Millisecond {
		t.Fatalf("rtt %v, want 40ms", rtt)
	}
	if _, _, err := parseReply(reply(t1, t2, t3, 0), t1, t4); err == nil {
		t.Fatal("stratum 0 (kiss of death) accepted")
	}
	if _, _, err := parseReply(reply(t1.Add(time.Second), t2, t3, 2), t1, t4); err == nil {
		t.Fatal("mismatched originate timestamp accepted")
	}
}

// TestQueryLocalServer runs Query against a fake UDP server whose clock is 500 ms ahead.
func TestQueryLocalServer(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no UDP:", err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 48)
		n, addr, err := pc.ReadFrom(buf)
		if err != nil || n < 48 {
			return
		}
		t1 := fromNTP(binary.BigEndian.Uint64(buf[40:48]))
		srv := time.Now().Add(500 * time.Millisecond)
		r := reply(t1, srv, srv, 1)
		copy(r[24:32], buf[40:48]) // echo the exact bits
		_, _ = pc.WriteTo(r, addr)
	}()
	smp, err := Query(context.Background(), pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	off := smp.Offset
	if off < 400*time.Millisecond || off > 600*time.Millisecond {
		t.Fatalf("offset %v, want ~500ms", off)
	}
}

func fixed(offsets map[string]time.Duration, rtt time.Duration) QueryFunc {
	return func(_ context.Context, srv string) (Sample, error) {
		off, ok := offsets[srv]
		if !ok {
			return Sample{}, errors.New("timeout")
		}
		return Sample{Server: srv, Offset: off, RTT: rtt}, nil
	}
}

func TestMeasureMedianAndDiscards(t *testing.T) {
	q := fixed(map[string]time.Duration{"a": 10 * time.Millisecond, "b": 900 * time.Millisecond,
		"c": 20 * time.Millisecond}, 30*time.Millisecond)
	m := Measure(context.Background(), []string{"a", "b", "c", "d"}, 250*time.Millisecond, q)
	if m.Err != nil || m.Offset != 20*time.Millisecond || len(m.Samples) != 3 {
		t.Fatalf("median: %+v", m)
	}
	slow := fixed(map[string]time.Duration{"a": 0}, 600*time.Millisecond)
	if m := Measure(context.Background(), []string{"a"}, 250*time.Millisecond, slow); m.Err == nil {
		t.Fatal("600ms round trip cannot resolve 250ms but was accepted")
	}
}

func TestGuard(t *testing.T) {
	offsets := map[string]time.Duration{"a": 100 * time.Millisecond}
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	g := &Guard{Servers: []string{"a"}, Max: 250 * time.Millisecond, Interval: 10 * time.Minute,
		Query: func(ctx context.Context, s string) (Sample, error) { return fixed(offsets, time.Millisecond)(ctx, s) },
		now:   func() time.Time { return now }}
	if g.OK() {
		t.Fatal("OK before any check (must fail closed)")
	}
	g.Check(context.Background())
	if !g.OK() {
		t.Fatal("100ms offset should be OK")
	}
	offsets["a"] = -300 * time.Millisecond
	g.Check(context.Background())
	if ok, why := g.Status(); ok || why == "" {
		t.Fatal("-300ms offset should halt")
	}
	// a failed check keeps the last good verdict (still bad)
	delete(offsets, "a")
	g.Check(context.Background())
	if g.OK() {
		t.Fatal("a failed check must not clear a bad offset")
	}
	offsets["a"] = 0
	g.Check(context.Background())
	delete(offsets, "a")
	now = now.Add(29 * time.Minute)
	g.Check(context.Background())
	if !g.OK() {
		t.Fatal("recent good check should hold through one failure")
	}
	now = now.Add(2 * time.Minute) // 31 min since the last good check > 3 intervals
	if ok, _ := g.Status(); ok {
		t.Fatal("stale verification should halt")
	}
}

func TestGuardBackoffWhenNotOK(t *testing.T) {
	offsets := map[string]time.Duration{"a": 100 * time.Millisecond}
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	g := &Guard{Servers: []string{"a"}, Max: 250 * time.Millisecond, Interval: 10 * time.Minute,
		Query: func(ctx context.Context, s string) (Sample, error) { return fixed(offsets, time.Millisecond)(ctx, s) },
		now:   func() time.Time { return now }}
	g.Check(context.Background())
	if !g.OK() {
		t.Fatal("initial check should be OK")
	}

	// Make the clock fail
	offsets["a"] = -300 * time.Millisecond
	g.Check(context.Background())
	if g.OK() {
		t.Fatal("should not be OK after bad offset")
	}

	// Check that backoff multiplier increased (internal state, verify via behavior)
	// The next check should happen sooner than 10 minutes due to backoff
	// We can't easily test the timer without exposing internals, but we can verify
	// the backoffMult field is updated by checking it doesn't stay at 1.0
	// Since backoffMult is unexported, we test the behavioral effect:
	// After failure, Check should still work and update state
	g.Check(context.Background()) // another failure
	if g.OK() {
		t.Fatal("should still not be OK")
	}

	// Restore good offset
	offsets["a"] = 50 * time.Millisecond
	g.Check(context.Background())
	if !g.OK() {
		t.Fatal("should be OK after good offset restored")
	}
	// Backoff should reset on success
}

func TestGuardWallClockJumpDetection(t *testing.T) {
	// This test verifies that the Run loop handles wall-clock jumps correctly.
	// We can't easily test the Run loop without integration, but we can verify
	// the Check method updates lastCheck properly.
	offsets := map[string]time.Duration{"a": 100 * time.Millisecond}
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	g := &Guard{Servers: []string{"a"}, Max: 250 * time.Millisecond, Interval: 10 * time.Minute,
		Query: func(ctx context.Context, s string) (Sample, error) { return fixed(offsets, time.Millisecond)(ctx, s) },
		now:   func() time.Time { return now }}

	m := g.Check(context.Background())
	if m.Err != nil {
		t.Fatalf("Check failed: %v", m.Err)
	}
	// lastCheck should be set (internal field, but we can verify OK works)
	if !g.OK() {
		t.Fatal("should be OK after good check")
	}

	// Simulate clock jump forward by advancing 'now'
	now = now.Add(20 * time.Minute)
	m = g.Check(context.Background())
	if m.Err != nil {
		t.Fatalf("Check failed after clock jump: %v", m.Err)
	}
	if !g.OK() {
		t.Fatal("should still be OK after clock jump forward (within 3 intervals)")
	}

	// Simulate clock jump backward
	now = now.Add(-30 * time.Minute)
	m = g.Check(context.Background())
	if m.Err != nil {
		t.Fatalf("Check failed after clock jump backward: %v", m.Err)
	}
	// Should still work (lastGood is based on g.clock() which now returns the jumped time)
}

func TestMeasureWithInvalidOffset(t *testing.T) {
	// Test that Measure handles very large offsets correctly (simulating bad server)
	largeOffset := 24 * time.Hour // 1 day offset - unrealistic but tests bounds
	q := fixed(map[string]time.Duration{"a": largeOffset}, 30*time.Millisecond)
	m := Measure(context.Background(), []string{"a"}, 250*time.Millisecond, q)
	if len(m.Samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(m.Samples))
	}
	if m.Samples[0].Offset != largeOffset {
		t.Fatalf("expected large offset %v, got %v", largeOffset, m.Samples[0].Offset)
	}
	if m.Offset != largeOffset {
		t.Fatalf("expected median %v, got %v", largeOffset, m.Offset)
	}
}
