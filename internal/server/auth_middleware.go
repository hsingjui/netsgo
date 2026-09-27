package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const sessionCookieName = "netsgo_session"

// extractToken extracts the JWT token from the request.
// Priority: Authorization header > Cookie
func extractToken(r *http.Request) string {
	// 1. Authorization: Bearer <token>
	if auth := r.Header.Get("Authorization"); auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
			return parts[1]
		}
	}
	// 2. Cookie fallback (browser)
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	return ""
}

// AdminClaims JWT 载荷 — 仅存 sessionID，业务信息从 session 中查找
type AdminClaims struct {
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

// RequestPrincipal is reconstructed from user_sessions JOIN users for every
// request.  Role is a presentation compatibility field; IsAdmin is the only
// authorization flag and never comes from a JWT claim.
type RequestPrincipal struct {
	SessionID string
	UserID    string
	Username  string
	IsAdmin   bool
	Role      string
}

// SessionInfo is retained as a source-compatible alias for existing handlers.
type SessionInfo = RequestPrincipal

// GenerateAdminToken 生成一个新的 JWT Token（绑定 session）
func (s *Server) GenerateAdminToken(session *AdminSession) (string, error) {
	secret, err := s.auth.adminStore.GetJWTSecret()
	if err != nil {
		return "", fmt.Errorf("get jwt secret: %w", err)
	}

	claims := AdminClaims{
		SessionID: session.ID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(session.ExpiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// RequireAuth 验证 JWT 令牌 + 服务端 session 是否有效
// 支持两种认证方式（优先级从高到低）:
//  1. Authorization: Bearer <token> — API 编程调用
//  2. Cookie netsgo_session — 浏览器 Web 面板
//
// JWT 只作为 session 载体，真正的权限判定来自 session 状态
func (s *Server) RequirePrincipal(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		tokenString := extractToken(r)
		if tokenString == "" {
			writeAPIError(w, http.StatusUnauthorized, "missing_credentials", "missing credentials")
			return
		}

		// 🔑 核心：检查 adminStore 是否已初始化
		if s.auth == nil || s.auth.adminStore == nil {
			writeAPIError(w, http.StatusInternalServerError, "admin_store_unavailable", "admin store not initialized")
			return
		}
		claims := &AdminClaims{}
		secret, err := s.auth.adminStore.GetJWTSecret()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "jwt_secret_unavailable", "jwt secret unavailable")
			return
		}

		token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return secret, nil
		})

		if err != nil || !token.Valid {
			writeAPIError(w, http.StatusUnauthorized, "invalid_or_expired_token", "invalid or expired token")
			return
		}

		session := s.auth.adminStore.GetSession(claims.SessionID)
		if session == nil {
			// session 被删除（登出/踢出/过期）→ 401
			writeAPIError(w, http.StatusUnauthorized, "session_expired_or_revoked", "session expired or revoked")
			return
		}

		// 同一浏览器 session 内 UA 不会改变，变化说明 token 可能被盗用
		if r.UserAgent() != session.UserAgent {
			slog.Warn("session UA mismatch, possible token theft",
				"session_id", session.ID, "user", session.Username, "module", "security")
			s.recordSessionEnvironmentMismatch(r, session)
			writeAPIError(w, http.StatusUnauthorized, "session_environment_mismatch", "session environment mismatch")
			return
		}

		// session 有效 → 注入用户信息到 Context
		info := &RequestPrincipal{
			SessionID: session.ID,
			UserID:    session.UserID,
			Username:  session.Username,
			IsAdmin:   session.Role == "admin",
			Role:      session.Role,
		}
		ctx := context.WithValue(r.Context(), sessionContextKey, info)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// RequireAuth remains the compatibility name for handlers that allow any
// authenticated operational user.  New code should use RequirePrincipal.
func (s *Server) RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return s.RequirePrincipal(next)
}

func (s *Server) RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.RequirePrincipal(func(w http.ResponseWriter, r *http.Request) {
		principal := GetPrincipalFromContext(r.Context())
		if principal == nil || !principal.IsAdmin {
			writeAPIError(w, http.StatusForbidden, "administrator_access_required", "administrator access required")
			return
		}

		if isAdminMutationRequest(r) {
			if !bufferAdminMutationBody(w, r) {
				return
			}
			s.runAdminAuthorizationHook("before_mutation_boundary", principal)
			s.adminAuthorizationMu.Lock()
			defer s.adminAuthorizationMu.Unlock()
			s.runAdminAuthorizationHook("after_mutation_boundary", principal)
			if !s.revalidateAdminPrincipal(w, principal) {
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// Read-only and streaming handlers only need a current decision at their
		// boundary. Holding the read lock for an SSE lifetime would prevent the
		// revocation path that is responsible for closing that stream.
		s.adminAuthorizationMu.RLock()
		valid := s.revalidateAdminPrincipal(w, principal)
		s.adminAuthorizationMu.RUnlock()
		if !valid {
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bufferAdminMutationBody(w http.ResponseWriter, r *http.Request) bool {
	if r.Body == nil || r.Body == http.NoBody {
		return true
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, passkeyJSONRequestBodyLimitBytes+1))
	_ = r.Body.Close()
	if err != nil {
		writeJSONRequestDecodeError(w, err)
		return false
	}
	if int64(len(body)) > passkeyJSONRequestBodyLimitBytes {
		writeJSONRequestDecodeError(w, errJSONRequestBodyTooLarge)
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return true
}

func isAdminMutationRequest(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func (s *Server) revalidateAdminPrincipal(w http.ResponseWriter, principal *RequestPrincipal) bool {
	if principal == nil || s.auth == nil || s.auth.adminStore == nil {
		writeAPIError(w, http.StatusUnauthorized, "session_expired_or_revoked", "session expired or revoked")
		return false
	}
	session := s.auth.adminStore.GetSession(principal.SessionID)
	if session == nil || session.UserID != principal.UserID {
		writeAPIError(w, http.StatusUnauthorized, "session_expired_or_revoked", "session expired or revoked")
		return false
	}
	if session.Role != "admin" {
		writeAPIError(w, http.StatusForbidden, "administrator_access_required", "administrator access required")
		return false
	}
	principal.Username = session.Username
	principal.Role = session.Role
	principal.IsAdmin = true
	return true
}

func (s *Server) runAdminAuthorizationHook(stage string, principal *RequestPrincipal) {
	if s != nil && s.adminAuthorizationHook != nil {
		s.adminAuthorizationHook(stage, principal)
	}
}

// sessionContextKey context key 类型（避免碰撞）
type contextKey string

const sessionContextKey contextKey = "session_info"

// GetSessionFromContext 从 Context 中提取当前请求的 session 信息
func GetSessionFromContext(ctx context.Context) *SessionInfo {
	info, ok := ctx.Value(sessionContextKey).(*SessionInfo)
	if !ok {
		return nil
	}
	return info
}

func GetPrincipalFromContext(ctx context.Context) *RequestPrincipal {
	info, ok := ctx.Value(sessionContextKey).(*RequestPrincipal)
	if !ok {
		return nil
	}
	return info
}

// setSessionCookie 设置 httpOnly session cookie

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/api",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.isHTTPSRequest(r),
		SameSite: http.SameSiteStrictMode,
	})
}

// clearSessionCookie 清除 session cookie
func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/api",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.isHTTPSRequest(r),
		SameSite: http.SameSiteStrictMode,
	})
}
