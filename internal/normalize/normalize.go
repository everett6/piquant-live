// Package normalize converts dataset records into engine events, per venue.
//
// Venues do not agree on what a sequence number, a side, or even a timestamp
// means, so each venue gets its own parser and its own documented assumptions.
// Nothing here guesses: a record that the venue adapter cannot interpret is
// rejected with a reason, and the replay report counts rejections by reason.
//
// Normalisation never reorders or drops events silently. The replay engine is
// responsible for handling out-of-order arrival; the normaliser only converts
// units and checks that a message makes sense for its venue.
package normalize

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"piquant-live/internal/config"
	"piquant-live/internal/dataset"
	"piquant-live/internal/event"
)

// Venue adapters.
const (
	Binance  = "binance"
	Coinbase = "coinbase"
	Kraken   = "kraken"
)

// RejectKind classifies a refused record so the quality report can aggregate
// by cause.
type RejectKind string

// Rejection kinds.
const (
	KindUnknownVenue  RejectKind = "unknown-venue"
	KindUnknownSymbol RejectKind = "unknown-symbol"
	KindNoTimestamp   RejectKind = "missing-timestamp"
	KindBadTimestamp  RejectKind = "bad-timestamp"
	KindScale         RejectKind = "price-or-size-out-of-range"
	KindSnapshotShape RejectKind = "snapshot-shape"
	KindUnsupported   RejectKind = "unsupported-record"
)

// RejectError is a normalisation failure that carries its kind, so callers can
// count causes without matching on message text.
type RejectError struct {
	Kind RejectKind
	Msg  string
}

func (e *RejectError) Error() string { return string(e.Kind) + ": " + e.Msg }

