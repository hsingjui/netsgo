package server

import (
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"netsgo/pkg/protocol"
)

func TestControlAuthResponseUsesPublishedClientWriter(t *testing.T) {
	s, ts, cleanup := setupWSTestNoConn(t)
	defer cleanup()

	published := make(chan *ClientConn, 1)
	resume := make(chan struct{})
	hookReturned := make(chan struct{})
	resumeClosed := false
	defer func() {
		if !resumeClosed {
			close(resume)
		}
	}()
	s.controlAuthBeforeResponseHook = func(client *ClientConn) {
		published <- client
		<-resume
		close(hookReturned)
	}

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/control"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("open control channel: %v", err)
	}
	defer mustClose(t, conn)

	caps := protocol.DefaultClientCapabilities()
	authMessage, err := protocol.NewMessage(protocol.MsgTypeAuth, protocol.AuthRequest{
		Key:       "test-key",
		InstallID: "install-auth-writer-serialization",
		Client: protocol.ClientInfo{
			Hostname:     "auth-writer-serialization",
			OS:           "linux",
			Arch:         "amd64",
			Version:      "0.1.0",
			Capabilities: &caps,
		},
	})
	if err != nil {
		t.Fatalf("create auth message: %v", err)
	}
	if err := conn.WriteJSON(authMessage); err != nil {
		t.Fatalf("send auth message: %v", err)
	}

	var client *ClientConn
	select {
	case client = <-published:
	case <-time.After(testReadTimeout(2 * time.Second)):
		t.Fatal("authentication did not publish the client before responding")
	}

	// Hold the writer used by every post-publication control message. The auth
	// response must wait on this same lock; writing directly to the WebSocket
	// would make it observable before the lock is released.
	client.writeMu.Lock()
	writerLocked := true
	defer func() {
		if writerLocked {
			client.writeMu.Unlock()
		}
	}()
	close(resume)
	resumeClosed = true
	select {
	case <-hookReturned:
	case <-time.After(testReadTimeout(2 * time.Second)):
		t.Fatal("authentication response hook did not return")
	}

	type readResult struct {
		message protocol.Message
		err     error
	}
	readDone := make(chan readResult, 1)
	go func() {
		var message protocol.Message
		readDone <- readResult{message: message, err: conn.ReadJSON(&message)}
	}()

	select {
	case result := <-readDone:
		client.writeMu.Unlock()
		writerLocked = false
		if result.err != nil {
			t.Fatalf("auth response read failed while writer was locked: %v", result.err)
		}
		t.Fatalf("auth response bypassed the published client's writer lock: type=%s", result.message.Type)
	case <-time.After(200 * time.Millisecond):
	}

	client.writeMu.Unlock()
	writerLocked = false
	select {
	case result := <-readDone:
		if result.err != nil {
			t.Fatalf("read serialized auth response: %v", result.err)
		}
		if result.message.Type != protocol.MsgTypeAuthResp {
			t.Fatalf("serialized response type = %s, want %s", result.message.Type, protocol.MsgTypeAuthResp)
		}
		var authResp protocol.AuthResponse
		if err := result.message.ParsePayload(&authResp); err != nil {
			t.Fatalf("parse serialized auth response: %v", err)
		}
		if !authResp.Success || authResp.Code != protocol.AuthCodeOK {
			t.Fatalf("serialized auth response = %+v", authResp)
		}
	case <-time.After(testReadTimeout(2 * time.Second)):
		t.Fatal("auth response did not resume after releasing the writer lock")
	}
}
