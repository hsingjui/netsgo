package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"netsgo/pkg/protocol"
)

func dialControlWSForRuntimeGate(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/control"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial control websocket: %v", err)
	}
	return conn
}

func testRuntimeGateClientInfo(hostname string) protocol.ClientInfo {
	caps := protocol.DefaultClientCapabilities()
	return protocol.ClientInfo{
		Hostname:     hostname,
		OS:           "linux",
		Arch:         "amd64",
		Version:      "test",
		Capabilities: &caps,
	}
}

func TestControlAuthMissingStoreFailsClosed(t *testing.T) {
	s := New(0)
	ts := httptest.NewServer(s.newHTTPMux())
	defer ts.Close()

	conn := dialControlWSForRuntimeGate(t, ts)
	defer mustClose(t, conn)

	var message protocol.Message
	if err := conn.ReadJSON(&message); err != nil {
		t.Fatalf("read missing-store authentication response: %v", err)
	}
	var response protocol.AuthResponse
	if err := message.ParsePayload(&response); err != nil {
		t.Fatalf("parse missing-store authentication response: %v", err)
	}
	if response.Success || response.Code != protocol.AuthCodeServerUninitialized || !response.Retryable || response.ClearToken {
		t.Fatalf("missing-store response = %+v", response)
	}

	count := 0
	s.RangeClients(func(_ string, _ *ClientConn) bool {
		count++
		return true
	})
	if count != 0 {
		t.Fatalf("missing store must not publish a client session, got %d", count)
	}
}

func TestControlAuthSetsResolvedOwnerUserID(t *testing.T) {
	s, conn, _, cleanup := setupWSTest(t)
	defer cleanup()

	response := doAuthWithInstallID(t, conn, "owned-host", "owned-install", "test-key")
	if !response.Success {
		t.Fatalf("key authentication failed: %+v", response)
	}
	owner, err := s.auth.adminStore.ValidateAdminPassword("admin", "password123")
	if err != nil {
		t.Fatalf("load expected owner: %v", err)
	}
	value, ok := s.clients.Load(response.ClientID)
	if !ok {
		t.Fatal("authenticated client was not published")
	}
	client := value.(*ClientConn)
	if client.OwnerUserID == "" || client.OwnerUserID != owner.ID {
		t.Fatalf("ClientConn.OwnerUserID = %q, want %q", client.OwnerUserID, owner.ID)
	}
}

func TestControlAuthDisabledOwnerRejectsKeyAndToken(t *testing.T) {
	s, ts, cleanup := setupWSTestNoConn(t)
	defer cleanup()

	admin, err := s.auth.adminStore.ValidateAdminPassword("admin", "password123")
	if err != nil {
		t.Fatalf("load administrator: %v", err)
	}
	owner, err := s.auth.adminStore.CreateUser("runtime-gate-owner", "Password123")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	const rawKey = "runtime-gate-key"
	if _, err := s.auth.adminStore.AddAPIKeyForUser(owner.ID, "runtime-gate", rawKey, []string{"connect"}, nil); err != nil {
		t.Fatalf("create owner key: %v", err)
	}

	exchange, err := s.auth.adminStore.RegisterClientAndExchangeToken(rawKey, "runtime-gate-install", testRuntimeGateClientInfo("runtime-gate-host"), "192.0.2.9")
	if err != nil {
		t.Fatalf("exchange token while owner is active: %v", err)
	}
	if exchange.Client.OwnerUserID != owner.ID {
		t.Fatalf("registered client owner = %q, want %q", exchange.Client.OwnerUserID, owner.ID)
	}
	if _, _, err := s.auth.adminStore.SetUserStatus(admin.ID, owner.ID, UserStatusDisabled); err != nil {
		t.Fatalf("disable owner: %v", err)
	}

	for _, test := range []struct {
		name  string
		key   string
		token string
	}{
		{name: "key", key: rawKey},
		{name: "token", token: exchange.Token},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn := dialControlWSForRuntimeGate(t, ts)
			defer mustClose(t, conn)

			response := doAuthRequest(t, conn, "runtime-gate-host", "runtime-gate-install", test.key, test.token)
			if response.Success || response.Code != protocol.AuthCodeUserDisabled || !response.Retryable || response.ClearToken {
				t.Fatalf("disabled owner response = %+v", response)
			}
		})
	}

	if _, ok := s.clients.Load(exchange.Client.ID); ok {
		t.Fatal("disabled owner must not publish a control session")
	}
}

