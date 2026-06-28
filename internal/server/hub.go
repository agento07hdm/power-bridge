package server

import "sync"

// wsHub tracks all active WebSocket connections and allows broadcasting
// raw JSON messages to all of them simultaneously.
//
// Each connection registers a buffered send channel. The hub writes into
// those channels without blocking; slow consumers simply miss a frame.
type wsHub struct {
	mu      sync.Mutex
	clients map[chan<- []byte]struct{}
}

func newWSHub() *wsHub {
	return &wsHub{clients: make(map[chan<- []byte]struct{})}
}

func (h *wsHub) register(ch chan<- []byte) {
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
}

func (h *wsHub) unregister(ch chan<- []byte) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
}

// broadcast sends data to every registered client channel.
// If a client's channel is full the frame is dropped for that client.
func (h *wsHub) broadcast(data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- data:
		default:
		}
	}
}

// len returns the number of currently connected clients.
func (h *wsHub) len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}
