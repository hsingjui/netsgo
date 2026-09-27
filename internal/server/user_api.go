package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type userActionCapabilities struct {
	CanChangeAdmin    bool `json:"can_change_admin"`
	CanDisable        bool `json:"can_disable"`
	CanEnable         bool `json:"can_enable"`
	CanDelete         bool `json:"can_delete"`
	CanUpdateUsername bool `json:"can_update_username"`
	CanUpdatePassword bool `json:"can_update_password"`
	CanRevokeSessions bool `json:"can_revoke_sessions"`
}

type userResponse struct {
	ID          string                  `json:"id"`
	Username    string                  `json:"username"`
	IsAdmin     bool                    `json:"is_admin"`
	Status      UserStatus              `json:"status"`
	CreatedAt   time.Time               `json:"created_at"`
	UpdatedAt   time.Time               `json:"updated_at"`
	LastLogin   *time.Time              `json:"last_login,omitempty"`
	Operational bool                    `json:"operational"`
	Actions     *userActionCapabilities `json:"actions,omitempty"`
}

type userPageResponse struct {
	Items      []userResponse `json:"items"`
	NextCursor *string        `json:"next_cursor"`
	HasMore    bool           `json:"has_more"`
}

func (s *Server) publishUserListChanged(action, userID string) {
	if s == nil || s.events == nil {
		return
	}
	s.events.PublishJSON("user_list_changed", map[string]string{
		"action":  action,
		"user_id": userID,
	})
}

