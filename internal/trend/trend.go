// Package trend is the PREREG6 forward paper test of the SMA trend filter: T1 (spot, 1x), T2 (futures, 2x)
// and a buy-and-hold benchmark, advanced once per completed UTC day. It is a standalone ledger and never
// places orders or touches the paper engine.
package trend

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Variant is a frozen PREREG6 strategy definition.
type Variant struct {
	Name        string  `json:"name"`
	Lev         float64 `json:"lev"`            // position when trend holds (0 when not); BH holds Lev always
	FeeBps      float64 `json:"fee_bps"`        // per unit of turnover
	SlipBps     float64 `json:"slip_bps"`       // per unit of turnover
	FundingBpsH float64 `json:"funding_bps_h"`  // per hour per unit of |position|
	SMA         int     `json:"sma"`            // 0 = always hold (benchmark)
	KillDD      float64 `json:"kill_dd"`        // stop the variant at this drawdown from peak (0 = never)
	Pair        bool    `json:"pair,omitempty"` // PREREG11: half BTC, half ETH sleeve, each Lev when above its own SMA
}

// Variants are frozen by PREREG6.
var Variants = []Variant{
	{Name: "T1_spot_1x", Lev: 1, FeeBps: 2, SlipBps: 1, SMA: 50},
	{Name: "T2_futures_2x", Lev: 2, FeeBps: 6, SlipBps: 1, FundingBpsH: 0.1, SMA: 50, KillDD: 0.5},
	{Name: "BH", Lev: 1, SMA: 0},
	// PREREG11 P1 (passed 2026-11-11, research/quant_lab/PREREG11.md): stepped by AdvanceETH, not Advance.
	{Name: "P1_btc_eth_2x", Lev: 2, FeeBps: 6, SlipBps: 1, FundingBpsH: 0.1, SMA: 50, Pair: true},
}

// Day is one completed UTC daily candle.
type Day struct {
	Date  string  `json:"date"` // YYYY-MM-DD (UTC)
	Close float64 `json:"close"`
}

// DayRec is one ledger entry.
type DayRec struct {
	Date   string  `json:"date"`
	Ret    float64 `json:"ret"`    // net return of the day (held position, funding, rebalancing cost)
	PosOut float64 `json:"pos"`    // position decided at this day's close
	Equity float64 `json:"equity"` // after the day
}

// Ledger tracks one variant.
type Ledger struct {
	Variant Variant  `json:"variant"`
	Equity  float64  `json:"equity"`
	Peak    float64  `json:"peak"`
	MaxDD   float64  `json:"max_dd"`
	Pos     float64  `json:"pos"`
	PosAlt  float64  `json:"pos_alt,omitempty"` // ETH sleeve position (pair variants)
	Killed  bool     `json:"killed"`
	Days    []DayRec `json:"days"`
}

// State is the persisted forward test.
type State struct {
	Registered string             `json:"registered"`
	LastDate   string             `json:"last_date"`
	History    []Day              `json:"history"` // closes used for the SMA (capped)
	Ledgers    map[string]*Ledger `json:"ledgers"`
	ETHHistory []Day              `json:"eth_history,omitempty"` // ETH closes for pair variants (capped)
	ETHLast    string             `json:"eth_last,omitempty"`
}

const historyCap = 400

// NewState starts a forward test.
func NewState(registered string) *State {
	st := &State{Registered: registered, Ledgers: map[string]*Ledger{}}
	st.ensureLedgers()
	return st
}

// ensureLedgers adds variants missing from an older state file; they start flat at equity 1.
func (st *State) ensureLedgers() {
	if st.Ledgers == nil {
		st.Ledgers = map[string]*Ledger{}
	}
	for _, v := range Variants {
		if _, ok := st.Ledgers[v.Name]; !ok {
			st.Ledgers[v.Name] = &Ledger{Variant: v, Equity: 1, Peak: 1}
		}
	}
}

func sma(h []Day, n int) (float64, bool) {
	if n <= 0 || len(h) < n {
		return 0, false
	}
	s := 0.0
	for _, d := range h[len(h)-n:] {
		s += d.Close
	}
	return s / float64(n), true
}

// Seed loads pre-registration closes for the SMA without touching any ledger (forward test starts after).
func (st *State) Seed(days []Day) {
	sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })
	for _, d := range days {
		if d.Close > 0 && d.Date > st.LastDate {
			st.History = append(st.History, d)
			st.LastDate = d.Date
		}
	}
	if len(st.History) > historyCap {
		st.History = st.History[len(st.History)-historyCap:]
	}
}

