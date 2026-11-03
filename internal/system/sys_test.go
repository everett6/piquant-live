package system

import (
	"errors"
	"runtime"
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
	(Watchdog{}).Run(time.Millisecond) // must not panic or block
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
