//go:build linux && arm64

package system

// sysGetCPU is the getcpu(2) syscall number on arm64. It differs from amd64
// (309) — the previous unconditional 309 silently called sched_setattr(2) here,
// which is why this constant is per-arch. Value from syscall/zsysnum_linux_arm64.go.
const sysGetCPU = 168
