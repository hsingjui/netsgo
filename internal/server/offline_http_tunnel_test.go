package server

import (
	"testing"
	"time"

	"netsgo/pkg/protocol"
)

func registerOfflineHTTPTestClient(t *testing.T, s *Server, hostname string) string {
	t.Helper()

	record, err := s.auth.adminStore.GetOrCreateClient(
		"install-"+hostname,
		protocol.ClientInfo{
			Hostname: hostname,
			OS:       "linux",
			Arch:     "amd64",
			Version:  "test",
		},
		"127.0.0.1:12345",
	)
	if err != nil {
		t.Fatalf("failed to register offline client: %v", err)
	}
	return record.ID
}

func TestLoadOfflineTunnelBySelectorPrefersNameOverID(t *testing.T) {
	s, _, _, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()

	clientID := registerOfflineHTTPTestClient(t, s, "offline-selector")
	seedStoredTunnel(t, s, clientID, protocol.ProxyNewRequest{
		ID:         "name-tunnel-id",
		Name:       "id-of-other",
		Type:       protocol.ProxyTypeTCP,
		RemotePort: 18081,
	}, protocol.ProxyStatusStopped)
	seedStoredTunnel(t, s, clientID, protocol.ProxyNewRequest{
		ID:         "id-of-other",
		Name:       "other",
		Type:       protocol.ProxyTypeTCP,
		RemotePort: 18082,
	}, protocol.ProxyStatusStopped)

	stored, err := s.loadOfflineTunnelBySelector(clientID, "id-of-other")
	if err != nil {
		t.Fatalf("loadOfflineTunnelBySelector failed: %v", err)
	}
	if stored.Name != "id-of-other" || stored.ID != "name-tunnel-id" {
		t.Fatalf("selector should prefer exact name matches over ID matches, got name=%q id=%q", stored.Name, stored.ID)
	}
}

func TestLifecycle_ClientDisconnect_DoesNotRewriteStoreState(t *testing.T) {
	s, ts, cleanup := setupWSTestNoConn(t)
	defer cleanup()

	s.store = newTestTunnelStore(t)

	wsConn, authResp := connectAndAuth(t, ts, "disconnect-http-store")
	defer mustClose(t, wsConn)

	deadline := time.Now().Add(2 * time.Second)
	var liveClient *ClientConn
	for time.Now().Before(deadline) {
		if value, ok := s.clients.Load(authResp.ClientID); ok {
			client := value.(*ClientConn)
			if client.getState() == clientStateLive {
				liveClient = client
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if liveClient == nil {
		t.Fatal("timed out waiting for live client")
		return
	}

	liveClient.proxyMu.Lock()
	liveClient.proxies["active-http"] = &ProxyTunnel{
		Config: protocol.ProxyConfig{
			Name:         "active-http",
			Type:         protocol.ProxyTypeHTTP,
			LocalIP:      "127.0.0.1",
			LocalPort:    3000,
			Domain:       "keep-active.example.com",
			ClientID:     authResp.ClientID,
			DesiredState: protocol.ProxyDesiredStateRunning,
			RuntimeState: protocol.ProxyRuntimeStateExposed,
		},
		done: make(chan struct{}),
	}
	liveClient.proxyMu.Unlock()

	mustAddStableTunnel(t, s.store, StoredTunnel{
		ProxyNewRequest: protocol.ProxyNewRequest{
			Name:      "active-http",
			Type:      protocol.ProxyTypeHTTP,
			LocalIP:   "127.0.0.1",
			LocalPort: 3000,
			Domain:    "keep-active.example.com",
		},
		DesiredState: protocol.ProxyDesiredStateRunning,
		RuntimeState: protocol.ProxyRuntimeStateExposed,
		ClientID:     authResp.ClientID,
		Hostname:     "disconnect-http-store",
	})

	if !s.invalidateLogicalSessionIfCurrent(authResp.ClientID, liveClient.generation, "test_disconnect") {
		t.Fatal("disconnect should successfully invalidate the current logical session")
	}

	stored, exists := s.store.GetTunnel(authResp.ClientID, "active-http")
	if !exists {
		t.Fatal("HTTP tunnel record in the store should remain after disconnect")
	}
	if stored.DesiredState != protocol.ProxyDesiredStateRunning || stored.RuntimeState != protocol.ProxyRuntimeStateExposed {
		t.Fatalf("client disconnect should not rewrite the store target state, got %s/%s", stored.DesiredState, stored.RuntimeState)
	}
	if stored.Domain != "keep-active.example.com" {
		t.Fatalf("Domain should be preserved after client disconnect, got %s", stored.Domain)
	}
}
