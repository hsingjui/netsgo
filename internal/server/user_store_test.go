package server

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func TestDecodeUserListCursorReturnsTypedErrors(t *testing.T) {
	encode := func(payload string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(payload))
	}
	for _, raw := range []string{
		"%",
		encode("{"),
		encode(`{}`),
		encode(`{"created_at":"not-a-time","id":"user-1"}`),
	} {
		if _, err := decodeUserListCursor(raw); !errors.Is(err, ErrInvalidUserCursor) {
			t.Fatalf("decodeUserListCursor(%q) error = %v, want ErrInvalidUserCursor", raw, err)
		}
	}
}

func TestCreateSessionRechecksActiveUserAndLatestIdentity(t *testing.T) {
	store := newInitializedAdminStore(t)
	admin, err := store.GetSingleAdminUser()
	if err != nil {
		t.Fatalf("load administrator: %v", err)
	}
	user, err := store.CreateUser("session-race-user", "Password123")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	challengeKinds := []string{
		adminAuthChallengeKindMFA,
		adminAuthChallengeKindPasskeyLogin,
		adminAuthChallengeKindTOTPSetup,
		adminAuthChallengeKindPasskeyRegister,
	}
	challengeIDs := make([]string, 0, len(challengeKinds))
	for _, kind := range challengeKinds {
		challenge, err := store.StoreAuthChallenge(user.ID, kind, "{}", nil, time.Minute)
		if err != nil {
			t.Fatalf("store %s challenge: %v", kind, err)
		}
		challengeIDs = append(challengeIDs, challenge.ID)
	}
	if _, changed, err := store.SetUserStatus(admin.ID, user.ID, UserStatusDisabled); err != nil || !changed {
		t.Fatalf("disable user = (changed %v, err %v)", changed, err)
	}
	for index, challengeID := range challengeIDs {
		if _, err := store.GetAuthChallenge(challengeID, challengeKinds[index]); err == nil {
			t.Fatalf("%s challenge survived disable", challengeKinds[index])
		}
	}
	if _, err := store.CreateSession(user.ID, "stale-name", "admin", "127.0.0.1", "test"); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("session creation for disabled user error = %v, want ErrUserDisabled", err)
	}

	retryChallenge, err := store.StoreAuthChallenge(user.ID, adminAuthChallengeKindMFA, "{}", nil, time.Minute)
	if err != nil {
		t.Fatalf("store challenge before repeated disable: %v", err)
	}
	if _, changed, err := store.SetUserStatus(admin.ID, user.ID, UserStatusDisabled); err != nil || changed {
		t.Fatalf("repeat disable = (changed %v, err %v), want false, nil", changed, err)
	}
	if _, err := store.GetAuthChallenge(retryChallenge.ID, adminAuthChallengeKindMFA); err == nil {
		t.Fatal("repeated disable must clear authentication challenges")
	}

	if _, _, err := store.SetUserStatus(admin.ID, user.ID, UserStatusActive); err != nil {
		t.Fatalf("enable user: %v", err)
	}
	if _, err := store.UpdateUserUsername(user.ID, "current-name"); err != nil {
		t.Fatalf("rename user: %v", err)
	}
	if _, _, err := store.SetUserAdmin(admin.ID, user.ID, true); err != nil {
		t.Fatalf("promote user: %v", err)
	}
	session, err := store.CreateSession(user.ID, "stale-name", "user", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create session after enable: %v", err)
	}
	if session.Username != "current-name" || session.Role != "admin" {
		t.Fatalf("session identity = %q/%q, want current-name/admin", session.Username, session.Role)
	}
}

