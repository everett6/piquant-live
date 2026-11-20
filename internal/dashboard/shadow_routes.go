package dashboard

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	autoDiscoverStateFile = "auto_discover_state.json"
	shadowCandidatesFile  = "shadow_candidates.json"
	shadowPaperStateFile  = "shadow_paper_state.json"
)

type shadowQueueResponse struct {
	State            any      `json:"state"`
	ShadowCandidates any      `json:"shadow_candidates"`
	ShadowPaper      any      `json:"shadow_paper"`
	MissingFiles     []string `json:"missing_files"`
	ResultsDir       string   `json:"results_dir"`
}

func resolveResearchResultsDir() string {
	if env := strings.TrimSpace(os.Getenv("PIQUANT_RESEARCH_RESULTS_DIR")); env != "" {
		return env
	}
	candidates := []string{
		filepath.Join("research", "quant_lab", "results"),
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, "PiQuant", "piquant-live", "research", "quant_lab", "results"))
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}
	return candidates[0]
}

func readOptionalJSON(path string, fallback any) (any, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return fallback, true
	}
	var decoded any
	if err := json.Unmarshal(b, &decoded); err != nil || decoded == nil {
		return fallback, false
	}
	return decoded, false
}

// handlePaperShadowQueue exposes the quarantined alpha-discovery state, shadow
// candidate queue, and shadow paper evaluation state without modifying or executing them.
func (s *Server) handlePaperShadowQueue(w http.ResponseWriter, r *http.Request) {
	if s.authenticate(r) == authNone {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	resultsDir := resolveResearchResultsDir()
	missing := []string{}

	state, stateMissing := readOptionalJSON(filepath.Join(resultsDir, autoDiscoverStateFile), nil)
	if stateMissing {
		missing = append(missing, autoDiscoverStateFile)
	}

	candidates, candidatesMissing := readOptionalJSON(filepath.Join(resultsDir, shadowCandidatesFile), []any{})
	if candidatesMissing {
		missing = append(missing, shadowCandidatesFile)
	}

	shadowPaper, paperMissing := readOptionalJSON(filepath.Join(resultsDir, shadowPaperStateFile), nil)
	if paperMissing {
		missing = append(missing, shadowPaperStateFile)
	}

	writeJSON(w, http.StatusOK, shadowQueueResponse{
		State:            state,
		ShadowCandidates: candidates,
		ShadowPaper:      shadowPaper,
		MissingFiles:     missing,
		ResultsDir:       resultsDir,
	})
}
