package server

import (
	"errors"
	"sync"
	"testing"
	"time"

	"netsgo/pkg/protocol"
)

type p2pProjectionRetryFixture struct {
	server     *Server
	stored     StoredTunnel
	item       p2pProjectionRetryItem
	ownerEpoch uint64
}

func newP2PProjectionRetryFixture(t *testing.T, mode P2PProjectionMode) p2pProjectionRetryFixture {
	t.Helper()
	s := New(0)
	s.store = newTestTunnelStore(t)
	stored := testClientRelayStoredTunnel(t)
	stored.OwnerUserID = "p2p-projection-owner"
	stored.TransportPolicy = protocol.TransportPolicyDirectPreferred
	stored.ActualTransport = protocol.ActualTransportServerRelay

	const ingressGeneration = 11
	const targetGeneration = 22
	_, lifecycle, err := s.p2p.ensureGrant(p2pGrantSpec{
		tunnelID: stored.ID, revision: stored.Revision,
		ingressClientID: stored.Ingress.ClientID, targetClientID: stored.Target.ClientID,
		ingressGeneration: ingressGeneration, targetGeneration: targetGeneration,
	})
	if err != nil {
		t.Fatalf("ensure P2P grant: %v", err)
	}
	transition := P2PProjectionTransition{Mode: mode, SessionID: lifecycle.Session.SessionID}
	expected := ""
	if mode == P2PProjectionReady {
		expected = lifecycle.Session.SessionID
		stored.P2P = P2PState{State: protocol.P2PStateGathering, SessionID: lifecycle.Session.SessionID}
		s.p2p.mu.Lock()
		session := s.p2p.byID[lifecycle.Session.SessionID]
		session.ready[session.clientA] = true
		session.ready[session.clientB] = true
		lifecycle.Session = snapshotP2PSession(session)
		s.p2p.mu.Unlock()
		lifecycle.ReadyEdge = true
	} else {
		lifecycle.StatusState = protocol.P2PStateGathering
	}
	lifecycle.Transition = transition
	lifecycle.ExpectedSessionID = expected
	stored = mustAddStableTunnelForServer(t, s, stored)

	gate := s.lifecycleGate(stored.OwnerUserID)
	gate.mu.RLock()
	ownerEpoch := gate.epoch
	gate.mu.RUnlock()
	s.clients.Store(stored.Ingress.ClientID, &ClientConn{
		ID: stored.Ingress.ClientID, OwnerUserID: stored.OwnerUserID, OwnerEpoch: ownerEpoch,
		generation: ingressGeneration, state: clientStateLive,
	})
	s.clients.Store(stored.Target.ClientID, &ClientConn{
		ID: stored.Target.ClientID, OwnerUserID: stored.OwnerUserID, OwnerEpoch: ownerEpoch,
		generation: targetGeneration, state: clientStateLive,
	})
	return p2pProjectionRetryFixture{
		server: s, stored: stored, ownerEpoch: ownerEpoch,
		item: p2pProjectionRetryItem{
			OwnerUserID: stored.OwnerUserID, OwnerEpoch: ownerEpoch,
			Result: lifecycle, Transition: transition, Expected: expected, Attempts: 1,
		},
	}
}

func putDueP2PProjectionRetry(s *Server, item p2pProjectionRetryItem) string {
	key := p2pProjectionRetryKey(item.Result)
	item.Next = time.Now().Add(-time.Second)
	s.p2pProjectionMu.Lock()
	s.p2pProjectionRetries[key] = item
	s.p2pProjectionMu.Unlock()
	return key
}

