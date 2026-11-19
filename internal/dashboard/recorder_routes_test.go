package dashboard

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestHandleRecorder(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(Options{Token: testToken, PeerToken: peerToken, CarryDir: filepath.Join(dir, "carry"),
		DisableLivePing: true})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	auth := map[string]string{"Authorization": "Bearer " + peerToken}

	if rec := get(t, h, http.MethodGet, "/api/recorder", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d, want 401", rec.Code)
	}

	// No status files yet: both keys present and null.
	var body map[string]any
	rec := get(t, h, http.MethodGet, "/api/recorder", auth, nil)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		t.Fatalf("empty dir = %d %s", rec.Code, rec.Body.String())
	}
	if body["ticks"] != nil || body["collector"] != nil {
		t.Fatalf("missing files should be null: %v", body)
	}

	// With a ticks status file (and a corrupt collector file, which stays null).
	ticks := `{"connected":{"coinbase":true,"kraken":false},"trades":{"BTC-USD":12}}`
	if err := os.WriteFile(filepath.Join(dir, "ticks_status.json"), []byte(ticks), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "status.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	body = nil
	rec = get(t, h, http.MethodGet, "/api/recorder", auth, nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	tk, ok := body["ticks"].(map[string]any)
	if !ok {
		t.Fatalf("ticks not decoded: %v", body)
	}
	if tk["connected"].(map[string]any)["coinbase"] != true {
		t.Fatalf("connected not passed through: %v", tk)
	}
	if age, ok := tk["age_seconds"].(float64); !ok || age < 0 || age > 60 {
		t.Fatalf("age_seconds = %v", tk["age_seconds"])
	}
	if body["collector"] != nil {
		t.Fatalf("corrupt collector file should be null: %v", body["collector"])
	}
}
