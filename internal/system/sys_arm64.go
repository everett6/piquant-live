//go:build linux && arm64

package system

// pinning works on every Linux arch: CPU affinity is a plain syscall.
const pinningSupported = true

// setAffinity restricts the calling thread to the CPUs in mask.
func setAffinity(mask *uint64) error { return setAffinityErr(mask) }

// MachineInfo describes the production board.
func MachineInfo() string { return "raspberry-pi-5 (bcm2712, cortex-a76)" }
