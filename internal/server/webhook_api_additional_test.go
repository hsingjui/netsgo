package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func serveWebhookRawAPIRequest(t *testing.T, handler http.Handler, method, path, token, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", "Go-http-client/1.1")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeWebhookAPIResponse[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode API response status=%d body=%s: %v", response.Code, response.Body.String(), err)
	}
	return value
}

func assertWebhookAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code, field string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("API error status=%d body=%s, want %d", response.Code, response.Body.String(), status)
	}
	payload := decodeWebhookAPIResponse[apiErrorResponse](t, response)
	if payload.Code != code || payload.Field != field || payload.Error == "" {
		t.Fatalf("API error = %+v, want code %q field %q", payload, code, field)
	}
}

func TestWebhookAPICompleteLifecycleIntegration(t *testing.T) {
	server, handler, cleanup := setupActivityAPIAuthTest(t)
	defer cleanup()
	owner, token := issueRoleToken(t, server, "webhook-api-lifecycle")

	catalogResponse := serveWebhookAPIRequest(t, handler, http.MethodGet, "/api/webhooks/catalog", token, nil)
	if catalogResponse.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", catalogResponse.Code, catalogResponse.Body.String())
	}
	catalog := decodeWebhookAPIResponse[WebhookCatalog](t, catalogResponse)
	if len(catalog.Events) != len(webhookEventTargetKinds) || len(catalog.Variables) == 0 || catalog.DefaultBody == "" {
		t.Fatalf("catalog = %+v", catalog)
	}

	input := testWebhookInput("")
	input.Name = "API lifecycle"
	input.Events = []string{"client.online"}
	input.URL = "https://preview.example/hook?event={{event.name.en-US}}&delivery={{delivery.id}}"
	previewInput := cloneWebhookInput(input)
	previewInput.ID = "wh_api_preview"
	previewResponse := serveWebhookAPIRequest(t, handler, http.MethodPost, "/api/webhooks/preview", token, webhookPreviewRequest{Config: previewInput, Event: "client.online"})
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	preview := decodeWebhookAPIResponse[WebhookPreview](t, previewResponse)
	if preview.Event != "client.online" || !strings.Contains(preview.URL, "event=Client%20online") || preview.Headers["X-NetsGo-Attempt"] != "1" || preview.Body == nil || !strings.Contains(*preview.Body, "客户端上线") || !strings.Contains(*preview.Body, "Client online") {
		t.Fatalf("preview = %+v", preview)
	}

	createResponse := serveWebhookAPIRequest(t, handler, http.MethodPost, "/api/webhooks", token, input)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}
	created := decodeWebhookAPIResponse[ActivityWebhook](t, createResponse)
	if !strings.HasPrefix(created.ID, "wh_") || created.Revision != 1 || created.Name != input.Name {
		t.Fatalf("created = %+v", created)
	}

	getResponse := serveWebhookAPIRequest(t, handler, http.MethodGet, "/api/webhooks/"+created.ID, token, nil)
	got := decodeWebhookAPIResponse[ActivityWebhook](t, getResponse)
	if getResponse.Code != http.StatusOK || got.ID != created.ID {
		t.Fatalf("get status=%d body=%s", getResponse.Code, getResponse.Body.String())
	}
	input.ID = created.ID
	input.ExpectedRevision = created.Revision + 1
	conflict := serveWebhookAPIRequest(t, handler, http.MethodPut, "/api/webhooks/"+created.ID, token, input)
	assertWebhookAPIError(t, conflict, http.StatusConflict, "webhook_revision_conflict", "")

	input.ExpectedRevision = created.Revision
	input.Name = "API lifecycle updated"
	updateResponse := serveWebhookAPIRequest(t, handler, http.MethodPut, "/api/webhooks/"+created.ID, token, input)
	if updateResponse.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updateResponse.Code, updateResponse.Body.String())
	}
	updated := decodeWebhookAPIResponse[ActivityWebhook](t, updateResponse)
	if updated.Revision != 2 || updated.Name != input.Name {
		t.Fatalf("updated = %+v", updated)
	}
	input.ExpectedRevision = updated.Revision

	client := registerWebhookClient(t, server.auth.adminStore, owner.ID, "webhook-api-client", "api-client")
	clock := time.Date(2026, 8, 26, 16, 0, 0, 0, time.UTC)
	appendWebhookClientEvent(t, server.auth.adminStore, owner, client.ID, "online", "webhook-api-event", clock)
	server.webhookStore.now = func() time.Time { return clock.Add(time.Minute) }
	testResponse := serveWebhookAPIRequest(t, handler, http.MethodPost, "/api/webhooks/test", token, webhookPreviewRequest{Config: input, Event: "client.online"})
	if testResponse.Code != http.StatusAccepted {
		t.Fatalf("test status=%d body=%s", testResponse.Code, testResponse.Body.String())
	}
	testDelivery := decodeWebhookAPIResponse[WebhookDelivery](t, testResponse)
	if testDelivery.Origin != WebhookOriginTest || testDelivery.Status != WebhookDeliveryQueued || testDelivery.WebhookID != created.ID {
		t.Fatalf("test delivery = %+v", testDelivery)
	}

	page1Response := serveWebhookAPIRequest(t, handler, http.MethodGet, "/api/webhooks/"+created.ID+"/deliveries?limit=1", token, nil)
	if page1Response.Code != http.StatusOK {
		t.Fatalf("delivery page 1 status=%d body=%s", page1Response.Code, page1Response.Body.String())
	}
	page1 := decodeWebhookAPIResponse[WebhookDeliveryPage](t, page1Response)
	if len(page1.Items) != 1 || page1.Items[0].ID != testDelivery.ID || !page1.HasMore || page1.NextCursor == "" {
		t.Fatalf("delivery page 1 = %+v", page1)
	}
	page2Response := serveWebhookAPIRequest(t, handler, http.MethodGet, "/api/webhooks/"+created.ID+"/deliveries?limit=1&cursor="+page1.NextCursor, token, nil)
	if page2Response.Code != http.StatusOK {
		t.Fatalf("delivery page 2 status=%d body=%s", page2Response.Code, page2Response.Body.String())
	}
	page2 := decodeWebhookAPIResponse[WebhookDeliveryPage](t, page2Response)
	if len(page2.Items) != 1 || page2.Items[0].Origin != WebhookOriginEvent || page2.HasMore {
		t.Fatalf("delivery page 2 = %+v", page2)
	}
	eventDelivery := page2.Items[0]

	queuedResponse := serveWebhookAPIRequest(t, handler, http.MethodGet, "/api/webhooks/"+created.ID+"/deliveries?status=queued&limit=100", token, nil)
	queued := decodeWebhookAPIResponse[WebhookDeliveryPage](t, queuedResponse)
	if queuedResponse.Code != http.StatusOK || len(queued.Items) != 2 {
		t.Fatalf("queued deliveries status=%d page=%+v", queuedResponse.Code, queued)
	}
	detailResponse := serveWebhookAPIRequest(t, handler, http.MethodGet, "/api/webhook-deliveries/"+eventDelivery.ID, token, nil)
	if detailResponse.Code != http.StatusOK || decodeWebhookAPIResponse[WebhookDelivery](t, detailResponse).EventID != eventDelivery.EventID {
		t.Fatalf("delivery detail status=%d body=%s", detailResponse.Code, detailResponse.Body.String())
	}

	server.webhookStore.now = func() time.Time { return clock.Add(2 * time.Minute) }
	replayResponse := serveWebhookAPIRequest(t, handler, http.MethodPost, "/api/webhook-deliveries/"+eventDelivery.ID+"/replay", token, nil)
	if replayResponse.Code != http.StatusAccepted {
		t.Fatalf("replay status=%d body=%s", replayResponse.Code, replayResponse.Body.String())
	}
	replay := decodeWebhookAPIResponse[WebhookDelivery](t, replayResponse)
	if replay.Origin != WebhookOriginReplay || replay.ID == eventDelivery.ID || replay.EventID != eventDelivery.EventID {
		t.Fatalf("replay = %+v", replay)
	}
	testReplay := serveWebhookAPIRequest(t, handler, http.MethodPost, "/api/webhook-deliveries/"+testDelivery.ID+"/replay", token, nil)
	assertWebhookAPIError(t, testReplay, http.StatusConflict, "webhook_replay_unavailable", "")

	deleteResponse := serveWebhookAPIRequest(t, handler, http.MethodDelete, "/api/webhooks/"+created.ID, token, nil)
	if deleteResponse.Code != http.StatusNoContent || deleteResponse.Body.Len() != 0 {
		t.Fatalf("delete status=%d body=%s", deleteResponse.Code, deleteResponse.Body.String())
	}
	missing := serveWebhookAPIRequest(t, handler, http.MethodGet, "/api/webhooks/"+created.ID, token, nil)
	assertWebhookAPIError(t, missing, http.StatusNotFound, "webhook_not_found", "")
	preserved := serveWebhookAPIRequest(t, handler, http.MethodGet, "/api/webhook-deliveries/"+eventDelivery.ID, token, nil)
	if preserved.Code != http.StatusOK {
		t.Fatalf("preserved delivery status=%d body=%s", preserved.Code, preserved.Body.String())
	}
}

