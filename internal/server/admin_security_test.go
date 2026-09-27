package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/pquerna/otp/totp"
)

func adminUserTOTPState(t *testing.T, store *AdminStore, userID string) (bool, string) {
	t.Helper()
	var enabled int
	var secret string
	if err := store.db.QueryRow(`SELECT totp_enabled, totp_secret FROM users WHERE id = ?`, userID).Scan(&enabled, &secret); err != nil {
		t.Fatalf("load admin totp state: %v", err)
	}
	return intToBool(enabled), secret
}

func countAdminPasskeys(t *testing.T, store *AdminStore) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM admin_passkeys`).Scan(&count); err != nil {
		t.Fatalf("count admin passkeys: %v", err)
	}
	return count
}

func TestPasswordLoginRevalidatesAtSessionCommit(t *testing.T) {
	s, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()
	user, err := s.auth.adminStore.CreateUser("stale-password-login", "Password123")
	if err != nil {
		t.Fatal(err)
	}

	enteredCommit := make(chan struct{})
	releaseCommit := make(chan struct{})
	released := false
	defer func() {
		s.adminAuthorizationHook = nil
		if !released {
			close(releaseCommit)
		}
	}()
	s.adminAuthorizationHook = func(stage string, principal *RequestPrincipal) {
		if stage == "before_login_commit" && principal != nil && principal.UserID == user.ID {
			close(enteredCommit)
			<-releaseCommit
		}
	}

	loginResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		loginResult <- doMuxRequest(t, handler, http.MethodPost, "/api/auth/login", "", []byte(`{"username":"stale-password-login","password":"Password123"}`))
	}()
	select {
	case <-enteredCommit:
	case <-time.After(time.Second):
		t.Fatal("password login did not reach its final commit boundary")
	}

	reset := doMuxRequest(t, handler, http.MethodPut, "/api/admin/users/"+user.ID+"/password", adminToken, []byte(`{"password":"ChangedPassword123"}`))
	if reset.Code != http.StatusOK {
		t.Fatalf("password reset status = %d: %s", reset.Code, reset.Body.String())
	}
	close(releaseCommit)
	released = true

	var login *httptest.ResponseRecorder
	select {
	case login = <-loginResult:
	case <-time.After(time.Second):
		t.Fatal("stale password login did not return")
	}
	if login.Code != http.StatusUnauthorized {
		t.Fatalf("stale password login status = %d, want %d: %s", login.Code, http.StatusUnauthorized, login.Body.String())
	}
	var sessions int
	if err := s.auth.adminStore.db.QueryRow(`SELECT COUNT(*) FROM user_sessions WHERE user_id = ?`, user.ID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("stale password login created %d session(s)", sessions)
	}
}

func TestMFALoginRevalidatesPasswordBeforeChallengeCommit(t *testing.T) {
	s, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()
	admin, err := s.auth.adminStore.GetSingleAdminUser()
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.auth.adminStore.CreateUser("stale-mfa-login", "Password123")
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, err := s.auth.adminStore.SetUserAdmin(admin.ID, user.ID, true); err != nil || !changed {
		t.Fatalf("promote MFA user = (changed %v, err %v)", changed, err)
	}
	if _, err := s.auth.adminStore.db.Exec(`UPDATE users SET totp_enabled = 1, totp_secret = ? WHERE id = ?`, "JBSWY3DPEHPK3PXP", user.ID); err != nil {
		t.Fatalf("enable TOTP: %v", err)
	}

	enteredCommit := make(chan struct{})
	releaseCommit := make(chan struct{})
	released := false
	defer func() {
		s.adminAuthorizationHook = nil
		if !released {
			close(releaseCommit)
		}
	}()
	s.adminAuthorizationHook = func(stage string, principal *RequestPrincipal) {
		if stage == "before_login_commit" && principal != nil && principal.UserID == user.ID {
			close(enteredCommit)
			<-releaseCommit
		}
	}

	loginResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		loginResult <- doMuxRequest(t, handler, http.MethodPost, "/api/auth/login", "", []byte(`{"username":"stale-mfa-login","password":"Password123"}`))
	}()
	select {
	case <-enteredCommit:
	case <-time.After(time.Second):
		t.Fatal("MFA login did not reach its challenge commit boundary")
	}

	reset := doMuxRequest(t, handler, http.MethodPut, "/api/admin/users/"+user.ID+"/password", adminToken, []byte(`{"password":"ChangedPassword123"}`))
	if reset.Code != http.StatusOK {
		t.Fatalf("password reset status = %d: %s", reset.Code, reset.Body.String())
	}
	close(releaseCommit)
	released = true

	var login *httptest.ResponseRecorder
	select {
	case login = <-loginResult:
	case <-time.After(time.Second):
		t.Fatal("stale MFA login did not return")
	}
	if login.Code != http.StatusUnauthorized {
		t.Fatalf("stale MFA login status = %d, want %d: %s", login.Code, http.StatusUnauthorized, login.Body.String())
	}
	var apiErr apiErrorResponse
	if err := json.Unmarshal(login.Body.Bytes(), &apiErr); err != nil || apiErr.Code != "login_credentials_changed" {
		t.Fatalf("stale MFA login error = (%+v, %v)", apiErr, err)
	}
	var challenges, sessions int
	if err := s.auth.adminStore.db.QueryRow(`SELECT COUNT(*) FROM admin_auth_challenges WHERE user_id = ? AND kind = ?`, user.ID, adminAuthChallengeKindMFA).Scan(&challenges); err != nil {
		t.Fatal(err)
	}
	if err := s.auth.adminStore.db.QueryRow(`SELECT COUNT(*) FROM user_sessions WHERE user_id = ?`, user.ID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if challenges != 0 || sessions != 0 {
		t.Fatalf("stale MFA login persisted challenges=%d sessions=%d", challenges, sessions)
	}
}

func TestPasskeyLoginCommitRejectsChangedCredentials(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *AdminStore, AdminUser, AdminPasskey)
	}{
		{
			name: "password reset",
			mutate: func(t *testing.T, store *AdminStore, user AdminUser, _ AdminPasskey) {
				if _, err := store.ResetUserPassword(user.ID, "ChangedPassword123"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "passkey deleted",
			mutate: func(t *testing.T, store *AdminStore, user AdminUser, passkey AdminPasskey) {
				actor := ActivityActor{Type: "admin", ID: user.ID, Name: user.Username}
				if _, err := store.DeletePasskeyWithActivity(user.ID, passkey.ID, actor); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, cleanup := setupTestServerWithDB(t, true)
			defer cleanup()
			user, err := s.auth.adminStore.ValidateUserPassword("admin", "password123")
			if err != nil {
				t.Fatal(err)
			}
			passkey := AdminPasskey{
				ID:           "passkey-login-race",
				UserID:       user.ID,
				Name:         "Race key",
				CredentialID: "credential-login-race",
				RPID:         "localhost",
				Origin:       "http://localhost",
				CreatedAt:    time.Now(),
			}
			if _, err := s.auth.adminStore.db.Exec(`INSERT INTO admin_passkeys
				(id, user_id, name, credential_id, credential_json, rp_id, origin, created_at, last_used_at)
				VALUES (?, ?, ?, ?, '{}', ?, ?, ?, NULL)`,
				passkey.ID, passkey.UserID, passkey.Name, passkey.CredentialID, passkey.RPID, passkey.Origin, formatTime(passkey.CreatedAt)); err != nil {
				t.Fatal(err)
			}
			tt.mutate(t, s.auth.adminStore, *user, passkey)
			if _, err := s.revalidatePasskeyLoginCommit(*user, passkey, passkey.CredentialID); !errors.Is(err, errLoginCredentialChanged) {
				t.Fatalf("changed passkey login credential error = %v, want %v", err, errLoginCredentialChanged)
			}
		})
	}
}

func countAdminAuthChallenges(t *testing.T, store *AdminStore) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM admin_auth_challenges`).Scan(&count); err != nil {
		t.Fatalf("count admin auth challenges: %v", err)
	}
	return count
}

