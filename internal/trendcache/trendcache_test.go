package trendcache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"piquant-live/internal/carry"
	"piquant-live/internal/trend"
)

func TestTrendCacheTrendState(t *testing.T) {
	ttmpDir := t.TempDir()
	path := filepath.Join(ttmpDir, "trend_state.json")

	// Create initial state
	st := trend.NewState("2026-01-01")
	st.Seed([]trend.Day{{Date: "2026-01-01", Close: 100}})
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}

	cache := NewTrendCache()

	// First load - cache miss
	st1, err := cache.GetTrendState(path)
	if err != nil {
		t.Fatal(err)
	}
	if st1 == nil {
		t.Fatal("expected state, got nil")
	}
	if st1.Registered != "2026-01-01" {
		t.Fatalf("expected registered 2026-01-01, got %s", st1.Registered)
	}

	// Second load - cache hit
	st2, err := cache.GetTrendState(path)
	if err != nil {
		t.Fatal(err)
	}
	if st2 != st1 {
		t.Fatal("expected same pointer on cache hit")
	}

	// Modify file - should invalidate cache
	time.Sleep(10 * time.Millisecond) // ensure mtime changes
	st.Seed([]trend.Day{{Date: "2026-01-02", Close: 101}})
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}

	st3, err := cache.GetTrendState(path)
	if err != nil {
		t.Fatal(err)
	}
	if st3 == st1 {
		t.Fatal("expected new state after file modification")
	}
	if st3.LastDate != "2026-01-02" {
		t.Fatalf("expected last date 2026-01-02, got %s", st3.LastDate)
	}
}

func TestTrendCacheCarryState(t *testing.T) {
	ttmpDir := t.TempDir()
	path := filepath.Join(ttmpDir, "carry_state.json")

	// Create initial carry state
	cs := &carry.State{
		Days: []carry.DayRec{
			{Date: "2026-01-01", Ret: 0.001},
		},
	}
	if err := cs.Save(path); err != nil {
		t.Fatal(err)
	}

	cache := NewTrendCache()

	// First load
	cs1, err := cache.GetCarryState(path)
	if err != nil {
		t.Fatal(err)
	}
	if cs1 == nil || len(cs1.Days) != 1 {
		t.Fatal("expected carry state with 1 day")
	}

	// Second load - cache hit
	cs2, err := cache.GetCarryState(path)
	if err != nil {
		t.Fatal(err)
	}
	if cs2 != cs1 {
		t.Fatal("expected same pointer on cache hit")
	}

	// Test non-existent file
	cs3, err := cache.GetCarryState(filepath.Join(ttmpDir, "nonexistent.json"))
	if err != nil {
		t.Fatalf("expected nil error for non-existent file, got %v", err)
	}
	if cs3 != nil {
		t.Fatalf("expected nil state for non-existent file, got %+v", cs3)
	}
}

func TestTrendCacheDTB3Rates(t *testing.T) {
	ttmpDir := t.TempDir()
	path := filepath.Join(ttmpDir, "DTB3.csv")

	// Create DTB3 CSV
	if err := os.WriteFile(path, []byte("date,rate\n2026-01-01,5.0\n2026-01-02,5.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cache := NewTrendCache()

	// First load
	rates1, err := cache.GetDTB3Rates(path)
	if err != nil {
		t.Fatal(err)
	}
	if rates1["2026-01-01"] != 5.0 || rates1["2026-01-02"] != 5.1 {
		t.Fatalf("unexpected rates: %v", rates1)
	}

	// Second load - cache hit
	rates2, err := cache.GetDTB3Rates(path)
	if err != nil {
		t.Fatal(err)
	}
	if rates2 == nil || rates2["2026-01-01"] != 5.0 {
		t.Fatal("expected valid rates on cache hit")
	}

	// Test non-existent file
	rates3, err := cache.GetDTB3Rates(filepath.Join(ttmpDir, "nonexistent.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rates3) != 0 {
		t.Fatal("expected empty map for non-existent file")
	}
}

func TestTrendCacheInvalidation(t *testing.T) {
	ttmpDir := t.TempDir()
	path := filepath.Join(ttmpDir, "trend_state.json")

	st := trend.NewState("2026-01-01")
	st.Seed([]trend.Day{{Date: "2026-01-01", Close: 100}})
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}

	cache := NewTrendCache()

	// Load once
	st1, _ := cache.GetTrendState(path)
	if st1 == nil {
		t.Fatal("expected state")
	}

	// Invalidate trend only
	cache.InvalidateTrend()

	// Should reload
	st2, _ := cache.GetTrendState(path)
	if st2 == st1 {
		t.Fatal("expected reload after InvalidateTrend")
	}

	// Invalidate all
	cache.Invalidate()

	// Should reload again
	st3, _ := cache.GetTrendState(path)
	if st3 == st2 {
		t.Fatal("expected reload after Invalidate")
	}
}

func TestTrendCacheConcurrentAccess(t *testing.T) {
	ttmpDir := t.TempDir()
	path := filepath.Join(ttmpDir, "trend_state.json")

	st := trend.NewState("2026-01-01")
	st.Seed([]trend.Day{{Date: "2026-01-01", Close: 100}})
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}

	cache := NewTrendCache()

	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				_, _ = cache.GetTrendState(path)
			}
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}
