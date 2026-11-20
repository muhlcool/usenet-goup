package nntp

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"usenet-goup/internal/archiver"
	"usenet-goup/internal/sysproc"
	"usenet-goup/internal/yenc"
)

const DefaultSegmentKB = 768

// MaxConnections begrenzt parallele NNTP-Verbindungen (Schutz vor Fehleingaben).
const MaxConnections = 100

type ServerConfig struct {
	Host            string `json:"host"`
	Port            int    `json:"port"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	Connections     int    `json:"connections"`
	Newsgroup       string `json:"newsgroup"`
	Poster          string `json:"poster"`
	RandomizePoster bool   `json:"randomizePoster"`
	MaxRetries      int    `json:"maxRetries"`
	// TLSMode: "" / "auto" = nach Port (119 Klartext, sonst TLS), "on" = immer TLS, "off" = nie TLS.
	TLSMode string `json:"tlsMode,omitempty"`
}

// withDefaults setzt fehlende oder unsinnige Werte (früher an drei Stellen kopiert).
func (cfg ServerConfig) withDefaults() ServerConfig {
	if cfg.Port <= 0 {
		cfg.Port = 563
	}
	if cfg.Connections < 1 {
		cfg.Connections = 4
	}
	if cfg.Connections > MaxConnections {
		cfg.Connections = MaxConnections
	}
	if cfg.MaxRetries < 1 {
		cfg.MaxRetries = 3
	}
	return cfg
}

func useTLS(cfg ServerConfig) bool {
	switch strings.ToLower(strings.TrimSpace(cfg.TLSMode)) {
	case "on", "true", "ssl", "tls":
		return true
	case "off", "false", "plain":
		return false
	default:
		return IsTLSPort(cfg.Port)
	}
}

type UploadResult struct {
	NZBFiles      []NZBFile
	UsedHeader    string
	Groups        []string
	PostTimestamp int64
}

type TestResult struct {
	Success   bool   `json:"success"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	TLS       bool   `json:"tls"`
	Greeting  string `json:"greeting"`
	PostingOK bool   `json:"postingOK"`
	LatencyMs int64  `json:"latencyMs"`
	MaxConns  int    `json:"maxConns"`
	ReqConns  int    `json:"reqConns"`
	ConnNote  string `json:"connNote"`
	Error     string `json:"error,omitempty"`
}

func IsTLSPort(port int) bool {
	switch port {
	case 119, 80, 23:
		return false
	default:
		return true
	}
}

func isConnectionLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "(482)") ||
		strings.Contains(msg, "(502)") ||
		strings.Contains(msg, "too many") ||
		strings.Contains(msg, "connection limit") ||
		strings.Contains(msg, "max connections")
}

type uploadTask struct {
	fileHandle  *os.File
	fileIdx     int
	totalFiles  int
	fileName    string
	fileSize    int64
	partNum     int
	totalParts  int
	offset      int64
	byteLen     int
	baseSubject string
	newsgroup   string
	poster      string
	resultSlot  *NZBSegment
}

type nntpConn struct {
	conn      net.Conn
	reader    *bufio.Reader
	writer    *bufio.Writer
	greeting  string
	postingOK bool
}

func ParseNewsgroups(raw string) []string {
	replaced := strings.ReplaceAll(raw, ";", ",")
	parts := strings.Split(replaced, ",")
	var groups []string
	seen := make(map[string]bool)
	for _, p := range parts {
		g := strings.TrimSpace(p)
		if g != "" && !seen[g] {
			seen[g] = true
			groups = append(groups, g)
		}
	}
	if len(groups) == 0 {
		return []string{"alt.binaries.misc"}
	}
	return groups
}

func RandomPoster() string {
	name := archiver.RandomString(10)
	user := archiver.RandomString(10)
	domain := strings.ToLower(archiver.RandomString(10))
	return fmt.Sprintf("%s <%s@%s.invalid>", name, user, domain)
}

