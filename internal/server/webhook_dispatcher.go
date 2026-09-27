package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	webhookUserStartInterval = 2 * time.Second
	webhookRequestTimeout    = 10 * time.Second
	webhookDeliveryLease     = 30 * time.Second
	webhookDispatchPoll      = 200 * time.Millisecond
	webhookRecoveryPeriod    = time.Second
	webhookMaintenancePeriod = 6 * time.Hour
	webhookQueueLogPeriod    = time.Minute
	webhookRetryAfterMax     = 5 * time.Minute
	webhookDispatchBatch     = 32
)

type webhookDispatcher struct {
	store      *WebhookStore
	events     *EventBus
	client     *http.Client
	now        func() time.Time
	wake       chan struct{}
	stop       chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
	requestCtx context.Context
	cancel     context.CancelFunc
}

func newWebhookDispatcher(store *WebhookStore, events *EventBus) *webhookDispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &webhookDispatcher{
		store:  store,
		events: events,
		client: &http.Client{
			Timeout: webhookRequestTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: &webhookPolicyRoundTripper{
				inner: &http.Transport{DialContext: guardedWebhookDialContext(store)},
				store: store,
			},
		},
		now: time.Now, wake: make(chan struct{}, 1), stop: make(chan struct{}),
		requestCtx: ctx, cancel: cancel,
	}
}

// webhookPolicyRoundTripper closes pooled keep-alive connections whenever the
// private-target policy is (re-)disabled. Idle connection reuse bypasses
// DialContext, so without this a toggle from allow to block would keep serving
// already-established private connections.
type webhookPolicyRoundTripper struct {
	inner *http.Transport
	store *WebhookStore
}

func (r *webhookPolicyRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if r.store != nil && !r.store.currentSettings().AllowPrivateTargets {
		r.inner.CloseIdleConnections()
	}
	return r.inner.RoundTrip(request)
}

// guardedWebhookDialContext resolves the target before connecting and refuses
// non-public addresses while private targets are disallowed. Dialing the
// validated IP directly (instead of the hostname) closes the DNS-rebinding
// window between validation and connection.
func guardedWebhookDialContext(store *WebhookStore) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	resolver := &net.Resolver{}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if store == nil || store.currentSettings().AllowPrivateTargets {
			return dialer.DialContext(ctx, network, addr)
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		dialIPs := []net.IP{}
		if ip := net.ParseIP(host); ip != nil {
			dialIPs = append(dialIPs, ip)
		} else {
			resolved, err := resolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, item := range resolved {
				dialIPs = append(dialIPs, item.IP)
			}
		}
		if len(dialIPs) == 0 {
			return nil, fmt.Errorf("webhook target %q did not resolve to any address", host)
		}
		for _, ip := range dialIPs {
			if !webhookAddressAllowed(ip) {
				return nil, fmt.Errorf("webhook target %q resolves to a private address", host)
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(dialIPs[0].String(), port))
	}
}

func (d *webhookDispatcher) Start() {
	if d == nil || d.store == nil {
		return
	}
	d.wg.Add(1)
	go d.loop()
}

func (d *webhookDispatcher) Wake() {
	if d == nil {
		return
	}
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *webhookDispatcher) Stop(ctx context.Context) error {
	if d == nil {
		return nil
	}
	d.stopOnce.Do(func() { close(d.stop) })
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		d.cancel()
		select {
		case <-done:
		case <-time.After(250 * time.Millisecond):
		}
		return ctx.Err()
	}
}

