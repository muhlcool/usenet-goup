//go:build !windows

package sysproc

import (
	"path/filepath"
	"strings"
)

func NormalizePath(p string) string {
	cleaned := strings.TrimSpace(p)
	if cleaned == "" {
		return ""
	}
	cleaned = strings.Trim(cleaned, `"'`)
	cleaned = filepath.Clean(cleaned)
	if absPath, err := filepath.Abs(cleaned); err == nil {
		return absPath
	}
	return cleaned
}

func DisplayPath(p string) string {
	return p
}
