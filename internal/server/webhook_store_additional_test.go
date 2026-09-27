package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"netsgo/pkg/protocol"
)

func registerWebhookClient(t *testing.T, store *AdminStore, ownerUserID, installID, hostname string) *RegisteredClient {
	t.Helper()
	client, err := store.GetOrCreateClientForUser(ownerUserID, installID, protocol.ClientInfo{
		Hostname: hostname, OS: "linux", Arch: "amd64", Version: "0.1.0",
	}, "127.0.0.1:12345")
	if err != nil {
		t.Fatalf("register Webhook client: %v", err)
	}
	return client
}

func appendWebhookClientEvent(t *testing.T, store *AdminStore, owner User, clientID, action, dedupeKey string, occurredAt time.Time) int64 {
	t.Helper()
	activityStore := newActivityStoreWithDB("", store.db, false)
	activityStore.now = func() time.Time { return occurredAt }
	payload := newActivityClientLifecyclePayload(action, "", 1, true, ActivitySummaryArgs{ClientName: clientID})
	eventID, err := activityStore.Append(ActivityEventSpec{
		OccurredAt: occurredAt, Category: ActivityCategoryClient, Action: action, Source: "test",
		ScopeUserID: owner.ID, SubjectUserID: owner.ID, DedupeKey: dedupeKey,
		Actor:   ActivityActor{Type: "client", ID: clientID},
		Payload: payload,
		Clients: []ActivityClientSubject{{ClientID: clientID, Relation: "subject"}},
	})
	if err != nil {
		t.Fatalf("append Webhook client event: %v", err)
	}
	return eventID
}

func TestWebhookStoreCRUDRevisionAndTargetOwnership(t *testing.T) {
	adminStore, webhookStore, owner := newWebhookStoreFixture(t)
	ownerClient := registerWebhookClient(t, adminStore, owner.ID, "webhook-owner-client", "owner-client")
	other, err := adminStore.CreateUser("webhook-target-other", "Password123")
	if err != nil {
		t.Fatal(err)
	}
	otherClient := registerWebhookClient(t, adminStore, other.ID, "webhook-other-client", "other-client")

	input := testWebhookInput("  wh_crud  ")
	input.Name = "  CRUD Webhook  "
	input.TargetMode = WebhookTargetSelected
	input.TargetIDs = []string{ownerClient.ID, ownerClient.ID}
	input.Events = []string{"client.offline", "client.online", "client.online"}
	created, err := webhookStore.Create(owner.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "wh_crud" || created.Name != "CRUD Webhook" || created.Revision != 1 || !created.Enabled {
		t.Fatalf("created Webhook = %+v", created)
	}
	if !reflect.DeepEqual(created.TargetIDs, []string{ownerClient.ID}) || !reflect.DeepEqual(created.Events, []string{"client.offline", "client.online"}) {
		t.Fatalf("created relations = targets %v events %v", created.TargetIDs, created.Events)
	}
	if _, err := webhookStore.Get(other.ID, created.ID); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("cross-user Get error = %v", err)
	}
	if _, err := webhookStore.Create(owner.ID, input); !errors.Is(err, ErrWebhookRevisionConflict) {
		t.Fatalf("duplicate create error = %v", err)
	}

	update := normalizeWebhookInput(input)
	update.Name = "Updated Webhook"
	if _, err := webhookStore.Update(owner.ID, created.ID, update); !errors.Is(err, ErrWebhookRevisionConflict) {
		t.Fatalf("missing revision update error = %v", err)
	}
	update.ExpectedRevision = created.Revision + 1
	if _, err := webhookStore.Update(owner.ID, created.ID, update); !errors.Is(err, ErrWebhookRevisionConflict) {
		t.Fatalf("stale revision update error = %v", err)
	}
	update.ExpectedRevision = created.Revision
	update.TargetIDs = []string{otherClient.ID}
	err = func() error {
		_, updateErr := webhookStore.Update(owner.ID, created.ID, update)
		return updateErr
	}()
	assertWebhookValidationError(t, err, "targets", "target_not_found")

	update.TargetIDs = []string{ownerClient.ID}
	update.Events = []string{"client.online"}
	updated, err := webhookStore.Update(owner.ID, created.ID, update)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.Name != "Updated Webhook" || !reflect.DeepEqual(updated.Events, []string{"client.online"}) {
		t.Fatalf("updated Webhook = %+v", updated)
	}
	if err := webhookStore.Delete(other.ID, created.ID); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("cross-user delete error = %v", err)
	}
	if err := webhookStore.Delete(owner.ID, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := webhookStore.Get(owner.ID, created.ID); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("deleted Get error = %v", err)
	}
}

