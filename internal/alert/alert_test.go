package alert

import (
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
