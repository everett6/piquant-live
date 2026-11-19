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

// OrderEntryEndpoint returns the host orders are sent to for a region/venue (probed for latency).
func OrderEntryEndpoint(region string) string {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case RegionUSFutures, RegionCoinbaseSpot, RegionCoinbaseFutures, "coinbase":
		return "https://api.coinbase.com"
	case RegionKrakenSpot:
		return "https://api.kraken.com"
	case RegionKrakenFutures, "kraken":
		return "https://futures.kraken.com"
	case RegionBybit:
		return "https://api.bybit.com"
	case RegionDeribit:
		return "https://www.deribit.com"
	case RegionGlobal, "binance", "binance-global":
		return "https://api.binance.com"
	default:
		return "https://api.binance.us"
	}
}

// Regions and exchange venues accepted by ApplyRegionProfile.
const (
	RegionUS              = "us"               // Binance.US Spot (1.0x, 0/2 bps)
	RegionUSFutures       = "us-futures"       // Coinbase Financial Markets CDE nano futures (up to 5x, 2/6 bps)
	RegionCoinbaseSpot    = "coinbase-spot"    // Coinbase Spot (1.0x, 2/3 bps, 0 funding)
	RegionCoinbaseFutures = "coinbase-futures" // Coinbase CDE Perpetual-Style Futures balanced 1.0x..2.5x (2/6 bps, 0.1 bp/h)
	RegionKrakenSpot      = "kraken-spot"      // Kraken Pro Spot (1.0x, 2/4 bps, 0 funding)
	RegionKrakenFutures   = "kraken-futures"   // Kraken Pro Margin / Futures balanced 1.0x..2.5x (2/5 bps, 0.1 bp/h)
	RegionBybit           = "bybit"            // Bybit Unified Margin 1.0x..2.5x (AWS Tokyo ap-northeast-1 ~1.5ms, 1/5.5 bps, 0.1 bp/h)
	RegionDeribit         = "deribit"          // Deribit Perps 1.0x..2.5x (Equinix LD4 ~2ms, 0/5 bps, 0.1 bp/h)
	RegionGlobal          = "global"           // Unconstrained global research mode
)

// VenueLeverageProfile describes an exchange's fee, funding, leverage, and optimal cloud datacenter region.
type VenueLeverageProfile struct {
	Venue             string  `json:"venue"`
	Endpoint          string  `json:"endpoint"`
	OptimalCloudAWS   string  `json:"optimal_cloud_aws"`
	SpotOnly          bool    `json:"spot_only"`
	MinLeverage       float64 `json:"min_leverage"`
	BaseLeverage      float64 `json:"base_leverage"`
	MaxLeverage       float64 `json:"max_leverage"`
	MakerFeeBps       float64 `json:"maker_fee_bps"`
	TakerFeeBps       float64 `json:"taker_fee_bps"`
	FundingBpsPerHour float64 `json:"funding_bps_per_hour"`
}

// SupportedVenueProfiles returns the multi-exchange 1.0x and 1.0x..2.5x leverage schedules across US and global venues.
func SupportedVenueProfiles() []VenueLeverageProfile {
	return []VenueLeverageProfile{
		{Venue: RegionCoinbaseSpot, Endpoint: "https://api.coinbase.com", OptimalCloudAWS: "us-east-1 (N. Virginia)", SpotOnly: true, MinLeverage: 1.0, BaseLeverage: 1.0, MaxLeverage: 1.0, MakerFeeBps: 2.0, TakerFeeBps: 3.0, FundingBpsPerHour: 0.0},
		{Venue: RegionCoinbaseFutures, Endpoint: "https://api.coinbase.com", OptimalCloudAWS: "us-east-1 (N. Virginia)", SpotOnly: false, MinLeverage: 1.0, BaseLeverage: 1.5, MaxLeverage: 2.5, MakerFeeBps: 2.0, TakerFeeBps: 6.0, FundingBpsPerHour: 0.1},
		{Venue: RegionKrakenSpot, Endpoint: "https://api.kraken.com", OptimalCloudAWS: "eu-west-1 / us-east-1", SpotOnly: true, MinLeverage: 1.0, BaseLeverage: 1.0, MaxLeverage: 1.0, MakerFeeBps: 2.0, TakerFeeBps: 4.0, FundingBpsPerHour: 0.0},
		{Venue: RegionKrakenFutures, Endpoint: "https://futures.kraken.com", OptimalCloudAWS: "eu-west-2 (London)", SpotOnly: false, MinLeverage: 1.0, BaseLeverage: 1.5, MaxLeverage: 2.5, MakerFeeBps: 2.0, TakerFeeBps: 5.0, FundingBpsPerHour: 0.1},
		{Venue: RegionBybit, Endpoint: "https://api.bybit.com", OptimalCloudAWS: "ap-northeast-1 (Tokyo)", SpotOnly: false, MinLeverage: 1.0, BaseLeverage: 1.5, MaxLeverage: 2.5, MakerFeeBps: 1.0, TakerFeeBps: 5.5, FundingBpsPerHour: 0.1},
		{Venue: RegionDeribit, Endpoint: "https://www.deribit.com", OptimalCloudAWS: "eu-west-2 (London)", SpotOnly: false, MinLeverage: 1.0, BaseLeverage: 1.5, MaxLeverage: 2.5, MakerFeeBps: 0.0, TakerFeeBps: 5.0, FundingBpsPerHour: 0.1},
		{Venue: RegionUS, Endpoint: "https://api.binance.us", OptimalCloudAWS: "us-east-1 (N. Virginia)", SpotOnly: true, MinLeverage: 1.0, BaseLeverage: 1.0, MaxLeverage: 1.0, MakerFeeBps: USMakerFeeBps, TakerFeeBps: USTakerFeeBps, FundingBpsPerHour: 0.0},
	}
}

