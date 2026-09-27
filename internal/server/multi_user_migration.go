package server

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type multiUserMigrationOrphanRowsError struct {
	Table       string
	OrphanCount int64
}

func (e *multiUserMigrationOrphanRowsError) Error() string {
	return fmt.Sprintf(
		"multi-user migration cannot continue: %s contains %d row(s) whose user_id has no matching admin_users row",
		e.Table,
		e.OrphanCount,
	)
}

func validateMultiUserOwnershipMigrationTx(tx *sql.Tx) error {
	for _, check := range []struct {
		table string
		name  string
	}{
		{"admin_totp_recovery_codes", "admin_totp_recovery_codes_orphaned"},
		{"admin_passkeys", "admin_passkeys_orphaned"},
		{"admin_auth_challenges", "admin_auth_challenges_orphaned"},
	} {
		orphanCount, err := multiUserMigrationValidationCount(tx, check.name)
		if err != nil {
			return err
		}
		if orphanCount != 0 {
			return &multiUserMigrationOrphanRowsError{Table: check.table, OrphanCount: orphanCount}
		}
	}

	for _, pair := range []struct {
		source string
		target string
	}{
		{"admin_users_source", "users_target"},
		{"admin_totp_recovery_codes_source", "admin_totp_recovery_codes_target"},
		{"admin_passkeys_source", "admin_passkeys_target"},
		{"admin_auth_challenges_source", "admin_auth_challenges_target"},
		{"api_keys_source", "api_keys_target"},
		{"registered_clients_source", "registered_clients_target"},
		{"tunnels_source", "tunnels_target"},
		{"traffic_buckets_source", "traffic_buckets_target"},
	} {
		if err := validateMultiUserMigrationCountsEqual(tx, pair.source, pair.target); err != nil {
			return err
		}
	}

	if err := validateMultiUserSessionCounts(tx); err != nil {
		return err
	}
	if err := validateMultiUserActivityCounts(tx); err != nil {
		return err
	}
	for _, table := range []string{
		"admin_users",
		"admin_sessions",
		"admin_totp_recovery_codes_multi_user_migration",
		"admin_passkeys_multi_user_migration",
		"admin_auth_challenges_multi_user_migration",
		"tunnels_multi_user_ownership_legacy",
	} {
		if err := validateMultiUserTableAbsent(tx, table); err != nil {
			return err
		}
	}

	for _, index := range []multiUserMigrationIndex{
		{"users", "idx_users_page", false, []string{"created_at", "id"}},
		{"users", "idx_users_status_page", false, []string{"status", "created_at", "id"}},
		{"user_sessions", "idx_user_sessions_user", false, []string{"user_id"}},
		{"user_sessions", "idx_user_sessions_expires", false, []string{"expires_at"}},
		{"admin_totp_recovery_codes", "idx_admin_totp_recovery_codes_user_unused", false, []string{"user_id", "used_at"}},
		{"admin_passkeys", "idx_admin_passkeys_user", false, []string{"user_id"}},
		{"admin_passkeys", "idx_admin_passkeys_rp", false, []string{"rp_id", "origin"}},
		{"admin_auth_challenges", "idx_admin_auth_challenges_user_kind", false, []string{"user_id", "kind"}},
		{"admin_auth_challenges", "idx_admin_auth_challenges_expires", false, []string{"expires_at"}},
		{"api_keys", "idx_api_keys_owner", false, []string{"owner_user_id", "created_at"}},
		{"registered_clients", "idx_registered_clients_owner", false, []string{"owner_user_id", "created_at"}},
		{"tunnels", "idx_tunnels_user_topology", false, []string{"owner_user_id", "topology", "created_at"}},
		{"traffic_buckets", "idx_traffic_user_query", false, []string{"owner_user_id", "resolution", "bucket_start"}},
		{"activity_events", "idx_activity_events_user", false, []string{"scope_user_id", "occurred_at_ns", "id"}},
		{"activity_events", "idx_activity_events_subject_user", false, []string{"subject_user_id", "occurred_at_ns", "id"}},
	} {
		if err := validateMultiUserIndex(tx, index); err != nil {
			return err
		}
	}
	for _, index := range []multiUserMigrationIndex{
		{"users", "", true, []string{"username"}},
		{"admin_totp_recovery_codes", "", true, []string{"code_hash"}},
		{"admin_passkeys", "", true, []string{"credential_id"}},
	} {
		if err := validateMultiUserUniqueIndex(tx, index); err != nil {
			return err
		}
	}

	for _, foreignKey := range []multiUserMigrationForeignKey{
		{"user_sessions", "user_id", "users", "id", "CASCADE"},
		{"admin_totp_recovery_codes", "user_id", "users", "id", "CASCADE"},
		{"admin_passkeys", "user_id", "users", "id", "CASCADE"},
		{"admin_auth_challenges", "user_id", "users", "id", "CASCADE"},
		{"api_keys", "owner_user_id", "users", "id", "CASCADE"},
		{"registered_clients", "owner_user_id", "users", "id", "CASCADE"},
		{"tunnels", "owner_user_id", "users", "id", "CASCADE"},
		{"traffic_buckets", "owner_user_id", "users", "id", "CASCADE"},
		{"activity_events", "scope_user_id", "users", "id", "CASCADE"},
		{"activity_events", "subject_user_id", "users", "id", "CASCADE"},
	} {
		if err := validateMultiUserForeignKey(tx, foreignKey); err != nil {
			return err
		}
	}
	if err := validateMultiUserForeignKeyCheck(tx); err != nil {
		return err
	}
	if err := validateMultiUserInitializedOwnership(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE multi_user_migration_validation`); err != nil {
		return fmt.Errorf("drop multi-user migration validation table: %w", err)
	}
	return nil
}

func validateMultiUserMigrationCountsEqual(tx *sql.Tx, source, target string) error {
	sourceCount, err := multiUserMigrationValidationCount(tx, source)
	if err != nil {
		return err
	}
	targetCount, err := multiUserMigrationValidationCount(tx, target)
	if err != nil {
		return err
	}
	if sourceCount != targetCount {
		return fmt.Errorf("multi-user migration row count mismatch: %s=%d, %s=%d", source, sourceCount, target, targetCount)
	}
	return nil
}

func validateMultiUserSessionCounts(tx *sql.Tx) error {
	source, err := multiUserMigrationValidationCount(tx, "admin_sessions_source")
	if err != nil {
		return err
	}
	target, err := multiUserMigrationValidationCount(tx, "user_sessions_target")
	if err != nil {
		return err
	}
	revoked, err := multiUserMigrationValidationCount(tx, "user_sessions_revoked")
	if err != nil {
		return err
	}
	if source != target+revoked {
		return fmt.Errorf("multi-user migration session count mismatch: source=%d, target=%d, revoked=%d", source, target, revoked)
	}
	return nil
}

func validateMultiUserActivityCounts(tx *sql.Tx) error {
	source, err := multiUserMigrationValidationCount(tx, "activity_events_source")
	if err != nil {
		return err
	}
	target, err := multiUserMigrationValidationCount(tx, "activity_events_target")
	if err != nil {
		return err
	}
	removed, err := multiUserMigrationValidationCount(tx, "session_environment_mismatch_removed")
	if err != nil {
		return err
	}
	if source != target+removed {
		return fmt.Errorf("multi-user migration activity count mismatch: source=%d, target=%d, removed=%d", source, target, removed)
	}

	var invalidCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM activity_events
		WHERE action = 'session_environment_mismatch' AND subject_user_id IS NULL`).Scan(&invalidCount); err != nil {
		return fmt.Errorf("check migrated session-environment activities: %w", err)
	}
	if invalidCount != 0 {
		return fmt.Errorf("multi-user migration left %d session-environment activities without a subject user", invalidCount)
	}
	return nil
}

