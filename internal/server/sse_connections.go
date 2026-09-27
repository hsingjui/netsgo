package server

import (
	"context"
	"log"
	"net/http"
	"sync"
)

// sseConnectionRegistry owns cancellation for authenticated browser event
// streams. It deliberately lives above EventBus: EventBus only transports
// domain events and must not depend on HTTP sessions.
type sseConnectionRegistry struct {
	mu        sync.Mutex
	nextID    uint64
	entries   map[uint64]sseConnection
	byUser    map[string]map[uint64]struct{}
	bySession map[string]map[uint64]struct{}
}

type sseConnection struct {
	userID    string
	sessionID string
	cancel    context.CancelFunc
}

func newSSEConnectionRegistry() *sseConnectionRegistry {
	return &sseConnectionRegistry{
		entries:   make(map[uint64]sseConnection),
		byUser:    make(map[string]map[uint64]struct{}),
		bySession: make(map[string]map[uint64]struct{}),
	}
}

func (r *sseConnectionRegistry) register(userID, sessionID string, cancel context.CancelFunc) func() {
	if r == nil || userID == "" || sessionID == "" || cancel == nil {
		return func() {}
	}
	r.mu.Lock()
	r.nextID++
	id := r.nextID
	r.entries[id] = sseConnection{userID: userID, sessionID: sessionID, cancel: cancel}
	addSSERegistryIndex(r.byUser, userID, id)
	addSSERegistryIndex(r.bySession, sessionID, id)
	r.mu.Unlock()
	return func() { r.unregister(id) }
}

func addSSERegistryIndex(index map[string]map[uint64]struct{}, key string, id uint64) {
	entries := index[key]
	if entries == nil {
		entries = make(map[uint64]struct{})
		index[key] = entries
	}
	entries[id] = struct{}{}
}

func (r *sseConnectionRegistry) unregister(id uint64) {
	if r == nil || id == 0 {
		return
	}
	r.mu.Lock()
	r.removeLocked(id)
	r.mu.Unlock()
}

func (r *sseConnectionRegistry) removeLocked(id uint64) (sseConnection, bool) {
	entry, ok := r.entries[id]
	if !ok {
		return sseConnection{}, false
	}
	delete(r.entries, id)
	removeSSERegistryIndex(r.byUser, entry.userID, id)
	removeSSERegistryIndex(r.bySession, entry.sessionID, id)
	return entry, true
}

func removeSSERegistryIndex(index map[string]map[uint64]struct{}, key string, id uint64) {
	entries := index[key]
	delete(entries, id)
	if len(entries) == 0 {
		delete(index, key)
	}
}

func (r *sseConnectionRegistry) cancelUser(userID string) int {
	if r == nil || userID == "" {
		return 0
	}
	return r.cancelIndexed(r.byUser, userID)
}

func (r *sseConnectionRegistry) cancelSession(sessionID string) int {
	if r == nil || sessionID == "" {
		return 0
	}
	return r.cancelIndexed(r.bySession, sessionID)
}

func (r *sseConnectionRegistry) cancelAll() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	entries := make([]sseConnection, 0, len(r.entries))
	for id := range r.entries {
		if entry, ok := r.removeLocked(id); ok {
			entries = append(entries, entry)
		}
	}
	r.mu.Unlock()
	for _, entry := range entries {
		entry.cancel()
	}
	return len(entries)
}

func (r *sseConnectionRegistry) cancelIndexed(index map[string]map[uint64]struct{}, key string) int {
	r.mu.Lock()
	ids := index[key]
	entries := make([]sseConnection, 0, len(ids))
	for id := range ids {
		if entry, ok := r.removeLocked(id); ok {
			entries = append(entries, entry)
		}
	}
	r.mu.Unlock()
	for _, entry := range entries {
		entry.cancel()
	}
	return len(entries)
}

func (s *Server) getSSEConnectionRegistry() *sseConnectionRegistry {
	if s == nil {
		return nil
	}
	s.sseConnectionMu.Lock()
	defer s.sseConnectionMu.Unlock()
	if s.sseConnections == nil {
		s.sseConnections = newSSEConnectionRegistry()
	}
	return s.sseConnections
}

func (s *Server) registerSSEConnection(r *http.Request) (context.Context, func()) {
	principal := GetPrincipalFromContext(r.Context())
	if principal == nil || principal.UserID == "" || principal.SessionID == "" {
		return r.Context(), func() {}
	}
	streamContext, cancel := context.WithCancel(r.Context())
	unregister := s.getSSEConnectionRegistry().register(principal.UserID, principal.SessionID, cancel)
	return streamContext, func() {
		unregister()
		cancel()
	}
}

func (s *Server) cancelSSEForUser(userID, reason string) {
	if count := s.getSSEConnectionRegistry().cancelUser(userID); count > 0 {
		log.Printf("📡 Closed %d SSE connection(s) for a user: %s", count, reason)
	}
}

func (s *Server) cancelSSEForSession(sessionID, reason string) {
	if count := s.getSSEConnectionRegistry().cancelSession(sessionID); count > 0 {
		log.Printf("📡 Closed %d SSE connection(s) for a session: %s", count, reason)
	}
}

func (s *Server) cancelAllSSE(reason string) {
	if count := s.getSSEConnectionRegistry().cancelAll(); count > 0 {
		log.Printf("📡 Closed %d SSE connection(s): %s", count, reason)
	}
}