func dialNNTP(cfg ServerConfig) (*nntpConn, error) {
	addr := net.JoinHostPort(strings.Trim(cfg.Host, "[]"), strconv.Itoa(cfg.Port))
	var rawConn net.Conn
	var err error

	dialer := &net.Dialer{Timeout: 12 * time.Second}
	if useTLS(cfg) {
		rawConn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
			ServerName: cfg.Host,
		})
	} else {
		rawConn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return nil, err
	}

	if tcpConn, ok := rawConn.(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
		_ = tcpConn.SetWriteBuffer(512 * 1024)
	}

	c := &nntpConn{
		conn:   rawConn,
		reader: bufio.NewReaderSize(rawConn, 64*1024),
		writer: bufio.NewWriterSize(rawConn, 256*1024),
	}

	code, line, err := c.readResponse()
	if err != nil || (code != 200 && code != 201) {
		c.close()
		return nil, fmt.Errorf("NNTP Greeting fehlgeschlagen (%d): %s %v", code, line, err)
	}
	c.greeting = line
	c.postingOK = (code == 200)

	if cfg.Username != "" {
		if err := c.sendCmd("AUTHINFO USER %s", headerSafe(cfg.Username)); err != nil {
			c.close()
			return nil, err
		}
		code, line, err = c.readResponse()
		if err != nil {
			c.close()
			return nil, err
		}
		if code == 381 {
			if err := c.sendCmd("AUTHINFO PASS %s", headerSafe(cfg.Password)); err != nil {
				c.close()
				return nil, err
			}
			code, line, err = c.readResponse()
			if err != nil || code != 281 {
				c.close()
				return nil, fmt.Errorf("NNTP Auth fehlgeschlagen (%d): %s", code, line)
			}
		} else if code != 281 {
			c.close()
			return nil, fmt.Errorf("NNTP Auth abgelehnt (%d): %s", code, line)
		}
	}

	return c, nil
}

func TestConnection(cfg ServerConfig) TestResult {
	cfg = cfg.withDefaults()
	reqConns := min(cfg.Connections, 60)

	res := TestResult{
		Host:     cfg.Host,
		Port:     cfg.Port,
		TLS:      useTLS(cfg),
		ReqConns: reqConns,
	}
	if strings.TrimSpace(cfg.Host) == "" {
		res.Error = "Kein NNTP-Host angegeben."
		return res
	}

	start := time.Now()
	firstConn, err := dialNNTP(cfg)
	res.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		return res
	}

	// Prüfen, ob der Server wirklich POST erlaubt (z. B. 340 vs. 440 Posting Not Allowed), ohne einen Dummy-Artikel zu senden.
	canPost := firstConn.postingOK
	if err := firstConn.sendCmd("POST"); err == nil {
		pCode, _, pErr := firstConn.readResponse()
		if pErr == nil {
			if pCode == 440 {
				canPost = false
			} else if pCode == 340 {
				canPost = true
				// POST abbrechen, indem wir die Verbindung sauber trennen statt einen leeren Artikel zu posten.
				_ = firstConn.conn.Close()
				firstConn.conn = nil
				reDial, rErr := dialNNTP(cfg)
				if rErr == nil {
					firstConn = reDial
				}
			}
		}
	}

	res.Success = true
	res.Greeting = firstConn.greeting
	res.PostingOK = canPost

	conns := make([]*nntpConn, 0, reqConns)
	conns = append(conns, firstConn)
	var limitErr string

	for i := 1; i < reqConns; i++ {
		c, dialErr := dialNNTP(cfg)
		if dialErr != nil {
			limitErr = dialErr.Error()
			break
		}
		conns = append(conns, c)
	}

	for _, c := range conns {
		c.close()
	}

	res.MaxConns = len(conns)
	if res.MaxConns == reqConns {
		res.ConnNote = fmt.Sprintf("Alle %d angeforderten Verbindungen erfolgreich akzeptiert.", reqConns)
	} else {
		res.ConnNote = fmt.Sprintf("Provider erlaubt maximal %d von %d angeforderten Verbindungen (%s).", res.MaxConns, reqConns, limitErr)
	}
	return res
}

