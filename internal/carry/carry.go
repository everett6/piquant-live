// Package carry is the forward paper ledger for PREREG19 C1: always-on funding carry on BTC and ETH (long spot,
// short perpetual at 2x on the short leg, equal halves). Funding comes from Binance's monthly public files (the
// live API is blocked in the US), so the ledger advances one completed month at a time. Rules mirror
// research/quant_lab/strategies19.py exactly.
package carry

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	NPerE   = 1 / 1.5 // notional per unit of coin equity
	LiqMult = 1.5
	LiqFee  = 0.01
)

// Costs per side as fractions; Haircut multiplies funding received.
type Costs struct {
	Spot, Perp, Haircut float64
}

var (
	Base   = Costs{Spot: 3e-4, Perp: 7e-4, Haircut: 1}
	Stress = Costs{Spot: 6e-4, Perp: 14e-4, Haircut: 0.75}
)

// Coins are the two halves (Coinbase product, Binance symbol).
var Coins = []struct{ Name, Product, Symbol string }{
	{"BTC", "BTC-USD", "BTCUSDT"},
	{"ETH", "ETH-USD", "ETHUSDT"},
}

// Rate is one 8-hourly funding payment.
type Rate struct {
	At   time.Time
	Rate float64
}

// DayInput is one coin's data for one UTC day.
type DayInput struct {
	Close, High      float64
	FundPos, FundNeg float64 // sums of the day's positive and negative rates
}

// DayRec is one ledger day.
type DayRec struct {
	Date         string  `json:"date"`
	Ret          float64 `json:"ret"`
	RetStress    float64 `json:"ret_stress"`
	Equity       float64 `json:"equity"`
	EquityStress float64 `json:"equity_stress"`
	FundBTC      float64 `json:"fund_btc"` // the day's summed 8h rates
	FundETH      float64 `json:"fund_eth"`
	Liq          bool    `json:"liq,omitempty"`
}

// State is the persisted ledger.
type State struct {
	Registered   string             `json:"registered"` // ledger opened at this day's close
	LastDate     string             `json:"last_date"`
	Equity       float64            `json:"equity"`
	EquityStress float64            `json:"equity_stress"`
	Peak         float64            `json:"peak"`
	PeakStress   float64            `json:"peak_stress"`
	MaxDD        float64            `json:"max_dd"`
	MaxDDStress  float64            `json:"max_dd_stress"`
	PrevClose    map[string]float64 `json:"prev_close"`
	Liquidations map[string]int     `json:"liquidations"`
	Days         []DayRec           `json:"days"`
}

// New opens the ledger at the close of date (paying the opening cost of both pairs).
func New(date string, closes map[string]float64) *State {
	open := func(c Costs) float64 { return 1 - (c.Spot+c.Perp)*NPerE }
	st := &State{Registered: date, LastDate: date, Equity: open(Base), EquityStress: open(Stress),
		PrevClose: map[string]float64{}, Liquidations: map[string]int{}}
	st.Peak, st.PeakStress = 1, 1
	for k, v := range closes {
		st.PrevClose[k] = v
	}
	st.updateDD()
	return st
}

func (st *State) updateDD() {
	st.Peak = math.Max(st.Peak, st.Equity)
	st.PeakStress = math.Max(st.PeakStress, st.EquityStress)
	st.MaxDD = math.Max(st.MaxDD, 1-st.Equity/st.Peak)
	st.MaxDDStress = math.Max(st.MaxDDStress, 1-st.EquityStress/st.PeakStress)
}

func coinRet(in DayInput, prev float64, c Costs) (float64, bool) {
	r := (in.FundPos*c.Haircut + in.FundNeg) * NPerE
	r -= math.Abs(in.Close/prev-1) * (c.Spot + c.Perp) * NPerE
	liq := in.High >= LiqMult*prev
	if liq {
		r -= (LiqFee + c.Perp) * NPerE
	}
	return r, liq
}

