// Package replay streams a dataset through the engine deterministically.
//
// A Replay owns one file, one normaliser per venue, and the order books of every
// instrument it touches. It is driven one event at a time: the caller decides
// whether to feed the event engine, the models, or only the book, which is what
// keeps evaluation independent of data ingestion.
//
// Determinism is the contract. The same file replayed twice produces the same
// events in the same order, with the same rejections and the same sequence-gap
// counts, on any architecture: the replay never reads the wall clock, never
// randomises, and never depends on goroutine scheduling.
//
// Ordering. Records are delivered in file order. That is the order the capture
// writer observed, and it is the only order that is reproducible from the file
// alone. A capture whose receive timestamps go backwards is reported (both by
// the dataset reader and by the feature engine) rather than silently re-sorted:
// sorting by venue time would rewrite history, and sorting by receive time would
// assume a clock the file cannot vouch for.
package replay

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"piquant-live/internal/config"
	"piquant-live/internal/dataset"
	"piquant-live/internal/event"
	"piquant-live/internal/normalize"
	"piquant-live/internal/orderbook"
)

// ErrFinished is returned by Next once the dataset is exhausted.
var ErrFinished = errors.New("replay: dataset exhausted")

// BookStatus is the reconstruction state of one book, for the run report.
type BookStatus struct {
	Venue      string
	Instrument string
	Symbol     uint16
	Status     orderbook.Status
	Ready      bool
	Updates    uint64
	Snapshots  uint64
	Gaps       uint64
	Rejected   uint64
	Trades     uint64
}

// Stats is the replay's running quality and progress report.
type Stats struct {
	Path           string
	Checksum       string
	Records        int64 // accepted by the dataset reader
	Delivered      int64 // handed to the caller as an envelope
	Duplicates     int64
	Rejected       int64
	Rejects        map[normalize.RejectKind]int64
	RejectedByBook int64
	Books          []BookStatus

	// Reordered counts envelopes delivered with a receive timestamp older than
	// the previous envelope's.
	Reordered int64
	FirstRecv int64
	LastRecv  int64

	Reader dataset.Quality
}

// Options configures a replay.
type Options struct {
	// Config supplies symbol ids and per-venue instrument names.
	Config *config.Config
	// Speed is the replay pacing: 0 or negative means "as fast as possible",
	// 1 plays back in real time relative to the capture's receive timestamps,
	// and 10 plays back ten times faster.
	Speed float64
	// MaxBooks bounds how many instruments may be reconstructed at once. Each
	// book is a fixed 1 KiB, so the default of 32 is a 32 KiB ceiling.
	MaxBooks int
}

// Replay is one dataset replay. It is not safe for concurrent use.
type Replay struct {
	opts     Options
	reader   *dataset.Reader
	norms    map[string]*normalize.Normalizer
	books    map[bookKey]*orderbook.Book
	venues   map[string]*venueMeta
	stats    Stats
	prevRecv int64

	started  bool
	wallBase time.Time
	dataBase int64

	finished bool
}

type bookKey struct{ venue, instrument string }

type venueMeta struct {
	name  string
	id    uint16
	books map[bookKey]uint16 // instruments seen
}

// Open prepares a replay of path. It computes the dataset checksum up front so
// the report identifies exactly the bytes being read.
func Open(path string, opts Options) (*Replay, error) {
	if opts.Config == nil {
		return nil, errors.New("replay: Options.Config is required")
	}
	if opts.MaxBooks <= 0 {
		opts.MaxBooks = 32
	}
	sum, err := dataset.Checksum(path)
	if err != nil {
		return nil, err
	}
	r, err := dataset.Open(path)
	if err != nil {
		return nil, err
	}
	return &Replay{
		opts:   opts,
		reader: r,
		norms:  make(map[string]*normalize.Normalizer),
		books:  make(map[bookKey]*orderbook.Book),
		venues: make(map[string]*venueMeta),
		stats: Stats{
			Path:     path,
			Checksum: sum,
			Rejects:  make(map[normalize.RejectKind]int64),
		},
	}, nil
}

// Stats returns the running report.
func (r *Replay) Stats() Stats {
	s := r.stats
	s.Reader = r.reader.Quality()
	s.Rejects = make(map[normalize.RejectKind]int64, len(r.stats.Rejects))
	for k, v := range r.stats.Rejects {
		s.Rejects[k] = v
	}
	keyList := make([]bookKey, 0, len(r.books))
	for k := range r.books {
		keyList = append(keyList, k)
	}
	sort.Slice(keyList, func(i, j int) bool {
		if keyList[i].venue != keyList[j].venue {
			return keyList[i].venue < keyList[j].venue
		}
		return keyList[i].instrument < keyList[j].instrument
	})
	s.Books = s.Books[:0]
	for _, k := range keyList {
		b := r.books[k]
		s.Books = append(s.Books, BookStatus{
			Venue:      k.venue,
			Instrument: k.instrument,
			Symbol:     b.Symbol,
			Status:     b.Status,
			Ready:      b.Ready,
			Updates:    b.Updates,
			Snapshots:  b.Snapshots,
			Gaps:       b.Gaps,
			Trades:     b.Trades,
		})
	}
	return s
}

