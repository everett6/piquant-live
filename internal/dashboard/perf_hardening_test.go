package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"piquant-live/internal/discovery"
)

func TestPathTraversalRejection(t *testing.T) {
	dataDir := t.TempDir()
	s := newTestServer(t, func(o *Options) {
		o.Role = discovery.RoleEngine
		o.DataDirs = []string{dataDir}
	})
	h := s.Handler()

	endpoints := []string{
		"/api/experiments/run",
		"/api/searches/run",
		"/api/searches/quantum",
	}
	badDatasets := []string{
		"",
		".",
		"/etc/passwd",
		"../../etc/passwd",
		"../outside.csv",
		filepath.Join(filepath.Dir(dataDir), "sibling", "file.csv"),
	}

	for _, ep := range endpoints {
		for _, ds := range badDatasets {
			payload, _ := json.Marshal(map[string]any{"dataset": ds})
			req := httptest.NewRequest(http.MethodPost, ep, bytes.NewReader(payload))
			req.RemoteAddr = "192.168.1.55:4444"
			req.Header.Set("Authorization", "Bearer "+peerToken)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s with dataset %q: got status %d, want 400 (body: %s)", ep, ds, rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "feasibility") {
				t.Errorf("%s with dataset %q: bypassed path containment check", ep, ds)
			}
		}
	}

	// Valid path inside DataDirs[0] should pass containment check (reaching feasibility check for experiment run).
	validFile := filepath.Join(dataDir, "valid.csv")
	if err := os.WriteFile(validFile, []byte("ts,price\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"dataset": validFile})
	req := httptest.NewRequest(http.MethodPost, "/api/experiments/run", bytes.NewReader(payload))
	req.RemoteAddr = "192.168.1.55:4444"
	req.Header.Set("Authorization", "Bearer "+peerToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "within configured data directories") {
		t.Fatalf("valid path inside DataDirs was rejected: %s", rec.Body.String())
	}
}

func TestMaxBytesReaderOnJSONPost(t *testing.T) {
	s := newTestServer(t, nil)
	h := s.Handler()

	oversized := `{"title":"` + strings.Repeat("a", 70*1024) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/news/ingest", strings.NewReader(oversized))
	req.RemoteAddr = "192.168.1.55:4444"
	req.Header.Set("Authorization", "Bearer "+peerToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized POST /api/news/ingest got %d, want 400", rec.Code)
	}
}

func TestHandlePeersAndSyncPeerEngine(t *testing.T) {
	s := newTestServer(t, nil)
	h := s.Handler()

	if rec := get(t, h, http.MethodGet, "/api/peers", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /api/peers = %d, want 401", rec.Code)
	}

	authHdr := map[string]string{"Authorization": "Bearer " + peerToken}
	rec := get(t, h, http.MethodGet, "/api/peers", authHdr, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated /api/peers = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /api/peers: %v", err)
	}
	if _, ok := body["peers"]; !ok {
		t.Fatalf("expected 'peers' key in /api/peers response")
	}

	// Verify syncPeerEngine uses PeerStatus.endpoint (populated from p.Endpoint()).
	mockEngine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+peerToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, Status{
			V:    Version,
			Role: discovery.RoleEngine,
			Name: "pi5-test",
			Mode: "advanced",
		})
	}))
	defer mockEngine.Close()

	peers := []PeerStatus{
		{
			Role:     discovery.RoleEngine,
			Name:     "pi5-test",
			Addr:     "127.0.0.1",
			Live:     true,
			endpoint: mockEngine.URL,
		},
	}
	remote := s.syncPeerEngine(context.Background(), peers)
	if remote == nil || remote.Name != "pi5-test" {
		t.Fatalf("syncPeerEngine failed to fetch via endpoint: got %+v", remote)
	}
}

