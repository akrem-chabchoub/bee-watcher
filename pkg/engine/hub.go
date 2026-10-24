package engine

import (
	"encoding/json"
	"fmt"
	"sync"
)

// Frame is one pre-encoded server-sent event.
type Frame []byte

// encodeFrame renders an SSE frame. id may be 0 (no id line).
func encodeFrame(typ string, id uint64, v any) (Frame, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode %s frame: %w", typ, err)
	}
	return rawFrame(typ, id, data), nil
}

func rawFrame(typ string, id uint64, data []byte) Frame {
	b := make([]byte, 0, len(data)+64)
	b = append(b, "event: "...)
	b = append(b, typ...)
	b = append(b, '\n')
	if id > 0 {
		b = fmt.Appendf(b, "id: %d\n", id)
	}
	b = append(b, "data: "...)
	b = append(b, data...)
	b = append(b, "\n\n"...)
	return b
}

// Subscriber receives frames. Lagged is closed when the subscriber could not
// keep up and was dropped; the client reconnects with Last-Event-ID and is
// replayed from history.
type Subscriber struct {
	C      chan Frame
	Lagged chan struct{}
	once   sync.Once
}

// Hub fans frames out to subscribers without ever blocking the engine.
type Hub struct {
	mu   sync.Mutex
	subs map[*Subscriber]struct{}
}

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{subs: map[*Subscriber]struct{}{}} }

// Subscribe registers a subscriber.
func (h *Hub) Subscribe() *Subscriber {
	s := &Subscriber{C: make(chan Frame, 512), Lagged: make(chan struct{})}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s
}

// Unsubscribe removes a subscriber.
func (h *Hub) Unsubscribe(s *Subscriber) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
}

// Count returns the number of subscribers.
func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Publish sends f to every subscriber; slow subscribers are dropped.
func (h *Hub) Publish(f Frame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		select {
		case s.C <- f:
		default:
			s.once.Do(func() { close(s.Lagged) })
			delete(h.subs, s)
		}
	}
}
