package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"netsgo/pkg/protocol"
)

var (
	errStoredTunnelClientNotFound = errors.New("tunnel client not found")
	errStoredTunnelNotFound       = errors.New("tunnel not found")
)

func tunnelProvisionErrorMessage(err error) string {
	var rejected *tunnelProvisionRejectedError
	switch {
	case errors.As(err, &rejected):
		if rejected.message != "" {
			return rejected.message
		}
		return "client rejected tunnel provisioning"
	case errors.Is(err, errTunnelProvisionAckTimeout):
		return "timed out waiting for client provisioning ack"
	case errors.Is(err, errTunnelProvisionAckCancelled):
		return "logical session is no longer valid"
	default:
		return err.Error()
	}
}

func (s *Server) mustGetTunnel(client *ClientConn, name string) (*ProxyTunnel, error) {
	client.proxyMu.RLock()
	defer client.proxyMu.RUnlock()

	tunnel, ok := client.proxies[name]
	if !ok {
		return nil, fmt.Errorf("tunnel %q not found", name)
	}
	return tunnel, nil
}

func findTunnelBySelector(client *ClientConn, selector string) (string, *ProxyTunnel, bool) {
	client.proxyMu.RLock()
	defer client.proxyMu.RUnlock()

	if tunnel, ok := client.proxies[selector]; ok {
		return selector, tunnel, true
	}
	for name, tunnel := range client.proxies {
		if tunnel.Config.ID == selector {
			return name, tunnel, true
		}
	}
	return "", nil, false
}

func (s *Server) persistTunnelStates(clientID, name, desiredState, runtimeState, errMsg string) error {
	if s.store == nil {
		return nil
	}
	return s.store.UpdateStates(clientID, name, desiredState, runtimeState, errMsg)
}

func (s *Server) upsertTunnelPlaceholder(client *ClientConn, req protocol.ProxyNewRequest, desiredState, runtimeState, errMsg string, createdAt time.Time) protocol.ProxyConfig {
	return s.upsertTunnelPlaceholderWithRevision(client, req, 1, desiredState, runtimeState, errMsg, createdAt)
}

func (s *Server) upsertTunnelPlaceholderWithRevision(client *ClientConn, req protocol.ProxyNewRequest, revision int64, desiredState, runtimeState, errMsg string, createdAt time.Time) protocol.ProxyConfig {
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	if revision <= 0 {
		revision = 1
	}
	config := protocol.ProxyConfig{
		ID:                req.ID,
		Name:              req.Name,
		Revision:          revision,
		Type:              req.Type,
		LocalIP:           req.LocalIP,
		LocalPort:         req.LocalPort,
		RemotePort:        req.RemotePort,
		BindIP:            normalizeServerBindIP(req.BindIP),
		Domain:            req.Domain,
		ClientID:          client.ID,
		BandwidthSettings: req.BandwidthSettings,
		CreatedAt:         createdAt.UTC(),
	}
	setProxyConfigStates(&config, desiredState, runtimeState, errMsg)
	client.proxyMu.Lock()
	if client.proxies == nil {
		client.proxies = make(map[string]*ProxyTunnel)
	}
	tunnel := &ProxyTunnel{
		Config: config,
		limits: newDirectionalBandwidthRuntime(req.BandwidthSettings, realBandwidthClock{}),
		done:   make(chan struct{}),
	}
	initializeTunnelRuntimeFromState(tunnel, client.ID, time.Now())
	client.proxies[req.Name] = tunnel
	client.proxyMu.Unlock()
	return config
}

func (s *Server) failRestoredTunnelAfterReady(client *ClientConn, stored StoredTunnel, message string) {
	s.removeTunnelRuntime(client, stored.Name)
	_ = s.notifyClientProxyClose(client, stored.Name, "provision_failed")
	req := stored.ProxyNewRequest
	req.BindIP = tunnelIngressBindIP(stored)
	config := s.upsertTunnelPlaceholderWithRevision(client, req, stored.Revision, protocol.ProxyDesiredStateRunning, protocol.ProxyRuntimeStateError, message, stored.CreatedAt)
	_ = s.persistTunnelStates(client.ID, stored.Name, protocol.ProxyDesiredStateRunning, protocol.ProxyRuntimeStateError, message)
	s.emitTunnelChanged(client.ID, config, "error")
}

