package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"netsgo/pkg/protocol"
)

const defaultUserConvergenceTimeout = 10 * time.Second

var (
	ErrUserLifecycleEpochChanged = errors.New("user lifecycle epoch changed")
	ErrUserConvergenceIncomplete = errors.New("user runtime convergence incomplete")
	ErrResourceSessionRevoked    = errors.New("resource session expired or revoked")
)

// userLifecycleGate is retained for the lifetime of a Server. Keeping the
// entry stable prevents delete/recreate races from splitting one user's
// lifecycle across different locks.
type userLifecycleGate struct {
	mu    sync.RWMutex
	epoch uint64
}

func (s *Server) lifecycleGate(userID string) *userLifecycleGate {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	gate := &userLifecycleGate{epoch: 1}
	actual, _ := s.userLifecycleLocks.LoadOrStore(userID, gate)
	return actual.(*userLifecycleGate)
}

func (s *Server) runUserLifecycleHook(stage, userID string) {
	if s != nil && s.userLifecycleHook != nil {
		s.userLifecycleHook(stage, userID)
	}
}

// acquireUserLifecycleRead serializes the final publication of user-owned
// runtime state with disable/enable/delete. expectedEpoch is zero when the
// caller is capturing a fresh snapshot.
func (s *Server) acquireUserLifecycleRead(userID string, expectedEpoch uint64, requireOperational bool) (uint64, func(), error) {
	gate := s.lifecycleGate(userID)
	if gate == nil {
		return 0, func() {}, ErrUserNotFound
	}
	s.runUserLifecycleHook("before_read_gate", userID)
	gate.mu.RLock()
	release := gate.mu.RUnlock
	s.runUserLifecycleHook("after_read_gate", userID)
	if expectedEpoch != 0 && gate.epoch != expectedEpoch {
		release()
		return 0, func() {}, ErrUserLifecycleEpochChanged
	}
	if requireOperational {
		if err := s.ensureClientOwnerOperational(userID); err != nil {
			release()
			return 0, func() {}, err
		}
	}
	return gate.epoch, release, nil
}

func (s *Server) clientLifecycleCurrentLocked(client *ClientConn) bool {
	if client == nil || client.OwnerUserID == "" {
		return client != nil
	}
	gate := s.lifecycleGate(client.OwnerUserID)
	return gate != nil && gate.epoch == client.OwnerEpoch
}

func (s *Server) acquireStoredTunnelLifecycle(stored StoredTunnel, expectedEpoch uint64) (uint64, func(), error) {
	if strings.TrimSpace(stored.OwnerUserID) == "" {
		return 0, func() {}, nil
	}
	return s.acquireUserLifecycleRead(stored.OwnerUserID, expectedEpoch, true)
}

func (s *Server) withStoredTunnelPublication(stored StoredTunnel, expectedEpoch uint64, publish func() error) error {
	_, releaseGate, err := s.acquireStoredTunnelLifecycle(stored, expectedEpoch)
	if err != nil {
		return err
	}
	defer releaseGate()
	s.clientTunnelMutationMu.Lock()
	defer s.clientTunnelMutationMu.Unlock()
	releaseRuntimeOperation := s.tunnelRuntimeOps.lock(tunnelRuntimeOperationKey(stored.ID, stored.OwnerClientID, stored.Name))
	defer releaseRuntimeOperation()
	return publish()
}

func (s *Server) withClientRuntimePublication(client *ClientConn, publish func()) bool {
	if client == nil {
		return false
	}
	if client.OwnerUserID == "" {
		publish()
		return true
	}
	_, releaseGate, err := s.acquireUserLifecycleRead(client.OwnerUserID, client.OwnerEpoch, true)
	if err != nil {
		return false
	}
	defer releaseGate()
	s.clientTunnelMutationMu.Lock()
	defer s.clientTunnelMutationMu.Unlock()
	current, ok := s.clients.Load(client.ID)
	if !ok || current != client || !client.isLive() || !s.clientLifecycleCurrentLocked(client) {
		return false
	}
	publish()
	return true
}

