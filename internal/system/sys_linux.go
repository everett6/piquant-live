// Package system isolates everything that depends on the Linux kernel or on the
// Raspberry Pi hardware. The same call sites run on the x86_64 development PC
// and on the Pi 5; only the implementation behind them changes.
package system

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// CPUSet is a bitmask of logical CPUs, as used by sched_setaffinity(2).
type CPUSet struct{ bits [16]uint64 } // 1024 bits on every Linux arch

// Add sets cpu in the mask.
func (s *CPUSet) Add(cpu int) {
	if cpu >= 0 && cpu < 1024 {
		s.bits[cpu/64] |= 1 << (uint(cpu) % 64)
	}
}

// Has reports whether cpu is in the mask.
func (s *CPUSet) Has(cpu int) bool {
	return cpu >= 0 && cpu < 1024 && s.bits[cpu/64]&(1<<(uint(cpu)%64)) != 0
}

// Single returns a mask holding just cpu.
func Single(cpu int) CPUSet {
	var s CPUSet
	s.Add(cpu)
	return s
}

// ErrUnsupported is returned when a hardware feature is not available on this
// host (for example, the hardware watchdog on a development PC). Callers treat
// it as a degraded but valid mode.
var ErrUnsupported = errors.New("system: feature not supported on this host")

// CPUS returns the number of online logical CPUs.
func CPUS() int { return runtime.NumCPU() }

// Available reports whether a kernel/hardware feature exists on this host.
type Feature uint8

// Optional kernel and hardware features.
const (
	FeatureAffinity  Feature = iota // sched_setaffinity
	FeatureRealTime                 // SCHED_FIFO through sched_setscheduler
	FeatureWatchdog                 // /dev/watchdog
	FeatureHugePages                // transparent huge pages sysfs
	FeatureIsolation                // isolcpus/nohz_full boot parameters
)

func (f Feature) String() string {
	switch f {
	case FeatureAffinity:
		return "cpu-affinity"
	case FeatureRealTime:
		return "realtime-sched"
	case FeatureWatchdog:
		return "watchdog"
	case FeatureHugePages:
		return "hugepages"
	case FeatureIsolation:
		return "cpu-isolation"
	default:
		return "unknown"
	}
}

// Available reports whether f exists on this host.
func Available(f Feature) bool {
	switch f {
	case FeatureAffinity:
		return pinningSupported
	case FeatureRealTime:
		// Checking for the scheduler would require raising privileges; report
		// it as available and let SetRealtime surface EPERM.
		return runtime.GOOS == "linux"
	case FeatureWatchdog:
		_, err := os.Stat("/dev/watchdog")
		return err == nil
	case FeatureHugePages:
		_, err := os.Stat("/sys/kernel/mm/transparent_hugepage/enabled")
		return err == nil
	case FeatureIsolation:
		return isolationSupported()
	default:
		return false
	}
}

func isolationSupported() bool {
	b, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return false
	}
	return strings.Contains(string(b), "isolcpus=")
}

// PinCPUs restricts the calling OS thread to the CPUs in set. The thread must be
// locked first (LockThread) or the mask applies to whichever thread happens to
// run this goroutine.
func PinCPUs(set CPUSet) error {
	if !pinningSupported {
		return ErrUnsupported
	}
	if runtime.GOOS != "linux" {
		return ErrUnsupported
	}
	return setAffinity(&set.bits[0])
}

// PinCPU is shorthand for PinCPUs(Single(cpu)).
func PinCPU(cpu int) error { return PinCPUs(Single(cpu)) }

// LockThread locks the calling goroutine to the current OS thread.
func LockThread() { runtime.LockOSThread() }

// UnlockThread unlocks the calling goroutine from its OS thread.
func UnlockThread() { runtime.UnlockOSThread() }

// CurrentCPU reports the CPU the calling thread is running on now. It reads
// sched_getcpu(3) through the vDSO-less getcpu syscall.
func CurrentCPU() (int, error) {
	if runtime.GOOS != "linux" {
		return -1, ErrUnsupported
	}
	var cpu uint32
	// getcpu(2) has the same syscall number (309) on amd64, arm64 and arm.
	const sysGetCPU = 309
	_, _, errno := syscall.RawSyscall(sysGetCPU,
		uintptr(unsafe.Pointer(&cpu)), 0, 0)
	if errno != 0 {
		return -1, errno
	}
	return int(cpu), nil
}

// SetRealtime requests SCHED_FIFO at priority for the calling thread. It needs
// CAP_SYS_NICE (or a realtime-secured service) and is optional: on a stock Pi
// without realtime grants it returns the kernel's EPERM and the caller keeps
// running under CFS, where the CPU isolation and a pinned thread still help.
func SetRealtime(priority int) error {
	if runtime.GOOS != "linux" {
		return ErrUnsupported
	}
	const schedFIFO = 1
	param := struct {
		prio int32
		_    [4]byte
	}{prio: int32(priority)}
	_, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_SETSCHEDULER, 0,
		schedFIFO, uintptr(unsafe.Pointer(&param)))
	if errno != 0 {
		return fmt.Errorf("sched_setscheduler(SCHED_FIFO, %d): %w", priority, errno)
	}
	return nil
}

