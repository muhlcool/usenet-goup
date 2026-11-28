//go:build !windows

package media

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Nachstellung des Hängers: Ein "mediainfo", das sich wie die Windows-GUI verhält
// (kehrt nie zurück), darf ExtractMediaInfo nicht blockieren.
func TestHangingMediaInfoIsRejected(t *testing.T) {
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep nicht verfügbar")
	}
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "mediainfo")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec "+sleepBin+" 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	oldTimeout := miProbeTimeout
	miProbeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { miProbeTimeout = oldTimeout })

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "film.iso"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_ = ExtractMediaInfo(src, func(string) {})
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("ExtractMediaInfo hing %v (GUI-MediaInfo wurde nicht erkannt)", d)
	}
	if findMediaInfoCLI() != "" {
		t.Fatal("hängendes mediainfo wurde als CLI akzeptiert")
	}
}

// Ein echtes CLI-mediainfo wird erkannt und benutzt.
func TestMediaInfoCLIAccepted(t *testing.T) {
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "mediainfo")
	script := "#!/bin/sh\nif [ \"$1\" = \"--Version\" ]; then echo 'MediaInfo Command line,'; echo 'MediaInfoLib - v24.06'; exit 0; fi\necho 'General'\necho 'Format                                   : ISO 9660'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "film.iso"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	out := ExtractMediaInfo(src, func(string) {})
	if want := "ISO 9660"; !contains(out, want) {
		t.Fatalf("CLI-Ausgabe nicht übernommen: %q", out)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