// withClientFinalAccountingPublication accepts generation-pinned accounting
// that was already queued on an authenticated control connection before that
// logical client session began closing. Unlike runtime publication, a final
// report must not require the ClientConn to remain live or current: normal data
// channel teardown archives the P2P grant before the control loop necessarily
// dispatches the queued report. The coordinator still authorizes the exact
// client generation, grant, epoch, and monotonic sequence.
func (s *Server) withClientFinalAccountingPublication(client *ClientConn, publish func()) bool {
	if client == nil {
		return false
	}
	if client.OwnerUserID == "" {
		publish()
		return true
	}
	_, releaseGate, err := s.acquireUserLifecycleRead(client.OwnerUserID, client.OwnerEpoch, true)
	if err != nil {
		return false
	}
	defer releaseGate()
	s.clientTunnelMutationMu.Lock()
	defer s.clientTunnelMutationMu.Unlock()
	if !s.clientLifecycleCurrentLocked(client) {
		return false
	}
	publish()
	return true
}

func (s *Server) newUserConvergenceContext() (context.Context, context.CancelFunc) {
	timeout := defaultUserConvergenceTimeout
	if s != nil && s.userConvergenceTimeout > 0 {
		timeout = s.userConvergenceTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	if s == nil || s.done == nil {
		return ctx, cancel
	}
	select {
	case <-s.done:
		cancel()
		return ctx, cancel
	default:
	}
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func checkUserConvergenceContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrUserConvergenceIncomplete, err)
	}
	return nil
}

// convergeUserRuntime is called while the target user's write gate is held.
// It is synchronous and idempotent: retries clean any state left by an earlier
// timeout without emitting another user_disabled state transition.
func (s *Server) convergeUserRuntime(ctx context.Context, userID string) error {
	if err := checkUserConvergenceContext(ctx); err != nil {
		return err
	}
	if s.userConvergenceHook != nil {
		if err := s.userConvergenceHook(ctx, userID); err != nil {
			return fmt.Errorf("%w: %v", ErrUserConvergenceIncomplete, err)
		}
	}

	// Projection retries captured before the lifecycle epoch changed must not
	// survive a successful convergence. A dequeued retry is also removed from
	// the logical queue here; its captured epoch prevents its local copy from
	// committing after the write gate is released.
	s.cancelOwnedP2PProjectionRetries(userID, nil)

	s.cancelSSEForUser(userID, "user_disabled")

	registeredClientIDs := make(map[string]struct{})
	if s.auth != nil && s.auth.adminStore != nil {
		clients, err := s.auth.adminStore.GetRegisteredClientsForUser(userID)
		if err != nil {
			return fmt.Errorf("%w: list owned clients: %v", ErrUserConvergenceIncomplete, err)
		}
		for _, client := range clients {
			registeredClientIDs[client.ID] = struct{}{}
		}
	}

	type sessionRef struct {
		clientID   string
		generation uint64
	}
	refs := make([]sessionRef, 0)
	s.RangeClients(func(clientID string, client *ClientConn) bool {
		if client.OwnerUserID == userID {
			refs = append(refs, sessionRef{clientID: clientID, generation: client.generation})
			registeredClientIDs[clientID] = struct{}{}
		}
		return true
	})
	participants := make(map[string]uint64, len(refs))
	for _, ref := range refs {
		participants[ref.clientID] = ref.generation
	}
	if s.p2p != nil {
		results := s.p2p.closeClients(userID, participants, "user_disabled")
		for _, result := range results {
			outbounds := result.Outbounds
			result.Outbounds = nil
			s.applyP2PLifecycleResult(result)
			if err := s.sendP2PConvergenceOutbounds(ctx, userID, outbounds); err != nil {
				return err
			}
		}
	}
	// Tear down every owned control transport before the normal invalidation
	// path starts sending P2P/tunnel cleanup notices. All endpoint clients for
	// a user-owned tunnel have the same owner, so those notices then fail fast
	// instead of letting a slow network writer outlive the convergence context.
	// ClientConn keeps pointer teardown separate from writer serialization, so
	// Close can also interrupt a write that began before disable acquired the
	// user gate.
	s.clientTunnelMutationMu.Lock()
	for _, ref := range refs {
		if err := checkUserConvergenceContext(ctx); err != nil {
			s.clientTunnelMutationMu.Unlock()
			return err
		}
		value, ok := s.clients.Load(ref.clientID)
		if !ok {
			continue
		}
		client := value.(*ClientConn)
		if client.generation != ref.generation {
			continue
		}
		if conn := client.detachControlConn(); conn != nil {
			_ = conn.Close()
		}
	}
	s.clientTunnelMutationMu.Unlock()
	for _, ref := range refs {
		if err := checkUserConvergenceContext(ctx); err != nil {
			return err
		}
		s.invalidateLogicalSessionIfCurrent(ref.clientID, ref.generation, "user_disabled")
	}

	var ownedTunnels []StoredTunnel
	if s.store != nil {
		var err error
		ownedTunnels, err = s.store.GetTunnelsByUserID(userID)
		if err != nil {
			return fmt.Errorf("%w: list owned tunnels: %v", ErrUserConvergenceIncomplete, err)
		}
	}
	ownedTunnelIDs := make(map[string]struct{}, len(ownedTunnels))
	for _, stored := range ownedTunnels {
		ownedTunnelIDs[stored.ID] = struct{}{}
		if err := checkUserConvergenceContext(ctx); err != nil {
			return err
		}
		registeredClientIDs[stored.OwnerClientID] = struct{}{}
		registeredClientIDs[stored.Ingress.ClientID] = struct{}{}
		registeredClientIDs[stored.Target.ClientID] = struct{}{}
		if err := s.unprovisionStoredUnifiedTunnel(stored, "owner_disabled", true); err != nil {
			return fmt.Errorf("%w: unprovision tunnel %s: %v", ErrUserConvergenceIncomplete, stored.ID, err)
		}
		if s.unifiedRuntime != nil {
			s.unifiedRuntime.clearTunnelIssues(stored.ID, stored.Revision)
		}
		if stored.DesiredState == protocol.ProxyDesiredStateRunning {
			if err := s.updateStoredTunnelRuntime(stored, protocol.ProxyRuntimeStateOffline, ""); err != nil {
				return fmt.Errorf("%w: project tunnel %s offline: %v", ErrUserConvergenceIncomplete, stored.ID, err)
			}
		}
	}
	// Include tunnel identity in the second pass so malformed legacy retry
	// entries without a usable owner cannot escape lifecycle cleanup.
	s.cancelOwnedP2PProjectionRetries(userID, ownedTunnelIDs)

	if err := checkUserConvergenceContext(ctx); err != nil {
		return err
	}
	if residual := s.userRuntimeResidual(userID, registeredClientIDs, ownedTunnels, ownedTunnelIDs); residual != "" {
		return fmt.Errorf("%w: %s", ErrUserConvergenceIncomplete, residual)
	}
	return nil
}

