//go:build !windows

// Tests, die Unix-Pfade oder Shell-Skripte brauchen (Linux und macOS).
package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"usenet-goup/internal/nntp"
	"usenet-goup/internal/sysproc"
)

// Geräte und Systemordner als Quelle: in beiden Modi abgelehnt, nichts wird hochgeladen.
func TestRejectsDeviceAndSystemSources(t *testing.T) {
	e := newTestEnv(t)
	captureUploads(e)
	for _, src := range []string{"/dev/null", "/dev", "/proc"} {
		for _, mode := range []string{"pack", "direct"} {
			req := e.runReq(false, "news.example")
			req["sourcePath"] = src
			req["uploadMode"] = mode
			rec, out := e.post("/api/run", req)
			if rec.Code != 400 {
				t.Fatalf("%s (%s): erwartet 400, got %d %v", src, mode, rec.Code, out)
			}
		}
	}
	if e.uploads.Load() != 0 {
		t.Fatal("trotz Gerätepfad hochgeladen")
	}
}

// Ausgabeordner darf kein Gerät/Systemordner sein.
func TestRejectsDeviceOutputDir(t *testing.T) {
	e := newTestEnv(t)
	captureUploads(e)
	req := e.runReq(false, "news.example")
	req["nzbOutDir"] = "/dev"
	if rec, out := e.post("/api/run", req); rec.Code != 400 {
		t.Fatalf("erwartet 400, got %d %v", rec.Code, out)
	}
}

// NFO: /dev/zero und übergroße Dateien werden nicht eingelesen (kein Speicher-Überlauf).
func TestNFOReadIsBounded(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "film.mkv")
	_ = os.WriteFile(src, []byte("x"), 0o600)
	done := make(chan string, 1)
	go func() { c, _ := nntp.FindAndReadNFO(src, "/dev/zero"); done <- c }()
	select {
	case c := <-done:
		if c != "" {
			t.Fatal("/dev/zero als NFO gelesen")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("NFO-Lesen von /dev/zero blockiert/läuft voll")
	}
	big := filepath.Join(dir, "film.nfo")
	_ = os.WriteFile(big, []byte(strings.Repeat("A", 3<<20)), 0o600)
	if c, _ := nntp.FindAndReadNFO(src, ""); c != "" {
		t.Fatal("3-MB-Datei als NFO eingelesen")
	}
	_ = os.WriteFile(big, []byte("echte NFO"), 0o600)
	if c, _ := nntp.FindAndReadNFO(src, ""); c != "echte NFO" {
		t.Fatalf("normale NFO nicht gelesen: %q", c)
	}
}

// fakeParPar legt ein "parpar" in den PATH, das Arbeitsordner und Argumente protokolliert
// und die angeforderte .par2-Datei (plus ein Volume) erzeugt.
func fakeParPar(t *testing.T) (logPath string) {
	t.Helper()
	bin := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "parpar.log")
	script := `#!/bin/sh
{ echo "CWD=$(pwd)"; for a in "$@"; do echo "ARG=$a"; done; } >> "` + logPath + `"
out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then out="$a"; fi
  prev="$a"
done
echo par2 > "$out"
echo vol > "${out%.par2}.vol00+01.par2"
echo 100%
`
	if err := os.WriteFile(filepath.Join(bin, "parpar"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func readLog(t *testing.T, p string) (cwd string, args []string) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "", nil
	}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		switch {
		case strings.HasPrefix(l, "CWD="):
			cwd = strings.TrimPrefix(l, "CWD=")
		case strings.HasPrefix(l, "ARG="):
			args = append(args, strings.TrimPrefix(l, "ARG="))
		}
	}
	return cwd, args
}

// "Nur hochladen" mit PAR2: Originale nur gelesen, PAR2 im Arbeitsordner, Datennamen verschleiert,
// PAR2-Namen bleiben (Downloader erkennt sie an der Endung).
func TestDirectPar2OriginalsUntouchedNamesObfuscated(t *testing.T) {
	logPath := fakeParPar(t)
	e := newTestEnv(t)
	t.Setenv("TMPDIR", t.TempDir())
	dir := filepath.Join(e.dir, "Buecher")
	writeFile(t, filepath.Join(dir, "a.epub"), "aaaa")
	writeFile(t, filepath.Join(dir, "b.pdf"), "bbbb")
	before := snapshotDir(t, dir)
	got := captureUploads(e)

	req := directReq(e, dir, false)
	req["par2Engine"] = "parpar"
	rec, out := e.post("/api/run", req)
	if rec.Code != 200 {
		t.Fatalf("Upload fehlgeschlagen: %d %v", rec.Code, out)
	}

	cwd, args := readLog(t, logPath)
	if filepath.Clean(cwd) != filepath.Clean(dir) {
		t.Fatalf("PAR2 muss im Quellordner rechnen (relative Namen), cwd=%q", cwd)
	}
	if len(args) < 2 || args[len(args)-2] != "a.epub" || args[len(args)-1] != "b.pdf" {
		t.Fatalf("PAR2 muss die Originalnamen ohne Pfad bekommen, args=%v", args)
	}
	if !reflect.DeepEqual(before, snapshotDir(t, dir)) {
		t.Fatal("Quellordner wurde verändert (z. B. .par2 hineingeschrieben)")
	}

	up := (*got)[0]
	if len(up.files) != 4 {
		t.Fatalf("erwartet 2 Originale + 2 PAR2, got %v", up.files)
	}
	for i, f := range up.files {
		isPar := strings.HasSuffix(f, ".par2")
		switch {
		case !isPar && (up.names[i] == "" || up.names[i] == filepath.Base(f)):
			t.Fatalf("Datei %s nicht verschleiert (Name %q)", f, up.names[i])
		case isPar && up.names[i] != "":
			t.Fatalf("PAR2-Datei %s darf nicht umbenannt werden", f)
		case isPar && strings.HasPrefix(f, dir):
			t.Fatalf("PAR2 liegt im Quellordner: %s", f)
		}
	}
	if !reflect.DeepEqual(before, snapshotDir(t, dir)) {
		t.Fatal("Originale nach Upload verändert/gelöscht")
	}
}

