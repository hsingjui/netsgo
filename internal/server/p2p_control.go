package server

import (
	"context"
	"log"
	"math/rand/v2"
	"sort"
	"time"

	"netsgo/pkg/protocol"
)

type p2pRetryState struct {
	failures int
	next     time.Time
}

func clientSupportsP2P(client *ClientConn) bool {
	if client == nil {
		return false
	}
	caps := client.GetInfo().Capabilities
	return caps != nil && caps.P2P.Supported && caps.P2P.Impl == protocol.P2PImplWebRTCICE
}

func (s *Server) ensureP2PForTunnel(stored StoredTunnel, ingress, target *ClientConn) error {
	if stored.TransportPolicy == protocol.TransportPolicyServerRelayOnly || !clientSupportsP2P(ingress) || !clientSupportsP2P(target) {
		return nil
	}
	if stored.OwnerUserID == "" || ingress.OwnerUserID != stored.OwnerUserID || target.OwnerUserID != stored.OwnerUserID ||
		ingress.OwnerEpoch == 0 || target.OwnerEpoch != ingress.OwnerEpoch {
		return ErrUserLifecycleEpochChanged
	}
	if !s.p2pRetryAllowed(ingress.ID, target.ID) {
		return nil
	}
	grant, lifecycle, err := s.p2p.ensureGrant(p2pGrantSpec{
		tunnelID: stored.ID, revision: stored.Revision,
		ownerUserID: stored.OwnerUserID, ownerEpoch: ingress.OwnerEpoch,
		ingressClientID: ingress.ID, targetClientID: target.ID,
		ingressGeneration: ingress.generation, targetGeneration: target.generation,
		totalBPS: stored.TotalBPS,
	})
	if err != nil {
		return err
	}
	if !lifecycle.GrantCreated {
		return nil
	}
	messages, err := s.p2p.prepareMessages(grant.sessionID)
	if err != nil {
		return err
	}
	lifecycle.Transition = P2PProjectionTransition{Mode: P2PProjectionGathering, SessionID: grant.sessionID}
	if s.p2p.sessionReady(grant.sessionID) {
		lifecycle.Transition.Mode = P2PProjectionReady
	}
	lifecycle.Outbounds = messages
	s.sendP2PLifecycleResult(lifecycle)
	return nil
}

func (s *Server) sendP2POutbounds(messages []p2pOutbound) {
	for _, outbound := range messages {
		if err := s.sendP2POutbound(outbound); err != nil {
			log.Printf("⚠️ send P2P control message failed [%s]: %v", outbound.clientID, err)
		}
	}
}

func (s *Server) scheduleP2POutbounds(messages ...p2pOutbound) {
	if len(messages) == 0 {
		return
	}
	if s.done != nil {
		select {
		case <-s.done:
			return
		default:
		}
	}
	outbounds := append([]p2pOutbound(nil), messages...)
	go func() {
		if s.done != nil {
			select {
			case <-s.done:
				return
			default:
			}
		}
		s.sendP2POutbounds(outbounds)
	}()
}

func (s *Server) sendP2POutbound(outbound p2pOutbound) error {
	if outbound.clientID == "" || outbound.clientGeneration == 0 || outbound.ownerUserID == "" || outbound.ownerEpoch == 0 {
		return ErrUserLifecycleEpochChanged
	}
	defer s.runUserLifecycleHook("p2p_outbound_after_send", outbound.ownerUserID)
	client, ok := s.loadLiveClient(outbound.clientID)
	if !ok || client.generation != outbound.clientGeneration || client.OwnerUserID != outbound.ownerUserID || client.OwnerEpoch != outbound.ownerEpoch {
		return ErrUserLifecycleEpochChanged
	}
	msg, err := protocol.NewMessage(outbound.messageType, outbound.payload)
	if err != nil {
		return err
	}
	s.runUserLifecycleHook("p2p_outbound_before_gate", outbound.ownerUserID)
	_, releaseOwnerGate, err := s.acquireUserLifecycleRead(outbound.ownerUserID, outbound.ownerEpoch, true)
	if err != nil {
		return err
	}
	defer releaseOwnerGate()
	s.clientTunnelMutationMu.Lock()
	defer s.clientTunnelMutationMu.Unlock()
	current, ok := s.clients.Load(outbound.clientID)
	if !ok || current != client || client.generation != outbound.clientGeneration ||
		client.OwnerUserID != outbound.ownerUserID || client.OwnerEpoch != outbound.ownerEpoch ||
		!client.isLive() || !s.clientLifecycleCurrentLocked(client) {
		return ErrUserLifecycleEpochChanged
	}
	return s.writeControlMessageBefore(client, msg, time.Now().Add(lifecycleControlWriteTimeout))
}

