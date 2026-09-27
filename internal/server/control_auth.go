package server

import (
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"

	"netsgo/pkg/protocol"
)

const wsMaxMessageSize = 1 << 20

const wsDataMaxMessageSize = 512 * 1024

func checkWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}

var controlUpgrader = websocket.Upgrader{
	CheckOrigin:  checkWSOrigin,
	Subprotocols: []string{protocol.WSSubProtocolControl},
}

var dataUpgrader = websocket.Upgrader{
	HandshakeTimeout:  10 * time.Second,
	ReadBufferSize:    32 * 1024,
	WriteBufferSize:   32 * 1024,
	CheckOrigin:       checkWSOrigin,
	EnableCompression: false,
	Subprotocols:      []string{protocol.WSSubProtocolData},
}

func generateDataToken() (string, error) {
	buf, err := randomBytes(32)
	if err != nil {
		return "", fmt.Errorf("failed to generate data token: %w", err)
	}
	return fmt.Sprintf("%x", buf), nil
}

func (s *Server) handleControlWS(w http.ResponseWriter, r *http.Request) {
	conn, err := controlUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("❌ WebSocket upgrade failed: %v", err)
		return
	}
	release, accepted := s.trackManagedConn(conn)
	if !accepted {
		return
	}
	defer release()
	defer func() { _ = conn.Close() }()

	conn.SetReadLimit(wsMaxMessageSize)

	clientAddr := s.clientIP(r)
	if clientAddr == "" {
		clientAddr = remoteIP(r.RemoteAddr)
	}

	log.Printf("📡 New control channel connection: %s [client_ip=%s]", r.RemoteAddr, clientAddr)

	client, err := s.handleAuth(conn, r, clientAddr)
	if err != nil {
		log.Printf("❌ Client authentication failed [%s]: %v", r.RemoteAddr, err)
		return
	}

	info := client.GetInfo()
	log.Printf("✅ Client authenticated: %s (%s/%s) [ID: %s, generation=%d]", info.Hostname, info.OS, info.Arch, client.ID, client.generation)

	if s.store != nil {
		if err := s.store.UpdateHostname(client.ID, info.Hostname); err != nil {
			log.Printf("⚠️ Failed to update tunnel display hostname [%s]: %v", client.ID, err)
		}
	}

	cause := s.controlLoop(client)
	s.invalidateLogicalSessionIfCurrentWithCause(client.ID, client.generation, cause)
}