func (c *nntpConn) sendCmd(format string, args ...any) error {
	_ = c.conn.SetDeadline(time.Now().Add(30 * time.Second))
	_, err := fmt.Fprintf(c.writer, format+"\r\n", args...)
	if err != nil {
		return err
	}
	return c.writer.Flush()
}

func (c *nntpConn) readResponse() (int, string, error) {
	_ = c.conn.SetDeadline(time.Now().Add(30 * time.Second))
	line, err := c.reader.ReadString('\n')
	if err != nil {
		return 0, "", err
	}
	line = strings.TrimSpace(line)
	var code int
	_, _ = fmt.Sscanf(line, "%d", &code)
	return code, line, nil
}

// headerSafe entfernt CR/LF/NUL, damit Dateinamen oder Konfigwerte keine zusätzlichen Header einschleusen.
func headerSafe(v string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return ' '
		}
		return r
	}, v)
}

// errDuplicate: der Server kennt die Message-ID schon. Bei einer Wiederholung heißt das,
// dass der vorige Versuch angekommen ist, obwohl seine Antwort verloren ging.
var errDuplicate = errors.New("Artikel bereits vorhanden (Duplikat)")

func isDuplicateResponse(code int, line string) bool {
	if code != 441 && code != 435 && code != 437 {
		return false
	}
	low := strings.ToLower(line)
	return strings.Contains(low, "duplicate") || strings.Contains(low, "already") ||
		strings.Contains(low, "exists") || strings.Contains(low, "435") || strings.Contains(low, "437")
}

// postArticle sendet einen Artikel RFC-3977-konform: POST, auf 340 warten, dann Artikel senden.
// Der Durchsatz kommt aus den parallelen Verbindungen, die Wartezeit auf 340 kostet nur eine
// Paketlaufzeit pro Artikel und Verbindung.
func (c *nntpConn) postArticle(msgID, from, newsgroups, subject string, yencBody []byte) (int64, error) {
	var header strings.Builder
	fmt.Fprintf(&header, "From: %s\r\n", headerSafe(from))
	fmt.Fprintf(&header, "Newsgroups: %s\r\n", headerSafe(newsgroups))
	fmt.Fprintf(&header, "Subject: %s\r\n", headerSafe(subject))
	fmt.Fprintf(&header, "Message-ID: <%s>\r\n", msgID)
	fmt.Fprintf(&header, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	header.WriteString("\r\n")
	headerStr := header.String()
	bruttoBytes := int64(6 + len(headerStr) + len(yencBody) + 3)

	if err := c.sendCmd("POST"); err != nil {
		return 0, err
	}
	code, line, err := c.readResponse()
	if err != nil {
		return 0, err
	}
	if code != 340 {
		return 0, fmt.Errorf("POST nicht erlaubt (%d): %s", code, line)
	}

	_ = c.conn.SetDeadline(time.Now().Add(90 * time.Second))
	_, _ = c.writer.WriteString(headerStr)
	_, _ = c.writer.Write(yencBody)
	_, _ = c.writer.WriteString(".\r\n")
	if err := c.writer.Flush(); err != nil {
		return 0, err
	}

	code, line, err = c.readResponse()
	if err != nil {
		return 0, err
	}
	if isDuplicateResponse(code, line) {
		return bruttoBytes, fmt.Errorf("%w (%d): %s", errDuplicate, code, line)
	}
	if code != 240 {
		return 0, fmt.Errorf("Artikel abgelehnt (%d): %s", code, line)
	}
	return bruttoBytes, nil
}

func (c *nntpConn) close() {
	if c.conn != nil {
		_ = c.sendCmd("QUIT")
		_ = c.conn.Close()
	}
}

func generateMsgID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s.%d@usenet.local", hex.EncodeToString(b), time.Now().UnixNano())
}

// uploadStats bündelt die Zähler für Fortschritt und Durchsatz.
type uploadStats struct {
	start      time.Time
	totalTasks int64
	totalNetMB float64
	completed  atomic.Int64
	netBytes   atomic.Int64
	grossBytes atomic.Int64
}

