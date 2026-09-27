package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"netsgo/pkg/protocol"
)

func sendLifecycleAuthRequest(t *testing.T, conn websocketJSONWriter, key, installID string) {
	t.Helper()
	caps := protocol.DefaultClientCapabilities()
	message, err := protocol.NewMessage(protocol.MsgTypeAuth, protocol.AuthRequest{
		Key:       key,
		InstallID: installID,
		Client: protocol.ClientInfo{
			Hostname:     "lifecycle-client",
			OS:           "linux",
			Arch:         "amd64",
			Version:      "test",
			Capabilities: &caps,
		},
	})
	if err != nil {
		t.Fatalf("build authentication request: %v", err)
	}
	if err := conn.WriteJSON(message); err != nil {
		t.Fatalf("send authentication request: %v", err)
	}
}

type websocketJSONWriter interface {
	WriteJSON(any) error
}

func lifecycleTestClient(userID string, epoch uint64, state clientState) *ClientConn {
	return &ClientConn{
		ID:          generateUUID(),
		OwnerUserID: userID,
		OwnerEpoch:  epoch,
		generation:  1,
		state:       state,
		proxies:     make(map[string]*ProxyTunnel),
	}
}

func TestControlAuthBeforeUserGateCannotPublishAfterDisable(t *testing.T) {
	s, ts, cleanup := setupWSTestNoConn(t)
	defer cleanup()

	owner, err := s.auth.adminStore.CreateUser("auth-before-gate", "Password123")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	const rawKey = "auth-before-gate-key"
	if _, err := s.auth.adminStore.AddAPIKeyForUser(owner.ID, "auth-before-gate", rawKey, []string{"connect"}, nil); err != nil {
		t.Fatalf("create owner API key: %v", err)
	}

	entered := make(chan struct{})
	resume := make(chan struct{})
	var once sync.Once
	s.userLifecycleHook = func(stage, userID string) {
		if stage == "before_read_gate" && userID == owner.ID {
			once.Do(func() {
				close(entered)
				<-resume
			})
		}
	}

	conn := dialControlWSForRuntimeGate(t, ts)
	defer mustClose(t, conn)
	sendLifecycleAuthRequest(t, conn, rawKey, "auth-before-gate-install")
	<-entered

	adminToken := loginAdminTokenLocal(t, ts.Config.Handler, "admin", "password123")
	disable := doMuxRequest(t, ts.Config.Handler, http.MethodPost, "/api/admin/users/"+owner.ID+"/disable", adminToken, nil)
	if disable.Code != http.StatusOK {
		t.Fatalf("disable user status = %d, want %d: %s", disable.Code, http.StatusOK, disable.Body.String())
	}
	close(resume)

	var responseMessage protocol.Message
	if err := conn.ReadJSON(&responseMessage); err != nil {
		t.Fatalf("read authentication response: %v", err)
	}
	var response protocol.AuthResponse
	if err := responseMessage.ParsePayload(&response); err != nil {
		t.Fatalf("parse authentication response: %v", err)
	}
	if response.Success || response.Code != protocol.AuthCodeUserDisabled {
		t.Fatalf("authentication response = %+v, want user-disabled rejection", response)
	}
	if count := countPublishedClients(s); count != 0 {
		t.Fatalf("published clients = %d, want 0", count)
	}
}

