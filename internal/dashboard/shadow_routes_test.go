package dashboard

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestHandlePaperShadowQueue_AuthAndMethod(t *testing.T) {
	srv := newTestServer(t, func(o *Options) {
		o.DisableLivePing = true
	})
	h := srv.Handler()
	auth := map[string]string{"Authorization": "Bearer " + peerToken}

	if rec := get(t, h, http.MethodGet, "/api/paper/shadow-queue", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d, want 401", rec.Code)
	}

	if rec := get(t, h, http.MethodPost, "/api/paper/shadow-queue", auth, nil); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d, want 405", rec.Code)
	}
}

func TestHandlePaperShadowQueue_EmptyAndMissingDir(t *testing.T) {
	srv := newTestServer(t, func(o *Options) {
		o.DisableLivePing = true
	})
	h := srv.Handler()
	auth := map[string]string{"Authorization": "Bearer " + peerToken}
	wantMissing := []string{
		autoDiscoverStateFile,
		shadowCandidatesFile,
		shadowPaperStateFile,
	}

	for _, tc := range []struct {
		name string
		dir  string
	}{
		{name: "missing dir", dir: filepath.Join(t.TempDir(), "missing-results")},
		{name: "empty dir", dir: t.TempDir()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PIQUANT_RESEARCH_RESULTS_DIR", tc.dir)

			rec := get(t, h, http.MethodGet, "/api/paper/shadow-queue", auth, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET = %d, want 200 (%s)", rec.Code, rec.Body.String())
			}

			var raw map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			for _, k := range []string{"state", "shadow_candidates", "shadow_paper", "missing_files", "results_dir"} {
				if _, ok := raw[k]; !ok {
					t.Fatalf("response missing key %q: %v", k, raw)
				}
			}
			if raw["state"] != nil {
				t.Fatalf("state = %v, want nil", raw["state"])
			}
			if raw["shadow_paper"] != nil {
				t.Fatalf("shadow_paper = %v, want nil", raw["shadow_paper"])
			}
			cands, ok := raw["shadow_candidates"].([]any)
			if !ok || len(cands) != 0 {
				t.Fatalf("shadow_candidates = %#v, want empty slice", raw["shadow_candidates"])
			}
			if raw["results_dir"] != tc.dir {
				t.Fatalf("results_dir = %v, want %q", raw["results_dir"], tc.dir)
			}

			var typed shadowQueueResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &typed); err != nil {
				t.Fatalf("unmarshal typed: %v", err)
			}
			if !reflect.DeepEqual(typed.MissingFiles, wantMissing) {
				t.Fatalf("missing_files = %v, want %v", typed.MissingFiles, wantMissing)
			}
		})
	}
}

