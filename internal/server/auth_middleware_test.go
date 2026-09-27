package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// setupMockAdminStore 创建一个用于测试的临时 AdminStore
func setupMockAdminStore(t *testing.T) (*AdminStore, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "admin_store_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	dbPath := filepath.Join(tmpDir, "admin.db")
	store, err := NewAdminStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create AdminStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.bcryptCost = bcrypt.MinCost // 测试用最低强度，避免 bcrypt 拖慢测试套件

	// 初始化一个默认的 admin
	err = store.Initialize("admin", "password123", "localhost", nil)
	if err != nil {
		t.Fatalf("Failed to initialize AdminStore: %v", err)
	}

	cleanup := func() {
		_ = store.Close()
		_ = os.RemoveAll(tmpDir)
	}

	return store, cleanup
}

func clearJWTSecretForTest(t *testing.T, store *AdminStore) {
	t.Helper()

	store.mu.Lock()
	defer store.mu.Unlock()

	if _, err := store.db.Exec(`PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatalf("enable ignore_check_constraints: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE server_config SET initialized = 1, jwt_secret = '' WHERE id = 1`); err != nil {
		t.Fatalf("clear jwt_secret: %v", err)
	}
	if _, err := store.db.Exec(`PRAGMA ignore_check_constraints = OFF`); err != nil {
		t.Fatalf("disable ignore_check_constraints: %v", err)
	}
}