func TestControlAuthHoldingUserGateIsRemovedBeforeDisableReturns(t *testing.T) {
	s, ts, cleanup := setupWSTestNoConn(t)
	defer cleanup()

	owner, err := s.auth.adminStore.CreateUser("auth-after-gate", "Password123")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	const rawKey = "auth-after-gate-key"
	if _, err := s.auth.adminStore.AddAPIKeyForUser(owner.ID, "auth-after-gate", rawKey, []string{"connect"}, nil); err != nil {
		t.Fatalf("create owner API key: %v", err)
	}

	entered := make(chan struct{})
	resume := make(chan struct{})
	var once sync.Once
	s.userLifecycleHook = func(stage, userID string) {
		if stage == "after_read_gate" && userID == owner.ID {
			once.Do(func() {
				close(entered)
				<-resume
			})
		}
	}

	conn := dialControlWSForRuntimeGate(t, ts)
	defer mustClose(t, conn)
	sendLifecycleAuthRequest(t, conn, rawKey, "auth-after-gate-install")
	<-entered

	adminToken := loginAdminTokenLocal(t, ts.Config.Handler, "admin", "password123")
	disableDone := make(chan int, 1)
	go func() {
		response := doMuxRequest(t, ts.Config.Handler, http.MethodPost, "/api/admin/users/"+owner.ID+"/disable", adminToken, nil)
		disableDone <- response.Code
	}()
	close(resume)

	if status := <-disableDone; status != http.StatusOK {
		t.Fatalf("disable user status = %d, want %d", status, http.StatusOK)
	}
	if count := countPublishedClients(s); count != 0 {
		t.Fatalf("published clients after disable = %d, want 0", count)
	}
}

func TestDataPublicationBeforeUserGateIsRejectedByDisable(t *testing.T) {
	s, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()

	owner, err := s.auth.adminStore.CreateUser("data-before-gate", "Password123")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	gate := s.lifecycleGate(owner.ID)
	client := lifecycleTestClient(owner.ID, gate.epoch, clientStatePendingData)
	s.clients.Store(client.ID, client)

	entered := make(chan struct{})
	resume := make(chan struct{})
	var once sync.Once
	s.userLifecycleHook = func(stage, userID string) {
		if stage == "before_read_gate" && userID == owner.ID {
			once.Do(func() {
				close(entered)
				<-resume
			})
		}
	}
	promoted := make(chan bool, 1)
	go func() { promoted <- s.promotePendingToLiveIfCurrent(client) }()
	<-entered

	disable := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+owner.ID+"/disable", adminToken, nil)
	if disable.Code != http.StatusOK {
		t.Fatalf("disable user status = %d, want %d: %s", disable.Code, http.StatusOK, disable.Body.String())
	}
	close(resume)
	if <-promoted {
		t.Fatal("stale pending data session was promoted after disable")
	}
	if _, ok := s.clients.Load(client.ID); ok {
		t.Fatal("disabled user's pending session remains published")
	}
}

func TestDataPublicationHoldingUserGateConvergesBeforeDisableReturns(t *testing.T) {
	s, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()

	owner, err := s.auth.adminStore.CreateUser("data-after-gate", "Password123")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	gate := s.lifecycleGate(owner.ID)
	client := lifecycleTestClient(owner.ID, gate.epoch, clientStatePendingData)
	s.clients.Store(client.ID, client)

	entered := make(chan struct{})
	resume := make(chan struct{})
	var once sync.Once
	s.userLifecycleHook = func(stage, userID string) {
		if stage == "after_read_gate" && userID == owner.ID {
			once.Do(func() {
				close(entered)
				<-resume
			})
		}
	}
	promoted := make(chan bool, 1)
	go func() { promoted <- s.promotePendingToLiveIfCurrent(client) }()
	<-entered

	disableDone := make(chan int, 1)
	go func() {
		response := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+owner.ID+"/disable", adminToken, nil)
		disableDone <- response.Code
	}()
	close(resume)

	if !<-promoted {
		t.Fatal("data publication holding the active user's read gate should complete")
	}
	if status := <-disableDone; status != http.StatusOK {
		t.Fatalf("disable user status = %d, want %d", status, http.StatusOK)
	}
	if _, ok := s.clients.Load(client.ID); ok {
		t.Fatal("disable returned while the just-published data session remained")
	}
}

