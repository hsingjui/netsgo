package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"netsgo/pkg/protocol"
)

func (s *Server) waitForClientTunnelPreflight(client *ClientConn, req protocol.TunnelPreflightRequest) (protocol.TunnelPreflightResponse, error) {
	if req.RequestID == "" {
		return protocol.TunnelPreflightResponse{}, fmt.Errorf("preflight request missing request_id")
	}
	ch, err := s.beginClientTunnelPreflightWait(client, req)
	if err != nil {
		return protocol.TunnelPreflightResponse{}, err
	}

	timeout := s.tunnels.preflightTimeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp, ok := <-ch:
		if !ok {
			return protocol.TunnelPreflightResponse{}, errTunnelPreflightCancelled
		}
		if err := validateTunnelPreflightResponse(req, resp); err != nil {
			return protocol.TunnelPreflightResponse{}, err
		}
		return resp, nil
	case <-timer.C:
		s.tunnels.unregisterPreflightWaiter(client, req.RequestID)
		return protocol.TunnelPreflightResponse{}, errTunnelPreflightTimeout
	case <-s.done:
		s.tunnels.unregisterPreflightWaiter(client, req.RequestID)
		return protocol.TunnelPreflightResponse{}, errTunnelPreflightCancelled
	}
}

func (s *Server) beginClientTunnelPreflightWait(client *ClientConn, req protocol.TunnelPreflightRequest) (<-chan protocol.TunnelPreflightResponse, error) {
	if client == nil {
		return nil, errTunnelPreflightCancelled
	}
	releaseOwnerGate := func() {}
	if client.OwnerUserID != "" {
		_, release, err := s.acquireUserLifecycleRead(client.OwnerUserID, client.OwnerEpoch, true)
		if err != nil {
			return nil, err
		}
		releaseOwnerGate = release
	}
	defer releaseOwnerGate()

	s.clientTunnelMutationMu.Lock()
	defer s.clientTunnelMutationMu.Unlock()
	current, ok := s.clients.Load(client.ID)
	if !ok || current != client || !client.isLive() || !s.clientLifecycleCurrentLocked(client) {
		return nil, errTunnelPreflightCancelled
	}
	ch, err := s.tunnels.registerPreflightWaiter(client, req.RequestID)
	if err != nil {
		return nil, err
	}

	msg, err := protocol.NewMessage(protocol.MsgTypeTunnelPreflight, req)
	if err == nil {
		err = s.writeControlMessageBefore(client, msg, time.Now().Add(lifecycleControlWriteTimeout))
	}
	if err != nil {
		s.tunnels.unregisterPreflightWaiter(client, req.RequestID)
		return nil, err
	}
	return ch, nil
}

func validateTunnelPreflightResponse(req protocol.TunnelPreflightRequest, resp protocol.TunnelPreflightResponse) error {
	if resp.RequestID != req.RequestID {
		return fmt.Errorf("preflight response request_id mismatch")
	}
	if resp.TunnelID != req.TunnelID {
		return fmt.Errorf("preflight response tunnel_id mismatch")
	}
	if resp.Revision != req.Revision {
		return fmt.Errorf("preflight response revision mismatch")
	}
	if resp.Role != req.Role {
		return fmt.Errorf("preflight response role mismatch")
	}
	return nil
}