func (s *uploadStats) snapshot(active bool, label string) sysproc.ProgressStats {
	done := s.completed.Load()
	net := s.netBytes.Load()
	gross := s.grossBytes.Load()
	elapsed := time.Since(s.start).Seconds()
	st := sysproc.ProgressStats{
		Phase:         "upload",
		PhaseLabel:    label,
		Active:        active,
		CompletedSegs: done,
		TotalSegs:     s.totalTasks,
		UploadedNetMB: float64(net) / (1024 * 1024),
		TotalNetMB:    s.totalNetMB,
	}
	if s.totalTasks > 0 {
		st.Percent = float64(done) / float64(s.totalTasks) * 100
	}
	if elapsed > 0 {
		st.NettoMBs = float64(net) / (1024 * 1024) / elapsed
		st.BruttoMBs = float64(gross) / (1024 * 1024) / elapsed
		st.BruttoMbits = float64(gross) * 8 / (1000 * 1000 * elapsed)
	}
	return st
}

// UploadFiles lädt alle Dateien segmentweise hoch. ctx bricht den Upload ab (Abbrechen-Button / Beenden).
func UploadFiles(ctx context.Context, cfg ServerConfig, filePaths []string, obfuscateHeaders bool, headerLen int, logFn func(string), progressFn func(sysproc.ProgressStats), heartbeatFn func()) (*UploadResult, error) {
	if len(filePaths) == 0 {
		return nil, fmt.Errorf("keine Dateien zum Hochladen")
	}
	cfg = cfg.withDefaults()

	groupsList := ParseNewsgroups(cfg.Newsgroup)
	nntpGroupHeader := strings.Join(groupsList, ",")

	effectivePoster := strings.TrimSpace(cfg.Poster)
	if cfg.RandomizePoster || effectivePoster == "" {
		effectivePoster = RandomPoster()
		logFn(fmt.Sprintf("Zufälliger From-Header aktiv: %s", effectivePoster))
	}

	segmentBytes := DefaultSegmentKB * 1024
	nzbFiles := make([]NZBFile, len(filePaths))
	openHandles := make([]*os.File, 0, len(filePaths))
	defer func() {
		for _, fh := range openHandles {
			_ = fh.Close()
		}
	}()

	var tasks []uploadTask
	var totalBytes int64
	postTimestamp := time.Now().Unix()

	baseSubject := "Upload"
	if obfuscateHeaders {
		baseSubject = archiver.RandomString(headerLen)
	}

	for idx, path := range filePaths {
		normFile := sysproc.NormalizePath(path)
		info, err := os.Stat(normFile)
		if err != nil {
			return nil, err
		}
		fSize := info.Size()
		totalBytes += fSize
		fName := filepath.Base(sysproc.DisplayPath(normFile))
		totalParts := int((fSize + int64(segmentBytes) - 1) / int64(segmentBytes))
		if totalParts == 0 {
			totalParts = 1
		}

		effectiveBaseSubj := baseSubject
		if !obfuscateHeaders {
			effectiveBaseSubj = fName
		}

		nzbFiles[idx] = NZBFile{
			Poster:   effectivePoster,
			Date:     postTimestamp,
			Subject:  FormatSubject(effectiveBaseSubj, idx+1, len(filePaths), fName, 1, totalParts),
			Groups:   groupsList,
			Segments: make([]NZBSegment, totalParts),
		}

		f, err := os.Open(normFile)
		if err != nil {
			return nil, err
		}
		openHandles = append(openHandles, f)

		for p := 0; p < totalParts; p++ {
			offset := int64(p) * int64(segmentBytes)
			toRead := segmentBytes
			if offset+int64(toRead) > fSize {
				toRead = int(fSize - offset)
			}
			tasks = append(tasks, uploadTask{
				fileHandle:  f,
				fileIdx:     idx + 1,
				totalFiles:  len(filePaths),
				fileName:    fName,
				fileSize:    fSize,
				partNum:     p + 1,
				totalParts:  totalParts,
				offset:      offset,
				byteLen:     toRead,
				baseSubject: effectiveBaseSubj,
				newsgroup:   nntpGroupHeader,
				poster:      effectivePoster,
				resultSlot:  &nzbFiles[idx].Segments[p],
			})
		}
	}

	modeStr := "SSL/TLS"
	if !useTLS(cfg) {
		modeStr = "Unverschlüsselt (Klartext)"
	}
	stats := &uploadStats{
		start:      time.Now(),
		totalTasks: int64(len(tasks)),
		totalNetMB: float64(totalBytes) / (1024 * 1024),
	}
	logFn(fmt.Sprintf("Starte Streaming-Upload: %d Dateien | %d Segmente | %.2f MB | Port %d (%s) | %d Verbindungen...",
		len(filePaths), len(tasks), stats.totalNetMB, cfg.Port, modeStr, cfg.Connections))

	stopTicker := make(chan struct{})
	tickerDone := make(chan struct{})
	go func() {
		defer close(tickerDone)
		ticker := time.NewTicker(450 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if heartbeatFn != nil {
					heartbeatFn()
				}
				if progressFn != nil {
					progressFn(stats.snapshot(true, "Phase 3/3: NNTP-Upload"))
				}
			case <-stopTicker:
				return
			}
		}
	}()

	// Runden: Segmente, die wegen Verbindungslimits übrig bleiben, werden mit weniger Verbindungen
	// erneut verteilt. So kann kein Segment still verloren gehen.
	var firstErr atomic.Pointer[error]
	setErr := func(e error) { firstErr.CompareAndSwap(nil, &e) }
	pending := tasks
	conns := cfg.Connections
	for round := 1; len(pending) > 0 && firstErr.Load() == nil; round++ {
		peak := runUploadRound(ctx, cfg, conns, pending, stats, logFn, setErr, firstErr.Load)
		if firstErr.Load() != nil || ctx.Err() != nil {
			break
		}
		var missing []uploadTask
		for _, t := range pending {
			if t.resultSlot.MsgID == "" {
				missing = append(missing, t)
			}
		}
		if len(missing) == 0 {
			break
		}
		if len(missing) == len(pending) || round >= 5 {
			setErr(fmt.Errorf("%d Segmente konnten nicht hochgeladen werden (kein Fortschritt mit dem Provider)", len(missing)))
			break
		}
		if peak < 1 {
			peak = 1
		}
		conns = peak
		logFn(fmt.Sprintf("Hinweis: %d Segmente offen — neue Runde mit %d Verbindungen.", len(missing), conns))
		pending = missing
	}

	close(stopTicker)
	<-tickerDone

	if progressFn != nil {
		progressFn(stats.snapshot(false, "Phase 3/3: NNTP-Upload abgeschlossen"))
	}

	if e := firstErr.Load(); e != nil {
		return nil, *e
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("Upload abgebrochen: %w", err)
	}
	// Letzte Absicherung: eine NZB mit leeren Message-IDs darf nie entstehen.
	for _, t := range tasks {
		if t.resultSlot.MsgID == "" {
			return nil, fmt.Errorf("interner Fehler: Segment %d von %s fehlt", t.partNum, t.fileName)
		}
	}

	usedHeader := baseSubject
	if !obfuscateHeaders {
		usedHeader = filepath.Base(sysproc.DisplayPath(filePaths[0]))
	}

	return &UploadResult{
		NZBFiles:      nzbFiles,
		UsedHeader:    usedHeader,
		Groups:        groupsList,
		PostTimestamp: postTimestamp,
	}, nil
}