// Book returns the live book for one instrument, creating it on first use.
func (r *Replay) Book(venue, instrument string) (*orderbook.Book, error) {
	key := bookKey{venue: strings.ToLower(venue), instrument: normalize.InstrumentKey(venue, instrument)}
	if b, ok := r.books[key]; ok {
		return b, nil
	}
	if len(r.books) >= r.opts.MaxBooks {
		return nil, fmt.Errorf("replay: more than %d instruments in the dataset; raise Options.MaxBooks", r.opts.MaxBooks)
	}
	norm, err := r.normalizer(key.venue)
	if err != nil {
		return nil, err
	}
	sym, ok := norm.SymbolFor(instrument)
	if !ok {
		return nil, fmt.Errorf("replay: %s:%s is not configured", venue, instrument)
	}
	b := &orderbook.Book{Venue: config.VenueID(venue), Symbol: sym}
	r.books[key] = b
	meta := r.venues[key.venue]
	if meta == nil {
		meta = &venueMeta{name: key.venue, id: config.VenueID(venue), books: make(map[bookKey]uint16)}
		r.venues[key.venue] = meta
	}
	meta.books[key] = sym
	return b, nil
}

func (r *Replay) normalizer(venue string) (*normalize.Normalizer, error) {
	venue = strings.ToLower(venue)
	if n, ok := r.norms[venue]; ok {
		return n, nil
	}
	n, err := normalize.New(venue, r.opts.Config)
	if err != nil {
		return nil, err
	}
	r.norms[venue] = n
	return n, nil
}

// Step is one delivered event together with the book it was applied to.
type Step struct {
	Env  event.Envelope
	Book *orderbook.Book
	// Applied reports whether the book accepted the envelope. A false value
	// with no error means the book refused it on its own audit (a sequence
	// gap, a crossed book); the replay keeps going so the gap is visible in
	// the report rather than aborting the run.
	Applied bool
	// Rejection is the normaliser's failure, if the record was refused.
	Rejection error
}

// Next returns the next accepted event with its book already updated.
//
// Records the normaliser reuses (duplicates) are skipped; records it cannot
// interpret are counted in Stats and skipped. An I/O failure ends the replay
// with an error.
func (r *Replay) Next() (Step, error) {
	for {
		if r.finished {
			return Step{}, ErrFinished
		}
		rec, err := r.reader.Next()
		if err == io.EOF {
			r.finished = true
			return Step{}, ErrFinished
		}
		if err != nil {
			return Step{}, err
		}
		book, err := r.Book(rec.Venue, rec.Instrument)
		if err != nil {
			// An instrument the config does not name, or a venue it does not
			// support, is a data problem: count it and keep going.
			r.stats.Rejected++
			r.stats.Rejects[normalize.KindUnknownSymbol]++
			continue
		}
		norm := r.norms[strings.ToLower(rec.Venue)]
		before := norm.Duplicates()
		env, err := norm.Envelope(rec)
		r.stats.Duplicates += norm.Duplicates() - before
		if err != nil {
			norm.CountReject(err)
			r.stats.Rejected++
			var re *normalize.RejectError
			if errors.As(err, &re) {
				r.stats.Rejects[re.Kind]++
			}
			continue
		}
		if env == nil {
			// Heartbeat or duplicate.
			continue
		}
		if !r.started {
			r.started = true
			r.stats.FirstRecv = env.RecvTime
			r.wallBase = time.Now()
			r.dataBase = env.RecvTime
		}
		if env.RecvTime < r.prevRecv {
			r.stats.Reordered++
		}
		r.prevRecv = env.RecvTime
		r.stats.LastRecv = env.RecvTime
		r.pace(env.RecvTime)
		applied := book.Apply(env)
		if !applied {
			r.stats.RejectedByBook++
		}
		r.stats.Delivered++
		return Step{Env: *env, Book: book, Applied: applied}, nil
	}
}

// pace sleeps to honour Options.Speed. It is the only place the replay touches
// the wall clock, and with Speed <= 0 it does nothing at all.
func (r *Replay) pace(recv int64) {
	if r.opts.Speed <= 0 {
		return
	}
	target := time.Duration(float64(recv-r.dataBase) / r.opts.Speed)
	elapsed := time.Since(r.wallBase)
	if target > elapsed {
		time.Sleep(target - elapsed)
	}
}

// Reset rewinds the replay to the beginning of the dataset: books, normalisers
// and counters are rebuilt, so a second pass produces the same events as the
// first. Nothing about the source file is modified.
func (r *Replay) Reset() error {
	if err := r.reader.Close(); err != nil {
		return err
	}
	fresh, err := Open(r.stats.Path, r.opts)
	if err != nil {
		return err
	}
	*r = *fresh
	return nil
}

// Close releases the dataset file.
func (r *Replay) Close() error {
	if r.reader == nil {
		return nil
	}
	return r.reader.Close()
}

// Summary renders the replay as a short paragraph for a report.
func (s Stats) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "dataset %s (sha256 %s): %d records read, %d events delivered, ",
		s.Path, shortSum(s.Checksum), s.Records, s.Delivered)
	fmt.Fprintf(&b, "%d duplicates, %d rejected by the normaliser (%s), %d refused by a book, %d reordered",
		s.Duplicates, s.Rejected, s.rejectSummary(), s.RejectedByBook, s.Reordered)
	for _, bk := range s.Books {
		fmt.Fprintf(&b, "; %s:%s %s (%d updates, %d snapshots, %d gaps)",
			bk.Venue, bk.Instrument, bk.Status, bk.Updates, bk.Snapshots, bk.Gaps)
	}
	return b.String()
}

func (s Stats) rejectSummary() string {
	if len(s.Rejects) == 0 {
		return "none"
	}
	kinds := make([]string, 0, len(s.Rejects))
	for k, v := range s.Rejects {
		kinds = append(kinds, fmt.Sprintf("%s=%d", k, v))
	}
	sort.Strings(kinds)
	return strings.Join(kinds, ", ")
}

func shortSum(sum string) string {
	if len(sum) <= 16 {
		return sum
	}
	return sum[:16]
}
