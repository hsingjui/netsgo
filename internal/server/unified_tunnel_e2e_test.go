package server

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	clientpkg "netsgo/internal/client"
	"netsgo/internal/socks5wire"
)

func TestUnifiedServerExposeSOCKS5EndToEndWithRealClient(t *testing.T) {
	s := New(0)
	initTestAdminStore(t, s)
	s.store = newTestTunnelStore(t)
	ts := newIPv4HTTPTestServer(t, s.newHTTPMux())
	defer ts.Close()
	token := loginAdminTokenLocal(t, s.StartHTTPOnly(), "admin", "password123")

	targetAddr, targetPort := startTestTCPEchoService(t)
	targetClient := startUnifiedE2EClient(t, s, ts.URL, "install-e2e-server-socks5-target")
	targetID := waitForUnifiedE2EClientReady(t, s, targetClient)
	ingressPort := reserveTCPPort(t)

	create := []byte(fmt.Sprintf(`{
		"name":"e2e-server-socks5",
		"topology":"server_expose",
		"ingress":{"location":"server","type":"socks5_listen","config":{
			"bind_ip":"127.0.0.1",
			"port":%d,
			"allowed_source_cidrs":["127.0.0.0/8"],
			"auth":{"type":"none"}
		}},
		"target":{"location":"client","client_id":"%s","type":"socks5_connect_handler","config":{
			"allowed_target_cidrs":["127.0.0.0/8"],
			"allowed_target_hosts":["%s"],
			"allowed_target_ports":[%d],
			"dial_timeout_seconds":5
		}},
		"transport_policy":"server_relay_only",
		"confirm_no_auth_risk":true
	}`, ingressPort, targetID, targetAddr, targetPort))
	resp := doMuxRequest(t, s.StartHTTPOnly(), http.MethodPost, "/api/tunnels", token, create)
	if resp.Code != http.StatusCreated {
		t.Fatalf("server_expose SOCKS5 create: want 201, got %d body=%s", resp.Code, resp.Body.String())
	}
	var created tunnelSpecAPI
	if err := mustDecodeJSON(t, resp.Body, &created); err != nil {
		t.Fatalf("decode created tunnel: %v", err)
	}
	waitForUnifiedTunnelRuntimeState(t, s, token, created.ID, tunnelRuntimeStateActive)

	conn := dialSOCKS5ConnectNoAuth(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(ingressPort)), targetAddr, targetPort)
	defer func() { _ = conn.Close() }()
	assertSOCKS5Echo(t, conn, []byte("server-expose socks5 payload"))
}

func TestUnifiedClientToClientTCPEndToEndWithRealClients(t *testing.T) {
	s := New(0)
	initTestAdminStore(t, s)
	s.store = newTestTunnelStore(t)
	ts := newIPv4HTTPTestServer(t, s.newHTTPMux())
	defer ts.Close()
	token := loginAdminTokenLocal(t, s.StartHTTPOnly(), "admin", "password123")

	targetAddr, targetPort := startTestTCPEchoService(t)
	targetClient := startUnifiedE2EClient(t, s, ts.URL, "install-e2e-c2c-target")
	ingressClient := startUnifiedE2EClient(t, s, ts.URL, "install-e2e-c2c-ingress")
	targetID := waitForUnifiedE2EClientReady(t, s, targetClient)
	ingressID := waitForUnifiedE2EClientReady(t, s, ingressClient)
	ingressPort := reserveTCPPort(t)

	create := []byte(fmt.Sprintf(`{
		"name":"e2e-c2c-tcp",
		"topology":"client_to_client",
		"ingress":{"location":"client","client_id":"%s","type":"tcp_listen","config":{"bind_ip":"127.0.0.1","port":%d,"allowed_source_cidrs":["0.0.0.0/0","::/0"]}},
		"target":{"location":"client","client_id":"%s","type":"tcp_service","config":{"ip":"%s","port":%d}},
		"transport_policy":"server_relay_only"
	}`, ingressID, ingressPort, targetID, targetAddr, targetPort))
	resp := doMuxRequest(t, s.StartHTTPOnly(), http.MethodPost, "/api/tunnels", token, create)
	if resp.Code != http.StatusCreated {
		t.Fatalf("client_to_client create: want 201, got %d body=%s", resp.Code, resp.Body.String())
	}
	var created tunnelSpecAPI
	if err := mustDecodeJSON(t, resp.Body, &created); err != nil {
		t.Fatalf("decode created tunnel: %v", err)
	}
	waitForUnifiedTunnelRuntimeState(t, s, token, created.ID, tunnelRuntimeStateActive)

	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ingressPort)), 2*time.Second)
	if err != nil {
		t.Fatalf("dial client ingress listener: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set client ingress deadline: %v", err)
	}

	payload := []byte("client-to-client tcp payload")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write ingress payload: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read echoed payload through c2c tunnel: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echoed payload mismatch: got %q want %q", got, payload)
	}
}