func TestWebhookStoreEnforcesPerUserLimitIndependently(t *testing.T) {
	adminStore, webhookStore, owner := newWebhookStoreFixture(t)
	for index := 0; index < webhookMaxPerUser; index++ {
		if _, err := webhookStore.Create(owner.ID, testWebhookInput(fmt.Sprintf("wh_limit_%02d", index))); err != nil {
			t.Fatalf("create Webhook %d: %v", index, err)
		}
	}
	if _, err := webhookStore.Create(owner.ID, testWebhookInput("wh_limit_overflow")); !errors.Is(err, ErrWebhookLimitReached) {
		t.Fatalf("overflow create error = %v", err)
	}
	other, err := adminStore.CreateUser("webhook-limit-other", "Password123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := webhookStore.Create(other.ID, testWebhookInput("wh_limit_other")); err != nil {
		t.Fatalf("second user create at independent limit: %v", err)
	}
}

func TestActivityWebhookMatchingHonorsOwnerEnabledEventAndSelectedTargets(t *testing.T) {
	adminStore, webhookStore, owner := newWebhookStoreFixture(t)
	clientA := registerWebhookClient(t, adminStore, owner.ID, "webhook-match-a", "match-a")
	clientB := registerWebhookClient(t, adminStore, owner.ID, "webhook-match-b", "match-b")
	other, err := adminStore.CreateUser("webhook-match-other", "Password123")
	if err != nil {
		t.Fatal(err)
	}

	all := testWebhookInput("wh_match_all")
	all.Events = []string{"client.online"}
	selectedA := testWebhookInput("wh_match_selected_a")
	selectedA.TargetMode, selectedA.TargetIDs, selectedA.Events = WebhookTargetSelected, []string{clientA.ID}, []string{"client.online"}
	selectedB := testWebhookInput("wh_match_selected_b")
	selectedB.TargetMode, selectedB.TargetIDs, selectedB.Events = WebhookTargetSelected, []string{clientB.ID}, []string{"client.online"}
	disabled := testWebhookInput("wh_match_disabled")
	disabled.Enabled, disabled.Events = false, []string{"client.online"}
	wrongEvent := testWebhookInput("wh_match_offline")
	wrongEvent.Events = []string{"client.offline"}
	for _, fixture := range []WebhookConfigInput{all, selectedA, selectedB, disabled, wrongEvent} {
		if _, err := webhookStore.Create(owner.ID, fixture); err != nil {
			t.Fatalf("create %s: %v", fixture.ID, err)
		}
	}
	if _, err := webhookStore.Create(other.ID, testWebhookInput("wh_match_other_owner")); err != nil {
		t.Fatal(err)
	}

	eventID := appendWebhookClientEvent(t, adminStore, owner, clientA.ID, "online", "webhook-match-online", time.Now().UTC())
	for webhookID, want := range map[string]int{
		all.ID: 1, selectedA.ID: 1, selectedB.ID: 0, disabled.ID: 0, wrongEvent.ID: 0,
	} {
		page, err := webhookStore.ListDeliveries(owner.ID, webhookID, "", 10, "")
		if err != nil {
			t.Fatalf("list %s deliveries: %v", webhookID, err)
		}
		if len(page.Items) != want {
			t.Fatalf("Webhook %s delivery count = %d, want %d", webhookID, len(page.Items), want)
		}
		if want == 1 && (page.Items[0].EventID != strconv.FormatInt(eventID, 10) || page.Items[0].Origin != WebhookOriginEvent) {
			t.Fatalf("Webhook %s delivery = %+v", webhookID, page.Items[0])
		}
	}
	otherPage, err := webhookStore.ListDeliveries(other.ID, "wh_match_other_owner", "", 10, "")
	if err != nil || len(otherPage.Items) != 0 {
		t.Fatalf("other owner deliveries = %+v, %v", otherPage, err)
	}
}

