package system

import (
	"runtime"
	"syscall"
	"unsafe"
)

var archName string = runtime.GOARCH

// setAffinityErr restricts the calling thread to the CPUs in mask (the first
// 64 of them), with the rest of the 1024-bit cpu_set_t explicitly cleared.
func setAffinityErr(mask *uint64) error {
	var m [16]uint64
	m[0] = *mask
	_, _, errno := syscall.RawSyscall6(syscall.SYS_SCHED_SETAFFINITY, 0,
		uintptr(unsafe.Sizeof(m)), uintptr(unsafe.Pointer(&m[0])), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