func TestAuthMiddleware_MissingHeader(t *testing.T) {
	s := New(0)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()

	handler := s.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Missing Authorization header should return 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_InvalidFormat(t *testing.T) {
	s := New(0)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "InvalidFormatToken")
	w := httptest.NewRecorder()

	handler := s.RequireAuth(func(w http.ResponseWriter, r *http.Request) {})
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Invalid Authorization format should return 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_NilAuthReturnsStoreUnavailable(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	handler := s.RequirePrincipal(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("nil auth status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	var response apiErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode nil-auth error: %v", err)
	}
	if response.Code != "admin_store_unavailable" {
		t.Fatalf("nil auth error code = %q, want admin_store_unavailable", response.Code)
	}
}

func TestAuthMiddleware_InvalidTokenSignature(t *testing.T) {
	store, cleanup := setupMockAdminStore(t)
	defer cleanup()

	s := New(0)
	s.auth.adminStore = store

	// 创建一个被其他密钥签名的 token
	claims := AdminClaims{
		SessionID: "fake-session",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, _ := token.SignedString([]byte("wrong-secret"))

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	w := httptest.NewRecorder()

	handler := s.RequireAuth(func(w http.ResponseWriter, r *http.Request) {})
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Token with invalid signature should return 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_FallbackSecretTokenRejected(t *testing.T) {
	store, cleanup := setupMockAdminStore(t)
	defer cleanup()

	s := New(0)
	s.auth.adminStore = store

	session := mustCreateSession(t, store, "user-1", "admin", "admin", "127.0.0.1", "test-client")
	claims := AdminClaims{
		SessionID: session.ID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, _ := token.SignedString([]byte("netsgo-dev-fallback-secret"))

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	w := httptest.NewRecorder()

	handler := s.RequireAuth(func(w http.ResponseWriter, r *http.Request) {})
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Token signed with old fallback secret should return 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_ExpiredToken(t *testing.T) {
	store, cleanup := setupMockAdminStore(t)
	defer cleanup()

	s := New(0)
	s.auth.adminStore = store

	// 创建一个已过期的 token
	claims := AdminClaims{
		SessionID: "fake-session",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	secret, err := store.GetJWTSecret()
	if err != nil {
		t.Fatalf("Failed to get JWT Secret: %v", err)
	}
	tokenString, _ := token.SignedString(secret)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	w := httptest.NewRecorder()

	handler := s.RequireAuth(func(w http.ResponseWriter, r *http.Request) {})
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expired token should return 401, got %d", w.Code)
	}
}

func TestGenerateAdminToken_MissingJWTSecret(t *testing.T) {
	store, cleanup := setupMockAdminStore(t)
	defer cleanup()

	s := New(0)
	s.auth.adminStore = store
	clearJWTSecretForTest(t, store)

	session := mustCreateSession(t, store, "user-1", "admin", "admin", "127.0.0.1", "test-client")
	_, err := s.GenerateAdminToken(session)
	if !errors.Is(err, errJWTSecretMissing) {
		t.Fatalf("GenerateAdminToken should return errJWTSecretMissing when JWT Secret is missing, got %v", err)
	}
}

func TestAuthMiddleware_MissingJWTSecret(t *testing.T) {
	store, cleanup := setupMockAdminStore(t)
	defer cleanup()

	s := New(0)
	s.auth.adminStore = store
	clearJWTSecretForTest(t, store)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	handler := s.RequireAuth(func(w http.ResponseWriter, r *http.Request) {})
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Should return 500 when JWT Secret is missing, got %d", w.Code)
	}
}

func TestAuthMiddleware_ValidTokenButSessionRevoked(t *testing.T) {
	store, cleanup := setupMockAdminStore(t)
	defer cleanup()

	s := New(0)
	s.auth.adminStore = store

	// 创建一个合法的 session 并生成 token
	session := mustCreateSession(t, store, "user-1", "admin", "admin", "127.0.0.1", "test-client")
	tokenString, err := s.GenerateAdminToken(session)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	// 模拟 session 被注销/踢出
	mustDeleteSession(t, store, session.ID)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	w := httptest.NewRecorder()

	handler := s.RequireAuth(func(w http.ResponseWriter, r *http.Request) {})
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Token for revoked Session should return 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_ValidTokenSuccess(t *testing.T) {
	store, cleanup := setupMockAdminStore(t)
	defer cleanup()

	s := New(0)
	s.auth.adminStore = store

	session := mustCreateSession(t, store, "user-1", "admin", "admin", "127.0.0.1", "test-client")
	tokenString, err := s.GenerateAdminToken(session)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	req.Header.Set("User-Agent", "test-client")
	w := httptest.NewRecorder()

	// 验证请求是否成功到达了 handler
	handlerCalled := false
	handler := s.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)

		// 验证上下文中是否成功注入了 session 信息
		info := GetSessionFromContext(r.Context())
		if info == nil {
			t.Errorf("SessionInfo not found in context")
		} else if info.SessionID != session.ID {
			t.Errorf("Expected SessionID %s in context, got %s", session.ID, info.SessionID)
		}
	})

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Valid token should return 200, got %d", w.Code)
	}
	if !handlerCalled {
		t.Errorf("Handler was not called")
	}
}

func TestRequireAdminMutationRevalidatesAfterConcurrentDemotion(t *testing.T) {
	store, cleanup := setupMockAdminStore(t)
	defer cleanup()

	s := New(0)
	s.auth.adminStore = store
	initialAdmin, err := store.ValidateUserPassword("admin", "password123")
	if err != nil {
		t.Fatal(err)
	}
	secondAdmin, err := store.CreateUser("second-admin", "Password123")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SetUserAdmin(initialAdmin.ID, secondAdmin.ID, true); err != nil {
		t.Fatal(err)
	}
	secondAdmin, err = store.GetUser(secondAdmin.ID)
	if err != nil {
		t.Fatal(err)
	}
	session := mustCreateSession(t, store, secondAdmin.ID, secondAdmin.Username, secondAdmin.Role, "127.0.0.1", "test-client")
	token, err := s.GenerateAdminToken(session)
	if err != nil {
		t.Fatal(err)
	}

	boundaryReached := make(chan struct{})
	s.adminAuthorizationHook = func(stage string, principal *RequestPrincipal) {
		if stage == "before_mutation_boundary" && principal.UserID == secondAdmin.ID {
			close(boundaryReached)
		}
	}

	// Hold the final authorization boundary so the request is admitted by
	// RequirePrincipal first, then revoke its administrator role before it can
	// enter the privileged handler.
	s.adminAuthorizationMu.Lock()
	called := false
	req := httptest.NewRequest(http.MethodPost, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "test-client")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.RequireAdmin(func(http.ResponseWriter, *http.Request) {
			called = true
		}).ServeHTTP(w, req)
		close(done)
	}()
	select {
	case <-boundaryReached:
	case <-time.After(time.Second):
		s.adminAuthorizationMu.Unlock()
		t.Fatal("request did not reach the final administrator boundary")
	}
	if _, _, err := store.SetUserAdmin(initialAdmin.ID, secondAdmin.ID, false); err != nil {
		s.adminAuthorizationMu.Unlock()
		t.Fatal(err)
	}
	s.adminAuthorizationMu.Unlock()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request did not finish after demotion")
	}
	if called {
		t.Fatal("demoted administrator reached privileged mutation handler")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusUnauthorized, w.Body.String())
	}
}

