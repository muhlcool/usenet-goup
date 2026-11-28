package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"usenet-goup/internal/media"
	"usenet-goup/internal/nntp"
	"usenet-goup/internal/sysproc"
)

type capturedUpload struct {
	files []string
	names []string
	obf   bool
}

// captureUploads ersetzt den Upload und merkt sich Dateien und Upload-Namen.
func captureUploads(e *testEnv) *[]capturedUpload {
	var got []capturedUpload
	e.a.upload = func(ctx context.Context, cfg nntp.ServerConfig, files []string, names []string, obf bool, hl int, l func(string), p func(sysproc.ProgressStats), h func()) (*nntp.UploadResult, error) {
		e.uploads.Add(1)
		got = append(got, capturedUpload{files: append([]string(nil), files...), names: append([]string(nil), names...), obf: obf})
		res := &nntp.UploadResult{Groups: []string{"alt.test"}, UsedHeader: "hdr", PostTimestamp: 1}
		for range files {
			res.NZBFiles = append(res.NZBFiles, nntp.NZBFile{Subject: "s", Groups: res.Groups, Segments: []nntp.NZBSegment{{Bytes: 1, Number: 1, MsgID: "x@test"}}})
		}
		return res, nil
	}
	return &got
}

func directReq(e *testEnv, src string, par2Off bool) map[string]any {
	return map[string]any{
		"uploadMode": "direct", "par2Disabled": par2Off, "par2Pct": 10,
		"sourcePath": src, "obfuscate": true, "obfuscationLength": 16,
		"nzbOutDir": filepath.Join(e.dir, "out"), "templateType": "misc",
		"server": map[string]any{"host": "news.example"},
	}
}

func writeFile(t *testing.T, p string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			m[p] = string(b)
		}
		return nil
	})
	return m
}

// Einzeldatei mit beliebiger Endung: unverändert, ohne Kopie, ohne Arbeitsordner hochgeladen.
func TestDirectUploadSingleFileUnchanged(t *testing.T) {
	e := newTestEnv(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	src := filepath.Join(e.dir, "src", "irgendwas.xyz")
	writeFile(t, src, "inhalt-123")
	before := snapshotDir(t, filepath.Join(e.dir, "src"))
	got := captureUploads(e)

	rec, out := e.post("/api/run", directReq(e, src, true))
	if rec.Code != 200 || out["status"] != "ok" {
		t.Fatalf("Direkt-Upload fehlgeschlagen: %d %v", rec.Code, out)
	}
	if len(*got) != 1 || !reflect.DeepEqual((*got)[0].files, []string{src}) {
		t.Fatalf("erwartet genau die Originaldatei, got %+v", *got)
	}
	if (*got)[0].names != nil && len((*got)[0].names) > 0 {
		t.Fatalf("ohne PAR2 dürfen Dateinamen nicht verschleiert werden, got %v", (*got)[0].names)
	}
	if !reflect.DeepEqual(before, snapshotDir(t, filepath.Join(e.dir, "src"))) {
		t.Fatal("Quellordner wurde verändert")
	}
	if m, _ := filepath.Glob(filepath.Join(tmp, "usenet-goup-*")); len(m) != 0 {
		t.Fatalf("unnötiger Arbeitsordner angelegt: %v", m)
	}
}

// Ordner: Systemdateien, leere Dateien und Verknüpfungen werden übersprungen, Reihenfolge stabil.
func TestDirectUploadFolderSkipsSystemFiles(t *testing.T) {
	e := newTestEnv(t)
	dir := filepath.Join(e.dir, "Album")
	writeFile(t, filepath.Join(dir, "02.mp3"), "b")
	writeFile(t, filepath.Join(dir, "01.mp3"), "a")
	writeFile(t, filepath.Join(dir, "cover.jpg"), "c")
	writeFile(t, filepath.Join(dir, "Thumbs.db"), "x")
	writeFile(t, filepath.Join(dir, ".DS_Store"), "x")
	writeFile(t, filepath.Join(dir, "._01.mp3"), "x")
	writeFile(t, filepath.Join(dir, "leer.txt"), "")
	_ = os.MkdirAll(filepath.Join(dir, "leerer-unterordner"), 0o700)
	secret := filepath.Join(e.dir, "geheim.txt")
	writeFile(t, secret, "nicht hochladen")
	_ = os.Symlink(secret, filepath.Join(dir, "link.txt"))
	got := captureUploads(e)

	rec, out := e.post("/api/run", directReq(e, dir, true))
	if rec.Code != 200 {
		t.Fatalf("Ordner-Upload fehlgeschlagen: %d %v", rec.Code, out)
	}
	want := []string{filepath.Join(dir, "01.mp3"), filepath.Join(dir, "02.mp3"), filepath.Join(dir, "cover.jpg")}
	if !reflect.DeepEqual((*got)[0].files, want) {
		t.Fatalf("erwartet %v, got %v", want, (*got)[0].files)
	}
}

// Unterordner mit Inhalt: klare Fehlermeldung statt stiller Auswahl, kein Upload.
func TestDirectUploadRejectsSubfolders(t *testing.T) {
	e := newTestEnv(t)
	dir := filepath.Join(e.dir, "Album")
	writeFile(t, filepath.Join(dir, "CD1", "01.mp3"), "a")
	writeFile(t, filepath.Join(dir, "info.txt"), "i")
	captureUploads(e)

	rec, out := e.post("/api/run", directReq(e, dir, true))
	if rec.Code != 400 || !bytes.Contains([]byte(out["error"].(string)), []byte("CD1")) {
		t.Fatalf("erwartet 400 mit Hinweis auf CD1, got %d %v", rec.Code, out)
	}
	if e.uploads.Load() != 0 {
		t.Fatal("trotz Unterordner hochgeladen")
	}
}

func TestDirectUploadRejectsSymlinkSource(t *testing.T) {
	e := newTestEnv(t)
	target := filepath.Join(e.dir, "ziel.bin")
	writeFile(t, target, "x")
	link := filepath.Join(e.dir, "link.bin")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("Symlinks hier nicht möglich")
	}
	captureUploads(e)
	if rec, _ := e.post("/api/run", directReq(e, link, true)); rec.Code != 400 {
		t.Fatalf("Symlink als Quelle muss abgelehnt werden, got %d", rec.Code)
	}
}

