// Package trendcache provides mtime-based caching for trend, carry, DTB3, and macro HRP data.
// Extracted from internal/dashboard/server.go for reuse across CLI, paper, dashboard, and execute.
package trendcache

import (
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"piquant-live/internal/carry"
	"piquant-live/internal/trend"
)

// fileStamp captures file metadata for mtime-based invalidation.
type fileStamp struct {
	path        string
	modUnixNano int64
	size        int64
	exists      bool
}

type pairedStamp struct {
	main fileStamp
	bak  fileStamp
}

type trendDataCache struct {
	trendValid bool
	trendStamp pairedStamp
	trendState *trend.State

	carryValid bool
	carryStamp pairedStamp
	carryState *carry.State
	carryErr   error

	dtb3Valid bool
	dtb3Stamp fileStamp
	dtb3Rates map[string]float64

	macroValid     bool
	macroEqDir     string
	macroDTB3      fileStamp
	macroRatesHash uint64
	macroStamps    []fileStamp
	macroSum       trend.MacroSleeveSummary
	hasMacro       bool
}

// TrendCache provides mtime-based caching for trend-related data.
// Safe for concurrent use.
type TrendCache struct {
	mu     sync.RWMutex
	cache  trendDataCache
	hits   atomic.Uint64
	misses atomic.Uint64
}

// NewTrendCache creates a new cache instance.
func NewTrendCache() *TrendCache {
	return &TrendCache{}
}

// Hits returns the number of cache hits.
func (c *TrendCache) Hits() uint64 {
	return c.hits.Load()
}

// Misses returns the number of cache misses.
func (c *TrendCache) Misses() uint64 {
	return c.misses.Load()
}

func statFileStamp(path string) fileStamp {
	if path == "" {
		return fileStamp{}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{path: path}
	}
	return fileStamp{
		path:        path,
		modUnixNano: fi.ModTime().UnixNano(),
		size:        fi.Size(),
		exists:      true,
	}
}

func statPairedStamp(path string) pairedStamp {
	if path == "" {
		return pairedStamp{}
	}
	return pairedStamp{
		main: statFileStamp(path),
		bak:  statFileStamp(path + ".bak"),
	}
}

func macroStampsMatch(eqDir string, cached []fileStamp, cachedDTB3, curDTB3 fileStamp, cachedDir string, cachedRatesHash, curRatesHash uint64) bool {
	if cachedDir != eqDir || cachedDTB3 != curDTB3 || cachedRatesHash != curRatesHash || len(cached) != len(trend.MacroEquityTickers) {
		return false
	}
	for i, sym := range trend.MacroEquityTickers {
		if statFileStamp(filepath.Join(eqDir, sym+".csv")) != cached[i] {
			return false
		}
	}
	return true
}

// GetTrendState returns the trend state, using mtime-based caching.
// Returns (nil, nil) if the file doesn't exist.
func (c *TrendCache) GetTrendState(path string) (*trend.State, error) {
	stamp := statPairedStamp(path)
	c.mu.RLock()
	if c.cache.trendValid && c.cache.trendStamp == stamp {
		st := c.cache.trendState
		c.mu.RUnlock()
		c.hits.Add(1)
		return st, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	stamp = statPairedStamp(path)
	if c.cache.trendValid && c.cache.trendStamp == stamp {
		c.hits.Add(1)
		return c.cache.trendState, nil
	}
	c.misses.Add(1)
	st, err := trend.Load(path)
	if err != nil {
		return nil, err
	}
	c.cache.trendValid = true
	c.cache.trendStamp = stamp
	c.cache.trendState = st
	return st, nil
}

// GetCarryState returns the carry state, using mtime-based caching.
// Returns (nil, nil) if the file doesn't exist.
func (c *TrendCache) GetCarryState(path string) (*carry.State, error) {
	stamp := statPairedStamp(path)
	c.mu.RLock()
	if c.cache.carryValid && c.cache.carryStamp == stamp {
		st, err := c.cache.carryState, c.cache.carryErr
		c.mu.RUnlock()
		c.hits.Add(1)
		return st, err
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	stamp = statPairedStamp(path)
	if c.cache.carryValid && c.cache.carryStamp == stamp {
		c.hits.Add(1)
		return c.cache.carryState, c.cache.carryErr
	}
	c.misses.Add(1)
	st, err := carry.Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.cache.carryValid = true
			c.cache.carryStamp = stamp
			c.cache.carryState = nil
			c.cache.carryErr = nil
			return nil, nil
		}
		return nil, err
	}
	c.cache.carryValid = true
	c.cache.carryStamp = stamp
	c.cache.carryState = st
	c.cache.carryErr = nil
	return st, nil
}

