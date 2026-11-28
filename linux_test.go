//go:build linux

// Nur Linux: AppImage-Datenordner und tmpfs-Erkennung.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"usenet-goup/internal/sysproc"
)

// Als AppImage gestartet: Daten landen in ~/.config/usenet-goup, nie im Arbeitsverzeichnis.
func TestDataDirForAppImage(t *testing.T) {
	cfgHome := t.TempDir()
	workDir := t.TempDir()
	t.Setenv("APPIMAGE", "/home/nutzer/Downloads/Usenet_GoUp-x86_64.AppImage")
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Chdir(workDir)

	got, legacy := getDataDir()
	want := filepath.Join(cfgHome, "usenet-goup")
	if got != want {
		t.Fatalf("AppImage-Datenordner: erwartet %q, got %q", want, got)
	}
	if legacy != "" {
		t.Fatalf("AppImage darf keinen Legacy-Ordner melden, got %q", legacy)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Fatalf("Datenordner wurde nicht angelegt: %v", err)
	}
	if entries, _ := os.ReadDir(workDir); len(entries) != 0 {
		t.Fatalf("Arbeitsverzeichnis wurde beschrieben: %v", entries)
	}
}

// Ohne AppImage bleibt der portable Modus (Ordner der Programmdatei) unverändert.
func TestDataDirWithoutAppImageUnchanged(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("APPIMAGE", "")
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	if got, _ := getDataDir(); got == filepath.Join(cfgHome, "usenet-goup") {
		t.Fatalf("ohne AppImage darf nicht auf den Konfigurationsordner umgestellt werden, got %q", got)
	}
}

// ramTempDir liefert einen frischen Ordner auf einem tmpfs (/dev/shm), sonst Skip.
func ramTempDir(t *testing.T) string {
	t.Helper()
	if !sysproc.IsRAMBacked("/dev/shm") {
		t.Skip("/dev/shm ist hier kein tmpfs")
	}
	dir, err := os.MkdirTemp("/dev/shm", "goup-test-*")
	if err != nil {
		t.Skip("/dev/shm nicht beschreibbar")
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func usenetDirsIn(t *testing.T, root string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(root, "usenet-goup-*"))
	return m
}

func TestIsRAMBacked(t *testing.T) {
	ram := ramTempDir(t)
	if !sysproc.IsRAMBacked(ram) {
		t.Fatal("tmpfs-Ordner nicht als RAM erkannt")
	}
	disk := t.TempDir()
	if sysproc.IsRAMBacked(disk) && !strings.HasPrefix(disk, "/dev/shm") {
		t.Skip("Test-Temp liegt selbst im RAM")
	}
	if sysproc.IsRAMBacked(disk) {
		t.Fatal("Platten-Ordner fälschlich als RAM erkannt")
	}
}

// Raspberry-Pi-Fall: /tmp ist tmpfs. Das Paket muss im Datenordner (work/) landen, nicht im RAM.
func TestJobDirAvoidsRAMTemp(t *testing.T) {
	e := newTestEnv(t)
	if sysproc.IsRAMBacked(e.a.appDir) {
		t.Skip("Datenordner liegt im RAM")
	}
	ram := ramTempDir(t)
	t.Setenv("TMPDIR", ram)
	_ = os.MkdirAll(filepath.Join(e.dir, "out"), 0o700)

	rec, out := e.post("/api/run", e.runReq(true, ""))
	if rec.Code != 200 || out["status"] != "dryrun_ok" {
		t.Fatalf("Dry-Run fehlgeschlagen: %d %v", rec.Code, out)
	}
	if d := usenetDirsIn(t, ram); len(d) != 0 {
		t.Fatalf("Arbeitsordner im RAM angelegt: %v", d)
	}
	if d := usenetDirsIn(t, filepath.Join(e.a.appDir, "work")); len(d) != 1 {
		t.Fatalf("erwartet genau einen Arbeitsordner in work/, got %v", d)
	}
}

// Normalfall: Temp auf der Platte wird weiter genutzt, work/ bleibt unberührt.
func TestJobDirUsesDiskTemp(t *testing.T) {
	e := newTestEnv(t)
	tmp := t.TempDir()
	if sysproc.IsRAMBacked(tmp) {
		t.Skip("Test-Temp liegt im RAM")
	}
	t.Setenv("TMPDIR", tmp)
	_ = os.MkdirAll(filepath.Join(e.dir, "out"), 0o700)

	if rec, out := e.post("/api/run", e.runReq(true, "")); rec.Code != 200 {
		t.Fatalf("Dry-Run fehlgeschlagen: %d %v", rec.Code, out)
	}
	if d := usenetDirsIn(t, tmp); len(d) != 1 {
		t.Fatalf("erwartet Arbeitsordner im Temp, got %v", d)
	}
	if d := usenetDirsIn(t, filepath.Join(e.a.appDir, "work")); len(d) != 0 {
		t.Fatalf("work/ unnötig benutzt: %v", d)
	}
}

// Cache leeren räumt auch verwaiste Ordner in work/ auf.
func TestCacheClearCleansWorkDir(t *testing.T) {
	e := newTestEnv(t)
	t.Setenv("TMPDIR", t.TempDir())
	orphan := filepath.Join(e.a.appDir, "work", "usenet-goup-999")
	if err := os.MkdirAll(filepath.Join(orphan, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if rec, out := e.post("/api/cache/clear", map[string]any{}); rec.Code != 200 {
		t.Fatalf("Cache leeren fehlgeschlagen: %d %v", rec.Code, out)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("verwaister Ordner in work/ wurde nicht entfernt")
	}
}
