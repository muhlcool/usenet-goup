package sysproc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestIsDeviceNamespace(t *testing.T) {
	cases := map[string]bool{
		`\\.\PhysicalDrive0`:                true,
		`//./PhysicalDrive0`:                true,
		`\\.\C:`:                            true,
		`\\?\GLOBALROOT\Device\Harddisk0`:   true,
		`\\?\globalroot\device\harddisk0`:   true,
		`\\?\Volume{1234-abcd}`:             true,
		`\\?\Volume{1234-abcd}\`:            true,
		`\\?\Volume{1234-abcd}\Filme\a.mkv`: false,
		`\\?\C:\Filme\a.mkv`:                false,
		`\\?\UNC\server\share\a.mkv`:        false,
		`\\server\share\a.mkv`:              false,
		`C:\Filme\a.mkv`:                    false,
		`/home/nutzer/a.mkv`:                false,
	}
	for in, want := range cases {
		if got := IsDeviceNamespace(in); got != want {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}

func TestIsSystemPseudoPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("nur Linux/macOS")
	}
	cases := map[string]bool{
		"/dev": true, "/dev/sda": true, "/proc/self/environ": true, "/sys/block": true,
		"/devices": false, "/home/dev/a.mkv": false, "/srv/proc": false,
	}
	for in, want := range cases {
		if got := IsSystemPseudoPath(in); got != want {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}

func TestCheckPlainPathRejectsDevices(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.mkv")
	_ = os.WriteFile(f, []byte("x"), 0o600)
	for _, ok := range []string{dir, f} {
		if _, err := CheckPlainPath(ok); err != nil {
			t.Fatalf("%s muss erlaubt sein: %v", ok, err)
		}
	}
	if runtime.GOOS == "windows" {
		if _, err := CheckPlainPath(`\\.\PhysicalDrive0`); err == nil {
			t.Fatal("Rohlaufwerk muss abgelehnt werden")
		}
		return
	}
	for _, bad := range []string{"/dev/null", "/dev", "/proc"} {
		if _, err := CheckPlainPath(bad); err == nil {
			t.Fatalf("%s muss abgelehnt werden", bad)
		}
	}
}

// Ein Gerät wie /dev/zero darf den Speicher nicht füllen; große Dateien werden abgewiesen.
func TestReadSmallFileLimits(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "a.nfo")
	_ = os.WriteFile(small, []byte("hallo"), 0o600)
	if b, err := ReadSmallFile(small, 100); err != nil || string(b) != "hallo" {
		t.Fatalf("kleine Datei: %q %v", b, err)
	}
	big := filepath.Join(dir, "b.nfo")
	_ = os.WriteFile(big, []byte(strings.Repeat("x", 101)), 0o600)
	if _, err := ReadSmallFile(big, 100); err == nil {
		t.Fatal("zu große Datei muss abgelehnt werden")
	}
	if runtime.GOOS == "windows" {
		return
	}
	done := make(chan error, 1)
	go func() { _, err := ReadSmallFile("/dev/zero", 100); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("/dev/zero muss abgelehnt werden")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("/dev/zero blockiert")
	}
}