func TestControlAuthMissingClientOwnerFailsClosed(t *testing.T) {
	s, ts, cleanup := setupWSTestNoConn(t)
	defer cleanup()

	exchange, err := s.auth.adminStore.RegisterClientAndExchangeToken("test-key", "missing-owner-install", testRuntimeGateClientInfo("missing-owner-host"), "192.0.2.10")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if _, err := s.auth.adminStore.db.Exec(`DELETE FROM registered_clients WHERE id = ?`, exchange.Client.ID); err != nil {
		t.Fatalf("remove client owner fixture: %v", err)
	}

	conn := dialControlWSForRuntimeGate(t, ts)
	defer mustClose(t, conn)
	response := doTokenAuthWithInstallID(t, conn, "missing-owner-host", "missing-owner-install", exchange.Token)
	if response.Success || response.Code != protocol.AuthCodeServerUninitialized || !response.Retryable || response.ClearToken {
		t.Fatalf("missing owner response = %+v", response)
	}
	if _, ok := s.clients.Load(exchange.Client.ID); ok {
		t.Fatal("missing owner must not publish a control session")
	}
}

func TestInvalidateLogicalSessionsForUserReusesSessionInvalidation(t *testing.T) {
	s := New(0)
	s.clients.Store("owned", &ClientConn{ID: "owned", OwnerUserID: "user-a", generation: 1, state: clientStatePendingData, proxies: make(map[string]*ProxyTunnel)})
	s.clients.Store("other", &ClientConn{ID: "other", OwnerUserID: "user-b", generation: 1, state: clientStatePendingData, proxies: make(map[string]*ProxyTunnel)})

	if got := s.invalidateLogicalSessionsForUser("user-a", "user_disabled"); got != 1 {
		t.Fatalf("invalidated sessions = %d, want 1", got)
	}
	if _, ok := s.clients.Load("owned"); ok {
		t.Fatal("owned session should be removed")
	}
	if _, ok := s.clients.Load("other"); !ok {
		t.Fatal("other user's session should remain")
	}
}

func TestDisabledTunnelOwnerProjectsOfflineOwnerIssue(t *testing.T) {
	s, _, _, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()

	admin, err := s.auth.adminStore.ValidateAdminPassword("admin", "password123")
	if err != nil {
		t.Fatalf("load administrator: %v", err)
	}
	owner, err := s.auth.adminStore.CreateUser("disabled-tunnel-owner", "Password123")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	client, err := s.auth.adminStore.GetOrCreateClientForUser(owner.ID, "disabled-tunnel-owner-install", testRuntimeGateClientInfo("disabled-tunnel-owner-host"), "127.0.0.1:1234")
	if err != nil {
		t.Fatalf("register owner client: %v", err)
	}
	seedStoredTunnel(t, s, client.ID, protocol.ProxyNewRequest{Name: "disabled-owner-tunnel", Type: protocol.ProxyTypeTCP}, protocol.ProxyStatusPending)
	stored, err := s.store.GetTunnelE(client.ID, "disabled-owner-tunnel")
	if err != nil {
		t.Fatalf("load stored tunnel: %v", err)
	}
	if _, changed, err := s.auth.adminStore.SetUserStatus(admin.ID, owner.ID, UserStatusDisabled); err != nil || !changed {
		t.Fatalf("disable owner = (%v, %v)", changed, err)
	}

	spec := specFromStoredTunnel(stored, s)
	if spec.RuntimeState != protocol.ProxyRuntimeStateOffline {
		t.Fatalf("runtime state = %q, want offline", spec.RuntimeState)
	}
	if len(spec.Issues) != 1 || spec.Issues[0].Code != protocol.TunnelIssueCodeOwnerDisabled || spec.Issues[0].Severity != "warning" {
		t.Fatalf("owner-disabled issues = %+v", spec.Issues)
	}
}
