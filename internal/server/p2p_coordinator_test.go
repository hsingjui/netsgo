package server

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"netsgo/pkg/protocol"
)

func TestP2PCoordinatorConcurrentGrantStormSharesOnePairSession(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	c := newP2PCoordinator(func() time.Time { return now })
	const grants = 128
	sessionIDs := make(chan string, grants)
	errors := make(chan error, grants)
	var wg sync.WaitGroup
	for i := 0; i < grants; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			grant, lifecycle, err := c.ensureGrant(p2pGrantSpec{tunnelID: fmt.Sprintf("storm-%03d", i), revision: 1, ingressClientID: "a", targetClientID: "b", ingressGeneration: 10, targetGeneration: 20})
			if err != nil || !lifecycle.GrantCreated {
				errors <- fmt.Errorf("grant %d: grantCreated=%v err=%v", i, lifecycle.GrantCreated, err)
				return
			}
			sessionIDs <- grant.sessionID
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	close(sessionIDs)
	var want string
	for id := range sessionIDs {
		if want == "" {
			want = id
		}
		if id != want {
			t.Fatalf("grant storm created multiple pair sessions: %q != %q", id, want)
		}
	}
	if c.sessionCount() != 1 {
		t.Fatalf("grant storm session count=%d want=1", c.sessionCount())
	}
	messages, err := c.prepareMessages(want)
	if err != nil || len(messages) != 2 {
		t.Fatalf("pair prepare messages=%d err=%v", len(messages), err)
	}
	for _, message := range messages {
		prepare := message.payload.(protocol.P2PSessionPrepare)
		if len(prepare.Grants) != grants {
			t.Fatalf("prepared grants=%d want=%d", len(prepare.Grants), grants)
		}
	}
}

func TestP2PCoordinatorSharesPairSessionAndKeepsTunnelRolesPerGrant(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	c := newP2PCoordinator(func() time.Time { return now })

	first, lifecycle, err := c.ensureGrant(p2pGrantSpec{tunnelID: "t1", revision: 1, ingressClientID: "a", targetClientID: "b", ingressGeneration: 10, targetGeneration: 20})
	if err != nil || !lifecycle.GrantCreated {
		t.Fatalf("first grant: grantCreated=%v err=%v", lifecycle.GrantCreated, err)
	}
	if !lifecycle.SessionCreated {
		t.Fatal("first grant must create the pair session")
	}
	second, lifecycle, err := c.ensureGrant(p2pGrantSpec{tunnelID: "t2", revision: 3, ingressClientID: "b", targetClientID: "a", ingressGeneration: 20, targetGeneration: 10})
	if err != nil || !lifecycle.GrantCreated {
		t.Fatalf("second grant: grantCreated=%v err=%v", lifecycle.GrantCreated, err)
	}
	if lifecycle.SessionCreated {
		t.Fatal("second grant on the same pair must not recreate the session")
	}
	if first.sessionID != second.sessionID {
		t.Fatalf("pair did not share session: %s != %s", first.sessionID, second.sessionID)
	}
	if first.forClient("a").LocalRole != protocol.DataStreamRoleIngress || second.forClient("a").LocalRole != protocol.DataStreamRoleTarget {
		t.Fatal("tunnel role was incorrectly fixed at pair scope")
	}
	if got := c.sessionCount(); got != 1 {
		t.Fatalf("session count: want 1 got %d", got)
	}
}
func TestP2PCoordinatorLifecycleSnapshotsAreImmutable(t *testing.T) {
	c := newP2PCoordinator(time.Now)
	first, started, err := c.ensureGrant(p2pGrantSpec{tunnelID: "t1", revision: 1, ingressClientID: "a", targetClientID: "b", ingressGeneration: 10, targetGeneration: 20})
	if err != nil || !started.SessionCreated || !started.GrantCreated || len(started.Session.Grants) != 1 {
		t.Fatalf("started lifecycle = %+v, err=%v", started, err)
	}
	_, attached, err := c.ensureGrant(p2pGrantSpec{tunnelID: "t2", revision: 2, ingressClientID: "a", targetClientID: "b", ingressGeneration: 10, targetGeneration: 20})
	if err != nil || attached.SessionCreated || !attached.GrantCreated || attached.Session.SessionID != first.sessionID || len(attached.Session.Grants) != 2 {
		t.Fatalf("attached lifecycle = %+v, err=%v", attached, err)
	}
	if len(started.Session.Grants) != 1 || started.Session.Grants[0].TunnelID != "t1" {
		t.Fatalf("past lifecycle snapshot mutated: %+v", started.Session.Grants)
	}
	statusA, err := c.recordReady("a", 10, protocol.P2PSessionStatus{SessionID: first.sessionID, Sequence: 1, State: protocol.P2PStateConnected})
	if err != nil || !statusA.ReportAccepted || statusA.ReadyEdge {
		t.Fatalf("first ready report = %+v, err=%v", statusA, err)
	}
	statusB, err := c.recordReady("b", 20, protocol.P2PSessionStatus{SessionID: first.sessionID, Sequence: 1, State: protocol.P2PStateConnected})
	if err != nil || !statusB.ReadyEdge || !statusB.Session.Ready {
		t.Fatalf("pair ready report = %+v, err=%v", statusB, err)
	}
	closed := c.closeSession(first.sessionID, "participant offline raw detail")
	if !closed.ClosedEdge || closed.ReasonCode != "participant_offline" || len(closed.Session.Grants) != 2 {
		t.Fatalf("closed lifecycle = %+v", closed)
	}
	if len(attached.Session.Grants) != 2 || attached.Session.Ready {
		t.Fatalf("attached snapshot mutated after ready/close: %+v", attached.Session)
	}
}