func TestRequireAdminMutationDoesNotBlockRevocationWhileReadingBody(t *testing.T) {
	store, cleanup := setupMockAdminStore(t)
	defer cleanup()

	s := New(0)
	s.auth.adminStore = store
	initialAdmin, err := store.ValidateUserPassword("admin", "password123")
	if err != nil {
		t.Fatal(err)
	}
	secondAdmin, err := store.CreateUser("slow-body-admin", "Password123")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SetUserAdmin(initialAdmin.ID, secondAdmin.ID, true); err != nil {
		t.Fatal(err)
	}
	secondAdmin, err = store.GetUser(secondAdmin.ID)
	if err != nil {
		t.Fatal(err)
	}

	issueToken := func(user User) string {
		session := mustCreateSession(t, store, user.ID, user.Username, user.Role, "127.0.0.1", "test-client")
		token, err := s.GenerateAdminToken(session)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	initialToken := issueToken(*initialAdmin)
	secondToken := issueToken(secondAdmin)

	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	called := false
	slowReq := httptest.NewRequest(http.MethodPut, "/slow-config", reader)
	slowReq.Header.Set("Authorization", "Bearer "+secondToken)
	slowReq.Header.Set("User-Agent", "test-client")
	slowResp := httptest.NewRecorder()
	slowDone := make(chan struct{})
	go func() {
		s.RequireAdmin(func(http.ResponseWriter, *http.Request) {
			called = true
		}).ServeHTTP(slowResp, slowReq)
		close(slowDone)
	}()
	if _, err := writer.Write([]byte(`{"value":`)); err != nil {
		t.Fatal(err)
	}

	demoteResp := httptest.NewRecorder()
	demoteReq := httptest.NewRequest(http.MethodPut, "/demote", nil)
	demoteReq.Header.Set("Authorization", "Bearer "+initialToken)
	demoteReq.Header.Set("User-Agent", "test-client")
	demoteDone := make(chan struct{})
	go func() {
		s.RequireAdmin(func(w http.ResponseWriter, _ *http.Request) {
			if _, _, err := store.SetUserAdmin(initialAdmin.ID, secondAdmin.ID, false); err != nil {
				writeAPIError(w, http.StatusInternalServerError, "demote_failed", err.Error())
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}).ServeHTTP(demoteResp, demoteReq)
		close(demoteDone)
	}()
	select {
	case <-demoteDone:
	case <-time.After(time.Second):
		_ = writer.CloseWithError(errors.New("test timed out"))
		t.Fatal("administrator revocation blocked on an incomplete request body")
	}
	if demoteResp.Code != http.StatusNoContent {
		_ = writer.CloseWithError(errors.New("demotion failed"))
		t.Fatalf("demote status = %d: %s", demoteResp.Code, demoteResp.Body.String())
	}
	if _, err := writer.Write([]byte(`true}`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-slowDone:
	case <-time.After(time.Second):
		t.Fatal("slow request did not finish after its body completed")
	}
	if called {
		t.Fatal("revoked administrator reached privileged mutation handler")
	}
	if slowResp.Code != http.StatusUnauthorized {
		t.Fatalf("slow request status = %d, want %d: %s", slowResp.Code, http.StatusUnauthorized, slowResp.Body.String())
	}
}

func TestSelfResourceMutationRevalidatesAfterConcurrentSessionRevocation(t *testing.T) {
	store, cleanup := setupMockAdminStore(t)
	defer cleanup()

	s := New(0)
	s.auth.adminStore = store
	user, err := store.CreateUser("slow-body-user", "Password123")
	if err != nil {
		t.Fatal(err)
	}
	session := mustCreateSession(t, store, user.ID, user.Username, user.Role, "127.0.0.1", "test-client")
	token, err := s.GenerateAdminToken(session)
	if err != nil {
		t.Fatal(err)
	}

	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	committed := false
	req := httptest.NewRequest(http.MethodPut, "/api/keys/key-a/disable", reader)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "test-client")
	resp := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.requireSelfResourceScope(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]bool
			if err := decodeJSONRequestBody(r, &body); err != nil {
				writeJSONRequestDecodeError(w, err)
				return
			}
			scope, ok := requireResourceScope(w, r)
			if !ok {
				return
			}
			release, err := s.acquireResourceMutation(scope, true)
			if err != nil {
				writeResourceLifecycleError(w, err)
				return
			}
			defer release()
			committed = true
		}).ServeHTTP(resp, req)
		close(done)
	}()
	if _, err := writer.Write([]byte(`{"value":`)); err != nil {
		t.Fatal(err)
	}

	revoked := make(chan error, 1)
	go func() {
		gate := s.lifecycleGate(user.ID)
		gate.mu.Lock()
		err := store.DeleteSessionsByUserID(user.ID)
		gate.mu.Unlock()
		revoked <- err
	}()
	select {
	case err := <-revoked:
		if err != nil {
			_ = writer.CloseWithError(err)
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		_ = writer.CloseWithError(errors.New("test timed out"))
		t.Fatal("session revocation blocked on an incomplete ordinary-user request body")
	}

	if _, err := writer.Write([]byte(`true}`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ordinary-user mutation did not finish after its body completed")
	}
	if committed {
		t.Fatal("revoked ordinary-user session reached the mutation commit")
	}
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusUnauthorized, resp.Body.String())
	}
}

