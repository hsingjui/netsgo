package server

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"netsgo/pkg/protocol"
)

func collectP2PControlTypes(conn *websocket.Conn) (<-chan string, <-chan struct{}) {
	types := make(chan string, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(types)
		for {
			var message protocol.Message
			if err := conn.ReadJSON(&message); err != nil {
				return
			}
			types <- message.Type
		}
	}()
	return types, done
}

func TestP2PRenewCapturedBeforeDisableCannotSendAfterEpochBump(t *testing.T) {
	s, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()

	owner, err := s.auth.adminStore.CreateUser("p2p-epoch-owner", "Password123")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	gate := s.lifecycleGate(owner.ID)

	peerA, serverA := newTestWebSocketPair(t)
	peerB, serverB := newTestWebSocketPair(t)
	defer mustClose(t, peerA)
	defer mustClose(t, peerB)
	clientA := &ClientConn{ID: "p2p-epoch-a", OwnerUserID: owner.ID, OwnerEpoch: gate.epoch, generation: 10, state: clientStateLive, conn: serverA, proxies: make(map[string]*ProxyTunnel)}
	clientB := &ClientConn{ID: "p2p-epoch-b", OwnerUserID: owner.ID, OwnerEpoch: gate.epoch, generation: 20, state: clientStateLive, conn: serverB, proxies: make(map[string]*ProxyTunnel)}
	s.clients.Store(clientA.ID, clientA)
	s.clients.Store(clientB.ID, clientB)

	_, _, err = s.p2p.ensureGrant(p2pGrantSpec{
		tunnelID: "p2p-epoch-tunnel", revision: 1,
		ownerUserID: owner.ID, ownerEpoch: gate.epoch,
		ingressClientID: clientA.ID, targetClientID: clientB.ID,
		ingressGeneration: clientA.generation, targetGeneration: clientB.generation,
	})
	if err != nil {
		t.Fatalf("ensure grant: %v", err)
	}
	renewed := s.p2p.renew(func(string, uint64) bool { return true })

	typesA, readerDoneA := collectP2PControlTypes(peerA)
	typesB, readerDoneB := collectP2PControlTypes(peerB)
	entered := make(chan struct{})
	resume := make(chan struct{})
	var once sync.Once
	s.userLifecycleHook = func(stage, userID string) {
		if stage == "p2p_outbound_before_gate" && userID == owner.ID {
			once.Do(func() {
				close(entered)
				<-resume
			})
		}
	}
	sendDone := make(chan struct{})
	go func() {
		s.sendP2POutbounds(renewed.Outbounds)
		close(sendDone)
	}()
	<-entered

	disable := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+owner.ID+"/disable", adminToken, nil)
	if disable.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", disable.Code, disable.Body.String())
	}
	close(resume)
	select {
	case <-sendDone:
	case <-time.After(3 * time.Second):
		t.Fatal("stale P2P renew sender did not finish")
	}
	select {
	case <-readerDoneA:
	case <-time.After(3 * time.Second):
		t.Fatal("participant A control connection was not detached")
	}
	select {
	case <-readerDoneB:
	case <-time.After(3 * time.Second):
		t.Fatal("participant B control connection was not detached")
	}

	for participant, observed := range map[string]<-chan string{"A": typesA, "B": typesB} {
		sawClosed := false
		for messageType := range observed {
			if messageType == protocol.MsgTypeP2PLease || messageType == protocol.MsgTypeP2PTunnelGrant {
				t.Fatalf("participant %s received stale renew message %q after disable", participant, messageType)
			}
			if messageType == protocol.MsgTypeP2PClosed {
				sawClosed = true
			}
		}
		if !sawClosed {
			t.Fatalf("participant %s did not receive bounded P2P close before detach", participant)
		}
	}
}

