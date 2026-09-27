package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func requireUserAPIErrorCode(t *testing.T, responseBody []byte, want string) {
	t.Helper()
	var response apiErrorResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode API error response: %v", err)
	}
	if response.Code != want {
		t.Fatalf("API error code = %q, want %q (body %s)", response.Code, want, responseBody)
	}
}

func TestAPIUserDisabledLoginReturnsDedicatedError(t *testing.T) {
	_, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()

	create := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users", adminToken, []byte(`{"username":"disabled-login-user","password":"Password123"}`))
	if create.Code != http.StatusCreated {
		t.Fatalf("create disabled-login user status = %d, want %d: %s", create.Code, http.StatusCreated, create.Body.String())
	}
	var user userResponse
	if err := json.Unmarshal(create.Body.Bytes(), &user); err != nil {
		t.Fatalf("decode created disabled-login user: %v", err)
	}

	disable := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+user.ID+"/disable", adminToken, nil)
	if disable.Code != http.StatusOK {
		t.Fatalf("disable user status = %d, want %d: %s", disable.Code, http.StatusOK, disable.Body.String())
	}

	login := doMuxRequest(t, handler, http.MethodPost, "/api/auth/login", "", []byte(`{"username":"disabled-login-user","password":"Password123"}`))
	if login.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user login status = %d, want %d: %s", login.Code, http.StatusUnauthorized, login.Body.String())
	}
	requireUserAPIErrorCode(t, login.Body.Bytes(), "user_disabled")
}