func storedTunnelToProxyConfig(stored StoredTunnel) protocol.ProxyConfig {
	config := protocol.ProxyConfig{
		ID:                stored.ID,
		Name:              stored.Name,
		Revision:          stored.Revision,
		Type:              stored.Type,
		LocalIP:           stored.LocalIP,
		LocalPort:         stored.LocalPort,
		RemotePort:        stored.RemotePort,
		BindIP:            tunnelIngressBindIP(stored),
		Domain:            stored.Domain,
		ClientID:          stored.ClientID,
		Topology:          stored.Topology,
		OwnerClientID:     stored.OwnerClientID,
		BandwidthSettings: stored.BandwidthSettings,
		CreatedAt:         stored.CreatedAt,
	}
	if stored.TransportPolicy != "" {
		config.TransportPolicy = stored.TransportPolicy
	}
	if stored.ActualTransport != "" {
		config.ActualTransport = stored.ActualTransport
	}
	if stored.Ingress.Location != "" || stored.Ingress.Type != "" {
		ingress := endpointSpecProtocolFromStored(stored.Ingress)
		config.Ingress = &ingress
	}
	if stored.Target.Location != "" || stored.Target.Type != "" || stored.Target.ClientID != "" {
		target := endpointSpecProtocolFromStored(stored.Target)
		config.Target = &target
	}
	if stored.P2P.State != "" || stored.P2P.Error != "" || stored.P2P.SessionID != "" {
		config.P2P = &protocol.P2PState{
			State:     stored.P2P.State,
			Error:     stored.P2P.Error,
			SessionID: stored.P2P.SessionID,
		}
	}
	setProxyConfigStates(&config, stored.DesiredState, stored.RuntimeState, stored.Error)
	return config
}

func (s *Server) loadOfflineTunnelBySelector(clientID, selector string) (StoredTunnel, error) {
	if s.auth.adminStore == nil {
		return StoredTunnel{}, errStoredTunnelClientNotFound
	}
	if _, ok := s.auth.adminStore.GetRegisteredClient(clientID); !ok {
		return StoredTunnel{}, errStoredTunnelClientNotFound
	}
	if s.store == nil {
		return StoredTunnel{}, errStoredTunnelNotFound
	}

	stored, err := s.store.GetTunnelE(clientID, selector)
	if err == nil {
		return stored, nil
	}
	if !errors.Is(err, ErrTunnelNotFound) {
		return StoredTunnel{}, err
	}

	stored, err = s.store.GetTunnelByIDE(clientID, selector)
	if errors.Is(err, ErrTunnelNotFound) {
		return StoredTunnel{}, errStoredTunnelNotFound
	}
	if err != nil {
		return StoredTunnel{}, err
	}
	return stored, nil
}

func (s *Server) stopOfflineTunnel(clientID, name string) (protocol.ProxyConfig, error) {
	stored, err := s.loadOfflineTunnelBySelector(clientID, name)
	if err != nil {
		return protocol.ProxyConfig{}, err
	}
	name = stored.Name
	if err := s.store.UpdateStates(clientID, name, protocol.ProxyDesiredStateStopped, protocol.ProxyRuntimeStateIdle, ""); err != nil {
		return protocol.ProxyConfig{}, err
	}

	updated, err := s.store.GetTunnelE(clientID, name)
	if errors.Is(err, ErrTunnelNotFound) {
		return protocol.ProxyConfig{}, fmt.Errorf("tunnel %q not found", name)
	}
	if err != nil {
		return protocol.ProxyConfig{}, fmt.Errorf("failed to reload tunnel %q: %w", name, err)
	}

	config := storedTunnelToProxyConfig(updated)
	s.emitTunnelChanged(clientID, config, "stopped")
	return config, nil
}