func TestAdminStore_TOTPRecoveryCodesAndReset(t *testing.T) {
	store := newInitializedAdminStore(t)
	user, err := store.ValidateAdminPassword("admin", "Admin1234")
	if err != nil {
		t.Fatalf("ValidateAdminPassword failed: %v", err)
	}

	setupToken, _, _, _, err := store.BeginTOTPSetup(*user, "NetsGo")
	if err != nil {
		t.Fatalf("BeginTOTPSetup failed: %v", err)
	}
	challenge, err := store.GetAuthChallenge(setupToken, adminAuthChallengeKindTOTPSetup)
	if err != nil {
		t.Fatalf("GetAuthChallenge failed: %v", err)
	}
	var metadata struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal([]byte(challenge.SessionJSON), &metadata); err != nil {
		t.Fatalf("decode setup metadata: %v", err)
	}
	code, err := totp.GenerateCode(metadata.Secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode failed: %v", err)
	}
	actor := ActivityActor{Type: "admin", ID: user.ID, Name: user.Username}
	if _, _, err := store.ConfirmTOTPSetupWithActivity(user.ID, setupToken, "000000", actor); err == nil {
		t.Fatal("wrong TOTP setup code should be rejected")
	}
	if _, err := store.GetAuthChallenge(setupToken, adminAuthChallengeKindTOTPSetup); err != nil {
		t.Fatalf("wrong TOTP setup code should not consume setup token: %v", err)
	}
	recoveryCodes, _, err := store.ConfirmTOTPSetupWithActivity(user.ID, setupToken, code, actor)
	if err != nil {
		t.Fatalf("ConfirmTOTPSetup failed: %v", err)
	}
	if len(recoveryCodes) != adminRecoveryCodeCount {
		t.Fatalf("expected %d recovery codes, got %d", adminRecoveryCodeCount, len(recoveryCodes))
	}
	enabled, secret := adminUserTOTPState(t, store, user.ID)
	if !enabled || secret == "" {
		t.Fatalf("TOTP should be enabled with a secret, enabled=%v secret=%q", enabled, secret)
	}

	refreshed, err := store.GetAdminUserByID(user.ID)
	if err != nil {
		t.Fatalf("GetAdminUserByID failed: %v", err)
	}
	verified, err := store.VerifyAdminSecurityCredentials(refreshed.ID, "Admin1234", recoveryCodes[0])
	if err != nil {
		t.Fatalf("recovery code should verify once: %v", err)
	}
	if !verified.RecoveryCodeUsed {
		t.Fatal("expected recovery code usage to be reported")
	}
	if _, err := store.VerifyAdminSecurityCredentials(refreshed.ID, "Admin1234", recoveryCodes[0]); err == nil {
		t.Fatal("recovery code should be single-use")
	}

	if _, err := store.db.Exec(`INSERT INTO admin_passkeys (id, user_id, name, credential_id, credential_json, rp_id, origin, created_at) VALUES (?, ?, 'key', 'cred', '{}', 'example.com', 'https://example.com', ?)`,
		generateUUID(), user.ID, formatTime(time.Now())); err != nil {
		t.Fatalf("seed passkey: %v", err)
	}
	if _, err := store.StoreAuthChallenge(user.ID, adminAuthChallengeKindMFA, "{}", nil, time.Minute); err != nil {
		t.Fatalf("StoreAuthChallenge failed: %v", err)
	}
	session := mustCreateSession(t, store, user.ID, user.Username, user.Role, "127.0.0.1", "ua")
	if store.GetSession(session.ID) == nil {
		t.Fatal("expected seeded session")
	}

	if err := store.ResetAdminUser("root", "NewPass123"); err != nil {
		t.Fatalf("ResetAdminUser failed: %v", err)
	}
	newUser, err := store.ValidateAdminPassword("root", "NewPass123")
	if err != nil {
		t.Fatalf("new admin user should validate: %v", err)
	}
	enabled, secret = adminUserTOTPState(t, store, newUser.ID)
	if enabled || secret != "" {
		t.Fatalf("reset admin user should clear TOTP, enabled=%v secret=%q", enabled, secret)
	}
	if countAdminPasskeys(t, store) != 0 {
		t.Fatal("reset admin user should clear passkeys")
	}
	if countAdminAuthChallenges(t, store) != 0 {
		t.Fatal("reset admin user should clear auth challenges")
	}
	if countAdminSessions(t, store) != 0 {
		t.Fatal("reset admin user should clear sessions")
	}
}

