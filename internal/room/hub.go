package room

import "sync"

// Hub wakes readers waiting for new events in a room. Wait returns a channel
// that is closed on the next Notify for that room.
type Hub struct {
	mu    sync.Mutex
	rooms map[string]chan struct{}
}

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{rooms: map[string]chan struct{}{}} }

// Wait returns a channel closed when the room gets new events. Take it before
// reading the log, then select on it, so no event is missed.
func (h *Hub) Wait(roomID string) <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch, ok := h.rooms[roomID]
	if !ok {
		ch = make(chan struct{})
		h.rooms[roomID] = ch
	}
	return ch
}

// Notify wakes every reader waiting on the room.
func (h *Hub) Notify(roomID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.rooms[roomID]; ok {
		close(ch)
		delete(h.rooms, roomID)
	}
}