func (d *webhookDispatcher) loop() {
	defer d.wg.Done()
	poll := time.NewTicker(webhookDispatchPoll)
	defer poll.Stop()
	recovery := time.NewTicker(webhookRecoveryPeriod)
	defer recovery.Stop()
	maintenance := time.NewTicker(webhookMaintenancePeriod)
	defer maintenance.Stop()
	queueLog := time.NewTicker(webhookQueueLogPeriod)
	defer queueLog.Stop()
	if err := d.store.RecoverInterrupted(d.now().UTC()); err != nil {
		log.Printf("Webhook delivery recovery failed: %v", err)
	}
	if _, err := d.store.Prune(d.now().UTC()); err != nil {
		log.Printf("Webhook delivery cleanup failed: %v", err)
	}
	for {
		select {
		case <-d.stop:
			return
		case <-d.wake:
			d.dispatchDue()
		case <-poll.C:
			d.dispatchDue()
		case <-recovery.C:
			if err := d.store.RecoverInterrupted(d.now().UTC()); err != nil {
				log.Printf("Webhook delivery recovery failed: %v", err)
			}
		case <-queueLog.C:
			d.logQueueStats()
		case <-maintenance.C:
			if err := d.store.RecoverInterrupted(d.now().UTC()); err != nil {
				log.Printf("Webhook delivery recovery failed: %v", err)
			}
			if deleted, err := d.store.Prune(d.now().UTC()); err != nil {
				log.Printf("Webhook delivery cleanup failed: %v", err)
			} else if deleted > 0 {
				log.Printf("Webhook delivery cleanup removed %d historical records", deleted)
			}
		}
	}
}

func (d *webhookDispatcher) logQueueStats() {
	count, oldestWait, err := d.store.QueueStats(d.now().UTC())
	if err != nil {
		log.Printf("Webhook delivery queue stats failed: %v", err)
		return
	}
	if count > 0 {
		log.Printf("Webhook delivery queue: pending=%d oldest_wait=%s", count, oldestWait.Round(time.Second))
	}
}

func (d *webhookDispatcher) dispatchDue() {
	owners, err := d.store.DueOwners(d.now().UTC(), webhookDispatchBatch)
	if err != nil {
		log.Printf("Webhook delivery queue scan failed: %v", err)
		return
	}
	for _, ownerUserID := range owners {
		claimed, ok, err := d.store.ClaimDue(ownerUserID, d.now().UTC())
		if err != nil {
			log.Printf("Webhook delivery claim failed [owner_user_id=%s]: %v", ownerUserID, err)
			continue
		}
		if !ok {
			continue
		}
		d.wg.Add(1)
		go func(delivery webhookStoredDelivery) {
			defer d.wg.Done()
			d.execute(delivery)
		}(claimed)
	}
}

func (d *webhookDispatcher) execute(delivery webhookStoredDelivery) {
	var body io.Reader
	if delivery.RequestBody != nil {
		body = bytes.NewBufferString(*delivery.RequestBody)
	}
	request, err := http.NewRequestWithContext(d.requestCtx, string(delivery.RequestMethod), delivery.RequestURL, body)
	if err == nil {
		for key, value := range delivery.RequestHeaders {
			request.Header.Set(key, value)
		}
	}
	startedAt := d.now().UTC()
	var response *http.Response
	if err == nil {
		response, err = d.client.Do(request)
	}
	result := webhookAttemptResult{Err: err}
	if response != nil {
		result.StatusCode = response.StatusCode
		result.Headers = captureWebhookHeaders(response.Header, webhookResponseBodyMaxBytes)
		body, truncated, readErr := readWebhookResponseBody(response.Body)
		_ = response.Body.Close()
		result.Body, result.BodyTruncated = body, truncated
		if err == nil && readErr != nil {
			err = fmt.Errorf("read response body: %w", readErr)
			result.Err = err
		}
	}
	completedAt := d.now().UTC()
	result.CompletedAt = completedAt
	result.Duration = completedAt.Sub(startedAt)
	if response != nil {
		result.RetryAfter, result.RetryAfterSet = parseWebhookRetryAfter(response.Header.Get("Retry-After"), completedAt)
	}
	status, err := d.store.CompleteAttempt(delivery, result)
	if err != nil {
		log.Printf("Webhook delivery completion failed [delivery_id=%s]: %v", delivery.ID, err)
		return
	}
	if d.events != nil {
		d.events.PublishScopedJSON("webhook_delivery_changed", delivery.OwnerUserID, map[string]any{
			"webhook_id": delivery.WebhookID, "delivery_id": delivery.ID, "status": status,
		})
	}
	if status == WebhookDeliveryRetrying {
		d.Wake()
	}
}