func TestAPIUserManagementContract(t *testing.T) {
	_, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()

	create := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users", adminToken, []byte(`{"username":"alice","password":"Alice1234"}`))
	if create.Code != http.StatusCreated {
		t.Fatalf("create user status = %d, want %d: %s", create.Code, http.StatusCreated, create.Body.String())
	}
	var created userResponse
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created user: %v", err)
	}
	if created.ID == "" || created.Username != "alice" || created.IsAdmin || created.Status != UserStatusActive || !created.Operational {
		t.Fatalf("created user = %+v, want active non-administrator alice", created)
	}
	var createdPayload map[string]json.RawMessage
	if err := json.Unmarshal(create.Body.Bytes(), &createdPayload); err != nil {
		t.Fatalf("decode created user payload: %v", err)
	}
	for _, forbidden := range []string{"password_hash", "totp_secret", "session_id", "api_key", "client_token"} {
		if _, exists := createdPayload[forbidden]; exists {
			t.Fatalf("user DTO must not expose %q", forbidden)
		}
	}
	if _, exists := createdPayload["actions"]; exists {
		t.Fatalf("mutation response should omit optional action capabilities: %s", create.Body.String())
	}

	list := doMuxRequest(t, handler, http.MethodGet, "/api/admin/users?status=active&is_admin=false&limit=1", adminToken, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list users status = %d, want %d: %s", list.Code, http.StatusOK, list.Body.String())
	}
	var listed userPageResponse
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode user list: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].ID != created.ID || listed.Items[0].IsAdmin {
		t.Fatalf("filtered user list = %+v, want only created ordinary user", listed)
	}
	if listed.Items[0].Actions == nil {
		t.Fatal("authoritative list response should include action capabilities")
	}

	userToken := loginAdminTokenLocal(t, handler, "alice", "Alice1234")
	forbidden := doMuxRequest(t, handler, http.MethodGet, "/api/admin/users", userToken, nil)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("regular user admin list status = %d, want %d: %s", forbidden.Code, http.StatusForbidden, forbidden.Body.String())
	}
	requireUserAPIErrorCode(t, forbidden.Body.Bytes(), "administrator_access_required")

	me := doMuxRequest(t, handler, http.MethodGet, "/api/auth/me", userToken, nil)
	if me.Code != http.StatusOK {
		t.Fatalf("regular user auth/me status = %d, want %d: %s", me.Code, http.StatusOK, me.Body.String())
	}
	var principal struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if err := json.Unmarshal(me.Body.Bytes(), &principal); err != nil {
		t.Fatalf("decode auth/me: %v", err)
	}
	if principal.ID != created.ID || principal.Username != created.Username || principal.IsAdmin {
		t.Fatalf("auth/me = %+v, want alice non-administrator", principal)
	}

	promotion := doMuxRequest(t, handler, http.MethodPut, "/api/admin/users/"+created.ID+"/admin", adminToken, []byte(`{"is_admin":true}`))
	if promotion.Code != http.StatusOK {
		t.Fatalf("promote alice status = %d, want %d: %s", promotion.Code, http.StatusOK, promotion.Body.String())
	}
	// The original administrator may now self-demote, but a second attempt to
	// remove the final active administrator must still fail deterministically.
	var promoted userResponse
	if err := json.Unmarshal(promotion.Body.Bytes(), &promoted); err != nil {
		t.Fatalf("decode promoted user: %v", err)
	}
	if !promoted.IsAdmin {
		t.Fatalf("promoted user = %+v, want administrator", promoted)
	}

	adminSelfDemote := doMuxRequest(t, handler, http.MethodPut, "/api/admin/users/"+created.ID+"/admin", userToken, []byte(`{"is_admin":false}`))
	if adminSelfDemote.Code != http.StatusUnauthorized {
		t.Fatalf("revoked former user session status = %d, want %d: %s", adminSelfDemote.Code, http.StatusUnauthorized, adminSelfDemote.Body.String())
	}

	// The promotion revoked alice's original session.  Login again as an admin
	// and demote the original administrator, leaving alice as the last admin.
	aliceAdminToken := loginAdminTokenLocal(t, handler, "alice", "Alice1234")
	adminList := doMuxRequest(t, handler, http.MethodGet, "/api/admin/users", aliceAdminToken, nil)
	if adminList.Code != http.StatusOK {
		t.Fatalf("promoted user admin list status = %d, want %d: %s", adminList.Code, http.StatusOK, adminList.Body.String())
	}

	// Use the current list to identify the initialized administrator rather
	// than assuming a generated UUID.
	var all userPageResponse
	if err := json.Unmarshal(adminList.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode administrators list: %v", err)
	}
	var initialAdminID string
	for _, user := range all.Items {
		if user.ID != created.ID && user.IsAdmin {
			initialAdminID = user.ID
			break
		}
	}
	if initialAdminID == "" {
		t.Fatal("initialized administrator not found in user list")
	}
	if demote := doMuxRequest(t, handler, http.MethodPut, "/api/admin/users/"+initialAdminID+"/admin", aliceAdminToken, []byte(`{"is_admin":false}`)); demote.Code != http.StatusOK {
		t.Fatalf("demote original administrator status = %d, want %d: %s", demote.Code, http.StatusOK, demote.Body.String())
	}
	lastAdmin := doMuxRequest(t, handler, http.MethodPut, "/api/admin/users/"+created.ID+"/admin", aliceAdminToken, []byte(`{"is_admin":false}`))
	if lastAdmin.Code != http.StatusConflict {
		t.Fatalf("demote final active administrator status = %d, want %d: %s", lastAdmin.Code, http.StatusConflict, lastAdmin.Body.String())
	}
	requireUserAPIErrorCode(t, lastAdmin.Body.Bytes(), "last_operational_admin")

	selfDisable := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+created.ID+"/disable", aliceAdminToken, nil)
	if selfDisable.Code != http.StatusConflict {
		t.Fatalf("self disable status = %d, want %d: %s", selfDisable.Code, http.StatusConflict, selfDisable.Body.String())
	}
	requireUserAPIErrorCode(t, selfDisable.Body.Bytes(), "self_user_lifecycle_forbidden")

	selfDelete := doMuxRequest(t, handler, http.MethodDelete, "/api/admin/users/"+created.ID, aliceAdminToken, nil)
	if selfDelete.Code != http.StatusConflict {
		t.Fatalf("self delete status = %d, want %d: %s", selfDelete.Code, http.StatusConflict, selfDelete.Body.String())
	}
	requireUserAPIErrorCode(t, selfDelete.Body.Bytes(), "self_user_lifecycle_forbidden")
}

