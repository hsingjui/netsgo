package server

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"netsgo/pkg/protocol"

	"golang.org/x/crypto/bcrypt"
)

func (s *AdminStore) appendActivityTx(tx *sql.Tx, spec ActivityEventSpec) (int64, error) {
	if s.activityStore == nil {
		return 0, nil
	}
	return s.activityStore.appendTx(tx, spec)
}

func adminActivitySpec(action string, actor ActivityActor, args ActivitySummaryArgs) ActivityEventSpec {
	if actor.Type == "" {
		actor = systemActivityActor()
	}
	return ActivityEventSpec{
		OccurredAt: time.Now().UTC(),
		Category:   ActivityCategoryAdmin,
		Action:     action,
		Source:     "server",
		Actor:      actor,
		Payload:    newActivityPayload(ActivityCategoryAdmin, action, args),
	}
}

func clientManagementActivitySpec(action string, actor ActivityActor, client RegisteredClient, before, after string) ActivityEventSpec {
	args := ActivitySummaryArgs{ClientName: activityClientDisplayName(client), Before: before, After: after}
	payload := newActivityTransitionPayload(ActivityCategoryClient, action, args, before, after)
	return ActivityEventSpec{
		OccurredAt:    time.Now().UTC(),
		Category:      ActivityCategoryClient,
		Action:        action,
		Source:        "server",
		Actor:         actor,
		ScopeUserID:   client.OwnerUserID,
		SubjectUserID: client.OwnerUserID,
		Payload:       payload,
		Clients: []ActivityClientSubject{{
			ClientID:    client.ID,
			Relation:    "subject",
			DisplayName: client.DisplayName,
			Hostname:    client.Info.Hostname,
		}},
	}
}

func activityClientDisplayName(client RegisteredClient) string {
	if strings.TrimSpace(client.DisplayName) != "" {
		return client.DisplayName
	}
	if strings.TrimSpace(client.Info.Hostname) != "" {
		return client.Info.Hostname
	}
	return client.ID
}

func (s *AdminStore) UpdateServerConfigWithActivity(config ServerConfig, actor ActivityActor) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)

	before := ServerConfig{ActivityRetention: DefaultActivityRetentionPolicy()}
	if err := tx.QueryRow(`SELECT server_addr,
		activity_debug_retention_days, activity_debug_min_count,
		activity_info_retention_days, activity_info_min_count,
		activity_warning_retention_days, activity_warning_min_count,
		activity_error_retention_days, activity_error_min_count
		FROM server_config WHERE id = 1`).Scan(&before.ServerAddr,
		&before.ActivityRetention.Debug.Days, &before.ActivityRetention.Debug.MinCount,
		&before.ActivityRetention.Info.Days, &before.ActivityRetention.Info.MinCount,
		&before.ActivityRetention.Warning.Days, &before.ActivityRetention.Warning.MinCount,
		&before.ActivityRetention.Error.Days, &before.ActivityRetention.Error.MinCount); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	before.AllowedPorts, err = loadAllowedPorts(tx)
	if err != nil {
		return 0, err
	}
	if config.ActivityRetention == (ActivityRetentionPolicy{}) {
		config.ActivityRetention = before.ActivityRetention
	}
	if err := config.ActivityRetention.validate(); err != nil {
		return 0, err
	}
	if before.ServerAddr == config.ServerAddr && reflect.DeepEqual(before.AllowedPorts, config.AllowedPorts) && before.ActivityRetention == config.ActivityRetention {
		if err := commitTx(tx, &committed); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO server_config (id) VALUES (1)`); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE server_config SET server_addr = ?,
		activity_debug_retention_days = ?, activity_debug_min_count = ?,
		activity_info_retention_days = ?, activity_info_min_count = ?,
		activity_warning_retention_days = ?, activity_warning_min_count = ?,
		activity_error_retention_days = ?, activity_error_min_count = ? WHERE id = 1`, config.ServerAddr,
		config.ActivityRetention.Debug.Days, config.ActivityRetention.Debug.MinCount,
		config.ActivityRetention.Info.Days, config.ActivityRetention.Info.MinCount,
		config.ActivityRetention.Warning.Days, config.ActivityRetention.Warning.MinCount,
		config.ActivityRetention.Error.Days, config.ActivityRetention.Error.MinCount); err != nil {
		return 0, err
	}
	if err := replaceAllowedPorts(tx, config.AllowedPorts); err != nil {
		return 0, err
	}
	if err := s.maybeFailSave(); err != nil {
		return 0, err
	}
	activityID, err := s.appendActivityTx(tx, adminActivitySpec("server_config_changed", actor, ActivitySummaryArgs{}))
	if err != nil {
		return 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return 0, err
	}
	return activityID, nil
}

