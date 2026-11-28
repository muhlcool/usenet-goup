package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSecurityMiddleware(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := securityMiddleware(ok)
	type tc struct {
		name, method, path, host, site, mode, origin, ctype string
		body                                                string
		want                                                int
	}
	cases := []tc{
		{"same-origin json", "POST", "/api/run", listenAddr, "same-origin", "cors", "http://" + listenAddr, "application/json", "{}", 200},
		{"cross-site no-cors text/plain", "POST", "/api/run", listenAddr, "cross-site", "no-cors", "https://evil.example", "text/plain", "{}", 403},
		{"dns rebinding host", "GET", "/api/config", "evil.example:8095", "same-origin", "cors", "", "", "", 403},
		{"img shutdown", "GET", "/api/shutdown", listenAddr, "cross-site", "no-cors", "", "", "", 403},
		{"old browser text/plain", "POST", "/api/run", listenAddr, "", "", "", "text/plain", "{}", 415},
		{"foreign origin", "POST", "/api/run", listenAddr, "", "", "https://evil.example", "application/json", "{}", 403},
		{"link navigation to UI", "GET", "/", listenAddr, "cross-site", "navigate", "", "", "", 200},
		{"typed URL", "GET", "/", "localhost:8095", "none", "navigate", "", "", "", 200},
		{"post without body", "POST", "/api/cache/clear", listenAddr, "same-origin", "cors", "http://" + listenAddr, "", "", 200},
		{"ipv6 loopback host", "GET", "/api/config", "[::1]:8095", "same-origin", "cors", "", "", "", 200},
		{"localhost falscher port", "GET", "/api/config", "localhost:9999", "same-origin", "cors", "", "", "", 403},
		{"host ohne port", "GET", "/api/config", "localhost", "same-origin", "cors", "", "", "", 403},
		{"host-suffix-trick", "GET", "/api/config", "127.0.0.1.evil.example:8095", "same-origin", "cors", "", "", "", 403},
		{"andere lokale web-app same-site", "POST", "/api/run", listenAddr, "same-site", "cors", "http://localhost:3000", "application/json", "{}", 403},
		{"andere lokale web-app ohne fetch-metadata", "POST", "/api/run", listenAddr, "", "", "http://localhost:3000", "application/json", "{}", 403},
		{"null origin (sandbox/file)", "POST", "/api/config", listenAddr, "", "", "null", "application/json", "{}", 403},
		{"json mit charset", "POST", "/api/run", listenAddr, "same-origin", "cors", "http://" + listenAddr, "application/json; charset=utf-8", "{}", 200},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "http://"+c.host+c.path, strings.NewReader(c.body))
		req.Host = c.host
		for k, v := range map[string]string{"Sec-Fetch-Site": c.site, "Sec-Fetch-Mode": c.mode, "Origin": c.origin, "Content-Type": c.ctype} {
			if v != "" {
				req.Header.Set(k, v)
			}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: got %d want %d", c.name, rec.Code, c.want)
		}
	}
}

func TestSanitizeRequest(t *testing.T) {
	var r PipelineRequest
	r.PartSizeMB, r.Par2Pct, r.PasswordLength = -5, 500, 2
	r.Server.Connections = 9999
	sanitizeRequest(&r)
	if r.PartSizeMB < 1 || r.Par2Pct != 100 || r.PasswordLength < 8 || r.Server.Connections > 100 {
		t.Fatalf("%+v", r)
	}
}

func TestRequiredWorkSpace(t *testing.T) {
	if got := requiredWorkSpace(10<<30, 10); got < 11<<30 || got > 12<<30 {
		t.Fatalf("got %d", got)
	}
}

// Die Keys brauchen echte Eingabefelder: Ohne Feld schickt die automatische Speicherung
// sie leer mit und überschreibt den gespeicherten Wert in config.json.
func TestUIHasNoImageUploadOrKeyFields(t *testing.T) {
	html, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"imgbbApiKey", "tmdbApiKey", "imageHoster"} {
		if strings.Contains(string(html), id) {
			t.Fatalf("entferntes UI-Element/Feld %q steht noch in web/index.html", id)
		}
	}
}