func multiUserMigrationValidationCount(tx *sql.Tx, name string) (int64, error) {
	var value int64
	err := tx.QueryRow(`SELECT value FROM multi_user_migration_validation WHERE name = ?`, name).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("multi-user migration validation value %q is missing", name)
	}
	if err != nil {
		return 0, fmt.Errorf("read multi-user migration validation value %q: %w", name, err)
	}
	return value, nil
}

func validateMultiUserTableAbsent(tx *sql.Tx, table string) error {
	var name string
	err := tx.QueryRow(`SELECT name FROM sqlite_schema WHERE type = 'table' AND name = ?`, table).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check old table %q: %w", table, err)
	}
	return fmt.Errorf("multi-user migration left old table %q", name)
}

type multiUserMigrationIndex struct {
	table   string
	name    string
	unique  bool
	columns []string
}

func validateMultiUserIndex(tx *sql.Tx, want multiUserMigrationIndex) error {
	indexes, err := multiUserMigrationIndexes(tx, want.table)
	if err != nil {
		return err
	}
	for _, got := range indexes {
		if got.name != want.name {
			continue
		}
		if got.unique != want.unique || !sameMultiUserColumns(got.columns, want.columns) {
			return fmt.Errorf("multi-user migration index %s.%s has unexpected definition", want.table, want.name)
		}
		return nil
	}
	return fmt.Errorf("multi-user migration index %s.%s is missing", want.table, want.name)
}

