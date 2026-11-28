package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"usenet-goup/internal/media"
	"usenet-goup/internal/nntp"
	"usenet-goup/internal/sysproc"
)

type testEnv struct {
	verified atomic.Int64
	t        *testing.T
	a        *app
	h        http.Handler
	dir      string
	src      string
	uploads  atomic.Int32
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	// Eigener Temp-Ordner je Test: Arbeitspakete landen nie im System-Temp (auch unter Windows).
	tmp := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, tmp)
	}
	a := newApp(filepath.Join(dir, "data"), "")
	_ = os.MkdirAll(a.appDir, 0o700)
	a.exit = func(int) {}
	env := &testEnv{t: t, a: a, dir: dir}
	a.upload = func(ctx context.Context, cfg nntp.ServerConfig, files []string, names []string, obf bool, hl int, logFn func(string), pf func(sysproc.ProgressStats), hb func()) (*nntp.UploadResult, error) {
		env.uploads.Add(1)
		res := &nntp.UploadResult{Groups: []string{"alt.test"}, UsedHeader: "hdr", PostTimestamp: 1}
		for i, f := range files {
			if _, err := os.Stat(f); err != nil {
				return nil, err
			}
			res.NZBFiles = append(res.NZBFiles, nntp.NZBFile{Subject: f, Groups: res.Groups,
				Segments: []nntp.NZBSegment{{Bytes: 1, Number: 1, MsgID: "id" + string(rune('a'+i)) + "@test"}}})
		}
		return res, nil
	}
	// Standard im Test: Prüfung nach dem Upload findet alles (kein echter Server nötig).
	a.verify = func(ctx context.Context, cfg nntp.ServerConfig, ids []string, recheck time.Duration, l func(string)) (nntp.VerifyResult, error) {
		env.verified.Add(1)
		return nntp.VerifyResult{Checked: len(ids)}, nil
	}
	env.h = a.routes(fstest.MapFS{"index.html": {Data: []byte("ok")}})
	_ = os.MkdirAll(filepath.Join(dir, "out"), 0o700)
	env.src = filepath.Join(dir, "Der.Film.2024.mkv")
	_ = os.WriteFile(env.src, bytes.Repeat([]byte("x"), 3*1024*1024+7), 0o600)
	return env
}

