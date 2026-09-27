package server

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"netsgo/pkg/protocol"
)

func newClientActivityServer(t *testing.T) *Server {
	t.Helper()
	s := New(0)
	path := filepath.Join(t.TempDir(), serverDBFileName)
	db, err := openServerDB(path)
	if err != nil {
		t.Fatalf("openServerDB() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s.serverDB = db
	s.activityStore = newActivityStoreWithDB(path, db, false)
	return s
}

func TestClientActivityOnlineBeforeOfflineAndDeduplicated(t *testing.T) {
	s := newClientActivityServer(t)
	client := &ClientConn{
		ID: "client-activity", InstallID: "install", Info: protocol.ClientInfo{Hostname: "activity-host"},
		generation: 1, state: clientStatePendingData, proxies: make(map[string]*ProxyTunnel),
	}
	s.clients.Store(client.ID, client)
	if !s.promotePendingToLiveIfCurrent(client) {
		t.Fatal("promotion should succeed")
	}
	if s.promotePendingToLiveIfCurrent(client) {
		t.Fatal("duplicate promotion should fail")
	}
	if !s.invalidateLogicalSessionIfCurrent(client.ID, client.generation, "data_session_closed") {
		t.Fatal("invalidation should succeed")
	}
	if s.invalidateLogicalSessionIfCurrent(client.ID, client.generation, "control_loop_exit") {
		t.Fatal("duplicate invalidation should fail")
	}

	page, err := s.activityStore.Query(ActivityQuery{Scope: ActivityScopeClient, ScopeID: client.ID, Limit: 50})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("activity item count = %d, want 2", len(page.Items))
	}
	if got := []string{page.Items[1].Action, page.Items[0].Action}; !reflect.DeepEqual(got, []string{"online", "offline"}) {
		t.Fatalf("lifecycle actions = %#v", got)
	}
	if page.Items[0].Severity != ActivitySeverityWarning {
		t.Fatalf("unexpected disconnect severity = %q", page.Items[0].Severity)
	}
}

func TestClientActivityUsesRegisteredDisplayName(t *testing.T) {
	s := newClientActivityServer(t)
	now := formatTime(time.Now().UTC())
	if _, err := s.serverDB.Exec(`INSERT INTO registered_clients
		(id, install_id, display_name, hostname, created_at, last_seen)
		VALUES (?, ?, ?, ?, ?, ?)`, "client-display", "install-display", "Office Mac", "stored-host", now, now); err != nil {
		t.Fatalf("insert registered client: %v", err)
	}
	client := &ClientConn{
		ID: "client-display", InstallID: "install-display", Info: protocol.ClientInfo{Hostname: "wire-host"},
		generation: 1, state: clientStatePendingData, proxies: make(map[string]*ProxyTunnel),
	}
	id := s.appendClientLifecycle(client, "online", clientDisconnectCause{})
	if id == 0 {
		t.Fatal("appendClientLifecycle() did not persist an event")
	}
	item, err := s.activityStore.GetByID(id)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	var payload activityPayloadV1
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.SummaryArgs.ClientName != "Office Mac" || item.Actor.Name != "Office Mac" {
		t.Fatalf("client lifecycle names = payload %q actor %q", payload.SummaryArgs.ClientName, item.Actor.Name)
	}
	if len(item.Clients) != 1 || item.Clients[0].DisplayName != "Office Mac" || item.Clients[0].Hostname != "stored-host" {
		t.Fatalf("client lifecycle snapshot = %+v", item.Clients)
	}
}

func TestNormalizeClientDisconnectCauseClassifiesUserVisibleOfflineReasons(t *testing.T) {
	tests := []struct {
		reason string
		code   string
	}{
		{reason: "server_shutdown", code: "server_shutdown"},
		{reason: "normal_closure", code: "normal_closure"},
		{reason: "pending_data_timeout", code: "timeout"},
		{reason: "user_disabled", code: "user_disabled"},
		{reason: "data_session_closed", code: "data_channel_closed"},
		{reason: "control_loop_exit", code: "transport_error"},
		{reason: "replaced", code: "replaced"},
		{reason: "unrecognized", code: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			if got := normalizeClientDisconnectCause(tt.reason).ReasonCode; got != tt.code {
				t.Fatalf("normalizeClientDisconnectCause(%q) = %q, want %q", tt.reason, got, tt.code)
			}
		})
	}
}

func TestClientActivityStaleGenerationProducesNoEvent(t *testing.T) {
	s := newClientActivityServer(t)
	current := &ClientConn{ID: "client-stale", generation: 2, state: clientStatePendingData, proxies: make(map[string]*ProxyTunnel)}
	stale := &ClientConn{ID: current.ID, generation: 1, state: clientStatePendingData, proxies: make(map[string]*ProxyTunnel)}
	s.clients.Store(current.ID, current)
	if s.promotePendingToLiveIfCurrent(stale) {
		t.Fatal("stale generation promotion should fail")
	}
	if s.invalidateLogicalSessionIfCurrent(stale.ID, stale.generation, "control_loop_exit") {
		t.Fatal("stale generation invalidation should fail")
	}
	maxID, err := s.activityStore.MaxID()
	if err != nil {
		t.Fatalf("MaxID() error = %v", err)
	}
	if maxID != 0 {
		t.Fatalf("stale generation produced activity ID %d", maxID)
	}
}

func TestClientActivityBootIDScopesGenerationDedupe(t *testing.T) {
	s := newClientActivityServer(t)
	for index, bootID := range []string{"boot-one", "boot-two"} {
		s.activityBootID = bootID
		client := &ClientConn{ID: "client-restart", generation: 1, state: clientStatePendingData, proxies: make(map[string]*ProxyTunnel)}
		s.clients.Store(client.ID, client)
		if !s.promotePendingToLiveIfCurrent(client) {
			t.Fatalf("promotion %d should succeed", index)
		}
		if !s.invalidateLogicalSessionIfCurrent(client.ID, client.generation, "normal_closure") {
			t.Fatalf("invalidation %d should succeed", index)
		}
	}
	page, err := s.activityStore.Query(ActivityQuery{Scope: ActivityScopeClient, ScopeID: "client-restart", Limit: 50})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(page.Items) != 4 {
		t.Fatalf("cross-boot activity count = %d, want 4", len(page.Items))
	}
}

func TestClientActivityPromotionAndInvalidationRacePreservesOrder(t *testing.T) {
	s := newClientActivityServer(t)
	for i := 0; i < 50; i++ {
		client := &ClientConn{ID: "client-race-" + fmt.Sprint(i), generation: 1, state: clientStatePendingData, proxies: make(map[string]*ProxyTunnel)}
		s.clients.Store(client.ID, client)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			s.promotePendingToLiveIfCurrent(client)
		}()
		go func() {
			defer wg.Done()
			<-start
			s.invalidateLogicalSessionIfCurrent(client.ID, client.generation, "data_session_closed")
		}()
		close(start)
		wg.Wait()

		page, err := s.activityStore.Query(ActivityQuery{Scope: ActivityScopeClient, ScopeID: client.ID, Limit: 10})
		if err != nil {
			t.Fatalf("iteration %d Query() error = %v", i, err)
		}
		switch len(page.Items) {
		case 0:
			// Invalidation won while PendingData; no lifecycle edge is observable.
		case 2:
			if page.Items[1].Action != "online" || page.Items[0].Action != "offline" || page.Items[1].ID >= page.Items[0].ID {
				t.Fatalf("iteration %d lifecycle order = %+v", i, page.Items)
			}
		default:
			t.Fatalf("iteration %d partial lifecycle events = %+v", i, page.Items)
		}
	}
}
