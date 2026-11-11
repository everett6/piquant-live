package main

import (
	"testing"

	"piquant-live/internal/paper"
)

func TestResolveFeedVenue(t *testing.T) {
	cases := []struct {
		flag, region, ws, want string
		err                    bool
	}{
		{"auto", paper.RegionUSFutures, "", "coinbase", false},
		{"auto", paper.RegionUS, "", "binance", false},
		{"auto", paper.RegionUSFutures, "ws://custom", "binance", false},
		{"Coinbase", paper.RegionGlobal, "", "coinbase", false},
		{"binance", paper.RegionUSFutures, "", "binance", false},
		{"kraken", paper.RegionUS, "", "", true},
	}
	for _, c := range cases {
		got, err := resolveFeedVenue(c.flag, c.region, c.ws)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("resolveFeedVenue(%q,%q,%q) = %q,%v want %q err=%v", c.flag, c.region, c.ws, got, err, c.want, c.err)
		}
	}
}
