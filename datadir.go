package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func exeDir() string {
	if exePath, err := os.Executable(); err == nil {
		dir := filepath.Dir(exePath)
		low := strings.ToLower(dir)
		if !strings.Contains(low, "go-build") && !strings.HasPrefix(low, strings.ToLower(os.TempDir())) {
			return dir
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

func isWritableDir(dir string) bool {
	f, err := os.CreateTemp(dir, ".usenet-goup-wtest-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// getDataDir: portabler Modus (Ordner der Programmdatei), falls beschreibbar. Sonst
// (Program Files, /usr/local/bin, .app-Bundle) das Benutzer-Konfigurationsverzeichnis.
func getDataDir() (dataDir string, legacyDir string) {
	// AppImage: Das Programm läuft aus einem schreibgeschützten Mount unter /tmp, exeDir()
	// fiele dann auf das Arbeitsverzeichnis zurück (beim Doppelklick meist ~). Daher immer
	// das Benutzer-Konfigurationsverzeichnis (~/.config/usenet-goup) nutzen.
	if os.Getenv("APPIMAGE") != "" {
		if base, err := os.UserConfigDir(); err == nil {
			dir := filepath.Join(base, "usenet-goup")
			if err := os.MkdirAll(dir, 0o700); err == nil {
				return dir, ""
			}
		}
	}
	exe := exeDir()
	if isWritableDir(exe) {
		return exe, ""
	}
	if base, err := os.UserConfigDir(); err == nil {
		dir := filepath.Join(base, "usenet-goup")
		if err := os.MkdirAll(dir, 0o700); err == nil {
			return dir, exe
		}
	}
	return exe, ""
}

// requiredWorkSpace schätzt den Platzbedarf im Arbeitsordner: Archiv (Store-Modus) + PAR2 + Reserve.
func requiredWorkSpace(srcBytes int64, par2Pct int) int64 {
	need := srcBytes + srcBytes*int64(par2Pct)/100
	return need + need/50 + 64*1024*1024
}

// directWorkSpace: Platzbedarf bei "Nur hochladen" (nur die PAR2-Dateien, keine Kopie).
func directWorkSpace(srcBytes int64, par2Pct int) int64 {
	need := srcBytes * int64(par2Pct) / 100
	return need + need/50 + 64*1024*1024
}

func formatGB(b int64) string { return fmt.Sprintf("%.1f GB", float64(b)/(1<<30)) }
