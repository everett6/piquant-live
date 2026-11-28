package alert

import (
	"fmt"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLoadEnv(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "alert.env")
	_ = os.WriteFile(p, []byte("# comment\nexport PIQUANT_SMTP_USER=a@example.com\nPIQUANT_SMTP_PASS=\"abcd efgh ijkl mnop\"\n"), 0o600)
	c, err := LoadEnv(p)
	if err != nil || !c.Configured() || c.Pass != "abcdefghijklmnop" || c.To != "a@example.com" ||
		c.Addr != "smtp.gmail.com:587" {
		t.Fatalf("%+v %v", c, err)
	}
	c, err = LoadEnv(filepath.Join(dir, "missing"))
	if err != nil || c.Configured() {
		t.Fatal("missing file must give an unconfigured config, no error")
	}
}

func TestSendCooldownAndMessage(t *testing.T) {
	var mu sync.Mutex
	var got []string
	done := make(chan struct{}, 4)
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	a := &Alerter{Cfg: Config{User: "a@example.com", Pass: "x", To: "b@example.com", Addr: "smtp.example.com:587"},
		Source: "pi", Cooldown: time.Hour, Logf: func(string, ...any) {},
		now: func() time.Time { return now },
		send: func(addr string, _ smtp.Auth, from string, to []string, msg []byte) error {
			mu.Lock()
			got = append(got, string(msg))
			mu.Unlock()
			done <- struct{}{}
			return nil
		}}
	if !a.Send("kill", "Kill switch", "line1\nline2") {
		t.Fatal("first send suppressed")
	}
	<-done
	if a.Send("kill", "Kill switch", "again") {
		t.Fatal("cooldown ignored")
	}
	now = now.Add(61 * time.Minute)
	if !a.Send("kill", "Kill switch", "later") {
		t.Fatal("cooldown never expires")
	}
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || !strings.Contains(got[0], "Subject: [PiQuant pi] Kill switch\r\n") ||
		!strings.Contains(got[0], "line1\r\nline2") {
		t.Fatalf("messages %q", got)
	}
}

func TestUnconfiguredLogsOnly(t *testing.T) {
	var logged []string
	a := &Alerter{Source: "desktop", Cooldown: time.Hour,
		Logf: func(f string, args ...any) { logged = append(logged, f) },
		send: func(string, smtp.Auth, string, []string, []byte) error { t.Fatal("sent without config"); return nil }}
	a.Send("x", "subject", "body")
	if len(logged) != 1 {
		t.Fatalf("logged %v", logged)
	}
	var nilA *Alerter
	if nilA.Send("x", "s", "b") {
		t.Fatal("nil alerter must be a no-op")
	}
	if maskEmail("someone@example.com") != "s***@example.com" {
		t.Fatal("mask")
	}
}

func TestLogfSerialization(t *testing.T) {
	// Test that Logf calls are serialized (no concurrent access issues)
	var mu sync.Mutex
	var logged []string
	a := &Alerter{Source: "test", Cooldown: time.Hour,
		Logf: func(f string, args ...any) {
			mu.Lock()
			logged = append(logged, fmt.Sprintf(f, args...))
			mu.Unlock()
		},
		send: func(string, smtp.Auth, string, []string, []byte) error { return nil }}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			a.Send(fmt.Sprintf("key-%d", id), "subject", "body")
		}(i)
	}
	wg.Wait()

	// All 10 sends should have been logged (no cooldown conflict since keys differ)
	mu.Lock()
	if len(logged) != 10 {
		t.Fatalf("expected 10 log entries, got %d: %v", len(logged), logged)
	}
	mu.Unlock()
}

func TestDefaultCooldown(t *testing.T) {
	// Test that Cooldown defaults to 1 hour when not explicitly set
	var logged []string
	a := &Alerter{Source: "test", // Cooldown not set (zero value)
		Logf: func(f string, args ...any) { logged = append(logged, fmt.Sprintf(f, args...)) },
		send: func(string, smtp.Auth, string, []string, []byte) error { return nil }}

	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	// First send should succeed
	if !a.Send("key1", "subject", "body") {
		t.Fatal("first send should succeed")
	}
	if len(logged) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logged))
	}

	// Second send with same key within default cooldown (1 hour) should be suppressed
	if a.Send("key1", "subject", "body") {
		t.Fatal("second send should be suppressed by default cooldown")
	}
	if len(logged) != 1 {
		t.Fatalf("expected still 1 log entry, got %d", len(logged))
	}

	// Advance time past default cooldown
	now = now.Add(61 * time.Minute)
	a.now = func() time.Time { return now }
	if !a.Send("key1", "subject", "body") {
		t.Fatal("send should succeed after cooldown expires")
	}
	if len(logged) != 2 {
		t.Fatalf("expected 2 log entries after cooldown expiry, got %d", len(logged))
	}
}

func TestSendNowRequiresConfig(t *testing.T) {
	a := &Alerter{Source: "test"}
	err := a.SendNow("subject", "body")
	if err == nil || err.Error() != "email not configured" {
		t.Fatalf("expected 'email not configured' error, got %v", err)
	}
}

func TestSendLogsBodyWhenEmailOff(t *testing.T) {
	var lines []string
	a := &Alerter{Source: "raspberrypi", Logf: func(f string, args ...any) { lines = append(lines, fmt.Sprintf(f, args...)) }}
	long := strings.Repeat("x", 400)
	if !a.Send("venue-kraken", "Tick recording problem", "kraken websocket\ndisconnected for 5+ minutes") {
		t.Fatal("first send suppressed")
	}
	a.Send("gap-BTC", "Tick recording problem", long)
	if len(lines) != 2 {
		t.Fatalf("want 2 log lines, got %v", lines)
	}
	if lines[0] != "[ALERT] [PiQuant raspberrypi] Tick recording problem: kraken websocket disconnected for 5+ minutes" {
		t.Fatalf("unexpected log line %q", lines[0])
	}
	if !strings.HasSuffix(lines[1], "...") || len(lines[1]) > 360 {
		t.Fatalf("long body not capped: %d chars", len(lines[1]))
	}
}