func TestUnifiedClientToClientSOCKS5EndToEndWithRealClients(t *testing.T) {
	s := New(0)
	initTestAdminStore(t, s)
	s.store = newTestTunnelStore(t)
	ts := newIPv4HTTPTestServer(t, s.newHTTPMux())
	defer ts.Close()
	token := loginAdminTokenLocal(t, s.StartHTTPOnly(), "admin", "password123")

	targetAddr, targetPort := startTestTCPEchoService(t)
	targetClient := startUnifiedE2EClient(t, s, ts.URL, "install-e2e-c2c-socks5-target")
	ingressClient := startUnifiedE2EClient(t, s, ts.URL, "install-e2e-c2c-socks5-ingress")
	targetID := waitForUnifiedE2EClientReady(t, s, targetClient)
	ingressID := waitForUnifiedE2EClientReady(t, s, ingressClient)
	ingressPort := reserveTCPPort(t)

	create := []byte(fmt.Sprintf(`{
		"name":"e2e-c2c-socks5",
		"topology":"client_to_client",
		"ingress":{"location":"client","client_id":"%s","type":"socks5_listen","config":{
			"bind_ip":"127.0.0.1",
			"port":%d,
			"allowed_source_cidrs":["127.0.0.0/8"],
			"auth":{"type":"none"}
		}},
		"target":{"location":"client","client_id":"%s","type":"socks5_connect_handler","config":{
			"allowed_target_cidrs":["127.0.0.0/8"],
			"allowed_target_hosts":["%s"],
			"allowed_target_ports":[%d],
			"dial_timeout_seconds":5
		}},
		"transport_policy":"server_relay_only"
	}`, ingressID, ingressPort, targetID, targetAddr, targetPort))
	resp := doMuxRequest(t, s.StartHTTPOnly(), http.MethodPost, "/api/tunnels", token, create)
	if resp.Code != http.StatusCreated {
		t.Fatalf("client_to_client SOCKS5 create: want 201, got %d body=%s", resp.Code, resp.Body.String())
	}
	var created tunnelSpecAPI
	if err := mustDecodeJSON(t, resp.Body, &created); err != nil {
		t.Fatalf("decode created tunnel: %v", err)
	}
	waitForUnifiedTunnelRuntimeState(t, s, token, created.ID, tunnelRuntimeStateActive)

	conn := dialSOCKS5ConnectNoAuth(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(ingressPort)), targetAddr, targetPort)
	defer func() { _ = conn.Close() }()
	assertSOCKS5Echo(t, conn, []byte("client-to-client socks5 payload"))
}

