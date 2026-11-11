package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Signal is the LLM's structured reading of one headline: the "news matrix" row.
type Signal struct {
	EventType  string   `json:"event_type"`
	Assets     []string `json:"assets"`
	Direction  float64  `json:"direction"`  // -1 bearish .. +1 bullish
	Magnitude  float64  `json:"magnitude"`  // 0 trivial .. 1 market-moving
	Confidence float64  `json:"confidence"` // 0..1
	Novelty    float64  `json:"novelty"`    // 0 rehash/opinion .. 1 new fact
	Horizon    string   `json:"horizon"`
}

// ExtractRow links a Signal to the stored headline it came from.
type ExtractRow struct {
	ID       string    `json:"id"`
	Source   string    `json:"src"` // "news" or "gdelt/<label>"
	At       time.Time `json:"at"`  // when the headline was first seen
	Title    string    `json:"title"`
	Model    string    `json:"model"`
	Prompt   string    `json:"prompt_ver"`
	Signal   Signal    `json:"signal"`
	Latency  float64   `json:"latency_s"`
	ParseErr string    `json:"parse_err,omitempty"`
}

// PromptVersion changes whenever extractionSystem or the examples change.
const PromptVersion = "v2"

var eventTypes = []string{"regulation", "enforcement", "hack_exploit", "etf_flows", "macro_rates",
	"macro_inflation", "macro_trade", "adoption", "exchange_ops", "market_move", "other"}

const extractionSystem = "You score crypto market news for a BTC/ETH trading model. Return JSON only.\n" +
	"event_type decision tests: regulation = a binding rule, rule proposal, final guidance, exemptive relief or agency approval. " +
	"enforcement = lawsuits, charges, fines, arrests. hack_exploit = hacks, exploits, stolen funds, hijacked sites. " +
	"etf_flows = ETF inflows/outflows/filings/approvals. macro_rates = a central bank rate decision, minutes or policy statement. " +
	"macro_inflation = CPI/PCE/jobs data releases. macro_trade = tariffs, sanctions, trade policy. " +
	"adoption = companies or governments buying, listing for clients, or integrating crypto. exchange_ops = exchange listings, outages, withdrawals. " +
	"market_move = reports of price moves or liquidations. other = meetings, roundtables, statistics, surveys, MOUs, personnel, PR, opinion, analysis, predictions.\n" +
	"assets: tickers the news is about (BTC, ETH, SOL...). Use ALL only when it touches crypto markets broadly. " +
	"Name the token when the headline names one. Non-crypto enforcement or routine bank actions get assets [], direction 0, magnitude <= 0.1.\n" +
	"direction (-1..+1, effect on the listed assets' price, not the topic's tone) is 0 unless the headline states a concrete change " +
	"(a decision outcome, a flow, a price move, a hack). Minutes or statements without content, roundtables, scheduled releases and routine approvals get direction 0.\n" +
	"magnitude anchors: 0 admin; 0.1 routine enforcement or approval; 0.2 single-protocol or small-cap news (hard cap, assets = that token); " +
	"0.3-0.4 notable firm adoption or rule proposals; 0.5-0.7 major rule, ETF flows over $500M, central bank decision; 0.8-1.0 systemic shock.\n" +
	"confidence: how sure you are about direction, 0..1. novelty: 0 rehash/opinion .. 1 genuinely new fact. " +
	"Opinion/prediction pieces get novelty <= 0.2, magnitude <= 0.2, confidence <= 0.3."

var extractionExamples = []struct {
	in  string
	out Signal
}{
	{"SEC sues major exchange for operating unregistered securities platform",
		Signal{"enforcement", []string{"ALL"}, -0.6, 0.6, 0.7, 0.9, "days"}},
	{"Bitcoin could reach $200k, says fund manager",
		Signal{"other", []string{"BTC"}, 0.2, 0.05, 0.2, 0.1, "weeks"}},
	{"Federal Reserve announces enforcement action against regional bank holding company",
		Signal{"enforcement", []string{}, 0, 0.05, 0.6, 0.5, "days"}},
	{"SEC to host roundtable on crypto custody next month",
		Signal{"other", []string{"ALL"}, 0, 0.05, 0.5, 0.3, "weeks"}},
	{"Payments giant integrates stablecoin settlement on Monad network",
		Signal{"adoption", []string{"MON"}, 0.2, 0.2, 0.5, 0.7, "days"}},
	{"Spot bitcoin ETFs see $1.1B daily outflows as price slides",
		Signal{"etf_flows", []string{"BTC"}, -0.6, 0.6, 0.7, 0.8, "days"}},
}

func signalSchema() map[string]any {
	num := map[string]any{"type": "number"}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"event_type": map[string]any{"type": "string", "enum": eventTypes},
			"assets":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"direction":  num, "magnitude": num, "confidence": num, "novelty": num,
			"horizon": map[string]any{"type": "string", "enum": []string{"minutes", "hours", "days", "weeks"}},
		},
		"required": []string{"event_type", "assets", "direction", "magnitude", "confidence", "novelty", "horizon"},
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// normalize clamps ranges and maps unknown labels to "other".
func (s *Signal) normalize() {
	s.Direction = clamp(s.Direction, -1, 1)
	s.Magnitude = clamp(s.Magnitude, 0, 1)
	s.Confidence = clamp(s.Confidence, 0, 1)
	s.Novelty = clamp(s.Novelty, 0, 1)
	ok := false
	for _, e := range eventTypes {
		ok = ok || s.EventType == e
	}
	if !ok {
		s.EventType = "other"
	}
	for i := range s.Assets {
		s.Assets[i] = strings.ToUpper(strings.TrimSpace(s.Assets[i]))
	}
}

