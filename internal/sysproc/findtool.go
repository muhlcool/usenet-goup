package sysproc

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// FindTool sucht ein externes Programm: PATH, Programmordner, Arbeitsverzeichnis, dann feste Pfade.
// Ersetzt die drei früheren Kopien in archiver, par2 und media.
func FindTool(names []string, extraPaths []string) string {
	exeDir := ""
	if exePath, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exePath)
	}
	wd, _ := os.Getwd()

	for _, n := range names {
		if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(n), ".exe") {
			n += ".exe"
		}
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
		for _, dir := range []string{exeDir, wd} {
			if dir == "" {
				continue
			}
			local := filepath.Join(dir, n)
			if st, err := os.Stat(local); err == nil && !st.IsDir() {
				return local
			}
		}
	}
	for _, p := range extraPaths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}
