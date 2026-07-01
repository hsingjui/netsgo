package server

func (s *Server) resolveTunnelProvisionAckWaiter(clientID string, generation uint64, resp provisionAckResult) bool {
	return s.tunnels.resolveProvisionAckWaiter(clientID, generation, resp)
}

func (s *Server) cancelTunnelProvisionAckWaiters(clientID string, generation uint64) {
	s.tunnels.cancelProvisionAckWaiters(clientID, generation)
	s.tunnels.cancelPreflightWaiters(clientID, generation)
}
