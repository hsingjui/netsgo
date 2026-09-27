package server

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"netsgo/pkg/protocol"
)

func testWebhookInput(id string) WebhookConfigInput {
	return WebhookConfigInput{
		ID: id, Name: "Client presence", Enabled: true,
		TargetKind: WebhookTargetClient, TargetMode: WebhookTargetAll,
		Method: WebhookMethodPOST, URL: "http://127.0.0.1/hook",
		Headers: []WebhookHeader{{ID: "content", Key: "Content-Type", Value: "application/json"}},
		Body:    defaultActivityWebhookBody, Events: []string{"client.online", "client.offline"},
	}
}

func newWebhookStoreFixture(t *testing.T) (*AdminStore, *WebhookStore, User) {
	t.Helper()
	adminStore := newInitializedAdminStore(t)
	user, err := adminStore.GetSingleAdminUser()
	if err != nil {
		t.Fatalf("load initialized user: %v", err)
	}
	webhookStore := newWebhookStoreWithDB(adminStore.db)
	webhookStore.settings = func() WebhookSettings {
		return WebhookSettings{AllowPrivateTargets: true, DailyDeliveryCap: defaultWebhookDailyDeliveryCap}
	}
	return adminStore, webhookStore, user
}

func TestActivityAppendCreatesOneDurableWebhookDelivery(t *testing.T) {
	adminStore, webhookStore, user := newWebhookStoreFixture(t)
	client, err := adminStore.GetOrCreateClientForUser(user.ID, "webhook-client-install", protocol.ClientInfo{
		Hostname: "edge-01", OS: "linux", Arch: "amd64", Version: "0.1.0",
	}, "127.0.0.1:12345")
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	created, err := webhookStore.Create(user.ID, testWebhookInput("wh_client_presence"))
	if err != nil {
		t.Fatalf("create Webhook: %v", err)
	}
	if created.Revision != 1 || len(created.Events) != 2 || created.Calls24h != 0 {
		t.Fatalf("created Webhook = %+v", created)
	}

	activityStore := newActivityStoreWithDB("", adminStore.db, false)
	spec := ActivityEventSpec{
		OccurredAt: time.Now().UTC(), Category: ActivityCategoryClient, Action: "online", Source: "test",
		ScopeUserID: user.ID, SubjectUserID: user.ID, DedupeKey: "webhook-client-online",
		Actor:   ActivityActor{Type: "client", ID: client.ID},
		Payload: newActivityClientLifecyclePayload("online", "", 1, true, ActivitySummaryArgs{ClientName: "edge-01"}),
		Clients: []ActivityClientSubject{{ClientID: client.ID, Relation: "subject"}},
	}
	eventID, err := activityStore.Append(spec)
	if err != nil {
		t.Fatalf("append activity: %v", err)
	}
	if duplicateID, err := activityStore.Append(spec); err != nil || duplicateID != eventID {
		t.Fatalf("deduplicated append = %d, %v, want %d", duplicateID, err, eventID)
	}
	page, err := webhookStore.ListDeliveries(user.ID, created.ID, "", 50, "")
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("delivery count = %d, want 1", len(page.Items))
	}
	delivery := page.Items[0]
	if delivery.Event != "client.online" || delivery.EventID != fmt.Sprint(eventID) || delivery.Status != WebhookDeliveryQueued {
		t.Fatalf("delivery = %+v", delivery)
	}
	if delivery.RequestHeaders["X-NetsGo-Delivery"] != delivery.ID || delivery.RequestHeaders["X-NetsGo-Event"] != delivery.EventID {
		t.Fatalf("system headers = %#v", delivery.RequestHeaders)
	}
}

