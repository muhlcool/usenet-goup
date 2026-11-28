//go:build !linux

package sysproc

// IsRAMBacked: außerhalb von Linux liegt das System-Temp praktisch nie im RAM.
func IsRAMBacked(path string) bool { return false }
