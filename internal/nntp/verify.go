package nntp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// statBatch: so viele STAT-Befehle werden am Stück gesendet (Pipelining, RFC 3977 §3.5).
const statBatch = 100

// VerifyResult fasst die Prüfung nach dem Upload zusammen.
type VerifyResult struct {
	Checked int
	Missing []string // Message-IDs, die der Server nicht kennt
}

// MessageIDs sammelt alle Message-IDs einer NZB.
func MessageIDs(files []NZBFile) []string {
	var ids []string
	for _, f := range files {
		for _, s := range f.Segments {
			ids = append(ids, s.MsgID)
		}
	}
	return ids
}

// VerifyArticles fragt per STAT nach, ob der Server alle Artikel kennt. Fehlende werden nach
// einer Wartezeit (Server-Verarbeitung) noch einmal geprüft.
func VerifyArticles(ctx context.Context, cfg ServerConfig, ids []string, recheckAfter time.Duration, logFn func(string)) (VerifyResult, error) {
	cfg = cfg.withDefaults()
	res := VerifyResult{Checked: len(ids)}
	missing, err := statAll(ctx, cfg, ids)
	if err != nil {
		return res, err
	}
	if len(missing) > 0 && recheckAfter > 0 {
		logFn(fmt.Sprintf("Prüfung: %d von %d Teilen noch nicht auf dem Server, erneute Prüfung in %s ...", len(missing), len(ids), recheckAfter))
		t := time.NewTimer(recheckAfter)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return res, ctx.Err()
		}
		if missing, err = statAll(ctx, cfg, missing); err != nil {
			return res, err
		}
	}
	res.Missing = missing
	return res, nil
}

func statAll(ctx context.Context, cfg ServerConfig, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	conns := min(cfg.Connections, 8, (len(ids)+statBatch-1)/statBatch)
	var (
		mu      sync.Mutex
		missing []string
		next    atomic.Int64
		wg      sync.WaitGroup
		errOnce sync.Once
		firstEr error
	)
	setErr := func(e error) { errOnce.Do(func() { firstEr = e }) }
	for w := 0; w < conns; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := dialNNTP(cfg)
			if err != nil {
				setErr(fmt.Errorf("Prüfung: Verbindung fehlgeschlagen: %w", err))
				return
			}
			defer conn.close()
			for ctx.Err() == nil {
				start := int(next.Add(statBatch)) - statBatch
				if start >= len(ids) {
					return
				}
				batch := ids[start:min(start+statBatch, len(ids))]
				miss, err := conn.statBatch(batch)
				if err != nil {
					setErr(err)
					return
				}
				mu.Lock()
				missing = append(missing, miss...)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstEr != nil {
		return nil, firstEr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return missing, nil
}

// statBatch sendet alle STAT-Befehle eines Blocks und liest danach die Antworten.
func (c *nntpConn) statBatch(ids []string) ([]string, error) {
	_ = c.conn.SetDeadline(time.Now().Add(60 * time.Second))
	for _, id := range ids {
		if strings.ContainsAny(id, "<>\r\n ") {
			return nil, fmt.Errorf("Prüfung: ungültige Message-ID %q", id)
		}
		if _, err := fmt.Fprintf(c.writer, "STAT <%s>\r\n", id); err != nil {
			return nil, err
		}
	}
	if err := c.writer.Flush(); err != nil {
		return nil, err
	}
	var missing []string
	for _, id := range ids {
		code, line, err := c.readResponse()
		if err != nil {
			return nil, err
		}
		switch code {
		case 223:
		case 430:
			missing = append(missing, id)
		default:
			return nil, fmt.Errorf("Prüfung: unerwartete Serverantwort auf STAT (%d): %s", code, line)
		}
	}
	return missing, nil
}