func TestAPIUserDeleteRequiresDisabledStateRevokesSessionAndPublishesListRefresh(t *testing.T) {
	s, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()

	create := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users", adminToken, []byte(`{"username":"bob","password":"BobPassword123"}`))
	if create.Code != http.StatusCreated {
		t.Fatalf("create user status = %d, want %d: %s", create.Code, http.StatusCreated, create.Body.String())
	}
	var user userResponse
	if err := json.Unmarshal(create.Body.Bytes(), &user); err != nil {
		t.Fatalf("decode created user: %v", err)
	}
	userToken := loginAdminTokenLocal(t, handler, user.Username, "BobPassword123")

	activeDelete := doMuxRequest(t, handler, http.MethodDelete, "/api/admin/users/"+user.ID, adminToken, nil)
	if activeDelete.Code != http.StatusConflict {
		t.Fatalf("delete active user status = %d, want %d: %s", activeDelete.Code, http.StatusConflict, activeDelete.Body.String())
	}
	requireUserAPIErrorCode(t, activeDelete.Body.Bytes(), "user_must_be_disabled")

	disable := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users/"+user.ID+"/disable", adminToken, nil)
	if disable.Code != http.StatusOK {
		t.Fatalf("disable user status = %d, want %d: %s", disable.Code, http.StatusOK, disable.Body.String())
	}
	var disabled userResponse
	if err := json.Unmarshal(disable.Body.Bytes(), &disabled); err != nil {
		t.Fatalf("decode disabled user: %v", err)
	}
	if disabled.Status != UserStatusDisabled || disabled.Operational {
		t.Fatalf("disabled user = %+v, want non-operational disabled user", disabled)
	}

	revoked := doMuxRequest(t, handler, http.MethodGet, "/api/auth/me", userToken, nil)
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user session status = %d, want %d: %s", revoked.Code, http.StatusUnauthorized, revoked.Body.String())
	}
	requireUserAPIErrorCode(t, revoked.Body.Bytes(), "session_expired_or_revoked")

	events := s.events.Subscribe()
	defer s.events.Unsubscribe(events)
	deleteUser := doMuxRequest(t, handler, http.MethodDelete, "/api/admin/users/"+user.ID, adminToken, nil)
	if deleteUser.Code != http.StatusNoContent {
		t.Fatalf("delete disabled user status = %d, want %d: %s", deleteUser.Code, http.StatusNoContent, deleteUser.Body.String())
	}
	select {
	case event := <-events:
		if event.Type != "user_list_changed" {
			t.Fatalf("delete event type = %q, want user_list_changed", event.Type)
		}
		var payload map[string]string
		if err := json.Unmarshal([]byte(event.Data), &payload); err != nil {
			t.Fatalf("decode delete list-refresh event: %v", err)
		}
		if payload["action"] != "deleted" || payload["user_id"] != user.ID {
			t.Fatalf("delete list-refresh payload = %#v, want deleted/%s", payload, user.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("delete did not publish a user-list refresh event")
	}
	missing := doMuxRequest(t, handler, http.MethodGet, "/api/admin/users/"+user.ID, adminToken, nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("load deleted user status = %d, want %d: %s", missing.Code, http.StatusNotFound, missing.Body.String())
	}
	requireUserAPIErrorCode(t, missing.Body.Bytes(), "user_not_found")
}

func TestAPIUserDeletionImpactContract(t *testing.T) {
	_, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()

	create := doMuxRequest(t, handler, http.MethodPost, "/api/admin/users", adminToken, []byte(`{"username":"impact-user","password":"Password123"}`))
	if create.Code != http.StatusCreated {
		t.Fatalf("create user status = %d, want %d: %s", create.Code, http.StatusCreated, create.Body.String())
	}
	var user userResponse
	if err := json.Unmarshal(create.Body.Bytes(), &user); err != nil {
		t.Fatalf("decode created user: %v", err)
	}

	response := doMuxRequest(t, handler, http.MethodGet, "/api/admin/users/"+user.ID+"/deletion-impact", adminToken, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("deletion impact status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	var impact UserDeletionImpact
	if err := json.Unmarshal(response.Body.Bytes(), &impact); err != nil {
		t.Fatalf("decode deletion impact: %v", err)
	}
	if impact.UserID != user.ID {
		t.Fatalf("deletion impact user_id = %q, want %q", impact.UserID, user.ID)
	}
	if impact.APIKeys != 0 || impact.Clients != 0 || impact.Tunnels != 0 || impact.TrafficBuckets != 0 {
		t.Fatalf("unexpected fresh-user owned-resource impact: %+v", impact)
	}
	if impact.ActivityEvents == 0 {
		t.Fatalf("deletion impact must include the user-created activity: %+v", impact)
	}
	if impact.GeneratedAt.IsZero() || time.Since(impact.GeneratedAt) > time.Minute {
		t.Fatalf("deletion impact generated_at = %s, want a current timestamp", impact.GeneratedAt)
	}

	missing := doMuxRequest(t, handler, http.MethodGet, "/api/admin/users/missing-user/deletion-impact", adminToken, nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing-user deletion impact status = %d, want %d: %s", missing.Code, http.StatusNotFound, missing.Body.String())
	}
	requireUserAPIErrorCode(t, missing.Body.Bytes(), "user_not_found")
}
