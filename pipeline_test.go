package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"usenet-goup/internal/nntp"
	"usenet-goup/internal/sysproc"
)

type testEnv struct {
	t       *testing.T
	a       *app
	h       http.Handler
	dir     string
	src     string
	uploads atomic.Int32
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	a := newApp(filepath.Join(dir, "data"), "")
	_ = os.MkdirAll(a.appDir, 0o700)
	a.exit = func(int) {}
	env := &testEnv{t: t, a: a, dir: dir}
	a.upload = func(ctx context.Context, cfg nntp.ServerConfig, files []string, obf bool, hl int, logFn func(string), pf func(sysproc.ProgressStats), hb func()) (*nntp.UploadResult, error) {
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
	e.a.upload = func(ctx context.Context, cfg nntp.ServerConfig, files []string, obf bool, hl int, l func(string), p func(sysproc.ProgressStats), h func()) (*nntp.UploadResult, error) {
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
	e.a.upload = func(ctx context.Context, cfg nntp.ServerConfig, files []string, obf bool, hl int, l func(string), p func(sysproc.ProgressStats), h func()) (*nntp.UploadResult, error) {
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
	e.post("/api/config", map[string]any{"host": "news.example", "password": "pw", "archivePassword": "ap", "tmdbApiKey": "tk", "imgbbApiKey": "ik"})
	raw, _ := os.ReadFile(e.a.configPath)
	for _, secret := range []string{`"pw"`, `"ap"`, `"tk"`, `"ik"`} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("%s steht im Klartext in config.json", secret)
		}
	}
	req := httptest.NewRequest("GET", "http://"+listenAddr+"/api/config", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	var cfg map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &cfg)
	if cfg["password"] != "pw" || cfg["archivePassword"] != "ap" || cfg["tmdbApiKey"] != "tk" || cfg["imgbbApiKey"] != "ik" {
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