func validateMultiUserUniqueIndex(tx *sql.Tx, want multiUserMigrationIndex) error {
	indexes, err := multiUserMigrationIndexes(tx, want.table)
	if err != nil {
		return err
	}
	for _, got := range indexes {
		if got.unique && sameMultiUserColumns(got.columns, want.columns) {
			return nil
		}
	}
	return fmt.Errorf("multi-user migration unique constraint %s(%s) is missing", want.table, strings.Join(want.columns, ", "))
}

type multiUserMigrationResolvedIndex struct {
	name    string
	unique  bool
	columns []string
}

func multiUserMigrationIndexes(tx *sql.Tx, table string) ([]multiUserMigrationResolvedIndex, error) {
	rows, err := tx.Query(`SELECT name, "unique" FROM pragma_index_list(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("list indexes for %q: %w", table, err)
	}
	var indexes []multiUserMigrationResolvedIndex
	for rows.Next() {
		var index multiUserMigrationResolvedIndex
		var unique int
		if err := rows.Scan(&index.name, &unique); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan index for %q: %w", table, err)
		}
		index.unique = unique != 0
		indexes = append(indexes, index)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate indexes for %q: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close indexes for %q: %w", table, err)
	}
	for index := range indexes {
		columns, err := multiUserMigrationIndexColumns(tx, indexes[index].name)
		if err != nil {
			return nil, err
		}
		indexes[index].columns = columns
	}
	return indexes, nil
}

func multiUserMigrationIndexColumns(tx *sql.Tx, index string) ([]string, error) {
	rows, err := tx.Query(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, index)
	if err != nil {
		return nil, fmt.Errorf("list columns for index %q: %w", index, err)
	}
	defer func() { _ = rows.Close() }()
	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return nil, fmt.Errorf("scan column for index %q: %w", index, err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate columns for index %q: %w", index, err)
	}
	return columns, nil
}

func sameMultiUserColumns(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type multiUserMigrationForeignKey struct {
	table    string
	from     string
	toTable  string
	toColumn string
	onDelete string
}

func validateMultiUserForeignKey(tx *sql.Tx, want multiUserMigrationForeignKey) error {
	rows, err := tx.Query(`SELECT "table", "from", "to", on_delete FROM pragma_foreign_key_list(?)`, want.table)
	if err != nil {
		return fmt.Errorf("list foreign keys for %q: %w", want.table, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var table, from, to, onDelete string
		if err := rows.Scan(&table, &from, &to, &onDelete); err != nil {
			return fmt.Errorf("scan foreign key for %q: %w", want.table, err)
		}
		if table == want.toTable && from == want.from && to == want.toColumn && strings.EqualFold(onDelete, want.onDelete) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate foreign keys for %q: %w", want.table, err)
	}
	return fmt.Errorf("multi-user migration foreign key %s.%s -> %s.%s ON DELETE %s is missing", want.table, want.from, want.toTable, want.toColumn, want.onDelete)
}

func validateMultiUserForeignKeyCheck(tx *sql.Tx) error {
	rows, err := tx.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("run foreign_key_check: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		var table, parent any
		var rowID, foreignKeyID any
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			return fmt.Errorf("scan foreign_key_check result: %w", err)
		}
		return fmt.Errorf("foreign_key_check failed for table %v row %v parent %v foreign key %v", table, rowID, parent, foreignKeyID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate foreign_key_check result: %w", err)
	}
	return nil
}

func validateMultiUserInitializedOwnership(tx *sql.Tx) error {
	var initialized int
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM server_config WHERE initialized = 1)`).Scan(&initialized); err != nil {
		return fmt.Errorf("check server initialization state: %w", err)
	}
	if initialized == 0 {
		return nil
	}

	var activeAdmins int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin = 1 AND status = 'active'`).Scan(&activeAdmins); err != nil {
		return fmt.Errorf("count active administrators: %w", err)
	}
	if activeAdmins == 0 {
		return errors.New("initialized server has no active administrator after multi-user migration")
	}
	for _, table := range []string{"api_keys", "registered_clients", "tunnels", "traffic_buckets"} {
		var missingOwners int
		query := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE owner_user_id IS NULL OR owner_user_id = ''`, table)
		if err := tx.QueryRow(query).Scan(&missingOwners); err != nil {
			return fmt.Errorf("count missing owners in %s: %w", table, err)
		}
		if missingOwners != 0 {
			return fmt.Errorf("initialized server has %d %s rows without an owner", missingOwners, table)
		}
	}
	return nil
}
