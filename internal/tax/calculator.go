package tax

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"
)

// RawTrade reflects the journal record format from dryrun_journal.jsonl.
type RawTrade struct {
	Timestamp   time.Time `json:"timestamp"`
	Symbol      string    `json:"symbol"`
	Side        string    `json:"side"` // "BUY" or "SELL"
	Role        string    `json:"role"` // "MAKER" or "TAKER"
	Price       float64   `json:"price"`
	Size        float64   `json:"size"`
	ValueUSDT   float64   `json:"value_usdt"`
	FeeUSDT     float64   `json:"fee_usdt"`
	EdgeBps     float64   `json:"edge_bps"`
	SlippageBps float64   `json:"slippage_bps"`
	PnLRealized float64   `json:"pnl_realized"`
	Strategy    string    `json:"strategy"`
	DataSource  string    `json:"data_source"`
}

// Calculator executes FIFO lot matching and computes IRS Form 8949 / Schedule D crypto taxes.
type Calculator struct {
	FederalRatePct float64
	StateRatePct   float64
}

// NewCalculator creates a tax calculator with Federal and State tax bracket rates.
func NewCalculator(fedRate, stateRate float64) *Calculator {
	if fedRate <= 0 {
		fedRate = 24.0 // 24% US Federal marginal rate baseline
	}
	if stateRate <= 0 {
		stateRate = 9.3 // 9.3% California Franchise Tax Board rate baseline
	}
	return &Calculator{
		FederalRatePct: fedRate,
		StateRatePct:   stateRate,
	}
}

// CalculateFromJournalFile reads trades from a JSONL journal file and produces a TaxSummary.
func (c *Calculator) CalculateFromJournalFile(path string, taxYear int) (*TaxSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return c.CalculateFromReader(f, taxYear)
}