func TestWebhookAPIValidationAndRequestPolicyErrors(t *testing.T) {
	server, handler, cleanup := setupActivityAPIAuthTest(t)
	defer cleanup()
	_, token := issueRoleToken(t, server, "webhook-api-errors")

	malformed := serveWebhookRawAPIRequest(t, handler, http.MethodPost, "/api/webhooks", token, "application/json", []byte(`{"name":`))
	assertWebhookAPIError(t, malformed, http.StatusBadRequest, "invalid_request_body", "")
	extraJSON := serveWebhookRawAPIRequest(t, handler, http.MethodPost, "/api/webhooks", token, "application/json", []byte(`{} {}`))
	assertWebhookAPIError(t, extraJSON, http.StatusBadRequest, "invalid_request_body", "")
	oversized := serveWebhookRawAPIRequest(t, handler, http.MethodPost, "/api/webhooks", token, "application/json", bytes.Repeat([]byte(" "), webhookJSONRequestLimitBytes+1))
	assertWebhookAPIError(t, oversized, http.StatusRequestEntityTooLarge, "request_body_too_large", "")

	invalid := testWebhookInput("wh_api_invalid")
	invalid.URL = "https://example.test/{{unknown.value}}"
	validation := serveWebhookAPIRequest(t, handler, http.MethodPost, "/api/webhooks", token, invalid)
	assertWebhookAPIError(t, validation, http.StatusUnprocessableEntity, "unknown_variable", "url")

	valid := testWebhookInput("wh_api_errors")
	create := serveWebhookAPIRequest(t, handler, http.MethodPost, "/api/webhooks", token, valid)
	if create.Code != http.StatusCreated {
		t.Fatalf("fixture create status=%d body=%s", create.Code, create.Body.String())
	}
	duplicate := serveWebhookAPIRequest(t, handler, http.MethodPost, "/api/webhooks", token, valid)
	assertWebhookAPIError(t, duplicate, http.StatusConflict, "webhook_revision_conflict", "")
	for _, test := range []struct {
		query, code string
		status      int
		field       string
	}{
		{query: "limit=0", status: http.StatusBadRequest, code: "invalid_delivery_limit"},
		{query: "limit=101", status: http.StatusBadRequest, code: "invalid_delivery_limit"},
		{query: "limit=abc", status: http.StatusBadRequest, code: "invalid_delivery_limit"},
		{query: "status=unknown", status: http.StatusBadRequest, code: "invalid_delivery_status"},
		{query: "cursor=not-base64", status: http.StatusUnprocessableEntity, code: "invalid_cursor", field: "cursor"},
	} {
		response := serveWebhookAPIRequest(t, handler, http.MethodGet, "/api/webhooks/"+valid.ID+"/deliveries?"+test.query, token, nil)
		assertWebhookAPIError(t, response, test.status, test.code, test.field)
	}
	notFound := serveWebhookAPIRequest(t, handler, http.MethodGet, "/api/webhook-deliveries/missing", token, nil)
	assertWebhookAPIError(t, notFound, http.StatusNotFound, "webhook_not_found", "")
}

