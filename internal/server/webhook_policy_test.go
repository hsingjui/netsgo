package server

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func policySettings(allowPrivate bool, cap int) WebhookSettings {
	return WebhookSettings{AllowPrivateTargets: allowPrivate, DailyDeliveryCap: cap}
}

func TestValidateWebhookInputPrivateTargetPolicy(t *testing.T) {
	catalog := activityWebhookCatalog()
	blockedURLs := []string{
		"http://127.0.0.1:8080/hook",
		"http://localhost/hook",
		"http://192.168.1.5/hook",
		"http://10.0.0.9/hook",
		"http://169.254.169.254/latest/meta-data",
		"http://[::1]/hook",
		"http://[fe80::1]/hook",
		"http://100.64.0.7/hook",
		"http://198.18.0.1/hook",
		"http://192.0.2.1/hook",
		"http://203.0.113.9/hook",
		"http://240.0.0.1/hook",
		"http://[2001:db8::1]/hook",
		"http://[100::1]/hook",
		"http://0.0.0.0/hook",
	}
	for _, url := range blockedURLs {
		input := normalizeWebhookInput(testWebhookInput("wh_private_policy"))
		input.URL = url
		err := validateWebhookInput(input, catalog.Fixtures, catalog.Variables, false)
		assertWebhookValidationError(t, err, "url", "url_target_not_allowed")
		loopback := normalizeWebhookInput(testWebhookInput("wh_private_policy"))
		loopback.URL = url
		if err := validateWebhookInput(loopback, catalog.Fixtures, catalog.Variables, true); err != nil {
			t.Fatalf("allow private: %q should pass, got %v", url, err)
		}
	}
}

func TestWebhookDispatcherPolicyBlocksPooledPrivateConnections(t *testing.T) {
	_, webhookStore, owner := newWebhookStoreFixture(t)
	allowed := policySettings(true, defaultWebhookDailyDeliveryCap)
	blocked := policySettings(false, defaultWebhookDailyDeliveryCap)
	current := &allowed
	webhookStore.settings = func() WebhookSettings { return *current }

	var hits int
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte("ok"))
	}))
	defer receiver.Close()

	input := testWebhookInput("wh_keepalive_guard")
	input.URL = receiver.URL + "/hook"
	input.Events = []string{"client.online"}

	enqueue := func(id string) WebhookDelivery {
		t.Helper()
		delivery, err := webhookStore.EnqueueTest(owner.ID, input, "client.online")
		if err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
		return delivery
	}
	execute := func(id string, delivery WebhookDelivery, claimTime time.Time) WebhookDelivery {
		t.Helper()
		claimed, ok, err := webhookStore.ClaimDue(owner.ID, claimTime)
		if err != nil || !ok {
			t.Fatalf("claim %s = %v, %v", id, ok, err)
		}
		dispatcher := newWebhookDispatcher(webhookStore, nil)
		dispatcher.execute(claimed)
		dispatcher.cancel()
		result, err := webhookStore.GetDelivery(owner.ID, delivery.ID)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	base := time.Now().UTC().Add(time.Minute)
	// Enqueue both while the policy still allows the private target; the
	// policy flips only for the second execution so the first request has
	// pooled a keep-alive connection by then.
	firstDelivery := enqueue("allowed")
	secondDelivery := enqueue("blocked-second")
	first := execute("allowed", firstDelivery, base)
	if first.Status != WebhookDeliverySuccess {
		t.Fatalf("allowed delivery status = %q (%s)", first.Status, first.Error)
	}

	current = &blocked
	second := execute("blocked", secondDelivery, base.Add(3*time.Second))
	if second.Status != WebhookDeliveryFailed {
		t.Fatalf("blocked pooled delivery status = %q, want failed", second.Status)
	}
	if !strings.Contains(second.Error, "private") {
		t.Fatalf("blocked pooled delivery error = %q, want private-address rejection", second.Error)
	}
	if hits != 1 {
		t.Fatalf("receiver hits = %d, want 1 (pooled connection must not be reused)", hits)
	}
}

