package sysproc

import (
	"bytes"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// RunWithProgress startet cmd (rar, 7z, parpar, par2), liest Prozentangaben aus stdout und ruft
// tick(cliPct, elapsed) alle 400 ms auf, bis der Prozess endet. Liefert stderr für Fehlermeldungen.
// Ersetzt die früher doppelt vorhandene Logik in archiver und par2.
func RunWithProgress(cmd *exec.Cmd, tick func(cliPct float64, elapsed time.Duration)) (stderr string, err error) {
	HideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	// Kindprozesse, die stdout erben, dürfen Wait nach einem Abbruch nicht ewig blockieren.
	cmd.WaitDelay = 5 * time.Second

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("Startfehler %s: %w", cmd.Path, err)
	}

	var mu sync.Mutex
	var cliPct float64
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		ReadPercentStream(stdout, func(p float64) {
			mu.Lock()
			if p > cliPct {
				cliPct = p
			}
			mu.Unlock()
		})
	}()

	stop := make(chan struct{})
	tickDone := make(chan struct{})
	go func() {
		defer close(tickDone)
		ticker := time.NewTicker(400 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				mu.Lock()
				p := cliPct
				mu.Unlock()
				if tick != nil {
					tick(p, time.Since(start))
				}
			case <-stop:
				return
			}
		}
	}()

	// Laut os/exec-Doku erst stdout zu Ende lesen, dann Wait aufrufen.
	<-readDone
	err = cmd.Wait()
	close(stop)
	<-tickDone
	return stderrBuf.String(), err
}

// SpeedMBs rechnet Bytes und Dauer in MB/s um (0 bei Dauer 0).
func SpeedMBs(bytes int64, elapsed time.Duration) float64 {
	if s := elapsed.Seconds(); s > 0 {
		return float64(bytes) / (1024 * 1024) / s
	}
	return 0
}