func (s *AdminStore) UpdateClientAuthRateLimitSettingsWithActivity(settings ClientAuthRateLimitSettings, actor ActivityActor) (int64, error) {
	if err := validateClientAuthRateLimitSettings(settings); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	var currentEnabled int
	var currentRate int
	if err := tx.QueryRow(`SELECT client_auth_rate_limit_enabled, client_auth_rate_limit_per_minute FROM server_config WHERE id = 1`).Scan(&currentEnabled, &currentRate); err != nil {
		return 0, err
	}
	if intToBool(currentEnabled) == settings.Enabled && currentRate == settings.RequestsPerMinute {
		if err := commitTx(tx, &committed); err != nil {
			return 0, err
		}
		return 0, nil
	}
	result, err := tx.Exec(`UPDATE server_config SET client_auth_rate_limit_enabled = ?, client_auth_rate_limit_per_minute = ? WHERE id = 1`, boolToInt(settings.Enabled), settings.RequestsPerMinute)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, fmt.Errorf("server config not initialized")
	}
	if err := s.maybeFailSave(); err != nil {
		return 0, err
	}
	activityID, err := s.appendActivityTx(tx, adminActivitySpec("client_auth_rate_limit_changed", actor, ActivitySummaryArgs{Value: int64(settings.RequestsPerMinute)}))
	if err != nil {
		return 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return 0, err
	}
	return activityID, nil
}

func (s *AdminStore) UpdateWebhookSettingsWithActivity(settings WebhookSettings, actor ActivityActor) (int64, error) {
	if err := validateWebhookSettings(settings); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	var currentAllow int
	var currentCap int
	if err := tx.QueryRow(`SELECT webhook_allow_private_targets, webhook_daily_delivery_cap FROM server_config WHERE id = 1`).Scan(&currentAllow, &currentCap); err != nil {
		return 0, err
	}
	if intToBool(currentAllow) == settings.AllowPrivateTargets && currentCap == settings.DailyDeliveryCap {
		if err := commitTx(tx, &committed); err != nil {
			return 0, err
		}
		return 0, nil
	}
	result, err := tx.Exec(`UPDATE server_config
		SET webhook_allow_private_targets = ?, webhook_daily_delivery_cap = ?
		WHERE id = 1`, boolToInt(settings.AllowPrivateTargets), settings.DailyDeliveryCap)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, fmt.Errorf("server config not initialized")
	}
	if err := s.maybeFailSave(); err != nil {
		return 0, err
	}
	activityID, err := s.appendActivityTx(tx, adminActivitySpec("webhook_policy_changed", actor, ActivitySummaryArgs{
		Value: int64(settings.DailyDeliveryCap),
	}))
	if err != nil {
		return 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return 0, err
	}
	return activityID, nil
}

func (s *AdminStore) UpdateClientDisplayNameWithActivity(clientID, displayName string, actor ActivityActor) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	client, err := loadRegisteredClient(tx, `WHERE id = ?`, clientID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrRegisteredClientNotFound
	}
	if err != nil {
		return 0, err
	}
	if client.DisplayName == displayName {
		if err := commitTx(tx, &committed); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if _, err := tx.Exec(`UPDATE registered_clients SET display_name = ? WHERE id = ?`, displayName, clientID); err != nil {
		return 0, err
	}
	if err := s.maybeFailSave(); err != nil {
		return 0, err
	}
	activityID, err := s.appendActivityTx(tx, clientManagementActivitySpec("display_name_changed", actor, client, client.DisplayName, displayName))
	if err != nil {
		return 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return 0, err
	}
	return activityID, nil
}

func (s *AdminStore) UpdateClientBandwidthSettingsWithActivity(clientID string, settings protocol.BandwidthSettings, actor ActivityActor) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	client, err := loadRegisteredClient(tx, `WHERE id = ?`, clientID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrRegisteredClientNotFound
	}
	if err != nil {
		return 0, err
	}
	if client.IngressBPS == settings.IngressBPS && client.EgressBPS == settings.EgressBPS {
		if err := commitTx(tx, &committed); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if _, err := tx.Exec(`UPDATE registered_clients SET ingress_bps = ?, egress_bps = ? WHERE id = ?`, settings.IngressBPS, settings.EgressBPS, clientID); err != nil {
		return 0, err
	}
	if err := s.maybeFailSave(); err != nil {
		return 0, err
	}
	before := fmt.Sprintf("%d/%d", client.IngressBPS, client.EgressBPS)
	after := fmt.Sprintf("%d/%d", settings.IngressBPS, settings.EgressBPS)
	activityID, err := s.appendActivityTx(tx, clientManagementActivitySpec("bandwidth_changed", actor, client, before, after))
	if err != nil {
		return 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return 0, err
	}
	return activityID, nil
}