func TestAPI_LoginRequiresMFAWhenEnabled(t *testing.T) {
	s, cleanup := setupTestServerWithDB(t, true)
	defer cleanup()

	user, err := s.auth.adminStore.ValidateAdminPassword("admin", "password123")
	if err != nil {
		t.Fatalf("ValidateAdminPassword failed: %v", err)
	}
	if _, err := s.auth.adminStore.db.Exec(`UPDATE users SET totp_enabled = 1, totp_secret = ? WHERE id = ?`, "JBSWY3DPEHPK3PXP", user.ID); err != nil {
		t.Fatalf("enable totp: %v", err)
	}

	body := []byte(`{"username":"admin","password":"password123"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleAPILogin(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("login with TOTP enabled should return 200 mfa_required, got %d: %s", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("mfa_required response should not set a session cookie")
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if payload["mfa_required"] != true || payload["mfa_token"] == "" {
		t.Fatalf("expected mfa_required payload, got %#v", payload)
	}
}

func TestAPI_MFATokenInvalidAfterDisableAndEnable(t *testing.T) {
	s, cleanup := setupTestServerWithDB(t, true)
	defer cleanup()
	admin, err := s.auth.adminStore.GetSingleAdminUser()
	if err != nil {
		t.Fatalf("load administrator: %v", err)
	}
	user, err := s.auth.adminStore.CreateUser("mfa-disable-user", "Password123")
	if err != nil {
		t.Fatalf("create MFA user: %v", err)
	}
	if _, changed, err := s.auth.adminStore.SetUserAdmin(admin.ID, user.ID, true); err != nil || !changed {
		t.Fatalf("promote MFA user = (changed %v, err %v)", changed, err)
	}
	if _, err := s.auth.adminStore.db.Exec(`UPDATE users SET totp_enabled = 1, totp_secret = ? WHERE id = ?`, "JBSWY3DPEHPK3PXP", user.ID); err != nil {
		t.Fatalf("enable user TOTP: %v", err)
	}

	loginBody := []byte(`{"username":"mfa-disable-user","password":"Password123"}`)
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(loginBody))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	s.handleAPILogin(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("begin MFA login status = %d: %s", loginResponse.Code, loginResponse.Body.String())
	}
	var loginPayload struct {
		MFAToken string `json:"mfa_token"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &loginPayload); err != nil || loginPayload.MFAToken == "" {
		t.Fatalf("decode MFA token = (%q, %v)", loginPayload.MFAToken, err)
	}
	if _, changed, err := s.auth.adminStore.SetUserStatus(admin.ID, user.ID, UserStatusDisabled); err != nil || !changed {
		t.Fatalf("disable MFA user = (changed %v, err %v)", changed, err)
	}

	verify := func(stage string) {
		t.Helper()
		body := []byte(`{"mfa_token":"` + loginPayload.MFAToken + `","code":"000000"}`)
		request := httptest.NewRequest(http.MethodPost, "/api/auth/mfa/verify", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		s.handleAPIMFAVerify(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s old MFA token status = %d, want 401: %s", stage, response.Code, response.Body.String())
		}
		var apiErr apiErrorResponse
		if err := json.Unmarshal(response.Body.Bytes(), &apiErr); err != nil || apiErr.Code != "invalid_mfa_token" {
			t.Fatalf("%s old MFA token error = (%+v, %v)", stage, apiErr, err)
		}
	}
	verify("disabled")
	if _, changed, err := s.auth.adminStore.SetUserStatus(admin.ID, user.ID, UserStatusActive); err != nil || !changed {
		t.Fatalf("enable MFA user = (changed %v, err %v)", changed, err)
	}
	verify("re-enabled")
}

func TestAPI_MFAVerifyRateLimitsAfterTenInvalidCodes(t *testing.T) {
	s, cleanup := setupTestServerWithDB(t, true)
	defer cleanup()
	s.auth.mfaLimiter = newMFAAttemptLimiter(time.Minute, 10, 5*time.Minute)

	user, err := s.auth.adminStore.ValidateAdminPassword("admin", "password123")
	if err != nil {
		t.Fatalf("ValidateAdminPassword failed: %v", err)
	}
	if _, err := s.auth.adminStore.db.Exec(`UPDATE users SET totp_enabled = 1, totp_secret = ? WHERE id = ?`, "JBSWY3DPEHPK3PXP", user.ID); err != nil {
		t.Fatalf("enable totp: %v", err)
	}

	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader([]byte(`{"username":"admin","password":"password123"}`)))
	loginReq.RemoteAddr = "203.0.113.10:1000"
	loginResp := httptest.NewRecorder()
	s.handleAPILogin(loginResp, loginReq)
	if loginResp.Code != http.StatusOK {
		t.Fatalf("login should begin MFA challenge, got %d: %s", loginResp.Code, loginResp.Body.String())
	}
	var loginBody struct {
		MFAToken string `json:"mfa_token"`
	}
	if err := json.Unmarshal(loginResp.Body.Bytes(), &loginBody); err != nil {
		t.Fatalf("decode login body: %v", err)
	}
	if loginBody.MFAToken == "" {
		t.Fatal("mfa_token should be present")
	}

	body := []byte(`{"mfa_token":"` + loginBody.MFAToken + `","code":"000000"}`)
	for i := 1; i <= 10; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/mfa/verify", bytes.NewReader(body))
		req.RemoteAddr = "203.0.113.10:1000"
		w := httptest.NewRecorder()
		s.handleAPIMFAVerify(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: want 401, got %d body=%s", i, w.Code, w.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/mfa/verify", bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.10:1000"
	w := httptest.NewRecorder()
	s.handleAPIMFAVerify(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt 11: want 429, got %d body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("rate limited MFA response should include Retry-After")
	}
	var payload apiErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode 429 body: %v", err)
	}
	if payload.Code != "mfa_attempts_exceeded" {
		t.Fatalf("want mfa_attempts_exceeded, got %#v", payload)
	}
}

