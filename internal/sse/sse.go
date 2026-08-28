// Package sse provides a tiny pub/sub broker and Server-Sent Events helpers.
//
// The broker fans server-side events (build progress, deployment state) out
// to browser connections; container log tails write to their connection
// directly since each tail is its own docker stream.
package sse

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Event is one SSE event. Name maps to the SSE "event:" field; Data is sent
// line-by-line in "data:" fields.
type Event struct {
	Name string
	Data string
}

// Broker fans events out by topic. Subscribers that fall behind have events
// dropped (logs are best-effort; a reconnect replays the tail).
type Broker struct {
	mu   sync.Mutex
	subs map[string]map[chan Event]struct{}
}

const subscriberBuffer = 256

// NewBroker builds an empty broker.
func NewBroker() *Broker {
	return &Broker{subs: make(map[string]map[chan Event]struct{})}
}

// Subscribe registers a channel for a topic. cancel unregisters and closes
// the channel.
func (b *Broker) Subscribe(topic string) (<-chan Event, func()) {
	ch := make(chan Event, subscriberBuffer)
	b.mu.Lock()
	if b.subs[topic] == nil {
		b.subs[topic] = make(map[chan Event]struct{})
	}
	b.subs[topic][ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs[topic], ch)
			if len(b.subs[topic]) == 0 {
				delete(b.subs, topic)
			}
			b.mu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

// Publish sends an event to every subscriber of a topic. Slow subscribers
// have the event dropped rather than blocking the publisher.
func (b *Broker) Publish(topic string, ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[topic] {
		select {
		case ch <- ev:
		default:
		}
	}
}

// SetHeaders prepares a response for event streaming.
func SetHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}

// WriteEvent formats and writes one event, flushing immediately.
func WriteEvent(w http.ResponseWriter, ev Event) error {
	if ev.Name != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", ev.Name); err != nil {
			return err
		}
	}
	// Split data on newlines: each line becomes its own data: field.
	start := 0
	for i := 0; i <= len(ev.Data); i++ {
		if i == len(ev.Data) || ev.Data[i] == '\n' {
			if _, err := fmt.Fprintf(w, "data: %s\n", ev.Data[start:i]); err != nil {
				return err
			}
			start = i + 1
		}
	}
	if _, err := fmt.Fprint(w, "\n"); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// Heartbeat keeps the connection alive (and detectable as broken) by sending
// SSE comments until ctx is done.
func Heartbeat(ctxDone <-chan struct{}, w http.ResponseWriter, flusher http.Flusher) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctxDone:
			return
		case <-t.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