func (e *testEnv) post(path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "http://"+listenAddr+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func (e *testEnv) get(path string) (*httptest.ResponseRecorder, map[string]any) {
	req := httptest.NewRequest("GET", "http://"+listenAddr+path, nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func (e *testEnv) runReq(dry bool, host string) map[string]any {
	return map[string]any{
		"dryRun": dry, "sourcePath": e.src, "engine": "native", "partSizeMB": 1, "par2Pct": 0,
		"obfuscate": true, "obfuscationLength": 16, "nzbOutDir": filepath.Join(e.dir, "out"),
		"server": map[string]any{"host": host},
	}
}

func TestDryRunThenUploadReusesPackage(t *testing.T) {
	e := newTestEnv(t)
	_ = os.MkdirAll(filepath.Join(e.dir, "out"), 0o700)
	rec, out := e.post("/api/run", e.runReq(true, ""))
	if rec.Code != 200 || out["status"] != "dryrun_ok" || out["fileCount"].(float64) != 4 {
		t.Fatalf("dry run: %d %v", rec.Code, out)
	}
	tempDir := out["tempDir"].(string)

	rec, out = e.post("/api/run", e.runReq(false, "news.example"))
	if rec.Code != 200 || out["status"] != "ok" {
		t.Fatalf("upload: %d %v", rec.Code, out)
	}
	nzb := out["nzbPath"].(string)
	if _, err := os.Stat(nzb); err != nil {
		t.Fatalf("NZB fehlt: %v", err)
	}
	if data, _ := os.ReadFile(strings.TrimSuffix(nzb, ".nzb") + ".txt"); !strings.Contains(string(data), "[EZPOST]Der.Film.2024|hdr|alt.test|") {
		t.Fatalf("Vorlage falsch: %s", data)
	}
	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Fatal("Paketordner wurde nach Erfolg nicht gelöscht")
	}
}

func TestUploadFailureKeepsPackageForRetry(t *testing.T) {
	e := newTestEnv(t)
	okUpload := e.a.upload
	e.a.upload = func(ctx context.Context, cfg nntp.ServerConfig, files []string, names []string, obf bool, hl int, l func(string), p func(sysproc.ProgressStats), h func()) (*nntp.UploadResult, error) {
		return nil, errors.New("Server weg")
	}
	if rec, _ := e.post("/api/run", e.runReq(false, "news.example")); rec.Code != 500 {
		t.Fatalf("Fehler erwartet, got %d", rec.Code)
	}
	if len(e.a.dryCache.Files) == 0 {
		t.Fatal("Paket wurde nach Upload-Fehler nicht behalten")
	}
	e.a.upload = okUpload
	if rec, out := e.post("/api/run", e.runReq(false, "news.example")); rec.Code != 200 {
		t.Fatalf("Wiederholung: %d %v", rec.Code, out)
	}
}

func TestSecondJobGets409AndCancelWorks(t *testing.T) {
	e := newTestEnv(t)
	started := make(chan struct{})
	e.a.upload = func(ctx context.Context, cfg nntp.ServerConfig, files []string, names []string, obf bool, hl int, l func(string), p func(sysproc.ProgressStats), h func()) (*nntp.UploadResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan int)
	go func() {
		rec, _ := e.post("/api/run", e.runReq(false, "news.example"))
		done <- rec.Code
	}()
	<-started
	if rec, _ := e.post("/api/run", e.runReq(false, "news.example")); rec.Code != http.StatusConflict {
		t.Fatalf("409 erwartet, got %d", rec.Code)
	}
	if _, out := e.post("/api/cancel", nil); out["cancelled"] != true {
		t.Fatal("Abbruch nicht bestätigt")
	}
	select {
	case code := <-done:
		if code != 500 {
			t.Fatalf("abgebrochener Job: %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Job reagiert nicht auf Abbruch")
	}
}

func TestConfigSecretsEncryptedOnDisk(t *testing.T) {
	e := newTestEnv(t)
	e.post("/api/config", map[string]any{"host": "news.example", "password": "pw", "archivePassword": "ap"})
	raw, _ := os.ReadFile(e.a.configPath)
	for _, secret := range []string{`"pw"`, `"ap"`} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("%s steht im Klartext in config.json", secret)
		}
	}
	req := httptest.NewRequest("GET", "http://"+listenAddr+"/api/config", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	var cfg map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &cfg)
	if cfg["password"] != "pw" || cfg["archivePassword"] != "ap" {
		t.Fatalf("GET liefert %v", cfg)
	}
}

func TestShutdownCallsExit(t *testing.T) {
	e := newTestEnv(t)
	exited := make(chan int, 1)
	e.a.exit = func(c int) { exited <- c }
	if rec, _ := e.post("/api/shutdown", map[string]any{}); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("exit wurde nicht aufgerufen")
	}
}

func TestRejectsForbiddenCharsInManualPassword(t *testing.T) {
	e := newTestEnv(t)
	for _, bad := range []string{`Pa[ss]word`, `Pa"ssword`, `Pass|word`} {
		payload := e.runReq(true, "")
		payload["autoPassword"] = false
		payload["archivePassword"] = bad
		rec, out := e.post("/api/run", payload)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for password %q, got %d (%v)", bad, rec.Code, out)
		}
	}
}

