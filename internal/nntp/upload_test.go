package nntp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeServer: NNTP-Server mit Verbindungslimit, der Artikel nach Message-ID speichert.
type fakeServer struct {
	ln       net.Listener
	limit    int64
	active   atomic.Int64
	dropEach int64        // jede n-te Verbindung nach dem ersten Artikel trennen (0 = nie)
	loseAck  int64        // jede n-te Verbindung: Artikel speichern, aber 240 nie senden (0 = nie)
	early    atomic.Int64 // Verstöße: Artikeldaten vor der 340-Antwort empfangen
	dups     atomic.Int64 // beantwortete Duplikate
	connSeq  atomic.Int64
	mu       sync.Mutex
	articles map[string][]byte
	raw      []string       // komplette Artikel (Header + yEnc), für Namensprüfungen
	unknown  atomic.Int64   // unerwartete Befehle (z. B. eingeschleust über Dateinamen)
	hidden   map[string]int // STAT meldet diese IDs so oft als fehlend (430), danach vorhanden
	stats    atomic.Int64   // Anzahl STAT-Befehle
}

func newFakeServer(t *testing.T, limit int64) *fakeServer {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{ln: ln, limit: limit, articles: map[string][]byte{}, hidden: map[string]int{}}
	go s.serve()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *fakeServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *fakeServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *fakeServer) handle(c net.Conn) {
	defer c.Close()
	if s.active.Add(1) > s.limit {
		s.active.Add(-1)
		fmt.Fprint(c, "482 Too many connections\r\n")
		return
	}
	defer s.active.Add(-1)
	seq := s.connSeq.Add(1)
	r := bufio.NewReader(c)
	fmt.Fprint(c, "200 fake ready\r\n")
	posted := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmdLine := strings.TrimSpace(line)
		if strings.HasPrefix(cmdLine, "STAT <") {
			s.stats.Add(1)
			id := strings.TrimSuffix(strings.TrimPrefix(cmdLine, "STAT <"), ">")
			s.mu.Lock()
			_, ok := s.articles[id]
			if n := s.hidden[id]; ok && n > 0 {
				s.hidden[id] = n - 1
				ok = false
			}
			s.mu.Unlock()
			if ok {
				fmt.Fprintf(c, "223 0 <%s>\r\n", id)
			} else {
				fmt.Fprint(c, "430 No such article\r\n")
			}
			continue
		}
		switch cmdLine {
		case "QUIT":
			fmt.Fprint(c, "205 bye\r\n")
			return
		case "POST":
			if s.dropEach > 0 && seq%s.dropEach == 0 && posted == 1 {
				return // Verbindungsabbruch simulieren
			}
			// Strenger Server: vor 340 dürfen keine Artikeldaten ankommen.
			_ = c.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
			if _, err := r.Peek(1); err == nil {
				s.early.Add(1)
			}
			_ = c.SetReadDeadline(time.Time{})
			fmt.Fprint(c, "340 send\r\n")
			var art bytes.Buffer
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				art.WriteString(l)
			}
			raw := art.String()
			hdr, body, _ := strings.Cut(raw, "\r\n\r\n")
			var msgID string
			for _, h := range strings.Split(hdr, "\r\n") {
				if strings.HasPrefix(h, "Message-ID: <") {
					msgID = strings.TrimSuffix(strings.TrimPrefix(h, "Message-ID: <"), ">")
				}
			}
			s.mu.Lock()
			s.raw = append(s.raw, raw)
			_, dup := s.articles[msgID]
			if !dup {
				s.articles[msgID] = decodeYenc(body)
			}
			s.mu.Unlock()
			posted++
			if dup {
				s.dups.Add(1)
				fmt.Fprint(c, "441 435 Duplicate Message-ID\r\n")
				continue
			}
			if s.loseAck > 0 && seq%s.loseAck == 0 && posted == 1 {
				return // Artikel angenommen, Bestätigung geht verloren
			}
			fmt.Fprint(c, "240 ok\r\n")
		default:
			s.unknown.Add(1)
			fmt.Fprint(c, "500 what\r\n")
		}
	}
}

func decodeYenc(body string) []byte {
	var out []byte
	for _, l := range strings.Split(body, "\r\n") {
		if l == "" || strings.HasPrefix(l, "=ybegin") || strings.HasPrefix(l, "=ypart") || strings.HasPrefix(l, "=yend") {
			continue
		}
		if strings.HasPrefix(l, "..") {
			l = l[1:]
		}
		for i := 0; i < len(l); i++ {
			ch := l[i]
			if ch == '=' {
				i++
				ch = l[i] - 64
			}
			out = append(out, ch-42)
		}
	}
	return out
}

func runUpload(t *testing.T, limit int64, conns int, dropEach int64) *fakeServer {
	return runUploadWith(t, limit, conns, func(s *fakeServer) { s.dropEach = dropEach })
}