func TestStaleLifecycleEpochCannotRepublishAfterDisableEnable(t *testing.T) {
	s, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()

	owner, err := s.auth.adminStore.CreateUser("stale-epoch", "Password123")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	gate := s.lifecycleGate(owner.ID)
	gate.mu.RLock()
	staleEpoch := gate.epoch
	gate.mu.RUnlock()

	disable := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+owner.ID+"/disable", adminToken, nil)
	if disable.Code != http.StatusOK {
		t.Fatalf("disable user status = %d, want %d: %s", disable.Code, http.StatusOK, disable.Body.String())
	}
	enable := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+owner.ID+"/enable", adminToken, nil)
	if enable.Code != http.StatusOK {
		t.Fatalf("enable user status = %d, want %d: %s", enable.Code, http.StatusOK, enable.Body.String())
	}

	published := false
	err = s.withStoredTunnelPublication(StoredTunnel{
		ProxyNewRequest: protocol.ProxyNewRequest{
			ID:   "stale-epoch-tunnel",
			Name: "stale-epoch-tunnel",
		},
		OwnerUserID: owner.ID,
	}, staleEpoch, func() error {
		published = true
		return nil
	})
	if !errors.Is(err, ErrUserLifecycleEpochChanged) {
		t.Fatalf("stale publication error = %v, want %v", err, ErrUserLifecycleEpochChanged)
	}
	if published {
		t.Fatal("stale reconcile publication ran after disable and enable")
	}
}

func TestStaleLifecycleEpochCannotSendProvisionOrPreflight(t *testing.T) {
	s := New(0)
	const ownerUserID = "stale-control-owner"
	gate := s.lifecycleGate(ownerUserID)
	client := lifecycleTestClient(ownerUserID, gate.epoch, clientStateLive)
	s.clients.Store(client.ID, client)

	gate.mu.Lock()
	gate.epoch++
	gate.mu.Unlock()

	_, err := s.beginClientTunnelProvisionAckWait(client, protocol.TunnelProvisionRequest{
		TunnelID: "stale-provision",
		Revision: 1,
		Role:     protocol.DataStreamRoleTarget,
	})
	if !errors.Is(err, ErrUserLifecycleEpochChanged) {
		t.Fatalf("stale provision error = %v, want %v", err, ErrUserLifecycleEpochChanged)
	}
	s.tunnels.pendingProvisionAckMu.Lock()
	pendingProvision := len(s.tunnels.pendingProvisionAcks)
	s.tunnels.pendingProvisionAckMu.Unlock()
	if pendingProvision != 0 {
		t.Fatalf("stale provision left %d waiter(s)", pendingProvision)
	}

	_, err = s.beginClientTunnelPreflightWait(client, protocol.TunnelPreflightRequest{RequestID: "stale-preflight"})
	if !errors.Is(err, ErrUserLifecycleEpochChanged) {
		t.Fatalf("stale preflight error = %v, want %v", err, ErrUserLifecycleEpochChanged)
	}
	s.tunnels.pendingPreflightMu.Lock()
	pendingPreflight := len(s.tunnels.pendingPreflights)
	s.tunnels.pendingPreflightMu.Unlock()
	if pendingPreflight != 0 {
		t.Fatalf("stale preflight left %d waiter(s)", pendingPreflight)
	}
}

func TestControlWriteDeadlineFailureDetachesConnection(t *testing.T) {
	peer, serverConn := newTestWebSocketPair(t)
	defer mustClose(t, peer)
	defer func() { _ = serverConn.Close() }()
	client := &ClientConn{ID: "deadline-client", conn: serverConn}

	err := client.writeJSONBefore(map[string]string{"payload": "blocked"}, time.Now().Add(-time.Second))
	if err == nil {
		t.Fatal("expired control write deadline unexpectedly succeeded")
	}
	client.mu.Lock()
	attached := client.conn != nil
	client.mu.Unlock()
	if attached {
		t.Fatal("failed control write left the connection attached")
	}
}

