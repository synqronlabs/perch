// Package hub fans out in-process mailbox change signals to IMAP sessions.
package hub

import "sync"

// Hub is a concurrency-safe set of mailbox watchers. A notification is a
// coalescing signal, not a queue: watchers re-read the mailbox when woken.
type Hub struct {
	mu   sync.Mutex
	next int
	subs map[int64]map[int]chan struct{}
}

// New returns an empty hub.
func New() *Hub {
	return &Hub{subs: make(map[int64]map[int]chan struct{})}
}

// Subscribe returns a wake channel and a function that removes the
// subscription. The channel is buffered with one element so publishers never
// block.
func (h *Hub) Subscribe(mailboxID int64) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.next++
	id := h.next
	if h.subs[mailboxID] == nil {
		h.subs[mailboxID] = make(map[int]chan struct{})
	}
	h.subs[mailboxID][id] = ch
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[mailboxID], id)
		if len(h.subs[mailboxID]) == 0 {
			delete(h.subs, mailboxID)
		}
		h.mu.Unlock()
	}
}

// Notify wakes every watcher of mailboxID.
func (h *Hub) Notify(mailboxID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs[mailboxID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