// Fehlgeschlagener Upload und "Cache leeren" dürfen Originaldateien nie löschen.
func TestDirectSourceSurvivesFailureAndCacheClear(t *testing.T) {
	e := newTestEnv(t)
	dir := filepath.Join(e.dir, "Buecher")
	writeFile(t, filepath.Join(dir, "a.epub"), "a")
	writeFile(t, filepath.Join(dir, "b.pdf"), "b")
	before := snapshotDir(t, dir)
	e.a.upload = func(ctx context.Context, cfg nntp.ServerConfig, files []string, names []string, obf bool, hl int, l func(string), p func(sysproc.ProgressStats), h func()) (*nntp.UploadResult, error) {
		return nil, os.ErrDeadlineExceeded
	}
	if rec, _ := e.post("/api/run", directReq(e, dir, true)); rec.Code != 500 {
		t.Fatalf("erwartet Upload-Fehler 500, got %d", rec.Code)
	}
	if rec, _ := e.post("/api/cache/clear", map[string]any{}); rec.Code != 200 {
		t.Fatalf("Cache leeren fehlgeschlagen: %d", rec.Code)
	}
	if !reflect.DeepEqual(before, snapshotDir(t, dir)) {
		t.Fatal("Originaldateien wurden gelöscht oder verändert")
	}
	got := captureUploads(e)
	if rec, _ := e.post("/api/run", directReq(e, dir, true)); rec.Code != 200 || len(*got) != 1 {
		t.Fatalf("erneuter Versuch fehlgeschlagen: %d", rec.Code)
	}
	if !reflect.DeepEqual(before, snapshotDir(t, dir)) {
		t.Fatal("Originaldateien nach erfolgreichem Upload verändert/gelöscht")
	}
}

// Moduswahl und PAR2-Haken werden gespeichert; unbekannter Modus fällt auf "pack" zurück.
func TestConfigStoresUploadModeAndPar2Switch(t *testing.T) {
	e := newTestEnv(t)
	e.post("/api/config", map[string]any{"host": "news.example", "uploadMode": "direct", "par2Disabled": true})
	_, cfg := e.get("/api/config")
	if cfg["uploadMode"] != "direct" || cfg["par2Disabled"] != true {
		t.Fatalf("Einstellungen nicht übernommen: %v %v", cfg["uploadMode"], cfg["par2Disabled"])
	}
	e.post("/api/config", map[string]any{"host": "news.example", "uploadMode": "../../etc"})
	if _, cfg = e.get("/api/config"); cfg["uploadMode"] != "pack" {
		t.Fatalf("unbekannter Modus muss zu pack werden, got %v", cfg["uploadMode"])
	}
}

// Alte config.json ohne neue Felder: Packen bleibt Standard, PAR2 bleibt an.
func TestOldConfigKeepsPackAndPar2(t *testing.T) {
	e := newTestEnv(t)
	writeFile(t, e.a.configPath, `{"host":"news.example","par2Pct":10}`)
	_, cfg := e.get("/api/config")
	if cfg["uploadMode"] != "pack" || cfg["par2Disabled"] != false {
		t.Fatalf("Upgrade-Fall: erwartet pack + PAR2 an, got %v %v", cfg["uploadMode"], cfg["par2Disabled"])
	}
}