// sendP2PConvergenceOutbounds runs with the owner's lifecycle write gate held.
// It therefore must not reacquire the read gate. Only terminal messages are
// permitted, and each write is pinned to the archived owner epoch and exact
// participant generation with an explicit deadline.
func (s *Server) sendP2PConvergenceOutbounds(ctx context.Context, ownerUserID string, messages []p2pOutbound) error {
	for _, outbound := range messages {
		if err := checkUserConvergenceContext(ctx); err != nil {
			return err
		}
		if outbound.messageType != protocol.MsgTypeP2PTunnelRevoke && outbound.messageType != protocol.MsgTypeP2PClosed {
			continue
		}
		if outbound.ownerUserID != ownerUserID || outbound.ownerEpoch == 0 || outbound.clientGeneration == 0 {
			continue
		}
		msg, err := protocol.NewMessage(outbound.messageType, outbound.payload)
		if err != nil {
			continue
		}
		deadline := time.Now().Add(lifecycleControlWriteTimeout)
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
			deadline = contextDeadline
		}

		s.clientTunnelMutationMu.Lock()
		value, ok := s.clients.Load(outbound.clientID)
		if !ok {
			s.clientTunnelMutationMu.Unlock()
			continue
		}
		client := value.(*ClientConn)
		current := client.generation == outbound.clientGeneration && client.OwnerUserID == ownerUserID &&
			client.OwnerEpoch == outbound.ownerEpoch && client.isLive()
		if current {
			err = s.writeControlMessageBefore(client, msg, deadline)
		}
		s.clientTunnelMutationMu.Unlock()
		if current && err != nil {
			log.Printf("⚠️ send P2P convergence message failed [%s]: %v", outbound.clientID, err)
		}
	}
	return checkUserConvergenceContext(ctx)
}
func (s *Server) sendP2PLifecycleResults(results []p2pLifecycleResult) {
	for _, result := range results {
		s.sendP2PLifecycleResult(result)
	}
}

func (s *Server) sendP2PLifecycleResult(result p2pLifecycleResult) {
	// P2P publication is commonly reached while the caller holds the owner's
	// read gate and clientTunnelMutationMu. Sending inline would recursively
	// acquire that read gate in sendP2POutbound; once disable is waiting for the
	// write gate, Go's RWMutex blocks the recursive RLock and deadlocks the
	// publisher. Project synchronously, but start epoch/generation-pinned writes
	// only after the caller has a chance to release its publication locks.
	outbounds := result.Outbounds
	result.Outbounds = nil
	s.applyP2PLifecycleResult(result)
	if len(outbounds) > 0 {
		s.scheduleP2POutbounds(outbounds...)
	}
}

func (s *Server) p2pLeaseLoop() {
	ticker := time.NewTicker(p2pLeaseRenewEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			result := s.p2p.renew(func(clientID string, generation uint64) bool { return s.isCurrentLive(clientID, generation) })
			s.sendP2PLifecycleResults(result.Closed)
			s.sendP2POutbounds(result.Outbounds)
		case <-s.done:
			return
		}
	}
}

func (s *Server) handleP2PSignalMessage(client *ClientConn, msg protocol.Message) {
	if !s.isCurrentLive(client.ID, client.generation) {
		return
	}
	var signal protocol.P2PSignal
	if err := msg.ParsePayload(&signal); err != nil {
		return
	}
	peer, err := s.p2p.authorizeSignal(client.ID, client.generation, signal)
	if err != nil {
		log.Printf("⚠️ rejected P2P signal [%s]: %v", client.ID, err)
		return
	}
	if s.p2pSignalDropHook != nil && s.p2pSignalDropHook(client.ID, peer.clientID, signal) {
		return
	}
	_ = s.forwardP2PControl(peer, protocol.MsgTypeP2PSignal, signal)
}

