package server

import (
	"log"
	"time"
)

const (
	p2pProjectionRetryCapacity = 256
	p2pProjectionRetryBase     = time.Second
	p2pProjectionRetryMax      = 30 * time.Second
)

type p2pProjectionRetryItem struct {
	OwnerUserID string
	OwnerEpoch  uint64
	InFlight    bool
	Result      p2pLifecycleResult
	Transition  P2PProjectionTransition
	Expected    string
	Attempts    int
	Next        time.Time
}

func p2pProjectionRetryKey(result p2pLifecycleResult) string {
	action := ""
	switch {
	case result.ClosedEdge:
		action = "session_closed"
	case result.DetachedEdge:
		action = "tunnel_detached"
	case result.ReadyEdge:
		action = "connected"
	case result.FailedEdge:
		action = "failed"
	default:
		action = result.StatusState
	}
	return result.projectionKey(action)
}

func (s *Server) enqueueP2PProjectionRetry(item p2pProjectionRetryItem) bool {
	key := p2pProjectionRetryKey(item.Result)
	if key == "" || item.OwnerUserID == "" || item.OwnerEpoch == 0 {
		return false
	}
	s.p2pProjectionMu.Lock()
	if _, exists := s.p2pProjectionRetries[key]; !exists && len(s.p2pProjectionRetries) >= p2pProjectionRetryCapacity {
		s.p2pProjectionMu.Unlock()
		log.Printf("⚠️ P2P projection retry queue full; reconciling session %s", item.Result.Session.SessionID)
		s.reconcileP2PProjectionTargets(item)
		return false
	}
	if existing, exists := s.p2pProjectionRetries[key]; exists {
		if existing.OwnerUserID != item.OwnerUserID || existing.OwnerEpoch > item.OwnerEpoch {
			s.p2pProjectionMu.Unlock()
			return false
		}
		if existing.OwnerEpoch == item.OwnerEpoch && existing.Attempts > item.Attempts {
			item.Attempts = existing.Attempts
		}
	}
	item.InFlight = false
	item.Next = time.Now().Add(p2pProjectionRetryDelay(item.Attempts))
	s.p2pProjectionRetries[key] = item
	s.p2pProjectionMu.Unlock()
	select {
	case s.p2pProjectionWake <- struct{}{}:
	default:
	}
	return true
}

func p2pProjectionRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := p2pProjectionRetryBase << min(attempt-1, 5)
	if delay > p2pProjectionRetryMax {
		return p2pProjectionRetryMax
	}
	return delay
}

func (s *Server) p2pProjectionRetryLoop() {
	defer close(s.p2pProjectionDone)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.p2pProjectionStop:
			return
		case <-s.p2pProjectionWake:
			s.retryDueP2PProjections(time.Now())
		case now := <-ticker.C:
			s.retryDueP2PProjections(now)
		}
	}
}

func (s *Server) retryDueP2PProjections(now time.Time) {
	s.p2pProjectionMu.Lock()
	due := make(map[string]p2pProjectionRetryItem)
	for key, item := range s.p2pProjectionRetries {
		if !item.InFlight && !item.Next.After(now) {
			item.InFlight = true
			s.p2pProjectionRetries[key] = item
			due[key] = item
		}
	}
	s.p2pProjectionMu.Unlock()
	for key, item := range due {
		s.runUserLifecycleHook("p2p_projection_retry_dequeued", item.OwnerUserID)
		item.Attempts++
		current, err := s.applyP2PProjectionRetry(item)
		if err != nil {
			log.Printf("⚠️ P2P projection retry failed [%s]: %v", key, err)
			if !s.requeueP2PProjectionRetryAtExpectedEpoch(item) {
				s.removeP2PProjectionRetry(key, item)
			}
			continue
		}
		if !current {
			s.removeP2PProjectionRetry(key, item)
			continue
		}
		s.removeP2PProjectionRetry(key, item)
	}
}