func TestHandlePaperShadowQueue_PopulatedFixtures(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIQUANT_RESEARCH_RESULTS_DIR", dir)

	stateJSON := `{
  "n_trials": 816,
  "runs": 3,
  "accepted": 1,
  "rejected": 17,
  "last_run_utc": "2026-11-19T00:36:12Z"
}`
	candidatesJSON := `[
  {
    "expr": "tanh(ema(btc_ret5, 10) * 2.0)",
    "source": "llamacpp",
    "role": "overlay",
    "accepted": true,
    "status": "SHADOW_PAPER_ONLY",
    "base_metrics": {"geo_monthly_pct": 4.25, "sharpe": 1.85}
  }
]`
	paperJSON := `{
  "updated_at": "2026-11-19T00:37:00Z",
  "active_candidates": 1,
  "cumulative_return_pct": 1.42
}`

	if err := os.WriteFile(filepath.Join(dir, autoDiscoverStateFile), []byte(stateJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, shadowCandidatesFile), []byte(candidatesJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, shadowPaperStateFile), []byte(paperJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newTestServer(t, func(o *Options) {
		o.DisableLivePing = true
	})
	auth := map[string]string{"Authorization": "Bearer " + peerToken}
	rec := get(t, srv.Handler(), http.MethodGet, "/api/paper/shadow-queue", auth, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var body struct {
		State            map[string]any   `json:"state"`
		ShadowCandidates []map[string]any `json:"shadow_candidates"`
		ShadowPaper      map[string]any   `json:"shadow_paper"`
		MissingFiles     []string         `json:"missing_files"`
		ResultsDir       string           `json:"results_dir"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.ResultsDir != dir {
		t.Fatalf("results_dir = %q, want %q", body.ResultsDir, dir)
	}
	if body.MissingFiles == nil || len(body.MissingFiles) != 0 {
		t.Fatalf("missing_files = %#v, want non-nil empty slice", body.MissingFiles)
	}
	if body.State["n_trials"] != float64(816) || body.State["runs"] != float64(3) {
		t.Fatalf("unexpected state: %v", body.State)
	}
	if len(body.ShadowCandidates) != 1 || body.ShadowCandidates[0]["status"] != "SHADOW_PAPER_ONLY" {
		t.Fatalf("unexpected shadow_candidates: %v", body.ShadowCandidates)
	}
	if body.ShadowPaper["active_candidates"] != float64(1) {
		t.Fatalf("unexpected shadow_paper: %v", body.ShadowPaper)
	}
}

func TestHandlePaperShadowQueue_PartialAndCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIQUANT_RESEARCH_RESULTS_DIR", dir)

	if err := os.WriteFile(filepath.Join(dir, autoDiscoverStateFile), []byte(`{"n_trials": 820}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, shadowCandidatesFile), []byte(`{corrupt json`), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newTestServer(t, func(o *Options) {
		o.DisableLivePing = true
	})
	auth := map[string]string{"Authorization": "Bearer " + peerToken}
	rec := get(t, srv.Handler(), http.MethodGet, "/api/paper/shadow-queue", auth, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var body struct {
		State            map[string]any `json:"state"`
		ShadowCandidates []any          `json:"shadow_candidates"`
		ShadowPaper      any            `json:"shadow_paper"`
		MissingFiles     []string       `json:"missing_files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.State["n_trials"] != float64(820) {
		t.Fatalf("state = %v", body.State)
	}
	if body.ShadowCandidates == nil || len(body.ShadowCandidates) != 0 {
		t.Fatalf("corrupt shadow_candidates = %#v, want empty slice", body.ShadowCandidates)
	}
	if body.ShadowPaper != nil {
		t.Fatalf("missing shadow_paper = %v, want nil", body.ShadowPaper)
	}
	if !reflect.DeepEqual(body.MissingFiles, []string{shadowPaperStateFile}) {
		t.Fatalf("missing_files = %v, want [%s]", body.MissingFiles, shadowPaperStateFile)
	}
}

func TestHandlePaperShadowQueue_HomeFallbackResolution(t *testing.T) {
	fakeHome := t.TempDir()
	fallbackDir := filepath.Join(fakeHome, "PiQuant", "piquant-live", "research", "quant_lab", "results")
	if err := os.MkdirAll(fallbackDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fallbackDir, autoDiscoverStateFile), []byte(`{"n_trials": 811}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Ensure cwd relative path does not intercept and env override is empty.
	t.Setenv("PIQUANT_RESEARCH_RESULTS_DIR", "")
	t.Setenv("HOME", fakeHome)

	srv := newTestServer(t, func(o *Options) {
		o.DisableLivePing = true
	})
	auth := map[string]string{"Authorization": "Bearer " + peerToken}
	rec := get(t, srv.Handler(), http.MethodGet, "/api/paper/shadow-queue", auth, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var body struct {
		State        map[string]any `json:"state"`
		MissingFiles []string       `json:"missing_files"`
		ResultsDir   string         `json:"results_dir"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.ResultsDir != fallbackDir {
		t.Fatalf("results_dir = %q, want %q", body.ResultsDir, fallbackDir)
	}
	if body.State["n_trials"] != float64(811) {
		t.Fatalf("state = %v", body.State)
	}
	if !reflect.DeepEqual(body.MissingFiles, []string{shadowCandidatesFile, shadowPaperStateFile}) {
		t.Fatalf("missing_files = %v", body.MissingFiles)
	}
}