func TestSessionManagerRejectsLongLivedAdmissionAfterShutdownStarts(t *testing.T) {
	sessions := newSessionManager()
	release, accepted := sessions.beginLongLivedHandler()
	if !accepted {
		t.Fatal("session manager rejected admission before shutdown")
	}
	sessions.beginShutdown()
	if _, accepted := sessions.beginLongLivedHandler(); accepted {
		t.Fatal("session manager admitted a handler after shutdown started")
	}
	release()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := sessions.waitForLongLivedHandlers(ctx); err != nil {
		t.Fatalf("wait for admitted handler: %v", err)
	}
}

func TestStaleClientRelayGrantCannotOpenOnNewEpochTarget(t *testing.T) {
	s := New(0)
	s.store = newTestTunnelStore(t)
	stored := mustAddStableTunnelForServer(t, s, testClientRelayStoredTunnel(t))
	s.c2c.set(stored)
	gate := s.lifecycleGate(stored.OwnerUserID)

	_, sourceSession := newTestClientRelayDataSession(t)
	oldTargetPeer, oldTargetSession := newTestClientRelayDataSession(t)
	_ = oldTargetPeer
	source := &ClientConn{
		ID:          stored.Ingress.ClientID,
		OwnerUserID: stored.OwnerUserID,
		OwnerEpoch:  gate.epoch,
		generation:  1,
		state:       clientStateLive,
		dataSession: sourceSession,
	}
	target := &ClientConn{
		ID:          stored.Target.ClientID,
		OwnerUserID: stored.OwnerUserID,
		OwnerEpoch:  gate.epoch,
		generation:  2,
		state:       clientStateLive,
		dataSession: oldTargetSession,
	}
	s.clients.Store(source.ID, source)
	s.clients.Store(target.ID, target)
	header := testClientRelayHeader(stored)
	grant, err := s.authorizeClientRelayStream(source, sourceSession, header)
	if err != nil {
		t.Fatalf("authorize old relay stream: %v", err)
	}

	gate.mu.Lock()
	gate.epoch++
	currentEpoch := gate.epoch
	gate.mu.Unlock()
	_, newSourceSession := newTestClientRelayDataSession(t)
	newTargetPeer, newTargetSession := newTestClientRelayDataSession(t)
	s.clients.Store(source.ID, &ClientConn{
		ID:          source.ID,
		OwnerUserID: stored.OwnerUserID,
		OwnerEpoch:  currentEpoch,
		generation:  3,
		state:       clientStateLive,
		dataSession: newSourceSession,
	})
	s.clients.Store(target.ID, &ClientConn{
		ID:          target.ID,
		OwnerUserID: stored.OwnerUserID,
		OwnerEpoch:  currentEpoch,
		generation:  4,
		state:       clientStateLive,
		dataSession: newTargetSession,
	})

	acceptedOnNewTarget := make(chan bool, 1)
	go func() {
		stream, acceptErr := newTargetPeer.Accept()
		if stream != nil {
			_ = stream.Close()
		}
		acceptedOnNewTarget <- acceptErr == nil
	}()
	stream, err := s.openRelayStreamForGrant(grant, header)
	if stream != nil {
		_ = stream.Close()
		t.Fatal("stale relay grant returned a target stream")
	}
	if !errors.Is(err, ErrUserLifecycleEpochChanged) {
		t.Fatalf("stale relay grant error = %v, want %v", err, ErrUserLifecycleEpochChanged)
	}
	s.publishClientRelayStreamOpenFailure(grant, err)
	_ = newTargetSession.Close()
	select {
	case accepted := <-acceptedOnNewTarget:
		if accepted {
			t.Fatal("stale relay grant opened a stream on the new target session")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new target accept did not unblock after session close")
	}

	reloaded, err := s.store.GetTunnelByID(stored.ID)
	if err != nil {
		t.Fatalf("reload tunnel after stale relay: %v", err)
	}
	if reloaded.RuntimeState != stored.RuntimeState || reloaded.Error != stored.Error {
		t.Fatalf("stale relay mutated stored runtime: before=%s/%q after=%s/%q", stored.RuntimeState, stored.Error, reloaded.RuntimeState, reloaded.Error)
	}
	if issues := s.unifiedRuntime.issuesForStoredTunnel(stored, true); len(issues) != 0 {
		t.Fatalf("stale relay published issues: %+v", issues)
	}
}

func TestUserDisableTimeoutRetryConvergesWithoutDuplicateStateActivity(t *testing.T) {
	s, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()
	s.activityStore = s.auth.adminStore.activityStore

	owner, err := s.auth.adminStore.CreateUser("disable-timeout", "Password123")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	s.userConvergenceTimeout = 50 * time.Millisecond
	s.userConvergenceHook = func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}

	first := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+owner.ID+"/disable", adminToken, nil)
	if first.Code != http.StatusServiceUnavailable {
		t.Fatalf("first disable status = %d, want %d: %s", first.Code, http.StatusServiceUnavailable, first.Body.String())
	}
	requireUserAPIErrorCode(t, first.Body.Bytes(), "user_disable_incomplete")
	disabled, err := s.auth.adminStore.GetUser(owner.ID)
	if err != nil {
		t.Fatalf("load disabled owner: %v", err)
	}
	if disabled.Status != UserStatusDisabled {
		t.Fatalf("owner status after timeout = %q, want %q", disabled.Status, UserStatusDisabled)
	}

	s.userConvergenceHook = nil
	second := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+owner.ID+"/disable", adminToken, nil)
	if second.Code != http.StatusOK {
		t.Fatalf("retry disable status = %d, want %d: %s", second.Code, http.StatusOK, second.Body.String())
	}
	assertUserActivityCount(t, s, owner.ID, "user_disabled", 1)
	assertUserActivityCount(t, s, owner.ID, "user_convergence_incomplete", 1)
}