// Extractor sends headlines to a local Ollama server.
type Extractor struct {
	Endpoint string // e.g. http://127.0.0.1:11434
	Model    string
	Client   *http.Client
}

// Extract returns the structured signal for one headline.
func (x *Extractor) Extract(ctx context.Context, title string) (Signal, error) {
	msgs := []map[string]string{{"role": "system", "content": extractionSystem}}
	for _, ex := range extractionExamples {
		out, _ := json.Marshal(ex.out)
		msgs = append(msgs, map[string]string{"role": "user", "content": ex.in},
			map[string]string{"role": "assistant", "content": string(out)})
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": title})
	body, _ := json.Marshal(map[string]any{
		"model": x.Model, "stream": false, "format": signalSchema(),
		"options": map[string]any{"temperature": 0}, "messages": msgs,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(x.Endpoint, "/")+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return Signal{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := x.Client.Do(req)
	if err != nil {
		return Signal{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return Signal{}, fmt.Errorf("ollama HTTP %d: %s", resp.StatusCode, cleanText(string(raw), 200))
	}
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Signal{}, err
	}
	var s Signal
	if err := json.Unmarshal([]byte(out.Message.Content), &s); err != nil {
		return Signal{}, fmt.Errorf("bad model JSON: %w", err)
	}
	s.normalize()
	return s, nil
}

type pendingHeadline struct {
	id, src, title string
	at             time.Time
}

// loadPending lists stored headlines (live news first, newest first, then GDELT) with no extraction yet.
func loadPending(dir string, done map[string]bool) []pendingHeadline {
	var live, hist []pendingHeadline
	files, _ := filepath.Glob(filepath.Join(dir, "news", "*.jsonl.gz"))
	for _, f := range files {
		_ = ReadRows(f, func(b []byte) error {
			var r NewsRow
			if json.Unmarshal(b, &r) == nil && r.Title != "" && !done[r.ID] {
				done[r.ID] = true // dedup within this pass too
				live = append(live, pendingHeadline{r.ID, "news", r.Title, r.ObservedAt})
			}
			return nil
		})
	}
	gfiles, _ := filepath.Glob(filepath.Join(dir, "gdelt", "*", "*.jsonl.gz"))
	for _, f := range gfiles {
		src := "gdelt/" + filepath.Base(filepath.Dir(f))
		_ = ReadRows(f, func(b []byte) error {
			var r GDELTRow
			if json.Unmarshal(b, &r) != nil || r.Title == "" {
				return nil
			}
			id := headlineID(r.URL, r.Title)
			if !done[id] {
				done[id] = true
				hist = append(hist, pendingHeadline{id, src, r.Title, r.SeenAt})
			}
			return nil
		})
	}
	sort.Slice(live, func(a, b int) bool { return live[a].at.After(live[b].at) })
	sort.Slice(hist, func(a, b int) bool { return hist[a].at.After(hist[b].at) })
	return append(live, hist...)
}

func loadDone(outDir string) map[string]bool {
	done := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(outDir, "*.jsonl.gz"))
	for _, f := range files {
		_ = ReadRows(f, func(b []byte) error {
			var r struct {
				ID     string `json:"id"`
				Prompt string `json:"prompt_ver"`
			}
			if json.Unmarshal(b, &r) == nil && r.ID != "" && r.Prompt == PromptVersion {
				done[r.ID] = true
			}
			return nil
		})
	}
	return done
}

// RunExtraction processes every stored headline without a signal, writing
// <dir>/extract/<day>.jsonl.gz (by headline time). It returns the number processed.
// Headlines whose model output fails to parse are recorded with ParseErr so they are not retried forever.
func RunExtraction(ctx context.Context, x *Extractor, dir string, limit int, progress func(string)) (int, error) {
	outDir := filepath.Join(dir, "extract")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return 0, err
	}
	store, err := newDailyStore(outDir)
	if err != nil {
		return 0, err
	}
	defer store.Flush()
	pending := loadPending(dir, loadDone(outDir))
	if limit > 0 && len(pending) > limit {
		pending = pending[:limit]
	}
	n := 0
	for i, h := range pending {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		start := time.Now()
		sig, err := x.Extract(ctx, h.title)
		row := ExtractRow{ID: h.id, Source: h.src, At: h.at, Title: h.title, Model: x.Model,
			Prompt: PromptVersion, Signal: sig, Latency: time.Since(start).Seconds()}
		if err != nil {
			if ctx.Err() != nil || !strings.HasPrefix(err.Error(), "bad model JSON") {
				return n, err // server down etc.: stop, retry on the next run
			}
			row.ParseErr = err.Error()
		}
		_ = store.Add(h.at, row)
		n++
		if n%20 == 0 {
			if err := store.Flush(); err != nil {
				return n, err
			}
			if progress != nil {
				progress(fmt.Sprintf("%d/%d headlines extracted", i+1, len(pending)))
			}
		}
	}
	return n, nil
}
