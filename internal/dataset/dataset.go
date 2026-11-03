// Package dataset reads and writes the versioned historical capture format.
//
// A dataset is one file of newline-delimited JSON records. Each line is one
// venue message, already decoded from the venue's wire format but not yet
// normalised: the reader does not guess what a field means, it checks the
// envelope and hands the record to the venue-aware normaliser.
//
// The format is deliberately boring: a line is a complete record, so a capture
// truncated by a power cut loses only the last (partial) line, which the reader
// reports as a rejection instead of failing the whole file. Files are read with
// a streaming scanner, so a 4 GiB capture never has to fit in RAM.
//
//	A record is: {"v":1,"venue":"binance","instrument":"BTCUSDT",
//	              "type":"trade|depth|snapshot|heartbeat",
//	              "send_ts":<unix ns>,"recv_ts":<unix ns>,
//	              "seq":<source sequence, 0 when the venue has none>,
//	              ...type-specific fields...}
//
// See docs/DATA_FORMAT.md for the full field table and the per-venue notes.
package dataset

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Version is the record schema version this package reads and writes.
const Version = 1

// RecordType names the payload of a record.
type RecordType string

// Record types.
const (
	TypeTrade     RecordType = "trade"
	TypeDepth     RecordType = "depth"
	TypeSnapshot  RecordType = "snapshot"
	TypeHeartbeat RecordType = "heartbeat"
)

// Side names the aggressor of a trade or the side of a depth update.
type Side string

// Sides.
const (
	Buy  Side = "buy"
	Sell Side = "sell"
)

// Level is one price level in a record, in venue units (not fixed point).
type Level struct {
	Price float64 `json:"price"`
	Qty   float64 `json:"qty"`
}

// Record is one line of a dataset. Field names are part of the format; adding
// a field is a compatible change, changing or removing one is not.
type Record struct {
	Version    int        `json:"v"`
	Venue      string     `json:"venue"`
	Instrument string     `json:"instrument"`
	Type       RecordType `json:"type"`
	SendTS     int64      `json:"send_ts"` // venue timestamp, unix ns; 0 if absent
	RecvTS     int64      `json:"recv_ts"` // local receive timestamp, unix ns; 0 if absent
	Seq        uint64     `json:"seq"`     // source sequence; 0 when the venue has none

	// Trade.
	Price   float64 `json:"price,omitempty"`
	Qty     float64 `json:"qty,omitempty"`
	Side    Side    `json:"side,omitempty"`
	TradeID uint64  `json:"trade_id,omitempty"`

	// Depth: absolute sizes at the listed prices, qty 0 deletes the level.
	// Snapshot: both sides, best first.
	Bids []Level `json:"bids,omitempty"`
	Asks []Level `json:"asks,omitempty"`
}

// Validate checks the record's own fields, without venue knowledge.
func (r *Record) Validate() error {
	if r.Version != Version {
		return fmt.Errorf("unsupported record version %d (want %d)", r.Version, Version)
	}
	if r.Venue == "" {
		return errors.New("missing venue")
	}
	if r.Instrument == "" {
		return errors.New("missing instrument")
	}
	if r.RecvTS < 0 || r.SendTS < 0 {
		return errors.New("negative timestamp")
	}
	if r.RecvTS == 0 && r.SendTS == 0 {
		return errors.New("record has no timestamp at all")
	}
	switch r.Type {
	case TypeTrade:
		if !(r.Price > 0) || !(r.Qty > 0) {
			return fmt.Errorf("trade with non-positive price/qty (%g/%g)", r.Price, r.Qty)
		}
		if r.Side != Buy && r.Side != Sell {
			return fmt.Errorf("trade with side %q", r.Side)
		}
	case TypeDepth:
		if r.Side != Buy && r.Side != Sell {
			return fmt.Errorf("depth with side %q", r.Side)
		}
		levels := r.Bids
		if r.Side == Sell {
			levels = r.Asks
		}
		if len(levels) == 0 {
			return fmt.Errorf("depth message for side %q carries no levels on that side", r.Side)
		}
		if err := validateLevels(levels, false); err != nil {
			return err
		}
	case TypeSnapshot:
		if len(r.Bids) == 0 || len(r.Asks) == 0 {
			return errors.New("snapshot with an empty side")
		}
		if err := validateLevels(r.Bids, true); err != nil {
			return fmt.Errorf("bids: %w", err)
		}
		if err := validateLevels(r.Asks, false); err != nil {
			return fmt.Errorf("asks: %w", err)
		}
		if r.Bids[0].Price >= r.Asks[0].Price {
			return fmt.Errorf("crossed or locked snapshot (%g >= %g)", r.Bids[0].Price, r.Asks[0].Price)
		}
	case TypeHeartbeat:
	default:
		return fmt.Errorf("unknown record type %q", r.Type)
	}
	return nil
}