// Advance applies completed days newer than LastDate, in order. Days before or equal to LastDate are ignored,
// so repeated calls with overlapping candles are idempotent. The first day ever seen only seeds history.
func (st *State) Advance(days []Day) int {
	sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })
	n := 0
	for _, d := range days {
		if d.Close <= 0 || d.Date <= st.LastDate {
			continue
		}
		var prev float64
		if len(st.History) > 0 {
			prev = st.History[len(st.History)-1].Close
		}
		st.History = append(st.History, d)
		if len(st.History) > historyCap {
			st.History = st.History[len(st.History)-historyCap:]
		}
		st.LastDate = d.Date
		if prev <= 0 {
			continue // seed only
		}
		bar := d.Close/prev - 1
		for _, name := range sortedKeys(st.Ledgers) {
			if l := st.Ledgers[name]; !l.Variant.Pair {
				l.step(d.Date, bar, st.History)
			}
		}
		n++
	}
	return n
}

func capDays(h []Day) []Day {
	if len(h) > historyCap {
		return h[len(h)-historyCap:]
	}
	return h
}

// SeedETH loads pre-registration ETH closes without touching any ledger.
func (st *State) SeedETH(days []Day) {
	sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })
	for _, d := range days {
		if d.Close > 0 && d.Date > st.ETHLast {
			st.ETHHistory = append(st.ETHHistory, d)
			st.ETHLast = d.Date
		}
	}
	st.ETHHistory = capDays(st.ETHHistory)
}

// AdvanceETH steps pair variants for ETH days newer than ETHLast whose BTC day has already been applied by
// Advance (so both sleeves see the same completed day). Idempotent like Advance.
func (st *State) AdvanceETH(days []Day) int {
	sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })
	n := 0
	for _, d := range days {
		if d.Close <= 0 || d.Date <= st.ETHLast || d.Date > st.LastDate {
			continue
		}
		bi := -1
		for i := len(st.History) - 1; i > 0; i-- {
			if st.History[i].Date == d.Date {
				bi = i
				break
			}
		}
		var prevE float64
		if len(st.ETHHistory) > 0 {
			prevE = st.ETHHistory[len(st.ETHHistory)-1].Close
		}
		st.ETHHistory = capDays(append(st.ETHHistory, d))
		st.ETHLast = d.Date
		if prevE <= 0 || bi < 1 {
			continue // seed only, or the BTC day is missing
		}
		btcHist := st.History[:bi+1]
		barB := btcHist[bi].Close/btcHist[bi-1].Close - 1
		barE := d.Close/prevE - 1
		for _, name := range sortedKeys(st.Ledgers) {
			if l := st.Ledgers[name]; l.Variant.Pair {
				l.stepPair(d.Date, barB, barE, btcHist, st.ETHHistory)
			}
		}
		n++
	}
	return n
}

// sleeve returns one leg's day return (held position, funding, rebalancing cost) and its new target.
func (v Variant) sleeve(pos, bar float64, hist []Day) (ret, target float64) {
	ret = pos*bar - math.Abs(pos)*v.FundingBpsH*24*1e-4
	if m, ok := sma(hist, v.SMA); ok && hist[len(hist)-1].Close > m {
		target = v.Lev
	}
	ret -= math.Abs(target-pos) * (v.FeeBps + v.SlipBps) * 1e-4
	return ret, target
}

func (l *Ledger) stepPair(date string, barB, barE float64, histB, histE []Day) {
	rb, tb := l.Variant.sleeve(l.Pos, barB, histB)
	re, te := l.Variant.sleeve(l.PosAlt, barE, histE)
	ret := 0.5*rb + 0.5*re // equal halves, rebalanced daily (as in the PREREG11 backtest)
	l.Equity *= 1 + ret
	if l.Equity > l.Peak {
		l.Peak = l.Equity
	}
	if dd := 1 - l.Equity/l.Peak; dd > l.MaxDD {
		l.MaxDD = dd
	}
	l.Pos, l.PosAlt = tb, te
	l.Days = append(l.Days, DayRec{Date: date, Ret: ret, PosOut: 0.5*tb + 0.5*te, Equity: l.Equity})
}