func TestUserEnableConvergenceFailureLeavesUserDisabled(t *testing.T) {
	s, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()
	s.activityStore = s.auth.adminStore.activityStore

	owner, err := s.auth.adminStore.CreateUser("enable-failure", "Password123")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if response := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+owner.ID+"/disable", adminToken, nil); response.Code != http.StatusOK {
		t.Fatalf("disable user status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	s.userConvergenceHook = func(context.Context, string) error { return errors.New("injected cleanup failure") }

	enable := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+owner.ID+"/enable", adminToken, nil)
	if enable.Code != http.StatusServiceUnavailable {
		t.Fatalf("enable status = %d, want %d: %s", enable.Code, http.StatusServiceUnavailable, enable.Body.String())
	}
	requireUserAPIErrorCode(t, enable.Body.Bytes(), "user_disable_incomplete")
	current, err := s.auth.adminStore.GetUser(owner.ID)
	if err != nil {
		t.Fatalf("load owner: %v", err)
	}
	if current.Status != UserStatusDisabled {
		t.Fatalf("owner status after failed enable = %q, want %q", current.Status, UserStatusDisabled)
	}
}

func TestDeleteActiveUserPreservesPublishedClient(t *testing.T) {
	s, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()

	owner, err := s.auth.adminStore.CreateUser("delete-active", "Password123")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	gate := s.lifecycleGate(owner.ID)
	client := lifecycleTestClient(owner.ID, gate.epoch, clientStateLive)
	s.clients.Store(client.ID, client)

	response := doMuxRequest(t, handler, http.MethodDelete, "/api/admin/users/"+owner.ID, adminToken, nil)
	if response.Code != http.StatusConflict {
		t.Fatalf("delete active user status = %d, want %d: %s", response.Code, http.StatusConflict, response.Body.String())
	}
	requireUserAPIErrorCode(t, response.Body.Bytes(), "user_must_be_disabled")
	if current, ok := s.clients.Load(client.ID); !ok || current != client || !client.isLive() {
		t.Fatal("active-user deletion attempt changed the published client")
	}
}