// validateLevels checks price and size validity, and that prices strictly
// decrease (bids) or increase (asks).
func validateLevels(levels []Level, descending bool) error {
	for i := range levels {
		if !(levels[i].Price > 0) {
			return fmt.Errorf("level %d: non-positive price %g", i, levels[i].Price)
		}
		if levels[i].Qty < 0 {
			return fmt.Errorf("level %d: negative qty %g", i, levels[i].Qty)
		}
		if i > 0 {
			prev := levels[i-1].Price
			if descending && levels[i].Price >= prev {
				return fmt.Errorf("levels not strictly descending at %d (%g >= %g)", i, levels[i].Price, prev)
			}
			if !descending && levels[i].Price <= prev {
				return fmt.Errorf("levels not strictly ascending at %d (%g <= %g)", i, levels[i].Price, prev)
			}
		}
	}
	return nil
}

// Rejection records a line the reader refused, with enough context to fix the
// capture. Rejections are data-quality findings, never silently dropped.
type Rejection struct {
	Line int    // 1-based line number in the file
	Err  string // why the line was refused
}

// Quality summarises a dataset read.
type Quality struct {
	Records    int64 // accepted records
	Bytes      int64 // bytes read
	Rejected   int64 // lines refused (malformed, wrong version, invalid fields)
	Rejections []Rejection
	// FirstRecv, LastRecv bound the accepted records' receive timestamps.
	FirstRecv, LastRecv int64
	// Reordered counts accepted records whose receive timestamp is older than
	// the previous accepted record's.
	Reordered int64
}

// maxRejectionsKept bounds the in-memory rejection list; the count is exact.
const maxRejectionsKept = 64

// Add records one rejection, retaining only the first maxRejectionsKept.
func (q *Quality) Add(line int, err error) {
	q.Rejected++
	if len(q.Rejections) < maxRejectionsKept {
		q.Rejections = append(q.Rejections, Rejection{Line: line, Err: err.Error()})
	}
}

// Reader streams records out of a dataset file.
type Reader struct {
	f   *os.File
	sc  *bufio.Scanner
	buf []byte
	ln  int
	q   Quality

	prevRecv int64
	path     string
}

// maxLine bounds one record. A snapshot of 20 levels a side is a few hundred
// bytes; 1 MiB leaves room for venues that send deeper books.
const maxLine = 1 << 20

// Open opens path for streaming. It does not read the file yet; Next does.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	return &Reader{f: f, sc: sc, path: path}, nil
}

// Path returns the file the reader is streaming.
func (r *Reader) Path() string { return r.path }

// Next returns the next accepted record. A line that fails validation is
// recorded in Quality and skipped, so one bad line does not abort a capture.
// It returns io.EOF at the end.
func (r *Reader) Next() (*Record, error) {
	for {
		if !r.sc.Scan() {
			if err := r.sc.Err(); err != nil {
				return nil, fmt.Errorf("%s line %d: %w", r.path, r.ln+1, err)
			}
			return nil, io.EOF
		}
		r.ln++
		line := r.sc.Bytes()
		r.q.Bytes += int64(len(line)) + 1
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			r.q.Add(r.ln, err)
			continue
		}
		if err := rec.Validate(); err != nil {
			r.q.Add(r.ln, err)
			continue
		}
		recv := rec.RecvTS
		if recv == 0 {
			recv = rec.SendTS
		}
		if r.q.Records == 0 {
			r.q.FirstRecv = recv
		} else if recv < r.prevRecv {
			r.q.Reordered++
		}
		if recv > r.q.LastRecv {
			r.q.LastRecv = recv
		}
		r.prevRecv = recv
		r.q.Records++
		return &rec, nil
	}
}

// Quality returns the running quality report. It is valid before EOF.
func (r *Reader) Quality() Quality { return r.q }

// Close closes the file.
func (r *Reader) Close() error { return r.f.Close() }

// Writer appends records to a dataset file.
type Writer struct {
	f *os.File
	w *bufio.Writer
}

// Create truncates or creates path for writing.
func Create(path string) (*Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &Writer{f: f, w: bufio.NewWriterSize(f, 64<<10)}, nil
}

// Write appends one record.
func (w *Writer) Write(rec *Record) error {
	if rec.Version == 0 {
		rec.Version = Version
	}
	if err := rec.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := w.w.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// Close flushes and closes the file.
func (w *Writer) Close() error {
	if err := w.w.Flush(); err != nil {
		w.f.Close()
		return err
	}
	return w.f.Close()
}

// Checksum computes the SHA-256 of a dataset file, streaming it. The returned
// hex string identifies exactly the bytes an experiment consumed, so a report
// can be tied back to its input even after the file is moved.
func Checksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Describe renders a short human-readable summary, used in reports.
func (q Quality) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d records, %d rejected, %d reordered", q.Records, q.Rejected, q.Reordered)
	if len(q.Rejections) > 0 {
		b.WriteString("; first rejections: ")
		for i, rej := range q.Rejections {
			if i > 0 {
				b.WriteString(" | ")
			}
			fmt.Fprintf(&b, "line %d: %s", rej.Line, rej.Err)
		}
	}
	return b.String()
}