func TestWebhookPreviewAndTestDeliveryUseUnsavedConfiguration(t *testing.T) {
	adminStore, webhookStore, owner := newWebhookStoreFixture(t)
	client := registerWebhookClient(t, adminStore, owner.ID, "webhook-preview-client", "preview-client")
	input := testWebhookInput("wh_unsaved")
	input.ExpectedRevision = 9
	input.Name = "Unsaved Webhook"
	input.TargetMode, input.TargetIDs = WebhookTargetSelected, []string{client.ID}
	input.Events = []string{"client.online"}
	input.URL = "https://example.test/hook?event={{event.name.en-US}}&client={{client.id}}&delivery={{delivery.id}}"
	input.Headers = []WebhookHeader{{Key: "X-Webhook", Value: "{{webhook.name}}"}}
	input.Body = `{"attempt":"{{delivery.attempt}}","name":"{{event.name.en-US}}","webhook":"{{webhook.name}}"}`

	preview, err := webhookStore.Preview(input, "client.online")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Event != "client.online" || preview.Method != WebhookMethodPOST || preview.Headers["X-Webhook"] != input.Name {
		t.Fatalf("preview = %+v", preview)
	}
	if !strings.Contains(preview.URL, "event=Client%20online") || !strings.Contains(preview.URL, "delivery=dlv_sample_client_online") {
		t.Fatalf("preview URL = %s", preview.URL)
	}
	var previewBody map[string]any
	if preview.Body == nil || jsonUnmarshalUseNumber([]byte(*preview.Body), &previewBody) != nil {
		t.Fatalf("preview body = %v", preview.Body)
	}
	if previewBody["attempt"] != jsonNumber("1") || previewBody["name"] != "Client online" {
		t.Fatalf("preview typed body = %#v", previewBody)
	}

	delivery, err := webhookStore.EnqueueTest(owner.ID, input, "client.online")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := webhookStore.getStoredDelivery(owner.ID, delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Origin != WebhookOriginTest || stored.MaxAttempts != 1 || stored.ConfigRevision != input.ExpectedRevision || stored.SourceEventID.Valid {
		t.Fatalf("test delivery persistence = %+v", stored)
	}
	if !reflect.DeepEqual(stored.EventSnapshot.MatchedTargetIDs, []string{client.ID}) || stored.RequestHeaders["X-Webhook"] != input.Name {
		t.Fatalf("test delivery target/request = targets %v headers %#v", stored.EventSnapshot.MatchedTargetIDs, stored.RequestHeaders)
	}

	_, err = webhookStore.Preview(input, "client.offline")
	assertWebhookValidationError(t, err, "event", "event_not_selected")
	_, err = webhookStore.EnqueueTest(owner.ID, input, "client.offline")
	assertWebhookValidationError(t, err, "event", "event_not_selected")
}

// These aliases keep typed JSON assertions concise without changing production decoding.
func jsonNumber(value string) any { return json.Number(value) }

func jsonUnmarshalUseNumber(raw []byte, value any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	return decoder.Decode(value)
}

func TestWebhookDeliveryPaginationCursorAndStatusFilter(t *testing.T) {
	_, webhookStore, owner := newWebhookStoreFixture(t)
	clock := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	webhookStore.now = func() time.Time { return clock }
	input := testWebhookInput("wh_page")
	if _, err := webhookStore.Create(owner.ID, input); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 5)
	for index := 0; index < 5; index++ {
		delivery, err := webhookStore.EnqueueTest(owner.ID, input, "client.online")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, delivery.ID)
		clock = clock.Add(time.Minute)
	}
	for _, id := range []string{ids[1], ids[4]} {
		if _, err := webhookStore.db.Exec(`UPDATE activity_webhook_deliveries SET status = 'success', completed_at_ns = updated_at_ns WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}

	page1, err := webhookStore.ListDeliveries(owner.ID, input.ID, "", 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := deliveryIDs(page1.Items); !reflect.DeepEqual(got, []string{ids[4], ids[3]}) || !page1.HasMore || page1.NextCursor == "" {
		t.Fatalf("page 1 = ids %v has_more %v cursor %q", got, page1.HasMore, page1.NextCursor)
	}
	cursor, err := decodeWebhookDeliveryCursor(page1.NextCursor)
	if err != nil || cursor.ID != ids[3] {
		t.Fatalf("decoded cursor = %+v, %v", cursor, err)
	}
	page2, err := webhookStore.ListDeliveries(owner.ID, input.ID, page1.NextCursor, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := deliveryIDs(page2.Items); !reflect.DeepEqual(got, []string{ids[2], ids[1]}) || !page2.HasMore {
		t.Fatalf("page 2 = ids %v has_more %v", got, page2.HasMore)
	}
	page3, err := webhookStore.ListDeliveries(owner.ID, input.ID, page2.NextCursor, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := deliveryIDs(page3.Items); !reflect.DeepEqual(got, []string{ids[0]}) || page3.HasMore || page3.NextCursor != "" {
		t.Fatalf("page 3 = ids %v has_more %v cursor %q", got, page3.HasMore, page3.NextCursor)
	}
	success, err := webhookStore.ListDeliveries(owner.ID, input.ID, "", 1000, WebhookDeliverySuccess)
	if err != nil || !reflect.DeepEqual(deliveryIDs(success.Items), []string{ids[4], ids[1]}) {
		t.Fatalf("success filter = %v, %v", deliveryIDs(success.Items), err)
	}
	_, err = webhookStore.ListDeliveries(owner.ID, input.ID, "not-base64", 2, "")
	assertWebhookValidationError(t, err, "cursor", "invalid_cursor")
}

func deliveryIDs(items []WebhookDelivery) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.ID)
	}
	return result
}

func TestWebhookDeliveryCursorRejectsMalformedPayloads(t *testing.T) {
	valid, err := encodeWebhookDeliveryCursor(webhookDeliveryCursor{CreatedAtNS: 123, ID: "dlv"})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeWebhookDeliveryCursor(valid)
	if err != nil || decoded.CreatedAtNS != 123 || decoded.ID != "dlv" {
		t.Fatalf("cursor round trip = %+v, %v", decoded, err)
	}
	for _, raw := range []string{"%%%", "e30", "eyJjcmVhdGVkX2F0X25zIjoxMjN9", "eyJpZCI6ImRsdiJ9"} {
		if _, err := decodeWebhookDeliveryCursor(raw); err == nil {
			t.Fatalf("malformed cursor %q was accepted", raw)
		}
	}
}

func TestWebhookReplayUsesCurrentConfigurationAndOriginalEventSnapshot(t *testing.T) {
	adminStore, webhookStore, owner := newWebhookStoreFixture(t)
	clock := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	webhookStore.now = func() time.Time { return clock }
	client := registerWebhookClient(t, adminStore, owner.ID, "webhook-replay-client", "replay-client")
	input := testWebhookInput("wh_replay")
	input.Events = []string{"client.online"}
	input.URL = "https://initial.example/hook/{{event.occurred_at}}"
	created, err := webhookStore.Create(owner.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	eventID := appendWebhookClientEvent(t, adminStore, owner, client.ID, "online", "webhook-replay-event", clock)
	page, err := webhookStore.ListDeliveries(owner.ID, created.ID, "", 10, "")
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("event delivery page = %+v, %v", page, err)
	}
	original := page.Items[0]
	claimed, ok, err := webhookStore.ClaimDue(owner.ID, clock)
	if err != nil || !ok || claimed.ID != original.ID {
		t.Fatalf("claim original = %s, %v, %v", claimed.ID, ok, err)
	}
	if status, err := webhookStore.CompleteAttempt(claimed, webhookAttemptResult{CompletedAt: clock.Add(time.Second), StatusCode: 204}); err != nil || status != WebhookDeliverySuccess {
		t.Fatalf("complete original = %s, %v", status, err)
	}

	input.ExpectedRevision = created.Revision
	input.Name = "Current Replay Webhook"
	input.URL = "https://current.example/hook?event={{event.name.en-US}}&delivery={{delivery.id}}"
	input.Headers = []WebhookHeader{{Key: "X-Current", Value: "{{webhook.name}}"}}
	updated, err := webhookStore.Update(owner.ID, created.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * time.Second)
	replay, err := webhookStore.Replay(owner.ID, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := webhookStore.getStoredDelivery(owner.ID, replay.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID == original.ID || replay.Origin != WebhookOriginReplay || replay.EventID != strconv.FormatInt(eventID, 10) {
		t.Fatalf("replay identity = %+v", replay)
	}
	if stored.ConfigRevision != updated.Revision || stored.MaxAttempts != 3 || !stored.SourceEventID.Valid || stored.SourceEventID.Int64 != eventID {
		t.Fatalf("replay persisted metadata = %+v", stored)
	}
	if replay.WebhookName != updated.Name || !strings.Contains(replay.RequestURL, "current.example") || replay.RequestHeaders["X-Current"] != updated.Name {
		t.Fatalf("replay current request = %+v", replay)
	}
	unchangedOriginal, err := webhookStore.GetDelivery(owner.ID, original.ID)
	if err != nil || !strings.Contains(unchangedOriginal.RequestURL, "initial.example") {
		t.Fatalf("original snapshot changed = %+v, %v", unchangedOriginal, err)
	}
	current, err := webhookStore.Get(owner.ID, created.ID)
	if err != nil || current.Calls24h != 2 || current.LastStatus != "success" {
		t.Fatalf("Webhook health/calls after event and replay = %+v, %v", current, err)
	}

	testDelivery, err := webhookStore.EnqueueTest(owner.ID, input, "client.online")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := webhookStore.Replay(owner.ID, testDelivery.ID); !errors.Is(err, ErrWebhookReplayUnavailable) {
		t.Fatalf("test replay error = %v", err)
	}
	input.ExpectedRevision = updated.Revision
	input.Events = []string{"client.offline"}
	if _, err := webhookStore.Update(owner.ID, created.ID, input); err != nil {
		t.Fatal(err)
	}
	if _, err := webhookStore.Replay(owner.ID, original.ID); !errors.Is(err, ErrWebhookReplayUnavailable) {
		t.Fatalf("unsubscribed replay error = %v", err)
	}
}

func TestWebhookRecoveryAtAttemptLimitBecomesTerminal(t *testing.T) {
	_, webhookStore, owner := newWebhookStoreFixture(t)
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	webhookStore.now = func() time.Time { return now }
	delivery, err := webhookStore.EnqueueTest(owner.ID, testWebhookInput("wh_terminal_recovery"), "client.online")
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := webhookStore.ClaimDue(owner.ID, now)
	if err != nil || !ok {
		t.Fatalf("claim test delivery = %v, %v", ok, err)
	}
	recoveredAt := now.Add(webhookDeliveryLease + time.Second)
	if err := webhookStore.RecoverInterrupted(recoveredAt); err != nil {
		t.Fatal(err)
	}
	recovered, err := webhookStore.GetDelivery(owner.ID, delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != WebhookDeliveryFailed || recovered.NextAttemptAt != nil || recovered.Error != "server interrupted the request" {
		t.Fatalf("terminal recovery delivery = %+v", recovered)
	}
	if len(recovered.Attempts) != 1 || recovered.Attempts[0].Status != "failed" || recovered.Attempts[0].Error != "server interrupted the request" {
		t.Fatalf("terminal recovery attempts = %+v", recovered.Attempts)
	}
	if _, ok, err := webhookStore.ClaimDue(owner.ID, recoveredAt.Add(time.Hour)); err != nil || ok {
		t.Fatalf("terminal delivery reclaimed = %v, %v", ok, err)
	}
	if claimed.AttemptCount != 1 {
		t.Fatalf("claimed attempt count = %d", claimed.AttemptCount)
	}
}

func TestWebhookDueOwnersCancelsDisabledUserAndQueueStats(t *testing.T) {
	adminStore, webhookStore, admin := newWebhookStoreFixture(t)
	owner, err := adminStore.CreateUser("webhook-disabled-owner", "Password123")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 11, 0, 0, 0, time.UTC)
	webhookStore.now = func() time.Time { return now }
	delivery, err := webhookStore.EnqueueTest(owner.ID, testWebhookInput("wh_disabled_owner"), "client.online")
	if err != nil {
		t.Fatal(err)
	}
	count, oldest, err := webhookStore.QueueStats(now.Add(3 * time.Second))
	if err != nil || count != 1 || oldest != 3*time.Second {
		t.Fatalf("queue stats before disable = count %d oldest %v err %v", count, oldest, err)
	}
	if _, _, err := adminStore.SetUserStatus(admin.ID, owner.ID, UserStatusDisabled); err != nil {
		t.Fatal(err)
	}
	owners, err := webhookStore.DueOwners(now, 10)
	if err != nil || len(owners) != 0 {
		t.Fatalf("due owners after disable = %v, %v", owners, err)
	}
	canceled, err := webhookStore.GetDelivery(owner.ID, delivery.ID)
	if err != nil || canceled.Status != WebhookDeliveryCanceled || canceled.Error != "Webhook or user is no longer available" {
		t.Fatalf("disabled owner delivery = %+v, %v", canceled, err)
	}
	count, oldest, err = webhookStore.QueueStats(now.Add(4 * time.Second))
	if err != nil || count != 0 || oldest != 0 {
		t.Fatalf("queue stats after cancel = count %d oldest %v err %v", count, oldest, err)
	}
}
