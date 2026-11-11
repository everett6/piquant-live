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
	RegionUS        = "us"
	RegionUSFutures = "us-futures"
	RegionGlobal    = "global"
)

// US regulated perpetual-style futures assumptions (Coinbase Financial Markets nano
// contracts, CFTC-regulated, 24/7), from public coverage as of 2026-11 — verify on the
// venue before relying on them:
//   - Nano BTC = 0.01 BTC, nano ETH = 0.10 ETH per contract; up to ~10x leverage.
//   - Fees reported "as low as 0.02%"; we simulate 2 bps maker / 6 bps taker.
//   - Funding accrues hourly (~0.01%/hr example); charged here to the holder either side.
// The simulator still prices fills on the Binance.US spot book as a proxy for the
// futures book (basis and depth differ).
const (
	USFuturesMakerFeeBps    = 2.0
	USFuturesTakerFeeBps    = 6.0
	USFuturesBaseLeverage   = 4.0 // one BTC nano (~$740) must fit a $200 account
	USFuturesMaxLeverage    = 5.0
	USFuturesFundingBpsPerH = 1.0
)

// USFuturesContractSizes maps symbol prefixes to nano contract sizes.
var USFuturesContractSizes = map[string]float64{"BTC": 0.01, "ETH": 0.10}

// ApplyRegionProfile adjusts cfg to the trading restrictions of the given region.
// "us" forces a spot cash account (1x, no shorting) with Binance.US fees, latency and
// tick sizes. "global" leaves cfg unchanged.
func ApplyRegionProfile(cfg *DryRunConfig, region string) error {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case "", RegionUS:
		cfg.SpotOnly = true
		cfg.ContractSizes = nil
		cfg.FundingBpsPerHour = 0
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
	case RegionUSFutures:
		cfg.SpotOnly = false
		cfg.BaseLeverage = USFuturesBaseLeverage
		cfg.MaxLeverage = USFuturesMaxLeverage
		cfg.MakerFeeBps = USFuturesMakerFeeBps
		cfg.TakerFeeBps = USFuturesTakerFeeBps
		cfg.ZeroFeeMaker = false
		cfg.OrderLatency = USOrderLatency
		cfg.FundingBpsPerHour = USFuturesFundingBpsPerH
		cfg.ContractSizes = make(map[string]float64, len(USFuturesContractSizes))
		for k, v := range USFuturesContractSizes {
			cfg.ContractSizes[k] = v
		}
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
		return fmt.Errorf("unknown region %q (want %q, %q or %q)", region, RegionUS, RegionUSFutures, RegionGlobal)
	}
}
