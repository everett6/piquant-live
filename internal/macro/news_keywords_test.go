package macro

import (
	"testing"
	"time"
)

func TestAnalyze_WholeWordMatching(t *testing.T) {
	p := NewNewsPipeline(t.TempDir(), "")
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		title, summary string
		wantExploit    bool
		wantSign       int // -1, 0, +1
	}{
		{"Ethereum Foundation announces global hackathon winners", "", false, 0},
		{"Canada pension fund highlights bond strategy", "", false, 0},
		{"Federal Reserve Board issues enforcement action with Ontario Bancorporation, Inc.",
			"The Board announced an enforcement action, highlighting gains in compliance.", false, 0},
		{"DeFi lending protocol hacked, $40M drained", "", true, -1},
		{"Bitcoin rally pushes price to record high", "", false, +1},
		{"Crypto liquidations top $1B as market slides", "", false, -1},
		{"EIP-7702 upgrade goes live on Ethereum mainnet", "", false, +1},
	}
	for _, c := range cases {
		it := p.Analyze(c.title, c.summary, "test", "", now)
		sign := 0
		if it.SentimentScore > 0.05 {
			sign = 1
		} else if it.SentimentScore < -0.05 {
			sign = -1
		}
		if it.IsAdverseExploit != c.wantExploit || sign != c.wantSign {
			t.Errorf("%q: exploit=%v sentiment=%.2f, want exploit=%v sign=%d", c.title, it.IsAdverseExploit, it.SentimentScore, c.wantExploit, c.wantSign)
		}
	}
}

func TestContainsWord(t *testing.T) {
	n := normalizeWords("SEC sues exchange; Fed's rate-cut odds rise (CPI)")
	for _, w := range []string{"sec", "sues", "rate cut", "cpi", "fed"} {
		if !containsWord(n, w) {
			t.Errorf("expected %q to match", w)
		}
	}
	for _, w := range []string{"ex", "ate", "pi", "rate cuts"} {
		if containsWord(n, w) {
			t.Errorf("did not expect %q to match", w)
		}
	}
}
