// Package collect records live market data and news to a local dataset for research:
// 1-second order-book/trade snapshots, multi-feed headlines, and historical backfills
// (Coinbase 1-minute candles, GDELT headlines). Rows are JSON lines in daily gzip files;
// each flush appends a new gzip member, which standard gzip readers read as one stream.
package collect

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// dailyStore buffers JSON rows and appends them to <dir>/<YYYY-MM-DD>.jsonl.gz (UTC days).
type dailyStore struct {
	dir   string
	mu    sync.Mutex
	buf   map[string][]byte // day -> pending lines
	total uint64
}

func newDailyStore(dir string) (*dailyStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &dailyStore{dir: dir, buf: map[string][]byte{}}, nil
}

// Add buffers one row stamped at t.
func (s *dailyStore) Add(t time.Time, row any) error {
	b, err := json.Marshal(row)
	if err != nil {
		return err
	}
	day := t.UTC().Format("2006-01-02")
	s.mu.Lock()
	s.buf[day] = append(append(s.buf[day], b...), '\n')
	s.total++
	s.mu.Unlock()
	return nil
}

// Flush appends all buffered rows to their day files as new gzip members.
func (s *dailyStore) Flush() error {
	s.mu.Lock()
	pending := s.buf
	s.buf = map[string][]byte{}
	s.mu.Unlock()
	var firstErr error
	for day, data := range pending {
		if err := appendGzip(filepath.Join(s.dir, day+".jsonl.gz"), data); err != nil && firstErr == nil {
			firstErr = err
			s.mu.Lock() // keep the data for the next flush attempt
			s.buf[day] = append(data, s.buf[day]...)
			s.mu.Unlock()
		}
	}
	return firstErr
}

// Total returns the number of rows accepted since start.
func (s *dailyStore) Total() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

func appendGzip(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	if _, err := zw.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ReadRows decodes every JSON line of a (multi-member) .jsonl.gz file into fn.
// A truncated final member (crash mid-write) ends reading without an error.
func ReadRows(path string, fn func(line []byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if err := fn(sc.Bytes()); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil && err != io.ErrUnexpectedEOF {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// recentDayFiles lists up to n most recent day files in dir.
func recentDayFiles(dir string, n int) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.jsonl.gz"))
	sort.Strings(matches)
	if len(matches) > n {
		matches = matches[len(matches)-n:]
	}
	return matches
}

func dayFileExists(dir string, day time.Time) bool {
	_, err := os.Stat(filepath.Join(dir, day.UTC().Format("2006-01-02")+".jsonl.gz"))
	return err == nil
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == ' ' || r == ':' {
			return '_'
		}
		return r
	}, s)
}
