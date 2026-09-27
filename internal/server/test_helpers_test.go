package server

import (
	"encoding/json"
	"io"
	"testing"
	"time"

	"netsgo/pkg/protocol"
)

func mustClose(t testing.TB, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
}

type readDeadliner interface {
	SetReadDeadline(time.Time) error
}

func mustSetReadDeadline(t testing.TB, conn readDeadliner, deadline time.Time) {
	t.Helper()
	if err := conn.SetReadDeadline(deadline); err != nil {
		t.Fatalf("set read deadline failed: %v", err)
	}
}

func mustDecodeJSON(t testing.TB, reader io.Reader, value any) error {
	t.Helper()
	return json.NewDecoder(reader).Decode(value)
}

func waitForTunnelChangedEvent(t testing.TB, ch <-chan SSEEvent, action, tunnelName string) map[string]any {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		select {
		case event := <-ch:
			if event.Type != "tunnel_changed" {
				continue
			}

			var payload map[string]any
			if err := json.Unmarshal([]byte(event.Data), &payload); err != nil {
				t.Fatalf("failed to parse tunnel_changed event: %v", err)
			}

			gotAction, _ := payload["action"].(string)
			if gotAction != action {
				continue
			}

			tunnelPayload, ok := payload["tunnel"].(map[string]any)
			if !ok {
				t.Fatalf("tunnel_changed.tunnel has invalid type: %#v", payload["tunnel"])
			}

			if tunnelName != "" {
				gotName, _ := tunnelPayload["name"].(string)
				if gotName != tunnelName {
					continue
				}
			}

			return tunnelPayload
		case <-time.After(20 * time.Millisecond):
		}
	}

	t.Fatalf("did not receive tunnel_changed event for action=%q tunnel=%q", action, tunnelName)
	return nil
}

func assertNoTunnelChangedEvent(t testing.TB, ch <-chan SSEEvent, duration time.Duration, tunnelName string) {
	t.Helper()

	timer := time.NewTimer(duration)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			if event.Type != "tunnel_changed" {
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(event.Data), &payload); err != nil {
				t.Fatalf("failed to parse tunnel_changed event: %v", err)
			}
			if tunnelName != "" {
				tunnelPayload, _ := payload["tunnel"].(map[string]any)
				gotName, _ := tunnelPayload["name"].(string)
				if gotName != tunnelName {
					continue
				}
			}
			t.Fatalf("unexpected tunnel_changed event: %s", event.Data)
		case <-timer.C:
			return
		}
	}
}

func testStoredServerExposeTCPTunnel(id, name, clientID string, localPort, remotePort int, createdAt time.Time) StoredTunnel {
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	return StoredTunnel{
		ProxyNewRequest: protocol.ProxyNewRequest{
			ID:         id,
			Name:       name,
			Type:       protocol.ProxyTypeTCP,
			LocalIP:    "127.0.0.1",
			LocalPort:  localPort,
			RemotePort: remotePort,
		},
		ClientID:        clientID,
		OwnerClientID:   clientID,
		Revision:        1,
		Topology:        TunnelTopologyServerExpose,
		DesiredState:    protocol.ProxyDesiredStateRunning,
		RuntimeState:    protocol.ProxyRuntimeStateExposed,
		TransportPolicy: protocol.TransportPolicyServerRelayOnly,
		ActualTransport: protocol.ActualTransportServerRelay,
		Ingress: EndpointSpec{
			Location: protocol.EndpointLocationServer,
			Type:     protocol.IngressTypeTCPListen,
			Config:   mustRawJSON(tcpListenConfigAPI{BindIP: "0.0.0.0", Port: remotePort, AllowedSourceCIDRs: allowAllSourceCIDRs()}),
		},
		Target: EndpointSpec{
			Location: protocol.EndpointLocationClient,
			ClientID: clientID,
			Type:     protocol.TargetTypeTCPService,
			Config:   mustRawJSON(serviceConfigAPI{IP: "127.0.0.1", Port: localPort}),
		},
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
}

func testRuntimeOnlyProxyTunnel(id, name, clientID string, localPort, remotePort int, createdAt time.Time) *ProxyTunnel {
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	return &ProxyTunnel{
		Config: protocol.ProxyConfig{
			ID:              id,
			Name:            name,
			Type:            protocol.ProxyTypeTCP,
			LocalIP:         "127.0.0.1",
			LocalPort:       localPort,
			RemotePort:      remotePort,
			ClientID:        clientID,
			OwnerClientID:   clientID,
			CreatedAt:       createdAt,
			DesiredState:    protocol.ProxyDesiredStateRunning,
			RuntimeState:    protocol.ProxyRuntimeStateExposed,
			TransportPolicy: protocol.TransportPolicyServerRelayOnly,
			ActualTransport: protocol.ActualTransportServerRelay,
		},
		done: make(chan struct{}),
	}
}

func assertTunnelBandwidthFields(t testing.TB, tunnelPayload map[string]any, ingress, egress int64) {
	t.Helper()

	if tunnelPayload["ingress_bps"] != float64(ingress) {
		t.Fatalf("ingress_bps: want %d, got %v", ingress, tunnelPayload["ingress_bps"])
	}
	if tunnelPayload["egress_bps"] != float64(egress) {
		t.Fatalf("egress_bps: want %d, got %v", egress, tunnelPayload["egress_bps"])
	}
}

func mustCreateSession(t testing.TB, store *AdminStore, userID, username, role, remoteAddr, userAgent string) *AdminSession {
	t.Helper()
	// Pre-unified-identity tests used a fixed "user-1" fixture ID. The
	// initialized user now receives a generated ID, so resolve the existing
	// fixture by username before creating a foreign-key-backed session.
	if user, err := store.GetUser(userID); err == nil {
		userID, username, role = user.ID, user.Username, user.Role
	} else {
		page, listErr := store.ListUsers(UserListOptions{Query: username, Limit: maxUserListLimit})
		if listErr != nil {
			t.Fatalf("list users while resolving session fixture: %v", listErr)
		}
		resolved := false
		for _, candidate := range page.Items {
			if candidate.Username == username {
				userID, username, role = candidate.ID, candidate.Username, candidate.Role
				resolved = true
				break
			}
		}
		if !resolved {
			t.Fatalf("resolve session fixture user %q (%q): %v", userID, username, err)
		}
	}
	session, err := store.CreateSession(userID, username, role, remoteAddr, userAgent)
	if err != nil {
		t.Fatalf("create session failed: %v", err)
	}
	return session
}

func mustDeleteSession(t testing.TB, store *AdminStore, sessionID string) {
	t.Helper()
	if err := store.DeleteSession(sessionID); err != nil {
		t.Fatalf("delete session failed: %v", err)
	}
}