// applyP2PProjectionRetry serializes the final persisted projection with the
// owning user's lifecycle. The global mutation lock makes the tunnel/client
// snapshot atomic with the write, while the coordinator lock prevents a
// matching session generation from changing between validation and commit.
func (s *Server) applyP2PProjectionRetry(item p2pProjectionRetryItem) (bool, error) {
	_, releaseOwnerGate, err := s.acquireUserLifecycleRead(item.OwnerUserID, item.OwnerEpoch, true)
	if err != nil {
		// A retry is only valid for the epoch in which it was captured. Storage
		// availability errors are also dropped here: periodic unified reconcile
		// is the safe recovery path when ownership cannot be re-authorized.
		return false, nil
	}
	defer releaseOwnerGate()

	s.clientTunnelMutationMu.Lock()
	defer s.clientTunnelMutationMu.Unlock()
	current, err := s.p2pProjectionTargetsCurrentLocked(item)
	if err != nil || !current {
		return current, err
	}

	if s.p2p == nil {
		return false, nil
	}
	s.p2p.mu.Lock()
	if !s.p2pProjectionCoordinatorCurrentLocked(item) {
		s.p2p.mu.Unlock()
		return false, nil
	}
	if s.store == nil {
		s.p2p.mu.Unlock()
		return false, nil
	}
	projection, err := s.store.ApplyP2PLifecycle(item.Result.Session.Grants, item.Expected, item.Transition)
	s.p2p.mu.Unlock()
	if err == nil {
		s.emitP2PProjectionChanges(projection.Changes)
	}
	return true, err
}

func (s *Server) requeueP2PProjectionRetryAtExpectedEpoch(item p2pProjectionRetryItem) bool {
	_, releaseOwnerGate, err := s.acquireUserLifecycleRead(item.OwnerUserID, item.OwnerEpoch, true)
	if err != nil {
		return false
	}
	defer releaseOwnerGate()
	return s.enqueueP2PProjectionRetry(item)
}

// p2pProjectionTargetsCurrentLocked requires clientTunnelMutationMu. It
// rechecks every storage identity instead of relying solely on the SQL
// revision predicate, because retry ownership is part of authorization.
func (s *Server) p2pProjectionTargetsCurrentLocked(item p2pProjectionRetryItem) (bool, error) {
	if len(item.Result.Session.Grants) == 0 {
		return false, nil
	}
	for _, grant := range item.Result.Session.Grants {
		stored, ok, err := s.findStoredTunnelByID(grant.TunnelID)
		if err != nil {
			return false, err
		}
		if !ok || stored.Revision != grant.Revision || stored.OwnerUserID != item.OwnerUserID {
			return false, nil
		}
		if grant.IngressClientID != "" && stored.Ingress.ClientID != grant.IngressClientID {
			return false, nil
		}
		if grant.TargetClientID != "" && stored.Target.ClientID != grant.TargetClientID {
			return false, nil
		}
		if item.Expected != "" && stored.P2P.SessionID != item.Expected {
			return false, nil
		}
	}

	if item.Transition.Mode == P2PProjectionClosed {
		return true, nil
	}
	return s.p2pProjectionParticipantCurrentLocked(item.Result.Session.ClientA, item.Result.Session.GenerationA, item) &&
		s.p2pProjectionParticipantCurrentLocked(item.Result.Session.ClientB, item.Result.Session.GenerationB, item), nil
}

func (s *Server) p2pProjectionParticipantCurrentLocked(clientID string, generation uint64, item p2pProjectionRetryItem) bool {
	if clientID == "" || generation == 0 {
		return false
	}
	value, ok := s.clients.Load(clientID)
	if !ok {
		return false
	}
	client := value.(*ClientConn)
	return client.generation == generation && client.OwnerUserID == item.OwnerUserID &&
		client.OwnerEpoch == item.OwnerEpoch && client.isLive()
}