func closeP2PAfterFailedStatus(result p2pLifecycleResult) p2pLifecycleResult {
	result.Transition = P2PProjectionTransition{}
	return result
}

func (s *Server) handleP2PStatusMessage(client *ClientConn, msg protocol.Message) {
	if !s.isCurrentLive(client.ID, client.generation) {
		return
	}
	var status protocol.P2PSessionStatus
	if err := msg.ParsePayload(&status); err != nil {
		return
	}
	lifecycle, err := s.p2p.recordReady(client.ID, client.generation, status)
	if err != nil {
		return
	}
	ready := lifecycle.Session.Ready
	if lifecycle.ReadyEdge {
		log.Printf("🔗 P2P pair ready: session=%s", status.SessionID)
	}
	tunnelIDs := make([]string, 0, len(lifecycle.Session.Grants))
	for _, grant := range lifecycle.Session.Grants {
		tunnelIDs = append(tunnelIDs, grant.TunnelID)
	}
	mode := P2PProjectionGathering
	if ready {
		mode = P2PProjectionReady
	} else if status.State == protocol.P2PStateFailed {
		mode = P2PProjectionFailed
	}
	if len(tunnelIDs) > 0 {
		lifecycle.Transition = P2PProjectionTransition{Mode: mode, SessionID: status.SessionID}
	}
	lifecycle.ExpectedSessionID = status.SessionID
	if lifecycle.FailedEdge {
		lifecycle.ActivityActions = make(map[string][]p2pGrantSnapshot, 2)
		for _, grant := range lifecycle.Session.Grants {
			action := "failed"
			if stored, ok, _ := s.findStoredTunnelByID(grant.TunnelID); ok && stored.Revision == grant.Revision && stored.TransportPolicy == protocol.TransportPolicyDirectPreferred {
				action = "fallback"
			}
			lifecycle.ActivityActions[action] = append(lifecycle.ActivityActions[action], grant)
		}
	}
	s.sendP2PLifecycleResult(lifecycle)
	if ready {
		if len(tunnelIDs) > 0 {
			if stored, ok, _ := s.findStoredTunnelByID(tunnelIDs[0]); ok {
				s.resetP2PRetry(stored.Ingress.ClientID, stored.Target.ClientID)
			}
		}
	} else if status.State == protocol.P2PStateFailed {
		closed := closeP2PAfterFailedStatus(s.p2p.closeSession(status.SessionID, status.Error))
		s.sendP2PLifecycleResult(closed)
		s.scheduleP2PRetry(client.OwnerUserID, client.OwnerEpoch, lifecycle.Session)
	}
}

func p2pPairRetryKey(a, b string) string {
	pair := []string{a, b}
	sort.Strings(pair)
	return pair[0] + "\x00" + pair[1]
}
func (s *Server) p2pRetryAllowed(a, b string) bool {
	s.p2pRetryMu.Lock()
	defer s.p2pRetryMu.Unlock()
	state, ok := s.p2pRetries[p2pPairRetryKey(a, b)]
	return !ok || !state.next.After(time.Now())
}
func (s *Server) resetP2PRetry(a, b string) {
	s.p2pRetryMu.Lock()
	delete(s.p2pRetries, p2pPairRetryKey(a, b))
	s.p2pRetryMu.Unlock()
}

