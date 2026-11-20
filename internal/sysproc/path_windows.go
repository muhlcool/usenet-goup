//go:build windows

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
	if len(cleaned) >= 2 && cleaned[0] == '/' && cleaned[1] == '/' {
		cleaned = `\\` + strings.ReplaceAll(cleaned[2:], "/", `\`)
	} else {
		cleaned = filepath.FromSlash(cleaned)
	}
	cleaned = filepath.Clean(cleaned)

	if strings.HasPrefix(cleaned, `\\?\`) || strings.HasPrefix(cleaned, `\\.\`) {
		return cleaned
	}

	absPath, err := filepath.Abs(cleaned)
	if err == nil {
		cleaned = absPath
	}

	if len(cleaned) >= 240 {
		if strings.HasPrefix(cleaned, `\\`) {
			return `\\?\UNC\` + strings.TrimPrefix(cleaned, `\\`)
		}
		return `\\?\` + cleaned
	}
	return cleaned
}

func DisplayPath(p string) string {
	if strings.HasPrefix(p, `\\?\UNC\`) {
		return `\\` + strings.TrimPrefix(p, `\\?\UNC\`)
	}
	return strings.TrimPrefix(p, `\\?\`)
}
