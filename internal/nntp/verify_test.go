package nntp

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testFile(t *testing.T, size int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "daten.bin")
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), size), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVerifyFindsAllAndMissing(t *testing.T) {
	srv := newFakeServer(t, 8)
	f := testFile(t, 2*1024*1024)
	cfg := ServerConfig{Host: "127.0.0.1", Port: srv.port(), TLSMode: "off", Connections: 4, MaxRetries: 2, Newsgroup: "alt.test"}
	res, err := UploadFiles(context.Background(), cfg, []string{f}, true, 12, func(string) {}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := MessageIDs(res.NZBFiles)
	if len(ids) < 2 {
		t.Fatalf("zu wenige Segmente: %d", len(ids))
	}

	v, err := VerifyArticles(context.Background(), cfg, ids, 0, func(string) {})
	if err != nil || len(v.Missing) != 0 || v.Checked != len(ids) {
		t.Fatalf("alle vorhanden erwartet: %+v %v", v, err)
	}

	// Ein Teil fehlt dauerhaft, eins taucht erst bei der zweiten Prüfung auf (Server-Verzögerung).
	srv.mu.Lock()
	delete(srv.articles, ids[0])
	srv.hidden[ids[1]] = 1
	srv.mu.Unlock()
	var logs []string
	v, err = VerifyArticles(context.Background(), cfg, ids, 50*time.Millisecond, func(s string) { logs = append(logs, s) })
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Missing) != 1 || v.Missing[0] != ids[0] {
		t.Fatalf("erwartet genau %s fehlend, got %v", ids[0], v.Missing)
	}
	if len(logs) == 0 {
		t.Fatal("Hinweis auf erneute Prüfung fehlt")
	}
}

// Viele Artikel: gebündelte Abfrage (Pipelining) über mehrere Verbindungen.
func TestVerifyManyIDsBatched(t *testing.T) {
	srv := newFakeServer(t, 8)
	var ids []string
	srv.mu.Lock()
	for i := 0; i < 1050; i++ {
		id := generateMsgID()
		ids = append(ids, id)
		srv.articles[id] = nil
	}
	srv.mu.Unlock()
	cfg := ServerConfig{Host: "127.0.0.1", Port: srv.port(), TLSMode: "off", Connections: 20, Newsgroup: "alt.test"}
	v, err := VerifyArticles(context.Background(), cfg, ids, 0, func(string) {})
	if err != nil || len(v.Missing) != 0 {
		t.Fatalf("got %+v %v", v, err)
	}
	if n := srv.stats.Load(); n != int64(len(ids)) {
		t.Fatalf("erwartet %d STAT-Befehle, got %d", len(ids), n)
	}
	if bad, err := VerifyArticles(context.Background(), cfg, []string{"a>\r\nQUIT"}, 0, func(string) {}); err == nil {
		t.Fatalf("ungültige Message-ID muss abgelehnt werden: %+v", bad)
	}
}

// Eine endlos lange Antwortzeile darf keinen unbegrenzten Speicher belegen.
func TestOverlongServerLineRejected(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = c.Write(append([]byte("200 "), bytes.Repeat([]byte("a"), 200*1024)...))
		_, _ = c.Write([]byte("\r\n"))
	}()
	res := TestConnection(ServerConfig{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, TLSMode: "off", Connections: 1})
	if res.Success || !strings.Contains(res.Error, "zu lang") {
		t.Fatalf("erwartet Fehler 'zu lang', got %+v", res.Error)
	}
}
