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
	Lev         float64 `json:"lev"`           // position when trend holds (0 when not); BH holds Lev always
	FeeBps      float64 `json:"fee_bps"`       // per unit of turnover
	SlipBps     float64 `json:"slip_bps"`      // per unit of turnover
	FundingBpsH float64 `json:"funding_bps_h"` // per hour per unit of |position|
	SMA         int     `json:"sma"`           // 0 = always hold (benchmark)
	KillDD      float64 `json:"kill_dd"`       // stop the variant at this drawdown from peak (0 = never)
}

// Variants are frozen by PREREG6.
var Variants = []Variant{
	{Name: "T1_spot_1x", Lev: 1, FeeBps: 2, SlipBps: 1, SMA: 50},
	{Name: "T2_futures_2x", Lev: 2, FeeBps: 6, SlipBps: 1, FundingBpsH: 0.1, SMA: 50, KillDD: 0.5},
	{Name: "BH", Lev: 1, SMA: 0},
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
	Killed  bool     `json:"killed"`
	Days    []DayRec `json:"days"`
}

// State is the persisted forward test.
type State struct {
	Registered string             `json:"registered"`
	LastDate   string             `json:"last_date"`
	History    []Day              `json:"history"` // closes used for the SMA (capped)
	Ledgers    map[string]*Ledger `json:"ledgers"`
}

const historyCap = 400

// NewState starts a forward test.
func NewState(registered string) *State {
	st := &State{Registered: registered, Ledgers: map[string]*Ledger{}}
	for _, v := range Variants {
		st.Ledgers[v.Name] = &Ledger{Variant: v, Equity: 1, Peak: 1}
	}
	return st
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
			st.Ledgers[name].step(d.Date, bar, st.History)
		}
		n++
	}
	return n
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
