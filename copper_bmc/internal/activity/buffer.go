// Package activity keeps a ring buffer of agent events for the console and
// the control-plane Activity API (GET /v1/activity).
package activity

import (
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"
)

const defaultCapacity = 2000

// Event is one recorded agent activity line.
type Event struct {
	ID        uint64    `json:"id"`
	Time      time.Time `json:"time"`
	Component string    `json:"component"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}

// Buffer is a thread-safe ring of recent events. Every write also goes to the
// process console via the standard logger.
type Buffer struct {
	mu       sync.Mutex
	capacity int
	nextID   uint64
	events   []Event
	console  *log.Logger
}

// New creates a Buffer. capacity <= 0 uses the default.
func New(capacity int) *Buffer {
	if capacity <= 0 {
		capacity = defaultCapacity
	}
	return &Buffer{
		capacity: capacity,
		events:   make([]Event, 0, capacity),
		console:  log.Default(),
	}
}

// Info records an informational event.
func (b *Buffer) Info(component, format string, args ...any) {
	b.record("info", component, format, args...)
}

// Warn records a warning.
func (b *Buffer) Warn(component, format string, args ...any) {
	b.record("warn", component, format, args...)
}

// Error records an error-level event.
func (b *Buffer) Error(component, format string, args ...any) {
	b.record("error", component, format, args...)
}

func (b *Buffer) record(level, component, format string, args ...any) {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	component = strings.TrimSpace(component)
	if component == "" {
		component = "app"
	}

	b.mu.Lock()
	b.nextID++
	ev := Event{
		ID:        b.nextID,
		Time:      time.Now().UTC(),
		Component: component,
		Level:     level,
		Message:   msg,
	}
	if len(b.events) < b.capacity {
		b.events = append(b.events, ev)
	} else {
		copy(b.events, b.events[1:])
		b.events[b.capacity-1] = ev
	}
	b.mu.Unlock()

	b.console.Printf("[%s] %s", component, msg)
}

// Since returns events with ID > sinceID, newest-last, capped at limit.
// limit <= 0 means no extra cap beyond the buffer size.
func (b *Buffer) Since(sinceID uint64, limit int, component string) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()

	component = strings.TrimSpace(strings.ToLower(component))
	out := make([]Event, 0, len(b.events))
	for _, ev := range b.events {
		if ev.ID <= sinceID {
			continue
		}
		if component != "" && !strings.EqualFold(ev.Component, component) {
			continue
		}
		out = append(out, ev)
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// Snapshot returns the newest events (up to limit), oldest-first.
func (b *Buffer) Snapshot(limit int, component string) []Event {
	return b.Since(0, limit, component)
}

// Logger returns a *log.Logger whose lines are recorded under component.
// Suitable for copper_dhcp.Manager.SetLogger / Server.SetLogger.
func (b *Buffer) Logger(component string) *log.Logger {
	return log.New(&writer{buf: b, component: component}, "", 0)
}

type writer struct {
	buf       *Buffer
	component string
}

func (w *writer) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\r\n")
	if msg == "" {
		return len(p), nil
	}
	// copper_dhcp messages historically start with "dhcp "; component already labels them.
	if w.component == "dhcp" {
		msg = strings.TrimSpace(strings.TrimPrefix(msg, "dhcp"))
	}
	w.buf.Info(w.component, "%s", msg)
	return len(p), nil
}

var _ io.Writer = (*writer)(nil)