// CalculateFromReader parses streaming JSONL trade records and computes the tax summary.
func (c *Calculator) CalculateFromReader(r io.Reader, taxYear int) (*TaxSummary, error) {
	scanner := bufio.NewScanner(r)
	var trades []RawTrade
	for scanner.Scan() {
		line := bytes.Trim(scanner.Bytes(), "\x00\r\n \t")
		if len(line) == 0 {
			continue
		}
		var tr RawTrade
		if err := json.Unmarshal(line, &tr); err == nil && tr.Symbol != "" && tr.Size > 0 {
			trades = append(trades, tr)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return c.CalculateFromTrades(trades, taxYear), nil
}

// CalculateFromTrades runs FIFO cost-basis lot matching on a slice of RawTrades.
func (c *Calculator) CalculateFromTrades(trades []RawTrade, taxYear int) *TaxSummary {
	// Sort trades chronologically to ensure strict FIFO order
	sort.SliceStable(trades, func(i, j int) bool {
		return trades[i].Timestamp.Before(trades[j].Timestamp)
	})

	lotsBySymbol := make(map[string][]*TaxLot)
	var allDispositions []TaxDisposition

	for _, tr := range trades {
		sym := strings.ToUpper(tr.Symbol)
		side := strings.ToUpper(tr.Side)

		if side == "BUY" {
			// Inbound lot: enqueue
			costBasis := tr.Price * tr.Size
			lotsBySymbol[sym] = append(lotsBySymbol[sym], &TaxLot{
				ID:           fmt.Sprintf("%s-%d", sym, tr.Timestamp.UnixNano()),
				Symbol:       sym,
				AcquiredAt:   tr.Timestamp,
				Quantity:     tr.Size,
				CostBasisUSD: costBasis,
				FeeUSD:       tr.FeeUSDT,
				RemainingQty: tr.Size,
			})
		} else if side == "SELL" {
			// Outbound disposition: match against open lots in FIFO order
			sellQtyRemaining := tr.Size
			sellProceedsPerUnit := tr.Price
			sellFeePerUnit := 0.0
			if tr.Size > 0 {
				sellFeePerUnit = tr.FeeUSDT / tr.Size
			}

			lots := lotsBySymbol[sym]
			for len(lots) > 0 && sellQtyRemaining > 1e-8 {
				curLot := lots[0]
				matchQty := math.Min(curLot.RemainingQty, sellQtyRemaining)

				costPerUnit := 0.0
				buyFeePerUnit := 0.0
				if curLot.Quantity > 0 {
					costPerUnit = curLot.CostBasisUSD / curLot.Quantity
					buyFeePerUnit = curLot.FeeUSD / curLot.Quantity
				}

				proceedsUSD := matchQty * sellProceedsPerUnit
				basisUSD := matchQty * costPerUnit
				totalFeeUSD := matchQty * (buyFeePerUnit + sellFeePerUnit)
				netGainUSD := proceedsUSD - basisUSD - totalFeeUSD

				holdingType := HoldingShortTerm
				if tr.Timestamp.Sub(curLot.AcquiredAt) > 365*24*time.Hour {
					holdingType = HoldingLongTerm
				}

				allDispositions = append(allDispositions, TaxDisposition{
					Symbol:       sym,
					Description:  fmt.Sprintf("%.4f %s", matchQty, cleanAssetSymbol(sym)),
					AcquiredAt:   curLot.AcquiredAt,
					SoldAt:       tr.Timestamp,
					Quantity:     matchQty,
					ProceedsUSD:  proceedsUSD,
					CostBasisUSD: basisUSD,
					FeeUSD:       totalFeeUSD,
					NetGainUSD:   netGainUSD,
					HoldingType:  holdingType,
					TaxYear:      tr.Timestamp.Year(),
					Quarter:      GetQuarter(tr.Timestamp),
				})

				curLot.RemainingQty -= matchQty
				sellQtyRemaining -= matchQty

				if curLot.RemainingQty <= 1e-8 {
					lots = lots[1:]
				}
			}
			lotsBySymbol[sym] = lots

			// If selling without prior tracked buy lot (e.g. from starting position balance),
			// compute disposition with conservative break-even cost basis so tax report does not crash
			if sellQtyRemaining > 1e-8 {
				proceedsUSD := sellQtyRemaining * sellProceedsPerUnit
				feeUSD := sellQtyRemaining * sellFeePerUnit
				// Fallback basis: assume entry at sell price minus realized PnL proportion if recorded
				basisUSD := proceedsUSD - tr.PnLRealized - feeUSD
				if basisUSD < 0 {
					basisUSD = proceedsUSD
				}
				allDispositions = append(allDispositions, TaxDisposition{
					Symbol:       sym,
					Description:  fmt.Sprintf("%.4f %s", sellQtyRemaining, cleanAssetSymbol(sym)),
					AcquiredAt:   tr.Timestamp.Add(-1 * time.Hour), // Preceding lot imputed
					SoldAt:       tr.Timestamp,
					Quantity:     sellQtyRemaining,
					ProceedsUSD:  proceedsUSD,
					CostBasisUSD: basisUSD,
					FeeUSD:       feeUSD,
					NetGainUSD:   proceedsUSD - basisUSD - feeUSD,
					HoldingType:  HoldingShortTerm,
					TaxYear:      tr.Timestamp.Year(),
					Quarter:      GetQuarter(tr.Timestamp),
				})
			}
		}
	}

	// Filter dispositions by requested taxYear (0 = all years)
	var filtered []TaxDisposition
	for _, d := range allDispositions {
		if taxYear <= 0 || d.TaxYear == taxYear {
			filtered = append(filtered, d)
		}
	}

	targetYear := taxYear
	if targetYear <= 0 {
		if len(filtered) > 0 {
			targetYear = filtered[len(filtered)-1].TaxYear
		} else {
			targetYear = time.Now().Year()
		}
	}

	summary := &TaxSummary{
		TaxYear:          targetYear,
		GeneratedAt:      time.Now().UTC(),
		FederalRatePct:   c.FederalRatePct,
		StateRatePct:     c.StateRatePct,
		EffectiveRatePct: c.FederalRatePct + c.StateRatePct,
		Dispositions:     filtered,
	}

	quarterlyGains := map[string]float64{
		"Q1": 0.0,
		"Q2": 0.0,
		"Q3": 0.0,
		"Q4": 0.0,
	}

	for _, d := range filtered {
		summary.TotalDispositions++
		summary.TotalProceedsUSD += d.ProceedsUSD
		summary.TotalCostBasisUSD += d.CostBasisUSD
		summary.TotalFeesUSD += d.FeeUSD

		if d.HoldingType == HoldingShortTerm {
			if d.NetGainUSD >= 0 {
				summary.ShortTermGainsUSD += d.NetGainUSD
			} else {
				summary.ShortTermLossesUSD += math.Abs(d.NetGainUSD)
			}
			summary.NetShortTermGainUSD += d.NetGainUSD
		} else {
			if d.NetGainUSD >= 0 {
				summary.LongTermGainsUSD += d.NetGainUSD
			} else {
				summary.LongTermLossesUSD += math.Abs(d.NetGainUSD)
			}
			summary.NetLongTermGainUSD += d.NetGainUSD
		}

		quarterlyGains[d.Quarter] += d.NetGainUSD
	}

	summary.NetTaxableGainUSD = summary.NetShortTermGainUSD + summary.NetLongTermGainUSD

	// Calculate Estimated Federal and California State Tax Liabilities
	if summary.NetTaxableGainUSD > 0 {
		summary.EstimatedFederalTax = summary.NetTaxableGainUSD * (c.FederalRatePct * 0.01)
		summary.EstimatedStateTax = summary.NetTaxableGainUSD * (c.StateRatePct * 0.01)
		summary.TotalTaxReserveUSD = summary.EstimatedFederalTax + summary.EstimatedStateTax
	}

	// Build Safe Harbor IRS 1040-ES & CA 540-ES quarterly schedule
	summary.QuarterlySchedule = c.buildQuarterlySchedule(targetYear, quarterlyGains)

	return summary
}

// buildQuarterlySchedule constructs the Safe Harbor IRS 1040-ES / CA 540-ES payment milestones.
func (c *Calculator) buildQuarterlySchedule(year int, qGains map[string]float64) []QuarterlyEstimate {
	now := time.Now()
	quarters := []struct {
		q       string
		period  string
		dueYear int
		dueM    time.Month
		dueD    int
	}{
		{"Q1", "Jan 1 – Mar 31", year, time.April, 15},
		{"Q2", "Apr 1 – May 31", year, time.June, 15},
		{"Q3", "Jun 1 – Aug 31", year, time.September, 15},
		{"Q4", "Sep 1 – Dec 31", year + 1, time.January, 15},
	}

	var res []QuarterlyEstimate
	for _, qInfo := range quarters {
		dueDate := time.Date(qInfo.dueYear, qInfo.dueM, qInfo.dueD, 23, 59, 59, 0, time.Local)
		gain := qGains[qInfo.q]
		fedTax := 0.0
		stateTax := 0.0
		totalRes := 0.0
		if gain > 0 {
			fedTax = gain * (c.FederalRatePct * 0.01)
			stateTax = gain * (c.StateRatePct * 0.01)
			totalRes = fedTax + stateTax
		}

		isCurrent := false
		curQ := GetQuarter(now)
		if now.Year() == year && curQ == qInfo.q {
			isCurrent = true
		}
		isPastDue := now.After(dueDate)

		res = append(res, QuarterlyEstimate{
			Quarter:      qInfo.q,
			Period:       qInfo.period,
			DueDate:      dueDate.Format("January 02, 2006"),
			RealizedGain: gain,
			FederalTax:   fedTax,
			StateTax:     stateTax,
			TotalReserve: totalRes,
			IsCurrent:    isCurrent,
			IsPastDue:    isPastDue,
		})
	}
	return res
}

// ExportForm8949CSV generates standard IRS Form 8949 CSV format.
func ExportForm8949CSV(summary *TaxSummary) ([]byte, error) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)

	// IRS Form 8949 Standard Header
	header := []string{
		"Description of Property",
		"Date Acquired",
		"Date Sold or Disposed",
		"Proceeds (Sales Price)",
		"Cost or Other Basis",
		"Code(s)",
		"Amount of Adjustment (Fees)",
		"Gain or (Loss)",
		"Holding Term",
		"Quarter",
	}
	if err := writer.Write(header); err != nil {
		return nil, err
	}

	for _, d := range summary.Dispositions {
		record := []string{
			d.Description,
			d.AcquiredAt.Format("01/02/2006"),
			d.SoldAt.Format("01/02/2006"),
			fmt.Sprintf("%.2f", d.ProceedsUSD),
			fmt.Sprintf("%.2f", d.CostBasisUSD),
			"B", // Code B: Form 1099-B not received or not showing basis
			fmt.Sprintf("%.4f", d.FeeUSD),
			fmt.Sprintf("%.2f", d.NetGainUSD),
			string(d.HoldingType),
			d.Quarter,
		}
		if err := writer.Write(record); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	return buf.Bytes(), writer.Error()
}

// FormatTerminalReport renders an institutional tax report directly to terminal stdout.
func FormatTerminalReport(s *TaxSummary) string {
	var sb strings.Builder
	sb.WriteString("===============================================================================\n")
	sb.WriteString(fmt.Sprintf("         PIQUANT CRYPTO TAX REPORT — TAX YEAR %d (IRS & CA FTB)\n", s.TaxYear))
	sb.WriteString("===============================================================================\n")
	sb.WriteString(fmt.Sprintf(" Generated:           %s\n", s.GeneratedAt.Format("2006-01-02 15:04:05 UTC")))
	sb.WriteString(fmt.Sprintf(" Total Dispositions:  %d trades processed (FIFO Lot Matching)\n", s.TotalDispositions))
	sb.WriteString(fmt.Sprintf(" Gross Proceeds:      $%s USD\n", formatMoney(s.TotalProceedsUSD)))
	sb.WriteString(fmt.Sprintf(" Cost Basis:          $%s USD\n", formatMoney(s.TotalCostBasisUSD)))
	sb.WriteString(fmt.Sprintf(" Trading Fees Paid:   $%s USD (Fully Deductible Capital Expense)\n", formatMoney(s.TotalFeesUSD)))
	sb.WriteString("-------------------------------------------------------------------------------\n")
	sb.WriteString(" CAPITAL GAINS / LOSSES SUMMARY:\n")
	sb.WriteString(fmt.Sprintf("   Short-Term Gains:  +$%s USD\n", formatMoney(s.ShortTermGainsUSD)))
	sb.WriteString(fmt.Sprintf("   Short-Term Losses: -$%s USD\n", formatMoney(s.ShortTermLossesUSD)))
	sb.WriteString(fmt.Sprintf("   Net Short-Term:    %s$%s USD\n", sign(s.NetShortTermGainUSD), formatMoney(math.Abs(s.NetShortTermGainUSD))))
	sb.WriteString(fmt.Sprintf("   Net Long-Term:     %s$%s USD\n", sign(s.NetLongTermGainUSD), formatMoney(math.Abs(s.NetLongTermGainUSD))))
	sb.WriteString(fmt.Sprintf("   TOTAL TAXABLE:     %s$%s USD\n", sign(s.NetTaxableGainUSD), formatMoney(math.Abs(s.NetTaxableGainUSD))))
	sb.WriteString("-------------------------------------------------------------------------------\n")
	sb.WriteString(" ESTIMATED TAX LIABILITIES & ESCROW RESERVE:\n")
	sb.WriteString(fmt.Sprintf("   Federal Tax (IRS @ %.1f%%):         $%s USD\n", s.FederalRatePct, formatMoney(s.EstimatedFederalTax)))
	sb.WriteString(fmt.Sprintf("   California Tax (CA FTB @ %.1f%%):    $%s USD\n", s.StateRatePct, formatMoney(s.EstimatedStateTax)))
	sb.WriteString(fmt.Sprintf("   Effective Combined Rate:            %.1f%%\n", s.EffectiveRatePct))
	sb.WriteString(fmt.Sprintf("   ★ TOTAL TAX RESERVE TO SET ASIDE:  $%s USD ★\n", formatMoney(s.TotalTaxReserveUSD)))
	sb.WriteString("-------------------------------------------------------------------------------\n")
	sb.WriteString(" SAFE HARBOR QUARTERLY ESTIMATED TAX SCHEDULE (IRS 1040-ES & CA 540-ES):\n")
	sb.WriteString(" Quarter | Period            | Due Date           | Realized Gain | Tax Reserve | Status\n")
	sb.WriteString(" ------- | ----------------- | ------------------ | ------------- | ----------- | ------\n")
	for _, q := range s.QuarterlySchedule {
		status := "UPCOMING"
		if q.IsPastDue {
			status = "PAST DUE"
		} else if q.IsCurrent {
			status = "CURRENT"
		}
		sb.WriteString(fmt.Sprintf(" %-7s | %-17s | %-18s | %13s | %11s | %s\n",
			q.Quarter, q.Period, q.DueDate,
			fmt.Sprintf("$%.2f", q.RealizedGain),
			fmt.Sprintf("$%.2f", q.TotalReserve),
			status))
	}
	sb.WriteString("===============================================================================\n")
	sb.WriteString(" Actions:\n")
	sb.WriteString("   • Export Form 8949 CSV:   piquant tax --export form8949.csv\n")
	sb.WriteString("   • Print lot table:        piquant tax --table\n")
	sb.WriteString("   • Web control panel:      http://localhost:8080/tax\n")
	sb.WriteString("===============================================================================\n")
	return sb.String()
}

// FormatDispositionsTable formats a line-by-line Form 8949 ASCII table for inspection.
func FormatDispositionsTable(s *TaxSummary) string {
	var sb strings.Builder
	sb.WriteString("\nIRS FORM 8949 - SALES AND OTHER DISPOSITIONS OF CAPITAL ASSETS\n")
	sb.WriteString("-------------------------------------------------------------------------------------------------------------------------\n")
	sb.WriteString("Description      | Acquired   | Sold       | Proceeds   | Cost Basis | Fees Paid  | Gain / (Loss) | Holding Term | Quarter\n")
	sb.WriteString("-------------------------------------------------------------------------------------------------------------------------\n")
	for _, d := range s.Dispositions {
		sb.WriteString(fmt.Sprintf("%-16s | %10s | %10s | %10.2f | %10.2f | %10.4f | %13.2f | %-12s | %s\n",
			d.Description,
			d.AcquiredAt.Format("2006-01-02"),
			d.SoldAt.Format("2006-01-02"),
			d.ProceedsUSD,
			d.CostBasisUSD,
			d.FeeUSD,
			d.NetGainUSD,
			d.HoldingType,
			d.Quarter,
		))
	}
	sb.WriteString("-------------------------------------------------------------------------------------------------------------------------\n")
	return sb.String()
}

// GetQuarter determines calendar quarter for estimated taxes.
func GetQuarter(t time.Time) string {
	m := t.Month()
	switch {
	case m >= 1 && m <= 3:
		return "Q1"
	case m >= 4 && m <= 5:
		return "Q2"
	case m >= 6 && m <= 8:
		return "Q3"
	default:
		return "Q4"
	}
}

func cleanAssetSymbol(sym string) string {
	for _, quote := range []string{"USDT", "USDC", "USD", "BUSD"} {
		if strings.HasSuffix(sym, quote) {
			return strings.TrimSuffix(sym, quote)
		}
	}
	return sym
}

func sign(v float64) string {
	if v >= 0 {
		return "+"
	}
	return "-"
}

func formatMoney(v float64) string {
	parts := strings.Split(fmt.Sprintf("%.2f", v), ".")
	intPart := parts[0]
	decPart := parts[1]

	var res []byte
	n := len(intPart)
	for i, ch := range []byte(intPart) {
		if i > 0 && (n-i)%3 == 0 && ch != '-' {
			res = append(res, ',')
		}
		res = append(res, ch)
	}
	return string(res) + "." + decPart
}
