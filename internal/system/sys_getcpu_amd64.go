//go:build linux && amd64

package system

// sysGetCPU is the getcpu(2) syscall number. Go's syscall package does not
// export SYS_GETCPU for amd64 (it is only used internally), so the number is
// spelled out here and checked against the arch's syscall table by the test.
const sysGetCPU = 309
