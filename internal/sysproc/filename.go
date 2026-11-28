package sysproc

import (
	"strings"
	"unicode/utf8"
)

// maxNameBytes: Linux/macOS erlauben 255 Bytes pro Dateiname, Windows 255 UTF-16-Einheiten.
// 240 Bytes UTF-8 passt auf allen Plattformen, auch mit Umlauten/CJK.
const maxNameBytes = 240

var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// SafeOutFileName baut einen auf allen Plattformen gültigen Dateinamen aus name + suffix.
// Es werden immer die strengeren Windows-Regeln angewendet, damit NZBs zwischen Systemen kopierbar bleiben.
func SafeOutFileName(name string, suffix string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	sanitized := strings.TrimRight(b.String(), ". ")
	if sanitized == "" {
		sanitized = "release"
	}
	stem := sanitized
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	if windowsReserved[strings.ToUpper(stem)] {
		sanitized = "_" + sanitized
	}

	maxBase := maxNameBytes - len(suffix)
	if len(sanitized) > maxBase {
		cut := maxBase
		for cut > 0 && !utf8.RuneStart(sanitized[cut]) {
			cut--
		}
		sanitized = strings.TrimRight(sanitized[:cut], ". ")
	}
	return sanitized + suffix
}