// p2pProjectionCoordinatorCurrentLocked requires p2p.mu. A closed transition
// may legitimately outlive the removed coordinator session; its expected
// persisted session id still prevents it from closing a replacement session.
func (s *Server) p2pProjectionCoordinatorCurrentLocked(item p2pProjectionRetryItem) bool {
	snapshot := item.Result.Session
	if snapshot.SessionID == "" || snapshot.ClientA == "" || snapshot.ClientB == "" || snapshot.GenerationA == 0 || snapshot.GenerationB == 0 {
		return false
	}
	session := s.p2p.byID[snapshot.SessionID]
	if session == nil {
		return item.Transition.Mode == P2PProjectionClosed
	}
	if session.clientA != snapshot.ClientA || session.clientB != snapshot.ClientB ||
		session.generationA != snapshot.GenerationA || session.generationB != snapshot.GenerationB {
		return false
	}
	if item.Transition.Mode == P2PProjectionClosed {
		return true
	}
	for _, grant := range snapshot.Grants {
		current, ok := session.grants[grant.TunnelID]
		if !ok || current.revision != grant.Revision || current.ingressClientID != grant.IngressClientID || current.targetClientID != grant.TargetClientID {
			return false
		}
	}
	if item.Transition.Mode == P2PProjectionReady {
		return session.ready[session.clientA] && session.ready[session.clientB]
	}
	if item.Transition.Mode == P2PProjectionGathering && session.ready[session.clientA] && session.ready[session.clientB] {
		return false
	}
	return true
}

func (s *Server) removeP2PProjectionRetry(key string, item p2pProjectionRetryItem) {
	s.p2pProjectionMu.Lock()
	if current, ok := s.p2pProjectionRetries[key]; ok && current.OwnerUserID == item.OwnerUserID && current.OwnerEpoch == item.OwnerEpoch {
		delete(s.p2pProjectionRetries, key)
	}
	s.p2pProjectionMu.Unlock()
}

type p2pProjectionRetryCounts struct {
	Queued   int
	InFlight int
}

func (c p2pProjectionRetryCounts) total() int {
	return c.Queued + c.InFlight
}

func p2pProjectionRetryOwnedBy(item p2pProjectionRetryItem, userID string, tunnelIDs map[string]struct{}) bool {
	if userID != "" && item.OwnerUserID == userID {
		return true
	}
	for _, grant := range item.Result.Session.Grants {
		if _, ok := tunnelIDs[grant.TunnelID]; ok {
			return true
		}
	}
	return false
}

// cancelOwnedP2PProjectionRetries is called while the user's lifecycle write
// gate is held. A retry already copied by the worker is removed from the
// logical residual immediately and is still rejected by its captured epoch
// when it later attempts the read gate.
func (s *Server) cancelOwnedP2PProjectionRetries(userID string, tunnelIDs map[string]struct{}) p2pProjectionRetryCounts {
	var removed p2pProjectionRetryCounts
	s.p2pProjectionMu.Lock()
	for key, item := range s.p2pProjectionRetries {
		if !p2pProjectionRetryOwnedBy(item, userID, tunnelIDs) {
			continue
		}
		if item.InFlight {
			removed.InFlight++
		} else {
			removed.Queued++
		}
		delete(s.p2pProjectionRetries, key)
	}
	s.p2pProjectionMu.Unlock()
	return removed
}

func (s *Server) ownedP2PProjectionRetryResidual(userID string, tunnelIDs map[string]struct{}) p2pProjectionRetryCounts {
	var residual p2pProjectionRetryCounts
	s.p2pProjectionMu.Lock()
	defer s.p2pProjectionMu.Unlock()
	for _, item := range s.p2pProjectionRetries {
		if !p2pProjectionRetryOwnedBy(item, userID, tunnelIDs) {
			continue
		}
		if item.InFlight {
			residual.InFlight++
		} else {
			residual.Queued++
		}
	}
	return residual
}

func p2pClosedProjectionTransition(_ string) P2PProjectionTransition {
	return P2PProjectionTransition{Mode: P2PProjectionClosed}
}