func TestEnsureP2PDoesNotAcquireOutboundGateInline(t *testing.T) {
	s, _, _, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()
	owner, err := s.auth.adminStore.CreateUser("p2p-nested-read-owner", "Password123")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	gate := s.lifecycleGate(owner.ID)
	caps := protocol.DefaultClientCapabilities()
	ingress := &ClientConn{
		ID: "p2p-nested-read-a", OwnerUserID: owner.ID, OwnerEpoch: gate.epoch,
		generation: 10, state: clientStateLive, Info: protocol.ClientInfo{Capabilities: &caps},
		proxies: make(map[string]*ProxyTunnel),
	}
	target := &ClientConn{
		ID: "p2p-nested-read-b", OwnerUserID: owner.ID, OwnerEpoch: gate.epoch,
		generation: 20, state: clientStateLive, Info: protocol.ClientInfo{Capabilities: &caps},
		proxies: make(map[string]*ProxyTunnel),
	}
	s.clients.Store(ingress.ID, ingress)
	s.clients.Store(target.ID, target)

	entered := make(chan struct{})
	resume := make(chan struct{})
	var once sync.Once
	s.userLifecycleHook = func(stage, userID string) {
		if stage == "p2p_outbound_before_gate" && userID == owner.ID {
			once.Do(func() {
				close(entered)
				<-resume
			})
		}
	}

	stored := StoredTunnel{
		ProxyNewRequest: protocol.ProxyNewRequest{ID: "p2p-nested-read-tunnel", Name: "p2p-nested-read", Type: protocol.ProxyTypeTCP},
		OwnerUserID:     owner.ID, OwnerClientID: target.ID, ClientID: target.ID,
		Revision: 1, TransportPolicy: protocol.TransportPolicyDirectPreferred,
		Ingress: EndpointSpec{ClientID: ingress.ID}, Target: EndpointSpec{ClientID: target.ID},
	}
	gate.mu.RLock()
	returned := make(chan error, 1)
	go func() { returned <- s.ensureP2PForTunnel(stored, ingress, target) }()
	select {
	case err := <-returned:
		if err != nil {
			gate.mu.RUnlock()
			t.Fatalf("ensure P2P: %v", err)
		}
	case <-time.After(3 * time.Second):
		gate.mu.RUnlock()
		t.Fatal("ensure P2P waited for an inline outbound lifecycle gate")
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		gate.mu.RUnlock()
		t.Fatal("asynchronous P2P outbound did not reach lifecycle gate")
	}
	gate.mu.RUnlock()
	close(resume)
}