func TestWebhookDispatcherDialGuardBlocksPrivateWhenDisabled(t *testing.T) {
	_, webhookStore, owner := newWebhookStoreFixture(t)
	allowed := policySettings(true, defaultWebhookDailyDeliveryCap)
	blocked := policySettings(false, defaultWebhookDailyDeliveryCap)
	current := &allowed
	webhookStore.settings = func() WebhookSettings { return *current }

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer receiver.Close()

	input := testWebhookInput("wh_dial_guard")
	input.URL = receiver.URL + "/hook"
	delivery, err := webhookStore.EnqueueTest(owner.ID, input, "client.online")
	if err != nil {
		t.Fatalf("enqueue test delivery: %v", err)
	}

	clock := time.Now().UTC().Add(time.Minute)
	claimed, ok, err := webhookStore.ClaimDue(owner.ID, clock)
	if err != nil || !ok || claimed.ID != delivery.ID {
		t.Fatalf("claim = %v, %v, %v", claimed, ok, err)
	}

	current = &blocked
	dispatcher := newWebhookDispatcher(webhookStore, nil)
	defer dispatcher.cancel()
	dispatcher.execute(claimed)

	blockedResult, err := webhookStore.GetDelivery(owner.ID, delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blockedResult.Status != WebhookDeliveryFailed {
		t.Fatalf("blocked dial status = %q, want failed", blockedResult.Status)
	}
	if !strings.Contains(blockedResult.Error, "private") {
		t.Fatalf("blocked dial error = %q, want private-address rejection", blockedResult.Error)
	}
}

func TestWebhookPendingCapDropsEventsAndRejectsManual(t *testing.T) {
	adminStore, webhookStore, owner := newWebhookStoreFixture(t)
	client := registerWebhookClient(t, adminStore, owner.ID, "pending-client", "pending-host")

	input := testWebhookInput("wh_pending_cap")
	input.Events = []string{"client.online"}
	if _, err := webhookStore.Create(owner.ID, input); err != nil {
		t.Fatalf("create webhook: %v", err)
	}

	// Saturate the per-user pending budget directly; going through the API
	// would be drained by nothing here anyway (no dispatcher running).
	now := webhookStore.now().UTC()
	for i := 0; i < webhookMaxPendingPerUser; i++ {
		if _, err := adminStore.db.Exec(`INSERT INTO activity_webhook_deliveries (
			id, owner_user_id, webhook_id, webhook_name, origin, event_type,
			event_occurred_at_ns, status, attempt_count, max_attempts, next_attempt_at_ns, config_revision,
			config_snapshot_json, event_snapshot_json, values_snapshot_json,
			request_method, request_url, request_headers_json, created_at_ns, updated_at_ns
		) VALUES (?, ?, 'wh_other', 'other', 'event', 'client.online', ?, 'queued', 0, 3, ?, 1,
			'{}', '{}', '{}', 'POST', 'http://example.com/hook', '{}', ?, ?)`,
			fmt.Sprintf("dlv_pending_%d", i), owner.ID, now.UnixNano(), now.UnixNano(), now.UnixNano(), now.UnixNano()); err != nil {
			t.Fatalf("seed pending delivery %d: %v", i, err)
		}
	}

	appendWebhookClientEvent(t, adminStore, owner, client.ID, "online", "pending-cap-event", now)
	page, err := webhookStore.ListDeliveries(owner.ID, "wh_pending_cap", "", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("event delivery beyond pending cap should be dropped: %+v", page.Items)
	}

	if _, err := webhookStore.EnqueueTest(owner.ID, input, "client.online"); !errors.Is(err, ErrWebhookPendingFull) {
		t.Fatalf("manual enqueue error = %v, want pending cap", err)
	}
}

func TestWebhookDeliveriesRemainReadableAfterWebhookDeletion(t *testing.T) {
	server, handler, cleanup := setupActivityAPIAuthTest(t)
	defer cleanup()
	_, token := issueRoleToken(t, server, "webhook-deliveries-retention")

	input := testWebhookInput("")
	input.Events = []string{"client.online"}
	createResponse := serveWebhookAPIRequest(t, handler, http.MethodPost, "/api/webhooks", token, input)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create webhook status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}
	created := decodeWebhookAPIResponse[ActivityWebhook](t, createResponse)
	input.ID = created.ID
	input.ExpectedRevision = created.Revision

	testResponse := serveWebhookAPIRequest(t, handler, http.MethodPost, "/api/webhooks/test", token, webhookPreviewRequest{Config: input, Event: "client.online"})
	if testResponse.Code != http.StatusAccepted {
		t.Fatalf("test delivery status=%d body=%s", testResponse.Code, testResponse.Body.String())
	}

	deleteResponse := serveWebhookAPIRequest(t, handler, http.MethodDelete, "/api/webhooks/"+created.ID, token, nil)
	if deleteResponse.Code != http.StatusOK && deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("delete webhook status=%d body=%s", deleteResponse.Code, deleteResponse.Body.String())
	}

	listResponse := serveWebhookAPIRequest(t, handler, http.MethodGet, "/api/webhooks/"+created.ID+"/deliveries", token, nil)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("deliveries after deletion status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
	page := decodeWebhookAPIResponse[WebhookDeliveryPage](t, listResponse)
	if len(page.Items) != 1 || page.Items[0].Origin != WebhookOriginTest {
		t.Fatalf("retained deliveries = %+v, want the test delivery", page.Items)
	}
}

