//go:build windows

package sysproc

import (
	"syscall"
	"unsafe"
)

var procGetDiskFreeSpaceExW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

// FreeDiskBytes liefert den für den Benutzer verfügbaren Platz auf dem Laufwerk von path (-1 = unbekannt).
func FreeDiskBytes(path string) int64 {
	p, err := syscall.UTF16PtrFromString(DisplayPath(path))
	if err != nil {
		return -1
	}
	var freeAvail, total, totalFree uint64
	r, _, _ := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&freeAvail)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r == 0 {
		return -1
	}
	return int64(freeAvail)
}
