package main

import (
	"context"
	"sync"
	"time"
)

// jobControl: höchstens ein Job gleichzeitig, abbrechbar über /api/cancel und Beenden.
type jobControl struct {
	run    sync.Mutex
	mu     sync.Mutex
	cancel context.CancelFunc
}

func (j *jobControl) start() (context.Context, func(), bool) {
	if !j.run.TryLock() {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	j.mu.Lock()
	j.cancel = cancel
	j.mu.Unlock()
	return ctx, func() {
		j.mu.Lock()
		j.cancel = nil
		j.mu.Unlock()
		cancel()
		j.run.Unlock()
	}, true
}

func (j *jobControl) abort() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cancel != nil {
		j.cancel()
		return true
	}
	return false
}

// waitIdle wartet, bis ein abgebrochener Job seine Kindprozesse beendet hat.
func (j *jobControl) waitIdle(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if j.run.TryLock() {
			j.run.Unlock()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