func runUploadWith(t *testing.T, limit int64, conns int, setup func(*fakeServer)) *fakeServer {
	t.Helper()
	srv := newFakeServer(t, limit)
	setup(srv)
	dir := t.TempDir()
	var paths []string
	contents := map[string][]byte{}
	for i, size := range []int{0, 1, 768 * 1024, 768*1024*7 + 99, 3_000_000} {
		b := make([]byte, size)
		_, _ = rand.Read(b)
		p := filepath.Join(dir, "f"+strconv.Itoa(i)+".bin")
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
		contents[filepath.Base(p)] = b
	}
	cfg := ServerConfig{Host: "127.0.0.1", Port: srv.port(), TLSMode: "off", Connections: conns, MaxRetries: 4, Newsgroup: "alt.test"}
	res, err := UploadFiles(context.Background(), cfg, paths, true, 12, func(string) {}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range res.NZBFiles {
		var rebuilt []byte
		for _, seg := range f.Segments {
			if seg.MsgID == "" {
				t.Fatalf("Datei %d: leeres Segment %d", i, seg.Number)
			}
			srv.mu.Lock()
			rebuilt = append(rebuilt, srv.articles[seg.MsgID]...)
			srv.mu.Unlock()
		}
		want := contents[filepath.Base(paths[i])]
		if !bytes.Equal(rebuilt, want) {
			t.Fatalf("Datei %d: Inhalt weicht ab (%d vs %d Bytes)", i, len(rebuilt), len(want))
		}
	}
	if n := srv.early.Load(); n > 0 {
		t.Fatalf("%d Artikel vor der 340-Antwort gesendet (nicht RFC-konform)", n)
	}
	return srv
}

func TestUploadLostAckCountsDuplicateAsSuccess(t *testing.T) {
	srv := runUploadWith(t, 6, 3, func(s *fakeServer) { s.loseAck = 2 })
	if srv.dups.Load() == 0 {
		t.Fatal("Szenario nicht ausgelöst: kein Duplikat beantwortet")
	}
}

func TestUploadAllSegmentsUnderConnectionLimit(t *testing.T) { runUpload(t, 3, 12, 0) }
func TestUploadNoLimit(t *testing.T)                         { runUpload(t, 100, 8, 0) }
func TestUploadSurvivesDroppedConnections(t *testing.T)      { runUpload(t, 4, 4, 2) }

func TestUploadCancel(t *testing.T) {
	srv := newFakeServer(t, 10)
	dir := t.TempDir()
	p := filepath.Join(dir, "big.bin")
	_ = os.WriteFile(p, make([]byte, 20*1024*1024), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	cfg := ServerConfig{Host: "127.0.0.1", Port: srv.port(), TLSMode: "off", Connections: 2, Newsgroup: "alt.test"}
	if _, err := UploadFiles(ctx, cfg, []string{p}, false, 0, func(string) {}, nil, nil); err == nil {
		t.Fatal("Abbruch wurde nicht gemeldet")
	}
}

func TestHeaderSafe(t *testing.T) {
	if strings.ContainsAny(headerSafe("a\r\nX-Evil: 1"), "\r\n") {
		t.Fatal("CR/LF nicht entfernt")
	}
}

func TestDialNNTPAuthFlow(t *testing.T) {
	// 1. Server rejects AUTHINFO USER immediately (e.g. 481 / 502) -> must fail
	lnReject, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lnReject.Close()
	go func() {
		for {
			c, err := lnReject.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				fmt.Fprint(conn, "200 ready\r\n")
				r := bufio.NewReader(conn)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					cmd := strings.TrimSpace(line)
					if strings.HasPrefix(cmd, "AUTHINFO USER ") {
						fmt.Fprint(conn, "481 Authentication failed\r\n")
						return
					}
					if cmd == "QUIT" {
						fmt.Fprint(conn, "205 bye\r\n")
						return
					}
				}
			}(c)
		}
	}()

	cfgReject := ServerConfig{
		Host:     "127.0.0.1",
		Port:     lnReject.Addr().(*net.TCPAddr).Port,
		TLSMode:  "off",
		Username: "baduser",
		Password: "secret",
	}
	if conn, err := dialNNTP(cfgReject); err == nil {
		conn.close()
		t.Fatal("expected dialNNTP to fail when AUTHINFO USER is rejected with 481")
	}

	// 2. Server expects AUTHINFO USER -> 381 -> AUTHINFO PASS -> 281 -> succeeds
	lnOK, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lnOK.Close()
	go func() {
		for {
			c, err := lnOK.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				fmt.Fprint(conn, "200 ready\r\n")
				r := bufio.NewReader(conn)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					cmd := strings.TrimSpace(line)
					switch {
					case strings.HasPrefix(cmd, "AUTHINFO USER "):
						fmt.Fprint(conn, "381 PASS required\r\n")
					case strings.HasPrefix(cmd, "AUTHINFO PASS "):
						if cmd == "AUTHINFO PASS rightpass" {
							fmt.Fprint(conn, "281 Authentication accepted\r\n")
						} else {
							fmt.Fprint(conn, "481 Authentication failed\r\n")
						}
					case cmd == "QUIT":
						fmt.Fprint(conn, "205 bye\r\n")
						return
					}
				}
			}(c)
		}
	}()

	cfgOK := ServerConfig{
		Host:     "127.0.0.1",
		Port:     lnOK.Addr().(*net.TCPAddr).Port,
		TLSMode:  "off",
		Username: "gooduser",
		Password: "rightpass",
	}
	cOK, err := dialNNTP(cfgOK)
	if err != nil {
		t.Fatalf("expected dialNNTP to succeed with valid user/pass, got: %v", err)
	}
	cOK.close()

	cfgBadPass := cfgOK
	cfgBadPass.Password = "wrongpass"
	if cBad, err := dialNNTP(cfgBadPass); err == nil {
		cBad.close()
		t.Fatal("expected dialNNTP to fail with wrong password")
	}
}