func (s *Server) activeAdminCount() (int, error) {
	if s == nil || s.auth == nil || s.auth.adminStore == nil {
		return 0, ErrUserOwnerUnavailable
	}
	s.auth.adminStore.mu.RLock()
	defer s.auth.adminStore.mu.RUnlock()
	var count int
	if err := s.auth.adminStore.db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin = 1 AND status = ?`, string(UserStatusActive)).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func userActionCapabilitiesFor(principal *RequestPrincipal, user User, activeAdminCount int) userActionCapabilities {
	if principal == nil || !principal.IsAdmin {
		return userActionCapabilities{}
	}
	self := principal.UserID == user.ID
	lastOperationalAdmin := user.IsAdmin && user.Status == UserStatusActive && activeAdminCount <= 1
	return userActionCapabilities{
		CanChangeAdmin:    !lastOperationalAdmin,
		CanDisable:        !self && user.Status == UserStatusActive && !lastOperationalAdmin,
		CanEnable:         !self && user.Status == UserStatusDisabled,
		CanDelete:         !self && user.Status == UserStatusDisabled,
		CanUpdateUsername: true,
		CanUpdatePassword: true,
		CanRevokeSessions: true,
	}
}

func userResponseFor(principal *RequestPrincipal, user User, activeAdminCount int) userResponse {
	actions := userActionCapabilitiesFor(principal, user, activeAdminCount)
	return userResponse{
		ID:          user.ID,
		Username:    user.Username,
		IsAdmin:     user.IsAdmin,
		Status:      user.Status,
		CreatedAt:   user.CreatedAt,
		UpdatedAt:   user.UpdatedAt,
		LastLogin:   user.LastLogin,
		Operational: isOperationalUser(user),
		Actions:     &actions,
	}
}

func userMutationResponse(user User) userResponse {
	return userResponse{
		ID:          user.ID,
		Username:    user.Username,
		IsAdmin:     user.IsAdmin,
		Status:      user.Status,
		CreatedAt:   user.CreatedAt,
		UpdatedAt:   user.UpdatedAt,
		LastLogin:   user.LastLogin,
		Operational: isOperationalUser(user),
	}
}

func (s *Server) handleAPIAuthMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	principal := GetPrincipalFromContext(r.Context())
	if principal == nil {
		writeAPIError(w, http.StatusUnauthorized, "session_expired_or_revoked", "session expired or revoked")
		return
	}
	user, err := s.auth.adminStore.GetUser(principal.UserID)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "session_expired_or_revoked", "session expired or revoked")
		return
	}
	activeAdmins, err := s.activeAdminCount()
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "temporary_storage_failure", "temporary storage failure")
		return
	}
	encodeJSON(w, http.StatusOK, userResponseFor(principal, user, activeAdmins))
}

func parseUserListOptions(r *http.Request) (UserListOptions, error) {
	options := UserListOptions{Cursor: r.URL.Query().Get("cursor"), Query: r.URL.Query().Get("query")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			return UserListOptions{}, errors.New("limit must be a positive integer")
		}
		options.Limit = limit
	}
	if raw := r.URL.Query().Get("status"); raw != "" {
		status := UserStatus(raw)
		if status != UserStatusActive && status != UserStatusDisabled {
			return UserListOptions{}, ErrInvalidUserStatus
		}
		options.Status = &status
	}
	if raw := r.URL.Query().Get("is_admin"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return UserListOptions{}, errors.New("is_admin must be true or false")
		}
		options.IsAdmin = &value
	}
	return options, nil
}

func (s *Server) handleAPIAdminUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		options, err := parseUserListOptions(r)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_user_list_query", "invalid user list query")
			return
		}
		page, err := s.auth.adminStore.ListUsers(options)
		if err != nil {
			if errors.Is(err, ErrInvalidUserCursor) {
				writeAPIError(w, http.StatusBadRequest, "invalid_user_cursor", "invalid user cursor")
				return
			}
			writeAPIError(w, http.StatusServiceUnavailable, "temporary_storage_failure", "temporary storage failure")
			return
		}
		principal := GetPrincipalFromContext(r.Context())
		activeAdmins, err := s.activeAdminCount()
		if err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, "temporary_storage_failure", "temporary storage failure")
			return
		}
		response := userPageResponse{Items: make([]userResponse, 0, len(page.Items)), HasMore: page.HasMore}
		if page.NextCursor != "" {
			response.NextCursor = &page.NextCursor
		}
		for _, user := range page.Items {
			response.Items = append(response.Items, userResponseFor(principal, user, activeAdmins))
		}
		encodeJSON(w, http.StatusOK, response)
	case http.MethodPost:
		var request struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := decodeJSONRequestBody(r, &request); err != nil {
			writeJSONRequestDecodeError(w, err)
			return
		}
		user, activityID, err := s.auth.adminStore.CreateUserWithActivity(request.Username, request.Password, s.activityActorForRequest(r))
		if err != nil {
			switch {
			case errors.Is(err, ErrUserAlreadyExists):
				writeAPIError(w, http.StatusConflict, "username_taken", "username is already in use")
			case errors.Is(err, ErrInvalidUsername):
				writeAPIError(w, http.StatusBadRequest, "invalid_username", "invalid username")
			case errors.Is(err, ErrInvalidPassword):
				writeAPIError(w, http.StatusBadRequest, "invalid_password", "invalid password")
			default:
				writeAPIError(w, http.StatusServiceUnavailable, "user_mutation_failed", "user mutation failed")
			}
			return
		}
		s.publishActivityID(activityID)
		s.publishUserListChanged("created", user.ID)
		encodeJSON(w, http.StatusCreated, userMutationResponse(user))
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) targetUserForRequest(w http.ResponseWriter, r *http.Request) (User, bool) {
	userID := strings.TrimSpace(r.PathValue("user_id"))
	if userID == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_user_id", "user id is required")
		return User{}, false
	}
	user, err := s.auth.adminStore.GetUser(userID)
	if errors.Is(err, ErrUserNotFound) {
		writeAPIError(w, http.StatusNotFound, "user_not_found", "user not found")
		return User{}, false
	}
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "temporary_storage_failure", "temporary storage failure")
		return User{}, false
	}
	return user, true
}

func (s *Server) handleAPIAdminUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	user, ok := s.targetUserForRequest(w, r)
	if !ok {
		return
	}
	activeAdmins, err := s.activeAdminCount()
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "temporary_storage_failure", "temporary storage failure")
		return
	}
	encodeJSON(w, http.StatusOK, userResponseFor(GetPrincipalFromContext(r.Context()), user, activeAdmins))
}

func (s *Server) handleAPIAdminUserDeletionImpact(w http.ResponseWriter, r *http.Request) {
	userID := strings.TrimSpace(r.PathValue("user_id"))
	gate := s.lifecycleGate(userID)
	if gate == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_user_id", "user id is required")
		return
	}
	gate.mu.RLock()
	defer gate.mu.RUnlock()
	impact, err := s.auth.adminStore.GetUserDeletionImpact(userID)
	if errors.Is(err, ErrUserNotFound) {
		writeAPIError(w, http.StatusNotFound, "user_not_found", "user not found")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "temporary_storage_failure", "temporary storage failure")
		return
	}
	encodeJSON(w, http.StatusOK, impact)
}

func (s *Server) handleAPIAdminUserUsername(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	var request struct {
		Username string `json:"username"`
	}
	if err := decodeJSONRequestBody(r, &request); err != nil {
		writeJSONRequestDecodeError(w, err)
		return
	}
	userID := strings.TrimSpace(r.PathValue("user_id"))
	gate := s.lifecycleGate(userID)
	if gate == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_user_id", "user id is required")
		return
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	s.userManagementMu.Lock()
	user, activityID, err := s.auth.adminStore.UpdateUserUsernameWithActivity(userID, request.Username, s.activityActorForRequest(r))
	s.userManagementMu.Unlock()
	if errors.Is(err, ErrUserNotFound) {
		writeAPIError(w, http.StatusNotFound, "user_not_found", "user not found")
		return
	}
	if errors.Is(err, ErrUserAlreadyExists) {
		writeAPIError(w, http.StatusConflict, "username_taken", "username is already in use")
		return
	}
	if errors.Is(err, ErrInvalidUsername) {
		writeAPIError(w, http.StatusBadRequest, "invalid_username", "invalid username")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "user_mutation_failed", "user mutation failed")
		return
	}
	s.cancelSSEForUser(user.ID, "user_username_changed")
	s.publishActivityID(activityID)
	s.publishUserListChanged("username_changed", user.ID)
	encodeJSON(w, http.StatusOK, userMutationResponse(user))
}

func (s *Server) handleAPIAdminUserPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	var request struct {
		Password string `json:"password"`
	}
	if err := decodeJSONRequestBody(r, &request); err != nil {
		writeJSONRequestDecodeError(w, err)
		return
	}
	userID := strings.TrimSpace(r.PathValue("user_id"))
	gate := s.lifecycleGate(userID)
	if gate == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_user_id", "user id is required")
		return
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	s.userManagementMu.Lock()
	user, activityID, err := s.auth.adminStore.ResetUserPasswordWithActivity(userID, request.Password, s.activityActorForRequest(r))
	s.userManagementMu.Unlock()
	if errors.Is(err, ErrUserNotFound) {
		writeAPIError(w, http.StatusNotFound, "user_not_found", "user not found")
		return
	}
	if errors.Is(err, ErrInvalidPassword) {
		writeAPIError(w, http.StatusBadRequest, "invalid_password", "invalid password")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "user_mutation_failed", "user mutation failed")
		return
	}
	s.cancelSSEForUser(user.ID, "user_password_reset")
	s.publishActivityID(activityID)
	encodeJSON(w, http.StatusOK, userMutationResponse(user))
}

func (s *Server) handleAPIAdminUserAdmin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	var request struct {
		IsAdmin *bool `json:"is_admin"`
	}
	if err := decodeJSONRequestBody(r, &request); err != nil || request.IsAdmin == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_user_admin_state", "is_admin is required")
		return
	}
	principal := GetPrincipalFromContext(r.Context())
	userID := strings.TrimSpace(r.PathValue("user_id"))
	gate := s.lifecycleGate(userID)
	if gate == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_user_id", "user id is required")
		return
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	s.userManagementMu.Lock()
	user, changed, activityID, err := s.auth.adminStore.SetUserAdminWithActivity(principal.UserID, userID, *request.IsAdmin, s.activityActorForRequest(r))
	s.userManagementMu.Unlock()
	if !writeUserLifecycleError(w, err) {
		return
	}
	if changed {
		s.cancelSSEForUser(user.ID, "user_admin_changed")
		s.publishActivityID(activityID)
		s.publishUserListChanged("admin_changed", user.ID)
	}
	encodeJSON(w, http.StatusOK, userMutationResponse(user))
}

func (s *Server) handleAPIAdminUserDisable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	principal := GetPrincipalFromContext(r.Context())
	userID := strings.TrimSpace(r.PathValue("user_id"))
	gate := s.lifecycleGate(userID)
	if gate == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_user_id", "user id is required")
		return
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	s.userManagementMu.Lock()
	user, changed, activityID, err := s.auth.adminStore.SetUserStatusWithActivity(principal.UserID, userID, UserStatusDisabled, s.activityActorForRequest(r))
	s.userManagementMu.Unlock()
	if !writeUserLifecycleError(w, err) {
		return
	}
	gate.epoch++
	if changed {
		s.publishActivityID(activityID)
		s.publishUserListChanged("disabled", user.ID)
	}
	ctx, cancel := s.newUserConvergenceContext()
	err = s.convergeUserRuntime(ctx, user.ID)
	cancel()
	if err != nil {
		s.recordUserConvergenceIncomplete(user, s.activityActorForRequest(r), err)
		writeAPIError(w, http.StatusServiceUnavailable, "user_disable_incomplete", "user runtime convergence is incomplete")
		return
	}
	encodeJSON(w, http.StatusOK, userMutationResponse(user))
}

func (s *Server) handleAPIAdminUserEnable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	principal := GetPrincipalFromContext(r.Context())
	userID := strings.TrimSpace(r.PathValue("user_id"))
	gate := s.lifecycleGate(userID)
	if gate == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_user_id", "user id is required")
		return
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()

	s.userManagementMu.Lock()
	user, err := s.auth.adminStore.GetUser(userID)
	s.userManagementMu.Unlock()
	if !writeUserLifecycleError(w, err) {
		return
	}
	if user.Status == UserStatusActive {
		encodeJSON(w, http.StatusOK, userMutationResponse(user))
		return
	}
	if user.Status != UserStatusDisabled {
		writeAPIError(w, http.StatusBadRequest, "invalid_user_status", "invalid user status")
		return
	}

	ctx, cancel := s.newUserConvergenceContext()
	err = s.convergeUserRuntime(ctx, user.ID)
	cancel()
	if err != nil {
		s.recordUserConvergenceIncomplete(user, s.activityActorForRequest(r), err)
		writeAPIError(w, http.StatusServiceUnavailable, "user_disable_incomplete", "user runtime convergence is incomplete")
		return
	}

	s.userManagementMu.Lock()
	user, changed, activityID, err := s.auth.adminStore.SetUserStatusWithActivity(principal.UserID, userID, UserStatusActive, s.activityActorForRequest(r))
	s.userManagementMu.Unlock()
	if !writeUserLifecycleError(w, err) {
		return
	}
	gate.epoch++
	if changed {
		s.publishActivityID(activityID)
		s.publishUserListChanged("enabled", user.ID)
	}
	encodeJSON(w, http.StatusOK, userMutationResponse(user))
}

func (s *Server) handleAPIAdminUserDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	principal := GetPrincipalFromContext(r.Context())
	userID := strings.TrimSpace(r.PathValue("user_id"))
	gate := s.lifecycleGate(userID)
	if gate == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_user_id", "user id is required")
		return
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()

	s.userManagementMu.Lock()
	user, err := s.auth.adminStore.GetUser(userID)
	s.userManagementMu.Unlock()
	if !writeUserLifecycleError(w, err) {
		return
	}
	if principal.UserID == user.ID {
		writeUserLifecycleError(w, ErrSelfUserLifecycleMutation)
		return
	}
	if user.Status != UserStatusDisabled {
		writeUserLifecycleError(w, ErrUserMustBeDisabled)
		return
	}

	ctx, cancel := s.newUserConvergenceContext()
	err = s.convergeUserRuntime(ctx, user.ID)
	cancel()
	if err != nil {
		s.recordUserConvergenceIncomplete(user, s.activityActorForRequest(r), err)
		writeAPIError(w, http.StatusServiceUnavailable, "user_disable_incomplete", "user runtime convergence is incomplete")
		return
	}

	s.userManagementMu.Lock()
	err = s.trafficStore.withUserDeletionBoundary(user.ID, func() error {
		return s.auth.adminStore.DeleteDisabledUser(principal.UserID, user.ID)
	})
	s.userManagementMu.Unlock()
	if !writeUserLifecycleError(w, err) {
		return
	}
	gate.epoch++
	s.cancelSSEForUser(user.ID, "user_deleted")
	s.publishUserListChanged("deleted", user.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAPIAdminUserSessionsRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	userID := strings.TrimSpace(r.PathValue("user_id"))
	gate := s.lifecycleGate(userID)
	if gate == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_user_id", "user id is required")
		return
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if _, ok := s.targetUserForRequest(w, r); !ok {
		return
	}
	s.userManagementMu.Lock()
	activityID, err := s.auth.adminStore.DeleteSessionsByUserIDWithActivity(userID, s.activityActorForRequest(r))
	s.userManagementMu.Unlock()
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "session_revoke_failed", "failed to revoke user sessions")
		return
	}
	s.cancelSSEForUser(userID, "user_sessions_revoked")
	s.publishActivityID(activityID)
	encodeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func writeUserLifecycleError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	switch {
	case errors.Is(err, ErrUserNotFound):
		writeAPIError(w, http.StatusNotFound, "user_not_found", "user not found")
	case errors.Is(err, ErrUserMustBeDisabled):
		writeAPIError(w, http.StatusConflict, "user_must_be_disabled", "user must be disabled before deletion")
	case errors.Is(err, ErrSelfUserLifecycleMutation):
		writeAPIError(w, http.StatusConflict, "self_user_lifecycle_forbidden", "current user cannot be disabled or deleted")
	case errors.Is(err, ErrLastOperationalAdmin):
		writeAPIError(w, http.StatusConflict, "last_operational_admin", "at least one active administrator must remain")
	case errors.Is(err, ErrInvalidUserStatus):
		writeAPIError(w, http.StatusBadRequest, "invalid_user_status", "invalid user status")
	case errors.Is(err, ErrUserConvergenceIncomplete):
		writeAPIError(w, http.StatusServiceUnavailable, "user_disable_incomplete", "user runtime convergence is incomplete")
	default:
		writeAPIError(w, http.StatusServiceUnavailable, "user_mutation_failed", "user mutation failed")
	}
	return false
}