type webhookAttemptResult struct {
	CompletedAt   time.Time
	Duration      time.Duration
	StatusCode    int
	Headers       map[string]string
	Body          string
	BodyTruncated bool
	RetryAfter    time.Duration
	RetryAfterSet bool
	Err           error
}

func readWebhookResponseBody(body io.Reader) (string, bool, error) {
	if body == nil {
		return "", false, nil
	}
	raw, err := io.ReadAll(io.LimitReader(body, webhookResponseBodyMaxBytes+1))
	if err != nil {
		return "", false, err
	}
	if len(raw) > webhookResponseBodyMaxBytes {
		return string(raw[:webhookResponseBodyMaxBytes]), true, nil
	}
	return string(raw), false, nil
}

func captureWebhookHeaders(headers http.Header, maxBytes int) map[string]string {
	result := make(map[string]string)
	remaining := maxBytes
	for key, values := range headers {
		value := strings.Join(values, ", ")
		if len(key)+len(value) > remaining {
			break
		}
		result[key] = value
		remaining -= len(key) + len(value)
	}
	return result
}

func parseWebhookRetryAfter(raw string, now time.Time) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
		return min(time.Duration(seconds)*time.Second, webhookRetryAfterMax), true
	}
	if value, err := http.ParseTime(raw); err == nil {
		return min(max(value.Sub(now), 0), webhookRetryAfterMax), true
	}
	return 0, false
}

func webhookRetryable(statusCode int, err error) bool {
	if err != nil {
		return true
	}
	return statusCode == http.StatusRequestTimeout || statusCode == http.StatusTooManyRequests || statusCode >= 500
}

func webhookRetryDelay(attempt int, retryAfter time.Duration, retryAfterSet bool) time.Duration {
	if retryAfterSet {
		return min(retryAfter, webhookRetryAfterMax)
	}
	if attempt <= 1 {
		return 5 * time.Second
	}
	return 30 * time.Second
}

func (s *WebhookStore) DueOwners(now time.Time, limit int) ([]string, error) {
	canceled, err := s.cancelUndeliverableWaiting(now)
	if err != nil {
		return nil, err
	}
	if canceled > 0 {
		if _, err := s.Prune(now); err != nil {
			return nil, err
		}
	}
	rows, err := s.db.Query(`SELECT d.owner_user_id
		FROM activity_webhook_deliveries d
		LEFT JOIN activity_webhook_dispatch_slots slot ON slot.owner_user_id = d.owner_user_id
		WHERE d.status IN ('queued', 'retrying') AND d.next_attempt_at_ns <= ?
		AND d.lease_until_ns IS NULL
		AND COALESCE(slot.next_allowed_at_ns, 0) <= ?
		GROUP BY d.owner_user_id
		ORDER BY MIN(d.next_attempt_at_ns), MIN(d.created_at_ns), d.owner_user_id
		LIMIT ?`, now.UnixNano(), now.UnixNano(), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	owners := []string{}
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			return nil, err
		}
		owners = append(owners, owner)
	}
	return owners, rows.Err()
}

func (s *WebhookStore) QueueStats(now time.Time) (int64, time.Duration, error) {
	var count int64
	var oldestCreatedAt sql.NullInt64
	if err := s.db.QueryRow(`SELECT COUNT(*), MIN(created_at_ns)
		FROM activity_webhook_deliveries WHERE status IN ('queued', 'retrying')`).Scan(&count, &oldestCreatedAt); err != nil {
		return 0, 0, err
	}
	if !oldestCreatedAt.Valid {
		return count, 0, nil
	}
	return count, max(now.UTC().Sub(time.Unix(0, oldestCreatedAt.Int64)), 0), nil
}

