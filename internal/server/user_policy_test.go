package server

import (
	"errors"
	"testing"
)

func policyUser(id string, isAdmin bool, status UserStatus) User {
	return User{ID: id, IsAdmin: isAdmin, Status: status}
}

func TestIsOperationalUserFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		user User
		want bool
	}{
		{
			name: "active persisted user",
			user: policyUser("admin-a", true, UserStatusActive),
			want: true,
		},
		{
			name: "disabled user",
			user: policyUser("admin-a", true, UserStatusDisabled),
			want: false,
		},
		{
			name: "unknown status",
			user: policyUser("admin-a", true, UserStatus("suspended")),
			want: false,
		},
		{
			name: "missing row identity",
			user: policyUser("", true, UserStatusActive),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isOperationalUser(tt.user); got != tt.want {
				t.Fatalf("isOperationalUser(%+v) = %v, want %v", tt.user, got, tt.want)
			}
		})
	}
}

func TestValidateUserLifecycleMutation(t *testing.T) {
	activeAdminA := policyUser("admin-a", true, UserStatusActive)
	activeAdminB := policyUser("admin-b", true, UserStatusActive)
	disabledAdmin := policyUser("admin-disabled", true, UserStatusDisabled)
	activeUser := policyUser("user-a", false, UserStatusActive)

	tests := []struct {
		name        string
		users       []User
		actorUserID string
		before      User
		after       User
		deleting    bool
		wantErr     error
	}{
		{
			name:        "allows self demotion when another operational admin remains",
			users:       []User{activeAdminA, activeAdminB},
			actorUserID: activeAdminA.ID,
			before:      activeAdminA,
			after:       policyUser(activeAdminA.ID, false, UserStatusActive),
		},
		{
			name:        "rejects last operational admin demotion",
			users:       []User{activeAdminA, disabledAdmin},
			actorUserID: activeAdminB.ID,
			before:      activeAdminA,
			after:       policyUser(activeAdminA.ID, false, UserStatusActive),
			wantErr:     ErrLastOperationalAdmin,
		},
		{
			name:        "rejects last operational admin disable",
			users:       []User{activeAdminA, disabledAdmin},
			actorUserID: activeAdminB.ID,
			before:      activeAdminA,
			after:       policyUser(activeAdminA.ID, true, UserStatusDisabled),
			wantErr:     ErrLastOperationalAdmin,
		},
		{
			name:        "allows disabled admin deletion when another operational admin remains",
			users:       []User{activeAdminA, disabledAdmin},
			actorUserID: activeAdminA.ID,
			before:      disabledAdmin,
			after:       disabledAdmin,
			deleting:    true,
		},
		{
			name:        "rejects self disable even with another operational admin",
			users:       []User{activeAdminA, activeAdminB},
			actorUserID: activeAdminA.ID,
			before:      activeAdminA,
			after:       policyUser(activeAdminA.ID, true, UserStatusDisabled),
			wantErr:     ErrSelfUserLifecycleMutation,
		},
		{
			name:        "rejects self deletion even with another operational admin",
			users:       []User{activeAdminA, activeAdminB},
			actorUserID: activeAdminA.ID,
			before:      activeAdminA,
			after:       activeAdminA,
			deleting:    true,
			wantErr:     ErrSelfUserLifecycleMutation,
		},
		{
			name:        "allows an administrator to disable a regular user",
			users:       []User{activeAdminA, activeUser},
			actorUserID: activeAdminA.ID,
			before:      activeUser,
			after:       policyUser(activeUser.ID, false, UserStatusDisabled),
		},
		{
			name:        "keeps an idempotent active admin update valid",
			users:       []User{activeAdminA},
			actorUserID: "admin-b",
			before:      activeAdminA,
			after:       activeAdminA,
		},
		{
			name:        "does not count an unknown status as operational",
			users:       []User{activeAdminA, disabledAdmin},
			actorUserID: activeAdminB.ID,
			before:      activeAdminA,
			after:       policyUser(activeAdminA.ID, true, UserStatus("suspended")),
			wantErr:     ErrLastOperationalAdmin,
		},
		{
			name:        "rejects replacement with another user id",
			users:       []User{activeAdminA, activeAdminB},
			actorUserID: activeAdminB.ID,
			before:      activeAdminA,
			after:       policyUser(activeAdminB.ID, true, UserStatusActive),
			wantErr:     ErrUserLifecycleIdentityChange,
		},
		{
			name:        "rejects a target outside the transaction snapshot",
			users:       []User{activeAdminA},
			actorUserID: activeAdminA.ID,
			before:      activeUser,
			after:       activeUser,
			wantErr:     ErrUserLifecycleTargetNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateUserLifecycleMutation(tt.users, tt.actorUserID, tt.before, tt.after, tt.deleting)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("validateUserLifecycleMutation() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