// RuntimeInfo is a snapshot of the process scheduling state used for the
// startup report and for log correlation.
type RuntimeInfo struct {
	GOOS, GOARCH string
	GOVersion    string
	GOMAXPROCS   int
	CPUs         int
	Affinity     string
	PID          int
	Isolation    bool
	HugePages    string // "always", "madvise", "never", or ""
	Watchdog     bool
}

// Runtime reports the current process state.
func Runtime() RuntimeInfo {
	info := RuntimeInfo{
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		GOVersion:  runtime.Version(),
		GOMAXPROCS: runtime.GOMAXPROCS(0),
		CPUs:       CPUS(),
		PID:        os.Getpid(),
		Isolation:  Available(FeatureIsolation),
		Watchdog:   Available(FeatureWatchdog),
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", os.Getpid())); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "Cpus_allowed_list:") {
				info.Affinity = strings.TrimSpace(strings.TrimPrefix(line, "Cpus_allowed_list:"))
			}
		}
	}
	if b, err := os.ReadFile("/sys/kernel/mm/transparent_hugepage/enabled"); err == nil {
		s := string(b)
		if i := strings.IndexByte(s, '['); i >= 0 {
			if j := strings.IndexByte(s[i:], ']'); j > 0 {
				info.HugePages = s[i+1 : i+j]
			}
		}
	}
	return info
}

// Now returns the monotonic clock in nanoseconds. All internal timestamps are
// monotonic; wall-clock times are only attached at the storage boundary.
func Now() int64 { return time.Since(processStart).Nanoseconds() }

var processStart = time.Now()

// WallClock returns the wall clock in Unix nanoseconds.
func WallClock() int64 { return time.Now().UnixNano() }

// --- hardware watchdog -------------------------------------------------------

var (
	wdMu sync.Mutex
	wd   *watchdogState
)

type watchdogState struct {
	f        *os.File
	stop     atomic.Bool
	loopOn   atomic.Bool
	timeouts int64
}

// Watchdog drives /dev/watchdog when present and is a no-op otherwise.
//
// Usage on the Pi: Open, then Kick every few hundred milliseconds from the
// housekeeping loop, or let the built-in feeder run, and close with Stop(false)
// for a graceful shutdown. Stop(true) writes the magic 'V' byte so the kernel
// disarms the timer (no reboot). If the process dies without either, the
// hardware resets the board within the driver's timeout.
type Watchdog struct{}

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
	wd = &watchdogState{f: f}
	wdMu.Unlock()
	return nil
}

// Kick feeds the watchdog once.
func (Watchdog) Kick() error {
	wdMu.Lock()
	defer wdMu.Unlock()
	if wd == nil || wd.stop.Load() {
		return ErrUnsupported
	}
	_, err := wd.f.Write([]byte{0})
	return err
}

// Run feeds the watchdog every interval until Stop is called.
func (Watchdog) Run(interval time.Duration) {
	wdMu.Lock()
	w := wd
	wdMu.Unlock()
	if w == nil {
		return
	}
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	if !w.loopOn.CompareAndSwap(false, true) {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			if w.stop.Load() {
				return
			}
			wdMu.Lock()
			_, _ = w.f.Write([]byte{0})
			wdMu.Unlock()
		}
	}()
}

// Stop disarms the watchdog. With disarm=true it writes 'V' so a later exit or
// crash does not reset the board; with disarm=false it only stops feeding, and
// the hardware resets the board after the timeout (used to prove the watchdog
// works during commissioning).
func (Watchdog) Stop(disarm bool) error {
	wdMu.Lock()
	w := wd
	wd = nil
	wdMu.Unlock()
	if w == nil {
		return ErrUnsupported
	}
	w.stop.Store(true)
	if disarm {
		_, err := w.f.Write([]byte{'V'})
		cerr := w.f.Close()
		if err != nil {
			return err
		}
		return cerr
	}
	return w.f.Close()
}

// ParseCPUSet parses a Linux Cpus_allowed_list such as "2-3" or "0,2,4-6".
func ParseCPUSet(s string) (CPUSet, error) {
	var set CPUSet
	s = strings.TrimSpace(s)
	if s == "" {
		return set, nil
	}
	for _, part := range strings.Split(s, ",") {
		lo, hi, ranged := strings.Cut(part, "-")
		a, err := strconv.Atoi(strings.TrimSpace(lo))
		if err != nil {
			return set, fmt.Errorf("system: bad cpu list %q: %w", s, err)
		}
		b := a
		if ranged {
			if b, err = strconv.Atoi(strings.TrimSpace(hi)); err != nil {
				return set, fmt.Errorf("system: bad cpu list %q: %w", s, err)
			}
		}
		for c := a; c <= b; c++ {
			set.Add(c)
		}
	}
	return set, nil
}