func TestP2PCoordinatorExistingReadySessionRemainsReadyWhenGrantAdded(t *testing.T) {
	now := time.Now()
	c := newP2PCoordinator(func() time.Time { return now })
	first, _, err := c.ensureGrant(p2pGrantSpec{tunnelID: "t1", revision: 1, ingressClientID: "a", targetClientID: "b", ingressGeneration: 10, targetGeneration: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := c.recordReady("a", 10, protocol.P2PSessionStatus{SessionID: first.sessionID, Sequence: 1, State: protocol.P2PStateConnected}); err != nil || result.ReadyEdge {
		t.Fatalf("first peer ready: ready=%v err=%v", result.ReadyEdge, err)
	}
	if result, err := c.recordReady("b", 20, protocol.P2PSessionStatus{SessionID: first.sessionID, Sequence: 1, State: protocol.P2PStateConnected}); err != nil || !result.ReadyEdge {
		t.Fatalf("pair ready: ready=%v err=%v", result.ReadyEdge, err)
	}
	if !c.sessionReady(first.sessionID) {
		t.Fatal("pair should report ready after both peers connect")
	}
	second, lifecycle, err := c.ensureGrant(p2pGrantSpec{tunnelID: "t2", revision: 1, ingressClientID: "a", targetClientID: "b", ingressGeneration: 10, targetGeneration: 20})
	if err != nil || !lifecycle.GrantCreated {
		t.Fatalf("add grant to ready pair: grantCreated=%v err=%v", lifecycle.GrantCreated, err)
	}
	if second.sessionID != first.sessionID {
		t.Fatalf("new grant created a different pair session: first=%q second=%q", first.sessionID, second.sessionID)
	}
	if !c.sessionReady(first.sessionID) {
		t.Fatal("adding a grant must not hide the existing connected pair state")
	}
}

func TestP2PCoordinatorRejectsStaleSignalSequenceAndGeneration(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	c := newP2PCoordinator(func() time.Time { return now })
	grant, _, err := c.ensureGrant(p2pGrantSpec{tunnelID: "t1", revision: 1, ingressClientID: "a", targetClientID: "b", ingressGeneration: 10, targetGeneration: 20})
	if err != nil {
		t.Fatal(err)
	}
	signal := protocol.P2PSignal{SessionID: grant.sessionID, Sequence: 1, Kind: protocol.P2PSignalOffer, SDP: "v=0"}
	peer, err := c.authorizeSignal("a", 10, signal)
	if err != nil || peer.clientID != "b" {
		t.Fatalf("valid signal rejected: peer=%+v err=%v", peer, err)
	}
	if _, err := c.authorizeSignal("a", 10, signal); err == nil {
		t.Fatal("replayed signal accepted")
	}
	signal.Sequence++
	if _, err := c.authorizeSignal("a", 11, signal); err == nil {
		t.Fatal("wrong generation accepted")
	}
}

func TestP2PCoordinatorRevokesOneGrantWithoutClosingSharedPair(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	c := newP2PCoordinator(func() time.Time { return now })
	first, _, _ := c.ensureGrant(p2pGrantSpec{tunnelID: "t1", revision: 1, ingressClientID: "a", targetClientID: "b", ingressGeneration: 1, targetGeneration: 2})
	_, _, _ = c.ensureGrant(p2pGrantSpec{tunnelID: "t2", revision: 1, ingressClientID: "a", targetClientID: "b", ingressGeneration: 1, targetGeneration: 2})
	result := c.revokeTunnel("t1", 1, "disabled")
	if result.ClosedEdge || len(result.Outbounds) != 2 {
		t.Fatalf("first revoke closed=%v messages=%d", result.ClosedEdge, len(result.Outbounds))
	}
	if _, ok := c.session(first.sessionID); !ok {
		t.Fatal("shared pair closed while another grant remained")
	}
	result = c.revokeTunnel("t2", 1, "deleted")
	if !result.ClosedEdge || len(result.Outbounds) != 4 {
		t.Fatalf("last revoke closed=%v messages=%d", result.ClosedEdge, len(result.Outbounds))
	}
}

func TestP2PCoordinatorExpiresPairAtHardLeaseBoundary(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	c := newP2PCoordinator(func() time.Time { return now })
	grant, _, _ := c.ensureGrant(p2pGrantSpec{tunnelID: "t1", revision: 1, ingressClientID: "a", targetClientID: "b", ingressGeneration: 1, targetGeneration: 2})
	now = now.Add(p2pLeaseDuration)
	if expired := c.expire(); len(expired) != 1 || expired[0] != grant.sessionID {
		t.Fatalf("expired sessions: %v", expired)
	}
	if c.sessionCount() != 0 {
		t.Fatal("expired pair remained registered")
	}
}

func TestP2PCoordinatorClientDisconnectClosesPairOnlyForCurrentGeneration(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	c := newP2PCoordinator(func() time.Time { return now })
	grant, _, err := c.ensureGrant(p2pGrantSpec{tunnelID: "t1", revision: 1, ingressClientID: "a", targetClientID: "b", ingressGeneration: 10, targetGeneration: 20})
	if err != nil {
		t.Fatal(err)
	}
	if results := c.closeClient("a", 9, "stale disconnect"); len(results) != 0 || c.sessionCount() != 1 {
		t.Fatalf("stale generation closed current pair: results=%+v sessions=%d", results, c.sessionCount())
	}
	results := c.closeClient("a", 10, "control lost")
	out := results[0].Outbounds
	if len(out) != 1 || out[0].clientID != "b" || out[0].messageType != protocol.MsgTypeP2PClosed {
		t.Fatalf("current disconnect close notification=%+v", out)
	}
	status, ok := out[0].payload.(protocol.P2PSessionStatus)
	if !ok || status.SessionID != grant.sessionID || status.State != protocol.P2PStateClosed || status.Error != "control lost" {
		t.Fatalf("current disconnect status=%+v", out[0].payload)
	}
	if c.sessionCount() != 0 {
		t.Fatal("current Client disconnect left pair session alive")
	}
}

func TestP2PCoordinatorPinsOutboundsToOwnerEpochAndParticipantGenerations(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	c := newP2PCoordinator(func() time.Time { return now })
	grant, _, err := c.ensureGrant(p2pGrantSpec{
		tunnelID: "t1", revision: 1,
		ownerUserID: "owner-a", ownerEpoch: 7,
		ingressClientID: "a", targetClientID: "b",
		ingressGeneration: 10, targetGeneration: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPinned := func(outbound p2pOutbound) {
		t.Helper()
		wantGeneration := uint64(10)
		if outbound.clientID == "b" {
			wantGeneration = 20
		}
		if outbound.ownerUserID != "owner-a" || outbound.ownerEpoch != 7 || outbound.clientGeneration != wantGeneration {
			t.Fatalf("outbound pin = client=%q generation=%d owner=%q epoch=%d", outbound.clientID, outbound.clientGeneration, outbound.ownerUserID, outbound.ownerEpoch)
		}
	}
	prepare, err := c.prepareMessages(grant.sessionID)
	if err != nil || len(prepare) != 2 {
		t.Fatalf("prepare messages=%d err=%v", len(prepare), err)
	}
	for _, outbound := range prepare {
		assertPinned(outbound)
	}
	peer, err := c.authorizeSignal("a", 10, protocol.P2PSignal{SessionID: grant.sessionID, Sequence: 1, Kind: protocol.P2PSignalOffer, SDP: "v=0"})
	if err != nil || peer.clientID != "b" || peer.clientGeneration != 20 || peer.ownerUserID != "owner-a" || peer.ownerEpoch != 7 {
		t.Fatalf("forward participant pin=%+v err=%v", peer, err)
	}
	renewed := c.renew(nil)
	if len(renewed.Outbounds) != 4 {
		t.Fatalf("renew outbounds=%d want=4", len(renewed.Outbounds))
	}
	for _, outbound := range renewed.Outbounds {
		assertPinned(outbound)
	}
	closed := c.closeClients("owner-a", map[string]uint64{"a": 10, "b": 20}, "user_disabled")
	if len(closed) != 1 || len(closed[0].Outbounds) != 2 {
		t.Fatalf("close results=%+v", closed)
	}
	for _, outbound := range closed[0].Outbounds {
		assertPinned(outbound)
	}
}

func TestP2PCoordinatorStatsAreOwnerOnlyAndIdempotent(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	c := newP2PCoordinator(func() time.Time { return now })
	grant, _, _ := c.ensureGrant(p2pGrantSpec{tunnelID: "t1", revision: 1, ingressClientID: "a", targetClientID: "b", ingressGeneration: 10, targetGeneration: 20})
	report := protocol.P2PStatsReport{SessionID: grant.sessionID, GrantID: grant.grantID, TunnelID: "t1", Revision: 1, Epoch: "epoch", Sequence: 1, IngressBytes: 100, EgressBytes: 40}
	if _, _, err := c.acceptStats("a", 10, report); err == nil {
		t.Fatal("ingress client was allowed to report owner traffic")
	}
	in, out, err := c.acceptStats("b", 20, report)
	if err != nil || in != 100 || out != 40 {
		t.Fatalf("first report delta=(%d,%d) err=%v", in, out, err)
	}
	if _, _, err := c.acceptStats("b", 20, report); err == nil {
		t.Fatal("duplicate report accepted")
	}
	report.Sequence = 2
	report.IngressBytes = 125
	report.EgressBytes = 50
	in, out, err = c.acceptStats("b", 20, report)
	if err != nil || in != 25 || out != 10 {
		t.Fatalf("second report delta=(%d,%d) err=%v", in, out, err)
	}
}

func TestP2PCoordinatorAuthorizesCreditDirectionAndCumulativeBounds(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	c := newP2PCoordinator(func() time.Time { return now })
	grant, _, err := c.ensureGrant(p2pGrantSpec{tunnelID: "t1", revision: 1, ingressClientID: "a", targetClientID: "b", ingressGeneration: 10, targetGeneration: 20, totalBPS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	demand := protocol.P2PCreditDemand{SessionID: grant.sessionID, GrantID: grant.grantID, TunnelID: "t1", Revision: 1, Sequence: 1, DesiredBytes: 100}
	if _, err := c.authorizeCreditDemand("b", 20, demand); err == nil {
		t.Fatal("owner was allowed to send ingress demand")
	}
	peer, err := c.authorizeCreditDemand("a", 10, demand)
	if err != nil || peer.clientID != "b" {
		t.Fatalf("valid demand peer=%+v err=%v", peer, err)
	}
	credit := protocol.P2PCreditGrant{SessionID: grant.sessionID, GrantID: grant.grantID, TunnelID: "t1", Revision: 1, Sequence: 1, GrantedBytes: 101}
	if _, err := c.authorizeCreditGrant("b", 20, credit); err == nil {
		t.Fatal("grant exceeding demand accepted")
	}
	credit.GrantedBytes = 50
	peer, err = c.authorizeCreditGrant("b", 20, credit)
	if err != nil || peer.clientID != "a" {
		t.Fatalf("valid grant peer=%+v err=%v", peer, err)
	}
}

func TestP2PCoordinatorValidatesSignalRoleAndCandidateLimits(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	c := newP2PCoordinator(func() time.Time { return now })
	grant, _, err := c.ensureGrant(p2pGrantSpec{tunnelID: "t1", revision: 1, ingressClientID: "a", targetClientID: "b", ingressGeneration: 10, targetGeneration: 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.authorizeSignal("b", 20, protocol.P2PSignal{SessionID: grant.sessionID, Sequence: 1, Kind: protocol.P2PSignalOffer, SDP: "v=0"}); err == nil {
		t.Fatal("answerer was allowed to send an offer")
	}
	for i := 1; i <= p2pCandidatesPerWindow; i++ {
		signal := protocol.P2PSignal{SessionID: grant.sessionID, Sequence: uint64(i), Kind: protocol.P2PSignalCandidate, Candidate: "candidate:1"}
		if _, err := c.authorizeSignal("a", 10, signal); err != nil {
			t.Fatalf("candidate %d rejected: %v", i, err)
		}
	}
	if _, err := c.authorizeSignal("a", 10, protocol.P2PSignal{SessionID: grant.sessionID, Sequence: p2pCandidatesPerWindow + 1, Kind: protocol.P2PSignalCandidate, Candidate: "candidate:1"}); err == nil {
		t.Fatal("candidate rate limit was not enforced")
	}
	now = now.Add(p2pCandidateWindow)
	for i := p2pCandidatesPerWindow + 1; i <= protocol.P2PMaxCandidates; i++ {
		signal := protocol.P2PSignal{SessionID: grant.sessionID, Sequence: uint64(i + 1), Kind: protocol.P2PSignalCandidate, Candidate: "candidate:1"}
		if _, err := c.authorizeSignal("a", 10, signal); err != nil {
			t.Fatalf("candidate %d after window rejected: %v", i, err)
		}
		if (i-p2pCandidatesPerWindow)%p2pCandidatesPerWindow == 0 {
			now = now.Add(p2pCandidateWindow)
		}
	}
	if _, err := c.authorizeSignal("a", 10, protocol.P2PSignal{SessionID: grant.sessionID, Sequence: protocol.P2PMaxCandidates + 2, Kind: protocol.P2PSignalCandidate, Candidate: "candidate:1"}); err == nil {
		t.Fatal("candidate session limit was not enforced")
	}
}

func TestP2PCoordinatorRejectsInvalidAndReplayedStatus(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	c := newP2PCoordinator(func() time.Time { return now })
	grant, _, _ := c.ensureGrant(p2pGrantSpec{tunnelID: "t1", revision: 1, ingressClientID: "a", targetClientID: "b", ingressGeneration: 10, targetGeneration: 20})
	status := protocol.P2PSessionStatus{SessionID: grant.sessionID, Sequence: 1, State: protocol.P2PStateConnected}
	if _, err := c.recordReady("a", 10, status); err != nil {
		t.Fatalf("valid status rejected: %v", err)
	}
	if _, err := c.recordReady("a", 10, status); err == nil {
		t.Fatal("replayed status accepted")
	}
	status.Sequence = 2
	status.State = protocol.P2PStateClosed
	if _, err := c.recordReady("a", 10, status); err == nil {
		t.Fatal("client-reported closed status accepted")
	}
}

func TestP2PCoordinatorAcceptsOneFinalOwnerStatsReportAfterSessionClose(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	c := newP2PCoordinator(func() time.Time { return now })
	grant, _, _ := c.ensureGrant(p2pGrantSpec{tunnelID: "t1", revision: 1, ingressClientID: "a", targetClientID: "b", ingressGeneration: 10, targetGeneration: 20})
	initial := protocol.P2PStatsReport{SessionID: grant.sessionID, GrantID: grant.grantID, TunnelID: "t1", Revision: 1, Epoch: "epoch", Sequence: 1, IngressBytes: 10, EgressBytes: 4}
	if _, _, err := c.acceptStats("b", 20, initial); err != nil {
		t.Fatal(err)
	}
	_ = c.closeSession(grant.sessionID, "failed")
	final := initial
	final.Sequence = 2
	final.IngressBytes = 17
	final.EgressBytes = 9
	ingress, egress, err := c.acceptStats("b", 20, final)
	if err != nil || ingress != 7 || egress != 5 {
		t.Fatalf("final delta=(%d,%d) err=%v", ingress, egress, err)
	}
	if _, _, err := c.acceptStats("b", 20, final); err == nil {
		t.Fatal("replayed final stats accepted")
	}
	if _, _, err := c.acceptStats("a", 10, protocol.P2PStatsReport{SessionID: grant.sessionID, GrantID: grant.grantID, TunnelID: "t1", Revision: 1, Epoch: "epoch", Sequence: 3, IngressBytes: 18, EgressBytes: 10}); err == nil {
		t.Fatal("non-owner final stats accepted")
	}
	now = now.Add(p2pFinalStatsGrace)
	final.Sequence = 3
	final.IngressBytes++
	if _, _, err := c.acceptStats("b", 20, final); err == nil {
		t.Fatal("expired final stats grace accepted")
	}
}

func TestHandleP2PStatsAcceptsFinalReportFromClosingOwner(t *testing.T) {
	s := New(0)
	grant, _, err := s.p2p.ensureGrant(p2pGrantSpec{
		tunnelID: "t1", revision: 1,
		ingressClientID: "a", targetClientID: "b",
		ingressGeneration: 10, targetGeneration: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.p2p.closeSession(grant.sessionID, "participant offline")

	report := protocol.P2PStatsReport{
		SessionID: grant.sessionID, GrantID: grant.grantID,
		TunnelID: "t1", Revision: 1, Epoch: "epoch", Sequence: 1,
		IngressBytes: 17, EgressBytes: 9,
	}
	msg, err := protocol.NewMessage(protocol.MsgTypeP2PStatsReport, report)
	if err != nil {
		t.Fatal(err)
	}
	s.handleP2PStatsMessage(&ClientConn{ID: "b", generation: 20, state: clientStateClosing}, *msg)

	s.p2p.mu.Lock()
	cursor := s.p2p.closedStats[grant.grantID].cursor
	s.p2p.mu.Unlock()
	if cursor.sequence != report.Sequence || cursor.ingress != report.IngressBytes || cursor.egress != report.EgressBytes {
		t.Fatalf("final stats cursor=%+v want sequence=%d ingress=%d egress=%d", cursor, report.Sequence, report.IngressBytes, report.EgressBytes)
	}
}

type archivedP2PStatsControlFixture struct {
	server      *Server
	gate        *userLifecycleGate
	oldClient   *ClientConn
	replacement *ClientConn
	grant       p2pGrant
	report      protocol.P2PStatsReport
	message     protocol.Message
}

func newArchivedP2PStatsControlFixture(t *testing.T) archivedP2PStatsControlFixture {
	t.Helper()

	s := newUnifiedE2ETestServer(t)
	owner, err := s.auth.adminStore.CreateUser("p2p-final-accounting-owner", "Password123")
	if err != nil {
		t.Fatalf("create final-accounting owner: %v", err)
	}
	ingress, err := s.auth.adminStore.GetOrCreateClientForUser(owner.ID, "p2p-final-accounting-ingress", protocol.ClientInfo{Hostname: "p2p-final-accounting-ingress"}, "127.0.0.1")
	if err != nil {
		t.Fatalf("register final-accounting ingress: %v", err)
	}
	target, err := s.auth.adminStore.GetOrCreateClientForUser(owner.ID, "p2p-final-accounting-target", protocol.ClientInfo{Hostname: "p2p-final-accounting-target"}, "127.0.0.2")
	if err != nil {
		t.Fatalf("register final-accounting target: %v", err)
	}

	stored := testStoredC2CTunnelForReconcile(
		"p2p-final-accounting-tunnel",
		"p2p-final-accounting",
		protocol.ProxyDesiredStateRunning,
		protocol.ProxyRuntimeStateExposed,
		24441,
	)
	stored.ClientID = target.ID
	stored.OwnerClientID = target.ID
	stored.OwnerUserID = owner.ID
	stored.Ingress.ClientID = ingress.ID
	stored.Target.ClientID = target.ID
	stored.TransportPolicy = protocol.TransportPolicyDirectPreferred
	if _, err := s.store.AddTunnelForUser(owner.ID, stored, nil); err != nil {
		t.Fatalf("add final-accounting tunnel: %v", err)
	}

	gate := s.lifecycleGate(owner.ID)
	grant, _, err := s.p2p.ensureGrant(p2pGrantSpec{
		tunnelID: stored.ID, revision: stored.Revision,
		ownerUserID: owner.ID, ownerEpoch: gate.epoch,
		ingressClientID: ingress.ID, targetClientID: target.ID,
		ingressGeneration: 10, targetGeneration: 20,
	})
	if err != nil {
		t.Fatalf("ensure final-accounting grant: %v", err)
	}
	s.p2p.closeSession(grant.sessionID, "data_session_closed")

	oldClient := &ClientConn{
		ID: target.ID, OwnerUserID: owner.ID, OwnerEpoch: gate.epoch,
		generation: 20, state: clientStateClosing,
	}
	replacement := &ClientConn{
		ID: target.ID, OwnerUserID: owner.ID, OwnerEpoch: gate.epoch,
		generation: 21, state: clientStateLive,
	}
	report := protocol.P2PStatsReport{
		SessionID: grant.sessionID, GrantID: grant.grantID,
		TunnelID: stored.ID, Revision: stored.Revision,
		Epoch: grant.sessionID + "." + grant.grantID, Sequence: 1,
		IngressBytes: 24, EgressBytes: 24,
	}
	message, err := protocol.NewMessage(protocol.MsgTypeP2PStatsReport, report)
	if err != nil {
		t.Fatalf("build final-accounting report: %v", err)
	}
	return archivedP2PStatsControlFixture{
		server: s, gate: gate, oldClient: oldClient, replacement: replacement,
		grant: grant, report: report, message: *message,
	}
}

func (f archivedP2PStatsControlFixture) cursor(t *testing.T) p2pStatsCursor {
	t.Helper()
	f.server.p2p.mu.Lock()
	defer f.server.p2p.mu.Unlock()
	closed, ok := f.server.p2p.closedStats[f.grant.grantID]
	if !ok {
		t.Fatalf("archived stats grant %q is missing", f.grant.grantID)
	}
	return closed.cursor
}

func (f archivedP2PStatsControlFixture) drainDirectTraffic() (uint64, uint64) {
	var ingress, egress uint64
	for _, delta := range f.server.trafficAccumulator.Drain() {
		if delta.TunnelID == f.report.TunnelID && delta.Revision == f.report.Revision && delta.Transport == protocol.ActualTransportPeerDirect {
			ingress += delta.IngressBytes
			egress += delta.EgressBytes
		}
	}
	return ingress, egress
}

func TestHandleControlP2PStatsAcceptsArchivedGenerationAcrossPublicationStates(t *testing.T) {
	for _, publicationState := range []string{"closing_current", "removed", "replaced_generation"} {
		t.Run(publicationState, func(t *testing.T) {
			fixture := newArchivedP2PStatsControlFixture(t)
			switch publicationState {
			case "closing_current":
				fixture.server.clients.Store(fixture.oldClient.ID, fixture.oldClient)
			case "replaced_generation":
				fixture.server.clients.Store(fixture.replacement.ID, fixture.replacement)
			}

			fixture.server.handleControlMessage(fixture.oldClient, fixture.message)
			cursor := fixture.cursor(t)
			if cursor.sequence != fixture.report.Sequence || cursor.ingress != fixture.report.IngressBytes || cursor.egress != fixture.report.EgressBytes {
				t.Fatalf("archived stats cursor=%+v want sequence=%d ingress=%d egress=%d", cursor, fixture.report.Sequence, fixture.report.IngressBytes, fixture.report.EgressBytes)
			}
			if ingress, egress := fixture.drainDirectTraffic(); ingress != fixture.report.IngressBytes || egress != fixture.report.EgressBytes {
				t.Fatalf("direct traffic after final report=(%d,%d) want=(%d,%d)", ingress, egress, fixture.report.IngressBytes, fixture.report.EgressBytes)
			}

			fixture.server.handleControlMessage(fixture.oldClient, fixture.message)
			if replayCursor := fixture.cursor(t); replayCursor != cursor {
				t.Fatalf("replayed final report changed cursor: before=%+v after=%+v", cursor, replayCursor)
			}
			if ingress, egress := fixture.drainDirectTraffic(); ingress != 0 || egress != 0 {
				t.Fatalf("replayed final report duplicated traffic: got=(%d,%d)", ingress, egress)
			}
		})
	}
}

func TestHandleControlP2PStatsRejectsArchivedOwnerEpoch(t *testing.T) {
	fixture := newArchivedP2PStatsControlFixture(t)
	fixture.server.clients.Store(fixture.oldClient.ID, fixture.oldClient)
	fixture.gate.mu.Lock()
	fixture.gate.epoch++
	fixture.gate.mu.Unlock()

	fixture.server.handleControlMessage(fixture.oldClient, fixture.message)
	if cursor := fixture.cursor(t); cursor != (p2pStatsCursor{}) {
		t.Fatalf("stale owner epoch consumed archived report: cursor=%+v", cursor)
	}
	if ingress, egress := fixture.drainDirectTraffic(); ingress != 0 || egress != 0 {
		t.Fatalf("stale owner epoch recorded traffic: got=(%d,%d)", ingress, egress)
	}
}
