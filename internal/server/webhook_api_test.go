package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func serveWebhookAPIRequest(t *testing.T, handler http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload *bytes.Reader
	if body == nil {
		payload = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(raw)
	}
	request := httptest.NewRequest(method, path, payload)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", "Go-http-client/1.1")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestWebhookAPIsAreStrictlyCurrentUserScoped(t *testing.T) {
	server, handler, cleanup := setupActivityAPIAuthTest(t)
	defer cleanup()
	viewer, viewerToken := issueRoleToken(t, server, "webhook-viewer")
	_, otherToken := issueRoleToken(t, server, "webhook-other")
	_, adminToken := issueRoleToken(t, server, "admin")

	input := testWebhookInput("wh_viewer_only")
	createResponse := serveWebhookAPIRequest(t, handler, http.MethodPost, "/api/webhooks", viewerToken, input)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create Webhook status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}
	var created ActivityWebhook
	if err := json.Unmarshal(createResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	viewerList := serveWebhookAPIRequest(t, handler, http.MethodGet, "/api/webhooks", viewerToken, nil)
	otherList := serveWebhookAPIRequest(t, handler, http.MethodGet, "/api/webhooks", otherToken, nil)
	adminList := serveWebhookAPIRequest(t, handler, http.MethodGet, "/api/webhooks", adminToken, nil)
	for name, response := range map[string]*httptest.ResponseRecorder{
		"viewer": viewerList, "other": otherList, "admin-self": adminList,
	} {
		if response.Code != http.StatusOK {
			t.Fatalf("%s list status=%d body=%s", name, response.Code, response.Body.String())
		}
	}
	var viewerItems, otherItems, adminItems []ActivityWebhook
	if err := json.Unmarshal(viewerList.Body.Bytes(), &viewerItems); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(otherList.Body.Bytes(), &otherItems); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(adminList.Body.Bytes(), &adminItems); err != nil {
		t.Fatal(err)
	}
	if len(viewerItems) != 1 || viewerItems[0].ID != created.ID || len(otherItems) != 0 || len(adminItems) != 0 {
		t.Fatalf("scoped lists viewer=%+v other=%+v admin=%+v", viewerItems, otherItems, adminItems)
	}

	input.ExpectedRevision = created.Revision
	crossUpdate := serveWebhookAPIRequest(t, handler, http.MethodPut, "/api/webhooks/"+created.ID, otherToken, input)
	if crossUpdate.Code != http.StatusNotFound {
		t.Fatalf("cross-user update status=%d body=%s", crossUpdate.Code, crossUpdate.Body.String())
	}

	delivery, err := server.webhookStore.EnqueueTest(viewer.ID, input, "client.online")
	if err != nil {
		t.Fatal(err)
	}
	otherDelivery := serveWebhookAPIRequest(t, handler, http.MethodGet, "/api/webhook-deliveries/"+delivery.ID, otherToken, nil)
	if otherDelivery.Code != http.StatusNotFound {
		t.Fatalf("cross-user delivery status=%d body=%s", otherDelivery.Code, otherDelivery.Body.String())
	}
	viewerDelivery := serveWebhookAPIRequest(t, handler, http.MethodGet, "/api/webhook-deliveries/"+delivery.ID, viewerToken, nil)
	if viewerDelivery.Code != http.StatusOK {
		t.Fatalf("owner delivery status=%d body=%s", viewerDelivery.Code, viewerDelivery.Body.String())
	}
}