func TestDeleteDirtyDisabledUserRetriesConvergenceBeforeDeletion(t *testing.T) {
	s, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()
	s.activityStore = s.auth.adminStore.activityStore

	owner, err := s.auth.adminStore.CreateUser("delete-dirty", "Password123")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if response := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+owner.ID+"/disable", adminToken, nil); response.Code != http.StatusOK {
		t.Fatalf("disable user status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	gate := s.lifecycleGate(owner.ID)
	client := lifecycleTestClient(owner.ID, gate.epoch, clientStatePendingData)
	s.clients.Store(client.ID, client)
	s.userConvergenceHook = func(context.Context, string) error { return errors.New("injected cleanup failure") }

	first := doMuxRequest(t, handler, http.MethodDelete, "/api/admin/users/"+owner.ID, adminToken, nil)
	if first.Code != http.StatusServiceUnavailable {
		t.Fatalf("first delete status = %d, want %d: %s", first.Code, http.StatusServiceUnavailable, first.Body.String())
	}
	requireUserAPIErrorCode(t, first.Body.Bytes(), "user_disable_incomplete")
	if _, err := s.auth.adminStore.GetUser(owner.ID); err != nil {
		t.Fatalf("failed convergence deleted the user: %v", err)
	}
	if _, ok := s.clients.Load(client.ID); !ok {
		t.Fatal("injected pre-cleanup failure unexpectedly removed the dirty client")
	}

	s.userConvergenceHook = nil
	second := doMuxRequest(t, handler, http.MethodDelete, "/api/admin/users/"+owner.ID, adminToken, nil)
	if second.Code != http.StatusNoContent {
		t.Fatalf("retry delete status = %d, want %d: %s", second.Code, http.StatusNoContent, second.Body.String())
	}
	if _, ok := s.clients.Load(client.ID); ok {
		t.Fatal("retry delete left the dirty client published")
	}
	if _, err := s.auth.adminStore.GetUser(owner.ID); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("load deleted user error = %v, want %v", err, ErrUserNotFound)
	}
}

func TestLifecycleGatesSerializeOneUserWithoutBlockingAnother(t *testing.T) {
	s := New(0)
	first := s.lifecycleGate("first-user")
	second := s.lifecycleGate("second-user")
	first.mu.Lock()
	defer first.mu.Unlock()

	acquired := make(chan struct{})
	go func() {
		second.mu.Lock()
		close(acquired)
		second.mu.Unlock()
	}()
	<-acquired
}