func TestP2PForwardDoesNotReenterLifecycleGateWhileDisablePending(t *testing.T) {
	tests := []struct {
		name        string
		messageType string
	}{
		{name: "signal", messageType: protocol.MsgTypeP2PSignal},
		{name: "credit_demand", messageType: protocol.MsgTypeP2PCreditDemand},
		{name: "credit_grant", messageType: protocol.MsgTypeP2PCreditGrant},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
			defer cleanup()
			owner, err := s.auth.adminStore.CreateUser("p2p-forward-"+tc.name, "Password123")
			if err != nil {
				t.Fatalf("create owner: %v", err)
			}
			gate := s.lifecycleGate(owner.ID)
			peerA, serverA := newTestWebSocketPair(t)
			peerB, serverB := newTestWebSocketPair(t)
			defer mustClose(t, peerA)
			defer mustClose(t, peerB)
			clientA := &ClientConn{
				ID: "p2p-forward-a-" + tc.name, OwnerUserID: owner.ID, OwnerEpoch: gate.epoch,
				generation: 10, state: clientStateLive, conn: serverA, proxies: make(map[string]*ProxyTunnel),
			}
			clientB := &ClientConn{
				ID: "p2p-forward-b-" + tc.name, OwnerUserID: owner.ID, OwnerEpoch: gate.epoch,
				generation: 20, state: clientStateLive, conn: serverB, proxies: make(map[string]*ProxyTunnel),
			}
			s.clients.Store(clientA.ID, clientA)
			s.clients.Store(clientB.ID, clientB)
			grant, _, err := s.p2p.ensureGrant(p2pGrantSpec{
				tunnelID: "p2p-forward-tunnel-" + tc.name, revision: 1,
				ownerUserID: owner.ID, ownerEpoch: gate.epoch,
				ingressClientID: clientA.ID, targetClientID: clientB.ID,
				ingressGeneration: clientA.generation, targetGeneration: clientB.generation,
				totalBPS: 1000,
			})
			if err != nil {
				t.Fatalf("ensure grant: %v", err)
			}

			var sender *ClientConn
			var message *protocol.Message
			switch tc.messageType {
			case protocol.MsgTypeP2PSignal:
				sender = clientA
				message, err = protocol.NewMessage(tc.messageType, protocol.P2PSignal{
					SessionID: grant.sessionID, Sequence: 1, Kind: protocol.P2PSignalOffer, SDP: "v=0",
				})
			case protocol.MsgTypeP2PCreditDemand:
				sender = clientA
				message, err = protocol.NewMessage(tc.messageType, protocol.P2PCreditDemand{
					SessionID: grant.sessionID, GrantID: grant.grantID, TunnelID: grant.tunnelID,
					Revision: grant.revision, Sequence: 1, DesiredBytes: 100,
				})
			case protocol.MsgTypeP2PCreditGrant:
				demand := protocol.P2PCreditDemand{
					SessionID: grant.sessionID, GrantID: grant.grantID, TunnelID: grant.tunnelID,
					Revision: grant.revision, Sequence: 1, DesiredBytes: 100,
				}
				if _, authorizeErr := s.p2p.authorizeCreditDemand(clientA.ID, clientA.generation, demand); authorizeErr != nil {
					t.Fatalf("prime credit demand: %v", authorizeErr)
				}
				sender = clientB
				message, err = protocol.NewMessage(tc.messageType, protocol.P2PCreditGrant{
					SessionID: grant.sessionID, GrantID: grant.grantID, TunnelID: grant.tunnelID,
					Revision: grant.revision, Sequence: 1, GrantedBytes: 50,
				})
			}
			if err != nil {
				t.Fatalf("build forward message: %v", err)
			}

			typesA, readerDoneA := collectP2PControlTypes(peerA)
			typesB, readerDoneB := collectP2PControlTypes(peerB)
			outboundAtGate := make(chan struct{})
			allowOutboundGate := make(chan struct{})
			outboundDone := make(chan struct{})
			var beforeOnce sync.Once
			var afterOnce sync.Once
			s.userLifecycleHook = func(stage, userID string) {
				if userID != owner.ID {
					return
				}
				switch stage {
				case "p2p_outbound_before_gate":
					beforeOnce.Do(func() {
						close(outboundAtGate)
						<-allowOutboundGate
					})
				case "p2p_outbound_after_send":
					afterOnce.Do(func() { close(outboundDone) })
				}
			}

			handlerReturned := make(chan struct{})
			releasePublication := make(chan struct{})
			publicationDone := make(chan bool, 1)
			go func() {
				publicationDone <- s.withClientRuntimePublication(sender, func() {
					switch tc.messageType {
					case protocol.MsgTypeP2PSignal:
						s.handleP2PSignalMessage(sender, *message)
					case protocol.MsgTypeP2PCreditDemand:
						s.handleP2PCreditDemandMessage(sender, *message)
					case protocol.MsgTypeP2PCreditGrant:
						s.handleP2PCreditGrantMessage(sender, *message)
					}
					close(handlerReturned)
					<-releasePublication
				})
			}()
			select {
			case <-handlerReturned:
			case <-time.After(3 * time.Second):
				t.Fatal("P2P forward blocked inline while holding lifecycle publication locks")
			}
			select {
			case <-outboundAtGate:
			case <-time.After(3 * time.Second):
				t.Fatal("asynchronous P2P forward did not reach lifecycle gate")
			}

			disableDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				disableDone <- doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+owner.ID+"/disable", adminToken, nil)
			}()
			close(releasePublication)
			select {
			case published := <-publicationDone:
				if !published {
					t.Fatal("source publication unexpectedly rejected")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("source publication did not release for pending disable")
			}
			select {
			case resp := <-disableDone:
				if resp.Code != http.StatusOK {
					t.Fatalf("disable status=%d body=%s", resp.Code, resp.Body.String())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("disable remained blocked after source publication returned")
			}
			close(allowOutboundGate)
			select {
			case <-outboundDone:
			case <-time.After(3 * time.Second):
				t.Fatal("stale P2P forward did not finish after disable")
			}
			select {
			case <-readerDoneA:
			case <-time.After(3 * time.Second):
				t.Fatal("participant A was not detached")
			}
			select {
			case <-readerDoneB:
			case <-time.After(3 * time.Second):
				t.Fatal("participant B was not detached")
			}
			for participant, observed := range map[string]<-chan string{"A": typesA, "B": typesB} {
				for observedType := range observed {
					if observedType == tc.messageType {
						t.Fatalf("participant %s received stale forwarded message %q after disable", participant, observedType)
					}
				}
			}
		})
	}
}
