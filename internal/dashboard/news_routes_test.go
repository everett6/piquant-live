package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewsEndpoints_AuthRequired(t *testing.T) {
	s := newTestServer(t, nil)

	for _, path := range []string{"/api/news", "/api/news/sentiment", "/api/news/ingest"} {
		rec := get(t, s.Handler(), http.MethodGet, path, nil, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without auth got %d, want 401", path, rec.Code)
		}
	}
}

func TestNewsEndpoints_GetNews(t *testing.T) {
	s := newTestServer(t, nil)
	headers := map[string]string{"Authorization": "Bearer " + peerToken}

	rec := get(t, s.Handler(), http.MethodGet, "/api/news?limit=5", headers, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/news got %d, want 200", rec.Code)
	}

	var resp struct {
		Total int   `json:"total"`
		News  []any `json:"news"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode /api/news response: %v", err)
	}
	if resp.Total == 0 || len(resp.News) == 0 {
		t.Errorf("expected news items in response, got %d", resp.Total)
	}
}

func TestNewsEndpoints_GetSentiment(t *testing.T) {
	s := newTestServer(t, nil)
	headers := map[string]string{"Authorization": "Bearer " + peerToken}

	rec := get(t, s.Handler(), http.MethodGet, "/api/news/sentiment", headers, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/news/sentiment got %d, want 200", rec.Code)
	}

	var rep struct {
		MacroSentiment    float64            `json:"macro_sentiment"`
		SymbolSentiments  map[string]float64 `json:"symbol_sentiments"`
		SymbolBidKills    map[string]bool    `json:"symbol_bid_kills"`
		SpreadMultipliers map[string]float64 `json:"spread_multipliers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatalf("failed to decode sentiment report: %v", err)
	}
}

func TestNewsEndpoints_PostIngest(t *testing.T) {
	s := newTestServer(t, nil)

	body := []byte(`{
		"title": "Solana Lending Bridge Exploit: $80M Drained by Attacker",
		"summary": "Smart contract vulnerability exploited via flash loan manipulation.",
		"source": "DeFiSecurity",
		"url": "https://example.com/sec"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/news/ingest", bytes.NewReader(body))
	req.RemoteAddr = "192.168.1.55:4444"
	req.Header.Set("Authorization", "Bearer "+peerToken)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/news/ingest got %d, want 200, body: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		OK   bool `json:"ok"`
		Item struct {
			Category         string  `json:"category"`
			IsAdverseExploit bool    `json:"is_adverse_exploit"`
			SentimentScore   float64 `json:"sentiment_score"`
		} `json:"item"`
		Report struct {
			SymbolBidKills map[string]bool `json:"symbol_bid_kills"`
		} `json:"report"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal ingest response: %v", err)
	}

	if !resp.OK {
		t.Errorf("expected resp.OK == true")
	}
	if !resp.Item.IsAdverseExploit {
		t.Errorf("expected IsAdverseExploit == true for bridge exploit")
	}
	if !resp.Report.SymbolBidKills["SOLUSDT"] {
		t.Errorf("expected SymbolBidKills['SOLUSDT'] == true after exploit ingestion")
	}
}