// GetDTB3Rates returns the DTB3 rates, using mtime-based caching.
// Returns empty map if file doesn't exist.
func (c *TrendCache) GetDTB3Rates(path string) (map[string]float64, error) {
	stamp := statFileStamp(path)
	c.mu.RLock()
	if c.cache.dtb3Valid && c.cache.dtb3Stamp == stamp {
		rates := c.cache.dtb3Rates
		c.mu.RUnlock()
		c.hits.Add(1)
		return rates, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	stamp = statFileStamp(path)
	if c.cache.dtb3Valid && c.cache.dtb3Stamp == stamp {
		c.hits.Add(1)
		return c.cache.dtb3Rates, nil
	}
	c.misses.Add(1)
	rates, err := trend.LoadDTB3Rates(path)
	if err != nil {
		return nil, err
	}
	c.cache.dtb3Valid = true
	c.cache.dtb3Stamp = stamp
	c.cache.dtb3Rates = rates
	return rates, nil
}

// GetMacroHRP returns the macro HRP sleeve summary, using mtime-based caching.
// Returns (zero, false) if equities dir is empty or data unavailable.
func (c *TrendCache) GetMacroHRP(eqDir, dtb3Path string, rates map[string]float64) (trend.MacroSleeveSummary, bool) {
	if strings.TrimSpace(eqDir) == "" {
		return trend.MacroSleeveSummary{}, false
	}
	dtb3Stamp := statFileStamp(dtb3Path)
	ratesHash := hashRates(rates)
	c.mu.RLock()
	if c.cache.macroValid && macroStampsMatch(eqDir, c.cache.macroStamps, c.cache.macroDTB3, dtb3Stamp, c.cache.macroEqDir, c.cache.macroRatesHash, ratesHash) {
		sum, ok := c.cache.macroSum, c.cache.hasMacro
		c.mu.RUnlock()
		c.hits.Add(1)
		return sum, ok
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	dtb3Stamp = statFileStamp(dtb3Path)
	ratesHash = hashRates(rates)
	if c.cache.macroValid && macroStampsMatch(eqDir, c.cache.macroStamps, c.cache.macroDTB3, dtb3Stamp, c.cache.macroEqDir, c.cache.macroRatesHash, ratesHash) {
		c.hits.Add(1)
		return c.cache.macroSum, c.cache.hasMacro
	}
	c.misses.Add(1)
	curStamps := make([]fileStamp, len(trend.MacroEquityTickers))
	for i, sym := range trend.MacroEquityTickers {
		curStamps[i] = statFileStamp(filepath.Join(eqDir, sym+".csv"))
	}
	series, err := trend.LoadEquityCSVs(eqDir, trend.MacroEquityTickers)
	if err != nil {
		// Cache negative result with current stamps to avoid retry storm
		c.cache.macroValid = true
		c.cache.macroEqDir = eqDir
		c.cache.macroDTB3 = dtb3Stamp
		c.cache.macroRatesHash = ratesHash
		c.cache.macroStamps = curStamps
		c.cache.hasMacro = false
		return trend.MacroSleeveSummary{}, false
	}
	var sum trend.MacroSleeveSummary
	var ok bool
	if len(series) > 0 {
		sum, ok = trend.SimulateMacroHRPSleeve(series, rates)
	}
	c.cache.macroValid = true
	c.cache.macroEqDir = eqDir
	c.cache.macroDTB3 = dtb3Stamp
	c.cache.macroRatesHash = ratesHash
	c.cache.macroStamps = curStamps
	c.cache.macroSum = sum
	c.cache.hasMacro = ok
	return sum, ok
}

// hashRates computes a deterministic hash of the rates map for cache key.
func hashRates(rates map[string]float64) uint64 {
	if len(rates) == 0 {
		return 0
	}
	// Sort keys for deterministic ordering
	keys := make([]string, 0, len(rates))
	for k := range rates {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := fnv.New64a()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		v := rates[k]
		h.Write([]byte(fmt.Sprintf("%.12f", v)))
	}
	return h.Sum64()
}

// Invalidate clears all cached data.
func (c *TrendCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = trendDataCache{}
}

// InvalidateTrend clears only the trend state cache.
func (c *TrendCache) InvalidateTrend() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache.trendValid = false
	c.cache.trendStamp = pairedStamp{}
	c.cache.trendState = nil
}

// InvalidateCarry clears only the carry state cache.
func (c *TrendCache) InvalidateCarry() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache.carryValid = false
	c.cache.carryStamp = pairedStamp{}
	c.cache.carryState = nil
	c.cache.carryErr = nil
}

// InvalidateDTB3 clears only the DTB3 rates cache.
func (c *TrendCache) InvalidateDTB3() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache.dtb3Valid = false
	c.cache.dtb3Stamp = fileStamp{}
	c.cache.dtb3Rates = nil
}

// InvalidateMacro clears only the macro HRP cache.
func (c *TrendCache) InvalidateMacro() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache.macroValid = false
	c.cache.macroEqDir = ""
	c.cache.macroDTB3 = fileStamp{}
	c.cache.macroRatesHash = 0
	c.cache.macroStamps = nil
	c.cache.macroSum = trend.MacroSleeveSummary{}
	c.cache.hasMacro = false
}