func assertP2PProjectionSessionID(t *testing.T, store *TunnelStore, tunnelID, want string) {
	t.Helper()
	stored, err := store.GetTunnelByID(tunnelID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.P2P.SessionID != want {
		t.Fatalf("P2P session id = %q, want %q (state=%q)", stored.P2P.SessionID, want, stored.P2P.State)
	}
}

func TestP2PProjectionRetryQueueIsBoundedAndDeduplicated(t *testing.T) {
	s := New(0)
	base := p2pProjectionRetryItem{
		OwnerUserID: "owner-1", OwnerEpoch: 1,
		Result: p2pLifecycleResult{Session: p2pSessionSnapshot{SessionID: "shared"}, ClosedEdge: true, Sequence: 1},
	}
	if !s.enqueueP2PProjectionRetry(base) {
		t.Fatal("initial projection should be enqueueable")
	}
	if !s.enqueueP2PProjectionRetry(base) {
		t.Fatal("duplicate projection should remain enqueueable")
	}
	if len(s.p2pProjectionRetries) != 1 {
		t.Fatalf("deduplicated queue size = %d", len(s.p2pProjectionRetries))
	}
	for i := 1; i < p2pProjectionRetryCapacity; i++ {
		item := base
		item.Result.Session.SessionID = string(rune(i + 1))
		if !s.enqueueP2PProjectionRetry(item) {
			t.Fatalf("queue rejected item %d before capacity", i)
		}
	}
	overflow := base
	overflow.Result.Session.SessionID = "overflow"
	if s.enqueueP2PProjectionRetry(overflow) {
		t.Fatal("queue accepted an item over capacity")
	}
	if len(s.p2pProjectionRetries) != p2pProjectionRetryCapacity {
		t.Fatalf("bounded queue size = %d", len(s.p2pProjectionRetries))
	}
}

func TestP2PProjectionRetryDelayCaps(t *testing.T) {
	if got := p2pProjectionRetryDelay(1); got != time.Second {
		t.Fatalf("first delay = %v", got)
	}
	if got := p2pProjectionRetryDelay(100); got != p2pProjectionRetryMax {
		t.Fatalf("capped delay = %v", got)
	}
}

func TestP2PProjectionRetryPublishesCommittedChanges(t *testing.T) {
	fixture := newP2PProjectionRetryFixture(t, P2PProjectionReady)
	s := fixture.server
	ch := s.events.Subscribe()
	defer s.events.Unsubscribe(ch)
	key := putDueP2PProjectionRetry(s, fixture.item)
	s.retryDueP2PProjections(time.Now())
	payload := waitForTunnelChangedEvent(t, ch, "p2p_status", fixture.stored.Name)
	if payload["actual_transport"] != "peer_direct" {
		t.Fatalf("retry event actual_transport = %#v", payload["actual_transport"])
	}
	if _, exists := s.p2pProjectionRetries[key]; exists {
		t.Fatal("successful retry remained queued")
	}
}

func TestP2PProjectionRetryCapturesOwnerAndEpochAfterInitialFailure(t *testing.T) {
	fixture := newP2PProjectionRetryFixture(t, P2PProjectionGathering)
	s := fixture.server
	s.store.failSaveErr = errors.New("injected P2P projection save failure")
	s.store.failSaveCount = 1

	s.applyP2PLifecycleResult(fixture.item.Result)

	key := p2pProjectionRetryKey(fixture.item.Result)
	s.p2pProjectionMu.Lock()
	queued, ok := s.p2pProjectionRetries[key]
	s.p2pProjectionMu.Unlock()
	if !ok {
		t.Fatal("initial projection failure was not queued")
	}
	if queued.OwnerUserID != fixture.stored.OwnerUserID || queued.OwnerEpoch != fixture.ownerEpoch {
		t.Fatalf("captured owner = (%q, %d), want (%q, %d)", queued.OwnerUserID, queued.OwnerEpoch, fixture.stored.OwnerUserID, fixture.ownerEpoch)
	}
	if queued.Expected != "" {
		t.Fatalf("initial gathering expected session = %q, want empty", queued.Expected)
	}
}

func TestCancelOwnedP2PProjectionRetriesRemovesQueuedRetry(t *testing.T) {
	fixture := newP2PProjectionRetryFixture(t, P2PProjectionGathering)
	s := fixture.server
	if !s.enqueueP2PProjectionRetry(fixture.item) {
		t.Fatal("enqueue retry")
	}
	tunnelIDs := map[string]struct{}{fixture.stored.ID: {}}
	before := s.ownedP2PProjectionRetryResidual(fixture.stored.OwnerUserID, tunnelIDs)
	if before.Queued != 1 || before.InFlight != 0 {
		t.Fatalf("residual before cancel = %+v", before)
	}

	gate := s.lifecycleGate(fixture.stored.OwnerUserID)
	writeHeld := make(chan struct{})
	releaseWrite := make(chan struct{})
	removedCh := make(chan p2pProjectionRetryCounts, 1)
	done := make(chan struct{})
	go func() {
		gate.mu.Lock()
		gate.epoch += 2 // model disable followed by enable
		removedCh <- s.cancelOwnedP2PProjectionRetries(fixture.stored.OwnerUserID, tunnelIDs)
		close(writeHeld)
		<-releaseWrite
		gate.mu.Unlock()
		close(done)
	}()
	<-writeHeld
	removed := <-removedCh
	if removed.Queued != 1 || removed.InFlight != 0 {
		t.Fatalf("removed = %+v", removed)
	}
	if residual := s.ownedP2PProjectionRetryResidual(fixture.stored.OwnerUserID, tunnelIDs); residual.total() != 0 {
		t.Fatalf("residual under lifecycle write gate = %+v", residual)
	}
	close(releaseWrite)
	<-done

	s.retryDueP2PProjections(time.Now().Add(time.Hour))
	assertP2PProjectionSessionID(t, s.store, fixture.stored.ID, "")
}

func TestInFlightP2PProjectionRetryCannotPublishAfterEpochChange(t *testing.T) {
	fixture := newP2PProjectionRetryFixture(t, P2PProjectionGathering)
	s := fixture.server
	putDueP2PProjectionRetry(s, fixture.item)

	dequeued := make(chan struct{})
	resume := make(chan struct{})
	var once sync.Once
	s.userLifecycleHook = func(stage, userID string) {
		if stage == "p2p_projection_retry_dequeued" && userID == fixture.stored.OwnerUserID {
			once.Do(func() { close(dequeued) })
			<-resume
		}
	}
	retryDone := make(chan struct{})
	go func() {
		s.retryDueP2PProjections(time.Now())
		close(retryDone)
	}()
	<-dequeued

	tunnelIDs := map[string]struct{}{fixture.stored.ID: {}}
	residual := s.ownedP2PProjectionRetryResidual(fixture.stored.OwnerUserID, tunnelIDs)
	if residual.Queued != 0 || residual.InFlight != 1 {
		t.Fatalf("dequeued residual = %+v", residual)
	}
	gate := s.lifecycleGate(fixture.stored.OwnerUserID)
	gate.mu.Lock()
	gate.epoch += 2 // model disable followed by enable before the retry reaches gate.R
	removed := s.cancelOwnedP2PProjectionRetries(fixture.stored.OwnerUserID, tunnelIDs)
	gate.mu.Unlock()
	if removed.Queued != 0 || removed.InFlight != 1 {
		t.Fatalf("removed in-flight = %+v", removed)
	}
	close(resume)
	<-retryDone

	assertP2PProjectionSessionID(t, s.store, fixture.stored.ID, "")
	if residual := s.ownedP2PProjectionRetryResidual(fixture.stored.OwnerUserID, tunnelIDs); residual.total() != 0 {
		t.Fatalf("retry residual after old epoch drop = %+v", residual)
	}
}

func TestP2PProjectionRetryRequiresCurrentCoordinatorGeneration(t *testing.T) {
	fixture := newP2PProjectionRetryFixture(t, P2PProjectionGathering)
	s := fixture.server
	putDueP2PProjectionRetry(s, fixture.item)
	s.p2p.mu.Lock()
	s.p2p.byID[fixture.item.Result.Session.SessionID].generationA++
	s.p2p.mu.Unlock()

	s.retryDueP2PProjections(time.Now())

	assertP2PProjectionSessionID(t, s.store, fixture.stored.ID, "")
}

func TestClosedP2PProjectionRetryAllowsRemovedCoordinatorSession(t *testing.T) {
	fixture := newP2PProjectionRetryFixture(t, P2PProjectionReady)
	s := fixture.server
	closed := s.p2p.closeSession(fixture.item.Result.Session.SessionID, "participant_offline")
	item := p2pProjectionRetryItem{
		OwnerUserID: fixture.stored.OwnerUserID,
		OwnerEpoch:  fixture.ownerEpoch,
		Result:      closed,
		Transition:  closed.Transition,
		Expected:    closed.ExpectedSessionID,
		Attempts:    1,
	}
	putDueP2PProjectionRetry(s, item)

	s.retryDueP2PProjections(time.Now())

	stored, err := s.store.GetTunnelByID(fixture.stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.P2P.State != protocol.P2PStateClosed || stored.P2P.SessionID != "" {
		t.Fatalf("closed retry projection = %+v", stored.P2P)
	}
}
