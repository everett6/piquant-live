package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunExtraction_ScoresOnceNormalizesAndRecordsBadJSON(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
			Format   map[string]any             `json:"format"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Format == nil {
			t.Error("expected a JSON schema in format")
		}
		last := req.Messages[len(req.Messages)-1].Content
		content := `{"event_type":"weird","assets":[" btc "],"direction":3,"magnitude":-1,"confidence":0.5,"novelty":2,"horizon":"days"}`
		if strings.Contains(last, "garbled") {
			content = "not json"
		}
		b, _ := json.Marshal(map[string]any{"message": map[string]string{"content": content}})
		fmt.Fprint(w, string(b))
	}))
	defer srv.Close()

	dir := t.TempDir()
	news, _ := newDailyStore(filepath.Join(dir, "news"))
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	_ = news.Add(now, NewsRow{ID: "a1", ObservedAt: now, Title: "Bitcoin ETF inflows surge"})
	_ = news.Add(now, NewsRow{ID: "b2", ObservedAt: now.Add(time.Minute), Title: "garbled"})
	_ = news.Flush()
	gd, _ := newDailyStore(filepath.Join(dir, "gdelt", "macro"))
	_ = gd.Add(now, GDELTRow{SeenAt: now.Add(-time.Hour), Title: "Fed holds rates", URL: "https://g.test/1"})
	_ = gd.Flush()

	x := &Extractor{Endpoint: srv.URL, Model: "test", Client: srv.Client()}
	n, err := RunExtraction(context.Background(), x, dir, 0, nil)
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	var rows []ExtractRow
	_ = ReadRows(filepath.Join(dir, "extract", "2026-11-10.jsonl.gz"), func(b []byte) error {
		var r ExtractRow
		_ = json.Unmarshal(b, &r)
		rows = append(rows, r)
		return nil
	})
	if len(rows) != 3 || rows[0].ID != "b2" || rows[2].Source != "gdelt/macro" {
		t.Fatalf("expected live news newest-first then GDELT, got %+v", rows)
	}
	if rows[0].ParseErr == "" {
		t.Fatal("garbled output must be recorded with ParseErr")
	}
	s := rows[1].Signal
	if s.EventType != "other" || s.Direction != 1 || s.Magnitude != 0 || s.Novelty != 1 || s.Assets[0] != "BTC" {
		t.Fatalf("signal not normalized: %+v", s)
	}
	if n, _ := RunExtraction(context.Background(), x, dir, 0, nil); n != 0 || calls.Load() != 3 {
		t.Fatalf("second pass must skip done headlines, n=%d calls=%d", n, calls.Load())
	}
}

func TestRunExtraction_StopsWhenServerDown(t *testing.T) {
	dir := t.TempDir()
	news, _ := newDailyStore(filepath.Join(dir, "news"))
	now := time.Now().UTC()
	_ = news.Add(now, NewsRow{ID: "a1", ObservedAt: now, Title: "x"})
	_ = news.Flush()
	x := &Extractor{Endpoint: "http://127.0.0.1:1", Model: "m", Client: &http.Client{Timeout: time.Second}}
	if n, err := RunExtraction(context.Background(), x, dir, 0, nil); err == nil || n != 0 {
		t.Fatalf("expected error and nothing recorded, n=%d err=%v", n, err)
	}
}

func TestRunExtractionFile_ScoresListedHeadlinesOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["think"] != false {
			t.Error("expected think=false")
		}
		fmt.Fprint(w, `{"message":{"content":"{\"event_type\":\"etf_flows\",\"assets\":[\"BTC\"],\"direction\":0.5,\"magnitude\":0.5,\"confidence\":0.5,\"novelty\":0.5,\"horizon\":\"days\"}"}}`)
	}))
	defer srv.Close()
	dir := t.TempDir()
	in := filepath.Join(dir, "in.jsonl")
	_ = os.WriteFile(in, []byte(`{"id":"x1","title":"ETF inflows","at":"2024-01-02T03:04:05Z","src":"cni"}
{"id":"x1","title":"ETF inflows","at":"2024-01-02T03:04:05Z","src":"cni"}
{"id":"x2","title":"Fed holds","at":"2024-01-03T03:04:05Z","src":"cni"}
`), 0o644)
	x := &Extractor{Endpoint: srv.URL, Model: "m", Client: srv.Client()}
	out := filepath.Join(dir, "teacher")
	if n, err := RunExtractionFile(context.Background(), x, in, out, 0, nil); err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if n, _ := RunExtractionFile(context.Background(), x, in, out, 0, nil); n != 0 || calls.Load() != 2 {
		t.Fatalf("rerun must skip scored IDs, n=%d calls=%d", n, calls.Load())
	}
}