func (s *WebhookStore) cancelUndeliverableWaiting(now time.Time) (int64, error) {
	result, err := s.db.Exec(`UPDATE activity_webhook_deliveries AS d
		SET status = 'canceled', error = 'Webhook or user is no longer available', completed_at_ns = ?, updated_at_ns = ?, lease_until_ns = NULL
		WHERE d.status IN ('queued', 'retrying') AND d.lease_until_ns IS NULL AND (
			NOT EXISTS (SELECT 1 FROM users u WHERE u.id = d.owner_user_id AND u.status = 'active')
			OR (d.origin = 'event' AND NOT EXISTS (
				SELECT 1 FROM activity_webhooks w WHERE w.owner_user_id = d.owner_user_id AND w.id = d.webhook_id AND w.enabled = 1
			))
			OR (d.origin = 'replay' AND NOT EXISTS (
				SELECT 1 FROM activity_webhooks w WHERE w.owner_user_id = d.owner_user_id AND w.id = d.webhook_id
			))
		)`, now.UnixNano(), now.UnixNano())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *WebhookStore) ClaimDue(ownerUserID string, now time.Time) (webhookStoredDelivery, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return webhookStoredDelivery{}, false, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	var nextAllowed int64
	err = tx.QueryRow(`SELECT next_allowed_at_ns FROM activity_webhook_dispatch_slots WHERE owner_user_id = ?`, ownerUserID).Scan(&nextAllowed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return webhookStoredDelivery{}, false, err
	}
	if nextAllowed > now.UnixNano() {
		if err := commitTx(tx, &committed); err != nil {
			return webhookStoredDelivery{}, false, err
		}
		return webhookStoredDelivery{}, false, nil
	}
	stored, err := scanWebhookDelivery(tx.QueryRow(webhookDeliverySelectQuery+`
		WHERE owner_user_id = ? AND status IN ('queued', 'retrying') AND next_attempt_at_ns <= ?
		AND lease_until_ns IS NULL
		ORDER BY next_attempt_at_ns, created_at_ns, id LIMIT 1`, ownerUserID, now.UnixNano()))
	if errors.Is(err, sql.ErrNoRows) {
		if err := commitTx(tx, &committed); err != nil {
			return webhookStoredDelivery{}, false, err
		}
		return webhookStoredDelivery{}, false, nil
	}
	if err != nil {
		return webhookStoredDelivery{}, false, err
	}
	attempt := stored.AttemptCount + 1
	rendered, err := renderWebhookRequest(stored.ConfigSnapshot, stored.ValuesSnapshot, attempt)
	if err != nil {
		return webhookStoredDelivery{}, false, err
	}
	headersJSON, err := json.Marshal(rendered.Headers)
	if err != nil {
		return webhookStoredDelivery{}, false, err
	}
	leaseUntil := now.Add(webhookDeliveryLease).UnixNano()
	result, err := tx.Exec(`UPDATE activity_webhook_deliveries SET attempt_count = ?, lease_until_ns = ?,
		request_method = ?, request_url = ?, request_headers_json = ?, request_body = ?,
		started_at_ns = COALESCE(started_at_ns, ?), updated_at_ns = ?
		WHERE id = ? AND attempt_count = ? AND status IN ('queued', 'retrying')`, attempt, leaseUntil,
		rendered.Method, rendered.URL, string(headersJSON), rendered.Body, now.UnixNano(), now.UnixNano(), stored.ID, stored.AttemptCount)
	if err != nil {
		return webhookStoredDelivery{}, false, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return webhookStoredDelivery{}, false, nil
	}
	if _, err := tx.Exec(`INSERT INTO activity_webhook_delivery_attempts
		(delivery_id, attempt_number, status, started_at_ns) VALUES (?, ?, 'pending', ?)`, stored.ID, attempt, now.UnixNano()); err != nil {
		return webhookStoredDelivery{}, false, err
	}
	if _, err := tx.Exec(`INSERT INTO activity_webhook_dispatch_slots (owner_user_id, next_allowed_at_ns)
		VALUES (?, ?) ON CONFLICT(owner_user_id) DO UPDATE SET next_allowed_at_ns = excluded.next_allowed_at_ns`,
		ownerUserID, now.Add(webhookUserStartInterval).UnixNano()); err != nil {
		return webhookStoredDelivery{}, false, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return webhookStoredDelivery{}, false, err
	}
	stored.AttemptCount = attempt
	stored.LeaseUntilNS = sql.NullInt64{Int64: leaseUntil, Valid: true}
	stored.RequestMethod, stored.RequestURL, stored.RequestHeaders, stored.RequestBody = rendered.Method, rendered.URL, rendered.Headers, rendered.Body
	return stored, true, nil
}

