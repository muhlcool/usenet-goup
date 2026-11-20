package sysproc

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"
)

type CrashLogger struct {
	mu       sync.Mutex
	filePath string
	state    string
	lastTick time.Time
}

func NewCrashLogger(appDir string) *CrashLogger {
	return &CrashLogger{
		filePath: filepath.Join(appDir, "crash-freeze.log"),
		state:    "idle",
		lastTick: time.Now(),
	}
}

func (c *CrashLogger) Path() string {
	return c.filePath
}

func (c *CrashLogger) SetState(state string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = state
	c.lastTick = time.Now()
}

func (c *CrashLogger) Heartbeat() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastTick = time.Now()
}

func (c *CrashLogger) AppendEvent(kind string, detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastTick = time.Now()
	line := fmt.Sprintf("[%s] [%s] (state=%s) %s\n", time.Now().Format("2006-01-02 15:04:05.000"), kind, c.state, detail)
	f, err := os.OpenFile(c.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
}

func (c *CrashLogger) RecordPanic(source string, r any) {
	stack := string(debug.Stack())
	c.AppendEvent("PANIC:"+source, fmt.Sprintf("%v\n--- STACK ---\n%s", r, stack))
}

func (c *CrashLogger) StartFreezeWatchdog() {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		loggedStall := false
		for range ticker.C {
			c.mu.Lock()
			st := c.state
			elapsed := time.Since(c.lastTick)
			c.mu.Unlock()

			if st != "idle" && elapsed > 45*time.Second && !loggedStall {
				loggedStall = true
				c.AppendEvent("FREEZE-WARNING", fmt.Sprintf("Kein Fortschritt/Heartbeat seit %.1f Sekunden während '%s'", elapsed.Seconds(), st))
			} else if elapsed <= 45*time.Second {
				loggedStall = false
			}
		}
	}()
}