func (s *Server) handleAuth(conn *websocket.Conn, r *http.Request, clientAddr string) (*ClientConn, error) {
	remoteAddr := r.RemoteAddr
	ip := clientAddr
	if ip == "" {
		ip = remoteIP(remoteAddr)
	}
	// A Client connection is a user-owned runtime resource. Do not retain the
	// old unmanaged path when the stores required to resolve that owner have
	// not been installed yet.
	if s.auth == nil || s.auth.adminStore == nil {
		_ = writeAuthResult(conn, protocol.AuthResponse{
			Success:   false,
			Message:   "authentication temporarily unavailable",
			Code:      protocol.AuthCodeServerUninitialized,
			Retryable: true,
		})
		return nil, fmt.Errorf("authentication failed: required stores are unavailable")
	}
	if allowed, retryAfter := s.auth.allowClientAuthentication(ip); !allowed {
		log.Printf("🚫 Client authentication rate limited [%s, client_ip=%s]: wait %v", remoteAddr, ip, retryAfter)
		slog.Warn("Client authentication rate limited", "ip", ip, "module", "security")
		_ = writeAuthResult(conn, protocol.AuthResponse{
			Success:   false,
			Message:   "authentication failed",
			Code:      protocol.AuthCodeRateLimited,
			Retryable: true,
		})
		s.recordAuthFailure(r, "client_auth_rate_limited", "rate_limited")
		return nil, fmt.Errorf("authentication failed")
	}

	authTimeout := s.auth.authTimeout
	if authTimeout == 0 {
		authTimeout = 30 * time.Second
	}
	if err := conn.SetReadDeadline(time.Now().Add(authTimeout)); err != nil {
		return nil, fmt.Errorf("failed to set auth read deadline: %w", err)
	}

	var msg protocol.Message
	if err := conn.ReadJSON(&msg); err != nil {
		return nil, fmt.Errorf("failed to read authentication message: %w", err)
	}

	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("failed to clear auth read deadline: %w", err)
	}
	if msg.Type != protocol.MsgTypeAuth {
		return nil, fmt.Errorf("expected authentication message, got: %s", msg.Type)
	}

	var authReq protocol.AuthRequest
	if err := msg.ParsePayload(&authReq); err != nil {
		return nil, fmt.Errorf("failed to parse authentication payload: %w", err)
	}
	if authReq.InstallID == "" {
		return nil, fmt.Errorf("authentication failed: install_id cannot be empty")
	}

	var newToken string
	var clientID string
	var bandwidthSettings protocol.BandwidthSettings
	var clientTokenID string

	var registrationActivityID int64
	initialized, err := s.auth.adminStore.IsInitializedE()
	if err != nil {
		log.Printf("⚠️ Server initialization state unavailable, rejecting client connection [%s]: %v", remoteAddr, err)
		slog.Warn("Rejected client connection because initialization state could not be read", "ip", ip, "module", "security")
		_ = writeAuthResult(conn, protocol.AuthResponse{
			Success:   false,
			Message:   "authentication temporarily unavailable",
			Code:      protocol.AuthCodeServerUninitialized,
			Retryable: true,
		})
		return nil, fmt.Errorf("authentication failed: initialization state unavailable: %w", err)
	}
	if !initialized {
		log.Printf("⚠️ Server not initialized, rejecting client connection [%s]", remoteAddr)
		slog.Warn("Rejected client connection because server is not initialized", "ip", ip, "module", "security")
		_ = writeAuthResult(conn, protocol.AuthResponse{
			Success:   false,
			Message:   "authentication failed",
			Code:      protocol.AuthCodeServerUninitialized,
			Retryable: true,
		})
		return nil, fmt.Errorf("authentication failed")
	}

	// Resolve the owner without touching token activity or consuming a key.
	// The credential is validated again after entering that owner's gate.
	var ownerUserID string
	if authReq.Token != "" {
		ownerUserID, err = s.auth.adminStore.ResolveClientTokenOwner(authReq.Token)
		if err != nil {
			if errors.Is(err, ErrUserOwnerUnavailable) {
				return nil, s.rejectClientOwnerAuthentication(conn, r, err)
			}
			code, reason := clientTokenFailureReason(err)
			s.recordAuthFailure(r, "client_auth_failed", reason)
			_ = writeAuthResult(conn, protocol.AuthResponse{Success: false, Message: "authentication failed", Code: code, ClearToken: true})
			return nil, fmt.Errorf("authentication failed")
		}
	} else {
		ownerUserID, err = s.auth.adminStore.ResolveClientKeyOwner(authReq.Key)
		if err != nil {
			if errors.Is(err, ErrUserOwnerUnavailable) {
				return nil, s.rejectClientOwnerAuthentication(conn, r, err)
			}
			code, reason := clientKeyFailureReason(err)
			s.recordAuthFailure(r, "client_auth_failed", reason)
			_ = writeAuthResult(conn, protocol.AuthResponse{Success: false, Message: "authentication failed", Code: code})
			return nil, fmt.Errorf("authentication failed")
		}
	}

	ownerEpoch, releaseOwnerGate, err := s.acquireUserLifecycleRead(ownerUserID, 0, true)
	if err != nil {
		return nil, s.rejectClientOwnerAuthentication(conn, r, err)
	}
	s.clientTunnelMutationMu.Lock()
	mutationLocked := true
	releaseMutation := func() {
		if !mutationLocked {
			return
		}
		mutationLocked = false
		s.clientTunnelMutationMu.Unlock()
		releaseOwnerGate()
	}
	defer releaseMutation()

	if authReq.Token != "" {
		clientToken, err := s.auth.adminStore.ValidateClientToken(authReq.Token, authReq.InstallID)
		if err != nil {
			code, reason := clientTokenFailureReason(err)
			s.recordAuthFailure(r, "client_auth_failed", reason)
			releaseMutation()
			_ = writeAuthResult(conn, protocol.AuthResponse{
				Success:    false,
				Message:    "authentication failed",
				Code:       code,
				ClearToken: true,
			})
			return nil, fmt.Errorf("authentication failed")
		}

		clientID = clientToken.ClientID
		clientTokenID = clientToken.ID
		registered, ok := s.auth.adminStore.GetRegisteredClient(clientID)
		if !ok || registered.OwnerUserID == "" || registered.OwnerUserID != ownerUserID {
			releaseMutation()
			return nil, s.rejectClientOwnerAuthentication(conn, r, fmt.Errorf("client owner could not be resolved"))
		}
		if err := s.ensureClientOwnerOperational(ownerUserID); err != nil {
			releaseMutation()
			return nil, s.rejectClientOwnerAuthentication(conn, r, err)
		}
		bandwidthSettings = registeredClientBandwidthSettings(registered)
		if current, loaded := s.clients.Load(clientID); loaded {
			currentClient := current.(*ClientConn)
			if currentClient.getState() != clientStateClosing {
				log.Printf("⚠️ Concurrent token connection rejected: client_id=%s, install_id=%s, remote=%s", clientID, authReq.InstallID, remoteAddr)
				releaseMutation()
				_ = writeAuthResult(conn, protocol.AuthResponse{
					Success:   false,
					Message:   "authentication failed",
					Code:      protocol.AuthCodeConcurrentSession,
					Retryable: true,
				})
				s.recordAuthFailure(r, "client_auth_failed", "concurrent_session")
				return nil, fmt.Errorf("authentication failed")
			}
		}

		log.Printf("🔑 Client token authenticated [install_id=%s]", authReq.InstallID)
	} else {
		if registered, ok := s.auth.adminStore.GetRegisteredClientByInstallID(authReq.InstallID); ok {
			if current, loaded := s.clients.Load(registered.ID); loaded {
				currentClient := current.(*ClientConn)
				if currentClient.getState() != clientStateClosing {
					releaseMutation()
					_ = writeAuthResult(conn, protocol.AuthResponse{
						Success:   false,
						Message:   "authentication failed",
						Code:      protocol.AuthCodeConcurrentSession,
						Retryable: true,
					})
					s.recordAuthFailure(r, "client_auth_failed", "concurrent_session")
					return nil, fmt.Errorf("authentication failed")
				}
			}
		}

		exchange, err := s.auth.adminStore.RegisterClientAndExchangeToken(authReq.Key, authReq.InstallID, authReq.Client, ip)
		if err != nil {
			code, reason := clientKeyFailureReason(err)
			retryable := errors.Is(err, ErrUserDisabled)
			if retryable {
				code, reason = protocol.AuthCodeUserDisabled, "user_disabled"
			}
			s.recordAuthFailure(r, "client_auth_failed", reason)
			log.Printf("❌ Failed to exchange client key for token [%s]: %v", remoteAddr, err)
			releaseMutation()
			_ = writeAuthResult(conn, protocol.AuthResponse{
				Success:   false,
				Message:   "authentication failed",
				Code:      code,
				Retryable: retryable,
			})
			return nil, fmt.Errorf("authentication failed")
		}
		clientID = exchange.Client.ID
		if exchange.Client.OwnerUserID != ownerUserID {
			releaseMutation()
			return nil, s.rejectClientOwnerAuthentication(conn, r, fmt.Errorf("client owner changed during authentication"))
		}
		if err := s.ensureClientOwnerOperational(ownerUserID); err != nil {
			releaseMutation()
			return nil, s.rejectClientOwnerAuthentication(conn, r, err)
		}
		bandwidthSettings = registeredClientBandwidthSettings(exchange.Client)

		newToken = exchange.Token
		clientTokenID = exchange.TokenRow.ID
		log.Printf("🔑 Client key exchanged for token successfully [install_id=%s]", authReq.InstallID)
		registrationActivityID = exchange.ActivityID
	}

	dataToken, err := generateDataToken()
	if err != nil {
		return nil, err
	}

	// Recheck after all authentication side effects and immediately before
	// publishing the logical session. This closes the normal disable race
	// between token/key validation and the ClientConn becoming visible.
	if err := s.ensureClientOwnerOperational(ownerUserID); err != nil {
		releaseMutation()
		return nil, s.rejectClientOwnerAuthentication(conn, r, err)
	}

	client := &ClientConn{
		ID:             clientID,
		OwnerUserID:    ownerUserID,
		OwnerEpoch:     ownerEpoch,
		InstallID:      authReq.InstallID,
		Info:           authReq.Client,
		RemoteAddr:     ip,
		conn:           conn,
		proxies:        make(map[string]*ProxyTunnel),
		dataToken:      dataToken,
		clientTokenID:  clientTokenID,
		nextTokenTouch: time.Now().Add(clientTokenTouchInterval),
		generation:     s.nextClientGeneration(),
		state:          clientStatePendingData,
	}
	if err := client.SetBandwidthSettings(bandwidthSettings); err != nil {
		return nil, fmt.Errorf("invalid persisted bandwidth settings for client %s: %w", clientID, err)
	}
	s.clients.Store(clientID, client)
	s.startPendingDataTimer(client)
	releaseMutation()
	if registrationActivityID > 0 {
		s.publishActivityID(registrationActivityID)
	}
	if s.controlAuthBeforeResponseHook != nil {
		s.controlAuthBeforeResponseHook(client)
	}

	authResp := protocol.AuthResponse{
		Success:   true,
		Message:   "authentication successful",
		ClientID:  clientID,
		Token:     newToken,
		DataToken: client.dataToken,
		Code:      protocol.AuthCodeOK,
	}
	if err := client.writeAuthResult(authResp); err != nil {
		if current, ok := s.clients.Load(clientID); ok && current == client {
			_ = s.invalidateLogicalSessionIfCurrent(clientID, client.generation, "auth_response_failed")
		}
		return nil, fmt.Errorf("failed to send authentication response: %w", err)
	}

	return client, nil
}