// Step applies one day (it must be the day after LastDate) with inputs for every coin.
func (st *State) Step(date string, in map[string]DayInput) error {
	want, err := nextDay(st.LastDate)
	if err != nil {
		return err
	}
	if date != want {
		return fmt.Errorf("carry: step %s, expected %s", date, want)
	}
	var rb, rs float64
	rec := DayRec{Date: date}
	for _, c := range Coins {
		x, ok := in[c.Name]
		prev := st.PrevClose[c.Name]
		if !ok || x.Close <= 0 || prev <= 0 {
			return fmt.Errorf("carry: %s missing data on %s", c.Name, date)
		}
		b, liq := coinRet(x, prev, Base)
		s, _ := coinRet(x, prev, Stress)
		rb += b / float64(len(Coins))
		rs += s / float64(len(Coins))
		if liq {
			st.Liquidations[c.Name]++
			rec.Liq = true
		}
		switch c.Name {
		case "BTC":
			rec.FundBTC = x.FundPos + x.FundNeg
		case "ETH":
			rec.FundETH = x.FundPos + x.FundNeg
		}
		st.PrevClose[c.Name] = x.Close
	}
	st.Equity *= 1 + rb
	st.EquityStress *= 1 + rs
	rec.Ret, rec.RetStress, rec.Equity, rec.EquityStress = rb, rs, st.Equity, st.EquityStress
	st.Days = append(st.Days, rec)
	st.LastDate = date
	st.updateDD()
	return nil
}

func nextDay(d string) (string, error) {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return "", err
	}
	return t.AddDate(0, 0, 1).Format("2006-01-02"), nil
}

// DailyFunding sums rates per UTC day into positive and negative parts.
func DailyFunding(rates []Rate) map[string][2]float64 {
	out := map[string][2]float64{}
	for _, r := range rates {
		d := r.At.UTC().Format("2006-01-02")
		v := out[d]
		if r.Rate > 0 {
			v[0] += r.Rate
		} else {
			v[1] += r.Rate
		}
		out[d] = v
	}
	return out
}

// ParseFundingCSV reads a Binance fundingRate CSV (calc_time ms, interval hours, rate), with or without header.
func ParseFundingCSV(r io.Reader) ([]Rate, error) {
	rows, err := csv.NewReader(r).ReadAll()
	if err != nil {
		return nil, err
	}
	var out []Rate
	for _, row := range rows {
		if len(row) < 3 {
			continue
		}
		ms, err1 := strconv.ParseInt(strings.TrimSpace(row[0]), 10, 64)
		v, err2 := strconv.ParseFloat(strings.TrimSpace(row[2]), 64)
		if err1 != nil || err2 != nil {
			continue // header
		}
		out = append(out, Rate{At: time.UnixMilli(ms).UTC(), Rate: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// ParseFundingZip reads the CSV inside a monthly zip.
func ParseFundingZip(b []byte) ([]Rate, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".csv") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return ParseFundingCSV(io.LimitReader(rc, 8<<20))
	}
	return nil, errors.New("no csv in zip")
}

// FundingBase hosts Binance's public data files (tests replace it).
var FundingBase = "https://data.binance.vision"

// FundingURL is the public monthly file for a symbol and month (YYYY-MM).
func FundingURL(symbol, month string) string {
	return fmt.Sprintf("%s/data/futures/um/monthly/fundingRate/%s/%s-fundingRate-%s.zip", FundingBase,
		symbol, symbol, month)
}

// Load reads the state (os.ErrNotExist when there is none yet).
func Load(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	if st.PrevClose == nil {
		st.PrevClose = map[string]float64{}
	}
	if st.Liquidations == nil {
		st.Liquidations = map[string]int{}
	}
	return &st, nil
}

// Save writes the state atomically.
func (st *State) Save(path string) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Summary is a one-line report.
func (st *State) Summary() string {
	days := len(st.Days)
	ann := func(eq float64) float64 {
		if days == 0 {
			return 0
		}
		return math.Pow(eq, 365.0/float64(days)) - 1
	}
	return fmt.Sprintf("carry C1 since %s close, through %s (%d days): equity %.4f (stress %.4f), annualised %+.2f%% "+
		"(stress %+.2f%%), max drop %.2f%% (stress %.2f%%), liquidations %v", st.Registered, st.LastDate, days,
		st.Equity, st.EquityStress, ann(st.Equity)*100, ann(st.EquityStress)*100, st.MaxDD*100, st.MaxDDStress*100,
		st.Liquidations)
}