func TestAdminStoreUserLifecycleContract(t *testing.T) {
	store := newInitializedAdminStore(t)
	admin, err := store.GetSingleAdminUser()
	if err != nil {
		t.Fatalf("load initialized administrator: %v", err)
	}
	if _, err := store.CreateUser("   ", "Password123"); !errors.Is(err, ErrInvalidUsername) {
		t.Fatalf("blank username error = %v, want ErrInvalidUsername", err)
	}
	if _, err := store.CreateUser("weak-password", "short"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("weak create password error = %v, want ErrInvalidPassword", err)
	}

	user, err := store.CreateUser("alice", "Alice1234")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if user.ID == "" || user.Username != "alice" || user.IsAdmin || user.Status != UserStatusActive || user.Role != "user" {
		t.Fatalf("created user = %+v, want active non-administrator alice", user)
	}
	if _, err := store.CreateUser("alice", "Alice1234"); !errors.Is(err, ErrUserAlreadyExists) {
		t.Fatalf("duplicate user error = %v, want ErrUserAlreadyExists", err)
	}
	if _, err := store.UpdateUserUsername(user.ID, " "); !errors.Is(err, ErrInvalidUsername) {
		t.Fatalf("blank updated username error = %v, want ErrInvalidUsername", err)
	}
	if _, err := store.ResetUserPassword(user.ID, "short"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("weak reset password error = %v, want ErrInvalidPassword", err)
	}

	userSession := mustCreateSession(t, store, user.ID, user.Username, user.Role, "127.0.0.1", "user-agent")
	promoted, changed, err := store.SetUserAdmin(admin.ID, user.ID, true)
	if err != nil {
		t.Fatalf("promote user: %v", err)
	}
	if !changed || !promoted.IsAdmin || promoted.Role != "admin" {
		t.Fatalf("promote result = (%+v, %v), want changed administrator", promoted, changed)
	}
	if store.GetSession(userSession.ID) != nil {
		t.Fatal("administrator promotion must revoke the target user's web session")
	}

	adminSession := mustCreateSession(t, store, user.ID, user.Username, "admin", "127.0.0.1", "user-agent")
	demoted, changed, err := store.SetUserAdmin(admin.ID, user.ID, false)
	if err != nil {
		t.Fatalf("demote user: %v", err)
	}
	if !changed || demoted.IsAdmin || demoted.Role != "user" {
		t.Fatalf("demote result = (%+v, %v), want changed non-administrator", demoted, changed)
	}
	if store.GetSession(adminSession.ID) != nil {
		t.Fatal("administrator demotion must revoke the target user's web session")
	}

	if _, _, err := store.SetUserAdmin(admin.ID, admin.ID, false); !errors.Is(err, ErrLastOperationalAdmin) {
		t.Fatalf("demote final active administrator error = %v, want ErrLastOperationalAdmin", err)
	}
	if _, _, err := store.SetUserStatus(admin.ID, admin.ID, UserStatusDisabled); !errors.Is(err, ErrSelfUserLifecycleMutation) {
		t.Fatalf("self disable error = %v, want ErrSelfUserLifecycleMutation", err)
	}

	userSession = mustCreateSession(t, store, user.ID, user.Username, user.Role, "127.0.0.1", "user-agent")
	disabled, changed, err := store.SetUserStatus(admin.ID, user.ID, UserStatusDisabled)
	if err != nil {
		t.Fatalf("disable user: %v", err)
	}
	if !changed || disabled.Status != UserStatusDisabled {
		t.Fatalf("disable result = (%+v, %v), want changed disabled user", disabled, changed)
	}
	if store.GetSession(userSession.ID) != nil {
		t.Fatal("disabling a user must revoke the target user's web session")
	}
	if _, err := store.ValidateUserPassword(user.Username, "Alice1234"); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("disabled user password validation error = %v, want ErrUserDisabled", err)
	}
	now := time.Now().UTC()
	if _, err := store.db.Exec(`INSERT INTO user_sessions (id, user_id, created_at, expires_at, ip, user_agent)
		VALUES (?, ?, ?, ?, ?, ?)`, generateUUID(), user.ID, formatTime(now), formatTime(now.Add(time.Hour)), "127.0.0.1", "late-user-agent"); err != nil {
		t.Fatalf("seed late session for repeated-disable cleanup: %v", err)
	}
	if _, changed, err := store.SetUserStatus(admin.ID, user.ID, UserStatusDisabled); err != nil || changed {
		t.Fatalf("idempotent disable = (_, %v, %v), want (_, false, nil)", changed, err)
	}
	var remainingSessions int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM user_sessions WHERE user_id = ?`, user.ID).Scan(&remainingSessions); err != nil {
		t.Fatalf("count sessions after repeated disable: %v", err)
	}
	if remainingSessions != 0 {
		t.Fatalf("repeated disable left %d web session(s), want 0", remainingSessions)
	}

	if err := store.DeleteDisabledUser(admin.ID, user.ID); err != nil {
		t.Fatalf("delete disabled user: %v", err)
	}
	if _, err := store.GetUser(user.ID); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("load deleted user error = %v, want ErrUserNotFound", err)
	}
}

func TestAdminStoreUserListPaginationAndLiteralQuery(t *testing.T) {
	store := newInitializedAdminStore(t)
	for _, username := range []string{"literal%name", "literalXname", "zoe"} {
		if _, err := store.CreateUser(username, "Password123"); err != nil {
			t.Fatalf("create %q: %v", username, err)
		}
	}

	first, err := store.ListUsers(UserListOptions{Limit: 1})
	if err != nil {
		t.Fatalf("list first user page: %v", err)
	}
	if len(first.Items) != 1 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page = %+v, want one item and a next cursor", first)
	}
	second, err := store.ListUsers(UserListOptions{Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("list second user page: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("second page = %+v, want a different item", second)
	}
	if _, err := store.ListUsers(UserListOptions{Cursor: "not-a-cursor"}); !errors.Is(err, ErrInvalidUserCursor) {
		t.Fatalf("malformed cursor error = %v, want ErrInvalidUserCursor", err)
	}

	matched, err := store.ListUsers(UserListOptions{Query: "literal%"})
	if err != nil {
		t.Fatalf("literal query: %v", err)
	}
	if len(matched.Items) != 1 || matched.Items[0].Username != "literal%name" {
		t.Fatalf("literal query items = %+v, want only literal%%name", matched.Items)
	}
}

func TestAdminStoreUserManagementActivitiesAreScopedAndAtomic(t *testing.T) {
	store := newInitializedAdminStore(t)
	admin, err := store.GetSingleAdminUser()
	if err != nil {
		t.Fatalf("load administrator: %v", err)
	}
	actor := ActivityActor{Type: "admin", ID: admin.ID, Name: admin.Username}

	assertActivity := func(activityID int64, target User, action string) {
		t.Helper()
		if activityID <= 0 {
			t.Fatalf("%s activity ID = %d, want positive", action, activityID)
		}
		item, err := store.activityStore.GetByID(activityID)
		if err != nil {
			t.Fatalf("load %s activity: %v", action, err)
		}
		if item.Action != action || item.ScopeUserID != target.ID || item.SubjectUserID != target.ID {
			t.Fatalf("%s activity = %+v, want target scope/subject %q", action, item, target.ID)
		}
		if item.Actor.Type != "admin" || item.Actor.ID != admin.ID {
			t.Fatalf("%s actor = %+v, want administrator %q", action, item.Actor, admin.ID)
		}
	}

	target, activityID, err := store.CreateUserWithActivity("activity-user", "Password123", actor)
	if err != nil {
		t.Fatalf("create user with activity: %v", err)
	}
	assertActivity(activityID, target, "user_created")

	target, activityID, err = store.UpdateUserUsernameWithActivity(target.ID, "activity-user-renamed", actor)
	if err != nil {
		t.Fatalf("rename user with activity: %v", err)
	}
	assertActivity(activityID, target, "user_username_changed")

	target, activityID, err = store.ResetUserPasswordWithActivity(target.ID, "ResetPassword123", actor)
	if err != nil {
		t.Fatalf("reset password with activity: %v", err)
	}
	assertActivity(activityID, target, "user_password_reset")

	target, changed, activityID, err := store.SetUserAdminWithActivity(admin.ID, target.ID, true, actor)
	if err != nil || !changed {
		t.Fatalf("promote with activity = (%+v, %v, %d, %v)", target, changed, activityID, err)
	}
	assertActivity(activityID, target, "user_admin_granted")

	target, changed, activityID, err = store.SetUserAdminWithActivity(admin.ID, target.ID, false, actor)
	if err != nil || !changed {
		t.Fatalf("demote with activity = (%+v, %v, %d, %v)", target, changed, activityID, err)
	}
	assertActivity(activityID, target, "user_admin_revoked")

	target, changed, activityID, err = store.SetUserStatusWithActivity(admin.ID, target.ID, UserStatusDisabled, actor)
	if err != nil || !changed {
		t.Fatalf("disable with activity = (%+v, %v, %d, %v)", target, changed, activityID, err)
	}
	assertActivity(activityID, target, "user_disabled")

	target, changed, activityID, err = store.SetUserStatusWithActivity(admin.ID, target.ID, UserStatusActive, actor)
	if err != nil || !changed {
		t.Fatalf("enable with activity = (%+v, %v, %d, %v)", target, changed, activityID, err)
	}
	assertActivity(activityID, target, "user_enabled")

	_ = mustCreateSession(t, store, target.ID, target.Username, target.Role, "127.0.0.1", "user-agent")
	activityID, err = store.DeleteSessionsByUserIDWithActivity(target.ID, actor)
	if err != nil {
		t.Fatalf("revoke sessions with activity: %v", err)
	}
	assertActivity(activityID, target, "user_sessions_revoked")

	store.activityStore.failNextAppendsForTest(errors.New("injected activity failure"), 1)
	if _, _, err := store.CreateUserWithActivity("rolled-back-user", "Password123", actor); err == nil {
		t.Fatal("create user should roll back when activity append fails")
	}
	if _, err := store.GetUserByUsername("rolled-back-user"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("rolled-back user lookup error = %v, want ErrUserNotFound", err)
	}
}

type legacyOwnerDeletionFixture struct {
	store               *AdminStore
	target              User
	actor               User
	targetAPIKeyID      string
	targetClientID      string
	targetInstallID     string
	targetTunnelID      string
	otherTunnelID       string
	matchingActivityIDs []int64
	collisionActivityID int64
	otherActivityID     int64
}

func newLegacyOwnerDeletionFixture(t *testing.T) legacyOwnerDeletionFixture {
	t.Helper()
	store := newInitializedAdminStore(t)
	legacyOwner, err := store.GetSingleAdminUser()
	if err != nil {
		t.Fatalf("load legacy owner: %v", err)
	}
	replacementAdmin, err := store.CreateUser("replacement-admin", "Password123")
	if err != nil {
		t.Fatalf("create replacement administrator: %v", err)
	}
	replacementAdmin, changed, err := store.SetUserAdmin(legacyOwner.ID, replacementAdmin.ID, true)
	if err != nil || !changed {
		t.Fatalf("promote replacement administrator = (%+v, %v, %v)", replacementAdmin, changed, err)
	}

	targetKey, err := store.AddAPIKeyForUser(legacyOwner.ID, "legacy-owner-key", "sk-legacy-owner", nil, nil)
	if err != nil {
		t.Fatalf("create legacy-owner API key: %v", err)
	}
	if _, err := store.AddAPIKeyForUser(replacementAdmin.ID, "replacement-key", "sk-replacement", nil, nil); err != nil {
		t.Fatalf("create replacement API key: %v", err)
	}

	now := formatTime(time.Now().UTC())
	const (
		targetClientID  = "legacy-owner-client"
		targetInstallID = "legacy-owner-install"
		otherInstallID  = "replacement-install"
	)
	// Deliberately reuse the target user's ID as another user's client ID. The
	// actor namespace is typed, so deleting the user must not delete this client
	// actor or the replacement administrator's resources.
	otherClientID := legacyOwner.ID
	if _, err := store.db.Exec(`INSERT INTO registered_clients
		(id, owner_user_id, install_id, created_at, last_seen) VALUES
		(?, ?, ?, ?, ?),
		(?, ?, ?, ?, ?)`,
		targetClientID, legacyOwner.ID, targetInstallID, now, now,
		otherClientID, replacementAdmin.ID, otherInstallID, now, now); err != nil {
		t.Fatalf("seed owned clients: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO client_tokens
		(id, token_hash, install_id, key_id, client_id, created_at, last_active_at) VALUES
		('legacy-token-client', 'legacy-token-client-hash', ?, ?, ?, ?, ?),
		('legacy-token-install', 'legacy-token-install-hash', ?, ?, '', ?, ?),
		('replacement-token', 'replacement-token-hash', ?, '', ?, ?, ?),
		('empty-install-token', 'empty-install-token-hash', '', '', '', ?, ?)`,
		targetInstallID, targetKey.ID, targetClientID, now, now,
		targetInstallID, targetKey.ID, now, now,
		otherInstallID, otherClientID, now, now,
		now, now); err != nil {
		t.Fatalf("seed client tokens: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO client_stats (client_id) VALUES (?)`, targetClientID); err != nil {
		t.Fatalf("seed target client stats: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO client_disk_partitions (client_id, path) VALUES (?, '/data')`, targetClientID); err != nil {
		t.Fatalf("seed target client disk partition: %v", err)
	}

	tunnelStore, err := newTunnelStoreWithDB(store.path, store.db, false)
	if err != nil {
		t.Fatalf("open shared tunnel store: %v", err)
	}
	targetTunnel := testStoredServerExposeTCPTunnel("legacy-owner-tunnel", "legacy-owner-tunnel", targetClientID, 8081, 8801, time.Time{})
	targetTunnel.CreatedByUserID = legacyOwner.ID
	if _, err := tunnelStore.AddTunnelForUser(legacyOwner.ID, targetTunnel, nil); err != nil {
		t.Fatalf("seed legacy-owner tunnel: %v", err)
	}
	otherTunnel := testStoredServerExposeTCPTunnel("replacement-tunnel", "replacement-tunnel", otherClientID, 8082, 8802, time.Time{})
	otherTunnel.CreatedByUserID = legacyOwner.ID
	if _, err := tunnelStore.AddTunnelForUser(replacementAdmin.ID, otherTunnel, nil); err != nil {
		t.Fatalf("seed replacement tunnel: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO traffic_buckets
		(tunnel_id, owner_client_id, topology, transport, resolution, bucket_start, owner_user_id) VALUES
		(?, ?, 'server_expose', 'server_relay', 'minute', 1700000000, ?),
		(?, ?, 'server_expose', 'server_relay', 'minute', 1700000001, ?)`,
		targetTunnel.ID, targetClientID, legacyOwner.ID,
		otherTunnel.ID, otherClientID, replacementAdmin.ID); err != nil {
		t.Fatalf("seed traffic buckets: %v", err)
	}

	if _, changed, err := store.SetUserStatus(replacementAdmin.ID, legacyOwner.ID, UserStatusDisabled); err != nil || !changed {
		t.Fatalf("disable legacy owner = (_, %v, %v)", changed, err)
	}
	if _, err := store.db.Exec(`INSERT INTO user_sessions
		(id, user_id, created_at, expires_at, ip, user_agent)
		VALUES ('legacy-owner-session', ?, ?, '2030-01-01T00:00:00Z', '127.0.0.1', 'fixture')`, legacyOwner.ID, now); err != nil {
		t.Fatalf("seed target session: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO admin_totp_recovery_codes
		(id, user_id, code_hash, created_at) VALUES ('legacy-owner-recovery', ?, 'legacy-owner-recovery-hash', ?)`, legacyOwner.ID, now); err != nil {
		t.Fatalf("seed target recovery code: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO admin_passkeys
		(id, user_id, name, credential_id, credential_json, rp_id, origin, created_at)
		VALUES ('legacy-owner-passkey', ?, 'Legacy key', 'legacy-owner-credential', '{}', 'example.test', 'https://example.test', ?)`, legacyOwner.ID, now); err != nil {
		t.Fatalf("seed target passkey: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO admin_auth_challenges
		(id, user_id, kind, session_json, metadata_json, created_at, expires_at)
		VALUES ('legacy-owner-challenge', ?, 'mfa_login', '{}', '{}', ?, '2030-01-01T00:00:00Z')`, legacyOwner.ID, now); err != nil {
		t.Fatalf("seed target authentication challenge: %v", err)
	}

	insertActivity := func(actorType, actorID string, scopeUserID, subjectUserID any) int64 {
		t.Helper()
		result, err := store.db.Exec(`INSERT INTO activity_events
			(occurred_at_ns, recorded_at_ns, severity, category, action, source,
			 actor_type, actor_id, scope_user_id, subject_user_id)
			VALUES (?, ?, 'info', 'admin', 'fixture_event', 'test', ?, ?, ?, ?)`,
			time.Now().UnixNano(), time.Now().UnixNano(), actorType, actorID, scopeUserID, subjectUserID)
		if err != nil {
			t.Fatalf("seed activity event: %v", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("load seeded activity ID: %v", err)
		}
		return id
	}
	scopeActivityID := insertActivity("server", "server", legacyOwner.ID, nil)
	subjectActivityID := insertActivity("server", "server", nil, legacyOwner.ID)
	userActorActivityID := insertActivity("user", legacyOwner.ID, nil, nil)
	clientActorActivityID := insertActivity("client", targetClientID, nil, nil)
	clientRelationActivityID := insertActivity("server", "server", nil, nil)
	if _, err := store.db.Exec(`INSERT INTO activity_event_clients (event_id, client_id, relation)
		VALUES (?, ?, 'related')`, clientRelationActivityID, targetClientID); err != nil {
		t.Fatalf("seed target client activity relation: %v", err)
	}
	tunnelRelationActivityID := insertActivity("server", "server", nil, nil)
	if _, err := store.db.Exec(`INSERT INTO activity_event_tunnels (event_id, tunnel_id, relation)
		VALUES (?, ?, 'related')`, tunnelRelationActivityID, targetTunnel.ID); err != nil {
		t.Fatalf("seed target tunnel activity relation: %v", err)
	}
	collisionActivityID := insertActivity("client", legacyOwner.ID, replacementAdmin.ID, nil)
	otherActivityID := insertActivity("user", replacementAdmin.ID, replacementAdmin.ID, nil)

	return legacyOwnerDeletionFixture{
		store:               store,
		target:              legacyOwner,
		actor:               replacementAdmin,
		targetAPIKeyID:      targetKey.ID,
		targetClientID:      targetClientID,
		targetInstallID:     targetInstallID,
		targetTunnelID:      targetTunnel.ID,
		otherTunnelID:       otherTunnel.ID,
		matchingActivityIDs: []int64{scopeActivityID, subjectActivityID, userActorActivityID, clientActorActivityID, clientRelationActivityID, tunnelRelationActivityID},
		collisionActivityID: collisionActivityID,
		otherActivityID:     otherActivityID,
	}
}

func countUserDeletionFixtureRows(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("count deletion fixture rows: %v\nquery: %s", err, query)
	}
	return count
}

func assertUserDeletionImpact(t *testing.T, impact UserDeletionImpact, userID string) {
	t.Helper()
	if impact.UserID != userID {
		t.Fatalf("deletion impact user_id = %q, want %q", impact.UserID, userID)
	}
	if impact.APIKeys != 1 || impact.Clients != 1 || impact.Tunnels != 1 || impact.TrafficBuckets != 1 || impact.ActivityEvents != 6 {
		t.Fatalf("deletion impact = %+v, want 1 key/client/tunnel/traffic bucket and 6 activity events", impact)
	}
	if impact.GeneratedAt.IsZero() || impact.GeneratedAt.Location() != time.UTC {
		t.Fatalf("deletion impact generated_at = %v, want non-zero UTC time", impact.GeneratedAt)
	}
}

func TestAdminStoreLegacyOwnerDeletionImpactAndTypedCascade(t *testing.T) {
	fixture := newLegacyOwnerDeletionFixture(t)
	impact, err := fixture.store.GetUserDeletionImpact(fixture.target.ID)
	if err != nil {
		t.Fatalf("load legacy-owner deletion impact: %v", err)
	}
	assertUserDeletionImpact(t, impact, fixture.target.ID)
	if _, err := fixture.store.GetUserDeletionImpact("missing-user"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("missing-user deletion impact error = %v, want ErrUserNotFound", err)
	}

	if err := fixture.store.DeleteDisabledUser(fixture.actor.ID, fixture.target.ID); err != nil {
		t.Fatalf("delete disabled legacy owner: %v", err)
	}
	if _, err := fixture.store.GetUser(fixture.target.ID); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("load deleted legacy owner error = %v, want ErrUserNotFound", err)
	}

	targetChecks := []struct {
		name  string
		query string
		args  []any
	}{
		{"API keys", `SELECT COUNT(*) FROM api_keys WHERE owner_user_id = ?`, []any{fixture.target.ID}},
		{"API key permissions", `SELECT COUNT(*) FROM api_key_permissions WHERE api_key_id = ?`, []any{fixture.targetAPIKeyID}},
		{"clients", `SELECT COUNT(*) FROM registered_clients WHERE owner_user_id = ?`, []any{fixture.target.ID}},
		{"client tokens", `SELECT COUNT(*) FROM client_tokens WHERE client_id = ? OR install_id = ?`, []any{fixture.targetClientID, fixture.targetInstallID}},
		{"client stats", `SELECT COUNT(*) FROM client_stats WHERE client_id = ?`, []any{fixture.targetClientID}},
		{"client disks", `SELECT COUNT(*) FROM client_disk_partitions WHERE client_id = ?`, []any{fixture.targetClientID}},
		{"tunnels", `SELECT COUNT(*) FROM tunnels WHERE owner_user_id = ?`, []any{fixture.target.ID}},
		{"tunnel locks", `SELECT COUNT(*) FROM tunnel_resource_locks WHERE tunnel_id = ?`, []any{fixture.targetTunnelID}},
		{"traffic buckets", `SELECT COUNT(*) FROM traffic_buckets WHERE owner_user_id = ?`, []any{fixture.target.ID}},
		{"sessions", `SELECT COUNT(*) FROM user_sessions WHERE user_id = ?`, []any{fixture.target.ID}},
		{"recovery codes", `SELECT COUNT(*) FROM admin_totp_recovery_codes WHERE user_id = ?`, []any{fixture.target.ID}},
		{"passkeys", `SELECT COUNT(*) FROM admin_passkeys WHERE user_id = ?`, []any{fixture.target.ID}},
		{"authentication challenges", `SELECT COUNT(*) FROM admin_auth_challenges WHERE user_id = ?`, []any{fixture.target.ID}},
	}
	for _, check := range targetChecks {
		if count := countUserDeletionFixtureRows(t, fixture.store.db, check.query, check.args...); count != 0 {
			t.Fatalf("deleted legacy-owner %s rows = %d, want 0", check.name, count)
		}
	}
	activityArgs := make([]any, len(fixture.matchingActivityIDs))
	for i, id := range fixture.matchingActivityIDs {
		activityArgs[i] = id
	}
	if count := countUserDeletionFixtureRows(t, fixture.store.db,
		`SELECT COUNT(*) FROM activity_events WHERE id IN (?, ?, ?, ?, ?, ?)`, activityArgs...); count != 0 {
		t.Fatalf("deleted legacy-owner activity rows = %d, want 0", count)
	}
	if count := countUserDeletionFixtureRows(t, fixture.store.db,
		`SELECT COUNT(*) FROM activity_events WHERE id IN (?, ?)`, fixture.collisionActivityID, fixture.otherActivityID); count != 2 {
		t.Fatalf("unrelated activity rows = %d, want 2", count)
	}
	if count := countUserDeletionFixtureRows(t, fixture.store.db,
		`SELECT COUNT(*) FROM client_tokens WHERE id IN ('replacement-token', 'empty-install-token')`); count != 2 {
		t.Fatalf("unrelated token rows = %d, want 2", count)
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM api_keys WHERE owner_user_id = ?`,
		`SELECT COUNT(*) FROM registered_clients WHERE owner_user_id = ?`,
		`SELECT COUNT(*) FROM tunnels WHERE owner_user_id = ?`,
		`SELECT COUNT(*) FROM traffic_buckets WHERE owner_user_id = ?`,
	} {
		if count := countUserDeletionFixtureRows(t, fixture.store.db, query, fixture.actor.ID); count != 1 {
			t.Fatalf("replacement-owned row count for %q = %d, want 1", query, count)
		}
	}
	var createdBy sql.NullString
	if err := fixture.store.db.QueryRow(`SELECT created_by_user_id FROM tunnels WHERE id = ?`, fixture.otherTunnelID).Scan(&createdBy); err != nil {
		t.Fatalf("load replacement tunnel creator: %v", err)
	}
	if createdBy.Valid {
		t.Fatalf("replacement tunnel creator = %q, want NULL after deleting its creator", createdBy.String)
	}
	assertNoSQLiteForeignKeyViolations(t, fixture.store.db)
}

func TestAdminStoreLegacyOwnerDeletionRollsBackAtomically(t *testing.T) {
	fixture := newLegacyOwnerDeletionFixture(t)
	before, err := fixture.store.GetUserDeletionImpact(fixture.target.ID)
	if err != nil {
		t.Fatalf("load deletion impact before rollback test: %v", err)
	}
	assertUserDeletionImpact(t, before, fixture.target.ID)

	injected := errors.New("injected user deletion save failure")
	fixture.store.failSaveErr = injected
	fixture.store.failSaveCount = 1
	if err := fixture.store.DeleteDisabledUser(fixture.actor.ID, fixture.target.ID); !errors.Is(err, injected) {
		t.Fatalf("delete with injected save failure error = %v, want injected error", err)
	}
	after, err := fixture.store.GetUserDeletionImpact(fixture.target.ID)
	if err != nil {
		t.Fatalf("load deletion impact after rollback: %v", err)
	}
	assertUserDeletionImpact(t, after, fixture.target.ID)
	if count := countUserDeletionFixtureRows(t, fixture.store.db,
		`SELECT COUNT(*) FROM client_tokens WHERE client_id = ? OR install_id = ?`, fixture.targetClientID, fixture.targetInstallID); count != 2 {
		t.Fatalf("rolled-back target token rows = %d, want 2", count)
	}
	activityArgs := make([]any, len(fixture.matchingActivityIDs))
	for i, id := range fixture.matchingActivityIDs {
		activityArgs[i] = id
	}
	if count := countUserDeletionFixtureRows(t, fixture.store.db,
		`SELECT COUNT(*) FROM activity_events WHERE id IN (?, ?, ?, ?, ?, ?)`, activityArgs...); count != 6 {
		t.Fatalf("rolled-back target activity rows = %d, want 6", count)
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM users WHERE id = ?`,
		`SELECT COUNT(*) FROM user_sessions WHERE user_id = ?`,
		`SELECT COUNT(*) FROM admin_totp_recovery_codes WHERE user_id = ?`,
		`SELECT COUNT(*) FROM admin_passkeys WHERE user_id = ?`,
		`SELECT COUNT(*) FROM admin_auth_challenges WHERE user_id = ?`,
		`SELECT COUNT(*) FROM api_keys WHERE owner_user_id = ?`,
		`SELECT COUNT(*) FROM registered_clients WHERE owner_user_id = ?`,
		`SELECT COUNT(*) FROM tunnels WHERE owner_user_id = ?`,
		`SELECT COUNT(*) FROM traffic_buckets WHERE owner_user_id = ?`,
	} {
		if count := countUserDeletionFixtureRows(t, fixture.store.db, query, fixture.target.ID); count != 1 {
			t.Fatalf("rolled-back row count for %q = %d, want 1", query, count)
		}
	}
	var createdBy sql.NullString
	if err := fixture.store.db.QueryRow(`SELECT created_by_user_id FROM tunnels WHERE id = ?`, fixture.otherTunnelID).Scan(&createdBy); err != nil {
		t.Fatalf("load rolled-back replacement tunnel creator: %v", err)
	}
	if !createdBy.Valid || createdBy.String != fixture.target.ID {
		t.Fatalf("rolled-back replacement tunnel creator = %+v, want %q", createdBy, fixture.target.ID)
	}
	assertNoSQLiteForeignKeyViolations(t, fixture.store.db)
}
