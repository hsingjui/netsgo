package socks5wire

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"netsgo/internal/credential"
	"netsgo/pkg/protocol"
)

func TestReadConnectRequestRejectsNonzeroRSV(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

	go func() {
		req := []byte{Version, CommandConnect, 0x01, AddrIPv4, 127, 0, 0, 1, 0, 80}
		_, _ = clientConn.Write(req)
	}()

	_ = serverConn.SetReadDeadline(time.Now().Add(time.Second))
	if _, rep, ok := ReadConnectRequest(serverConn); ok || rep != RepGeneralFailure {
		t.Fatalf("nonzero RSV should be rejected with general failure: ok=%v rep=%#x", ok, rep)
	}
}

func TestReadConnectRequestRejectsUnsupportedCommands(t *testing.T) {
	tests := []struct {
		name    string
		command byte
	}{
		{name: "bind", command: CommandBind},
		{name: "udp associate", command: CommandUDPAssociate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			defer func() { _ = clientConn.Close() }()
			defer func() { _ = serverConn.Close() }()

			go func() {
				req := []byte{Version, tt.command, 0x00, AddrIPv4, 127, 0, 0, 1, 0, 80}
				_, _ = clientConn.Write(req)
			}()

			_ = serverConn.SetReadDeadline(time.Now().Add(time.Second))
			if _, rep, ok := ReadConnectRequest(serverConn); ok || rep != RepCommandUnsupported {
				t.Fatalf("unsupported command should be rejected: ok=%v rep=%#x", ok, rep)
			}
		})
	}
}

func TestReadConnectRequestConnectParsesIPv4(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

	go func() {
		req := []byte{Version, CommandConnect, 0x00, AddrIPv4, 127, 0, 0, 1, 0, 0}
		binary.BigEndian.PutUint16(req[8:10], 443)
		_, _ = clientConn.Write(req)
	}()

	_ = serverConn.SetReadDeadline(time.Now().Add(time.Second))
	req, rep, ok := ReadConnectRequest(serverConn)
	if !ok {
		t.Fatalf("CONNECT should parse: rep=%#x", rep)
	}
	if req.Host != "127.0.0.1" || req.Port != 443 || req.AddrType == "" {
		t.Fatalf("parsed request mismatch: %+v", req)
	}
}

func TestServeHandshakeUsernamePasswordAuth(t *testing.T) {
	hash, err := credential.HashPassword("secret")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	cfg := protocol.SOCKS5ListenConfig{
		Auth: protocol.SOCKS5AuthConfig{
			Type:         protocol.SOCKS5AuthTypeUsernamePassword,
			Username:     "alice",
			PasswordHash: hash,
		},
	}

	for _, tc := range []struct {
		name     string
		password string
		wantOK   bool
		wantAuth byte
	}{
		{name: "correct", password: "secret", wantOK: true, wantAuth: 0x00},
		{name: "wrong password", password: "wrong", wantOK: false, wantAuth: 0x01},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			defer func() { _ = clientConn.Close() }()
			defer func() { _ = serverConn.Close() }()

			resultCh := make(chan struct {
				req ConnectRequest
				ok  bool
			}, 1)
			go func() {
				req, ok := ServeHandshake(serverConn, cfg)
				resultCh <- struct {
					req ConnectRequest
					ok  bool
				}{req: req, ok: ok}
			}()

			_ = clientConn.SetDeadline(time.Now().Add(time.Second))
			if _, err := clientConn.Write([]byte{Version, 0x01, MethodUsernamePass}); err != nil {
				t.Fatalf("write method negotiation: %v", err)
			}
			var methodResp [2]byte
			if _, err := io.ReadFull(clientConn, methodResp[:]); err != nil {
				t.Fatalf("read method response: %v", err)
			}
			if methodResp != [2]byte{Version, MethodUsernamePass} {
				t.Fatalf("method response mismatch: %#v", methodResp)
			}

			authReq := []byte{AuthVersion, byte(len("alice"))}
			authReq = append(authReq, []byte("alice")...)
			authReq = append(authReq, byte(len(tc.password)))
			authReq = append(authReq, []byte(tc.password)...)
			if _, err := clientConn.Write(authReq); err != nil {
				t.Fatalf("write auth request: %v", err)
			}
			var authResp [2]byte
			if _, err := io.ReadFull(clientConn, authResp[:]); err != nil {
				t.Fatalf("read auth response: %v", err)
			}
			if authResp != [2]byte{AuthVersion, tc.wantAuth} {
				t.Fatalf("auth response mismatch: got %#v", authResp)
			}

			if tc.wantOK {
				req := []byte{Version, CommandConnect, 0x00, AddrIPv4, 127, 0, 0, 1, 0, 0}
				binary.BigEndian.PutUint16(req[8:10], 443)
				if _, err := clientConn.Write(req); err != nil {
					t.Fatalf("write CONNECT request: %v", err)
				}
			}
			select {
			case result := <-resultCh:
				if result.ok != tc.wantOK {
					t.Fatalf("ServeHandshake ok: got %v want %v", result.ok, tc.wantOK)
				}
				if tc.wantOK && (result.req.Host != "127.0.0.1" || result.req.Port != 443) {
					t.Fatalf("request mismatch: %+v", result.req)
				}
			case <-time.After(time.Second):
				t.Fatal("ServeHandshake did not return")
			}
		})
	}
}

func TestNegotiateMethodRejectsNoAcceptableMethods(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

	resultCh := make(chan struct {
		method byte
		ok     bool
	}, 1)
	go func() {
		method, ok := NegotiateMethod(serverConn, protocol.SOCKS5AuthTypeUsernamePassword)
		resultCh <- struct {
			method byte
			ok     bool
		}{method: method, ok: ok}
	}()

	_ = clientConn.SetDeadline(time.Now().Add(time.Second))
	if _, err := clientConn.Write([]byte{Version, 0x01, MethodNoAuth}); err != nil {
		t.Fatalf("write method negotiation: %v", err)
	}
	var resp [2]byte
	if _, err := io.ReadFull(clientConn, resp[:]); err != nil {
		t.Fatalf("read method response: %v", err)
	}
	if resp != [2]byte{Version, MethodNoAcceptable} {
		t.Fatalf("method response mismatch: %#v", resp)
	}
	select {
	case result := <-resultCh:
		if result.ok || result.method != MethodNoAcceptable {
			t.Fatalf("NegotiateMethod should reject unsupported methods, got method=%#x ok=%v", result.method, result.ok)
		}
	case <-time.After(time.Second):
		t.Fatal("NegotiateMethod did not return")
	}
}
