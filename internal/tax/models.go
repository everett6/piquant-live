package tax

import (
	"time"
)

// HoldingType classifies gain as short-term (<= 1 year) or long-term (> 1 year).
type HoldingType string

const (
	HoldingShortTerm HoldingType = "SHORT_TERM"
	HoldingLongTerm  HoldingType = "LONG_TERM"
)

// TaxLot tracks an individual purchase lot of crypto.
type TaxLot struct {
	ID           string    `json:"id"`
	Symbol       string    `json:"symbol"`
	AcquiredAt   time.Time `json:"acquired_at"`
	Quantity     float64   `json:"quantity"`
	CostBasisUSD float64   `json:"cost_basis_usd"`
	FeeUSD       float64   `json:"fee_usd"`
	RemainingQty float64   `json:"remaining_qty"`
}

// TaxDisposition represents a completed sale matching against a tax lot (Form 8949 item).
type TaxDisposition struct {
	Symbol       string      `json:"symbol"`
	Description  string      `json:"description"`   // e.g. "0.0010 BTC"
	AcquiredAt   time.Time   `json:"acquired_at"`
	SoldAt       time.Time   `json:"sold_at"`
	Quantity     float64     `json:"quantity"`
	ProceedsUSD  float64     `json:"proceeds_usd"`
	CostBasisUSD float64     `json:"cost_basis_usd"`
	FeeUSD       float64     `json:"fee_usd"`       // Deductible exchange fees
	NetGainUSD   float64     `json:"net_gain_usd"`  // Proceeds - CostBasis - FeeUSD
	HoldingType  HoldingType `json:"holding_type"`
	TaxYear      int         `json:"tax_year"`
	Quarter      string      `json:"quarter"`       // "Q1", "Q2", "Q3", "Q4"
}

// QuarterlyEstimate tracks IRS Form 1040-ES and CA Form 540-ES safe harbor estimated tax payments.
type QuarterlyEstimate struct {
	Quarter      string    `json:"quarter"`       // "Q1", "Q2", "Q3", "Q4"
	Period       string    `json:"period"`        // e.g. "Jan 1 - Mar 31"
	DueDate      string    `json:"due_date"`      // e.g. "April 15, 2026"
	RealizedGain float64   `json:"realized_gain"`
	FederalTax   float64   `json:"federal_tax"`
	StateTax     float64   `json:"state_tax"`
	TotalReserve float64   `json:"total_reserve"`
	IsCurrent    bool      `json:"is_current"`
	IsPastDue    bool      `json:"is_past_due"`
}

// TaxSummary holds the comprehensive tax report.
type TaxSummary struct {
	TaxYear              int                 `json:"tax_year"`
	GeneratedAt          time.Time           `json:"generated_at"`
	TotalDispositions    int                 `json:"total_dispositions"`
	TotalProceedsUSD     float64             `json:"total_proceeds_usd"`
	TotalCostBasisUSD    float64             `json:"total_cost_basis_usd"`
	TotalFeesUSD         float64             `json:"total_fees_usd"`
	ShortTermGainsUSD    float64             `json:"short_term_gains_usd"`
	ShortTermLossesUSD   float64             `json:"short_term_losses_usd"`
	NetShortTermGainUSD  float64             `json:"net_short_term_gain_usd"`
	LongTermGainsUSD     float64             `json:"long_term_gains_usd"`
	LongTermLossesUSD    float64             `json:"long_term_losses_usd"`
	NetLongTermGainUSD   float64             `json:"net_long_term_gain_usd"`
	NetTaxableGainUSD    float64             `json:"net_taxable_gain_usd"`
	FederalRatePct       float64             `json:"federal_rate_pct"`
	StateRatePct         float64             `json:"state_rate_pct"`
	EffectiveRatePct     float64             `json:"effective_rate_pct"`
	EstimatedFederalTax  float64             `json:"estimated_federal_tax"`
	EstimatedStateTax    float64             `json:"estimated_state_tax"`
	TotalTaxReserveUSD   float64             `json:"total_tax_reserve_usd"`
	QuarterlySchedule    []QuarterlyEstimate `json:"quarterly_schedule"`
	Dispositions         []TaxDisposition    `json:"dispositions,omitempty"`
}