func TestDisabledTargetCannotCreateOrEnableCapabilities(t *testing.T) {
	s, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()

	owner, err := s.auth.adminStore.CreateUser("disabled-capabilities", "Password123")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	target := createUnifiedAPITestClientForUser(t, s, owner.ID, "disabled-capabilities-install", "disabled-capabilities-target")
	key, err := s.auth.adminStore.AddAPIKeyForUser(owner.ID, "disabled-capabilities-key", "disabled-capabilities-secret", []string{"connect"}, nil)
	if err != nil {
		t.Fatalf("create owner key: %v", err)
	}
	if err := s.auth.adminStore.SetAPIKeyActive(key.ID, false); err != nil {
		t.Fatalf("disable owner key: %v", err)
	}
	if response := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+owner.ID+"/disable", adminToken, nil); response.Code != http.StatusOK {
		t.Fatalf("disable owner status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}

	keyCreate := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+owner.ID+"/keys", adminToken, []byte(`{"name":"late-key","permissions":["connect"]}`))
	if keyCreate.Code != http.StatusConflict {
		t.Fatalf("disabled owner key create status = %d, want %d: %s", keyCreate.Code, http.StatusConflict, keyCreate.Body.String())
	}
	requireUserAPIErrorCode(t, keyCreate.Body.Bytes(), "user_disabled")

	keyEnable := doMuxRequest(t, handler, http.MethodPut, "/api/admin/users/"+owner.ID+"/keys/"+key.ID+"/enable", adminToken, nil)
	if keyEnable.Code != http.StatusConflict {
		t.Fatalf("disabled owner key enable status = %d, want %d: %s", keyEnable.Code, http.StatusConflict, keyEnable.Body.String())
	}
	requireUserAPIErrorCode(t, keyEnable.Body.Bytes(), "user_disabled")

	tunnelCreate := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+owner.ID+"/tunnels", adminToken, unifiedCreatePayload("late-tunnel", target.ID, reserveTCPPort(t)))
	if tunnelCreate.Code != http.StatusConflict {
		t.Fatalf("disabled owner tunnel create status = %d, want %d: %s", tunnelCreate.Code, http.StatusConflict, tunnelCreate.Body.String())
	}
	requireUserAPIErrorCode(t, tunnelCreate.Body.Bytes(), "user_disabled")
	tunnels, err := s.store.GetTunnelsByUserID(owner.ID)
	if err != nil {
		t.Fatalf("list disabled owner tunnels: %v", err)
	}
	if len(tunnels) != 0 {
		t.Fatalf("disabled owner persisted tunnels: %+v", tunnels)
	}
}

func TestSelfTunnelCreateCapturedBeforeDisableCannotCommitAfterward(t *testing.T) {
	s, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()

	owner, err := s.auth.adminStore.CreateUser("stale-self-mutation", "Password123")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	ownerToken := loginAdminTokenLocal(t, handler, owner.Username, "Password123")
	target := createUnifiedAPITestClientForUser(t, s, owner.ID, "stale-self-install", "stale-self-target")

	enteredFinalGate := make(chan struct{})
	releaseFinalGate := make(chan struct{})
	var readGateCount atomic.Int32
	s.userLifecycleHook = func(stage, userID string) {
		if stage != "before_read_gate" || userID != owner.ID {
			return
		}
		if readGateCount.Add(1) == 2 {
			close(enteredFinalGate)
			<-releaseFinalGate
		}
	}

	responseCh := doMuxRequestAsync(t, handler, http.MethodPost, "/api/tunnels", ownerToken, unifiedCreatePayload("stale-self-tunnel", target.ID, reserveTCPPort(t)))
	select {
	case <-enteredFinalGate:
	case <-time.After(2 * time.Second):
		t.Fatal("self tunnel create did not reach its final lifecycle gate")
	}

	disable := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+owner.ID+"/disable", adminToken, nil)
	if disable.Code != http.StatusOK {
		t.Fatalf("disable owner status = %d, want %d: %s", disable.Code, http.StatusOK, disable.Body.String())
	}
	close(releaseFinalGate)

	var response *httptest.ResponseRecorder
	select {
	case response = <-responseCh:
	case <-time.After(2 * time.Second):
		t.Fatal("stale self tunnel create did not return")
	}
	if response.Code != http.StatusConflict {
		t.Fatalf("stale self tunnel create status = %d, want %d: %s", response.Code, http.StatusConflict, response.Body.String())
	}
	requireUserAPIErrorCode(t, response.Body.Bytes(), "user_lifecycle_changed")
	tunnels, err := s.store.GetTunnelsByUserID(owner.ID)
	if err != nil {
		t.Fatalf("list owner tunnels: %v", err)
	}
	if len(tunnels) != 0 {
		t.Fatalf("stale self request persisted tunnels after disable: %+v", tunnels)
	}
}