func TestMediaInfoSkippedForGamesAndMiscTemplates(t *testing.T) {
	for _, tplType := range []string{"games", "misc"} {
		t.Run(tplType, func(t *testing.T) {
			e := newTestEnv(t)
			// Create an AVI file in source dir that would produce MediaInfo if movies_series were used
			srcDir := filepath.Join(e.dir, "release_"+tplType)
			if err := os.MkdirAll(srcDir, 0o700); err != nil {
				t.Fatal(err)
			}
			// Minimal valid RIFF AVI header so ExtractMediaInfo would return AVI info if called
			aviFile := filepath.Join(srcDir, "movie.avi")
			aviHeader := []byte{
				'R', 'I', 'F', 'F', 76, 0, 0, 0,
				'A', 'V', 'I', ' ',
				'L', 'I', 'S', 'T', 64, 0, 0, 0,
				'h', 'd', 'r', 'l',
				'a', 'v', 'i', 'h', 56, 0, 0, 0,
				0xA0, 0x86, 0x01, 0x00, // 100000 us = 10 fps
				0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
				10, 0, 0, 0, // 10 frames
				0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
			}
			if err := os.WriteFile(aviFile, aviHeader, 0o600); err != nil {
				t.Fatal(err)
			}

			payload := e.runReq(false, "news.example")
			payload["sourcePath"] = aviFile // natives Splitting verarbeitet nur Einzeldateien
			payload["includeMediaInfo"] = true
			payload["templateType"] = tplType

			rec, out := e.post("/api/run", payload)
			if rec.Code != 200 || out["status"] != "ok" {
				t.Fatalf("run failed for %s: %d %v", tplType, rec.Code, out)
			}
			nzb := out["nzbPath"].(string)
			txtBytes, err := os.ReadFile(strings.TrimSuffix(nzb, ".nzb") + ".txt")
			if err != nil {
				t.Fatalf("missing txt template: %v", err)
			}
			if strings.Contains(string(txtBytes), "[MEDIAINFO]") {
				t.Fatalf("expected no [MEDIAINFO] block for templateType=%s, got:\n%s", tplType, string(txtBytes))
			}
		})
	}
}

