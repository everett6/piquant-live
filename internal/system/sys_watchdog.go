//go:build linux

package system

import "os"

// openWatchdog opens the hardware watchdog character device for writing.
func openWatchdog() (*os.File, error) {
	return os.OpenFile("/dev/watchdog", os.O_WRONLY, 0)
}