func TestWebhookAPIPublishesStrictlyScopedSSEEvents(t *testing.T) {
	server, handler, cleanup := setupActivityAPIAuthTest(t)
	defer cleanup()
	owner, ownerToken := issueRoleToken(t, server, "webhook-sse-owner")
	_, otherToken := issueRoleToken(t, server, "webhook-sse-other")
	ownerStream, cancelOwner, ownerDone := startAuthenticatedSSE(t, handler, "/api/events", ownerToken)
	defer func() {
		cancelOwner()
		waitForSSEStop(t, ownerDone, "owner Webhook SSE did not stop")
	}()
	otherStream, cancelOther, otherDone := startAuthenticatedSSE(t, handler, "/api/events", otherToken)
	defer func() {
		cancelOther()
		waitForSSEStop(t, otherDone, "other Webhook SSE did not stop")
	}()

	input := testWebhookInput("wh_sse_scope")
	created := serveWebhookAPIRequest(t, handler, http.MethodPost, "/api/webhooks", ownerToken, input)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	tested := serveWebhookAPIRequest(t, handler, http.MethodPost, "/api/webhooks/test", ownerToken, webhookPreviewRequest{Config: input, Event: "client.online"})
	if tested.Code != http.StatusAccepted {
		t.Fatalf("test status=%d body=%s", tested.Code, tested.Body.String())
	}
	delivery := decodeWebhookAPIResponse[WebhookDelivery](t, tested)

	deadline := time.Now().Add(time.Second)
	for {
		body := ownerStream.BodyString()
		if strings.Contains(body, "event: webhook_changed") && strings.Contains(body, input.ID) && strings.Contains(body, "event: webhook_delivery_changed") && strings.Contains(body, delivery.ID) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("owner SSE missing Webhook events: %s", body)
		}
		time.Sleep(time.Millisecond)
	}
	otherBody := otherStream.BodyString()
	if strings.Contains(otherBody, input.ID) || strings.Contains(otherBody, delivery.ID) || strings.Contains(otherBody, "event: webhook_changed") || strings.Contains(otherBody, "event: webhook_delivery_changed") {
		t.Fatalf("other user received scoped Webhook SSE: %s", otherBody)
	}
	if delivery.WebhookID != input.ID || owner.ID == "" {
		t.Fatalf("SSE fixture = owner %q delivery %+v", owner.ID, delivery)
	}
}