func TestWebhookSettingsUpdateWritesAdminActivity(t *testing.T) {
	adminStore, _, _ := newWebhookStoreFixture(t)
	actor := ActivityActor{Type: "admin", ID: "admin-id", Name: "admin"}

	idle, err := adminStore.UpdateWebhookSettingsWithActivity(WebhookSettings{AllowPrivateTargets: false, DailyDeliveryCap: defaultWebhookDailyDeliveryCap}, actor)
	if err != nil || idle != 0 {
		t.Fatalf("no-op update = (%d, %v), want no activity", idle, err)
	}
	activityID, err := adminStore.UpdateWebhookSettingsWithActivity(WebhookSettings{AllowPrivateTargets: true, DailyDeliveryCap: 120}, actor)
	if err != nil || activityID == 0 {
		t.Fatalf("policy update = (%d, %v), want activity", activityID, err)
	}
	var action string
	if err := adminStore.db.QueryRow(`SELECT action FROM activity_events WHERE id = ?`, activityID).Scan(&action); err != nil || action != "webhook_policy_changed" {
		t.Fatalf("activity action = %q, %v, want webhook_policy_changed", action, err)
	}
	got, err := adminStore.GetWebhookSettings()
	if err != nil || !got.AllowPrivateTargets || got.DailyDeliveryCap != 120 {
		t.Fatalf("settings after update = %+v, %v", got, err)
	}
}

func TestWebhookDailyCapRejectsTestAndReplayButNotEvents(t *testing.T) {
	adminStore, webhookStore, owner := newWebhookStoreFixture(t)
	webhookStore.settings = func() WebhookSettings { return policySettings(true, 1) }
	client := registerWebhookClient(t, adminStore, owner.ID, "cap-client", "cap-host")

	input := testWebhookInput("wh_daily_cap")
	input.Events = []string{"client.online"}
	if _, err := webhookStore.Create(owner.ID, input); err != nil {
		t.Fatalf("create webhook: %v", err)
	}
	if _, err := webhookStore.EnqueueTest(owner.ID, input, "client.online"); err != nil {
		t.Fatalf("first test delivery should pass: %v", err)
	}

	appendWebhookClientEvent(t, adminStore, owner, client.ID, "online", "cap-event", time.Now().UTC())
	page, err := webhookStore.ListDeliveries(owner.ID, "wh_daily_cap", "", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("event delivery must not be capped: %+v", page.Items)
	}

	if _, err := webhookStore.EnqueueTest(owner.ID, input, "client.online"); !errors.Is(err, ErrWebhookDailyCapReached) {
		t.Fatalf("second test delivery error = %v, want daily cap", err)
	}

	origin := page.Items[0]
	if origin.Origin == WebhookOriginTest {
		origin = page.Items[1]
	}
	if _, err := webhookStore.Replay(owner.ID, origin.ID); !errors.Is(err, ErrWebhookDailyCapReached) {
		t.Fatalf("replay error = %v, want daily cap", err)
	}
}