func (s *Server) userRuntimeResidual(userID string, clientIDs map[string]struct{}, tunnels []StoredTunnel, tunnelIDs map[string]struct{}) string {
	clientResidual := false
	s.RangeClients(func(_ string, client *ClientConn) bool {
		if client.OwnerUserID == userID {
			clientResidual = true
			return false
		}
		return true
	})
	if clientResidual {
		return "client session remains published"
	}

	if registry := s.getSSEConnectionRegistry(); registry != nil {
		registry.mu.Lock()
		remaining := len(registry.byUser[userID])
		registry.mu.Unlock()
		if remaining != 0 {
			return "SSE connection remains registered"
		}
	}

	if s.c2c != nil {
		s.c2c.mu.RLock()
		for _, tunnel := range tunnels {
			if _, ok := s.c2c.runtimes[tunnel.ID]; ok {
				s.c2c.mu.RUnlock()
				return "client relay runtime remains published"
			}
		}
		s.c2c.mu.RUnlock()
	}

	if s.p2p != nil {
		s.p2p.mu.Lock()
		for _, session := range s.p2p.byID {
			if _, ok := clientIDs[session.clientA]; ok {
				s.p2p.mu.Unlock()
				return "P2P session remains published"
			}
			if _, ok := clientIDs[session.clientB]; ok {
				s.p2p.mu.Unlock()
				return "P2P session remains published"
			}
		}
		s.p2p.mu.Unlock()
	}
	if retries := s.ownedP2PProjectionRetryResidual(userID, tunnelIDs); retries.total() != 0 {
		return fmt.Sprintf("P2P projection retries remain queued=%d in_flight=%d", retries.Queued, retries.InFlight)
	}
	return ""
}

func (s *Server) recordUserConvergenceIncomplete(target User, actor ActivityActor, err error) {
	slog.Warn("User runtime convergence incomplete",
		"user_id", target.ID,
		"error", err,
		"module", "user_lifecycle",
	)
	if s.activityStore == nil || target.ID == "" {
		return
	}
	spec := adminActivitySpec("user_convergence_incomplete", actor, ActivitySummaryArgs{ResourceName: target.Username})
	spec.Severity = ActivitySeverityWarning
	spec.ScopeUserID = target.ID
	spec.SubjectUserID = target.ID
	activityID, appendErr := s.activityStore.Append(spec)
	if appendErr != nil {
		slog.Warn("Failed to persist user convergence warning activity", "user_id", target.ID, "error", appendErr)
		return
	}
	s.publishActivityID(activityID)
}