// Beim Packen ist PAR2 immer an: ein (alter) par2Disabled-Wert wird ignoriert.
func TestPackModeIgnoresPar2Off(t *testing.T) {
	logPath := fakeParPar(t)
	e := newTestEnv(t)
	t.Setenv("TMPDIR", t.TempDir())
	captureUploads(e)
	req := e.runReq(false, "news.example")
	req["par2Pct"] = 10
	req["par2Engine"] = "parpar"
	req["par2Disabled"] = true
	if rec, out := e.post("/api/run", req); rec.Code != 200 {
		t.Fatalf("Upload fehlgeschlagen: %d %v", rec.Code, out)
	}
	if _, args := readLog(t, logPath); len(args) == 0 {
		t.Fatal("PAR2 im Packen-Modus nicht gestartet")
	}
}

// Ende-zu-Ende mit echtem par2cmdline (falls installiert): Downloader bekommt die Daten unter
// den verschleierten Upload-Namen in einen flachen Ordner, "par2 r" muss die Originalnamen
// wiederherstellen. Prüft auch das -B (Basispfad) bei PAR2 außerhalb des Quellordners.
func TestDirectPar2RealRenameRestoresNames(t *testing.T) {
	if _, err := exec.LookPath("par2"); err != nil {
		t.Skip("par2cmdline nicht installiert")
	}
	e := newTestEnv(t)
	t.Setenv("TMPDIR", t.TempDir())
	dir := filepath.Join(e.dir, "Hoerbuch")
	writeFile(t, filepath.Join(dir, "Kapitel 01.mp3"), strings.Repeat("eins", 50000))
	writeFile(t, filepath.Join(dir, "Kapitel 02.mp3"), strings.Repeat("zwei", 70000))
	got := captureUploads(e)
	var download string
	inner := e.a.upload
	e.a.upload = func(ctx context.Context, cfg nntp.ServerConfig, files []string, names []string, obf bool, hl int, l func(string), p func(sysproc.ProgressStats), h func()) (*nntp.UploadResult, error) {
		download = t.TempDir()
		for i, f := range files {
			n := filepath.Base(f)
			if names != nil && names[i] != "" {
				n = names[i]
			}
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(download, n), b, 0o600); err != nil {
				return nil, err
			}
		}
		return inner(ctx, cfg, files, names, obf, hl, l, p, h)
	}

	req := directReq(e, dir, false)
	req["par2Engine"] = "turbo"
	if rec, out := e.post("/api/run", req); rec.Code != 200 {
		t.Fatalf("Upload fehlgeschlagen: %d %v", rec.Code, out)
	}
	if len(*got) != 1 {
		t.Fatal("kein Upload")
	}
	if _, err := os.Stat(filepath.Join(download, "Kapitel 01.mp3")); err == nil {
		t.Fatal("Originalname im Download sichtbar, Verschleierung greift nicht")
	}
	pars, _ := filepath.Glob(filepath.Join(download, "*.par2"))
	if len(pars) == 0 {
		t.Fatal("keine PAR2 im Download")
	}
	main := pars[0]
	for _, p := range pars {
		if !strings.Contains(p, ".vol") {
			main = p
		}
	}
	extra, _ := filepath.Glob(filepath.Join(download, "*"))
	args := append([]string{"r", main}, extra...)
	cmd := exec.Command("par2", args...)
	cmd.Dir = download
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("par2 repair fehlgeschlagen: %v\n%s", err, out)
	}
	for _, n := range []string{"Kapitel 01.mp3", "Kapitel 02.mp3"} {
		want, _ := os.ReadFile(filepath.Join(dir, n))
		have, err := os.ReadFile(filepath.Join(download, n))
		if err != nil || string(have) != string(want) {
			t.Fatalf("%s nach par2-Reparatur nicht wiederhergestellt (%v)", n, err)
		}
	}
}
