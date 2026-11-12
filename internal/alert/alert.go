// Package alert sends owner alerts by email (Gmail SMTP with an app password). Credentials live in an env file on
// the machine, never in git:
//
//	PIQUANT_SMTP_USER=you@gmail.com
//	PIQUANT_SMTP_PASS=<16-character Gmail app password>
//	PIQUANT_ALERT_TO=you@gmail.com        (optional; defaults to PIQUANT_SMTP_USER)
//	PIQUANT_SMTP_ADDR=smtp.gmail.com:587  (optional)
//
// Without the file every alert is still printed to the log, so nothing is lost before the owner sets it up.
package alert

import (
	"bufio"
	"fmt"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultEnvPath is ~/.config/piquant/alert.env.
func DefaultEnvPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "piquant", "alert.env")
}

// Config is the SMTP setup.
type Config struct {
	User, Pass, To, Addr string
}

// Configured reports whether email can be sent.
func (c Config) Configured() bool { return c.User != "" && c.Pass != "" }

// LoadEnv reads KEY=VALUE lines (# comments and blank lines ignored; quotes stripped). A missing file gives an
// empty config and no error.
func LoadEnv(path string) (Config, error) {
	c := Config{Addr: "smtp.gmail.com:587"}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(strings.TrimPrefix(k, "export ")) {
		case "PIQUANT_SMTP_USER":
			c.User = v
		case "PIQUANT_SMTP_PASS":
			c.Pass = strings.ReplaceAll(v, " ", "") // Google shows app passwords in groups of four
		case "PIQUANT_ALERT_TO":
			c.To = v
		case "PIQUANT_SMTP_ADDR":
			c.Addr = v
		}
	}
	if c.To == "" {
		c.To = c.User
	}
	return c, sc.Err()
}

// Alerter sends alerts, at most one per key per Cooldown.
type Alerter struct {
	Cfg      Config
	Source   string // e.g. "pi" or "desktop", shown in the subject
	Cooldown time.Duration
	Logf     func(format string, a ...any)

	send func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
	now  func() time.Time
	mu   sync.Mutex
	last map[string]time.Time
}

// New loads the env file and returns an alerter (email off when the file is missing).
func New(envPath, source string) *Alerter {
	cfg, err := LoadEnv(envPath)
	a := &Alerter{Cfg: cfg, Source: source, Cooldown: time.Hour}
	if err != nil {
		a.logf("[ALERT] cannot read %s: %v (email off)", envPath, err)
	}
	if cfg.Configured() {
		a.logf("[ALERT] email alerts on (to %s)", maskEmail(cfg.To))
	} else {
		a.logf("[ALERT] email not configured (%s missing or incomplete); alerts go to the log only", envPath)
	}
	return a
}

func (a *Alerter) logf(format string, args ...any) {
	if a.Logf != nil {
		a.Logf(format, args...)
		return
	}
	fmt.Printf(format+"\n", args...)
}

func (a *Alerter) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// Send emails subject/body unless the same key was sent within Cooldown. It never blocks the caller for long:
// mail goes out on a goroutine. Returns false when suppressed by the cooldown. Safe on a nil Alerter.
func (a *Alerter) Send(key, subject, body string) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	if a.last == nil {
		a.last = map[string]time.Time{}
	}
	now := a.clock()
	if t, ok := a.last[key]; ok && now.Sub(t) < a.Cooldown {
		a.mu.Unlock()
		return false
	}
	a.last[key] = now
	a.mu.Unlock()
	subj := fmt.Sprintf("[PiQuant %s] %s", a.Source, subject)
	a.logf("[ALERT] %s", subj)
	if !a.Cfg.Configured() {
		return true
	}
	go func() {
		if err := a.deliver(subj, body, now); err != nil {
			a.logf("[ALERT] email failed: %v", err)
		}
	}()
	return true
}

// SendNow is Send but synchronous, returning the delivery error (for `piquant alert-test`).
func (a *Alerter) SendNow(subject, body string) error {
	subj := fmt.Sprintf("[PiQuant %s] %s", a.Source, subject)
	if !a.Cfg.Configured() {
		return fmt.Errorf("email not configured")
	}
	return a.deliver(subj, body, a.clock())
}

func (a *Alerter) deliver(subject, body string, at time.Time) error {
	host := a.Cfg.Addr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	msg := buildMessage(a.Cfg.User, a.Cfg.To, subject, body, at)
	send := a.send
	if send == nil {
		send = smtp.SendMail // upgrades to STARTTLS when the server offers it (Gmail does on 587)
	}
	return send(a.Cfg.Addr, smtp.PlainAuth("", a.Cfg.User, a.Cfg.Pass, host), a.Cfg.User,
		strings.Split(a.Cfg.To, ","), msg)
}

func buildMessage(from, to, subject, body string, at time.Time) []byte {
	clean := func(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\n", clean(from), clean(to), clean(subject),
		at.UTC().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	b.WriteString("\r\n")
	return []byte(b.String())
}

func maskEmail(s string) string {
	at := strings.Index(s, "@")
	if at <= 1 {
		return "***"
	}
	return s[:1] + "***" + s[at:]
}
