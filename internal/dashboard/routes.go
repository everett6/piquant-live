package dashboard

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"piquant-live/internal/secrets"
)

// routes registers every handler. All of them read.
func (s *Server) routes() {
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/login", s.handleLogin)
	s.mux.HandleFunc("/logout", s.handleLogout)
	s.mux.HandleFunc("/api/status", s.handleStatus)
	s.mux.HandleFunc("/api/health", s.handleHealth)
	s.mux.HandleFunc("/api/events", s.handleEvents)
	s.mux.HandleFunc("/api/logs", s.handleLogs)
	s.mux.HandleFunc("/api/peers", s.handlePeers)
}

// handleHealth is the unauthenticated-by-token liveness probe peers use. It
// reveals only that the process is up and which role it has: no host metrics,
// no experiment names, no dataset paths.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"role": string(s.opts.Role),
		"app":  s.opts.App,
		"v":    Version,
	})
}

// handleStatus returns the full status document. A peer token or a browser
// session is required.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	switch s.authenticate(r) {
	case authPeer, authSession:
	default:
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	writeJSON(w, http.StatusOK, s.Status(r.Context()))
}

// handlePeers returns only the peer table, for the auto-refresh path.
func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	if s.authenticate(r) == authNone {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"peers": s.Status(r.Context()).Peers})
}

// handleEvents streams status over Server-Sent Events so the page needs no
// polling loop and no extra dependency. The stream ends when the client leaves
// or after maxStream so a forgotten tab cannot hold a connection forever.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.authenticate(r) == authNone {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	const (
		interval  = 2 * time.Second
		maxStream = 30 * time.Minute
	)
	deadline := time.NewTimer(maxStream)
	defer deadline.Stop()
	tick := time.NewTicker(interval)
	defer tick.Stop()

	send := func() bool {
		body, err := json.Marshal(s.Status(r.Context()))
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: status\ndata: %s\n\n", body); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !send() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			return
		case <-tick.C:
			if !send() {
				return
			}
		}
	}
}

// LogLine is one line the log viewer returns.
type LogLine struct {
	N    int    `json:"n"`    // line number within the file
	Text string `json:"text"` // redacted
	// Level is parsed from the line when it has a recognisable marker.
	Level string `json:"level"`
}

// LogResponse is the log endpoint's payload.
type LogResponse struct {
	Files   []string  `json:"files"`
	File    string    `json:"file"`
	Lines   []LogLine `json:"lines"`
	Matched int       `json:"matched"`
	Scanned int       `json:"scanned"`
	Error   string    `json:"error,omitempty"`
}

const (
	maxLogTail     = 2000
	maxLogLineSize = 16 << 10
)

// handleLogs tails a configured log file, filtering by substring and level.
//
// The file list is fixed at startup (Options.LogFiles): a request cannot name an
// arbitrary path, so the endpoint cannot be turned into a file reader for the
// rest of the disk. Every line passes through the secret redactor before it is
// returned.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.authenticate(r) == authNone {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	q := r.URL.Query()
	tail := atoiDefault(q.Get("tail"), 200)
	if tail < 1 {
		tail = 1
	}
	if tail > maxLogTail {
		tail = maxLogTail
	}
	filter := strings.ToLower(q.Get("q"))
	level := strings.ToUpper(q.Get("level"))
	name := q.Get("file")

	resp := LogResponse{Files: s.logNames()}
	path, ok := s.logPath(name)
	if !ok {
		resp.Error = "unknown log file"
		writeJSON(w, http.StatusBadRequest, resp)
		return
	}
	resp.File = name
	lines, matched, scanned, err := s.tail(path, tail, filter, level)
	if err != nil {
		resp.Error = err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Lines, resp.Matched, resp.Scanned = lines, matched, scanned
	writeJSON(w, http.StatusOK, resp)
}

// logNames returns the file names the viewer offers, skipping missing files.
func (s *Server) logNames() []string {
	out := make([]string, 0, len(s.opts.LogFiles))
	for _, p := range s.opts.LogFiles {
		if _, err := os.Stat(p); err == nil {
			out = append(out, filepath.Base(p))
		}
	}
	return out
}

