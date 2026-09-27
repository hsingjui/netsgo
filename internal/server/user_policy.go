package server

import "errors"

var (
	// ErrLastOperationalAdmin prevents a committed user mutation from leaving
	// the server without an active administrator.
	ErrLastOperationalAdmin = errors.New("at least one active administrator must remain")
	// ErrSelfUserLifecycleMutation prevents a current administrator from
	// disabling or deleting the account that authorizes the request.
	ErrSelfUserLifecycleMutation   = errors.New("current user cannot be disabled or deleted")
	ErrUserLifecycleTargetNotFound = errors.New("user lifecycle target not found")
	ErrUserLifecycleIdentityChange = errors.New("user lifecycle mutation cannot change user id")
)

// isOperationalUser is deliberately fail-closed: only an existing, explicitly
// active user can authenticate or operate owned resources.
func isOperationalUser(user User) bool {
	return user.ID != "" && user.Status == UserStatusActive
}

// validateUserLifecycleMutation validates the cross-user invariants before the
// caller persists a status, administrator-flag, or hard-delete mutation.
//
// users is the transaction snapshot that contains before. after is the exact
// row to persist unless deleting is true. Persistence, status-transition
// validation, and authorization of the caller are intentionally kept outside
// this pure policy function.
func validateUserLifecycleMutation(users []User, actorUserID string, before, after User, deleting bool) error {
	if before.ID == "" {
		return ErrUserLifecycleTargetNotFound
	}
	if after.ID != before.ID {
		return ErrUserLifecycleIdentityChange
	}
	if actorUserID == before.ID && (deleting || after.Status == UserStatusDisabled) {
		return ErrSelfUserLifecycleMutation
	}

	foundTarget := false
	operationalAdmins := 0
	for _, user := range users {
		if user.ID == before.ID {
			foundTarget = true
			if deleting {
				continue
			}
			user = after
		}
		if user.IsAdmin && isOperationalUser(user) {
			operationalAdmins++
		}
	}
	if !foundTarget {
		return ErrUserLifecycleTargetNotFound
	}
	if operationalAdmins == 0 {
		return ErrLastOperationalAdmin
	}
	return nil
}
