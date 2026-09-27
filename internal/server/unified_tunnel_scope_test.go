package server

import (
	"bytes"
	"net/http"
	"testing"
)

func TestAPIUnifiedTunnelSelfListIsIsolatedByOwner(t *testing.T) {
	s, handler, adminToken, cleanup := setupTestServerWithStores(t, true)
	defer cleanup()

	admin, err := s.auth.adminStore.ValidateAdminPassword("admin", "password123")
	if err != nil {
		t.Fatalf("load administrator: %v", err)
	}
	member, err := s.auth.adminStore.CreateUser("tunnel-self-list-member", "Password123")
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	memberToken := loginAdminTokenLocal(t, handler, member.Username, "Password123")

	adminClient := createUnifiedAPITestClientForUser(t, s, admin.ID, "tunnel-self-list-admin-install", "tunnel-self-list-admin")
	memberClient := createUnifiedAPITestClientForUser(t, s, member.ID, "tunnel-self-list-member-install", "tunnel-self-list-member")

	adminCreate := doMuxRequest(t, handler, http.MethodPost, "/api/tunnels", adminToken, unifiedCreatePayload("admin-self-tunnel", adminClient.ID, reserveTCPPort(t)))
	if adminCreate.Code != http.StatusCreated {
		t.Fatalf("create administrator tunnel: status=%d body=%s", adminCreate.Code, adminCreate.Body.String())
	}
	memberCreate := doMuxRequest(t, handler, http.MethodPost, "/api/tunnels", memberToken, unifiedCreatePayload("member-self-tunnel", memberClient.ID, reserveTCPPort(t)))
	if memberCreate.Code != http.StatusCreated {
		t.Fatalf("create member tunnel: status=%d body=%s", memberCreate.Code, memberCreate.Body.String())
	}

	assertNames := func(label string, responseCode int, body []byte, want string) {
		t.Helper()
		if responseCode != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", label, responseCode, string(body))
		}
		var tunnels []tunnelSpecAPI
		if err := mustDecodeJSON(t, bytes.NewReader(body), &tunnels); err != nil {
			t.Fatalf("%s: decode tunnels: %v", label, err)
		}
		if len(tunnels) != 1 || tunnels[0].Name != want {
			t.Fatalf("%s: tunnels=%+v, want only %q", label, tunnels, want)
		}
	}

	adminSelf := doMuxRequest(t, handler, http.MethodGet, "/api/tunnels", adminToken, nil)
	assertNames("administrator self list", adminSelf.Code, adminSelf.Body.Bytes(), "admin-self-tunnel")

	memberSelf := doMuxRequest(t, handler, http.MethodGet, "/api/tunnels", memberToken, nil)
	assertNames("member self list", memberSelf.Code, memberSelf.Body.Bytes(), "member-self-tunnel")

	adminTarget := doMuxRequest(t, handler, http.MethodGet, "/api/admin/users/"+member.ID+"/tunnels", adminToken, nil)
	assertNames("administrator target-user list", adminTarget.Code, adminTarget.Body.Bytes(), "member-self-tunnel")
}
