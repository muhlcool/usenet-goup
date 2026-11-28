//go:build linux || darwin || freebsd

package sysproc

import "syscall"

// FreeDiskBytes liefert den für den Benutzer verfügbaren Platz im Dateisystem von path (-1 = unbekannt).
func FreeDiskBytes(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1
	}
	return int64(uint64(st.Bavail) * uint64(st.Bsize))
}
