//go:build linux

package sysproc

import "syscall"

const (
	tmpfsMagic = 0x01021994
	ramfsMagic = 0x858458F6
)

// IsRAMBacked meldet, ob path auf einem RAM-Dateisystem (tmpfs/ramfs) liegt, z. B. /tmp
// auf dem Raspberry Pi. Dort kostet jedes Byte im Arbeitsordner Arbeitsspeicher.
func IsRAMBacked(path string) bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false
	}
	t := uint32(st.Type)
	return t == tmpfsMagic || t == ramfsMagic
}
