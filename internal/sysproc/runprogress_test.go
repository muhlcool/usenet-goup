//go:build !windows

package sysproc

import (
	"context"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunWithProgressReadsPercent(t *testing.T) {
	cmd := exec.Command("sh", "-c", "echo 10%; sleep 0.5; printf '55.5%%'; sleep 0.5; echo fertig >&2; exit 3")
	var maxPct atomic.Value
	maxPct.Store(0.0)
	stderr, err := RunWithProgress(cmd, func(p float64, _ time.Duration) {
		if p > maxPct.Load().(float64) {
			maxPct.Store(p)
		}
	})
	if err == nil || stderr != "fertig\n" {
		t.Fatalf("err=%v stderr=%q", err, stderr)
	}
	if got := maxPct.Load().(float64); got != 55.5 {
		t.Fatalf("Prozent: %v", got)
	}
}

func TestRunWithProgressCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := RunWithProgress(exec.CommandContext(ctx, "sleep", "30"), nil)
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("Abbruch wirkt nicht: err=%v nach %v", err, time.Since(start))
	}
}
