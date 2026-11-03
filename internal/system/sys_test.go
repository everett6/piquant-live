package system

import (
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestCPUSetParse(t *testing.T) {
	cases := map[string][]int{
		"":        nil,
		"0":       {0},
		"2-3":     {2, 3},
		"0,2,4-6": {0, 2, 4, 5, 6},
		" 1 - 2 ": {1, 2},
	}
	for in, want := range cases {
		set, err := ParseCPUSet(in)
		if err != nil {
			t.Fatalf("ParseCPUSet(%q): %v", in, err)
		}
		for c := 0; c < CPUS(); c++ {
			has := false
			for _, w := range want {
				if w == c {
					has = true
				}
			}
			if set.Has(c) != has {
				t.Fatalf("ParseCPUSet(%q).Has(%d) = %v", in, c, set.Has(c))
			}
		}
	}
	if _, err := ParseCPUSet("x-2"); err == nil {
		t.Fatal("bad list parsed without error")
	}
}

// TestPinAndAffinity pins the current thread to one CPU and checks both the
// reported current CPU and the kernel's view of the mask.
func TestPinAndAffinity(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	if !Available(FeatureAffinity) {
		t.Skip("affinity unsupported")
	}
	LockThread()
	defer UnlockThread()
	if err := PinCPU(1 % CPUS()); err != nil {
		t.Skipf("sched_setaffinity denied in this sandbox: %v", err)
	}
	want := 1 % CPUS()
	if cpu, err := CurrentCPU(); err != nil {
		t.Fatalf("CurrentCPU: %v", err)
	} else if cpu != want {
		t.Fatalf("current CPU = %d, want %d", cpu, want)
	}
	info := Runtime()
	if info.Affinity != itoa(want) {
		t.Fatalf("affinity = %q, want %q", info.Affinity, itoa(want))
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [4]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

func TestNowMonotonic(t *testing.T) {
	a := Now()
	time.Sleep(time.Millisecond)
	if b := Now(); b <= a {
		t.Fatalf("clock did not advance: %d -> %d", a, b)
	}
	if WallClock() < 1<<40 {
		t.Fatal("wall clock looks wrong")
	}
}

func TestWatchdogUnsupported(t *testing.T) {
	if Available(FeatureWatchdog) {
		t.Skip("hardware watchdog present")
	}
	if err := (Watchdog{}).Open(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Open err = %v, want ErrUnsupported", err)
	}
	if err := (Watchdog{}).Kick(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Kick err = %v, want ErrUnsupported", err)
	}
	if err := (Watchdog{}).Stop(true); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Stop err = %v, want ErrUnsupported", err)
	}
	(Watchdog{}).Beat()                   // must not panic with no device
	(Watchdog{}).Run(time.Millisecond, 0) // must not panic or block
	if _, ok := (Watchdog{}).Status(); ok {
		t.Fatal("Status reported an open watchdog with no device")
	}
}

// fakeWatchdog installs a device backed by memory so the liveness gate can be
// tested without /dev/watchdog. It restores whatever was there before.
func fakeWatchdog(t *testing.T, maxStale time.Duration) *countWriter {
	t.Helper()
	wdMu.Lock()
	previous := wd
	w := &watchdogState{f: &countWriter{}, maxStale: maxStale}
	w.lastBeat.Store(time.Now().UnixNano())
	wd = w
	wdMu.Unlock()
	t.Cleanup(func() {
		wdMu.Lock()
		wd = previous
		wdMu.Unlock()
	})
	return w.f.(*countWriter)
}

type countWriter struct {
	n   atomic.Int64
	err error
}

func (c *countWriter) Write(p []byte) (int, error) {
	c.n.Add(int64(len(p)))
	return len(p), c.err
}

func (c *countWriter) Close() error { return nil }

func TestWatchdogFeedsWhileLoopBeats(t *testing.T) {
	dev := fakeWatchdog(t, 50*time.Millisecond)
	for i := 0; i < 5; i++ {
		(Watchdog{}).Beat()
		if err := (Watchdog{}).Kick(); err != nil {
			t.Fatalf("Kick: %v", err)
		}
	}
	if got := dev.n.Load(); got != 5 {
		t.Fatalf("device writes = %d, want 5", got)
	}
	st, ok := (Watchdog{}).Status()
	if !ok || st.Stale || st.Misses != 0 || st.FeedsAfter != 5 {
		t.Fatalf("status = %+v ok=%v", st, ok)
	}
}

// The point of the gate: a running feeder must not keep a wedged loop alive.
func TestWatchdogStopsFeedingWhenLoopWedges(t *testing.T) {
	dev := fakeWatchdog(t, 20*time.Millisecond)
	(Watchdog{}).Beat()
	if err := (Watchdog{}).Kick(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond) // loop is now stale: no beat since
	before := dev.n.Load()
	for i := 0; i < 3; i++ {
		if err := (Watchdog{}).Kick(); err != nil {
			t.Fatalf("Kick: %v", err)
		}
	}
	if after := dev.n.Load(); after != before {
		t.Fatalf("fed a stale loop: %d -> %d writes", before, after)
	}
	st, _ := (Watchdog{}).Status()
	if !st.Stale || st.Misses == 0 {
		t.Fatalf("status = %+v, want stale with a miss", st)
	}
	// A fresh beat revives feeding.
	(Watchdog{}).Beat()
	if err := (Watchdog{}).Kick(); err != nil {
		t.Fatal(err)
	}
	if st, _ := (Watchdog{}).Status(); st.Stale {
		t.Fatalf("status still stale after a beat: %+v", st)
	}
}

func TestWatchdogRunStopsFeedingStaleLoop(t *testing.T) {
	dev := fakeWatchdog(t, 10*time.Millisecond)
	(Watchdog{}).Run(5*time.Millisecond, 15*time.Millisecond)
	deadline := time.Now().Add(time.Second)
	for {
		(Watchdog{}).Beat()
		if dev.n.Load() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("feeder never wrote with a live loop")
		}
		time.Sleep(time.Millisecond)
	}
	// Stop beating and let the deadline pass; feeding must cease.
	time.Sleep(40 * time.Millisecond)
	settled := dev.n.Load()
	time.Sleep(60 * time.Millisecond)
	if got := dev.n.Load(); got != settled {
		t.Fatalf("feeder kept writing after the loop went stale: %d -> %d", settled, got)
	}
	if st, _ := (Watchdog{}).Status(); !st.Stale {
		t.Fatalf("status = %+v, want stale", st)
	}
	(Watchdog{}).Stop(true)
}

func TestRuntimeInfo(t *testing.T) {
	info := Runtime()
	if info.GOOS != runtime.GOOS || info.CPUs < 1 || info.PID <= 0 || info.GOMAXPROCS < 1 {
		t.Fatalf("bad runtime info: %+v", info)
	}
	if MachineInfo() == "" {
		t.Fatal("empty machine info")
	}
}

func TestSetRealtimeDoesNotPanic(t *testing.T) {
	LockThread()
	defer UnlockThread()
	err := SetRealtime(10)
	if err != nil && runtime.GOOS == "linux" {
		t.Logf("SCHED_FIFO unavailable (expected without CAP_SYS_NICE): %v", err)
	}
}
