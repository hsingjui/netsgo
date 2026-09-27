package server

import (
	"fmt"
	"log"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

type clientState string

const (
	clientStatePendingData clientState = "PendingData"
	clientStateLive        clientState = "Live"
	clientStateClosing     clientState = "Closing"
)

func (c *ClientConn) getState() clientState {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.state
}

func (c *ClientConn) isLive() bool {
	return c.getState() == clientStateLive
}

func (s *Server) nextClientGeneration() uint64 {
	return s.sessions.nextClientGeneration()
}

func (s *Server) startPendingDataTimer(client *ClientConn) {
	timeout := s.sessions.pendingDataTimeout
	if timeout <= 0 {
		return
	}

	timer := time.AfterFunc(timeout, func() {
		s.invalidateLogicalSessionIfCurrent(client.ID, client.generation, "pending_data_timeout")
	})

	client.stateMu.Lock()
	if client.state == clientStatePendingData && client.pendingTimer == nil {
		client.pendingTimer = timer
		client.stateMu.Unlock()
		return
	}
	client.stateMu.Unlock()
	timer.Stop()
}

func (s *Server) isCurrentGeneration(clientID string, generation uint64) bool {
	value, ok := s.clients.Load(clientID)
	if !ok {
		return false
	}
	client := value.(*ClientConn)
	return client.generation == generation
}

func (s *Server) isCurrentLive(clientID string, generation uint64) bool {
	value, ok := s.clients.Load(clientID)
	if !ok {
		return false
	}
	client := value.(*ClientConn)
	if client.generation != generation {
		return false
	}
	return client.isLive()
}

func (s *Server) waitForCurrentDataReady(client *ClientConn, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if !s.isCurrentGeneration(client.ID, client.generation) {
			return fmt.Errorf("logical session has been invalidated")
		}
		if client.getState() == clientStateClosing {
			return fmt.Errorf("logical session is closing")
		}

		client.dataMu.RLock()
		session := client.dataSession
		dataReady := session != nil && !session.IsClosed()
		client.dataMu.RUnlock()
		if dataReady {
			return nil
		}

		if timeout <= 0 || time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for data channel to become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (s *Server) loadLiveClient(clientID string) (*ClientConn, bool) {
	value, ok := s.clients.Load(clientID)
	if !ok {
		return nil, false
	}
	client := value.(*ClientConn)
	if !client.isLive() {
		return nil, false
	}
	return client, true
}

func (s *Server) promotePendingToLiveIfCurrent(client *ClientConn) bool {
	releaseGate := func() {}
	if client != nil && client.OwnerUserID != "" {
		_, release, err := s.acquireUserLifecycleRead(client.OwnerUserID, client.OwnerEpoch, true)
		if err != nil {
			return false
		}
		releaseGate = release
	}
	defer releaseGate()

	s.clientTunnelMutationMu.Lock()
	client.lifecycleMu.Lock()

	value, ok := s.clients.Load(client.ID)
	if !ok || value != client {
		client.lifecycleMu.Unlock()
		s.clientTunnelMutationMu.Unlock()
		return false
	}
	client.stateMu.Lock()
	if client.state != clientStatePendingData {
		client.stateMu.Unlock()
		client.lifecycleMu.Unlock()
		s.clientTunnelMutationMu.Unlock()
		return false
	}
	if client.pendingTimer != nil {
		client.pendingTimer.Stop()
		client.pendingTimer = nil
	}
	client.state = clientStateLive
	client.stateMu.Unlock()
	s.clientTunnelMutationMu.Unlock()

	activityID := s.appendClientLifecycle(client, "online", clientDisconnectCause{ReasonCode: "normal_closure", Expected: true})
	s.publishActivityID(activityID)
	client.lifecycleMu.Unlock()
	return true
}

// attachDataSessionIfCurrent performs the only publication of a data yamux
// session. The user read gate is held only for this final mutation, never while
// waiting for the WebSocket handshake acknowledgement.
func (s *Server) attachDataSessionIfCurrent(client *ClientConn, session *yamux.Session) (oldSession *yamux.Session, promoted, attached bool) {
	if client == nil || session == nil {
		return nil, false, false
	}
	releaseGate := func() {}
	if client.OwnerUserID != "" {
		_, release, err := s.acquireUserLifecycleRead(client.OwnerUserID, client.OwnerEpoch, true)
		if err != nil {
			return nil, false, false
		}
		releaseGate = release
	}
	defer releaseGate()

	s.clientTunnelMutationMu.Lock()
	client.lifecycleMu.Lock()
	value, ok := s.clients.Load(client.ID)
	if !ok || value != client || client.generation == 0 || !s.clientLifecycleCurrentLocked(client) {
		client.lifecycleMu.Unlock()
		s.clientTunnelMutationMu.Unlock()
		return nil, false, false
	}

	client.stateMu.Lock()
	state := client.state
	if state == clientStateClosing {
		client.stateMu.Unlock()
		client.lifecycleMu.Unlock()
		s.clientTunnelMutationMu.Unlock()
		return nil, false, false
	}
	if state == clientStatePendingData {
		if client.pendingTimer != nil {
			client.pendingTimer.Stop()
			client.pendingTimer = nil
		}
		client.state = clientStateLive
		promoted = true
	}
	client.stateMu.Unlock()

	client.dataMu.Lock()
	oldSession = client.dataSession
	client.dataSession = session
	client.dataMu.Unlock()
	s.clientTunnelMutationMu.Unlock()

	if promoted {
		activityID := s.appendClientLifecycle(client, "online", clientDisconnectCause{ReasonCode: "normal_closure", Expected: true})
		s.publishActivityID(activityID)
		s.events.PublishScopedJSON("client_online", client.OwnerUserID, map[string]any{
			"client_id": client.ID,
			"info":      client.GetInfo(),
		})
	}
	client.lifecycleMu.Unlock()
	return oldSession, promoted, true
}

func (s *Server) invalidateLogicalSessionIfCurrent(clientID string, generation uint64, reason string) bool {
	s.clientTunnelMutationMu.Lock()
	defer s.clientTunnelMutationMu.Unlock()
	return s.invalidateLogicalSessionIfCurrentLocked(clientID, generation, normalizeClientDisconnectCause(reason))
}
func (s *Server) invalidateLogicalSessionIfCurrentWithCause(clientID string, generation uint64, cause clientDisconnectCause) bool {
	s.clientTunnelMutationMu.Lock()
	defer s.clientTunnelMutationMu.Unlock()
	return s.invalidateLogicalSessionIfCurrentLocked(clientID, generation, cause)
}

// invalidateLogicalSessionsForUser closes every currently published logical
// Client session owned by userID. It deliberately reuses the single-session
// invalidation path so control WebSockets, data yamux sessions, P2P state, and
// runtime allocations converge together instead of becoming separate cleanup
// mechanisms.
func (s *Server) invalidateLogicalSessionsForUser(userID, reason string) int {
	if userID == "" {
		return 0
	}

	type sessionRef struct {
		clientID   string
		generation uint64
	}
	refs := make([]sessionRef, 0)
	s.RangeClients(func(clientID string, client *ClientConn) bool {
		if client.OwnerUserID == userID {
			refs = append(refs, sessionRef{clientID: clientID, generation: client.generation})
		}
		return true
	})

	invalidated := 0
	for _, ref := range refs {
		if s.invalidateLogicalSessionIfCurrent(ref.clientID, ref.generation, reason) {
			invalidated++
		}
	}
	return invalidated
}

func (s *Server) invalidateLogicalSessionIfCurrentLocked(clientID string, generation uint64, cause clientDisconnectCause) bool {
	value, ok := s.clients.Load(clientID)
	if !ok {
		return false
	}
	client := value.(*ClientConn)
	if client.generation != generation {
		return false
	}
	client.lifecycleMu.Lock()
	defer client.lifecycleMu.Unlock()
	value, ok = s.clients.Load(clientID)
	if !ok || value != client || client.generation != generation {
		return false
	}

	client.stateMu.Lock()
	if client.state == clientStateClosing {
		client.stateMu.Unlock()
		return false
	}
	wasLive := client.state == clientStateLive
	if client.pendingTimer != nil {
		client.pendingTimer.Stop()
		client.pendingTimer = nil
	}
	client.state = clientStateClosing
	client.stateMu.Unlock()

	s.cancelTunnelProvisionAckWaiters(clientID, generation)
	if s.p2p != nil {
		s.sendP2PLifecycleResults(s.p2p.closeClient(clientID, generation, cause.ReasonCode))
	}

	controlConn := client.detachControlConn()
	if controlConn != nil {
		_ = controlConn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, cause.ReasonCode),
			time.Now().Add(time.Second),
		)
		_ = controlConn.Close()
	}

	client.dataMu.Lock()
	dataSession := client.dataSession
	client.dataSession = nil
	client.dataMu.Unlock()
	if dataSession != nil && !dataSession.IsClosed() {
		_ = dataSession.Close()
	}

	s.CloseExposedProxyRuntime(client)
	s.releaseUnifiedRuntimeForClient(clientID)

	if wasLive {
		activityID := s.appendClientLifecycle(client, "offline", cause)
		s.publishActivityID(activityID)
		info := client.GetInfo()
		log.Printf("🔌 Client disconnected: %s [ID: %s, reason=%s]", info.Hostname, client.ID, cause.ReasonCode)
		s.events.PublishScopedJSON("client_offline", client.OwnerUserID, map[string]any{
			"client_id": client.ID,
		})
	}
	s.clients.CompareAndDelete(clientID, client)

	return true
}