func TestAuthMiddleware_SessionEnvironmentMismatchActivityIsUnknownActor(t *testing.T) {
	store, cleanup := setupMockAdminStore(t)
	defer cleanup()
	s := New(0)
	s.auth.adminStore = store
	s.ensureSharedStoreReferences()

	session := mustCreateSession(t, store, "user-activity", "admin", "admin", "127.0.0.1", "original-agent")
	tokenString, err := s.GenerateAdminToken(session)
	if err != nil {
		t.Fatalf("GenerateAdminToken() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.RemoteAddr = "192.0.2.77:4321"
	req.Header.Set("Authorization", "Bearer "+tokenString)
	req.Header.Set("User-Agent", "stolen-agent")
	w := httptest.NewRecorder()
	s.RequireAuth(func(http.ResponseWriter, *http.Request) { t.Fatal("mismatched request reached handler") }).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	page, err := s.activityStore.Query(ActivityQuery{Limit: 20})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("security activity = %+v, %v", page.Items, err)
	}
	item := page.Items[0]
	if item.Action != "session_environment_mismatch" || item.Actor.Type != "unknown" || item.Actor.ID != "" || item.Actor.Name != "" {
		t.Fatalf("mismatch activity = %+v", item)
	}
	if strings.Contains(string(item.Payload), "admin") || strings.Contains(string(item.Payload), "stolen-agent") || strings.Contains(string(item.Payload), session.ID) {
		t.Fatalf("security payload leaked identity or UA: %s", item.Payload)
	}
}

func TestGetSessionFromContext_Nil(t *testing.T) {
	ctx := context.Background()
	info := GetSessionFromContext(ctx)
	if info != nil {
		t.Errorf("Empty Context should return nil")
	}
}

func TestAuthMiddleware_StoreNotInitialized(t *testing.T) {
	s := New(0)
	// adminStore 为 nil

	// 造一个假但格式合法的 token，它的签名如果用 nil secret 会使用默认 []byte{} (或直接由于我们代码里使用 secret 而 panic/报错)
	// 为了确保走到 store 未初始化判断，我们需要给它一个 store，但为了避免麻烦，其实 requireAuth 里有检测 store nil 的逻辑

	// Create a token signed with empty secret so it passes signature verification
	claims := AdminClaims{
		SessionID: "fake",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, _ := token.SignedString([]byte{})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	w := httptest.NewRecorder()

	handler := s.RequireAuth(func(w http.ResponseWriter, r *http.Request) {})
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Uninitialized Store should return 500 (or 401 depending on secret validation result), got %d", w.Code)
	}
}

// ========== P5: Cookie 认证测试 ==========

func TestAuthMiddleware_CookieAuth_Success(t *testing.T) {
	store, cleanup := setupMockAdminStore(t)
	defer cleanup()

	s := New(0)
	s.auth.adminStore = store

	session := mustCreateSession(t, store, "user-1", "admin", "admin", "127.0.0.1", "test-client")
	tokenString, err := s.GenerateAdminToken(session)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tokenString})
	req.Header.Set("User-Agent", "test-client")
	w := httptest.NewRecorder()

	handlerCalled := false
	handler := s.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		info := GetSessionFromContext(r.Context())
		if info == nil {
			t.Errorf("SessionInfo not found in context")
		} else if info.SessionID != session.ID {
			t.Errorf("Expected SessionID %s in context, got %s", session.ID, info.SessionID)
		}
		w.WriteHeader(http.StatusOK)
	})

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Valid token in Cookie should return 200, got %d", w.Code)
	}
	if !handlerCalled {
		t.Errorf("Handler was not called")
	}
}

func TestAuthMiddleware_CookieAuth_InvalidToken(t *testing.T) {
	store, cleanup := setupMockAdminStore(t)
	defer cleanup()

	s := New(0)
	s.auth.adminStore = store

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "invalid-token"})
	w := httptest.NewRecorder()

	handler := s.RequireAuth(func(w http.ResponseWriter, r *http.Request) {})
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Invalid token in Cookie should return 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_HeaderPriority(t *testing.T) {
	store, cleanup := setupMockAdminStore(t)
	defer cleanup()

	s := New(0)
	s.auth.adminStore = store

	session := mustCreateSession(t, store, "user-1", "admin", "admin", "127.0.0.1", "test-client")
	validToken, err := s.GenerateAdminToken(session)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	// Header 中放合法 token，Cookie 中放非法 token
	// 应使用 Header 中的 token 认证成功
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+validToken)
	req.Header.Set("User-Agent", "test-client")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "invalid-cookie-token"})
	w := httptest.NewRecorder()

	handler := s.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Header takes precedence over Cookie, should return 200, got %d", w.Code)
	}
}

func TestAuthMiddleware_NoCredentials(t *testing.T) {
	s := New(0)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()

	handler := s.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Missing both header and cookie should return 401, got %d", w.Code)
	}
}