func TestClientMutationsCapturedBeforeDisableCannotCommitAfterward(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   func(string) string
		body   []byte
		assert func(*testing.T, RegisteredClient, RegisteredClient, bool)
	}{
		{
			name:   "display name",
			method: http.MethodPut,
			path:   func(clientID string) string { return "/api/clients/" + clientID + "/display-name" },
			body:   []byte(`{"display_name":"renamed"}`),
			assert: func(t *testing.T, before, after RegisteredClient, exists bool) {
				t.Helper()
				if !exists || after.DisplayName != before.DisplayName {
					t.Fatalf("display-name mutation committed after disable: before=%+v after=%+v exists=%v", before, after, exists)
				}
			},
		},
		{
			name:   "bandwidth",
			method: http.MethodPut,
			path:   func(clientID string) string { return "/api/clients/" + clientID + "/bandwidth-settings" },
			body:   []byte(`{"ingress_bps":128,"egress_bps":256}`),
			assert: func(t *testing.T, before, after RegisteredClient, exists bool) {
				t.Helper()
				if !exists || after.IngressBPS != before.IngressBPS || after.EgressBPS != before.EgressBPS {
					t.Fatalf("bandwidth mutation committed after disable: before=%+v after=%+v exists=%v", before, after, exists)
				}
			},
		},
		{
			name:   "delete",
			method: http.MethodDelete,
			path:   func(clientID string) string { return "/api/clients/" + clientID },
			assert: func(t *testing.T, _, _ RegisteredClient, exists bool) {
				t.Helper()
				if !exists {
					t.Fatal("client deletion committed after disable")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
			defer cleanup()
			owner, err := s.auth.adminStore.CreateUser("client-mutation-"+strings.ReplaceAll(tc.name, " ", "-"), "Password123")
			if err != nil {
				t.Fatal(err)
			}
			ownerToken := loginAdminTokenLocal(t, handler, owner.Username, "Password123")
			client := createUnifiedAPITestClientForUser(t, s, owner.ID, "client-mutation-"+tc.name, "client-mutation")

			enteredFinalGate := make(chan struct{})
			releaseFinalGate := make(chan struct{})
			var readGateCount atomic.Int32
			s.userLifecycleHook = func(stage, userID string) {
				if stage == "before_read_gate" && userID == owner.ID && readGateCount.Add(1) == 2 {
					close(enteredFinalGate)
					<-releaseFinalGate
				}
			}

			responseCh := doMuxRequestAsync(t, handler, tc.method, tc.path(client.ID), ownerToken, tc.body)
			select {
			case <-enteredFinalGate:
			case <-time.After(2 * time.Second):
				t.Fatal("client mutation did not reach its final lifecycle gate")
			}
			disable := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+owner.ID+"/disable", adminToken, nil)
			if disable.Code != http.StatusOK {
				t.Fatalf("disable owner status = %d: %s", disable.Code, disable.Body.String())
			}
			close(releaseFinalGate)

			var response *httptest.ResponseRecorder
			select {
			case response = <-responseCh:
			case <-time.After(2 * time.Second):
				t.Fatal("stale client mutation did not return")
			}
			if response.Code != http.StatusConflict {
				t.Fatalf("stale client mutation status = %d: %s", response.Code, response.Body.String())
			}
			requireUserAPIErrorCode(t, response.Body.Bytes(), "user_lifecycle_changed")
			after, exists := s.auth.adminStore.GetRegisteredClientForUser(owner.ID, client.ID)
			tc.assert(t, client, after, exists)
		})
	}
}

func countPublishedClients(s *Server) int {
	count := 0
	s.RangeClients(func(_ string, _ *ClientConn) bool {
		count++
		return true
	})
	return count
}

func assertUserActivityCount(t *testing.T, s *Server, userID, action string, want int) {
	t.Helper()
	var count int
	if err := s.auth.adminStore.db.QueryRow(`
		SELECT COUNT(*)
		FROM activity_events
		WHERE subject_user_id = ? AND action = ?
	`, userID, action).Scan(&count); err != nil {
		t.Fatalf("count %s activity: %v", action, err)
	}
	if count != want {
		t.Fatalf("%s activity count = %d, want %d", action, count, want)
	}
}
