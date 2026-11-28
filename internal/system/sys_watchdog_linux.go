package system

import (
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// hardware watchdog -----------------------------------------------------------

var (
	wdMu sync.Mutex
	wd   *watchdogState
)

// watchdogState is the live device plus the loop-liveness gate.
type watchdogState struct {
	f        io.WriteCloser
	done     chan struct{}
	stop     atomic.Bool
	loopOn   atomic.Bool
	lastBeat atomic.Int64 // UnixNano of the last application beat; 0 = never
	maxStale time.Duration
	stale    atomic.Bool
	feeds    atomic.Int64
	misses   atomic.Int64
}

// feedLocked writes one keepalive when the application loop is alive, and marks the
// watchdog stale when it is not. The caller must hold wdMu.
//
// This is the whole point of the gate: a process whose inference thread has
// wedged but whose timer goroutine still runs would otherwise keep the board
// alive forever. Only a beat from the loop under test refreshes the deadline.
func (w *watchdogState) feedLocked(now time.Time) error {
	if w.f == nil {
		return ErrUnsupported
	}
	last := w.lastBeat.Load()
	if last != 0 && now.Sub(time.Unix(0, last)) <= w.maxStale {
		w.stale.Store(false)
		w.feeds.Add(1)
		_, err := w.f.Write([]byte{0})
		return err
	}
	if !w.stale.Swap(true) {
		w.misses.Add(1)
	}
	return nil
}

// Watchdog drives /dev/watchdog when present and is a no-op otherwise.
//
// It is a liveness check on the application loop, not on the timer that feeds
// it. The loop calls Beat after each iteration it completes (or every N
// events); the feeder goroutine feeds the device every interval, but only while
// the last beat is at most maxStale old. Once the loop stops beating, feeding
// stops and the hardware resets the board after the driver's timeout.
//
// Usage on the Pi: Open, Run(interval, maxStale), Beat from the hot loop, and
// Stop(true) on a graceful shutdown. Stop(true) writes the magic 'V' byte so
// the kernel disarms the timer (no reboot). If the process dies without either,
// the hardware resets the board within the driver's timeout.
type Watchdog struct{}

// WatchdogStatus is a point-in-time view of the watchdog for the status report.
type WatchdogStatus struct {
	Open       bool
	LastBeat   time.Time
	BeatAge    time.Duration
	Stale      bool // the loop missed its deadline; feeding has stopped
	FeedsAfter int64
	Misses     int64
}

// Open opens /dev/watchdog if it exists. It returns ErrUnsupported on a
// development PC; callers log it as a degraded mode.
func (Watchdog) Open() error {
	if !Available(FeatureWatchdog) {
		return ErrUnsupported
	}
	f, err := openWatchdog()
	if err != nil {
		return err
	}
	wdMu.Lock()
	wd = &watchdogState{f: f, maxStale: 3 * time.Second}
	wd.lastBeat.Store(time.Now().UnixNano())
	wdMu.Unlock()
	return nil
}

// Beat records that the application loop completed an iteration. One atomic
// store; safe to call from the hot loop.
func (Watchdog) Beat() {
	wdMu.Lock()
	w := wd
	wdMu.Unlock()
	if w == nil || w.stop.Load() {
		return
	}
	w.lastBeat.Store(time.Now().UnixNano())
}

// Kick feeds the watchdog once. It refuses to feed a watchdog whose loop has
// gone stale, so a manual Kick cannot mask a wedged application.
func (Watchdog) Kick() error {
	wdMu.Lock()
	defer wdMu.Unlock()
	if wd == nil || wd.stop.Load() {
		return ErrUnsupported
	}
	return wd.feedLocked(time.Now())
}

// Run starts the feeder. It feeds every interval while the last Beat is at most
// maxStale old, and stops feeding once it is not. maxStale <= 0 uses
// 3*interval.
func (Watchdog) Run(interval, maxStale time.Duration) {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	if maxStale <= 0 {
		maxStale = 3 * interval
	}
	wdMu.Lock()
	w := wd
	if w == nil || w.stop.Load() {
		wdMu.Unlock()
		return
	}
	w.maxStale = maxStale
	if !w.loopOn.CompareAndSwap(false, true) {
		wdMu.Unlock()
		return
	}
	done := make(chan struct{})
	w.done = done
	wdMu.Unlock()

	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			if w.stop.Load() {
				return
			}
			wdMu.Lock()
			if w.stop.Load() || w.f == nil {
				wdMu.Unlock()
				return
			}
			_ = w.feedLocked(time.Now())
			wdMu.Unlock()
		}
	}()
}

// Status reports the watchdog's view of the loop. The second return value is
// false when no watchdog is open.
func (Watchdog) Status() (WatchdogStatus, bool) {
	wdMu.Lock()
	w := wd
	wdMu.Unlock()
	if w == nil {
		return WatchdogStatus{}, false
	}
	now := time.Now()
	last := time.Unix(0, w.lastBeat.Load())
	return WatchdogStatus{
		Open:       true,
		LastBeat:   last,
		BeatAge:    now.Sub(last),
		Stale:      w.stale.Load(),
		FeedsAfter: w.feeds.Load(),
		Misses:     w.misses.Load(),
	}, true
}

// Stop disarms the watchdog. With disarm=true it writes 'V' so a later exit or
// crash does not reset the board; with disarm=false it only stops feeding, and
// the hardware resets the board after the timeout (used to prove the watchdog
// works during commissioning).
func (Watchdog) Stop(disarm bool) error {
	wdMu.Lock()
	w := wd
	wd = nil
	var done chan struct{}
	if w != nil {
		w.stop.Store(true)
		done = w.done
	}
	wdMu.Unlock()
	if w == nil {
		return ErrUnsupported
	}
	if done != nil {
		<-done
	}
	wdMu.Lock()
	f := w.f
	w.f = nil
	wdMu.Unlock()
	if f == nil {
		return nil
	}
	if disarm {
		_, err := f.Write([]byte{'V'})
		cerr := f.Close()
		if err != nil {
			return err
		}
		return cerr
	}
	return f.Close()
}
