package trend

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type l2Level struct {
	Price float64
	Size  float64
}

type l2DaySummary struct {
	Date         string
	Day          Day
	CloseBestBid float64
	CloseBestAsk float64
	SpreadBps    float64
	Bids         []l2Level
	Asks         []l2Level
	Depth10USD   float64
	TouchMinBid  float64
	TouchMaxAsk  float64
	Rows         int
}

func skipSpace(b []byte, pos int) int {
	for pos < len(b) && (b[pos] == ' ' || b[pos] == '\t') {
		pos++
	}
	return pos
}

func scanNumberToken(b []byte, pos int) (float64, int, bool) {
	pos = skipSpace(b, pos)
	start := pos
	for pos < len(b) {
		c := b[pos]
		if (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '+' || c == 'e' || c == 'E' {
			pos++
			continue
		}
		break
	}
	if pos <= start {
		return 0, pos, false
	}
	v, err := strconv.ParseFloat(string(b[start:pos]), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, pos, false
	}
	return v, pos, true
}

func scanInt64Token(b []byte, pos int) (int64, int, bool) {
	pos = skipSpace(b, pos)
	start := pos
	for pos < len(b) {
		c := b[pos]
		if (c >= '0' && c <= '9') || c == '-' {
			pos++
			continue
		}
		break
	}
	if pos <= start {
		return 0, pos, false
	}
	v, err := strconv.ParseInt(string(b[start:pos]), 10, 64)
	if err != nil {
		return 0, pos, false
	}
	return v, pos, true
}

// extractL2TopFast scans t, bestBid, and bestAsk from a collected L2 JSON line with zero allocations.
func extractL2TopFast(line []byte) (tMs int64, bid, ask float64, ok bool) {
	idxT := bytes.Index(line, []byte(`"t":`))
	if idxT < 0 {
		return 0, 0, 0, false
	}
	tVal, _, okT := scanInt64Token(line, idxT+4)
	if !okT {
		return 0, 0, 0, false
	}
	idxB := bytes.Index(line, []byte(`"b":[[`))
	if idxB < 0 {
		return 0, 0, 0, false
	}
	bVal, _, okB := scanNumberToken(line, idxB+6)
	if !okB || bVal <= 0 {
		return 0, 0, 0, false
	}
	idxA := bytes.Index(line, []byte(`"a":[[`))
	if idxA < 0 {
		return 0, 0, 0, false
	}
	aVal, _, okA := scanNumberToken(line, idxA+6)
	if !okA || aVal <= 0 || aVal < bVal {
		return 0, 0, 0, false
	}
	return tVal, bVal, aVal, true
}

func parseL2DayFile(date, path string) (l2DaySummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return l2DaySummary{}, err
	}
	defer f.Close()

	zr, err := gzip.NewReader(f)
	if err != nil {
		return l2DaySummary{}, err
	}
	defer zr.Close()

	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)

	sum := l2DaySummary{
		Date:        date,
		TouchMinBid: math.Inf(1),
		TouchMaxAsk: 0,
	}
	var firstTs int64
	var openMid, highMid, lowMid, closeMid float64
	lowMid = math.Inf(1)
	var lastValidLine []byte

	for sc.Scan() {
		line := sc.Bytes()
		tMs, bid, ask, ok := extractL2TopFast(line)
		if !ok {
			continue
		}
		mid := 0.5 * (bid + ask)
		if sum.Rows == 0 {
			firstTs = tMs
			openMid = mid
		}
		if mid > highMid {
			highMid = mid
		}
		if mid < lowMid {
			lowMid = mid
		}
		closeMid = mid
		sum.CloseBestBid = bid
		sum.CloseBestAsk = ask
		if tMs-firstTs <= 300_000 {
			if bid < sum.TouchMinBid {
				sum.TouchMinBid = bid
			}
			if ask > sum.TouchMaxAsk {
				sum.TouchMaxAsk = ask
			}
		}
		sum.Rows++
		lastValidLine = append(lastValidLine[:0], line...)
	}
	if err := sc.Err(); err != nil && sum.Rows == 0 {
		return l2DaySummary{}, err
	}
	if sum.Rows == 0 || closeMid <= 0 {
		return l2DaySummary{}, io.ErrUnexpectedEOF
	}
	sum.Day = Day{Date: date, Open: openMid, High: highMid, Low: lowMid, Close: closeMid}

	if len(lastValidLine) > 0 {
		var full struct {
			Bids [][2]float64 `json:"b"`
			Asks [][2]float64 `json:"a"`
		}
		if jerr := json.Unmarshal(lastValidLine, &full); jerr == nil {
			for _, lvl := range full.Bids {
				if lvl[0] > 0 && lvl[1] > 0 {
					sum.Bids = append(sum.Bids, l2Level{Price: lvl[0], Size: lvl[1]})
					sum.Depth10USD += lvl[0] * lvl[1]
				}
			}
			for _, lvl := range full.Asks {
				if lvl[0] > 0 && lvl[1] > 0 {
					sum.Asks = append(sum.Asks, l2Level{Price: lvl[0], Size: lvl[1]})
					sum.Depth10USD += lvl[0] * lvl[1]
				}
			}
		}
	}
	if sum.CloseBestBid > 0 && sum.CloseBestAsk > 0 {
		mid := 0.5 * (sum.CloseBestBid + sum.CloseBestAsk)
		sum.SpreadBps = (sum.CloseBestAsk - sum.CloseBestBid) / mid * 1e4
	}
	return sum, nil
}