func (s *Server) applyP2PLifecycleResult(result p2pLifecycleResult) {
	if s.store != nil && len(result.Session.Grants) > 0 && result.Transition.Mode != "" {
		if _, err := s.store.ApplyP2PLifecycle(result.Session.Grants, result.ExpectedSessionID, result.Transition); err != nil {
			item, captured := s.captureP2PProjectionRetry(result, result.Transition, result.ExpectedSessionID, 1)
			queued := captured && s.enqueueP2PProjectionRetry(item)
			log.Printf("⚠️ Failed to project P2P lifecycle [%s], queued=%v: %v", result.Session.SessionID, queued, err)
		}
	}
	s.appendP2PActivities(result)
	for _, grant := range result.Session.Grants {
		if refreshed, ok, _ := s.findStoredTunnelByID(grant.TunnelID); ok && refreshed.Revision == grant.Revision {
			s.emitTunnelChangedIfStored(refreshed.OwnerClientID, storedTunnelToProxyConfig(refreshed), "p2p_status")
		}
	}
}

func (s *Server) captureP2PProjectionRetry(result p2pLifecycleResult, transition P2PProjectionTransition, expected string, attempts int) (p2pProjectionRetryItem, bool) {
	item := p2pProjectionRetryItem{Result: result, Transition: transition, Expected: expected, Attempts: attempts}
	ownerUserID := ""
	for _, grant := range result.Session.Grants {
		stored, ok, err := s.findStoredTunnelByID(grant.TunnelID)
		if err != nil || !ok || stored.Revision != grant.Revision || stored.OwnerUserID == "" {
			return p2pProjectionRetryItem{}, false
		}
		if ownerUserID == "" {
			ownerUserID = stored.OwnerUserID
		} else if ownerUserID != stored.OwnerUserID {
			return p2pProjectionRetryItem{}, false
		}
	}
	if ownerUserID == "" {
		return p2pProjectionRetryItem{}, false
	}

	ownerEpoch := uint64(0)
	participants := []struct {
		id         string
		generation uint64
	}{
		{id: result.Session.ClientA, generation: result.Session.GenerationA},
		{id: result.Session.ClientB, generation: result.Session.GenerationB},
	}
	for _, participant := range participants {
		value, ok := s.clients.Load(participant.id)
		if !ok {
			continue
		}
		client := value.(*ClientConn)
		if client.generation != participant.generation || client.OwnerUserID != ownerUserID || client.OwnerEpoch == 0 {
			continue
		}
		if ownerEpoch != 0 && ownerEpoch != client.OwnerEpoch {
			return p2pProjectionRetryItem{}, false
		}
		ownerEpoch = client.OwnerEpoch
	}
	if ownerEpoch == 0 {
		return p2pProjectionRetryItem{}, false
	}
	item.OwnerUserID = ownerUserID
	item.OwnerEpoch = ownerEpoch
	return item, true
}

func (s *Server) emitP2PProjectionChanges(changes []P2PProjectionChange) {
	for _, change := range changes {
		s.emitTunnelChangedIfStored(change.After.OwnerClientID, storedTunnelToProxyConfig(change.After), "p2p_status")
	}
}

func (s *Server) reconcileP2PProjectionTargets(item p2pProjectionRetryItem) {
	generations := map[string]uint64{
		item.Result.Session.ClientA: item.Result.Session.GenerationA,
		item.Result.Session.ClientB: item.Result.Session.GenerationB,
	}
	for _, grant := range item.Result.Session.Grants {
		stored, ok, err := s.findStoredTunnelByID(grant.TunnelID)
		if err != nil || !ok || stored.Revision != grant.Revision || stored.OwnerUserID != item.OwnerUserID {
			continue
		}
		task, err := s.newUnifiedTunnelReconcileTaskForGenerationsAtEpoch(stored, "p2p_projection_retry_overflow", item.OwnerEpoch, generations)
		if err != nil {
			continue
		}
		s.scheduleCapturedUnifiedTunnelReconcile(stored, task)
	}
}
