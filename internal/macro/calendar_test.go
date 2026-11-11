package macro

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMacroGuardianOfflineFallback(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewMacroGuardian(tmpDir, "http://invalid-non-existent-url.local")

	if len(g.events) == 0 {
		t.Fatal("expected embedded fallback events to be initialized, got 0")
	}

	st := g.Evaluate(time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC))
	if st.EventCount == 0 {
		t.Errorf("expected EventCount > 0, got %d", st.EventCount)
	}
	if st.NextEvent == nil {
		t.Errorf("expected NextEvent to be populated")
	} else if st.NextEvent.Title != "Core CPI m/m" && st.NextEvent.Title != "CPI y/y" {
		t.Logf("next event: %s at %s", st.NextEvent.Title, st.NextEvent.DateTime)
	}
}

func TestMacroGuardianPhaseTransitions(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewMacroGuardian(tmpDir, "http://invalid.local")

	eventTime := time.Date(2026, 11, 10, 13, 30, 0, 0, time.UTC)
	g.events = []MacroEvent{
		{
			Title:    "US Core CPI m/m",
			Country:  "USD",
			DateTime: eventTime,
			Impact:   ImpactHigh,
		},
	}

	// 1. T - 30 minutes: NOMINAL
	t1 := eventTime.Add(-30 * time.Minute)
	s1 := g.Evaluate(t1)
	if s1.Phase != PhaseNominal || !s1.AllowQuoting || s1.SpreadMultiplier != 1.0 {
		t.Errorf("expected PhaseNominal at T-30m, got phase=%s quoting=%v spread=%v", s1.PhaseName, s1.AllowQuoting, s1.SpreadMultiplier)
	}

	// 2. T - 10 minutes: PRE_EVENT_DEFENSE
	t2 := eventTime.Add(-10 * time.Minute)
	s2 := g.Evaluate(t2)
	if s2.Phase != PhasePreEvent || !s2.AllowQuoting || s2.SpreadMultiplier != 2.5 || s2.LeverageMultiplier != 0.5 {
		t.Errorf("expected PhasePreEvent at T-10m, got phase=%s quoting=%v spread=%v lev=%v", s2.PhaseName, s2.AllowQuoting, s2.SpreadMultiplier, s2.LeverageMultiplier)
	}

	// 3. T - 1 minute: EVENT_HALT (CRITICAL DEFENSE)
	t3 := eventTime.Add(-1 * time.Minute)
	s3 := g.Evaluate(t3)
	if s3.Phase != PhaseEventHalt || s3.AllowQuoting {
		t.Errorf("expected PhaseEventHalt with AllowQuoting=false at T-1m, got phase=%s quoting=%v", s3.PhaseName, s3.AllowQuoting)
	}

	// 4. T + 2 minutes: EVENT_HALT
	t4 := eventTime.Add(2 * time.Minute)
	s4 := g.Evaluate(t4)
	if s4.Phase != PhaseEventHalt || s4.AllowQuoting {
		t.Errorf("expected PhaseEventHalt with AllowQuoting=false at T+2m, got phase=%s quoting=%v", s4.PhaseName, s4.AllowQuoting)
	}

	// 5. T + 10 minutes: POST_EVENT_DRIFT
	t5 := eventTime.Add(10 * time.Minute)
	s5 := g.Evaluate(t5)
	if s5.Phase != PhasePostDrift || !s5.AllowQuoting || s5.SpreadMultiplier != 1.5 {
		t.Errorf("expected PhasePostDrift at T+10m, got phase=%s quoting=%v spread=%v", s5.PhaseName, s5.AllowQuoting, s5.SpreadMultiplier)
	}

	// 6. T + 45 minutes: NOMINAL (back to normal)
	t6 := eventTime.Add(45 * time.Minute)
	s6 := g.Evaluate(t6)
	if s6.Phase != PhaseNominal || !s6.AllowQuoting || s6.SpreadMultiplier != 1.0 {
		t.Errorf("expected PhaseNominal at T+45m, got phase=%s quoting=%v", s6.PhaseName, s6.AllowQuoting)
	}
}

func TestMacroGuardianSyncMockServer(t *testing.T) {
	mockPayload := `[
		{
			"title": "Fed Chair Powell Speaks",
			"country": "USD",
			"date": "2026-11-12T10:00:00-05:00",
			"impact": "High",
			"forecast": "",
			"previous": ""
		},
		{
			"title": "ECB President Lagarde Speaks",
			"country": "EUR",
			"date": "2026-11-12T09:00:00+01:00",
			"impact": "High"
		}
	]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mockPayload))
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	g := NewMacroGuardian(tmpDir, srv.URL)

	err := g.Sync(context.Background())
	if err != nil {
		t.Fatalf("expected successful sync, got err: %v", err)
	}

	found := false
	for _, ev := range g.events {
		if ev.Title == "Fed Chair Powell Speaks" {
			found = true
			if ev.Country != "USD" || ev.Impact != ImpactHigh {
				t.Errorf("unexpected event attrs: %+v", ev)
			}
			break
		}
	}

	if !found {
		t.Errorf("expected to find synced event 'Fed Chair Powell Speaks'")
	}
}