func TestAPI_MFAVerifyRateLimitSurvivesChallengeRotation(t *testing.T) {
	s, cleanup := setupTestServerWithDB(t, true)
	defer cleanup()
	s.auth.mfaLimiter = newMFAAttemptLimiter(time.Minute, 10, 5*time.Minute)

	user, err := s.auth.adminStore.ValidateAdminPassword("admin", "password123")
	if err != nil {
		t.Fatalf("ValidateAdminPassword failed: %v", err)
	}
	if _, err := s.auth.adminStore.db.Exec(`UPDATE users SET totp_enabled = 1, totp_secret = ? WHERE id = ?`, "JBSWY3DPEHPK3PXP", user.ID); err != nil {
		t.Fatalf("enable totp: %v", err)
	}

	loginFromIP := func() string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader([]byte(`{"username":"admin","password":"password123"}`)))
		req.RemoteAddr = "203.0.113.10:1000"
		w := httptest.NewRecorder()
		s.handleAPILogin(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("login should begin MFA challenge, got %d: %s", w.Code, w.Body.String())
		}
		var body struct {
			MFAToken string `json:"mfa_token"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode login body: %v", err)
		}
		if body.MFAToken == "" {
			t.Fatal("mfa_token should be present")
		}
		return body.MFAToken
	}

	firstToken := loginFromIP()
	for i := 1; i <= 10; i++ {
		body := []byte(`{"mfa_token":"` + firstToken + `","code":"000000"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/auth/mfa/verify", bytes.NewReader(body))
		req.RemoteAddr = "203.0.113.10:1000"
		w := httptest.NewRecorder()
		s.handleAPIMFAVerify(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: want 401, got %d body=%s", i, w.Code, w.Body.String())
		}
	}

	secondToken := loginFromIP()
	if secondToken == firstToken {
		t.Fatal("rotated MFA challenge should issue a new token")
	}
	body := []byte(`{"mfa_token":"` + secondToken + `","code":"000000"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/mfa/verify", bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.10:1000"
	w := httptest.NewRecorder()
	s.handleAPIMFAVerify(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("rotated challenge attempt: want 429, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestAPI_AdminSecurityResponse(t *testing.T) {
	_, handler, token, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()

	resp := doMuxRequest(t, handler, http.MethodGet, "/api/admin/security", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/security: want 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var payload adminSecurityResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode security response: %v", err)
	}
	if payload.User.Username != "admin" {
		t.Fatalf("security user: want admin, got %q", payload.User.Username)
	}
	if payload.TOTPEnabled {
		t.Fatal("TOTP should be disabled by default")
	}
	if payload.Passkeys == nil {
		t.Fatal("passkeys should be an empty array, not null")
	}
}

func TestAdminSecurityUsernameChangeClosesCurrentUserSSE(t *testing.T) {
	s, handler, cleanup := setupActivityAPIAuthTest(t)
	defer cleanup()
	_, adminToken := issueRoleToken(t, s, "admin")
	_, cancelSSE, sseDone := startAuthenticatedSSE(t, handler, "/api/events", adminToken)
	defer cancelSSE()

	body := []byte(`{"current_password":"password123","new_username":"admin-renamed"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/admin/security/username", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("User-Agent", "Go-http-client/1.1")
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("security username change status = %d body=%s", resp.Code, resp.Body.String())
	}
	waitForSSEStop(t, sseDone, "security username change did not close the revoked session SSE")
}

func TestAPI_PasskeyBeginRejectsHTTPNonLocalhost(t *testing.T) {
	t.Setenv("NETSGO_SERVER_ADDR", "http://example.com")
	s, cleanup := setupTestServerWithDB(t, true)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/begin", nil)
	req.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	s.handleAPIPasskeyLoginBegin(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("passkey begin on insecure origin should be rejected with 400, got %d", w.Code)
	}
}

func TestAPI_PasskeyBeginRequiresRegisteredCredential(t *testing.T) {
	t.Setenv("NETSGO_SERVER_ADDR", "http://localhost")
	s, cleanup := setupTestServerWithDB(t, true)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/begin", nil)
	req.Header.Set("Origin", "http://localhost")
	w := httptest.NewRecorder()
	s.handleAPIPasskeyLoginBegin(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("passkey begin without credentials should return 404, got %d: %s", w.Code, w.Body.String())
	}
	var payload apiErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if payload.Code != "passkey_not_registered" {
		t.Fatalf("expected passkey_not_registered, got %#v", payload)
	}
}

func TestAPI_PasskeyBeginRateLimitsChallengeCreation(t *testing.T) {
	t.Setenv("NETSGO_SERVER_ADDR", "http://localhost")
	s, cleanup := setupTestServerWithDB(t, true)
	defer cleanup()

	admin, err := s.auth.adminStore.ValidateUserPassword("admin", "password123")
	if err != nil {
		t.Fatal(err)
	}
	credential := webauthn.Credential{ID: []byte("rate-limit-credential")}
	rawCredential, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.auth.adminStore.db.Exec(`INSERT INTO admin_passkeys
		(id, user_id, name, credential_id, credential_json, rp_id, origin, created_at)
		VALUES (?, ?, 'key', ?, ?, 'localhost', 'http://localhost', ?)`,
		generateUUID(), admin.ID, credentialIDString(credential.ID), string(rawCredential), formatTime(time.Now())); err != nil {
		t.Fatal(err)
	}

	s.auth.passkeyBeginLimiter = NewRateLimiter(RateLimiterConfig{
		WindowSize:  time.Minute,
		MaxRequests: 1,
	})
	defer s.auth.passkeyBeginLimiter.Stop()

	begin := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/begin", nil)
		req.Header.Set("Origin", "http://localhost")
		w := httptest.NewRecorder()
		s.handleAPIPasskeyLoginBegin(w, req)
		return w
	}
	if first := begin(); first.Code != http.StatusOK {
		t.Fatalf("first passkey begin status = %d body=%s", first.Code, first.Body.String())
	}
	second := begin()
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second passkey begin status = %d body=%s", second.Code, second.Body.String())
	}
	if second.Header().Get("Retry-After") == "" {
		t.Fatal("rate-limited passkey begin should include Retry-After")
	}
	var payload apiErrorResponse
	if err := json.Unmarshal(second.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != "passkey_begin_rate_limited" {
		t.Fatalf("rate-limited passkey begin code = %q", payload.Code)
	}
	if got := countAdminAuthChallenges(t, s.auth.adminStore); got != 1 {
		t.Fatalf("rate-limited request created a challenge: count=%d", got)
	}
}