func (s *WebhookStore) CompleteAttempt(delivery webhookStoredDelivery, result webhookAttemptResult) (WebhookDeliveryStatus, error) {
	now := result.CompletedAt.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	headersJSON, _ := json.Marshal(result.Headers)
	errorMessage := webhookAttemptError(result)
	statusCodeValue := any(nil)
	if result.StatusCode > 0 {
		statusCodeValue = result.StatusCode
	}
	durationMS := max(result.Duration.Milliseconds(), 0)
	resultAttemptStatus := "failed"
	if result.Err == nil && result.StatusCode >= 200 && result.StatusCode < 300 {
		resultAttemptStatus = "success"
	}
	update, err := tx.Exec(`UPDATE activity_webhook_delivery_attempts SET status = ?, completed_at_ns = ?,
		duration_ms = ?, response_status = ?, response_headers_json = ?, response_body = ?, error = ?
		WHERE delivery_id = ? AND attempt_number = ? AND status = 'pending'`, resultAttemptStatus,
		now.UnixNano(), durationMS, statusCodeValue, string(headersJSON), result.Body, errorMessage, delivery.ID, delivery.AttemptCount)
	if err != nil {
		return "", err
	}
	changed, _ := update.RowsAffected()
	if changed != 1 {
		return "", errors.New("webhook delivery attempt is no longer pending")
	}
	finalStatus := WebhookDeliveryFailed
	nextAttemptAt := now
	terminal := true
	if resultAttemptStatus == "success" {
		finalStatus = WebhookDeliverySuccess
	} else if webhookRetryable(result.StatusCode, result.Err) && delivery.AttemptCount < delivery.MaxAttempts {
		allowed, err := webhookDeliveryCanContinueTx(tx, delivery)
		if err != nil {
			return "", err
		}
		if allowed {
			terminal = false
			finalStatus = WebhookDeliveryRetrying
			nextAttemptAt = now.Add(webhookRetryDelay(delivery.AttemptCount, result.RetryAfter, result.RetryAfterSet))
		} else {
			finalStatus = WebhookDeliveryCanceled
			errorMessage = "Webhook or user is no longer available"
		}
	}
	completedAt := any(nil)
	if terminal {
		completedAt = now.UnixNano()
	}
	_, err = tx.Exec(`UPDATE activity_webhook_deliveries SET status = ?, next_attempt_at_ns = ?, lease_until_ns = NULL,
		response_status = ?, response_headers_json = ?, response_body = ?, error = ?, duration_ms = ?,
		completed_at_ns = ?, updated_at_ns = ? WHERE id = ?`, finalStatus, nextAttemptAt.UnixNano(), statusCodeValue,
		string(headersJSON), result.Body, errorMessage, durationMS, completedAt, now.UnixNano(), delivery.ID)
	if err != nil {
		return "", err
	}
	if delivery.Origin != WebhookOriginTest {
		if err := updateWebhookHealthTx(tx, delivery, finalStatus, now); err != nil {
			return "", err
		}
	}
	if terminal {
		if err := pruneWebhookHistoryTx(tx, delivery.OwnerUserID, delivery.WebhookID, now); err != nil {
			return "", err
		}
	}
	if err := commitTx(tx, &committed); err != nil {
		return "", err
	}
	return finalStatus, nil
}

func webhookAttemptError(result webhookAttemptResult) string {
	if result.Err != nil {
		return result.Err.Error()
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return fmt.Sprintf("HTTP %d", result.StatusCode)
	}
	return ""
}