func TestDirectUploadFileLimit(t *testing.T) {
	old := maxDirectFiles
	maxDirectFiles = 20
	t.Cleanup(func() { maxDirectFiles = old })
	dir := t.TempDir()
	for i := 0; i <= maxDirectFiles; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("f%05d.bin", i)), "x")
	}
	if _, _, _, err := collectDirectFiles(dir); err == nil {
		t.Fatalf("mehr als %d Dateien müssen abgelehnt werden", maxDirectFiles)
	}
}

// "Nur hochladen" mit selbst verschlüsseltem Archiv: das Passwort landet (XML-sicher) in NZB und Vorlage.
func TestDirectExistingPasswordInNZBAndTemplate(t *testing.T) {
	e := newTestEnv(t)
	t.Setenv("TMPDIR", t.TempDir())
	src := filepath.Join(e.dir, "src", "film.rar")
	writeFile(t, src, "rar-daten")
	captureUploads(e)
	req := directReq(e, src, true)
	req["existingPassword"] = "  Geh&im<123  "
	rec, out := e.post("/api/run", req)
	if rec.Code != 200 {
		t.Fatalf("got %d %v", rec.Code, out)
	}
	nzb, err := os.ReadFile(filepath.Join(e.dir, "out", "film.rar.nzb"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(nzb, []byte(`<meta type="password">Geh&amp;im&lt;123</meta>`)) {
		t.Fatalf("Passwort fehlt in der NZB:\n%s", nzb)
	}
	if tpl, _ := out["postingTemplate"].(string); !bytes.Contains([]byte(tpl), []byte("Geh&im<123")) {
		t.Fatalf("Passwort fehlt in der Vorlage: %q", tpl)
	}
	if b, _ := os.ReadFile(filepath.Join(e.dir, "config.json")); bytes.Contains(b, []byte("Geh&im")) {
		t.Fatal("vorhandenes Passwort darf nicht in config.json landen")
	}
}

// Unzulässige Zeichen im vorhandenen Passwort werden abgewiesen, auf Deutsch und Englisch.
func TestDirectExistingPasswordRejectsBadChars(t *testing.T) {
	e := newTestEnv(t)
	src := filepath.Join(e.dir, "src", "film.rar")
	writeFile(t, src, "rar-daten")
	captureUploads(e)
	for _, pw := range []string{`ab|c`, "ab\nc", string(bytes.Repeat([]byte("x"), 129))} {
		req := directReq(e, src, true)
		req["existingPassword"] = pw
		rec, out := e.post("/api/run", req)
		if rec.Code != 400 || !bytes.HasPrefix([]byte(fmt.Sprint(out["errorEn"])), []byte("The existing password")) {
			t.Fatalf("%q: got %d %v", pw, rec.Code, out)
		}
	}
	if e.uploads.Load() != 0 {
		t.Fatal("bei ungültigem Passwort darf nichts hochgeladen werden")
	}
}

// Beim Packen zählt nur das eigene Archiv-Passwort; ein vorhandenes Passwort wird ignoriert.
func TestPackModeIgnoresExistingPassword(t *testing.T) {
	e := newTestEnv(t)
	captureUploads(e)
	req := e.runReq(false, "news.example")
	req["existingPassword"] = "nicht-verwenden"
	if rec, out := e.post("/api/run", req); rec.Code != 200 {
		t.Fatalf("got %d %v", rec.Code, out)
	}
	_ = filepath.WalkDir(e.dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Ext(p) == ".nzb" {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("nicht-verwenden")) {
				t.Fatalf("vorhandenes Passwort im Packen-Modus in %s", p)
			}
		}
		return nil
	})
}

// Vorlage abgeschaltet: nur die NZB, keine .txt, kein MediaInfo, keine Metadaten-Abfrage.
func TestNoPostingTemplateWritesOnlyNZB(t *testing.T) {
	e := newTestEnv(t)
	captureUploads(e)
	called := false
	e.a.fetchMeta = func(client *http.Client, releaseName, nfoContent, tmdbKey, lang string, logFn func(string)) media.MediaMeta {
		called = true
		return media.MediaMeta{}
	}
	req := e.runReq(false, "news.example")
	req["noPostingTemplate"] = true
	req["onlineMetadata"] = true
	req["templateType"] = "movies_series"
	rec, out := e.post("/api/run", req)
	if rec.Code != 200 || out["status"] != "ok" {
		t.Fatalf("got %d %v", rec.Code, out)
	}
	if out["txtPath"] != nil || out["postingTemplate"] != nil {
		t.Fatalf("ohne Vorlage keine .txt erwartet: %v", out)
	}
	if called {
		t.Fatal("ohne Vorlage dürfen keine Metadaten abgefragt werden")
	}
	nzbPath, _ := out["nzbPath"].(string)
	if _, err := os.Stat(nzbPath); err != nil {
		t.Fatalf("NZB fehlt: %v", err)
	}
	if _, err := os.Stat(strings.TrimSuffix(nzbPath, ".nzb") + ".txt"); !os.IsNotExist(err) {
		t.Fatal(".txt darf nicht entstehen")
	}
}
