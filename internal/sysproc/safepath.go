package sysproc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// IsDeviceNamespace erkennt Windows-Gerätepfade wie \\.\PhysicalDrive0, \\?\GLOBALROOT\...
// oder ein Rohvolume \\?\Volume{...}. Darüber ließe sich eine ganze Festplatte lesen.
func IsDeviceNamespace(p string) bool {
	s := strings.ReplaceAll(p, "/", `\`)
	up := strings.ToUpper(s)
	switch {
	case strings.HasPrefix(s, `\\.\`):
		return true
	case strings.HasPrefix(up, `\\?\GLOBALROOT`):
		return true
	case strings.HasPrefix(up, `\\?\VOLUME{`):
		rest := strings.TrimRight(s[len(`\\?\`):], `\`)
		return !strings.Contains(rest, `\`)
	}
	return false
}

// pseudoRoots: Systemordner unter Linux/macOS, die Geräte oder Kernel-Daten statt Dateien enthalten.
var pseudoRoots = []string{"/dev", "/proc", "/sys"}

// IsSystemPseudoPath erkennt Pfade in /dev, /proc oder /sys (nicht unter Windows).
func IsSystemPseudoPath(p string) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	c := filepath.Clean(p)
	for _, r := range pseudoRoots {
		if c == r || strings.HasPrefix(c, r+"/") {
			return true
		}
	}
	return false
}

// ErrNotPlainPath: Pfad zeigt auf ein Gerät, eine Pipe oder einen System-Pseudoordner.
var ErrNotPlainPath = errors.New("kein normaler Ordner und keine normale Datei (Gerät, Pipe oder Systemordner)")

// CheckPlainPath erlaubt nur normale Dateien und Ordner als Quelle oder Ziel.
// Geräte (z. B. /dev/sda, \\.\PhysicalDrive0), Pipes und Systemordner werden abgelehnt.
func CheckPlainPath(p string) (os.FileInfo, error) {
	if IsDeviceNamespace(p) || IsSystemPseudoPath(p) {
		return nil, ErrNotPlainPath
	}
	info, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return nil, ErrNotPlainPath
	}
	return info, nil
}

// ReadSmallFile liest eine normale Datei von höchstens maxBytes (z. B. NFO). Verhindert, dass
// eine riesige Datei oder ein Gerät wie /dev/zero den Arbeitsspeicher füllt.
func ReadSmallFile(p string, maxBytes int64) ([]byte, error) {
	if IsDeviceNamespace(p) || IsSystemPseudoPath(p) {
		return nil, ErrNotPlainPath
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNotPlainPath
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("Datei zu groß (%d Bytes, erlaubt %d)", info.Size(), maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("Datei zu groß (erlaubt %d Bytes)", maxBytes)
	}
	return data, nil
}