func TestUnifiedClientToClientUDPEndToEndWithRealClients(t *testing.T) {
	s := New(0)
	initTestAdminStore(t, s)
	s.store = newTestTunnelStore(t)
	ts := newIPv4HTTPTestServer(t, s.newHTTPMux())
	defer ts.Close()
	token := loginAdminTokenLocal(t, s.StartHTTPOnly(), "admin", "password123")

	targetAddr, targetPort := startTestUDPEchoService(t)
	targetClient := startUnifiedE2EClient(t, s, ts.URL, "install-e2e-c2c-udp-target")
	ingressClient := startUnifiedE2EClient(t, s, ts.URL, "install-e2e-c2c-udp-ingress")
	targetID := waitForUnifiedE2EClientReady(t, s, targetClient)
	ingressID := waitForUnifiedE2EClientReady(t, s, ingressClient)
	ingressPort := reserveUDPPort(t)

	create := []byte(fmt.Sprintf(`{
		"name":"e2e-c2c-udp",
		"topology":"client_to_client",
		"ingress":{"location":"client","client_id":"%s","type":"udp_listen","config":{"bind_ip":"127.0.0.1","port":%d,"allowed_source_cidrs":["0.0.0.0/0","::/0"]}},
		"target":{"location":"client","client_id":"%s","type":"udp_service","config":{"ip":"%s","port":%d}},
		"transport_policy":"server_relay_only"
	}`, ingressID, ingressPort, targetID, targetAddr, targetPort))
	resp := doMuxRequest(t, s.StartHTTPOnly(), http.MethodPost, "/api/tunnels", token, create)
	if resp.Code != http.StatusCreated {
		t.Fatalf("client_to_client create: want 201, got %d body=%s", resp.Code, resp.Body.String())
	}
	var created tunnelSpecAPI
	if err := mustDecodeJSON(t, resp.Body, &created); err != nil {
		t.Fatalf("decode created tunnel: %v", err)
	}
	waitForUnifiedTunnelRuntimeState(t, s, token, created.ID, tunnelRuntimeStateActive)

	conn, err := net.DialTimeout("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ingressPort)), 2*time.Second)
	if err != nil {
		t.Fatalf("dial client UDP ingress listener: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set client UDP ingress deadline: %v", err)
	}

	payload := []byte("client-to-client udp payload")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write UDP ingress payload: %v", err)
	}
	got := make([]byte, 1024)
	n, err := conn.Read(got)
	if err != nil {
		t.Fatalf("read echoed UDP payload through c2c tunnel: %v", err)
	}
	if string(got[:n]) != string(payload) {
		t.Fatalf("echoed UDP payload mismatch: got %q want %q", got[:n], payload)
	}
}

func startUnifiedE2EClient(t *testing.T, s *Server, serverURL, installID string) *clientpkg.Client {
	t.Helper()
	c := clientpkg.New(serverURL, "test-key")
	c.InstallID = installID
	c.DataDir = t.TempDir()
	c.DisableReconnect = true
	c.Logger = clientpkg.NewEventLogger(clientpkg.LogFormatJSON, io.Discard)

	errCh := make(chan error, 1)
	go func() {
		errCh <- c.Start()
	}()
	t.Cleanup(func() {
		c.Shutdown()
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("client %s did not shut down", installID)
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("client %s exited before ready: %v", installID, err)
		default:
		}
		if id := c.CurrentClientID(); id != "" {
			if live, ok := s.loadLiveClient(id); ok && clientHasDataSession(live) {
				return c
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("client %s did not become ready", installID)
	return c
}

func waitForUnifiedE2EClientReady(t *testing.T, s *Server, c *clientpkg.Client) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		id := c.CurrentClientID()
		if id != "" {
			if live, ok := s.loadLiveClient(id); ok && clientHasDataSession(live) {
				return id
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("client did not keep a ready live session")
	return ""
}

func dialSOCKS5ConnectNoAuth(t *testing.T, proxyAddr, targetHost string, targetPort int) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial SOCKS5 proxy %s: %v", proxyAddr, err)
	}
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		_ = conn.Close()
		t.Fatalf("set SOCKS5 client deadline: %v", err)
	}
	if _, err := conn.Write([]byte{socks5wire.Version, 0x01, socks5wire.MethodNoAuth}); err != nil {
		_ = conn.Close()
		t.Fatalf("write SOCKS5 method negotiation: %v", err)
	}
	var methodResp [2]byte
	if _, err := io.ReadFull(conn, methodResp[:]); err != nil {
		_ = conn.Close()
		t.Fatalf("read SOCKS5 method response: %v", err)
	}
	if methodResp != [2]byte{socks5wire.Version, socks5wire.MethodNoAuth} {
		_ = conn.Close()
		t.Fatalf("SOCKS5 method response: got %#v", methodResp)
	}

	req := buildSOCKS5ConnectRequest(t, targetHost, targetPort)
	if _, err := conn.Write(req); err != nil {
		_ = conn.Close()
		t.Fatalf("write SOCKS5 CONNECT request: %v", err)
	}
	if rep := readSOCKS5Reply(t, conn); rep != socks5wire.RepSuccess {
		_ = conn.Close()
		t.Fatalf("SOCKS5 CONNECT reply: want success, got %#x", rep)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		t.Fatalf("clear SOCKS5 client deadline: %v", err)
	}
	return conn
}

func buildSOCKS5ConnectRequest(t *testing.T, targetHost string, targetPort int) []byte {
	t.Helper()
	if targetPort < 1 || targetPort > 65535 {
		t.Fatalf("invalid target port %d", targetPort)
	}
	if ip := net.ParseIP(targetHost); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			req := []byte{socks5wire.Version, socks5wire.CommandConnect, 0x00, socks5wire.AddrIPv4, ip4[0], ip4[1], ip4[2], ip4[3], 0, 0}
			binary.BigEndian.PutUint16(req[8:10], uint16(targetPort))
			return req
		}
		ip16 := ip.To16()
		if ip16 == nil {
			t.Fatalf("invalid IP target %q", targetHost)
		}
		req := make([]byte, 4+16+2)
		req[0], req[1], req[2], req[3] = socks5wire.Version, socks5wire.CommandConnect, 0x00, socks5wire.AddrIPv6
		copy(req[4:20], ip16)
		binary.BigEndian.PutUint16(req[20:22], uint16(targetPort))
		return req
	}
	if len(targetHost) == 0 || len(targetHost) > 255 {
		t.Fatalf("invalid domain target %q", targetHost)
	}
	req := []byte{socks5wire.Version, socks5wire.CommandConnect, 0x00, socks5wire.AddrDomain, byte(len(targetHost))}
	req = append(req, []byte(targetHost)...)
	req = append(req, 0, 0)
	binary.BigEndian.PutUint16(req[len(req)-2:], uint16(targetPort))
	return req
}