// logPath resolves a requested name against the configured list. Comparison is
// on the base name, so "..", an absolute path or a symlink inside the name
// cannot escape the list.
func (s *Server) logPath(name string) (string, bool) {
	if name == "" {
		if len(s.opts.LogFiles) == 0 {
			return "", false
		}
		name = filepath.Base(s.opts.LogFiles[0])
	}
	for _, p := range s.opts.LogFiles {
		if filepath.Base(p) == name {
			return p, true
		}
	}
	return "", false
}

// tail reads the last n lines that match, scanning the file once. A log file on
// a Pi can be hundreds of megabytes, so the read is streamed and only the
// retained window is held in memory.
func (s *Server) tail(path string, n int, filter, level string) ([]LogLine, int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("cannot open the log: %w", err)
	}
	defer f.Close()

	ring := make([]LogLine, 0, n)
	matched, scanned := 0, 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxLogLineSize)
	for sc.Scan() {
		scanned++
		text := s.redact(sc.Text())
		lvl := lineLevel(text)
		if filter != "" && !strings.Contains(strings.ToLower(text), filter) {
			continue
		}
		if level != "" && lvl != level {
			continue
		}
		matched++
		ring = append(ring, LogLine{N: scanned, Text: text, Level: lvl})
		if len(ring) > n {
			ring = ring[1:]
		}
	}
	if err := sc.Err(); err != nil {
		return nil, matched, scanned, fmt.Errorf("cannot read the log: %w", err)
	}
	return ring, matched, scanned, nil
}

// lineLevel extracts a severity from the common log shapes: "level=info",
// "[warn]", "WARN:", "2026-11-02 INFO started", "[ERROR]".
//
// ERROR is checked first so a line that both warns and errors is reported as an
// error, and the bare-word form only matches at the start of a token so the
// word "warn" inside a message cannot promote a line.
func lineLevel(text string) string {
	upper := strings.ToUpper(text)
	for _, lvl := range []string{"ERROR", "WARN", "INFO", "DEBUG"} {
		token := " " + lvl
		for _, form := range []string{
			"LEVEL=" + lvl,
			"[" + lvl + "]",
			token + ":",
			token + " ",
			"[" + lvl,
		} {
			if strings.Contains(upper, form) {
				return lvl
			}
		}
	}
	if strings.HasPrefix(upper, "WARN:") || strings.HasPrefix(upper, "ERROR:") {
		return strings.SplitN(upper, ":", 2)[0]
	}
	return ""
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

// handleLogin exchanges the dashboard token for a session cookie.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Bound the form: a login is a few dozen bytes, and the body limit keeps
	// a malformed request from buffering.
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	ip := clientIP(false, r)
	if !s.lim.allow(ip) {
		wait := s.lim.retryAfter(ip).Round(time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		http.Error(w, "too many attempts; try again in "+wait.String(), http.StatusTooManyRequests)
		return
	}
	presented := r.FormValue("token")
	if !secrets.Equal(s.opts.Token, presented) {
		s.lim.fail(ip)
		http.Redirect(w, r, "/?error=1", http.StatusSeeOther)
		return
	}
	s.lim.succeed(ip)
	id, err := s.sess.issue(s.opts.Now())
	if err != nil {
		http.Error(w, "cannot issue a session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		// A LAN deployment is http://; Secure is set only when the request
		// arrived over TLS so the cookie is not silently dropped.
		Secure: r.TLS != nil,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sess.drop(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleIndex renders the login form or the panel.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if s.authenticate(r) == authNone {
		s.renderLogin(w, r)
		return
	}
	_, _ = io.WriteString(w, indexHTML)
}

// htmlEscape is the minimal escaper the two inline templates need. Templates
// are constants, so values are limited to the error flag.
func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request) {
	failed := r.URL.Query().Get("error") != ""
	note := ""
	if failed {
		note = `<p class="err">That token was not accepted.</p>`
	}
	_, _ = io.WriteString(w, strings.Replace(loginHTML, "{{NOTE}}", note, 1))
}

// localAddrsForDisplay is a small helper for the login page footer.
func localAddrsForDisplay() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		ip, _, err := net.ParseCIDR(a.String())
		if err != nil || ip.To4() == nil || ip.IsLoopback() {
			continue
		}
		out = append(out, ip.String())
	}
	sort.Strings(out)
	return out
}
