package server

import (
	"errors"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestAdminSecurityActivityMutationRollsBackOnEventFailure(t *testing.T) {
	store := newInitializedAdminStore(t)
	user, err := store.ValidateAdminPassword("admin", "Admin1234")
	if err != nil || user == nil {
		t.Fatalf("load admin user: %+v, %v", user, err)
	}
	actor := ActivityActor{Type: "admin", ID: user.ID, Name: user.Username}
	store.activityStore.failNextAppendsForTest(errors.New("injected activity failure"), 1)
	if _, err := store.UpdateAdminUsernameWithActivity(user.ID, "rolled-back-admin", actor); err == nil {
		t.Fatal("username update should fail when activity append fails")
	}
	reloaded, err := store.GetAdminUserByID(user.ID)
	if err != nil {
		t.Fatalf("reload admin user: %v", err)
	}
	if reloaded.Username != "admin" {
		t.Fatalf("username was not rolled back: %q", reloaded.Username)
	}
	maxID, err := store.activityStore.MaxID()
	if err != nil || maxID != 0 {
		t.Fatalf("rolled-back security activity max id = %d, %v", maxID, err)
	}
}

func TestAdminSecurityUserFieldMutationsAdvanceUpdatedAt(t *testing.T) {
	oldUpdatedAt := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

	assertAdvanced := func(t *testing.T, store *AdminStore, userID string) {
		t.Helper()
		user, err := store.GetAdminUserByID(userID)
		if err != nil {
			t.Fatalf("reload administrator: %v", err)
		}
		if !user.UpdatedAt.After(oldUpdatedAt) {
			t.Fatalf("administrator updated_at = %s, want after %s", user.UpdatedAt, oldUpdatedAt)
		}
	}

	prepare := func(t *testing.T) (*AdminStore, *AdminUser, ActivityActor) {
		t.Helper()
		store := newInitializedAdminStore(t)
		user, err := store.ValidateAdminPassword("admin", "Admin1234")
		if err != nil || user == nil {
			t.Fatalf("load administrator: %+v, %v", user, err)
		}
		if _, err := store.db.Exec(`UPDATE users SET updated_at = ? WHERE id = ?`, formatTime(oldUpdatedAt), user.ID); err != nil {
			t.Fatalf("set old updated_at: %v", err)
		}
		actor := ActivityActor{Type: "admin", ID: user.ID, Name: user.Username}
		return store, user, actor
	}

	t.Run("username", func(t *testing.T) {
		store, user, actor := prepare(t)
		if _, err := store.UpdateAdminUsernameWithActivity(user.ID, "renamed-admin", actor); err != nil {
			t.Fatalf("update username: %v", err)
		}
		assertAdvanced(t, store, user.ID)
	})

	t.Run("password", func(t *testing.T) {
		store, user, actor := prepare(t)
		if _, err := store.UpdateAdminPasswordWithActivity(user.ID, "Admin1234", "NewAdmin1234", actor); err != nil {
			t.Fatalf("update password: %v", err)
		}
		assertAdvanced(t, store, user.ID)
	})

	t.Run("totp enabled", func(t *testing.T) {
		store, user, actor := prepare(t)
		challengeID, secret, _, _, err := store.BeginTOTPSetup(*user, "NetsGo")
		if err != nil {
			t.Fatalf("begin TOTP setup: %v", err)
		}
		code, err := totp.GenerateCode(secret, time.Now())
		if err != nil {
			t.Fatalf("generate TOTP code: %v", err)
		}
		if _, _, err := store.ConfirmTOTPSetupWithActivity(user.ID, challengeID, code, actor); err != nil {
			t.Fatalf("confirm TOTP setup: %v", err)
		}
		assertAdvanced(t, store, user.ID)
	})

	t.Run("totp disabled", func(t *testing.T) {
		store, user, actor := prepare(t)
		if _, err := store.db.Exec(`UPDATE users SET totp_enabled = 1, totp_secret = ?, updated_at = ? WHERE id = ?`, "JBSWY3DPEHPK3PXP", formatTime(oldUpdatedAt), user.ID); err != nil {
			t.Fatalf("seed enabled TOTP: %v", err)
		}
		if _, err := store.DisableTOTPWithActivity(user.ID, actor); err != nil {
			t.Fatalf("disable TOTP: %v", err)
		}
		assertAdvanced(t, store, user.ID)
	})
}