func TestCleanupOrphanTempDirs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp) // Linux/macOS
	t.Setenv("TMP", tmp)    // Windows
	t.Setenv("TEMP", tmp)

	orphan := filepath.Join(tmp, "usenet-goup-111")
	keep := filepath.Join(tmp, "usenet-goup-222")
	foreign := filepath.Join(tmp, "anderes-programm")
	coverFile := filepath.Join(tmp, "usenet-goup-cover-1.jpg")
	for _, d := range []string{orphan, keep, foreign} {
		if err := os.MkdirAll(filepath.Join(d, "sub"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(coverFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if n := cleanupOrphanTempDirs(keep); n != 1 {
		t.Fatalf("erwartet 1 entfernten Ordner, got %d", n)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("verwaister usenet-goup-* Ordner wurde nicht entfernt")
	}
	for _, p := range []string{keep, foreign, coverFile} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s hätte erhalten bleiben müssen: %v", p, err)
		}
	}
}

func TestCacheClearRefusedWhileJobRunning(t *testing.T) {
	e := newTestEnv(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	running := filepath.Join(tmp, "usenet-goup-laufender-job")
	if err := os.MkdirAll(running, 0o700); err != nil {
		t.Fatal(err)
	}

	_, finish, ok := e.a.jobs.start()
	if !ok {
		t.Fatal("Job konnte nicht gestartet werden")
	}
	rec, _ := e.post("/api/cache/clear", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("erwartet 409 während eines Jobs, got %d", rec.Code)
	}
	if _, err := os.Stat(running); err != nil {
		t.Fatal("Arbeitsordner eines laufenden Jobs wurde gelöscht")
	}

	finish()
	rec, out := e.post("/api/cache/clear", nil)
	if rec.Code != http.StatusOK || out["cleanedTempDirs"].(float64) != 1 {
		t.Fatalf("nach Jobende erwartet 200 und 1 bereinigten Ordner, got %d %v", rec.Code, out)
	}
	if _, err := os.Stat(running); !os.IsNotExist(err) {
		t.Fatal("verwaister Ordner wurde nach Jobende nicht entfernt")
	}
}

func TestConfigDropsRemovedImageUploadFields(t *testing.T) {
	e := newTestEnv(t)
	e.post("/api/config", map[string]any{"host": "news.example", "imageHoster": "imgbb", "imgbbApiKey": "alt", "tmdbApiKey": "alt"})
	raw, err := os.ReadFile(e.a.configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"imageHoster", "imgbbApiKey", "tmdbApiKey"} {
		if strings.Contains(string(raw), `"`+k+`"`) {
			t.Fatalf("entferntes Feld %q steht noch in config.json", k)
		}
	}
}

func TestRunUsesBuiltInTMDBKey(t *testing.T) {
	e := newTestEnv(t)
	gotKey := "unverändert"
	e.a.fetchMeta = func(client *http.Client, releaseName, nfoContent, tmdbKey, lang string, logFn func(string)) media.MediaMeta {
		gotKey = tmdbKey
		return media.MediaMeta{}
	}
	payload := e.runReq(false, "news.example")
	payload["templateType"] = "movies_series"
	payload["onlineMetadata"] = true
	payload["tmdbApiKey"] = "von-aussen"
	if rec, out := e.post("/api/run", payload); rec.Code != http.StatusOK {
		t.Fatalf("run fehlgeschlagen: %d %v", rec.Code, out)
	}
	if gotKey != "" {
		t.Fatalf("Pipeline muss leeren Key (= eingebauter Standard) übergeben, got %q", gotKey)
	}
}

// Ohne Zustimmung keine Anfrage an Dritte, auch nicht mit dem alten Schlüssel aus config.json
// früherer Versionen. Eine IMDb-ID aus der NFO landet trotzdem in der Vorlage.
func TestOnlineMetadataOffByDefault(t *testing.T) {
	e := newTestEnv(t)
	called := false
	e.a.fetchMeta = func(client *http.Client, releaseName, nfoContent, tmdbKey, lang string, logFn func(string)) media.MediaMeta {
		called = true
		return media.MediaMeta{}
	}
	nfo := filepath.Join(t.TempDir(), "info.nfo")
	if err := os.WriteFile(nfo, []byte("https://www.imdb.com/title/tt0133093/"), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := e.runReq(false, "news.example")
	payload["templateType"] = "movies_series"
	payload["autoSearchCover"] = true
	payload["nfoPath"] = nfo
	rec, out := e.post("/api/run", payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("run fehlgeschlagen: %d %v", rec.Code, out)
	}
	if called {
		t.Fatal("Online-Metadaten ohne Zustimmung abgefragt")
	}
	if tpl, _ := out["postingTemplate"].(string); !strings.Contains(tpl, "[imdb]tt0133093[/imdb]") {
		t.Fatalf("IMDb-ID aus der NFO fehlt in der Vorlage: %q", tpl)
	}
}

// Aktionen mit Nebenwirkung nur per POST (kein Dialog/Abbruch über einen Link oder ein <img>).
// Fehlermeldungen kommen zusätzlich auf Englisch.
func TestSideEffectsNeedPOSTAndErrorsAreBilingual(t *testing.T) {
	e := newTestEnv(t)
	for _, p := range []string{"/api/browse", "/api/cancel", "/api/shutdown", "/api/cache/clear", "/api/clipboard", "/api/run"} {
		if rec, _ := e.get(p); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: %d, erwartet 405", p, rec.Code)
		}
	}
	req := e.runReq(false, "news.example")
	req["sourcePath"] = filepath.Join(t.TempDir(), "gibtsnicht")
	rec, out := e.post("/api/run", req)
	if rec.Code == http.StatusOK || !strings.HasPrefix(fmt.Sprint(out["errorEn"]), "Source path not usable:") {
		t.Fatalf("englische Fehlermeldung fehlt: %d %v", rec.Code, out)
	}
}

// Übergroße JSON-Bodies werden mit 413 abgewiesen und verändern nichts.
func TestOversizedBodiesRejected(t *testing.T) {
	e := newTestEnv(t)
	e.post("/api/config", map[string]any{"host": "news.example"})
	before, _ := os.ReadFile(e.a.configPath)

	big := func(n int) []byte {
		return []byte(`{"host":"` + strings.Repeat("a", n) + `"}`)
	}
	for _, c := range []struct {
		path string
		body []byte
	}{
		{"/api/config", big(maxSmallJSONBody + 1)},
		{"/api/test-connection", big(maxSmallJSONBody + 1)},
		{"/api/run", big(maxRunJSONBody + 1)},
	} {
		req := httptest.NewRequest("POST", "http://"+listenAddr+c.path, bytes.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: übergroßer Body erwartet 413, got %d", c.path, rec.Code)
		}
	}
	after, _ := os.ReadFile(e.a.configPath)
	if !bytes.Equal(before, after) {
		t.Fatal("config.json wurde durch übergroße Anfrage verändert")
	}
	if e.uploads.Load() != 0 {
		t.Fatal("übergroße Run-Anfrage hat einen Upload ausgelöst")
	}
}

// Normale Einstellungen (inkl. langer Pfade) liegen weit unter dem Limit.
func TestNormalConfigBodyAccepted(t *testing.T) {
	e := newTestEnv(t)
	rec, _ := e.post("/api/config", map[string]any{"host": "news.example", "nzbOutDir": strings.Repeat(`C:\sehr\langer\pfad`, 200)})
	if rec.Code != http.StatusOK {
		t.Fatalf("normale Config abgewiesen: %d", rec.Code)
	}
}

// Prüfung nach dem Upload: fehlende Teile -> Warnung, Paket bleibt für einen neuen Versuch erhalten.
func TestVerifyMissingKeepsPackageAndWarns(t *testing.T) {
	e := newTestEnv(t)
	e.a.verify = func(ctx context.Context, cfg nntp.ServerConfig, ids []string, recheck time.Duration, l func(string)) (nntp.VerifyResult, error) {
		return nntp.VerifyResult{Checked: len(ids), Missing: ids[:1]}, nil
	}
	rec, out := e.post("/api/run", e.runReq(false, "news.example"))
	if rec.Code != 200 || out["verifyMissing"] != float64(1) {
		t.Fatalf("erwartet Erfolg mit 1 fehlendem Teil, got %d %v", rec.Code, out)
	}
	if _, err := os.Stat(out["nzbPath"].(string)); err != nil {
		t.Fatal("NZB muss trotzdem geschrieben sein")
	}
	if len(e.a.dryCache.Files) == 0 {
		t.Fatal("Paket wurde trotz fehlender Teile gelöscht")
	}
}

// Die Prüfung läuft immer; Fehler der Prüfung selbst brechen den Job nicht ab.
func TestVerifyAlwaysRunsAndErrorTolerated(t *testing.T) {
	e := newTestEnv(t)
	req := e.runReq(false, "news.example")
	req["server"].(map[string]any)["skipVerify"] = true // alter Schlüssel wird ignoriert
	if rec, out := e.post("/api/run", req); rec.Code != 200 {
		t.Fatalf("got %d %v", rec.Code, out)
	}
	if e.verified.Load() == 0 {
		t.Fatal("Prüfung muss immer laufen")
	}
	e.a.verify = func(ctx context.Context, cfg nntp.ServerConfig, ids []string, recheck time.Duration, l func(string)) (nntp.VerifyResult, error) {
		return nntp.VerifyResult{}, errors.New("STAT nicht erlaubt")
	}
	if rec, out := e.post("/api/run", e.runReq(false, "news.example")); rec.Code != 200 || out["verifyMissing"] != float64(0) {
		t.Fatalf("Prüf-Fehler darf den Upload nicht scheitern lassen: %d %v", rec.Code, out)
	}
}

// Nach Absturz, kill oder Herunterfahren bleiben Arbeitsordner liegen: der nächste Start räumt
// sie im Temp- und im work-Ordner weg, alles andere bleibt unberührt.
func TestCleanupOnStartRemovesLeftovers(t *testing.T) {
	e := newTestEnv(t)
	var left []string
	for _, base := range e.a.workBases() {
		if err := os.MkdirAll(base, 0o700); err != nil {
			t.Fatal(err)
		}
		d, err := os.MkdirTemp(base, "usenet-goup-*")
		if err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(d, "B38zqFTO6ox1VBEO.001"), make([]byte, 1024), 0o600)
		left = append(left, d)
	}
	keep := filepath.Join(e.a.workBases()[0], "fremder-ordner")
	_ = os.MkdirAll(keep, 0o700)

	if n := e.a.cleanupOnStart(); n != len(left) {
		t.Fatalf("entfernt: %d, erwartet %d", n, len(left))
	}
	for _, d := range left {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Fatalf("Rest nicht entfernt: %s", d)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("fremder Ordner darf nicht gelöscht werden")
	}
}
