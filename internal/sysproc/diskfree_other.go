//go:build !linux && !darwin && !freebsd && !windows

package sysproc

// FreeDiskBytes: auf dieser Plattform nicht ermittelbar.
func FreeDiskBytes(path string) int64 { return -1 }
