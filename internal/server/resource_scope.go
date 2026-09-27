package server

import (
	"context"
	"errors"
	"net/http"
)

// ResourceScope is the explicit owner scope injected by routing.  Handlers
// must use it for every resource lookup instead of inferring global access
// from an administrator principal.
type ResourceScope struct {
	OwnerUserID string
	AdminTarget bool
	// SessionID is set only for a principal operating on their own resources.
	// The final mutation gate revalidates it while holding the same lifecycle
	// lock used by every production session-revocation path.
	SessionID string
	// ExpectedEpoch is captured when routing admits the request. Mutations use
	// it at their final commit boundary so a request authenticated before a
	// disable/delete cannot publish after that lifecycle transition.
	ExpectedEpoch uint64
}

type resourceScopeContextKey struct{}

func resourceScopeFromContext(ctx context.Context) (ResourceScope, bool) {
	scope, ok := ctx.Value(resourceScopeContextKey{}).(ResourceScope)
	return scope, ok && scope.OwnerUserID != ""
}

func (s *Server) requireSelfResourceScope(next http.HandlerFunc) http.HandlerFunc {
	return s.RequirePrincipal(func(w http.ResponseWriter, r *http.Request) {
		principal := GetPrincipalFromContext(r.Context())
		if principal == nil {
			writeAPIError(w, http.StatusUnauthorized, "session_expired_or_revoked", "session expired or revoked")
			return
		}
		scope, ok := s.captureResourceScope(w, principal.UserID, false, true)
		if !ok {
			return
		}
		scope.SessionID = principal.SessionID
		ctx := context.WithValue(r.Context(), resourceScopeContextKey{}, scope)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) requireAdminUserResourceScope(next http.HandlerFunc) http.HandlerFunc {
	return s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		user, ok := s.targetUserForRequest(w, r)
		if !ok {
			return
		}
		// Administrators may inspect and remove resources for a disabled user.
		// Individual mutation handlers decide whether operational status is
		// required, but every request still captures an epoch for serialization.
		scope, ok := s.captureResourceScope(w, user.ID, true, false)
		if !ok {
			return
		}
		ctx := context.WithValue(r.Context(), resourceScopeContextKey{}, scope)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAdminSelfResourceScope keeps legacy administrator-only resource
// routes explicit: an administrator using those routes operates on their own
// resources, never on an implicit global collection.
func (s *Server) requireAdminSelfResourceScope(next http.HandlerFunc) http.HandlerFunc {
	return s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		principal := GetPrincipalFromContext(r.Context())
		if principal == nil {
			writeAPIError(w, http.StatusUnauthorized, "session_expired_or_revoked", "session expired or revoked")
			return
		}
		scope, ok := s.captureResourceScope(w, principal.UserID, false, true)
		if !ok {
			return
		}
		scope.SessionID = principal.SessionID
		ctx := context.WithValue(r.Context(), resourceScopeContextKey{}, scope)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAdminSelfSessionMutation serializes security changes that revoke the
// current administrator's sessions with final self-resource mutation gates.
func (s *Server) requireAdminSelfSessionMutation(next http.HandlerFunc) http.HandlerFunc {
	return s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		principal := GetPrincipalFromContext(r.Context())
		if principal == nil {
			writeAPIError(w, http.StatusUnauthorized, "session_expired_or_revoked", "session expired or revoked")
			return
		}
		gate := s.lifecycleGate(principal.UserID)
		if gate == nil {
			writeAPIError(w, http.StatusUnauthorized, "session_expired_or_revoked", "session expired or revoked")
			return
		}
		gate.mu.Lock()
		defer gate.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) captureResourceScope(w http.ResponseWriter, ownerUserID string, adminTarget, requireOperational bool) (ResourceScope, bool) {
	epoch, release, err := s.acquireUserLifecycleRead(ownerUserID, 0, requireOperational)
	if err != nil {
		writeResourceLifecycleError(w, err)
		return ResourceScope{}, false
	}
	release()
	return ResourceScope{OwnerUserID: ownerUserID, AdminTarget: adminTarget, ExpectedEpoch: epoch}, true
}

func (s *Server) acquireResourceMutation(scope ResourceScope, requireOperational bool) (func(), error) {
	_, release, err := s.acquireUserLifecycleRead(scope.OwnerUserID, scope.ExpectedEpoch, requireOperational)
	if err != nil {
		return func() {}, err
	}
	if scope.SessionID != "" {
		session := s.auth.adminStore.GetSession(scope.SessionID)
		if session == nil || session.UserID != scope.OwnerUserID {
			release()
			return func() {}, ErrResourceSessionRevoked
		}
	}
	return release, nil
}

func (s *Server) acquireResourceTunnelMutation(scope ResourceScope, requireOperational bool) (func(), error) {
	releaseGate, err := s.acquireResourceMutation(scope, requireOperational)
	if err != nil {
		return func() {}, err
	}
	s.clientTunnelMutationMu.Lock()
	return func() {
		s.clientTunnelMutationMu.Unlock()
		releaseGate()
	}, nil
}

func (s *Server) acquireOwnedTunnelMutation(ownerUserID string, expectedEpoch uint64, requireOperational bool) (func(), error) {
	if ownerUserID == "" {
		s.clientTunnelMutationMu.Lock()
		return s.clientTunnelMutationMu.Unlock, nil
	}
	return s.acquireResourceTunnelMutation(ResourceScope{
		OwnerUserID:   ownerUserID,
		ExpectedEpoch: expectedEpoch,
	}, requireOperational)
}

func writeResourceLifecycleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrResourceSessionRevoked):
		writeAPIError(w, http.StatusUnauthorized, "session_expired_or_revoked", "session expired or revoked")
	case errors.Is(err, ErrUserNotFound):
		writeAPIError(w, http.StatusNotFound, "user_not_found", "user not found")
	case errors.Is(err, ErrUserDisabled):
		writeAPIError(w, http.StatusConflict, "user_disabled", "user is disabled")
	case errors.Is(err, ErrUserLifecycleEpochChanged):
		writeAPIError(w, http.StatusConflict, "user_lifecycle_changed", "user lifecycle changed while processing the request")
	default:
		writeAPIError(w, http.StatusServiceUnavailable, "user_mutation_failed", "user mutation failed")
	}
}

func isResourceLifecycleError(err error) bool {
	return errors.Is(err, ErrResourceSessionRevoked) ||
		errors.Is(err, ErrUserNotFound) ||
		errors.Is(err, ErrUserDisabled) ||
		errors.Is(err, ErrUserLifecycleEpochChanged)
}

func requireResourceScope(w http.ResponseWriter, r *http.Request) (ResourceScope, bool) {
	scope, ok := resourceScopeFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "resource_scope_missing", "resource scope is unavailable")
	}
	return scope, ok
}