// scheduleP2PRetry is called while the reporting client's lifecycle read gate
// is held. Build fixed tasks from that already-authorized epoch instead of
// recursively acquiring RWMutex or resolving a fresh epoch when the timer fires.
func (s *Server) scheduleP2PRetry(ownerUserID string, ownerEpoch uint64, session p2pSessionSnapshot) {
	if len(session.Grants) == 0 {
		return
	}
	stored, ok, _ := s.findStoredTunnelByID(session.Grants[0].TunnelID)
	if !ok {
		return
	}
	key := p2pPairRetryKey(stored.Ingress.ClientID, stored.Target.ClientID)
	s.p2pRetryMu.Lock()
	state := s.p2pRetries[key]
	state.failures++
	delay := 10 * time.Second
	if state.failures == 2 {
		delay = 30 * time.Second
	}
	if state.failures >= 3 {
		delay = time.Minute + time.Duration(rand.IntN(20001)-10000)*time.Millisecond
	}
	state.next = time.Now().Add(delay)
	s.p2pRetries[key] = state
	s.p2pRetryMu.Unlock()
	generations := map[string]uint64{
		session.ClientA: session.GenerationA,
		session.ClientB: session.GenerationB,
	}
	tasks := make([]unifiedTunnelReconcileTask, 0, len(session.Grants))
	for _, grant := range session.Grants {
		current, ok, err := s.findStoredTunnelByID(grant.TunnelID)
		if err != nil || !ok || current.OwnerUserID != ownerUserID {
			continue
		}
		task, err := s.newUnifiedTunnelReconcileTaskForGenerationsAtEpoch(current, "p2p_retry", ownerEpoch, generations)
		if err != nil {
			continue
		}
		tasks = append(tasks, task)
	}
	go func(tasks []unifiedTunnelReconcileTask, wait time.Duration) {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
			for _, task := range tasks {
				if err := s.unifiedReconcileRegistry().runTask(task, s.executeUnifiedTunnelReconcileTask); err != nil {
					log.Printf("⚠️ P2P retry reconcile failed [%s]: %v", task.TunnelID, err)
				}
			}
		case <-s.done:
		}
	}(tasks, delay)
}

func (s *Server) handleP2PStatsMessage(client *ClientConn, msg protocol.Message) {
	// A graceful disconnect can mark the logical session Closing before this
	// queued final report is read. acceptStats still authorizes the archived
	// grant against the authenticated connection's exact client generation.
	var report protocol.P2PStatsReport
	if err := msg.ParsePayload(&report); err != nil {
		return
	}
	ingress, egress, err := s.p2p.acceptStats(client.ID, client.generation, report)
	if err != nil {
		log.Printf("⚠️ rejected P2P traffic report [%s]: %v", client.ID, err)
		return
	}
	if ingress == 0 && egress == 0 {
		return
	}
	stored, ok, err := s.findStoredTunnelByID(report.TunnelID)
	ownerClientID := stored.OwnerClientID
	if ownerClientID == "" {
		ownerClientID = stored.Target.ClientID
	}
	if err != nil || !ok || stored.Revision != report.Revision || ownerClientID != client.ID {
		return
	}
	delta := trafficDeltaFromStoredTunnel(stored, ingress, egress)
	delta.Transport = protocol.ActualTransportPeerDirect
	s.recordTrafficDeltaAt(time.Now(), delta)
}

func (s *Server) handleP2PCreditDemandMessage(client *ClientConn, msg protocol.Message) {
	if !s.isCurrentLive(client.ID, client.generation) {
		return
	}
	var demand protocol.P2PCreditDemand
	if err := msg.ParsePayload(&demand); err != nil {
		return
	}
	peer, err := s.p2p.authorizeCreditDemand(client.ID, client.generation, demand)
	if err != nil {
		return
	}
	_ = s.forwardP2PControl(peer, protocol.MsgTypeP2PCreditDemand, demand)
}

func (s *Server) handleP2PCreditGrantMessage(client *ClientConn, msg protocol.Message) {
	if !s.isCurrentLive(client.ID, client.generation) {
		return
	}
	var credit protocol.P2PCreditGrant
	if err := msg.ParsePayload(&credit); err != nil {
		return
	}
	peer, err := s.p2p.authorizeCreditGrant(client.ID, client.generation, credit)
	if err != nil {
		return
	}
	_ = s.forwardP2PControl(peer, protocol.MsgTypeP2PCreditGrant, credit)
}

func (s *Server) forwardP2PControl(peer p2pParticipant, messageType string, payload any) error {
	s.scheduleP2POutbounds(p2pOutbound{
		clientID: peer.clientID, clientGeneration: peer.clientGeneration,
		ownerUserID: peer.ownerUserID, ownerEpoch: peer.ownerEpoch,
		messageType: messageType, payload: payload,
	})
	return nil
}