// runUploadRound verteilt queue auf bis zu conns Worker. Liefert die höchste Zahl gleichzeitig
// aktiver Verbindungen (für die nächste Runde, falls der Provider weniger erlaubt).
func runUploadRound(ctx context.Context, cfg ServerConfig, conns int, queue []uploadTask, stats *uploadStats,
	logFn func(string), setErr func(error), failed func() *error) int {

	var mu sync.Mutex
	idx := 0
	next := func() (uploadTask, bool) {
		mu.Lock()
		defer mu.Unlock()
		if idx >= len(queue) {
			return uploadTask{}, false
		}
		t := queue[idx]
		idx++
		return t, true
	}

	var active, peak atomic.Int64
	var limitWarned atomic.Bool
	segmentBytes := DefaultSegmentKB * 1024

	var wg sync.WaitGroup
	for w := 1; w <= conns; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					setErr(fmt.Errorf("Worker %d Panic: %v", workerID, r))
				}
			}()
			time.Sleep(time.Duration(workerID-1) * 5 * time.Millisecond)

			readBuf := make([]byte, segmentBytes)
			encBuf := make([]byte, 0, segmentBytes*2)

			var conn *nntpConn
			defer func() {
				if conn != nil {
					active.Add(-1)
					conn.close()
				}
			}()

			for {
				if failed() != nil || ctx.Err() != nil {
					return
				}
				task, ok := next()
				if !ok {
					return
				}

				slice := readBuf[:task.byteLen]
				if task.byteLen > 0 {
					if _, readErr := task.fileHandle.ReadAt(slice, task.offset); readErr != nil {
						setErr(fmt.Errorf("Worker %d Lesefehler bei %s (Part %d): %w", workerID, task.fileName, task.partNum, readErr))
						return
					}
				}

				encBuf = yenc.AppendSegment(encBuf, slice, task.fileName, task.fileSize, task.partNum, task.totalParts, task.offset)
				msgID := generateMsgID()
				subj := FormatSubject(task.baseSubject, task.fileIdx, task.totalFiles, task.fileName, task.partNum, task.totalParts)

				var postErr error
				var sentBrutto int64
				for attempt := 1; attempt <= cfg.MaxRetries; attempt++ {
					if ctx.Err() != nil {
						return
					}
					if conn == nil {
						var dialErr error
						conn, dialErr = dialNNTP(cfg)
						if dialErr != nil {
							postErr = dialErr
							if isConnectionLimitError(dialErr) && active.Load() > 0 {
								if limitWarned.CompareAndSwap(false, true) {
									logFn(fmt.Sprintf("Hinweis: Provider-Verbindungslimit erreicht (%v) — reduziere auf %d aktive Verbindungen.", dialErr, active.Load()))
								}
								// Task bleibt unerledigt (MsgID leer) und wird in der nächsten Runde
								// oder von einem noch laufenden Worker übernommen.
								mu.Lock()
								queue = append(queue, task)
								mu.Unlock()
								return
							}
							time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
							continue
						}
						if n := active.Add(1); n > peak.Load() {
							peak.Store(n)
						}
					}

					sentBrutto, postErr = conn.postArticle(msgID, task.poster, task.newsgroup, subj, encBuf)
					if postErr == nil {
						break
					}
					if errors.Is(postErr, errDuplicate) && attempt > 1 {
						// Der vorige Versuch ist angekommen, nur die Bestätigung fehlte.
						postErr = nil
						break
					}
					if errors.Is(postErr, errDuplicate) {
						// Beim ersten Versuch: Message-ID-Kollision (praktisch unmöglich) -> neue ID.
						msgID = generateMsgID()
						continue
					}
					active.Add(-1)
					conn.close()
					conn = nil
					time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
				}

				if postErr != nil {
					setErr(fmt.Errorf("Worker %d Fehler bei %s (Part %d): %w", workerID, task.fileName, task.partNum, postErr))
					return
				}

				*task.resultSlot = NZBSegment{
					Bytes:  task.byteLen,
					Number: task.partNum,
					MsgID:  msgID,
				}

				done := stats.completed.Add(1)
				stats.netBytes.Add(int64(task.byteLen))
				stats.grossBytes.Add(sentBrutto)

				if done%250 == 0 || done == stats.totalTasks {
					s := stats.snapshot(true, "")
					logFn(fmt.Sprintf("Fortschritt: %d/%d Segmente (%.1f%%) | Netto: %.2f MB/s | Brutto: %.2f MB/s (%.0f Mbit/s)",
						done, stats.totalTasks, s.Percent, s.NettoMBs, s.BruttoMBs, s.BruttoMbits))
				}
			}
		}(w)
	}
	wg.Wait()
	return int(peak.Load())
}