// US regulated perpetual-style futures assumptions (Coinbase Financial Markets nano
// contracts, CFTC-regulated, 24/7), from public coverage as of 2026-11 — verify on the
// venue before relying on them:
//   - Nano BTC = 0.01 BTC, nano ETH = 0.10 ETH per contract; up to ~10x leverage.
//   - Fees reported "as low as 0.02%"; we simulate 2 bps maker / 6 bps taker.
//   - Funding accrues hourly (~0.01%/hr example); charged here to the holder either side.
// With --feed-venue auto the simulator prices fills on the Coinbase spot feed as a proxy
// for the futures book (basis and depth differ).
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
	case RegionCoinbaseSpot:
		applyVenueConfig(cfg, true, 1.0, 1.0, 2.0, 3.0, 0.0, false, nil)
		return nil
	case RegionCoinbaseFutures, "coinbase":
		applyVenueConfig(cfg, false, 1.5, 2.5, 2.0, 6.0, 0.1, false, USFuturesContractSizes)
		return nil
	case RegionKrakenSpot:
		applyVenueConfig(cfg, true, 1.0, 1.0, 2.0, 4.0, 0.0, false, nil)
		return nil
	case RegionKrakenFutures, "kraken":
		applyVenueConfig(cfg, false, 1.5, 2.5, 2.0, 5.0, 0.1, false, nil)
		return nil
	case RegionBybit:
		applyVenueConfig(cfg, false, 1.5, 2.5, 1.0, 5.5, 0.1, false, nil)
		return nil
	case RegionDeribit:
		applyVenueConfig(cfg, false, 1.5, 2.5, 0.0, 5.0, 0.1, true, nil)
		return nil
	case RegionGlobal:
		return nil
	default:
		return fmt.Errorf("unknown region/venue %q (want %q, %q, %q, %q, %q, %q, %q, %q or %q)",
			region, RegionUS, RegionUSFutures, RegionCoinbaseSpot, RegionCoinbaseFutures, RegionKrakenSpot, RegionKrakenFutures, RegionBybit, RegionDeribit, RegionGlobal)
	}
}

func applyVenueConfig(cfg *DryRunConfig, spotOnly bool, baseLev, maxLev, makerBps, takerBps, fundingBpsH float64, zeroMaker bool, contracts map[string]float64) {
	cfg.SpotOnly = spotOnly
	cfg.BaseLeverage = baseLev
	cfg.MaxLeverage = maxLev
	cfg.MakerFeeBps = makerBps
	cfg.TakerFeeBps = takerBps
	cfg.FundingBpsPerHour = fundingBpsH
	cfg.ZeroFeeMaker = zeroMaker
	cfg.OrderLatency = USOrderLatency
	if contracts == nil {
		cfg.ContractSizes = nil
	} else {
		cfg.ContractSizes = make(map[string]float64, len(contracts))
		for k, v := range contracts {
			cfg.ContractSizes[k] = v
		}
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
}