func (s *Server) notifyClientProxyClose(client *ClientConn, name, reason string) error {
	message, err := protocol.NewMessage(protocol.MsgTypeProxyClose, protocol.ProxyCloseRequest{
		Name:   name,
		Reason: reason,
	})
	if err != nil {
		return err
	}
	return s.writeControlMessage(client, message)
}

func (s *Server) writeControlMessage(client *ClientConn, message *protocol.Message) error {
	if client.generation != 0 && !s.isCurrentLive(client.ID, client.generation) {
		return fmt.Errorf("client %s is not in the live session", client.ID)
	}

	if err := client.writeJSON(message); err != nil {
		return fmt.Errorf("failed to write control message: %w", err)
	}
	return nil
}

func (s *Server) emitTunnelChanged(clientID string, tunnel protocol.ProxyConfig, action string) {
	_, clientOnline := s.loadLiveClient(clientID)
	tunnel = proxyConfigForClientView(tunnel, clientOnline)
	log.Printf("🔎 tunnel_changed action=%s client_id=%s tunnel_id=%s name=%s desired=%s runtime=%s online=%v",
		action, clientID, tunnel.ID, tunnel.Name, tunnel.DesiredState, tunnel.RuntimeState, clientOnline)
	payload := map[string]any{
		"client_id": clientID,
		"action":    action,
		"tunnel":    tunnel,
	}
	s.events.PublishJSON("tunnel_changed", payload)
}

func encodeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("⚠️ Failed to encode JSON response: %v", err)
	}
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	encodeJSON(w, status, apiErrorResponse{
		Error:   message,
		Message: message,
		Code:    code,
	})
}

// affectedTunnel describes a tunnel affected by a port allowlist change.
type affectedTunnel struct {
	ClientID     string `json:"client_id"`
	Hostname     string `json:"hostname"`
	DisplayName  string `json:"display_name,omitempty"`
	TunnelName   string `json:"tunnel_name"`
	RemotePort   int    `json:"remote_port"`
	DesiredState string `json:"desired_state"`
	RuntimeState string `json:"runtime_state"`
	Error        string `json:"error,omitempty"`
}

// isPortInRanges checks whether a port is within the given allowlist ranges.
func isPortInRanges(port int, ranges []PortRange) bool {
	for _, pr := range ranges {
		if port >= pr.Start && port <= pr.End {
			return true
		}
	}
	return false
}

// findTunnelsAffectedByPortChange finds all tunnels affected by the new port allowlist rules.
// It scans both runtime tunnels and persisted tunnels for offline clients.
func (s *Server) findTunnelsAffectedByPortChange(newPorts []PortRange) ([]affectedTunnel, error) {
	// An empty allowlist means no port restriction, so nothing is affected.
	if len(newPorts) == 0 {
		return []affectedTunnel{}, nil
	}

	affected := []affectedTunnel{}
	seen := map[string]bool{} // key: "clientID:tunnelName"

	// 1) Scan runtime tunnels for online clients.
	s.clients.Range(func(_, value any) bool {
		client := value.(*ClientConn)
		client.RangeProxies(func(name string, tunnel *ProxyTunnel) bool {
			if tunnel.Config.RemotePort != 0 && !isPortInRanges(tunnel.Config.RemotePort, newPorts) {
				// Do not report tunnels already in error state again.
				if tunnel.Config.RuntimeState == protocol.ProxyRuntimeStateError {
					return true
				}
				key := client.ID + ":" + name
				seen[key] = true
				// Try to load display_name.
				displayName := ""
				if s.auth.adminStore != nil {
					if reg, ok := s.auth.adminStore.GetRegisteredClient(client.ID); ok {
						displayName = reg.DisplayName
					}
				}
				affected = append(affected, affectedTunnel{
					ClientID:     client.ID,
					Hostname:     client.GetInfo().Hostname,
					DisplayName:  displayName,
					TunnelName:   name,
					RemotePort:   tunnel.Config.RemotePort,
					DesiredState: tunnel.Config.DesiredState,
					RuntimeState: tunnel.Config.RuntimeState,
					Error:        tunnel.Config.Error,
				})
			}
			return true
		})
		return true
	})

	// 2) Scan persisted tunnels, including tunnels for offline clients.
	if s.store != nil {
		allStored, err := s.store.GetAllTunnels()
		if err != nil {
			return nil, fmt.Errorf("load persisted tunnels for port allocation: %w", err)
		} else {
			for _, st := range allStored {
				if st.RemotePort == 0 {
					continue
				}
				if st.RuntimeState == protocol.ProxyRuntimeStateError {
					continue
				}
				key := st.ClientID + ":" + st.Name
				if seen[key] {
					continue // Already counted from runtime state.
				}
				if !isPortInRanges(st.RemotePort, newPorts) {
					hostname := st.Hostname
					displayName := ""
					// Try to get a more detailed hostname and display name from adminStore.
					if s.auth.adminStore != nil && st.ClientID != "" {
						if reg, ok := s.auth.adminStore.GetRegisteredClient(st.ClientID); ok {
							hostname = reg.Info.Hostname
							displayName = reg.DisplayName
						}
					}
					affected = append(affected, affectedTunnel{
						ClientID:     st.ClientID,
						Hostname:     hostname,
						DisplayName:  displayName,
						TunnelName:   st.Name,
						RemotePort:   st.RemotePort,
						DesiredState: st.DesiredState,
						RuntimeState: st.RuntimeState,
						Error:        st.Error,
					})
				}
			}
		}
	}

	return affected, nil
}