func (s *AdminStore) CreateSessionWithActivity(userID, username, role, ip, ua string, actor ActivityActor) (*AdminSession, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionID, err := generateUUIDE()
	if err != nil {
		return nil, 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	session, currentUser, err := createSessionForActiveUserTx(tx, sessionID, userID, ip, ua, time.Now().UTC())
	if err != nil {
		return nil, 0, err
	}
	actor.ID = currentUser.ID
	actor.Name = currentUser.Username
	actor.Type = currentUser.Role
	if err := s.maybeFailSave(); err != nil {
		return nil, 0, err
	}
	activitySpec := adminActivitySpec("admin_login_succeeded", actor, ActivitySummaryArgs{})
	activitySpec.SubjectUserID = currentUser.ID
	if !currentUser.IsAdmin {
		activitySpec.ScopeUserID = currentUser.ID
	}
	activityID, err := s.appendActivityTx(tx, activitySpec)
	if err != nil {
		return nil, 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return nil, 0, err
	}
	return session, activityID, nil
}

func (s *AdminStore) DeleteSessionWithActivity(sessionID string, actor ActivityActor) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	result, err := tx.Exec(`DELETE FROM user_sessions WHERE id = ?`, sessionID)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, sql.ErrNoRows
	}
	if err := s.maybeFailSave(); err != nil {
		return 0, err
	}
	activityID, err := s.appendActivityTx(tx, adminActivitySpec("admin_logout", actor, ActivitySummaryArgs{}))
	if err != nil {
		return 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return 0, err
	}
	return activityID, nil
}

func (s *AdminStore) AddAPIKeyWithActivity(name, keyString string, permissions []string, expiresAt *time.Time, maxUses int, actor ActivityActor) (*APIKey, int64, error) {
	if maxUses < 0 {
		return nil, 0, fmt.Errorf("max_uses must not be negative")
	}
	permissions, err := normalizeKeyPermissions(permissions)
	if err != nil {
		return nil, 0, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(keyString), s.bcryptCost)
	if err != nil {
		return nil, 0, err
	}
	keyID, err := generateUUIDE()
	if err != nil {
		return nil, 0, err
	}
	key := APIKey{ID: keyID, Name: name, KeyHash: string(hash), LookupDigest: apiKeyLookupDigest(keyString), Permissions: permissions, CreatedAt: time.Now(), ExpiresAt: expiresAt, IsActive: true, MaxUses: maxUses}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	// This legacy helper is retained for internal callers that predate an
	// explicit resource scope. Assign it deterministically instead of emitting
	// an ownerless API key into the unified schema.
	ownerUserID, err := legacyOwnerUserIDInTx(tx)
	if err != nil {
		return nil, 0, err
	}
	key.OwnerUserID = ownerUserID
	if err := insertAPIKey(tx, key); err != nil {
		return nil, 0, err
	}
	if err := s.maybeFailSave(); err != nil {
		return nil, 0, err
	}
	activityID, err := s.appendActivityTx(tx, adminActivitySpec("api_key_created", actor, ActivitySummaryArgs{ResourceName: name, Value: int64(maxUses)}))
	if err != nil {
		return nil, 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return nil, 0, err
	}
	return &key, activityID, nil
}

// AddAPIKeyForUserWithActivity creates a key inside a server-selected user
// scope.  It intentionally accepts the owner separately from the activity
// actor: an administrator may manage a target user's keys without acquiring
// ownership of them.
func (s *AdminStore) AddAPIKeyForUserWithActivity(ownerUserID, name, keyString string, permissions []string, expiresAt *time.Time, maxUses int, actor ActivityActor) (*APIKey, int64, error) {
	if ownerUserID == "" {
		return nil, 0, fmt.Errorf("api key owner user id must not be empty")
	}
	if maxUses < 0 {
		return nil, 0, fmt.Errorf("max_uses must not be negative")
	}
	key, err := s.newAPIKey(name, keyString, permissions, expiresAt)
	if err != nil {
		return nil, 0, err
	}
	key.OwnerUserID = ownerUserID
	key.MaxUses = maxUses

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	if err := ensureOperationalUserInTx(tx, ownerUserID); err != nil {
		return nil, 0, err
	}
	if err := insertAPIKey(tx, key); err != nil {
		return nil, 0, err
	}
	if err := s.maybeFailSave(); err != nil {
		return nil, 0, err
	}
	activitySpec := adminActivitySpec("api_key_created", actor, ActivitySummaryArgs{ResourceName: name, Value: int64(maxUses)})
	activitySpec.ScopeUserID = ownerUserID
	activitySpec.SubjectUserID = ownerUserID
	activityID, err := s.appendActivityTx(tx, activitySpec)
	if err != nil {
		return nil, 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return nil, 0, err
	}
	return &key, activityID, nil
}