func (l *Ledger) step(date string, bar float64, hist []Day) {
	v := l.Variant
	ret := l.Pos*bar - math.Abs(l.Pos)*v.FundingBpsH*24*1e-4
	target := 0.0
	if !l.Killed {
		if v.SMA == 0 {
			target = v.Lev
		} else if m, ok := sma(hist, v.SMA); ok && hist[len(hist)-1].Close > m {
			target = v.Lev
		}
	}
	ret -= math.Abs(target-l.Pos) * (v.FeeBps + v.SlipBps) * 1e-4
	l.Equity *= 1 + ret
	if l.Equity > l.Peak {
		l.Peak = l.Equity
	}
	dd := 1 - l.Equity/l.Peak
	if dd > l.MaxDD {
		l.MaxDD = dd
	}
	if v.KillDD > 0 && dd >= v.KillDD && !l.Killed {
		l.Killed = true
		ret -= math.Abs(target) * (v.FeeBps + v.SlipBps) * 1e-4 // exit now
		l.Equity *= 1 - math.Abs(target)*(v.FeeBps+v.SlipBps)*1e-4
		target = 0
	}
	l.Pos = target
	l.Days = append(l.Days, DayRec{Date: date, Ret: ret, PosOut: target, Equity: l.Equity})
}

func sortedKeys(m map[string]*Ledger) []string {
	k := make([]string, 0, len(m))
	for s := range m {
		k = append(k, s)
	}
	sort.Strings(k)
	return k
}

// Regime reports whether the last completed daily close is above its SMA(n) (the PREREG6 rule).
func (st *State) Regime(n int) (up bool, close, avg float64, ok bool) {
	m, ok := sma(st.History, n)
	if !ok {
		return false, 0, 0, false
	}
	c := st.History[len(st.History)-1].Close
	return c > m, c, m, true
}

// Monthly returns compounded per YYYY-MM (all months present, including the current partial one).
func (l *Ledger) Monthly() map[string]float64 {
	out := map[string]float64{}
	for _, d := range l.Days {
		m := d.Date[:7]
		if _, ok := out[m]; !ok {
			out[m] = 1
		}
		out[m] *= 1 + d.Ret
	}
	for k := range out {
		out[k]--
	}
	return out
}

// Summary is the PREREG6 scorecard of one variant against BH over complete months.
type Summary struct {
	Name       string  `json:"name"`
	Equity     float64 `json:"equity"`
	MaxDD      float64 `json:"max_dd"`
	Sharpe     float64 `json:"sharpe"`
	Months     int     `json:"months"`
	Beats      int     `json:"beats"`
	BeatRate   float64 `json:"beat_rate"`
	Killed     bool    `json:"killed"`
	CurrentPos float64 `json:"current_pos"`
}

// Score compares each variant with BH; the month containing asOf (incomplete) is excluded.
func (st *State) Score(asOf time.Time) []Summary {
	bh := st.Ledgers["BH"]
	cur := asOf.UTC().Format("2006-01")
	var bhM map[string]float64
	if bh != nil {
		bhM = bh.Monthly()
	}
	var out []Summary
	for _, name := range sortedKeys(st.Ledgers) {
		l := st.Ledgers[name]
		s := Summary{Name: name, Equity: l.Equity, MaxDD: l.MaxDD, Sharpe: sharpe(l.Days), Killed: l.Killed, CurrentPos: l.Pos}
		if bhM != nil && name != "BH" {
			for m, r := range l.Monthly() {
				if m >= cur || (len(st.Registered) >= 7 && m <= st.Registered[:7]) {
					continue // skip the incomplete current month and the partial registration month
				}
				s.Months++
				if r > bhM[m] {
					s.Beats++
				}
			}
			if s.Months > 0 {
				s.BeatRate = float64(s.Beats) / float64(s.Months)
			}
		}
		out = append(out, s)
	}
	return out
}

func sharpe(days []DayRec) float64 {
	if len(days) < 2 {
		return 0
	}
	var s, s2 float64
	for _, d := range days {
		s += d.Ret
		s2 += d.Ret * d.Ret
	}
	n := float64(len(days))
	mu := s / n
	v := (s2 - n*mu*mu) / (n - 1)
	if v <= 0 {
		return 0
	}
	return mu / math.Sqrt(v) * math.Sqrt(365)
}

// Load reads a state file; a missing file returns (nil, nil).
func Load(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	st.ensureLedgers()
	return &st, nil
}

// Save writes the state atomically.
func (st *State) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
