package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"netsgo/pkg/protocol"
)

func appendScopedActivityForTest(t *testing.T, store *ActivityStore, userID, action string) int64 {
	t.Helper()
	spec := testActivitySpec(action, time.Now().UTC())
	spec.ScopeUserID = userID
	spec.SubjectUserID = userID
	id, err := store.Append(spec)
	if err != nil {
		t.Fatalf("append scoped activity: %v", err)
	}
	return id
}

func TestActivityStorePersistsAndFiltersUserScope(t *testing.T) {
	s, _, cleanup := setupActivityAPIAuthTest(t)
	defer cleanup()
	alice, _ := issueRoleToken(t, s, "alice")
	bob, _ := issueRoleToken(t, s, "bob")

	client, err := s.auth.adminStore.GetOrCreateClientForUser(alice.ID, "activity-scope-client", protocol.ClientInfo{Hostname: "alice-client"}, "192.0.2.8:1234")
	if err != nil {
		t.Fatal(err)
	}
	inferred := testActivitySpec("created", time.Now().UTC())
	inferred.Clients = []ActivityClientSubject{{ClientID: client.ID, Relation: "subject", Hostname: client.Info.Hostname}}
	inferred.Tunnels = nil
	inferredID, err := s.activityStore.Append(inferred)
	if err != nil {
		t.Fatal(err)
	}
	bobID := appendScopedActivityForTest(t, s.activityStore, bob.ID, "updated")
	if _, err := s.activityStore.Append(testActivitySpec("stopped", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}

	item, err := s.activityStore.GetByID(inferredID)
	if err != nil {
		t.Fatal(err)
	}
	if item.ScopeUserID != alice.ID || item.SubjectUserID != alice.ID {
		t.Fatalf("inferred activity scope = %q/%q, want %q/%q", item.ScopeUserID, item.SubjectUserID, alice.ID, alice.ID)
	}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), alice.ID) {
		t.Fatalf("activity JSON exposed server scope metadata: %s", raw)
	}

	page, err := s.activityStore.Query(ActivityQuery{Scope: ActivityScopeGlobal, ScopeUserID: alice.ID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := activityIDs(page.Items); !reflect.DeepEqual(got, []int64{inferredID}) {
		t.Fatalf("alice-scoped activity IDs = %v, want [%d]", got, inferredID)
	}
	maxID, err := s.activityStore.MaxIDForUser(alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if maxID != inferredID {
		t.Fatalf("alice activity cursor = %d, want %d", maxID, inferredID)
	}
	bobMaxID, err := s.activityStore.MaxIDForUser(bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bobMaxID != bobID {
		t.Fatalf("bob activity cursor = %d, want %d", bobMaxID, bobID)
	}
}

func TestActivityAPIEnforcesSelfTargetAndGlobalScopes(t *testing.T) {
	s, handler, cleanup := setupActivityAPIAuthTest(t)
	defer cleanup()
	alice, aliceToken := issueRoleToken(t, s, "alice")
	bob, bobToken := issueRoleToken(t, s, "bob")
	_, adminToken := issueRoleToken(t, s, "admin")
	aliceID := appendScopedActivityForTest(t, s.activityStore, alice.ID, "created")
	bobID := appendScopedActivityForTest(t, s.activityStore, bob.ID, "updated")

	requestPage := func(path, token string) ActivityPage {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("User-Agent", "Go-http-client/1.1")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", path, resp.Code, resp.Body.String())
		}
		var page ActivityPage
		if err := mustDecodeJSON(t, resp.Body, &page); err != nil {
			t.Fatal(err)
		}
		return page
	}

	self := requestPage("/api/activity?user_id="+alice.ID, bobToken)
	if got := activityIDs(self.Items); !reflect.DeepEqual(got, []int64{bobID}) {
		t.Fatalf("self activity IDs = %v, want [%d]", got, bobID)
	}
	target := requestPage("/api/admin/users/"+alice.ID+"/activity?user_id="+bob.ID, adminToken)
	if got := activityIDs(target.Items); !reflect.DeepEqual(got, []int64{aliceID}) {
		t.Fatalf("target activity IDs = %v, want [%d]", got, aliceID)
	}
	globalFiltered := requestPage("/api/admin/activity?user_id="+alice.ID, adminToken)
	if got := activityIDs(globalFiltered.Items); !reflect.DeepEqual(got, []int64{aliceID}) {
		t.Fatalf("global filtered activity IDs = %v, want [%d]", got, aliceID)
	}

	// The selected user's own route remains valid even when the caller is an
	// administrator; it is still self-scoped instead of becoming global.
	adminSelf := requestPage("/api/activity", aliceToken)
	if got := activityIDs(adminSelf.Items); !reflect.DeepEqual(got, []int64{aliceID}) {
		t.Fatalf("alice activity IDs = %v, want [%d]", got, aliceID)
	}
}
