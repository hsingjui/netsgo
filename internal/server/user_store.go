package server

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	defaultUserListLimit = 50
	maxUserListLimit     = 100
)

var (
	ErrUserNotFound         = errors.New("user not found")
	ErrUserMustBeDisabled   = errors.New("user must be disabled before deletion")
	ErrInvalidUserStatus    = errors.New("invalid user status")
	ErrInvalidUserCursor    = errors.New("invalid user cursor")
	ErrInvalidUsername      = errors.New("invalid username")
	ErrInvalidPassword      = errors.New("invalid password")
	ErrUserAlreadyExists    = errors.New("username is already in use")
	ErrUserOwnerUnavailable = errors.New("legacy owner user is unavailable")
)

// UserListOptions describes the server-side filters for cursor pagination.
// Limit is clamped by ListUsers instead of trusting callers.
type UserListOptions struct {
	Limit   int
	Cursor  string
	Query   string
	Status  *UserStatus
	IsAdmin *bool
}

type UserPage struct {
	Items      []User `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

type UserDeletionImpact struct {
	UserID         string    `json:"user_id"`
	APIKeys        int64     `json:"api_keys"`
	Clients        int64     `json:"clients"`
	Tunnels        int64     `json:"tunnels"`
	TrafficBuckets int64     `json:"traffic_buckets"`
	ActivityEvents int64     `json:"activity_events"`
	GeneratedAt    time.Time `json:"generated_at"`
}

type userListCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

func normalizeUserListLimit(limit int) int {
	if limit <= 0 {
		return defaultUserListLimit
	}
	if limit > maxUserListLimit {
		return maxUserListLimit
	}
	return limit
}

func encodeUserListCursor(user User) (string, error) {
	payload, err := json.Marshal(userListCursor{CreatedAt: formatTime(user.CreatedAt), ID: user.ID})
	if err != nil {
		return "", fmt.Errorf("encode user cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeUserListCursor(raw string) (userListCursor, error) {
	if raw == "" {
		return userListCursor{}, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return userListCursor{}, fmt.Errorf("%w: decode base64 payload: %v", ErrInvalidUserCursor, err)
	}
	var cursor userListCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return userListCursor{}, fmt.Errorf("%w: decode JSON payload: %v", ErrInvalidUserCursor, err)
	}
	if cursor.ID == "" || cursor.CreatedAt == "" {
		return userListCursor{}, fmt.Errorf("%w: missing sort position", ErrInvalidUserCursor)
	}
	if _, err := parseTime(cursor.CreatedAt); err != nil {
		return userListCursor{}, fmt.Errorf("%w: invalid created_at: %v", ErrInvalidUserCursor, err)
	}
	return cursor, nil
}

func ensureOperationalUserInTx(tx *sql.Tx, userID string) error {
	if userID == "" {
		return ErrUserNotFound
	}
	var status string
	err := tx.QueryRow(`SELECT status FROM users WHERE id = ?`, userID).Scan(&status)
	if err == sql.ErrNoRows {
		return ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("load user operational status: %w", err)
	}
	if UserStatus(status) != UserStatusActive {
		return ErrUserDisabled
	}
	return nil
}

func legacyOwnerUserIDInTx(tx *sql.Tx) (string, error) {
	var userID string
	err := tx.QueryRow(`SELECT id FROM users WHERE is_admin = 1 AND status = ? ORDER BY created_at, id LIMIT 1`, string(UserStatusActive)).Scan(&userID)
	if err == sql.ErrNoRows {
		return "", ErrUserOwnerUnavailable
	}
	if err != nil {
		return "", fmt.Errorf("load legacy owner user: %w", err)
	}
	return userID, nil
}

func getUserInTx(tx *sql.Tx, userID string) (User, error) {
	user, err := scanAdminUser(tx.QueryRow(`SELECT `+adminUserSelectColumns()+` FROM users WHERE id = ?`, userID))
	if err == sql.ErrNoRows {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("load user: %w", err)
	}
	return user, nil
}

func listUsersInTx(tx *sql.Tx) ([]User, error) {
	rows, err := tx.Query(`SELECT ` + adminUserSelectColumns() + ` FROM users ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list users for policy: %w", err)
	}
	defer func() { _ = rows.Close() }()

	users := make([]User, 0)
	for rows.Next() {
		user, err := scanAdminUser(rows)
		if err != nil {
			return nil, fmt.Errorf("scan user for policy: %w", err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate users for policy: %w", err)
	}
	return users, nil
}

func (s *AdminStore) GetUser(userID string) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, err := scanAdminUser(s.db.QueryRow(`SELECT `+adminUserSelectColumns()+` FROM users WHERE id = ?`, userID))
	if err == sql.ErrNoRows {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("load user: %w", err)
	}
	return user, nil
}