// Bedienelemente v1.37: PAR2-Haken nur bei
// "Nur hochladen", Quelle vor Modus; Entwickler-Infos sind raus.
func TestUILayoutV137(t *testing.T) {
	raw, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	for _, id := range []string{`id="par2Row"`, `id="posterField"`, "verifyMissing"} {
		if !strings.Contains(html, id) {
			t.Fatalf("%s fehlt in web/index.html", id)
		}
	}
	for _, id := range []string{"crashLogInfo", "uiPerfInfo", "directPwdNote", "<AppDir>", `id="maxMBs"`, `id="verifyUpload"`} {
		if strings.Contains(html, id) {
			t.Fatalf("%q sollte nicht mehr in der Oberfläche stehen", id)
		}
	}
	if strings.Index(html, `id="sourcePath"`) > strings.Index(html, `id="modePack"`) {
		t.Fatal("Quellpfad muss vor dem Modus-Schalter stehen")
	}
}

// Ein einzelnes "async" ohne folgende Funktion ist für node --check gültig
// (Ausdrucksanweisung), wirft aber beim Laden einen ReferenceError und bricht
// das gesamte Skript ab (leere Engine-Dropdowns, kein Auto-RAR/ParPar).
func TestUINoDanglingAsyncKeyword(t *testing.T) {
	html, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^\s*async\s*;?\s*$`)
	if loc := re.FindIndex(html); loc != nil {
		line := 1 + strings.Count(string(html[:loc[0]]), "\n")
		t.Fatalf("Verwaistes 'async' in web/index.html Zeile %d bricht das UI-Skript ab", line)
	}
}

// Kaputte Escapes (z. B. "\b" als echtes Backspace-Zeichen) fallen weder gofmt noch dem
// Compiler auf, wenn sie in README, HTML oder Skripten stehen.
func TestNoControlCharsInTextFiles(t *testing.T) {
	exts := map[string]bool{".go": true, ".md": true, ".html": true, ".ps1": true, ".yml": true, ".desktop": true, ".svg": true}
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "dist") {
			return filepath.SkipDir
		}
		if d.IsDir() || !exts[filepath.Ext(path)] {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, b := range raw {
			if b < 0x20 && b != '\n' && b != '\r' && b != '\t' {
				line := 1 + strings.Count(string(raw[:i]), "\n")
				t.Errorf("%s Zeile %d: Steuerzeichen 0x%02X (kaputtes Escape?)", path, line, b)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Das Archiv-Passwort geht an die Oberfläche, aber nie in die Konsolenausgabe.
func TestLogSecretKeepsPasswordOutOfConsole(t *testing.T) {
	var console bytes.Buffer
	log.SetOutput(&console)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	b := NewLogBroker(nil)
	ch := make(chan sseEvent, 4)
	b.mu.Lock()
	b.clients[ch] = true
	b.mu.Unlock()

	b.LogSecret("Passwort: Geheim123xyz", "Passwort: (nur in der Oberfläche)")
	if strings.Contains(console.String(), "Geheim123xyz") {
		t.Fatalf("Passwort in der Konsole: %q", console.String())
	}
	select {
	case ev := <-ch:
		if !strings.Contains(ev.data, "Geheim123xyz") {
			t.Fatalf("Oberfläche hat das Passwort nicht bekommen: %q", ev.data)
		}
	default:
		t.Fatal("keine Nachricht an die Oberfläche")
	}
}

// Schutz gegen Rückfall: keine Log-Zeile im Job-Code gibt ein Passwort über broker.Log aus.
func TestNoPasswordViaPlainLog(t *testing.T) {
	re := regexp.MustCompile(`broker\.Log\([^\n]*(?i:passwor)[^\n]*,\s*(pkg\.)?password\)`)
	for _, f := range []string{"pipeline.go", "direct.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if m := re.Find(src); m != nil {
			t.Fatalf("%s gibt ein Passwort über broker.Log aus: %s", f, m)
		}
	}
}