func TestDryRunStateCaching(t *testing.T) {
	dataDir := t.TempDir()
	paperDir := filepath.Join(dataDir, "paper")
	if err := os.MkdirAll(paperDir, 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(paperDir, "dryrun_state.json")
	if err := os.WriteFile(statePath, []byte(`{"initial_capital":1234.56,"equity":1250.0}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newTestServer(t, func(o *Options) {
		o.DataDirs = []string{dataDir}
	})

	st1 := s.resolveDryRunState()
	if st1.InitialCapital != 1234.56 {
		t.Fatalf("first resolveDryRunState InitialCapital = %v, want 1234.56", st1.InitialCapital)
	}
	s.dryRunMu.Lock()
	cachedValid := s.dryRunCache.valid
	s.dryRunMu.Unlock()
	if !cachedValid {
		t.Fatalf("expected dryRunCache.valid == true after disk load")
	}

	// Second call with unchanged mtime+size returns cached state.
	st2 := s.resolveDryRunState()
	if st2.InitialCapital != 1234.56 {
		t.Fatalf("second resolveDryRunState InitialCapital = %v, want 1234.56", st2.InitialCapital)
	}

	// Update file with different size and mtime -> cache must invalidate.
	if err := os.WriteFile(statePath, []byte(`{"initial_capital":2345.6789,"equity":2400.12}`), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(statePath, future, future)

	st3 := s.resolveDryRunState()
	if st3.InitialCapital != 2345.6789 {
		t.Fatalf("updated resolveDryRunState InitialCapital = %v, want 2345.6789", st3.InitialCapital)
	}

	// /api/paper/dryrun should return the resolved state without re-reading disk differently.
	rec := get(t, s.Handler(), http.MethodGet, "/api/paper/dryrun", map[string]string{"Authorization": "Bearer " + peerToken}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/paper/dryrun got %d", rec.Code)
	}
	var resp struct {
		State struct {
			InitialCapital float64 `json:"initial_capital"`
		} `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.State.InitialCapital != 2345.6789 {
		t.Fatalf("/api/paper/dryrun state.initial_capital = %v, want 2345.6789", resp.State.InitialCapital)
	}
}

func TestTaxCachingAndExportCSV(t *testing.T) {
	dataDir := t.TempDir()
	paperDir := filepath.Join(dataDir, "paper")
	if err := os.MkdirAll(paperDir, 0o755); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(paperDir, "dryrun_journal.jsonl")
	if err := os.WriteFile(journalPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	s := newTestServer(t, func(o *Options) {
		o.DataDirs = []string{dataDir}
	})
	authHdr := map[string]string{"Authorization": "Bearer " + peerToken}

	rec1 := get(t, s.Handler(), http.MethodGet, "/api/tax?year=2026", authHdr, nil)
	if rec1.Code != http.StatusOK {
		t.Fatalf("/api/tax got %d", rec1.Code)
	}
	s.taxMu.Lock()
	cacheLen := len(s.taxCache)
	s.taxMu.Unlock()
	if cacheLen != 1 {
		t.Fatalf("expected 1 taxCache entry, got %d", cacheLen)
	}

	// CSV export with default rates (24.0, 9.3) should hit the same cache key.
	recCSV := get(t, s.Handler(), http.MethodGet, "/api/tax/export.csv?year=2026", authHdr, nil)
	if recCSV.Code != http.StatusOK {
		t.Fatalf("/api/tax/export.csv got %d", recCSV.Code)
	}
	s.taxMu.Lock()
	cacheLenAfterCSV := len(s.taxCache)
	s.taxMu.Unlock()
	if cacheLenAfterCSV != 1 {
		t.Fatalf("expected /api/tax/export.csv to reuse same taxCache entry, got %d entries", cacheLenAfterCSV)
	}
}

func TestNewsPipelineSingleton(t *testing.T) {
	s := newTestServer(t, nil)
	p1 := s.resolveNewsPipeline()
	p2 := s.resolveNewsPipeline()
	if p1 == nil || p1 != p2 {
		t.Fatalf("expected singleton *macro.NewsPipeline when DryRun == nil, got %p vs %p", p1, p2)
	}
}

func TestPaperReportsLatestNoDiskWriteOnGet(t *testing.T) {
	dataDir := t.TempDir()
	s := newTestServer(t, func(o *Options) {
		o.DataDirs = []string{dataDir}
	})
	authHdr := map[string]string{"Authorization": "Bearer " + peerToken}

	rec := get(t, s.Handler(), http.MethodGet, "/api/paper/reports/latest", authHdr, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/paper/reports/latest got %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /api/paper/reports/latest: %v", err)
	}
	if s, ok := body["report"].(string); !ok || s == "" {
		t.Fatalf("expected non-empty report string")
	}

	reportsDir := filepath.Join(dataDir, "paper", "reports")
	if entries, err := os.ReadDir(reportsDir); err == nil && len(entries) > 0 {
		t.Fatalf("GET /api/paper/reports/latest should not write files to %s, found %d", reportsDir, len(entries))
	}
}

func TestDatasetBoundsValidation(t *testing.T) {
	dataDir := t.TempDir()
	s := newTestServer(t, func(o *Options) {
		o.Role = discovery.RoleEngine
		o.DataDirs = []string{dataDir}
	})
	h := s.Handler()

	cases := []struct {
		endpoint string
		body     string
	}{
		{"/api/datasets/generate", `{"instrument":"BTCUSDT","records":300000}`},
		{"/api/datasets/capture", `{"symbol":"BTCUSDT","ticks":100000}`},
		{"/api/datasets/capture", `{"symbol":"BTCUSDT","duration_sec":7200}`},
		{"/api/datasets/capture", `{"symbol":"BTCUSDT","duration_sec":-5}`},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, tc.endpoint, strings.NewReader(tc.body))
		req.RemoteAddr = "192.168.1.55:4444"
		req.Header.Set("Authorization", "Bearer "+peerToken)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s: got %d, want 400 (body: %s)", tc.endpoint, tc.body, rec.Code, rec.Body.String())
		}
	}
}

func TestAuthStoreCaps(t *testing.T) {
	now := time.Now()
	sess := newSessionStore(time.Hour)
	for i := 0; i < maxSessions+25; i++ {
		if _, err := sess.issue(now.Add(time.Duration(i) * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	sess.mu.Lock()
	sessLen := len(sess.m)
	sess.mu.Unlock()
	if sessLen > maxSessions {
		t.Fatalf("sessionStore exceeded cap: len=%d > %d", sessLen, maxSessions)
	}

	lim := newLoginLimiter(5, time.Hour)
	for i := 0; i < maxLoginLimiterEntries+25; i++ {
		lim.fail(fmt.Sprintf("10.0.%d.%d", i/256, i%256))
	}
	lim.mu.Lock()
	limLen := len(lim.m)
	lim.mu.Unlock()
	if limLen > maxLoginLimiterEntries {
		t.Fatalf("loginLimiter exceeded cap: len=%d > %d", limLen, maxLoginLimiterEntries)
	}
}

func TestEventsGracefulShutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	s := newTestServer(t, func(o *Options) {
		o.Addr = addr
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- s.ListenAndServe(ctx)
	}()

	// Wait for server to accept connections.
	var resp *http.Response
	for i := 0; i < 40; i++ {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/api/events", nil)
		req.Header.Set("Authorization", "Bearer "+peerToken)
		resp, err = http.DefaultClient.Do(req)
		if err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("failed to connect to /api/events: %v", err)
	}
	defer resp.Body.Close()

	// Read a small chunk to confirm SSE stream is open, then cancel ctx.
	buf := make([]byte, 64)
	_, _ = resp.Body.Read(buf)

	start := time.Now()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ListenAndServe returned error on shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ListenAndServe stalled > 2s waiting for open SSE stream")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("shutdown took %v, expected prompt exit", elapsed)
	}
}