func TestWebhookReplayRejectsWhenTargetsNoLongerMatch(t *testing.T) {
	adminStore, webhookStore, owner := newWebhookStoreFixture(t)
	clientA := registerWebhookClient(t, adminStore, owner.ID, "replay-client-a", "host-a")
	clientB := registerWebhookClient(t, adminStore, owner.ID, "replay-client-b", "host-b")

	input := testWebhookInput("wh_replay_match")
	input.Events = []string{"client.online"}
	input.TargetMode = WebhookTargetSelected
	input.TargetIDs = []string{clientA.ID}
	if _, err := webhookStore.Create(owner.ID, input); err != nil {
		t.Fatalf("create webhook: %v", err)
	}
	appendWebhookClientEvent(t, adminStore, owner, clientA.ID, "online", "replay-event", time.Now().UTC())
	page, err := webhookStore.ListDeliveries(owner.ID, "wh_replay_match", "", 10, "")
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("deliveries = %+v, %v", page.Items, err)
	}

	updated := input
	updated.ExpectedRevision = 1
	updated.TargetIDs = []string{clientB.ID}
	if _, err := webhookStore.Update(owner.ID, "wh_replay_match", updated); err != nil {
		t.Fatalf("retarget webhook: %v", err)
	}

	if _, err := webhookStore.Replay(owner.ID, page.Items[0].ID); !errors.Is(err, ErrWebhookReplayUnavailable) {
		t.Fatalf("replay after retarget error = %v, want unavailable", err)
	}
}
func TestWebhookAttemptIncludesBodyReadTimeAndFailure(t *testing.T) {
	_, webhookStore, owner := newWebhookStoreFixture(t)
	claimBase := time.Now().UTC().Add(time.Minute)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("late body"))
	}))
	defer slow.Close()

	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		panic(http.ErrAbortHandler)
	}))
	defer cut.Close()

	cases := []struct {
		name    string
		url     string
		failed  bool
		wantErr string
	}{
		{name: "slow body counts toward duration", url: slow.URL + "/hook", failed: false},
		{name: "cut body fails the attempt", url: cut.URL + "/hook", failed: true, wantErr: "read response body"},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := testWebhookInput("wh_body_" + string(rune('a'+index)))
			input.URL = tc.url
			input.Events = []string{"client.online"}
			delivery, err := webhookStore.EnqueueTest(owner.ID, input, "client.online")
			if err != nil {
				t.Fatalf("enqueue: %v", err)
			}
			claimed, ok, err := webhookStore.ClaimDue(owner.ID, claimBase.Add(time.Duration(index)*3*time.Second))
			if err != nil || !ok {
				t.Fatalf("claim = %v, %v", ok, err)
			}
			dispatcher := newWebhookDispatcher(webhookStore, nil)
			dispatcher.execute(claimed)
			dispatcher.cancel()

			result, err := webhookStore.GetDelivery(owner.ID, delivery.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.failed {
				if result.Status != WebhookDeliveryFailed {
					t.Fatalf("status = %q, want failed (%s)", result.Status, result.Error)
				}
				if !strings.Contains(result.Error, tc.wantErr) {
					t.Fatalf("error = %q, want %q", result.Error, tc.wantErr)
				}
				return
			}
			if result.Status != WebhookDeliverySuccess {
				t.Fatalf("status = %q, want success (%s)", result.Status, result.Error)
			}
			if result.DurationMS == nil || *result.DurationMS < 250 {
				t.Fatalf("duration = %v, want >= 250ms including body read", result.DurationMS)
			}
		})
	}
}

