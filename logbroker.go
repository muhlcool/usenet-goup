package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"usenet-goup/internal/sysproc"
)

type sseEvent struct {
	event string
	data  string
}

type LogBroker struct {
	mu      sync.Mutex
	clients map[chan sseEvent]bool
	crash   *sysproc.CrashLogger
}

func NewLogBroker(crash *sysproc.CrashLogger) *LogBroker {
	return &LogBroker{
		clients: make(map[chan sseEvent]bool),
		crash:   crash,
	}
}

func (b *LogBroker) Log(msg string) {
	log.Println(msg)
	if b.crash != nil {
		b.crash.Heartbeat()
		if strings.Contains(msg, "FEHLER") || strings.Contains(msg, "Abbruch") || strings.Contains(msg, "Hinweis:") {
			b.crash.AppendEvent("LOG", msg)
		}
	}
	b.broadcast("", msg)
}

func (b *LogBroker) Progress(stats sysproc.ProgressStats) {
	if b.crash != nil {
		b.crash.Heartbeat()
	}
	if raw, err := json.Marshal(stats); err == nil {
		b.broadcast("progress", string(raw))
	}
}

func (b *LogBroker) broadcast(event string, data string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.clients {
		select {
		case ch <- sseEvent{event: event, data: data}:
		default:
		}
	}
}

func (b *LogBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan sseEvent, 64)
	b.mu.Lock()
	b.clients[ch] = true
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.clients, ch)
		b.mu.Unlock()
	}()

	flusher, _ := w.(http.Flusher)
	for {
		select {
		case ev := <-ch:
			if ev.event != "" {
				fmt.Fprintf(w, "event: %s\n", ev.event)
			}
			// Mehrzeilige Meldungen: jede Zeile braucht ein eigenes "data:"-Feld (SSE-Format).
			for _, line := range strings.Split(strings.ReplaceAll(ev.data, "\r\n", "\n"), "\n") {
				fmt.Fprintf(w, "data: %s\n", line)
			}
			fmt.Fprint(w, "\n")
			if flusher != nil {
				flusher.Flush()
			}
		case <-r.Context().Done():
			return
		}
	}
}
