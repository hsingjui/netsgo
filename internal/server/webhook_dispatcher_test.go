package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"netsgo/pkg/protocol"
)

func appendWebhookClientActivity(t *testing.T, store *AdminStore, user User, dedupeKey string, occurredAt time.Time) int64 {
	t.Helper()
	client, err := store.GetOrCreateClientForUser(user.ID, "webhook-dispatch-client", protocol.ClientInfo{
		Hostname: "dispatch-edge", OS: "linux", Arch: "amd64", Version: "0.1.0",
	}, "127.0.0.1:12345")
	if err != nil {
		t.Fatalf("create dispatch client: %v", err)
	}
	activityStore := newActivityStoreWithDB("", store.db, false)
	activityStore.now = func() time.Time { return occurredAt }
	id, err := activityStore.Append(ActivityEventSpec{
		OccurredAt: occurredAt, Category: ActivityCategoryClient, Action: "online", Source: "test",
		ScopeUserID: user.ID, SubjectUserID: user.ID, DedupeKey: dedupeKey,
		Actor:   ActivityActor{Type: "client", ID: client.ID},
		Payload: newActivityClientLifecyclePayload("online", "", 1, true, ActivitySummaryArgs{ClientName: "dispatch-edge"}),
		Clients: []ActivityClientSubject{{ClientID: client.ID, Relation: "subject"}},
	})
	if err != nil {
		t.Fatalf("append dispatch activity: %v", err)
	}
	return id
}

func TestWebhookDispatcherRetriesAtPersistedUserSlot(t *testing.T) {
	adminStore, webhookStore, user := newWebhookStoreFixture(t)
	clock := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	webhookStore.now = func() time.Time { return clock }

	var mu sync.Mutex
	attemptHeaders := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attemptHeaders = append(attemptHeaders, r.Header.Get("X-NetsGo-Attempt"))
		attempt := len(attemptHeaders)
		mu.Unlock()
		if attempt == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "retry", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	input := testWebhookInput("wh_retry")
	input.URL = server.URL
	created, err := webhookStore.Create(user.ID, input)
	if err != nil {
		t.Fatalf("create retry Webhook: %v", err)
	}
	appendWebhookClientActivity(t, adminStore, user, "webhook-dispatch-retry", clock)
	page, err := webhookStore.ListDeliveries(user.ID, created.ID, "", 10, "")
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("queued retry delivery = %+v, %v", page, err)
	}
	deliveryID := page.Items[0].ID

	dispatcher := newWebhookDispatcher(webhookStore, nil)
	defer dispatcher.cancel()
	dispatcher.now = func() time.Time { return clock }
	claimed, ok, err := webhookStore.ClaimDue(user.ID, clock)
	if err != nil || !ok {
		t.Fatalf("claim first attempt = %v, %v", ok, err)
	}
	dispatcher.execute(claimed)
	afterFirst, err := webhookStore.GetDelivery(user.ID, deliveryID)
	if err != nil || afterFirst.Status != WebhookDeliveryRetrying || len(afterFirst.Attempts) != 1 {
		t.Fatalf("after first attempt = %+v, %v", afterFirst, err)
	}
	if _, ok, err := webhookStore.ClaimDue(user.ID, clock.Add(time.Second)); err != nil || ok {
		t.Fatalf("claim before persisted slot = %v, %v", ok, err)
	}

	clock = clock.Add(2 * time.Second)
	claimed, ok, err = webhookStore.ClaimDue(user.ID, clock)
	if err != nil || !ok {
		t.Fatalf("claim second attempt = %v, %v", ok, err)
	}
	dispatcher.execute(claimed)
	final, err := webhookStore.GetDelivery(user.ID, deliveryID)
	if err != nil || final.Status != WebhookDeliverySuccess || len(final.Attempts) != 2 {
		t.Fatalf("final retry delivery = %+v, %v", final, err)
	}
	firstStarted, _ := time.Parse(time.RFC3339Nano, final.Attempts[0].OccurredAt)
	secondStarted, _ := time.Parse(time.RFC3339Nano, final.Attempts[1].OccurredAt)
	if gap := secondStarted.Sub(firstStarted); gap < webhookUserStartInterval {
		t.Fatalf("attempt start gap = %v, want at least %v", gap, webhookUserStartInterval)
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(attemptHeaders) != "[1 2]" {
		t.Fatalf("attempt headers = %v", attemptHeaders)
	}
}

func TestWebhookDispatcherDoesNotFollowRedirects(t *testing.T) {
	_, webhookStore, user := newWebhookStoreFixture(t)
	var targetCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			targetCalls++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, "/target", http.StatusFound)
	}))
	defer server.Close()
	input := testWebhookInput("wh_redirect")
	input.URL = server.URL + "/redirect"
	delivery, err := webhookStore.EnqueueTest(user.ID, input, "client.online")
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := webhookStore.ClaimDue(user.ID, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("claim redirect test = %v, %v", ok, err)
	}
	dispatcher := newWebhookDispatcher(webhookStore, nil)
	defer dispatcher.cancel()
	dispatcher.execute(claimed)
	result, err := webhookStore.GetDelivery(user.ID, delivery.ID)
	if err != nil || result.Status != WebhookDeliveryFailed || result.StatusCode == nil || *result.StatusCode != http.StatusFound {
		t.Fatalf("redirect delivery = %+v, %v", result, err)
	}
	if targetCalls != 0 {
		t.Fatalf("redirect target calls = %d, want 0", targetCalls)
	}
}

func TestWebhookDispatcherTruncatesSuccessfulResponseWithoutError(t *testing.T) {
	_, webhookStore, user := newWebhookStoreFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", webhookResponseBodyMaxBytes+128)))
	}))
	defer server.Close()
	input := testWebhookInput("wh_truncated")
	input.URL = server.URL
	delivery, err := webhookStore.EnqueueTest(user.ID, input, "client.online")
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := webhookStore.ClaimDue(user.ID, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("claim truncation test = %v, %v", ok, err)
	}
	dispatcher := newWebhookDispatcher(webhookStore, nil)
	defer dispatcher.cancel()
	dispatcher.execute(claimed)
	result, err := webhookStore.GetDelivery(user.ID, delivery.ID)
	if err != nil || result.Status != WebhookDeliverySuccess {
		t.Fatalf("truncated delivery = %+v, %v", result, err)
	}
	if len(result.ResponseBody) != webhookResponseBodyMaxBytes || result.Error != "" {
		t.Fatalf("truncated response length=%d error=%q", len(result.ResponseBody), result.Error)
	}
}

func TestParseWebhookRetryAfterAcceptsZeroAndCapsDelay(t *testing.T) {
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	if delay, ok := parseWebhookRetryAfter("0", now); !ok || delay != 0 {
		t.Fatalf("Retry-After zero = %v, %v", delay, ok)
	}
	if delay, ok := parseWebhookRetryAfter("3600", now); !ok || delay != webhookRetryAfterMax {
		t.Fatalf("Retry-After cap = %v, %v", delay, ok)
	}
	if _, ok := parseWebhookRetryAfter("invalid", now); ok {
		t.Fatal("invalid Retry-After was accepted")
	}
}