func TestStoreAuthChallengeCapsAnonymousPasskeyLoginChallenges(t *testing.T) {
	s, cleanup := setupTestServerWithDB(t, true)
	defer cleanup()

	for i := 0; i < adminPasskeyLoginChallengeMaxActive; i++ {
		if _, err := s.auth.adminStore.StoreAuthChallenge("", adminAuthChallengeKindPasskeyLogin, "{}", nil, time.Minute); err != nil {
			t.Fatalf("store anonymous passkey challenge %d: %v", i, err)
		}
	}
	if _, err := s.auth.adminStore.StoreAuthChallenge("", adminAuthChallengeKindPasskeyLogin, "{}", nil, time.Minute); !errors.Is(err, errPasskeyLoginChallengeCapacity) {
		t.Fatalf("challenge above capacity error = %v", err)
	}
	if got := countAdminAuthChallenges(t, s.auth.adminStore); got != adminPasskeyLoginChallengeMaxActive {
		t.Fatalf("anonymous passkey challenge count = %d, want %d", got, adminPasskeyLoginChallengeMaxActive)
	}
}

func TestAPI_PasskeyLoginUsesDiscoverableCredentialOwner(t *testing.T) {
	t.Setenv("NETSGO_SERVER_ADDR", "http://localhost")
	s, cleanup := setupTestServerWithDB(t, true)
	defer cleanup()

	initialAdmin, err := s.auth.adminStore.ValidateUserPassword("admin", "password123")
	if err != nil {
		t.Fatal(err)
	}
	secondAdmin, err := s.auth.adminStore.CreateUser("passkey-admin", "Password123")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.auth.adminStore.SetUserAdmin(initialAdmin.ID, secondAdmin.ID, true); err != nil {
		t.Fatal(err)
	}

	credentials := []struct {
		userID     string
		credential webauthn.Credential
	}{
		{userID: initialAdmin.ID, credential: webauthn.Credential{ID: []byte("first-credential")}},
		{userID: secondAdmin.ID, credential: webauthn.Credential{ID: []byte("second-credential")}},
	}
	for _, item := range credentials {
		raw, err := json.Marshal(item.credential)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.auth.adminStore.db.Exec(`INSERT INTO admin_passkeys
			(id, user_id, name, credential_id, credential_json, rp_id, origin, created_at)
			VALUES (?, ?, ?, ?, ?, 'localhost', 'http://localhost', ?)`,
			generateUUID(), item.userID, "key", credentialIDString(item.credential.ID), string(raw), formatTime(time.Now())); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/begin", nil)
	req.Header.Set("Origin", "http://localhost")
	w := httptest.NewRecorder()
	s.handleAPIPasskeyLoginBegin(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("passkey begin status = %d: %s", w.Code, w.Body.String())
	}
	var begin struct {
		ChallengeID string `json:"challenge_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &begin); err != nil {
		t.Fatal(err)
	}
	challenge, err := s.auth.adminStore.GetAuthChallenge(begin.ChallengeID, adminAuthChallengeKindPasskeyLogin)
	if err != nil {
		t.Fatal(err)
	}
	if challenge.UserID != "" {
		t.Fatalf("discoverable login challenge owner = %q, want no candidate user", challenge.UserID)
	}
	session, err := unmarshalWebAuthnSession(challenge.SessionJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.UserID) != 0 || len(session.AllowedCredentialIDs) != 0 {
		t.Fatalf("passkey login session must be discoverable, got user=%q allowed=%d", session.UserID, len(session.AllowedCredentialIDs))
	}
	if _, changed, err := s.auth.adminStore.SetUserAdmin(secondAdmin.ID, initialAdmin.ID, false); err != nil {
		t.Fatalf("demote unrelated challenge candidate: %v", err)
	} else if !changed {
		t.Fatal("expected unrelated challenge candidate to be demoted")
	}
	if _, err := s.auth.adminStore.GetAuthChallenge(begin.ChallengeID, adminAuthChallengeKindPasskeyLogin); err != nil {
		t.Fatalf("discoverable login challenge followed unrelated user lifecycle: %v", err)
	}

	passkeys, err := s.auth.adminStore.ListPasskeysByRP("localhost", "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := s.passkeyLoginUserHandler(passkeys)(credentials[1].credential.ID, []byte(secondAdmin.ID))
	if err != nil {
		t.Fatalf("resolve second administrator passkey: %v", err)
	}
	waUser, ok := resolved.(adminWebAuthnUser)
	if !ok {
		t.Fatalf("resolved user type = %T", resolved)
	}
	if waUser.user.ID != secondAdmin.ID || len(waUser.credentials) != 1 || credentialIDString(waUser.credentials[0].ID) != credentialIDString(credentials[1].credential.ID) {
		t.Fatalf("resolved passkey user = %+v credentials=%v", waUser.user, waUser.credentials)
	}
	if _, err := s.passkeyLoginUserHandler(passkeys)(credentials[0].credential.ID, []byte(secondAdmin.ID)); err == nil {
		t.Fatal("second administrator must not authenticate with the first administrator credential")
	}
}

func TestAPI_PasskeyBeginRejectsOriginMismatch(t *testing.T) {
	t.Setenv("NETSGO_SERVER_ADDR", "https://admin.example.com")
	s, cleanup := setupTestServerWithDB(t, true)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/begin", nil)
	req.Header.Set("Origin", "https://other.example.com")
	w := httptest.NewRecorder()
	s.handleAPIPasskeyLoginBegin(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("passkey begin with mismatched origin should return 400, got %d: %s", w.Code, w.Body.String())
	}
	var payload apiErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if payload.Code != "passkey_unavailable" {
		t.Fatalf("expected passkey_unavailable, got %#v", payload)
	}
}