// markTunnelsPortNotAllowed marks tunnels affected by a port allowlist change as error.
func (s *Server) markTunnelsPortNotAllowed(affected []affectedTunnel) {
	for _, a := range affected {
		errMsg := fmt.Sprintf("port %d is not allowed", a.RemotePort)
		hasEventConfig := false
		eventConfig := protocol.ProxyConfig{
			Name:         a.TunnelName,
			RemotePort:   a.RemotePort,
			ClientID:     a.ClientID,
			DesiredState: protocol.ProxyDesiredStateRunning,
			RuntimeState: protocol.ProxyRuntimeStateError,
			Error:        errMsg,
		}

		// Update runtime state for online clients.
		if value, ok := s.clients.Load(a.ClientID); ok {
			client := value.(*ClientConn)
			client.proxyMu.Lock()
			if tunnel, exists := client.proxies[a.TunnelName]; exists {
				if isTunnelExposed(tunnel.Config) {
					closeTunnelRuntimeResources(tunnel)
					go func(c *ClientConn, name string) {
						_ = s.notifyClientProxyClose(c, name, "port_not_allowed")
					}(client, a.TunnelName)
				}
				setProxyConfigStates(&tunnel.Config, protocol.ProxyDesiredStateRunning, protocol.ProxyRuntimeStateError, errMsg)
				eventConfig = tunnel.Config
				hasEventConfig = true
			}
			client.proxyMu.Unlock()
		}

		if !hasEventConfig && s.store != nil {
			stored, err := s.store.GetTunnelE(a.ClientID, a.TunnelName)
			if err == nil {
				eventConfig = storedTunnelToProxyConfig(stored)
				setProxyConfigStates(&eventConfig, protocol.ProxyDesiredStateRunning, protocol.ProxyRuntimeStateError, errMsg)
				hasEventConfig = true
			} else if !errors.Is(err, ErrTunnelNotFound) {
				log.Printf("⚠️ failed to load persisted tunnel %s/%s for event fallback: %v", a.ClientID, a.TunnelName, err)
			}
		}

		s.emitTunnelChanged(a.ClientID, eventConfig, "port_not_allowed")

		_ = s.persistTunnelStates(a.ClientID, a.TunnelName, protocol.ProxyDesiredStateRunning, protocol.ProxyRuntimeStateError, errMsg)

		log.Printf("⚠️ Tunnel %s (port %d, client %s) was marked as error due to a port allowlist change",
			a.TunnelName, a.RemotePort, a.ClientID)
	}
}