// Upload-Namen ersetzen den Dateinamen in yEnc-Header und Subject; der Originalname
// (z. B. "Geheimer Titel.epub") darf in keinem Artikel auftauchen, die Datei bleibt unverändert.
func TestUploadNamedHidesOriginalFilename(t *testing.T) {
	srv := newFakeServer(t, 4)
	dir := t.TempDir()
	orig := filepath.Join(dir, "Geheimer Titel.epub")
	data := bytes.Repeat([]byte("buchinhalt-"), 100000)
	if err := os.WriteFile(orig, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := ServerConfig{Host: "127.0.0.1", Port: srv.port(), TLSMode: "off", Connections: 2, MaxRetries: 2, Newsgroup: "alt.test"}
	res, err := UploadFilesNamed(context.Background(), cfg, []string{orig}, []string{"Zufall123abc"}, true, 12, func(string) {}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.raw) == 0 {
		t.Fatal("keine Artikel empfangen")
	}
	for _, a := range srv.raw {
		if strings.Contains(a, "Geheimer") || strings.Contains(a, ".epub") {
			t.Fatalf("Originalname im Artikel sichtbar:\n%.300s", a)
		}
		if !strings.Contains(a, "name=Zufall123abc") {
			t.Fatalf("Upload-Name fehlt im yEnc-Header:\n%.300s", a)
		}
	}
	if strings.Contains(res.NZBFiles[0].Subject, "Geheimer") {
		t.Fatalf("Originalname im NZB-Subject: %s", res.NZBFiles[0].Subject)
	}
	if after, _ := os.ReadFile(orig); !bytes.Equal(after, data) {
		t.Fatal("Originaldatei verändert")
	}
	if _, err := UploadFilesNamed(context.Background(), cfg, []string{orig}, []string{"a", "b"}, true, 12, func(string) {}, nil, nil); err == nil {
		t.Fatal("falsche Anzahl Upload-Namen muss abgelehnt werden")
	}
}

// Ein präparierter Dateiname (bei "Nur hochladen" kommen fremde Namen ungefiltert rein) darf
// den Artikel nicht vorzeitig beenden und keine NNTP-Befehle einschleusen.
func TestMaliciousFilenameCannotInjectNNTP(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows erlaubt keine Zeilenumbrüche in Dateinamen")
	}
	srv := newFakeServer(t, 4)
	dir := t.TempDir()
	evil := filepath.Join(dir, "x\r\n.\r\nGROUP alt.evil\r\nPOST\r\n<b>.epub")
	if err := os.WriteFile(evil, []byte("inhalt"), 0o600); err != nil {
		t.Skip("Dateiname auf diesem Dateisystem nicht möglich")
	}
	cfg := ServerConfig{Host: "127.0.0.1", Port: srv.port(), TLSMode: "off", Connections: 1, MaxRetries: 1, Newsgroup: "alt.test"}
	res, err := UploadFiles(context.Background(), cfg, []string{evil}, false, 12, func(string) {}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := srv.unknown.Load(); n != 0 {
		t.Fatalf("%d eingeschleuste Befehle beim Server angekommen", n)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.raw) != 1 {
		t.Fatalf("erwartet genau 1 Artikel, got %d", len(srv.raw))
	}
	hdr, body, _ := strings.Cut(srv.raw[0], "\r\n\r\n")
	for _, l := range strings.Split(hdr, "\r\n") {
		if strings.HasPrefix(l, "GROUP") || strings.HasPrefix(l, "POST") {
			t.Fatalf("eingeschleuste Header-Zeile: %q", l)
		}
	}
	first, _, _ := strings.Cut(body, "\r\n")
	if !strings.HasPrefix(first, "=ybegin") || !strings.Contains(first, "name=") || strings.Contains(first, "\n") {
		t.Fatalf("yEnc-Kopfzeile beschädigt: %q", first)
	}
	if string(srv.articles[res.NZBFiles[0].Segments[0].MsgID]) != "inhalt" {
		t.Fatal("Inhalt beschädigt")
	}
	nzbPath := filepath.Join(t.TempDir(), "t.nzb")
	if err := WriteNZB(nzbPath, "t", "", res.NZBFiles); err != nil {
		t.Fatal(err)
	}
	nzb, _ := os.ReadFile(nzbPath)
	if strings.Contains(string(nzb), "<b>") || strings.Contains(string(nzb), "\r\n.\r\n") {
		t.Fatalf("NZB enthält unmaskierte Teile des Dateinamens:\n%s", nzb)
	}
}