// ensureClientOwnerOperational resolves the unified user policy from the
// authoritative store. A missing store, owner, or user record is deliberately
// an error rather than an implicit active user.
func (s *Server) ensureClientOwnerOperational(ownerUserID string) error {
	if s == nil || s.auth == nil || s.auth.adminStore == nil {
		return fmt.Errorf("required user store is unavailable")
	}
	if ownerUserID == "" {
		return fmt.Errorf("client owner is empty")
	}
	operational, err := s.auth.adminStore.IsUserOperational(ownerUserID)
	if err != nil {
		return fmt.Errorf("resolve client owner: %w", err)
	}
	if !operational {
		return ErrUserDisabled
	}
	return nil
}

func (s *Server) rejectClientOwnerAuthentication(conn *websocket.Conn, r *http.Request, err error) error {
	if errors.Is(err, ErrUserDisabled) {
		s.recordAuthFailure(r, "client_auth_failed", "user_disabled")
		_ = writeAuthResult(conn, protocol.AuthResponse{
			Success:   false,
			Message:   "authentication failed",
			Code:      protocol.AuthCodeUserDisabled,
			Retryable: true,
		})
		return fmt.Errorf("authentication failed: user is disabled")
	}

	log.Printf("⚠️ Client owner resolution unavailable [%s]: %v", r.RemoteAddr, err)
	_ = writeAuthResult(conn, protocol.AuthResponse{
		Success:   false,
		Message:   "authentication temporarily unavailable",
		Code:      protocol.AuthCodeServerUninitialized,
		Retryable: true,
	})
	return fmt.Errorf("authentication failed: owner resolution unavailable: %w", err)
}

func writeAuthResult(conn *websocket.Conn, authResp protocol.AuthResponse) error {
	message, err := protocol.NewMessage(protocol.MsgTypeAuthResp, authResp)
	if err != nil {
		return err
	}
	return conn.WriteJSON(message)
}

func (c *ClientConn) writeAuthResult(authResp protocol.AuthResponse) error {
	message, err := protocol.NewMessage(protocol.MsgTypeAuthResp, authResp)
	if err != nil {
		return err
	}
	return c.writeJSON(message)
}