func (s *Server) preflightClientIngressForUser(ownerUserID string, req tunnelCreateRequestAPI, ingressConfig ingressEndpointConfigAPI, existingID string) error {
	if req.Topology != tunnelTopologyClientToClient || req.Ingress.Location != tunnelEndpointLocationClient {
		return nil
	}
	if req.Ingress.Type != tunnelIngressTypeTCPListen && req.Ingress.Type != tunnelIngressTypeUDPListen && req.Ingress.Type != tunnelIngressTypeSOCKS5Listen {
		return nil
	}
	client, ok := s.loadLiveClientForUser(ownerUserID, req.Ingress.ClientID)
	if !ok {
		return nil
	}

	revision := int64(1)
	if existingID != "" && s.store != nil {
		if current, ok, findErr := s.findStoredTunnelByID(existingID); findErr == nil && ok {
			revision = current.Revision + 1
			if sameClientIngressResource(current.Ingress, req.Ingress, current.Topology, ingressConfig) {
				return nil
			}
		}
	}

	requestID, err := generateUUIDE()
	if err != nil {
		return err
	}

	resp, err := s.waitForClientTunnelPreflight(client, protocol.TunnelPreflightRequest{
		RequestID: requestID,
		TunnelID:  existingID,
		Revision:  revision,
		Role:      protocol.DataStreamRoleIngress,
		Ingress:   preflightIngressEndpointSpec(req, ingressConfig),
	})
	if err != nil {
		code := protocol.TunnelMutationErrorCodeIngressPreflightRejected
		status := http.StatusBadGateway
		if errors.Is(err, errTunnelPreflightTimeout) {
			code = protocol.TunnelMutationErrorCodeIngressPreflightTimeout
			status = http.StatusGatewayTimeout
		}
		return newProxyRequestValidationError(fmt.Errorf("ingress preflight failed: %w", err), "ingress.config.port", code, status)
	}
	if !resp.Accepted {
		code := resp.Code
		if code == "" {
			code = protocol.TunnelMutationErrorCodeIngressPreflightRejected
		}
		message := resp.Message
		if message == "" {
			message = "ingress preflight rejected"
		}
		status := http.StatusBadRequest
		if code == protocol.TunnelMutationErrorCodeIngressPortInUse || code == protocol.TunnelMutationErrorCodeIngressResourceConflict {
			status = http.StatusConflict
		}
		return newProxyRequestValidationError(fmt.Errorf("ingress preflight rejected: %s", message), "ingress.config.port", code, status)
	}
	return nil
}

func (s *Server) loadLiveClientForUser(ownerUserID, clientID string) (*ClientConn, bool) {
	client, ok := s.loadLiveClient(clientID)
	if !ok {
		return nil, false
	}
	if ownerUserID == "" {
		return client, true
	}
	if client.OwnerUserID != "" {
		return client, client.OwnerUserID == ownerUserID
	}
	if s.auth.adminStore == nil {
		return nil, false
	}
	if _, ok := s.auth.adminStore.GetRegisteredClientForUser(ownerUserID, clientID); !ok {
		return nil, false
	}
	return client, true
}

func preflightIngressEndpointSpec(req tunnelCreateRequestAPI, cfg ingressEndpointConfigAPI) protocol.EndpointSpec {
	return protocol.EndpointSpec{
		Location: req.Ingress.Location,
		ClientID: req.Ingress.ClientID,
		Type:     req.Ingress.Type,
		Config:   preflightIngressConfigRaw(req.Ingress.Type, cfg),
	}
}

func preflightIngressConfigRaw(endpointType string, cfg ingressEndpointConfigAPI) json.RawMessage {
	if endpointType == tunnelIngressTypeSOCKS5Listen {
		return mustRawJSON(tcpListenConfigAPI{
			BindIP:             cfg.BindIP,
			Port:               cfg.Port,
			AllowedSourceCIDRs: cfg.AllowedSourceCIDRs,
		})
	}
	return normalizedIngressConfigRaw(endpointType, cfg)
}

func sameClientIngressResource(current EndpointSpec, next endpointSpecAPI, currentTopology string, nextConfig ingressEndpointConfigAPI) bool {
	if current.Location != protocol.EndpointLocationClient || next.Location != protocol.EndpointLocationClient {
		return false
	}
	if current.ClientID != next.ClientID || current.Type != next.Type {
		return false
	}
	if current.Type != protocol.IngressTypeTCPListen && current.Type != protocol.IngressTypeUDPListen && current.Type != protocol.IngressTypeSOCKS5Listen {
		return false
	}
	currentCfg, err := decodeListenEndpointConfig(endpointSpecAPI(current), currentTopology)
	if err != nil {
		return false
	}
	return currentCfg.BindIP == nextConfig.BindIP && currentCfg.Port == nextConfig.Port
}