// estimateBookWalkSlippageBps simulates walking the 10-level L2 book for a dollar order size
// relative to mid price, returning adverse execution cost in basis points.
func estimateBookWalkSlippageBps(levels []l2Level, mid, notionalUSD float64, isBuy bool) float64 {
	if len(levels) == 0 || mid <= 0 || notionalUSD <= 0 {
		return 0
	}
	remUSD := notionalUSD
	var fillBase, fillUSD float64
	for _, lvl := range levels {
		lvlUSD := lvl.Price * lvl.Size
		takeUSD := math.Min(remUSD, lvlUSD)
		fillUSD += takeUSD
		fillBase += takeUSD / lvl.Price
		remUSD -= takeUSD
		if remUSD <= 1e-6 {
			break
		}
	}
	if remUSD > 1e-6 && len(levels) > 0 {
		worstPx := levels[len(levels)-1].Price
		fillUSD += remUSD
		fillBase += remUSD / worstPx
	}
	if fillBase <= 0 {
		return 0
	}
	vwap := fillUSD / fillBase
	if isBuy {
		return math.Max(0, (vwap-mid)/mid*1e4)
	}
	return math.Max(0, (mid-vwap)/mid*1e4)
}

func discoverCollectedL2Series(t *testing.T) (dates []string, summaries map[string]map[string]l2DaySummary) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot resolve home dir")
	}
	histBase := filepath.Join(home, "piquant-collect", "historical_l2", "book")
	liveBase := filepath.Join(home, "piquant-collect", "book")
	if _, err := os.Stat(histBase); err != nil {
		t.Skipf("historical L2 dir absent (%s): %v", histBase, err)
	}
	if _, err := os.Stat(liveBase); err != nil {
		t.Skipf("live L2 dir absent (%s): %v", liveBase, err)
	}

	assetDirs := map[string]struct {
		histSub string
		liveSub string
	}{
		"BTC": {histSub: "bybit-BTCUSDT", liveSub: "BTC-USD"},
		"ETH": {histSub: "bybit-ETHUSDT", liveSub: "ETH-USD"},
		"SOL": {histSub: "bybit-SOLUSDT", liveSub: "SOL-USD"},
	}

	todayUTC := time.Now().UTC().Format("2006-01-02")
	perAssetPaths := map[string]map[string]string{
		"BTC": {},
		"ETH": {},
		"SOL": {},
	}
	for asset, dirs := range assetDirs {
		hMatches, _ := filepath.Glob(filepath.Join(histBase, dirs.histSub, "*.jsonl.gz"))
		for _, p := range hMatches {
			d := strings.TrimSuffix(filepath.Base(p), ".jsonl.gz")
			if d < todayUTC {
				perAssetPaths[asset][d] = p
			}
		}
		lMatches, _ := filepath.Glob(filepath.Join(liveBase, dirs.liveSub, "*.jsonl.gz"))
		for _, p := range lMatches {
			d := strings.TrimSuffix(filepath.Base(p), ".jsonl.gz")
			// Prefer live Coinbase L2 for >= 2026-11-11; keep historical Bybit before that.
			if d < todayUTC && (d >= "2026-11-11" || perAssetPaths[asset][d] == "") {
				perAssetPaths[asset][d] = p
			}
		}
	}

	for d := range perAssetPaths["BTC"] {
		if perAssetPaths["ETH"][d] != "" && perAssetPaths["SOL"][d] != "" {
			dates = append(dates, d)
		}
	}
	sort.Strings(dates)
	if len(dates) < 60 {
		t.Skipf("insufficient overlapping L2 dates across BTC/ETH/SOL: %d", len(dates))
	}

	type parseJob struct {
		asset string
		date  string
		path  string
	}
	jobs := make(chan parseJob, len(dates)*3)
	for _, d := range dates {
		for _, asset := range []string{"BTC", "ETH", "SOL"} {
			jobs <- parseJob{asset: asset, date: d, path: perAssetPaths[asset][d]}
		}
	}
	close(jobs)

	summaries = map[string]map[string]l2DaySummary{
		"BTC": make(map[string]l2DaySummary, len(dates)),
		"ETH": make(map[string]l2DaySummary, len(dates)),
		"SOL": make(map[string]l2DaySummary, len(dates)),
	}
	var mu sync.Mutex
	var firstParseErr error
	workers := runtime.NumCPU()
	if workers < 2 {
		workers = 2
	}
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for j := range jobs {
				sum, perr := parseL2DayFile(j.date, j.path)
				mu.Lock()
				if perr != nil {
					if firstParseErr == nil {
						firstParseErr = perr
					}
				} else {
					summaries[j.asset][j.date] = sum
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstParseErr != nil {
		t.Fatalf("failed parsing L2 data: %v", firstParseErr)
	}

	validDates := make([]string, 0, len(dates))
	for _, d := range dates {
		if summaries["BTC"][d].Rows > 0 && summaries["ETH"][d].Rows > 0 && summaries["SOL"][d].Rows > 0 {
			validDates = append(validDates, d)
		}
	}
	return validDates, summaries
}

func TestQ57AndQ56OnCollectedL2Data(t *testing.T) {
	dates, summaries := discoverCollectedL2Series(t)
	if len(dates) < 60 {
		t.Skipf("fewer than 60 valid overlapping L2 days: %d", len(dates))
	}

	seqB := make([]Day, 0, len(dates))
	seqE := make([]Day, 0, len(dates))
	seqS := make([]Day, 0, len(dates))
	for _, d := range dates {
		seqB = append(seqB, summaries["BTC"][d].Day)
		seqE = append(seqE, summaries["ETH"][d].Day)
		seqS = append(seqS, summaries["SOL"][d].Day)
	}

	seedLen := 51
	st := NewState(dates[seedLen-1])
	st.Seed(seqB[:seedLen])
	st.SeedETH(seqE[:seedLen])
	st.SeedSOL(seqS[:seedLen])

	nB := st.Advance(seqB[seedLen:])
	nE := st.AdvanceETH(seqE[seedLen:])
	nS := st.AdvanceSOL(seqS[seedLen:])
	if nB != len(dates)-seedLen || nE != nB || nS != nB {
		t.Fatalf("stepped days mismatch: B=%d E=%d S=%d want %d", nB, nE, nS, len(dates)-seedLen)
	}

	// Verify real L2 spread, 10-level book walk slippage, and 300s passive fill rate per strategy.
	variantNames := []string{
		"Q51_btc_eth_sol_volparity_spot",
		"Q56_quantum_fast_spot_1x",
		"Q57_hrp_hurst_conformal_spot_1x",
	}
	for _, name := range variantNames {
		l := st.Ledgers[name]
		if l == nil || len(l.Days) != nB {
			t.Fatalf("ledger %s invalid len=%d want=%d", name, len(l.Days), nB)
		}
		if math.IsNaN(l.Equity) || math.IsInf(l.Equity, 0) || l.Equity <= 0 {
			t.Fatalf("ledger %s non-finite/non-positive equity=%v", name, l.Equity)
		}
		if math.IsNaN(l.MaxDD) || l.MaxDD < 0 || l.MaxDD > 1.0 {
			t.Fatalf("ledger %s invalid maxDD=%v", name, l.MaxDD)
		}

		var rebalEvents, passiveFills int
		var totalTurnover, sumSpreadBps, sumSlip1kBps, sumSlip25kBps, minDepth10USD float64
		minDepth10USD = math.Inf(1)

		prevPos := [3]float64{0, 0, 0}
		assets := [3]string{"BTC", "ETH", "SOL"}
		for i, rec := range l.Days {
			if i+1 >= len(l.Days) {
				break // need next day's first 300s touch window to score passive fills
			}
			curPos := [3]float64{rec.PosB, rec.PosE, rec.PosS}
			nextDate := l.Days[i+1].Date
			for k, asset := range assets {
				if curPos[k] < -1e-9 || curPos[k] > 1.0001 {
					t.Fatalf("%s day %s %s invalid position %v", name, rec.Date, asset, curPos[k])
				}
				delta := curPos[k] - prevPos[k]
				if math.Abs(delta) < 1e-6 {
					continue
				}
				rebalEvents++
				totalTurnover += math.Abs(delta)
				snap := summaries[asset][rec.Date]
				nextSnap := summaries[asset][nextDate]
				sumSpreadBps += snap.SpreadBps
				if snap.Depth10USD < minDepth10USD {
					minDepth10USD = snap.Depth10USD
				}
				mid := snap.Day.Close
				isBuy := delta > 0
				if isBuy {
					sumSlip1kBps += estimateBookWalkSlippageBps(snap.Asks, mid, 1000.0*math.Abs(delta), true)
					sumSlip25kBps += estimateBookWalkSlippageBps(snap.Asks, mid, 25000.0*math.Abs(delta), true)
					limitPx := snap.CloseBestBid
					if nextSnap.TouchMinBid <= limitPx {
						passiveFills++
					}
				} else {
					sumSlip1kBps += estimateBookWalkSlippageBps(snap.Bids, mid, 1000.0*math.Abs(delta), false)
					sumSlip25kBps += estimateBookWalkSlippageBps(snap.Bids, mid, 25000.0*math.Abs(delta), false)
					limitPx := snap.CloseBestAsk
					if nextSnap.TouchMaxAsk >= limitPx {
						passiveFills++
					}
				}
			}
			prevPos = curPos
		}

		avgSpread, avgSlip1k, avgSlip25k, fillRatePct := 0.0, 0.0, 0.0, 0.0
		if rebalEvents > 0 {
			avgSpread = sumSpreadBps / float64(rebalEvents)
			avgSlip1k = sumSlip1kBps / float64(rebalEvents)
			avgSlip25k = sumSlip25kBps / float64(rebalEvents)
			fillRatePct = 100.0 * float64(passiveFills) / float64(rebalEvents)
		}
		if math.IsInf(minDepth10USD, 1) {
			minDepth10USD = 0
		}
		t.Logf(
			"[L2 REPLAY %s..%s (%d fwd days)] %-32s | Eq=%.4f MaxDD=%.2f%% Sharpe=%.3f | Rebals=%d Turn=%.2fx Spread=%.3fbps Walk1k=%.3fbps Walk25k=%.3fbps PassiveFill300s=%.1f%% Min10LvlDepth=$%.0f",
			l.Days[0].Date, l.Days[len(l.Days)-1].Date, len(l.Days),
			name, l.Equity, l.MaxDD*100.0, sharpe(l.Days),
			rebalEvents, totalTurnover, avgSpread, avgSlip1k, avgSlip25k, fillRatePct, minDepth10USD,
		)
	}

	b57, ok := SelectBlend(st, nil, nil, "q57_hrp_hurst", nil)
	if !ok || b57.Name != "Q57_hrp_hurst_conformal_carry_spot" {
		t.Fatalf("SelectBlend(q57_hrp_hurst) on L2 data failed: ok=%v blend=%+v", ok, b57)
	}
	t.Logf("[L2 BLEND Q57] Name=%s Eq=%.4f MaxDD=%.2f%% MeanDD=%.2f%% Ulcer=%.2f%% Sharpe=%.3f Targets(BTC=%.2f%% ETH=%.2f%% SOL=%.2f%% Cash=%.2f%%)",
		b57.Name, b57.Equity, b57.MaxDD*100, b57.MeanDD*100, b57.UlcerIndex*100, b57.Sharpe,
		b57.TargetBTCPortPct, b57.TargetETHPortPct, b57.TargetSOLPortPct, b57.IdleTrendCashPct,
	)

	// Emergency guard sanity: if SOL drops 50% from last L2 close while active, EmergencyCheckAll fires cleanly.
	l57 := st.Ledgers["Q57_hrp_hurst_conformal_spot_1x"]
	if l57.Pos > 0 || l57.PosAlt > 0 || l57.PosSOL > 0 {
		last := l57.Days[len(l57.Days)-1]
		fired := st.EmergencyCheckAll(last.CloseB*0.5, last.CloseE*0.5, last.CloseS*0.5, time.Now())
		if len(fired) == 0 {
			t.Fatalf("expected EmergencyCheckAll to fire on 50%% L2 flash crash")
		}
	}
}
