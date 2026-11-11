package paper

import (
	"fmt"
	"strings"
	"time"
)

// US retail venue assumptions (Binance.US spot), checked 2026-11-10:
//   - Binance.com geo-blocks US IPs (HTTP 451), so the US book is Binance.US.
//   - US retail spot accounts get no margin and no shorting: 1x, long-only.
//   - Binance.US flat spot schedule since April 2026: 0% maker, 0.02% taker.
//   - Measured from the San Jose Pi: ~13ms ICMP RTT / ~23ms TCP connect to api.binance.us.
//   - Binance.US price tick is 0.01 on BTC and ETH pairs.
const (
	USMakerFeeBps   = 0.0
	USTakerFeeBps   = 2.0
	USOrderLatency  = 30 * time.Millisecond // measured RTT plus processing headroom
	usDefaultTickSz = 0.01
)

// Regions accepted by ApplyRegionProfile.
const (
	RegionUS     = "us"
	RegionGlobal = "global"
)

// ApplyRegionProfile adjusts cfg to the trading restrictions of the given region.
// "us" forces a spot cash account (1x, no shorting) with Binance.US fees, latency and
// tick sizes. "global" leaves cfg unchanged.
func ApplyRegionProfile(cfg *DryRunConfig, region string) error {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case "", RegionUS:
		cfg.SpotOnly = true
		cfg.BaseLeverage = 1.0
		cfg.MaxLeverage = 1.0
		cfg.MakerFeeBps = USMakerFeeBps
		cfg.TakerFeeBps = USTakerFeeBps
		cfg.OrderLatency = USOrderLatency
		if cfg.TickSizes == nil {
			cfg.TickSizes = make(map[string]float64, len(cfg.Symbols))
		}
		for _, s := range cfg.Symbols {
			u := strings.ToUpper(strings.TrimSpace(s))
			if _, ok := cfg.TickSizes[u]; !ok {
				cfg.TickSizes[u] = usDefaultTickSz
			}
		}
		return nil
	case RegionGlobal:
		return nil
	default:
		return fmt.Errorf("unknown region %q (want %q or %q)", region, RegionUS, RegionGlobal)
	}
}
