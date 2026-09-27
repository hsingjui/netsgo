package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// SSEEvent represents a single SSE event.
type SSEEvent struct {
	Type string // "ready" | "snapshot" | "stats_update" | "traffic_realtime" | "client_online" | "client_offline" | "tunnel_changed" | "activity_event" | "user_list_changed"
	Data string // JSON string
	// ScopeUserID is transport metadata only. An empty value is an
	// administrator-global event and is never delivered to a user-scoped SSE
	// subscription.
	ScopeUserID string
}

// EventBus manages SSE subscriber registration and broadcasting.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[chan SSEEvent]struct{}
}

// NewEventBus creates a new event bus.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[chan SSEEvent]struct{}),
	}
}

// Subscribe registers a new subscriber and returns its event channel.
func (eb *EventBus) Subscribe() chan SSEEvent {
	ch := make(chan SSEEvent, 64) // Buffer 64 events to avoid blocking on slow consumers.
	eb.mu.Lock()
	eb.subscribers[ch] = struct{}{}
	eb.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber and closes its channel.
// If the channel was already closed and removed by Close(), this is a no-op.
func (eb *EventBus) Unsubscribe(ch chan SSEEvent) {
	eb.mu.Lock()
	_, exists := eb.subscribers[ch]
	if exists {
		delete(eb.subscribers, ch)
	}
	eb.mu.Unlock()
	if exists {
		close(ch)
	}
}

func (eb *EventBus) HasSubscribers() bool {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	return len(eb.subscribers) > 0
}

// Close shuts down the event bus and disconnects all subscribers. (P15)
func (eb *EventBus) Close() {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	for ch := range eb.subscribers {
		close(ch)
		delete(eb.subscribers, ch)
	}
}

// Publish broadcasts an event to all subscribers.
// It is non-blocking and drops the event if a subscriber channel is full.
func (eb *EventBus) Publish(event SSEEvent) {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	for ch := range eb.subscribers {
		select {
		case ch <- event:
		default:
			// Drop the event to avoid blocking if the channel is full.
			log.Printf("⚠️ SSE subscriber channel is full, dropping event: %s", event.Type)
		}
	}
}

// PublishJSON marshals data to JSON and broadcasts it.
func (eb *EventBus) PublishJSON(eventType string, data any) {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		log.Printf("⚠️ Failed to marshal SSE event: %v", err)
		return
	}
	eb.Publish(SSEEvent{Type: eventType, Data: string(jsonBytes)})
}

// PublishScopedJSON sends an event only to SSE streams whose selected user
// scope matches scopeUserID. Callers must use PublishJSON intentionally for
// true administrator-global events; an empty scope is rejected here so an
// ownership lookup failure cannot become a user-visible broadcast.
func (eb *EventBus) PublishScopedJSON(eventType, scopeUserID string, data any) {
	if scopeUserID == "" {
		log.Printf("⚠️ Refusing to publish user-scoped SSE event without a scope: %s", eventType)
		return
	}
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		log.Printf("⚠️ Failed to marshal SSE event: %v", err)
		return
	}
	eb.Publish(SSEEvent{Type: eventType, Data: string(jsonBytes), ScopeUserID: scopeUserID})
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, data any) error {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, jsonBytes); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

type sseReadScope struct {
	userID string
	global bool
}

func sseReadScopeForRequest(r *http.Request) (sseReadScope, error) {
	if resourceScope, ok := resourceScopeFromContext(r.Context()); ok {
		return sseReadScope{userID: resourceScope.OwnerUserID}, nil
	}
	if principal := GetPrincipalFromContext(r.Context()); principal != nil {
		if !principal.IsAdmin {
			return sseReadScope{}, fmt.Errorf("user SSE request is missing a resource scope")
		}
		return sseReadScope{global: true}, nil
	}
	// Direct unit tests intentionally call the handler without route
	// middleware. There is no browser-reachable unauthenticated route.
	return sseReadScope{global: true}, nil
}

func (scope sseReadScope) allows(event SSEEvent) bool {
	if scope.global {
		return event.Type == "activity_event" || event.Type == "user_list_changed"
	}
	return event.ScopeUserID == scope.userID
}