func TestActivityWebhookDeliveriesRenderEachLanguageVariable(t *testing.T) {
	adminStore, webhookStore, user := newWebhookStoreFixture(t)
	for _, fixture := range []struct {
		id   string
		body string
		want string
	}{
		{id: "wh_language_en", body: `{"message":"{{event.name.en-US}}: {{client.name}}"}`, want: "Client online: Real client node"},
		{id: "wh_language_zh", body: `{"message":"{{event.name.zh-CN}}：{{client.name}}"}`, want: "客户端上线：Real client node"},
	} {
		input := testWebhookInput(fixture.id)
		input.Events = []string{"client.online"}
		input.Body = fixture.body
		if _, err := webhookStore.Create(user.ID, input); err != nil {
			t.Fatalf("create %s Webhook: %v", fixture.id, err)
		}
	}

	activityStore := newActivityStoreWithDB("", adminStore.db, false)
	_, err := activityStore.Append(ActivityEventSpec{
		OccurredAt: time.Now().UTC(), Category: ActivityCategoryClient, Action: "online", Source: "test",
		ScopeUserID: user.ID, SubjectUserID: user.ID, DedupeKey: "webhook-localized-real-event",
		Actor:   ActivityActor{Type: "client", ID: "client-real"},
		Payload: newActivityClientLifecyclePayload("online", "", 1, true, ActivitySummaryArgs{ClientName: "Real client node"}),
		Clients: []ActivityClientSubject{{ClientID: "client-real", Relation: "subject", DisplayName: "Real client node"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, fixture := range []struct {
		id   string
		want string
	}{
		{id: "wh_language_en", want: "Client online: Real client node"},
		{id: "wh_language_zh", want: "客户端上线：Real client node"},
	} {
		page, err := webhookStore.ListDeliveries(user.ID, fixture.id, "", 10, "")
		if err != nil || len(page.Items) != 1 || page.Items[0].RequestBody == nil {
			t.Fatalf("localized deliveries for %s = %+v, %v", fixture.id, page, err)
		}
		if !strings.Contains(*page.Items[0].RequestBody, fixture.want) {
			t.Fatalf("localized body for %s = %s, want %q", fixture.id, *page.Items[0].RequestBody, fixture.want)
		}
	}
}

func TestTunnelWebhookRendersCurrentOwnerClientName(t *testing.T) {
	adminStore, webhookStore, user := newWebhookStoreFixture(t)
	client, err := adminStore.GetOrCreateClientForUser(user.ID, "webhook-tunnel-owner-install", protocol.ClientInfo{
		Hostname: "owner-host", OS: "linux", Arch: "amd64", Version: "0.1.0",
	}, "127.0.0.1:12345")
	if err != nil {
		t.Fatalf("create tunnel owner client: %v", err)
	}
	if _, err := adminStore.UpdateClientDisplayNameWithActivity(client.ID, "Renamed owner", systemActivityActor()); err != nil {
		t.Fatalf("rename tunnel owner client: %v", err)
	}

	input := testWebhookInput("wh_tunnel_owner_client")
	input.TargetKind = WebhookTargetTunnel
	input.Events = []string{"tunnel.runtime_error"}
	input.Body = `{"client_id":"{{client.id}}","client_name":"{{client.name}}"}`
	created, err := webhookStore.Create(user.ID, input)
	if err != nil {
		t.Fatalf("create tunnel Webhook: %v", err)
	}

	before := testStoredServerExposeTCPTunnel("tunnel-webhook-owner", "Owner tunnel", client.ID, 8080, 18080, time.Now().UTC())
	before.OwnerUserID = user.ID
	after := before
	after.RuntimeState = protocol.ProxyRuntimeStateError
	after.Error = "test runtime failure"
	spec, changed := tunnelRuntimeActivitySpec(before, after)
	if !changed {
		t.Fatal("runtime error should produce a tunnel activity")
	}
	activityStore := newActivityStoreWithDB("", adminStore.db, false)
	if _, err := activityStore.Append(spec); err != nil {
		t.Fatalf("append tunnel runtime activity: %v", err)
	}

	page, err := webhookStore.ListDeliveries(user.ID, created.ID, "", 10, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].RequestBody == nil {
		t.Fatalf("tunnel owner delivery = %+v, %v", page, err)
	}
	body := *page.Items[0].RequestBody
	if !strings.Contains(body, `"client_id": "`+client.ID+`"`) || !strings.Contains(body, `"client_name": "Renamed owner"`) {
		t.Fatalf("tunnel owner Webhook body = %s", body)
	}
}

func TestActivityAndWebhookDeliveryRollbackTogether(t *testing.T) {
	adminStore, webhookStore, user := newWebhookStoreFixture(t)
	if _, err := webhookStore.Create(user.ID, testWebhookInput("wh_atomic_rollback")); err != nil {
		t.Fatalf("create Webhook: %v", err)
	}
	if _, err := adminStore.db.Exec(`CREATE TRIGGER reject_webhook_delivery
		BEFORE INSERT ON activity_webhook_deliveries
		BEGIN SELECT RAISE(ABORT, 'forced Webhook delivery failure'); END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	activityStore := newActivityStoreWithDB("", adminStore.db, false)
	_, err := activityStore.Append(ActivityEventSpec{
		OccurredAt: time.Now().UTC(), Category: ActivityCategoryClient, Action: "online", Source: "test",
		ScopeUserID: user.ID, SubjectUserID: user.ID, DedupeKey: "webhook-atomic-rollback",
		Actor:   ActivityActor{Type: "client", ID: "client-atomic"},
		Payload: newActivityClientLifecyclePayload("online", "", 1, true, ActivitySummaryArgs{ClientName: "atomic"}),
		Clients: []ActivityClientSubject{{ClientID: "client-atomic", Relation: "subject"}},
	})
	if err == nil || !strings.Contains(err.Error(), "forced Webhook delivery failure") {
		t.Fatalf("append activity error = %v, want forced delivery failure", err)
	}
	var eventCount, deliveryCount int
	if err := adminStore.db.QueryRow(`SELECT COUNT(*) FROM activity_events WHERE dedupe_key = 'webhook-atomic-rollback'`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := adminStore.db.QueryRow(`SELECT COUNT(*) FROM activity_webhook_deliveries WHERE webhook_id = 'wh_atomic_rollback'`).Scan(&deliveryCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 0 || deliveryCount != 0 {
		t.Fatalf("rolled back rows = events %d deliveries %d", eventCount, deliveryCount)
	}
}

func TestWebhookCatalogCoversSupportedActivityEvents(t *testing.T) {
	catalog := activityWebhookCatalog()
	if len(catalog.Events) != len(webhookEventTargetKinds) {
		t.Fatalf("catalog events=%d supported=%d", len(catalog.Events), len(webhookEventTargetKinds))
	}
	seen := map[string]bool{}
	for _, event := range catalog.Events {
		seen[event.Key] = true
		parts := strings.SplitN(event.Key, ".", 2)
		if len(parts) != 2 {
			t.Fatalf("invalid event key %q", event.Key)
		}
		if _, ok := activityCatalog[ActivityCategory(parts[0])][parts[1]]; !ok {
			t.Fatalf("event %q has no activity catalog entry", event.Key)
		}
		if webhookEventTargetKinds[event.Key] != event.TargetKind {
			t.Fatalf("event %q target kind=%q want=%q", event.Key, event.TargetKind, webhookEventTargetKinds[event.Key])
		}
		if catalog.Fixtures[event.Key] == nil {
			t.Fatalf("event %q has no fixture", event.Key)
		}
		snapshot := sampleWebhookEvent(event.Key)
		matched := matchedWebhookTargetIDs(event.TargetKind, WebhookTargetAll, nil, snapshot)
		if len(matched) == 0 {
			t.Fatalf("event %q has no matchable subjects", event.Key)
		}
		selected := matchedWebhookTargetIDs(event.TargetKind, WebhookTargetSelected, []string{matched[0]}, snapshot)
		if len(selected) != 1 || selected[0] != matched[0] {
			t.Fatalf("event %q selected match=%v want=%q", event.Key, selected, matched[0])
		}
	}
	for event := range webhookEventTargetKinds {
		if !seen[event] {
			t.Fatalf("supported event %q is missing from catalog", event)
		}
	}
}

func TestP2PActivityWithMultipleSubjectsCreatesOneDelivery(t *testing.T) {
	adminStore, webhookStore, user := newWebhookStoreFixture(t)
	input := testWebhookInput("wh_p2p_multi")
	input.TargetKind = WebhookTargetTunnel
	input.Events = []string{"p2p.connected"}
	created, err := webhookStore.Create(user.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	activityStore := newActivityStoreWithDB("", adminStore.db, false)
	_, err = activityStore.Append(ActivityEventSpec{
		OccurredAt: time.Now().UTC(), Category: ActivityCategoryP2P, Action: "connected", Source: "test",
		ScopeUserID: user.ID, SubjectUserID: user.ID,
		Actor:   systemActivityActor(),
		Payload: newActivityP2PPayload("connected", "", "session-webhook", 1, ActivitySummaryArgs{Count: 2}),
		Clients: []ActivityClientSubject{
			{ClientID: "client-a", Relation: "peer"},
			{ClientID: "client-b", Relation: "peer"},
		},
		Tunnels: []ActivityTunnelSubject{
			{TunnelID: "tunnel-a", Relation: "subject"},
			{TunnelID: "tunnel-b", Relation: "subject"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := webhookStore.ListDeliveries(user.ID, created.ID, "", 10, "")
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("P2P deliveries = %+v, %v", page, err)
	}
	stored, err := webhookStore.getStoredDelivery(user.ID, page.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(stored.EventSnapshot.MatchedTargetIDs) != "[tunnel-a tunnel-b]" {
		t.Fatalf("matched targets = %v", stored.EventSnapshot.MatchedTargetIDs)
	}
}

func TestWebhookUserStartGateSpacesEveryOrigin(t *testing.T) {
	adminStore, webhookStore, user := newWebhookStoreFixture(t)
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	webhookStore.now = func() time.Time { return now }
	first, err := webhookStore.EnqueueTest(user.ID, testWebhookInput("wh_test_a"), "client.online")
	if err != nil {
		t.Fatalf("enqueue first test: %v", err)
	}
	second, err := webhookStore.EnqueueTest(user.ID, testWebhookInput("wh_test_b"), "client.online")
	if err != nil {
		t.Fatalf("enqueue second test: %v", err)
	}
	claimed, ok, err := webhookStore.ClaimDue(user.ID, now)
	if err != nil || !ok || (claimed.ID != first.ID && claimed.ID != second.ID) {
		t.Fatalf("claim first = (%s, %v, %v), want one queued delivery", claimed.ID, ok, err)
	}
	if _, ok, err := webhookStore.ClaimDue(user.ID, now.Add(time.Second)); err != nil || ok {
		t.Fatalf("claim inside two-second gate = (%v, %v), want false", ok, err)
	}
	if _, ok, err := webhookStore.ClaimDue(user.ID, now.Add(2*time.Second)); err != nil || !ok {
		t.Fatalf("claim at two-second gate = (%v, %v), want true", ok, err)
	}

	other, err := adminStore.CreateUser("webhook-other", "Other1234")
	if err != nil {
		t.Fatalf("create second user: %v", err)
	}
	if _, err := webhookStore.EnqueueTest(other.ID, testWebhookInput("wh_other"), "client.online"); err != nil {
		t.Fatalf("enqueue second-user test: %v", err)
	}
	if _, ok, err := webhookStore.ClaimDue(other.ID, now); err != nil || !ok {
		t.Fatalf("second user should have an independent start gate: (%v, %v)", ok, err)
	}
}

func TestWebhookExpiredLeaseIsRecoveredBeforeAnotherAttempt(t *testing.T) {
	adminStore, webhookStore, user := newWebhookStoreFixture(t)
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	webhookStore.now = func() time.Time { return now }
	delivery, err := webhookStore.EnqueueTest(user.ID, testWebhookInput("wh_recovery"), "client.online")
	if err != nil {
		t.Fatalf("enqueue test: %v", err)
	}
	if _, err := adminStore.db.Exec(`UPDATE activity_webhook_deliveries SET max_attempts = 3 WHERE id = ?`, delivery.ID); err != nil {
		t.Fatalf("make recovery fixture retryable: %v", err)
	}
	claimed, ok, err := webhookStore.ClaimDue(user.ID, now)
	if err != nil || !ok || claimed.ID != delivery.ID {
		t.Fatalf("claim delivery = (%s, %v, %v)", claimed.ID, ok, err)
	}

	leaseExpiredAt := now.Add(webhookDeliveryLease + time.Second)
	if _, ok, err := webhookStore.ClaimDue(user.ID, leaseExpiredAt); err != nil || ok {
		t.Fatalf("expired leased delivery must wait for recovery = (%v, %v)", ok, err)
	}
	if err := webhookStore.RecoverInterrupted(leaseExpiredAt); err != nil {
		t.Fatalf("recover interrupted delivery: %v", err)
	}
	recovered, err := webhookStore.getStoredDelivery(user.ID, delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != WebhookDeliveryRetrying || recovered.LeaseUntilNS.Valid {
		t.Fatalf("recovered delivery = status %q lease %+v", recovered.Status, recovered.LeaseUntilNS)
	}
	if err := webhookStore.loadDeliveryAttempts(&recovered); err != nil {
		t.Fatal(err)
	}
	if len(recovered.Attempts) != 1 || recovered.Attempts[0].Status != "failed" {
		t.Fatalf("recovered attempts = %+v", recovered.Attempts)
	}
	second, ok, err := webhookStore.ClaimDue(user.ID, leaseExpiredAt)
	if err != nil || !ok || second.AttemptCount != 2 {
		t.Fatalf("claim recovered delivery = (attempt %d, %v, %v)", second.AttemptCount, ok, err)
	}
}

func TestWebhookPruneRequiresAgeAndCountBounds(t *testing.T) {
	adminStore, webhookStore, user := newWebhookStoreFixture(t)
	now := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	config := testWebhookInput("wh_retention")
	if _, err := webhookStore.Create(user.ID, config); err != nil {
		t.Fatalf("create Webhook: %v", err)
	}
	insertTerminal := func(id string, completedAt time.Time) {
		t.Helper()
		_, err := adminStore.db.Exec(`INSERT INTO activity_webhook_deliveries (
			id, owner_user_id, webhook_id, webhook_name, origin, event_type, event_occurred_at_ns,
			status, attempt_count, max_attempts, next_attempt_at_ns, config_revision,
			config_snapshot_json, event_snapshot_json, values_snapshot_json,
			request_method, request_url, request_headers_json, response_headers_json,
			created_at_ns, completed_at_ns, updated_at_ns
		) VALUES (?, ?, ?, 'Retention', 'event', 'client.online', ?, 'success', 1, 3, ?, 1,
			'{}', '{}', '{}', 'GET', 'http://127.0.0.1', '{}', '{}', ?, ?, ?)`,
			id, user.ID, config.ID, completedAt.UnixNano(), completedAt.UnixNano(), completedAt.UnixNano(), completedAt.UnixNano(), completedAt.UnixNano())
		if err != nil {
			t.Fatalf("insert terminal delivery %s: %v", id, err)
		}
	}
	for index := 0; index < webhookDeliveryHistoryLimit+1; index++ {
		insertTerminal("recent-"+formatRetentionIndex(index), now.Add(-time.Duration(index)*time.Second))
	}
	insertTerminal("expired", now.Add(-31*24*time.Hour))
	if _, err := adminStore.db.Exec(`INSERT INTO activity_webhook_delivery_attempts
		(delivery_id, attempt_number, status, started_at_ns, completed_at_ns) VALUES ('recent-1000', 1, 'success', ?, ?)`, now.UnixNano(), now.UnixNano()); err != nil {
		t.Fatalf("insert attempt for count-pruned delivery: %v", err)
	}
	oldQueued := now.Add(-60 * 24 * time.Hour)
	_, err := adminStore.db.Exec(`INSERT INTO activity_webhook_deliveries (
		id, owner_user_id, webhook_id, webhook_name, origin, event_type, event_occurred_at_ns,
		status, max_attempts, next_attempt_at_ns, config_revision, config_snapshot_json,
		event_snapshot_json, values_snapshot_json, request_method, request_url,
		request_headers_json, response_headers_json, created_at_ns, updated_at_ns
	) VALUES ('queued-old', ?, ?, 'Retention', 'event', 'client.online', ?, 'queued', 3, ?, 1,
		'{}', '{}', '{}', 'GET', 'http://127.0.0.1', '{}', '{}', ?, ?)`, user.ID, config.ID,
		oldQueued.UnixNano(), oldQueued.UnixNano(), oldQueued.UnixNano(), oldQueued.UnixNano())
	if err != nil {
		t.Fatalf("insert old queued delivery: %v", err)
	}
	deleted, err := webhookStore.Prune(now)
	if err != nil {
		t.Fatalf("prune deliveries: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want expired plus count overflow", deleted)
	}
	var terminalCount, queuedCount, attemptCount int
	if err := adminStore.db.QueryRow(`SELECT COUNT(*) FROM activity_webhook_deliveries WHERE webhook_id = ? AND status IN ('success','failed','canceled')`, config.ID).Scan(&terminalCount); err != nil {
		t.Fatal(err)
	}
	if err := adminStore.db.QueryRow(`SELECT COUNT(*) FROM activity_webhook_deliveries WHERE id = 'queued-old'`).Scan(&queuedCount); err != nil {
		t.Fatal(err)
	}
	if err := adminStore.db.QueryRow(`SELECT COUNT(*) FROM activity_webhook_delivery_attempts WHERE delivery_id = 'recent-1000'`).Scan(&attemptCount); err != nil {
		t.Fatal(err)
	}
	if terminalCount != webhookDeliveryHistoryLimit || queuedCount != 1 || attemptCount != 0 {
		t.Fatalf("after prune terminal=%d queued=%d attempt=%d", terminalCount, queuedCount, attemptCount)
	}
}

func TestWebhookDisableCancelsWaitingAndPreventsInFlightRetry(t *testing.T) {
	adminStore, webhookStore, user := newWebhookStoreFixture(t)
	clock := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	webhookStore.now = func() time.Time { return clock }
	input := testWebhookInput("wh_disable")
	created, err := webhookStore.Create(user.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	appendWebhookClientActivity(t, adminStore, user, "webhook-disable-in-flight", clock)
	clock = clock.Add(time.Millisecond)
	appendWebhookClientActivity(t, adminStore, user, "webhook-disable-waiting", clock)
	claimed, ok, err := webhookStore.ClaimDue(user.ID, clock)
	if err != nil || !ok {
		t.Fatalf("claim in-flight delivery = %v, %v", ok, err)
	}

	input.ExpectedRevision = created.Revision
	input.Enabled = false
	if _, err := webhookStore.Update(user.ID, created.ID, input); err != nil {
		t.Fatalf("disable Webhook: %v", err)
	}
	page, err := webhookStore.ListDeliveries(user.ID, created.ID, "", 10, "")
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("deliveries after disable = %+v, %v", page, err)
	}
	statusByID := map[string]WebhookDeliveryStatus{}
	for _, item := range page.Items {
		statusByID[item.ID] = item.Status
	}
	if statusByID[claimed.ID] != WebhookDeliveryQueued {
		t.Fatalf("in-flight status = %s, want queued until its attempt completes", statusByID[claimed.ID])
	}
	for id, status := range statusByID {
		if id != claimed.ID && status != WebhookDeliveryCanceled {
			t.Fatalf("waiting delivery %s status = %s, want canceled", id, status)
		}
	}
	finalStatus, err := webhookStore.CompleteAttempt(claimed, webhookAttemptResult{
		CompletedAt: clock.Add(time.Second), StatusCode: 503,
	})
	if err != nil || finalStatus != WebhookDeliveryCanceled {
		t.Fatalf("complete disabled in-flight delivery = %s, %v", finalStatus, err)
	}
}

func TestWebhookDeletePreservesCompletedDeliveryDetails(t *testing.T) {
	_, webhookStore, user := newWebhookStoreFixture(t)
	now := time.Now().UTC()
	webhookStore.now = func() time.Time { return now }
	input := testWebhookInput("wh_delete_history")
	created, err := webhookStore.Create(user.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	input.ExpectedRevision = created.Revision
	delivery, err := webhookStore.EnqueueTest(user.ID, input, "client.online")
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := webhookStore.ClaimDue(user.ID, now)
	if err != nil || !ok {
		t.Fatalf("claim delivery before delete = %v, %v", ok, err)
	}
	if _, err := webhookStore.CompleteAttempt(claimed, webhookAttemptResult{CompletedAt: now, StatusCode: 204}); err != nil {
		t.Fatal(err)
	}
	if err := webhookStore.Delete(user.ID, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := webhookStore.Get(user.ID, created.ID); err != ErrWebhookNotFound {
		t.Fatalf("deleted Webhook error = %v", err)
	}
	preserved, err := webhookStore.GetDelivery(user.ID, delivery.ID)
	if err != nil || preserved.Status != WebhookDeliverySuccess || len(preserved.Attempts) != 1 {
		t.Fatalf("preserved delivery = %+v, %v", preserved, err)
	}
}

func TestWebhookRowsCascadeWhenUserIsDeleted(t *testing.T) {
	adminStore, webhookStore, admin := newWebhookStoreFixture(t)
	target, err := adminStore.CreateUser("webhook-cascade", "Password123")
	if err != nil {
		t.Fatal(err)
	}
	input := testWebhookInput("wh_cascade")
	if _, err := webhookStore.Create(target.ID, input); err != nil {
		t.Fatal(err)
	}
	if _, err := webhookStore.EnqueueTest(target.ID, input, "client.online"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := webhookStore.ClaimDue(target.ID, time.Now().UTC()); err != nil || !ok {
		t.Fatalf("claim cascade delivery = %v, %v", ok, err)
	}
	if _, _, err := adminStore.SetUserStatus(admin.ID, target.ID, UserStatusDisabled); err != nil {
		t.Fatal(err)
	}
	if err := adminStore.DeleteDisabledUser(admin.ID, target.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{
		"activity_webhooks",
		"activity_webhook_events",
		"activity_webhook_deliveries",
		"activity_webhook_delivery_attempts",
		"activity_webhook_dispatch_slots",
	} {
		var count int
		if err := adminStore.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows after user deletion = %d", table, count)
		}
	}
	assertNoSQLiteForeignKeyViolations(t, adminStore.db)
}

func formatRetentionIndex(index int) string {
	return fmt.Sprintf("%04d", index)
}