func reject(kind RejectKind, format string, args ...any) error {
	return &RejectError{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// Normalizer converts records from one venue into envelopes.
//
// It is not safe for concurrent use: it tracks the last sequence per
// instrument, which the venue's ordering rules are checked against.
type Normalizer struct {
	venue   string
	symbols map[string]uint16 // instrument -> engine symbol id
	venues  map[uint16]uint16 // engine symbol id -> venue id
	last    map[uint16]uint64 // engine symbol id -> last accepted source sequence
	seqGap  map[uint16]uint64 // per-symbol sequence violations

	// rejections counts every refused record by reason.
	rejections map[RejectKind]int64
	duplicates int64
}

// New builds a normalizer for venue, using the configured symbols. Every
// instrument listed in cfg.Targets for a symbol is accepted for that symbol at
// this venue; the first configured target spelling is used as the canonical
// instrument name.
func New(venue string, cfg *config.Config) (*Normalizer, error) {
	if err := checkVenue(venue); err != nil {
		return nil, err
	}
	n := &Normalizer{
		venue:      strings.ToLower(venue),
		symbols:    make(map[string]uint16),
		venues:     make(map[uint16]uint16),
		last:       make(map[uint16]uint64),
		seqGap:     make(map[uint16]uint64),
		rejections: make(map[RejectKind]int64),
	}
	for _, s := range cfg.Symbols {
		id := config.SymbolID(s.Name)
		for _, target := range cfg.Targets[s.Name] {
			v, instr, ok := strings.Cut(target, ":")
			if !ok || strings.ToLower(v) != n.venue {
				continue
			}
			n.symbols[instrumentKey(n.venue, instr)] = id
			n.venues[id] = config.VenueID(n.venue)
		}
	}
	if len(n.symbols) == 0 {
		return nil, fmt.Errorf("normalize: %s has no configured instruments", venue)
	}
	return n, nil
}

func checkVenue(venue string) error {
	switch strings.ToLower(venue) {
	case Binance, Coinbase, Kraken:
		return nil
	}
	return fmt.Errorf("normalize: unsupported venue %q", venue)
}

// InstrumentKey canonicalises an instrument name for one venue. Binance uses
// exchange symbols (BTCUSDT), Coinbase and Kraken use dashes (BTC-USD).
func InstrumentKey(venue, instrument string) string { return instrumentKey(venue, instrument) }

// instrumentKey canonicalises an instrument name for one venue.
func instrumentKey(venue, instrument string) string {
	s := strings.ToUpper(strings.TrimSpace(instrument))
	if venue == Coinbase && !strings.Contains(s, "-") {
		// Coinbase also publishes BTCUSD in some endpoints.
		return s
	}
	return s
}

// Reject counts a refused message by kind.
func (n *Normalizer) Reject(r RejectKind) { n.rejections[r]++ }

// CountReject counts the error returned by Envelope when it is a RejectError,
// so a replay loop can report data quality without parsing messages.
func (n *Normalizer) CountReject(err error) {
	var re *RejectError
	if errors.As(err, &re) {
		n.rejections[re.Kind]++
		return
	}
	n.rejections[KindUnsupported]++
}

// Duplicates reports how many records were recognised as repeats of the last
// accepted sequence for their instrument.
func (n *Normalizer) Duplicates() int64 { return n.duplicates }

// Rejections returns a copy of the reason counts.
func (n *Normalizer) Rejections() map[RejectKind]int64 {
	out := make(map[RejectKind]int64, len(n.rejections))
	for k, v := range n.rejections {
		out[k] = v
	}
	return out
}

// SymbolFor returns the engine symbol id a venue instrument maps to.
func (n *Normalizer) SymbolFor(instrument string) (uint16, bool) {
	id, ok := n.symbols[instrumentKey(n.venue, instrument)]
	return id, ok
}

// SequenceGaps returns the per-symbol count of out-of-order source sequences.
func (n *Normalizer) SequenceGaps() map[uint16]uint64 {
	out := make(map[uint16]uint64, len(n.seqGap))
	for k, v := range n.seqGap {
		out[k] = v
	}
	return out
}

// Receive timestamps must look like unix nanoseconds: after 2001-09-09 and
// before 2262 (the int64 limit). Anything outside is a units error (seconds or
// microseconds in a nanosecond field) and would poison every time window.
const (
	minRecvTS = int64(1_000_000_000_000_000_000)
	maxRecvTS = int64(9_000_000_000_000_000_000)
)

// Envelope converts one record. It returns nil, nil for a record that is
// intentionally skipped (a heartbeat, or a duplicate), and nil, err for a
// record that must be counted as a rejection.
func (n *Normalizer) Envelope(rec *dataset.Record) (*event.Envelope, error) {
	if !strings.EqualFold(rec.Venue, n.venue) {
		return nil, reject(KindUnknownVenue, "record from venue %q", rec.Venue)
	}
	if rec.Type == dataset.TypeHeartbeat {
		return nil, nil
	}
	symID, ok := n.symbols[instrumentKey(n.venue, rec.Instrument)]
	if !ok {
		return nil, reject(KindUnknownSymbol, "instrument %q", rec.Instrument)
	}
	send, recv, err := timestamps(rec)
	if err != nil {
		return nil, err
	}
	env := &event.Envelope{
		Sequence: rec.Seq,
		Venue:    n.venues[symID],
		Symbol:   symID,
		SendTime: send,
		RecvTime: recv,
	}
	switch rec.Type {
	case dataset.TypeTrade:
		t, err := n.trade(rec)
		if err != nil {
			return nil, err
		}
		env.Kind = event.KindTrade
		env.Trade = *t
	case dataset.TypeDepth:
		d, err := n.depth(rec)
		if err != nil {
			return nil, err
		}
		env.Kind = event.KindDepth
		env.Depth = *d
	case dataset.TypeSnapshot:
		s, err := n.snapshot(rec)
		if err != nil {
			return nil, err
		}
		env.Kind = event.KindSnapshot
		env.Snap = *s
	default:
		return nil, reject(KindUnsupported, "record type %q", rec.Type)
	}
	// The engine requires a non-zero ingest sequence. A venue that publishes
	// none (the capture writer assigns one) keeps whatever the record carries.
	if env.Sequence == 0 {
		return nil, reject(KindUnsupported, "record carries no sequence")
	}
	// Venue sequence rules. Binance documents that a message with a sequence at
	// or below the last applied one is a repeat to be ignored; Coinbase and
	// Kraken sequence per instrument but have no gap-fill protocol, so an
	// exact repeat is a duplicate and a jump is a missing message. Either way
	// the count is kept, and the book decides how to recover.
	prev, seen := n.last[symID]
	if seen {
		if env.Sequence == prev || (n.venue == Binance && env.Sequence < prev) {
			n.duplicates++
			return nil, nil
		}
		if env.Sequence > prev+1 && n.venue != Binance {
			n.seqGap[symID]++
		}
		if env.Sequence < prev {
			// Out of order for a venue without a repeat rule: hand it to the
			// book, whose sequence audit turns it into a stale book.
			n.seqGap[symID]++
		}
	}
	if !seen || env.Sequence > prev {
		n.last[symID] = env.Sequence
	}
	return env, nil
}

// timestamps returns the venue send time and the local receive time in ns.
func timestamps(rec *dataset.Record) (send, recv int64, err error) {
	send, recv = rec.SendTS, rec.RecvTS
	if recv == 0 {
		recv = send
	}
	if recv == 0 {
		return 0, 0, reject(KindNoTimestamp, "record has no timestamp")
	}
	if recv < minRecvTS || recv > maxRecvTS {
		// Almost always seconds-or-microseconds accidentally placed in a ns
		// field. Rejecting is honest; reinterpreting would invent data.
		return 0, 0, reject(KindBadTimestamp, "recv_ts %d is not a plausibly nanosecond timestamp", recv)
	}
	if send == 0 {
		send = recv
	}
	return send, recv, nil
}

// trade converts a print.
func (n *Normalizer) trade(rec *dataset.Record) (*event.TradeEvent, error) {
	price, err := priceFixed(rec.Price)
	if err != nil {
		return nil, reject(KindScale, "price %g: %v", rec.Price, err)
	}
	qty, err := qtyFixed(rec.Qty)
	if err != nil {
		return nil, reject(KindScale, "qty %g: %v", rec.Qty, err)
	}
	side := event.SideBuy
	if rec.Side == dataset.Sell {
		side = event.SideSell
	} else if rec.Side != dataset.Buy {
		return nil, reject(KindUnsupported, "trade side %q", rec.Side)
	}
	return &event.TradeEvent{Price: price, Qty: qty, Side: side, TradeID: rec.TradeID}, nil
}

// depth converts one side of an incremental update. Venues send either one
// side per message (Binance, Coinbase) or a delta whose side is implicit in a
// signed size (Kraken); the record shape covers the first, and Kraken's
// adapter rejects a delta whose signed size cannot be interpreted rather than
// inventing a side.
func (n *Normalizer) depth(rec *dataset.Record) (*event.DepthEvent, error) {
	levels := rec.Bids
	if rec.Side == dataset.Sell {
		levels = rec.Asks
	}
	if len(levels) > event.MaxLevels {
		// More levels than the book tracks: keep the best MaxLevels, which are
		// first by the record validation rule.
		levels = levels[:event.MaxLevels]
	}
	d := &event.DepthEvent{Sequence: rec.Seq, Side: event.SideBuy}
	if rec.Side == dataset.Sell {
		d.Side = event.SideSell
	}
	d.NUpdates = uint8(len(levels))
	for i := range levels {
		p, err := priceFixed(levels[i].Price)
		if err != nil {
			return nil, reject(KindScale, "level %d price %g: %v", i, levels[i].Price, err)
		}
		q, err := qtyFixed(levels[i].Qty)
		if err != nil {
			return nil, reject(KindScale, "level %d qty %g: %v", i, levels[i].Qty, err)
		}
		d.Updates[i] = event.Level{Price: p, Qty: q}
	}
	return d, nil
}

// snapshot converts a full book. Sizes are kept as sent (including zero, which
// means an empty level); the book drops empty levels itself. Levels are
// ordered best-first; the capture writer is responsible for that order, and a
// snapshot that is not ordered is rejected here rather than re-sorted, because
// re-sorting a broken capture would hide the problem.
func (n *Normalizer) snapshot(rec *dataset.Record) (*event.SnapshotEvent, error) {
	if len(rec.Bids) > event.MaxLevels || len(rec.Asks) > event.MaxLevels {
		rec.Bids = rec.Bids[:min(len(rec.Bids), event.MaxLevels)]
		rec.Asks = rec.Asks[:min(len(rec.Asks), event.MaxLevels)]
	}
	if !sortedDesc(rec.Bids) || !sortedAsc(rec.Asks) {
		return nil, reject(KindSnapshotShape, "snapshot levels are not best-first")
	}
	s := &event.SnapshotEvent{Sequence: rec.Seq}
	for i := range rec.Bids {
		p, err := priceFixed(rec.Bids[i].Price)
		if err != nil {
			return nil, reject(KindScale, "bid %d price: %v", i, err)
		}
		q, err := qtyFixed(rec.Bids[i].Qty)
		if err != nil {
			return nil, reject(KindScale, "bid %d qty: %v", i, err)
		}
		s.Bids[i] = event.Level{Price: p, Qty: q}
	}
	for i := range rec.Asks {
		p, err := priceFixed(rec.Asks[i].Price)
		if err != nil {
			return nil, reject(KindScale, "ask %d price: %v", i, err)
		}
		q, err := qtyFixed(rec.Asks[i].Qty)
		if err != nil {
			return nil, reject(KindScale, "ask %d qty: %v", i, err)
		}
		s.Asks[i] = event.Level{Price: p, Qty: q}
	}
	s.NBids = uint8(len(rec.Bids))
	s.NAsks = uint8(len(rec.Asks))
	return s, nil
}

func sortedDesc(ls []dataset.Level) bool {
	return sort.SliceIsSorted(ls, func(i, j int) bool { return ls[i].Price > ls[j].Price })
}

func sortedAsc(ls []dataset.Level) bool {
	return sort.SliceIsSorted(ls, func(i, j int) bool { return ls[i].Price < ls[j].Price })
}

// priceFixed converts a price to fixed point. A price must be strictly
// positive: zero or negative is a corrupt record, not a book state.
func priceFixed(v float64) (int64, error) {
	if !(v > 0) {
		return 0, fmt.Errorf("non-positive price %g", v)
	}
	return toFixed(v, event.PriceScale, 18)
}

// qtyFixed converts a size to fixed point. Zero is legal (a deleted level).
func qtyFixed(v float64) (int64, error) { return toFixed(v, event.QtyScale, 16) }

// toFixed converts a floating-point price or size to fixed point, rejecting
// values that cannot be represented without overflow.
//
// The float64 form is only ever used at the capture boundary: JSON is float64,
// and a venue that encodes a price in decimal text can lose a bit or two in
// the round trip. Anything that does not fit the integer range is refused
// rather than wrapped.
func toFixed(v float64, scale int64, maxDigits int) (int64, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("not finite")
	}
	if v < 0 {
		return 0, fmt.Errorf("negative")
	}
	ticks := math.Round(v * float64(scale))
	if math.Abs(ticks) >= math.Pow10(maxDigits) {
		return 0, fmt.Errorf("more than %d digits", maxDigits)
	}
	return int64(ticks), nil
}