func readSOCKS5Reply(t *testing.T, conn net.Conn) byte {
	t.Helper()
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		t.Fatalf("read SOCKS5 reply header: %v", err)
	}
	if header[0] != socks5wire.Version || header[2] != 0x00 {
		t.Fatalf("invalid SOCKS5 reply header: %#v", header)
	}
	switch header[3] {
	case socks5wire.AddrIPv4:
		var rest [6]byte
		if _, err := io.ReadFull(conn, rest[:]); err != nil {
			t.Fatalf("read SOCKS5 IPv4 reply body: %v", err)
		}
	case socks5wire.AddrIPv6:
		var rest [18]byte
		if _, err := io.ReadFull(conn, rest[:]); err != nil {
			t.Fatalf("read SOCKS5 IPv6 reply body: %v", err)
		}
	case socks5wire.AddrDomain:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			t.Fatalf("read SOCKS5 domain reply length: %v", err)
		}
		rest := make([]byte, int(length[0])+2)
		if _, err := io.ReadFull(conn, rest); err != nil {
			t.Fatalf("read SOCKS5 domain reply body: %v", err)
		}
	default:
		t.Fatalf("unsupported SOCKS5 reply address type %#x", header[3])
	}
	return header[1]
}

func assertSOCKS5Echo(t *testing.T, conn net.Conn, payload []byte) {
	t.Helper()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set SOCKS5 echo deadline: %v", err)
	}
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write SOCKS5 payload: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read SOCKS5 echoed payload: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("SOCKS5 echoed payload mismatch: got %q want %q", got, payload)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		t.Fatalf("clear SOCKS5 echo deadline: %v", err)
	}
}

func waitForUnifiedTunnelRuntimeState(t *testing.T, s *Server, token, tunnelID, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last tunnelSpecAPI
	for time.Now().Before(deadline) {
		resp := doMuxRequest(t, s.StartHTTPOnly(), http.MethodGet, "/api/tunnels/"+tunnelID, token, nil)
		if resp.Code != http.StatusOK {
			t.Fatalf("GET tunnel: want 200, got %d body=%s", resp.Code, resp.Body.String())
		}
		if err := mustDecodeJSON(t, resp.Body, &last); err != nil {
			t.Fatalf("decode tunnel: %v", err)
		}
		if last.RuntimeState == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("tunnel %s runtime_state: want %q, last=%q issues=%+v", tunnelID, want, last.RuntimeState, last.Issues)
}

func startTestTCPEchoService(t *testing.T) (string, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo service: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func startTestUDPEchoService(t *testing.T) (string, int) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen UDP echo service: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = conn.WriteTo(buf[:n], addr)
		}
	}()

	addr := conn.LocalAddr().(*net.UDPAddr)
	return addr.IP.String(), addr.Port
}

func newIPv4HTTPTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen test HTTP server: %v", err)
	}
	ts := httptest.NewUnstartedServer(handler)
	ts.Listener = ln
	ts.Start()
	return ts
}