func (s *AdminStore) SetAPIKeyActiveWithActivity(id string, active bool, actor ActivityActor) (int64, error) {
	action := "api_key_disabled"
	if active {
		action = "api_key_enabled"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	var name string
	var current int
	if err := tx.QueryRow(`SELECT name, is_active FROM api_keys WHERE id = ?`, id).Scan(&name, &current); err != nil {
		return 0, err
	}
	if intToBool(current) == active {
		if err := commitTx(tx, &committed); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if _, err := tx.Exec(`UPDATE api_keys SET is_active = ? WHERE id = ?`, boolToInt(active), id); err != nil {
		return 0, err
	}
	if err := s.maybeFailSave(); err != nil {
		return 0, err
	}
	activityID, err := s.appendActivityTx(tx, adminActivitySpec(action, actor, ActivitySummaryArgs{ResourceName: name}))
	if err != nil {
		return 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return 0, err
	}
	return activityID, nil
}

// SetAPIKeyActiveForUserWithActivity modifies only a key owned by ownerUserID.
// A missing or cross-owner key is indistinguishable to callers.
func (s *AdminStore) SetAPIKeyActiveForUserWithActivity(ownerUserID, id string, active bool, actor ActivityActor) (int64, error) {
	action := "api_key_disabled"
	if active {
		action = "api_key_enabled"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	var name string
	var current int
	if err := tx.QueryRow(`SELECT name, is_active FROM api_keys WHERE id = ? AND owner_user_id = ?`, id, ownerUserID).Scan(&name, &current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrAPIKeyNotFound
		}
		return 0, err
	}
	if intToBool(current) == active {
		if err := commitTx(tx, &committed); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if _, err := tx.Exec(`UPDATE api_keys SET is_active = ? WHERE id = ? AND owner_user_id = ?`, boolToInt(active), id, ownerUserID); err != nil {
		return 0, err
	}
	if err := s.maybeFailSave(); err != nil {
		return 0, err
	}
	activitySpec := adminActivitySpec(action, actor, ActivitySummaryArgs{ResourceName: name})
	activitySpec.ScopeUserID = ownerUserID
	activitySpec.SubjectUserID = ownerUserID
	activityID, err := s.appendActivityTx(tx, activitySpec)
	if err != nil {
		return 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return 0, err
	}
	return activityID, nil
}

func (s *AdminStore) DeleteAPIKeyWithActivity(id string, actor ActivityActor) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	var name string
	if err := tx.QueryRow(`SELECT name FROM api_keys WHERE id = ?`, id).Scan(&name); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM api_keys WHERE id = ?`, id); err != nil {
		return 0, err
	}
	if err := s.maybeFailSave(); err != nil {
		return 0, err
	}
	activityID, err := s.appendActivityTx(tx, adminActivitySpec("api_key_deleted", actor, ActivitySummaryArgs{ResourceName: name}))
	if err != nil {
		return 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return 0, err
	}
	return activityID, nil
}

// DeleteAPIKeyForUserWithActivity deletes only a key owned by ownerUserID.
func (s *AdminStore) DeleteAPIKeyForUserWithActivity(ownerUserID, id string, actor ActivityActor) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	var name string
	if err := tx.QueryRow(`SELECT name FROM api_keys WHERE id = ? AND owner_user_id = ?`, id, ownerUserID).Scan(&name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrAPIKeyNotFound
		}
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM api_keys WHERE id = ? AND owner_user_id = ?`, id, ownerUserID); err != nil {
		return 0, err
	}
	if err := s.maybeFailSave(); err != nil {
		return 0, err
	}
	activitySpec := adminActivitySpec("api_key_deleted", actor, ActivitySummaryArgs{ResourceName: name})
	activitySpec.ScopeUserID = ownerUserID
	activitySpec.SubjectUserID = ownerUserID
	activityID, err := s.appendActivityTx(tx, activitySpec)
	if err != nil {
		return 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return 0, err
	}
	return activityID, nil
}