func (s *AdminStore) GetUserByUsername(username string) (User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return User{}, ErrUserNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, err := scanAdminUser(s.db.QueryRow(`SELECT `+adminUserSelectColumns()+` FROM users WHERE username = ?`, username))
	if err == sql.ErrNoRows {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("load user by username: %w", err)
	}
	return user, nil
}

// IsUserOperational reports whether userID resolves to a user in the only
// state that is allowed to authenticate or operate resources.  Unknown and
// future statuses fail closed.  The error distinguishes an unavailable or
// missing user record from a known non-operational status for callers that
// need to select an authentication response.
func (s *AdminStore) IsUserOperational(userID string) (bool, error) {
	if userID == "" {
		return false, ErrUserNotFound
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var status string
	err := s.db.QueryRow(`SELECT status FROM users WHERE id = ?`, userID).Scan(&status)
	if err == sql.ErrNoRows {
		return false, ErrUserNotFound
	}
	if err != nil {
		return false, fmt.Errorf("load user operational status: %w", err)
	}
	return UserStatus(status) == UserStatusActive, nil
}

func (s *AdminStore) ListUsers(options UserListOptions) (UserPage, error) {
	limit := normalizeUserListLimit(options.Limit)
	cursor, err := decodeUserListCursor(options.Cursor)
	if err != nil {
		return UserPage{}, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	clauses := make([]string, 0, 4)
	args := make([]any, 0, 6)
	if query := strings.TrimSpace(options.Query); query != "" {
		clauses = append(clauses, `username LIKE ? ESCAPE '\'`)
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
		args = append(args, "%"+escaped+"%")
	}
	if options.Status != nil {
		clauses = append(clauses, `status = ?`)
		args = append(args, string(*options.Status))
	}
	if options.IsAdmin != nil {
		clauses = append(clauses, `is_admin = ?`)
		args = append(args, boolToInt(*options.IsAdmin))
	}
	if cursor.ID != "" {
		clauses = append(clauses, `(created_at < ? OR (created_at = ? AND id < ?))`)
		args = append(args, cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
	}

	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit+1)
	rows, err := s.db.Query(`SELECT `+adminUserSelectColumns()+` FROM users`+where+` ORDER BY created_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return UserPage{}, fmt.Errorf("list users: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]User, 0, limit+1)
	for rows.Next() {
		user, err := scanAdminUser(rows)
		if err != nil {
			return UserPage{}, fmt.Errorf("scan listed user: %w", err)
		}
		items = append(items, user)
	}
	if err := rows.Err(); err != nil {
		return UserPage{}, fmt.Errorf("iterate users: %w", err)
	}

	page := UserPage{Items: items}
	if len(page.Items) > limit {
		page.HasMore = true
		page.Items = page.Items[:limit]
		page.NextCursor, err = encodeUserListCursor(page.Items[len(page.Items)-1])
		if err != nil {
			return UserPage{}, err
		}
	}
	return page, nil
}

func normalizeUsername(username string) (string, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return "", fmt.Errorf("%w: username is required", ErrInvalidUsername)
	}
	return username, nil
}

func userManagementActivitySpec(action string, target User, actor ActivityActor, args ActivitySummaryArgs) ActivityEventSpec {
	spec := adminActivitySpec(action, actor, args)
	spec.ScopeUserID = target.ID
	spec.SubjectUserID = target.ID
	return spec
}

func (s *AdminStore) CreateUser(username, password string) (User, error) {
	user, _, err := s.createUser(username, password, nil)
	return user, err
}

// CreateUserWithActivity creates a normal active user and records the
// administrator action in the same transaction.
func (s *AdminStore) CreateUserWithActivity(username, password string, actor ActivityActor) (User, int64, error) {
	return s.createUser(username, password, &actor)
}

func (s *AdminStore) createUser(username, password string, actor *ActivityActor) (User, int64, error) {
	username, err := normalizeUsername(username)
	if err != nil {
		return User{}, 0, err
	}
	if err := validatePassword(password); err != nil {
		return User{}, 0, fmt.Errorf("%w: password does not meet requirements: %w", ErrInvalidPassword, err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.bcryptCost)
	if err != nil {
		return User{}, 0, fmt.Errorf("hash user password: %w", err)
	}
	userID, err := generateUUIDE()
	if err != nil {
		return User{}, 0, err
	}
	now := time.Now().UTC()
	user := User{ID: userID, Username: username, PasswordHash: string(hash), Role: "user", IsAdmin: false, Status: UserStatusActive, CreatedAt: now, UpdatedAt: now}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	if _, err := tx.Exec(`INSERT INTO users (id, username, password_hash, is_admin, status, created_at, updated_at, last_login, totp_enabled, totp_secret)
		VALUES (?, ?, ?, 0, ?, ?, ?, NULL, 0, '')`, user.ID, user.Username, user.PasswordHash, string(user.Status), formatTime(now), formatTime(now)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return User{}, 0, ErrUserAlreadyExists
		}
		return User{}, 0, fmt.Errorf("create user: %w", err)
	}
	var activityID int64
	if actor != nil {
		activityID, err = s.appendActivityTx(tx, userManagementActivitySpec("user_created", user, *actor, ActivitySummaryArgs{ResourceName: user.Username}))
		if err != nil {
			return User{}, 0, err
		}
	}
	if err := s.maybeFailSave(); err != nil {
		return User{}, 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return User{}, 0, err
	}
	return user, activityID, nil
}

func (s *AdminStore) UpdateUserUsername(userID, username string) (User, error) {
	user, _, err := s.updateUserUsername(userID, username, nil)
	return user, err
}

func (s *AdminStore) UpdateUserUsernameWithActivity(userID, username string, actor ActivityActor) (User, int64, error) {
	return s.updateUserUsername(userID, username, &actor)
}

func (s *AdminStore) updateUserUsername(userID, username string, actor *ActivityActor) (User, int64, error) {
	username, err := normalizeUsername(username)
	if err != nil {
		return User{}, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	user, err := getUserInTx(tx, userID)
	if err != nil {
		return User{}, 0, err
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(`UPDATE users SET username = ?, updated_at = ? WHERE id = ?`, username, formatTime(now), userID); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return User{}, 0, ErrUserAlreadyExists
		}
		return User{}, 0, fmt.Errorf("update username: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM user_sessions WHERE user_id = ?`, userID); err != nil {
		return User{}, 0, fmt.Errorf("revoke user sessions after username update: %w", err)
	}
	user.Username, user.UpdatedAt = username, now
	var activityID int64
	if actor != nil {
		activityID, err = s.appendActivityTx(tx, userManagementActivitySpec("user_username_changed", user, *actor, ActivitySummaryArgs{ResourceName: user.Username}))
		if err != nil {
			return User{}, 0, err
		}
	}
	if err := s.maybeFailSave(); err != nil {
		return User{}, 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return User{}, 0, err
	}
	return user, activityID, nil
}

func (s *AdminStore) ResetUserPassword(userID, password string) (User, error) {
	user, _, err := s.resetUserPassword(userID, password, nil)
	return user, err
}

func (s *AdminStore) ResetUserPasswordWithActivity(userID, password string, actor ActivityActor) (User, int64, error) {
	return s.resetUserPassword(userID, password, &actor)
}

func (s *AdminStore) resetUserPassword(userID, password string, actor *ActivityActor) (User, int64, error) {
	if err := validatePassword(password); err != nil {
		return User{}, 0, fmt.Errorf("%w: password does not meet requirements: %w", ErrInvalidPassword, err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.bcryptCost)
	if err != nil {
		return User{}, 0, fmt.Errorf("hash user password: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	user, err := getUserInTx(tx, userID)
	if err != nil {
		return User{}, 0, err
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, string(hash), formatTime(now), userID); err != nil {
		return User{}, 0, fmt.Errorf("reset user password: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM user_sessions WHERE user_id = ?`, userID); err != nil {
		return User{}, 0, fmt.Errorf("revoke user sessions after password reset: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM admin_auth_challenges WHERE user_id = ?`, userID); err != nil {
		return User{}, 0, fmt.Errorf("clear user authentication challenges: %w", err)
	}
	user.PasswordHash, user.UpdatedAt = string(hash), now
	var activityID int64
	if actor != nil {
		activityID, err = s.appendActivityTx(tx, userManagementActivitySpec("user_password_reset", user, *actor, ActivitySummaryArgs{ResourceName: user.Username}))
		if err != nil {
			return User{}, 0, err
		}
	}
	if err := s.maybeFailSave(); err != nil {
		return User{}, 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return User{}, 0, err
	}
	return user, activityID, nil
}

func (s *AdminStore) SetUserAdmin(actorUserID, userID string, isAdmin bool) (User, bool, error) {
	user, changed, _, err := s.setUserAdmin(actorUserID, userID, isAdmin, nil)
	return user, changed, err
}

func (s *AdminStore) SetUserAdminWithActivity(actorUserID, userID string, isAdmin bool, actor ActivityActor) (User, bool, int64, error) {
	return s.setUserAdmin(actorUserID, userID, isAdmin, &actor)
}

func (s *AdminStore) setUserAdmin(actorUserID, userID string, isAdmin bool, actor *ActivityActor) (User, bool, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, false, 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	before, err := getUserInTx(tx, userID)
	if err != nil {
		return User{}, false, 0, err
	}
	after := before
	after.IsAdmin = isAdmin
	if isAdmin {
		after.Role = "admin"
	} else {
		after.Role = "user"
	}
	users, err := listUsersInTx(tx)
	if err != nil {
		return User{}, false, 0, err
	}
	if err := validateUserLifecycleMutation(users, actorUserID, before, after, false); err != nil {
		return User{}, false, 0, err
	}
	if before.IsAdmin == isAdmin {
		if err := commitTx(tx, &committed); err != nil {
			return User{}, false, 0, err
		}
		return before, false, 0, nil
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(`UPDATE users SET is_admin = ?, updated_at = ? WHERE id = ?`, boolToInt(isAdmin), formatTime(now), userID); err != nil {
		return User{}, false, 0, fmt.Errorf("update user administrator flag: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM user_sessions WHERE user_id = ?`, userID); err != nil {
		return User{}, false, 0, fmt.Errorf("revoke sessions after administrator change: %w", err)
	}
	if !isAdmin {
		if _, err := tx.Exec(`UPDATE users SET totp_enabled = 0, totp_secret = '' WHERE id = ?`, userID); err != nil {
			return User{}, false, 0, fmt.Errorf("clear administrator TOTP: %w", err)
		}
		for _, statement := range []string{
			`DELETE FROM admin_totp_recovery_codes WHERE user_id = ?`,
			`DELETE FROM admin_passkeys WHERE user_id = ?`,
			`DELETE FROM admin_auth_challenges WHERE user_id = ?`,
		} {
			if _, err := tx.Exec(statement, userID); err != nil {
				return User{}, false, 0, fmt.Errorf("clear administrator security material: %w", err)
			}
		}
	}
	after.UpdatedAt = now
	var activityID int64
	if actor != nil {
		action := "user_admin_revoked"
		if isAdmin {
			action = "user_admin_granted"
		}
		activityID, err = s.appendActivityTx(tx, userManagementActivitySpec(action, after, *actor, ActivitySummaryArgs{ResourceName: after.Username}))
		if err != nil {
			return User{}, false, 0, err
		}
	}
	if err := s.maybeFailSave(); err != nil {
		return User{}, false, 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return User{}, false, 0, err
	}
	return after, true, activityID, nil
}

func (s *AdminStore) SetUserStatus(actorUserID, userID string, status UserStatus) (User, bool, error) {
	user, changed, _, err := s.setUserStatus(actorUserID, userID, status, nil)
	return user, changed, err
}

func (s *AdminStore) SetUserStatusWithActivity(actorUserID, userID string, status UserStatus, actor ActivityActor) (User, bool, int64, error) {
	return s.setUserStatus(actorUserID, userID, status, &actor)
}

func (s *AdminStore) setUserStatus(actorUserID, userID string, status UserStatus, actor *ActivityActor) (User, bool, int64, error) {
	if status != UserStatusActive && status != UserStatusDisabled {
		return User{}, false, 0, ErrInvalidUserStatus
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, false, 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	before, err := getUserInTx(tx, userID)
	if err != nil {
		return User{}, false, 0, err
	}
	after := before
	after.Status = status
	users, err := listUsersInTx(tx)
	if err != nil {
		return User{}, false, 0, err
	}
	if err := validateUserLifecycleMutation(users, actorUserID, before, after, false); err != nil {
		return User{}, false, 0, err
	}
	if before.Status == status {
		// Repeat disable is the recovery path for an earlier incomplete
		// convergence. Revoke any session row that appeared after the first
		// attempt even though no second state-transition activity is emitted.
		if status == UserStatusDisabled {
			if err := revokeUserLoginStateTx(tx, userID); err != nil {
				return User{}, false, 0, err
			}
		}
		if err := commitTx(tx, &committed); err != nil {
			return User{}, false, 0, err
		}
		return before, false, 0, nil
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(`UPDATE users SET status = ?, updated_at = ? WHERE id = ?`, string(status), formatTime(now), userID); err != nil {
		return User{}, false, 0, fmt.Errorf("update user status: %w", err)
	}
	if status == UserStatusDisabled {
		if err := revokeUserLoginStateTx(tx, userID); err != nil {
			return User{}, false, 0, err
		}
	}
	after.UpdatedAt = now
	var activityID int64
	if actor != nil {
		action := "user_enabled"
		if status == UserStatusDisabled {
			action = "user_disabled"
		}
		activityID, err = s.appendActivityTx(tx, userManagementActivitySpec(action, after, *actor, ActivitySummaryArgs{ResourceName: after.Username}))
		if err != nil {
			return User{}, false, 0, err
		}
	}
	if err := s.maybeFailSave(); err != nil {
		return User{}, false, 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return User{}, false, 0, err
	}
	return after, true, activityID, nil
}

func revokeUserLoginStateTx(tx *sql.Tx, userID string) error {
	if _, err := tx.Exec(`DELETE FROM user_sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("revoke sessions after user disable: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM admin_auth_challenges WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("clear authentication challenges after user disable: %w", err)
	}
	return nil
}

// DeleteSessionsByUserIDWithActivity revokes every web session for a target
// user and records the administrative action atomically with that revocation.
func (s *AdminStore) DeleteSessionsByUserIDWithActivity(userID string, actor ActivityActor) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)

	target, err := getUserInTx(tx, userID)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM user_sessions WHERE user_id = ?`, userID); err != nil {
		return 0, fmt.Errorf("revoke user sessions: %w", err)
	}
	activityID, err := s.appendActivityTx(tx, userManagementActivitySpec("user_sessions_revoked", target, actor, ActivitySummaryArgs{ResourceName: target.Username}))
	if err != nil {
		return 0, err
	}
	if err := s.maybeFailSave(); err != nil {
		return 0, err
	}
	if err := commitTx(tx, &committed); err != nil {
		return 0, err
	}
	return activityID, nil
}

const userActivityDeletionPredicate = `scope_user_id = ? OR subject_user_id = ?
	OR (actor_type IN ('admin', 'user') AND actor_id = ?)
	OR (actor_type = 'client' AND actor_id IN (SELECT id FROM registered_clients WHERE owner_user_id = ?))
	OR id IN (SELECT event_id FROM activity_event_clients WHERE client_id IN (SELECT id FROM registered_clients WHERE owner_user_id = ?))
	OR id IN (SELECT event_id FROM activity_event_tunnels WHERE tunnel_id IN (SELECT id FROM tunnels WHERE owner_user_id = ?))`

func userActivityDeletionArgs(userID string) []any {
	return []any{userID, userID, userID, userID, userID, userID}
}

// GetUserDeletionImpact returns a transactionally consistent preview of the
// persisted rows that DeleteDisabledUser would remove for userID. Runtime
// convergence remains outside this storage-only preview.
func (s *AdminStore) GetUserDeletionImpact(userID string) (UserDeletionImpact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tx, err := s.db.Begin()
	if err != nil {
		return UserDeletionImpact{}, err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)

	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM users WHERE id = ?`, userID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return UserDeletionImpact{}, ErrUserNotFound
		}
		return UserDeletionImpact{}, fmt.Errorf("load deletion-impact user: %w", err)
	}

	impact := UserDeletionImpact{UserID: userID}
	counts := []struct {
		name  string
		query string
		args  []any
		dest  *int64
	}{
		{"api keys", `SELECT COUNT(*) FROM api_keys WHERE owner_user_id = ?`, []any{userID}, &impact.APIKeys},
		{"clients", `SELECT COUNT(*) FROM registered_clients WHERE owner_user_id = ?`, []any{userID}, &impact.Clients},
		{"tunnels", `SELECT COUNT(*) FROM tunnels WHERE owner_user_id = ?`, []any{userID}, &impact.Tunnels},
		{"traffic buckets", `SELECT COUNT(*) FROM traffic_buckets WHERE owner_user_id = ?`, []any{userID}, &impact.TrafficBuckets},
		{"activity events", `SELECT COUNT(*) FROM activity_events WHERE ` + userActivityDeletionPredicate, userActivityDeletionArgs(userID), &impact.ActivityEvents},
	}
	for _, count := range counts {
		if err := tx.QueryRow(count.query, count.args...).Scan(count.dest); err != nil {
			return UserDeletionImpact{}, fmt.Errorf("count user deletion-impact %s: %w", count.name, err)
		}
	}
	impact.GeneratedAt = time.Now().UTC()
	if err := commitTx(tx, &committed); err != nil {
		return UserDeletionImpact{}, err
	}
	return impact, nil
}

// DeleteDisabledUser removes the target user's credentials, owned resources,
// user-scoped history, and dangling creator references in one transaction.
// Runtime convergence is intentionally the caller's responsibility and must
// happen before this method is called.
func (s *AdminStore) DeleteDisabledUser(actorUserID, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer rollbackUnlessCommitted(tx, &committed)
	before, err := getUserInTx(tx, userID)
	if err != nil {
		return err
	}
	// A caller must never receive the generic disabled-state conflict when it
	// attempts to delete its own account.  Self-deletion is forbidden whether
	// or not the target has reached the deletion precondition.
	if actorUserID == before.ID {
		return ErrSelfUserLifecycleMutation
	}
	if before.Status != UserStatusDisabled {
		return ErrUserMustBeDisabled
	}
	users, err := listUsersInTx(tx)
	if err != nil {
		return err
	}
	if err := validateUserLifecycleMutation(users, actorUserID, before, before, true); err != nil {
		return err
	}

	if _, err := tx.Exec(`UPDATE tunnels SET created_by_user_id = NULL WHERE created_by_user_id = ? AND owner_user_id <> ?`, userID, userID); err != nil {
		return fmt.Errorf("clear deleted user as resource creator: %w", err)
	}

	// Delete events before deleting their related resource roots.  The event
	// relation tables cascade from activity_events.
	if _, err := tx.Exec(`DELETE FROM activity_events WHERE `+userActivityDeletionPredicate, userActivityDeletionArgs(userID)...); err != nil {
		return fmt.Errorf("delete user activity events: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM traffic_buckets WHERE owner_user_id = ?`, userID); err != nil {
		return fmt.Errorf("delete user traffic buckets: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM tunnel_resource_locks WHERE tunnel_id IN (SELECT id FROM tunnels WHERE owner_user_id = ?)`, userID); err != nil {
		return fmt.Errorf("delete user tunnel resource locks: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM tunnels WHERE owner_user_id = ?`, userID); err != nil {
		return fmt.Errorf("delete user tunnels: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM client_tokens
		WHERE client_id IN (SELECT id FROM registered_clients WHERE owner_user_id = ?)
			OR (install_id <> '' AND install_id IN (
				SELECT install_id FROM registered_clients WHERE owner_user_id = ? AND install_id <> ''
			))`, userID, userID); err != nil {
		return fmt.Errorf("delete user client tokens: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM registered_clients WHERE owner_user_id = ?`, userID); err != nil {
		return fmt.Errorf("delete user registered clients: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM api_keys WHERE owner_user_id = ?`, userID); err != nil {
		return fmt.Errorf("delete user api keys: %w", err)
	}
	for _, statement := range []string{
		`DELETE FROM user_sessions WHERE user_id = ?`,
		`DELETE FROM admin_totp_recovery_codes WHERE user_id = ?`,
		`DELETE FROM admin_passkeys WHERE user_id = ?`,
		`DELETE FROM admin_auth_challenges WHERE user_id = ?`,
	} {
		if _, err := tx.Exec(statement, userID); err != nil {
			return fmt.Errorf("delete user credential material: %w", err)
		}
	}
	result, err := tx.Exec(`DELETE FROM users WHERE id = ?`, userID)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrUserNotFound
	}
	if err := s.maybeFailSave(); err != nil {
		return err
	}
	return commitTx(tx, &committed)
}