func (s *Server) activityCursorForSSEScope(scope sseReadScope) (int64, error) {
	if s.activityStore == nil {
		return 0, nil
	}
	if scope.global {
		return s.activityStore.MaxID()
	}
	return s.activityStore.MaxIDForUser(scope.userID)
}

func (s *Server) snapshotForSSEScope(scope sseReadScope) consoleSnapshot {
	if scope.global {
		return s.collectSnapshot()
	}
	return s.collectSnapshotForUser(scope.userID)
}

// handleSSE handles a globally-admin-scoped or explicitly user-scoped SSE
// connection. The selected scope is fixed for the life of the connection.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}
	release, accepted := s.beginLongLivedHandler()
	if !accepted {
		writeAPIError(w, http.StatusServiceUnavailable, "server_shutting_down", "server is shutting down")
		return
	}
	defer release()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	scope, err := sseReadScopeForRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "sse_scope_missing", "SSE resource scope is unavailable")
		return
	}
	var streamContext context.Context
	var releaseStream func()
	principal := GetPrincipalFromContext(r.Context())
	if !scope.global {
		resourceScope, scopeOK := resourceScopeFromContext(r.Context())
		if !scopeOK || resourceScope.OwnerUserID != scope.userID {
			writeAPIError(w, http.StatusInternalServerError, "sse_scope_missing", "SSE resource scope is unavailable")
			return
		}
		requireOperational := principal != nil && principal.UserID == scope.userID
		_, releaseGate, gateErr := s.acquireUserLifecycleRead(scope.userID, resourceScope.ExpectedEpoch, requireOperational)
		if gateErr != nil {
			writeResourceLifecycleError(w, gateErr)
			return
		}
		streamContext, releaseStream = s.registerSSEConnection(r)
		releaseGate()
	} else {
		streamContext, releaseStream = s.registerSSEConnection(r)
	}
	defer releaseStream()
	if err := streamContext.Err(); err != nil {
		return
	}

	ch := s.events.Subscribe()
	defer s.events.Unsubscribe(ch)

	log.Printf("📡 SSE client connected: %s", r.RemoteAddr)

	activityCursor, err := s.activityCursorForSSEScope(scope)
	if err != nil {
		log.Printf("⚠️ Failed to read activity cursor: %v", err)
		return
	}
	if err := writeSSEEvent(w, flusher, "ready", map[string]any{"activity_cursor": activityCursor}); err != nil {
		log.Printf("⚠️ Failed to write initial SSE handshake: %v", err)
		return
	}
	if !scope.global {
		if err := writeSSEEvent(w, flusher, "snapshot", s.snapshotForSSEScope(scope)); err != nil {
			log.Printf("⚠️ Failed to write initial SSE snapshot: %v", err)
			return
		}
	}

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	var snapshotC <-chan time.Time
	if !scope.global {
		snapshotTicker := time.NewTicker(10 * time.Second)
		defer snapshotTicker.Stop()
		snapshotC = snapshotTicker.C
	}

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			if !scope.allows(event) {
				continue
			}
			eventData := event.Data
			if principal != nil && !principal.IsAdmin && event.Type == "activity_event" {
				var item ActivityItem
				if err := json.Unmarshal([]byte(eventData), &item); err != nil {
					log.Printf("⚠️ Refusing to send malformed activity SSE event: %v", err)
					continue
				}
				redactAdminActivityActorNetwork(&item)
				raw, err := json.Marshal(item)
				if err != nil {
					log.Printf("⚠️ Failed to redact activity SSE event: %v", err)
					continue
				}
				eventData = string(raw)
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, eventData); err != nil {
				log.Printf("⚠️ Failed to write SSE event: %v", err)
				return
			}
			flusher.Flush()
		case <-snapshotC:
			if err := writeSSEEvent(w, flusher, "snapshot", s.snapshotForSSEScope(scope)); err != nil {
				log.Printf("⚠️ Failed to write SSE snapshot: %v", err)
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				log.Printf("⚠️ Failed to write SSE heartbeat: %v", err)
				return
			}
			flusher.Flush()
		case <-streamContext.Done():
			log.Printf("📡 SSE client disconnected: %s", r.RemoteAddr)
			return
		}
	}
}