func TestWebhookInsertRejectsRenderedHeaderValue(t *testing.T) {
	adminStore, _, _ := newWebhookStoreFixture(t)
	tx, err := adminStore.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)

	config := testWebhookInput("wh_render_guard").snapshot(1)
	config.Headers = []WebhookHeader{{ID: "x-name", Key: "X-Client-Name", Value: "{{client.name}}"}}
	snapshot := sampleWebhookEvent("client.online")
	values := snapshot.values("dlv_render", config.ID, config.Name)
	values["client.name"] = "bad\nname"

	err = insertWebhookDeliveryTx(tx, "user-render", config, snapshot, values, WebhookOriginEvent, sql.NullInt64{Int64: 1, Valid: true}, 3, time.Now().UTC())
	var validation *webhookValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("insert error = %v, want validation error", err)
	}
	if validation.Field != "headers" || validation.Code != "invalid_header_value" {
		t.Fatalf("validation = %+v", validation)
	}
}

func TestWebhookEventEnqueueSkipsInvalidRenderedHeader(t *testing.T) {
	adminStore, webhookStore, owner := newWebhookStoreFixture(t)
	client := registerWebhookClient(t, adminStore, owner.ID, "hostile-client", "hostile-host")

	input := testWebhookInput("wh_hostile_render")
	input.Events = []string{"client.online"}
	input.Headers = append(input.Headers, WebhookHeader{ID: "x-name", Key: "X-Client-Name", Value: "{{client.name}}"})
	if _, err := webhookStore.Create(owner.ID, input); err != nil {
		t.Fatalf("create webhook: %v", err)
	}

	activityStore := newActivityStoreWithDB("", adminStore.db, false)
	spec := ActivityEventSpec{
		OccurredAt: time.Now().UTC(), Category: ActivityCategoryClient, Action: "online", Source: "test",
		ScopeUserID: owner.ID, SubjectUserID: owner.ID, DedupeKey: "hostile-render-event",
		Actor:   ActivityActor{Type: "client", ID: client.ID},
		Payload: newActivityClientLifecyclePayload("online", "", 1, true, ActivitySummaryArgs{ClientName: "edge"}),
		Clients: []ActivityClientSubject{{ClientID: client.ID, Relation: "subject", DisplayName: "bad\nname"}},
	}
	if _, err := activityStore.Append(spec); err != nil {
		t.Fatalf("append with hostile display name must not fail the activity: %v", err)
	}

	page, err := webhookStore.ListDeliveries(owner.ID, "wh_hostile_render", "", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("delivery with hostile rendered header should be skipped: %+v", page.Items)
	}
}