func webhookDeliveryCanContinueTx(tx *sql.Tx, delivery webhookStoredDelivery) (bool, error) {
	var active int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ? AND status = 'active'`, delivery.OwnerUserID).Scan(&active); err != nil {
		return false, err
	}
	if active == 0 {
		return false, nil
	}
	if delivery.Origin == WebhookOriginTest {
		return true, nil
	}
	query := `SELECT COUNT(*) FROM activity_webhooks WHERE owner_user_id = ? AND id = ?`
	if delivery.Origin == WebhookOriginEvent {
		query += ` AND enabled = 1`
	}
	var exists int
	if err := tx.QueryRow(query, delivery.OwnerUserID, delivery.WebhookID).Scan(&exists); err != nil {
		return false, err
	}
	return exists > 0, nil
}

func updateWebhookHealthTx(tx *sql.Tx, delivery webhookStoredDelivery, status WebhookDeliveryStatus, now time.Time) error {
	switch status {
	case WebhookDeliveryRetrying:
		_, err := tx.Exec(`UPDATE activity_webhooks SET last_status = 'retrying', last_called_at_ns = ?
			WHERE owner_user_id = ? AND id = ?`, now.UnixNano(), delivery.OwnerUserID, delivery.WebhookID)
		return err
	case WebhookDeliverySuccess:
		_, err := tx.Exec(`UPDATE activity_webhooks SET last_status = 'success', consecutive_failures = 0, last_called_at_ns = ?
			WHERE owner_user_id = ? AND id = ?`, now.UnixNano(), delivery.OwnerUserID, delivery.WebhookID)
		return err
	case WebhookDeliveryFailed:
		_, err := tx.Exec(`UPDATE activity_webhooks SET last_status = 'failed', consecutive_failures = consecutive_failures + 1, last_called_at_ns = ?
			WHERE owner_user_id = ? AND id = ?`, now.UnixNano(), delivery.OwnerUserID, delivery.WebhookID)
		return err
	default:
		return nil
	}
}

func (s *WebhookStore) RecoverInterrupted(now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	type terminalRecovery struct {
		ownerUserID string
		webhookID   string
		origin      WebhookDeliveryOrigin
	}
	rows, err := tx.Query(`SELECT owner_user_id, webhook_id, origin
		FROM activity_webhook_deliveries
		WHERE status IN ('queued', 'retrying') AND lease_until_ns IS NOT NULL AND lease_until_ns <= ?
		AND attempt_count >= max_attempts`, now.UnixNano())
	if err != nil {
		return err
	}
	terminal := []terminalRecovery{}
	for rows.Next() {
		var item terminalRecovery
		if err := rows.Scan(&item.ownerUserID, &item.webhookID, &item.origin); err != nil {
			_ = rows.Close()
			return err
		}
		terminal = append(terminal, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE activity_webhook_delivery_attempts SET status = 'failed', completed_at_ns = ?,
		duration_ms = MAX(0, (? - started_at_ns) / 1000000), error = 'server interrupted the request'
		WHERE status = 'pending' AND delivery_id IN (
			SELECT id FROM activity_webhook_deliveries WHERE lease_until_ns IS NOT NULL AND lease_until_ns <= ?
		)`, now.UnixNano(), now.UnixNano(), now.UnixNano()); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE activity_webhook_deliveries SET
		status = CASE WHEN attempt_count < max_attempts THEN 'retrying' ELSE 'failed' END,
		next_attempt_at_ns = ?, lease_until_ns = NULL,
		completed_at_ns = CASE WHEN attempt_count >= max_attempts THEN ? ELSE NULL END,
		error = 'server interrupted the request', updated_at_ns = ?
		WHERE status IN ('queued', 'retrying') AND lease_until_ns IS NOT NULL AND lease_until_ns <= ?`,
		now.UnixNano(), now.UnixNano(), now.UnixNano(), now.UnixNano()); err != nil {
		return err
	}
	for _, item := range terminal {
		delivery := webhookStoredDelivery{
			WebhookDelivery: WebhookDelivery{WebhookID: item.webhookID, Origin: item.origin},
			OwnerUserID:     item.ownerUserID,
		}
		if item.origin != WebhookOriginTest {
			if err := updateWebhookHealthTx(tx, delivery, WebhookDeliveryFailed, now); err != nil {
				return err
			}
		}
		if err := pruneWebhookHistoryTx(tx, item.ownerUserID, item.webhookID, now); err != nil {
			return err
		}
	}
	return commitTx(tx, &committed)
}