func TestActivityAppendInfersP2PScopeAndEnqueuesWebhookDelivery(t *testing.T) {
	adminStore, webhookStore, owner := newWebhookStoreFixture(t)
	peerA := registerWebhookClient(t, adminStore, owner.ID, "p2p-peer-a", "peer-a")
	peerB := registerWebhookClient(t, adminStore, owner.ID, "p2p-peer-b", "peer-b")

	now := formatTime(time.Now().UTC())
	if _, err := adminStore.db.Exec(`INSERT INTO tunnels (
		id, name, client_id, type, local_ip, local_port, remote_port, hostname,
		binding, revision, topology, owner_client_id, owner_user_id,
		ingress_location, ingress_type, target_location, target_client_id, target_type,
		transport_policy, desired_state, runtime_state, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"tunnel-p2p", "p2p-tunnel", peerA.ID, "tcp", "127.0.0.1", 8080, 18080, "",
		"client_id", 1, "server_expose", peerA.ID, owner.ID,
		"server", "tcp_listen", "client", peerA.ID, "tcp_service",
		"server_relay_only", "running", "active", now, now); err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}

	input := testWebhookInput("wh_p2p_scope")
	input.Events = []string{"p2p.connected"}
	input.TargetKind = WebhookTargetTunnel
	if _, err := webhookStore.Create(owner.ID, input); err != nil {
		t.Fatalf("create p2p webhook: %v", err)
	}

	// Mirror the production producer: p2pActivitySpec never sets ScopeUserID.
	spec := p2pActivitySpec(p2pLifecycleResult{
		Session: p2pSessionSnapshot{SessionID: "sess-prod", ClientA: peerA.ID, ClientB: peerB.ID},
	}, "connected", []p2pGrantSnapshot{{TunnelID: "tunnel-p2p"}}, "shared_session")
	activityStore := newActivityStoreWithDB("", adminStore.db, false)
	if _, err := activityStore.Append(spec); err != nil {
		t.Fatalf("append p2p activity without explicit scope: %v", err)
	}

	page, err := webhookStore.ListDeliveries(owner.ID, "wh_p2p_scope", "", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Origin != WebhookOriginEvent || page.Items[0].Event != "p2p.connected" {
		t.Fatalf("p2p deliveries = %+v, want one event delivery via inferred scope", page.Items)
	}
}

func TestValidateWebhookInputAllowsPublicTargetsWithDefaultPolicy(t *testing.T) {
	catalog := activityWebhookCatalog()
	publicURLs := []string{
		"https://93.184.216.34/hook",
		"http://8.8.8.8:8080/hook",
		"https://[2606:2800:220:1:248:1893:25c8:1946]/hook",
		"https://example.com/hook",
		"https://open.feishu.cn/open-apis/bot/v2/hook/test",
	}
	for _, rawURL := range publicURLs {
		input := normalizeWebhookInput(testWebhookInput("wh_public_policy"))
		input.URL = rawURL
		if err := validateWebhookInput(input, catalog.Fixtures, catalog.Variables, false); err != nil {
			t.Fatalf("default policy: public URL %q should pass, got %v", rawURL, err)
		}
	}
}

func TestURLHostnameNormalizesHostForms(t *testing.T) {
	cases := map[string]string{
		"open.feishu.cn":     "open.feishu.cn",
		"open.feishu.cn:443": "open.feishu.cn",
		"1.2.3.4":            "1.2.3.4",
		"1.2.3.4:8080":       "1.2.3.4",
		"[::1]":              "::1",
		"[2001:db8::1]:443":  "2001:db8::1",
	}
	for host, want := range cases {
		if got := urlHostname(host); got != want {
			t.Errorf("urlHostname(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestWebhookHostBlockedLiteralAddresses(t *testing.T) {
	blocked := []string{"192.168.1.5", "10.0.0.9", "127.0.0.1", "::1", "169.254.169.254"}
	for _, host := range blocked {
		if !webhookHostBlocked(host) {
			t.Errorf("webhookHostBlocked(%q) = false, want true", host)
		}
	}
	public := []string{"93.184.216.34", "8.8.8.8", "2606:2800:220:1:248:1893:25c8:1946"}
	for _, host := range public {
		if webhookHostBlocked(host) {
			t.Errorf("webhookHostBlocked(%q) = true, want false", host)
		}
	}
}

func TestWebhookStoreSetEnabledValidatesOnEnable(t *testing.T) {
	_, webhookStore, owner := newWebhookStoreFixture(t)
	allowPrivate := true
	webhookStore.settings = func() WebhookSettings {
		return policySettings(allowPrivate, defaultWebhookDailyDeliveryCap)
	}

	private := normalizeWebhookInput(testWebhookInput("wh_toggle_private"))
	private.URL = "http://192.168.1.10/hook"
	private.Enabled = false
	stored, err := webhookStore.Create(owner.ID, private)
	if err != nil {
		t.Fatalf("create private-url webhook with allow=true: %v", err)
	}

	allowPrivate = false
	if _, err := webhookStore.SetEnabled(owner.ID, stored.ID, true); err == nil {
		t.Fatal("enabling a private-URL webhook under the default policy should fail")
	} else {
		assertWebhookValidationError(t, err, "url", "url_target_not_allowed")
	}
	unchanged, err := webhookStore.Get(owner.ID, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Enabled || unchanged.Revision != stored.Revision {
		t.Fatalf("rejected enable must not change state: enabled=%v revision=%d", unchanged.Enabled, unchanged.Revision)
	}

	public := normalizeWebhookInput(testWebhookInput("wh_toggle_public"))
	public.URL = "https://93.184.216.34/hook"
	public.Enabled = false
	storedPublic, err := webhookStore.Create(owner.ID, public)
	if err != nil {
		t.Fatalf("create public-url webhook under the default policy: %v", err)
	}
	enabled, err := webhookStore.SetEnabled(owner.ID, storedPublic.ID, true)
	if err != nil {
		t.Fatalf("enable public-url webhook: %v", err)
	}
	if !enabled.Enabled || enabled.Revision != storedPublic.Revision+1 {
		t.Fatalf("enabled=%v revision=%d (want enabled, %d)", enabled.Enabled, enabled.Revision, storedPublic.Revision+1)
	}
	idempotent, err := webhookStore.SetEnabled(owner.ID, storedPublic.ID, true)
	if err != nil {
		t.Fatalf("idempotent enable: %v", err)
	}
	if idempotent.Revision != enabled.Revision {
		t.Fatalf("idempotent enable must not bump revision: %d vs %d", idempotent.Revision, enabled.Revision)
	}
	disabled, err := webhookStore.SetEnabled(owner.ID, storedPublic.ID, false)
	if err != nil {
		t.Fatalf("disable webhook: %v", err)
	}
	if disabled.Enabled || disabled.Revision != enabled.Revision+1 {
		t.Fatalf("disable result: enabled=%v revision=%d", disabled.Enabled, disabled.Revision)
	}
}

func TestWebhookEnqueueTestRejectsPrivateURLUnderDefaultPolicy(t *testing.T) {
	_, webhookStore, owner := newWebhookStoreFixture(t)
	webhookStore.settings = func() WebhookSettings {
		return policySettings(false, defaultWebhookDailyDeliveryCap)
	}
	input := normalizeWebhookInput(testWebhookInput("wh_test_private"))
	input.URL = "http://127.0.0.1:9000/hook"
	_, err := webhookStore.EnqueueTest(owner.ID, input, "client.online")
	if err == nil {
		t.Fatal("test delivery to a private URL must be rejected under the default policy")
	}
	assertWebhookValidationError(t, err, "url", "url_target_not_allowed")
}

func TestDisableWebhooksViolatingPrivatePolicy(t *testing.T) {
	_, webhookStore, owner := newWebhookStoreFixture(t)
	allowPrivate := true
	webhookStore.settings = func() WebhookSettings {
		return policySettings(allowPrivate, defaultWebhookDailyDeliveryCap)
	}

	private := normalizeWebhookInput(testWebhookInput("wh_sweep_private"))
	private.URL = "http://10.1.2.3/hook"
	private.Enabled = true
	if _, err := webhookStore.Create(owner.ID, private); err != nil {
		t.Fatalf("create enabled private webhook with allow=true: %v", err)
	}
	public := normalizeWebhookInput(testWebhookInput("wh_sweep_public"))
	public.URL = "https://93.184.216.34/hook"
	public.Enabled = true
	if _, err := webhookStore.Create(owner.ID, public); err != nil {
		t.Fatalf("create enabled public webhook: %v", err)
	}
	disabledDraft := normalizeWebhookInput(testWebhookInput("wh_sweep_off"))
	disabledDraft.URL = "http://10.1.2.4/hook"
	disabledDraft.Enabled = false
	if _, err := webhookStore.Create(owner.ID, disabledDraft); err != nil {
		t.Fatalf("create disabled private webhook: %v", err)
	}

	allowPrivate = false
	disabledCount, err := webhookStore.DisableWebhooksViolatingPrivatePolicy()
	if err != nil {
		t.Fatalf("disable policy violations: %v", err)
	}
	if disabledCount != 1 {
		t.Fatalf("disabled count = %d, want 1 (only the enabled private webhook)", disabledCount)
	}
	privateAfter, err := webhookStore.Get(owner.ID, "wh_sweep_private")
	if err != nil || privateAfter.Enabled {
		t.Fatalf("enabled private webhook should be disabled: %+v err=%v", privateAfter, err)
	}
	if privateAfter.Revision != 2 {
		t.Fatalf("disable should bump revision: %d", privateAfter.Revision)
	}
	publicAfter, err := webhookStore.Get(owner.ID, "wh_sweep_public")
	if err != nil || !publicAfter.Enabled {
		t.Fatalf("public webhook must stay enabled: %+v err=%v", publicAfter, err)
	}
	offAfter, err := webhookStore.Get(owner.ID, "wh_sweep_off")
	if err != nil || offAfter.Enabled || offAfter.Revision != 1 {
		t.Fatalf("already-disabled webhook must be untouched: %+v err=%v", offAfter, err)
	}
}
