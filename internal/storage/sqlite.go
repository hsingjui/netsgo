package storage

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const privateFileMode = 0o600

type Migration struct {
	Name        string
	Description string
	CreatedAt   string
	Up          string
	Down        string
	// ValidateTx runs after Up and before the migration is recorded in its
	// ledger. Returning an error rolls back the SQL and leaves the migration
	// unapplied.
	ValidateTx func(*sql.Tx) error
}

// MigrationPlanItem places one migration in a ledger while preserving the
// caller-provided global execution order. Strict items reject unknown entries
// already present in their ledger before any plan item is applied.
type MigrationPlanItem struct {
	Migration Migration
	Ledger    string
	Strict    bool
}

func Open(path string, migrations []Migration) (*sql.DB, error) {
	if _, err := validateMigrationList(migrations); err != nil {
		return nil, err
	}
	if err := PreflightStrictMigrations(path, "schema_migrations", migrations); err != nil {
		return nil, err
	}
	db, err := OpenConfigured(path)
	if err != nil {
		return nil, err
	}
	if err := applyMigrations(db, migrations); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := chmodSQLiteFiles(path); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// OpenConfigured opens a writable SQLite database with NetsGo's connection
// settings but does not apply migrations. Callers that need multiple migration
// ledgers can preflight and then use ApplyMigrationPlan.
func OpenConfigured(path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path must not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create sqlite directory: %w", err)
	}
	if err := ensurePrivateSQLiteFile(path); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)

	if err := configure(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := chmodSQLiteFiles(path); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// PreflightStrictMigrations checks an existing strict migration ledger through
// a read-only SQLite connection. It intentionally returns before opening a
// writable connection so an older binary refuses a newer strict schema without
// creating ledger rows or changing business data.
func PreflightStrictMigrations(path, table string, migrations []Migration) error {
	if len(migrations) == 0 {
		return nil
	}
	if _, err := validateMigrationList(migrations); err != nil {
		return err
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat sqlite database for strict migration preflight: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("sqlite path is a directory: %s", path)
	}
	// A zero-byte file is what OpenConfigured creates before SQLite initializes
	// it. It cannot contain a migration ledger, so avoid opening it read-only.
	if info.Size() == 0 {
		return nil
	}

	db, err := OpenReadOnly(path)
	if err != nil {
		return fmt.Errorf("open sqlite database for strict migration preflight: %w", err)
	}
	defer func() { _ = db.Close() }()
	return ValidateKnownMigrations(db, table, migrations)
}

// OpenReadOnly opens an existing SQLite database without creating the file,
// changing pragmas with write side effects, or applying migrations.
func OpenReadOnly(path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path must not be empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("sqlite path is a directory: %s", path)
	}

	db, err := sql.Open("sqlite", ReadOnlyDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite database read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)

	if err := configureReadOnly(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// ReadOnlyDSN returns a SQLite DSN that refuses writes and file creation.
func ReadOnlyDSN(path string) string {
	return readOnlyDSNForOS(path, runtime.GOOS)
}

func readOnlyDSNForOS(path, goos string) string {
	u := url.URL{Scheme: "file", Path: path}
	if goos == "windows" {
		u = windowsFileURL(path)
	} else {
		u.Path = filepath.ToSlash(path)
	}
	q := u.Query()
	q.Set("mode", "ro")
	u.RawQuery = q.Encode()
	return u.String()
}

func windowsFileURL(path string) url.URL {
	slashPath := strings.ReplaceAll(path, `\`, `/`)
	if trimmed, ok := strings.CutPrefix(slashPath, "//"); ok {
		host, rest, ok := strings.Cut(trimmed, "/")
		if ok {
			return url.URL{Scheme: "file", Host: host, Path: "/" + rest}
		}
	}
	if len(slashPath) >= 2 && slashPath[1] == ':' && !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	return url.URL{Scheme: "file", Path: slashPath}
}

// TableExists checks sqlite_master without creating schema artifacts.
func TableExists(db *sql.DB, tableName string) (bool, error) {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, tableName).Scan(&name)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func ensurePrivateSQLiteFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, privateFileMode)
	if err != nil {
		return fmt.Errorf("create sqlite database file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close sqlite database file: %w", err)
	}
	return chmodSQLiteFiles(path)
}

func chmodSQLiteFiles(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := chmodIfExists(candidate, privateFileMode); err != nil {
			return err
		}
	}
	return nil
}

func chmodIfExists(path string, mode os.FileMode) error {
	if err := os.Chmod(path, mode); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("secure sqlite file %s: %w", path, err)
	}
	return nil
}

func configure(db *sql.DB) error {
	statements := []string{
		`PRAGMA journal_mode = WAL;`,
		`PRAGMA synchronous = NORMAL;`,
		`PRAGMA foreign_keys = ON;`,
		`PRAGMA busy_timeout = 5000;`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("configure sqlite %q: %w", statement, err)
		}
	}
	return nil
}

func configureReadOnly(db *sql.DB) error {
	statements := []string{
		`PRAGMA query_only = ON;`,
		`PRAGMA foreign_keys = ON;`,
		`PRAGMA busy_timeout = 5000;`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("configure sqlite read-only %q: %w", statement, err)
		}
	}
	return nil
}

func applyMigrations(db *sql.DB, migrations []Migration) error {
	return ApplyMigrations(db, migrations)
}

// ApplyMigrations applies strict migrations using the primary schema_migrations ledger.
func ApplyMigrations(db *sql.DB, migrations []Migration) error {
	return applyMigrationsToTable(db, "schema_migrations", migrations, true)
}

// ApplyCompatibleMigrations applies additive, backward-compatible migrations in
// a separate ledger. Unknown rows are intentionally tolerated so an older
// binary can open a database previously used by a newer binary.
func ApplyCompatibleMigrations(db *sql.DB, table string, migrations []Migration) error {
	return applyMigrationsToTable(db, table, migrations, false)
}

// ApplyMigrationPlan applies migrations in the supplied global order while
// recording each item in its own ledger. Strict ledgers are checked for unknown
// applied migrations before this function creates a ledger or executes any Up
// SQL; compatible ledger rows remain forward-compatible.
func ApplyMigrationPlan(db *sql.DB, plan []MigrationPlanItem) error {
	if db == nil {
		return fmt.Errorf("sqlite database must not be nil")
	}

	type preparedPlanItem struct {
		migration Migration
		ledger    string
	}
	prepared := make([]preparedPlanItem, 0, len(plan))
	strictMigrations := make(map[string][]Migration)
	ledgerNames := make(map[string]string)
	seenMigrations := make(map[string]struct{}, len(plan))

	for index, item := range plan {
		ledgerName, err := sqliteIdentifier(item.Ledger)
		if err != nil {
			return fmt.Errorf("sqlite migration plan item %d: %w", index, err)
		}
		if err := validateMigration(item.Migration); err != nil {
			return err
		}
		if _, ok := seenMigrations[item.Migration.Name]; ok {
			return fmt.Errorf("sqlite migration %q is duplicated", item.Migration.Name)
		}
		seenMigrations[item.Migration.Name] = struct{}{}
		ledgerNames[item.Ledger] = ledgerName
		prepared = append(prepared, preparedPlanItem{
			migration: item.Migration,
			ledger:    item.Ledger,
		})
		if item.Strict {
			strictMigrations[item.Ledger] = append(strictMigrations[item.Ledger], item.Migration)
		}
	}

	// Do this before creating either ledger. In particular, a previous Server
	// that does not know a newer strict migration must fail without applying a
	// compatible migration that happens to precede it in this binary's plan.
	for ledger, migrations := range strictMigrations {
		if err := ValidateKnownMigrations(db, ledger, migrations); err != nil {
			return err
		}
	}
	for ledger, ledgerName := range ledgerNames {
		if err := ensureMigrationTable(db, ledger, ledgerName); err != nil {
			return err
		}
	}

	for _, item := range prepared {
		ledgerName := ledgerNames[item.ledger]
		if err := applyMigration(db, ledgerName, item.migration); err != nil {
			return err
		}
	}
	return nil
}

// ValidateKnownMigrations verifies that an existing migration ledger has no
// entries absent from migrations. It never creates a ledger table, so it is
// safe to use with a read-only database during startup preflight.
func ValidateKnownMigrations(db *sql.DB, table string, migrations []Migration) error {
	if db == nil {
		return fmt.Errorf("sqlite database must not be nil")
	}
	tableName, err := sqliteIdentifier(table)
	if err != nil {
		return err
	}
	knownMigrations, err := validateMigrationList(migrations)
	if err != nil {
		return err
	}
	if len(knownMigrations) == 0 {
		return nil
	}
	exists, err := TableExists(db, table)
	if err != nil {
		return fmt.Errorf("check migration table %q: %w", table, err)
	}
	if !exists {
		return nil
	}
	return rejectUnknownAppliedMigrations(db, tableName, knownMigrations)
}

func applyMigrationsToTable(db *sql.DB, table string, migrations []Migration, rejectUnknown bool) error {
	if db == nil {
		return fmt.Errorf("sqlite database must not be nil")
	}
	tableName, err := sqliteIdentifier(table)
	if err != nil {
		return err
	}
	_, err = validateMigrationList(migrations)
	if err != nil {
		return err
	}
	if rejectUnknown && len(migrations) > 0 {
		if err := ValidateKnownMigrations(db, table, migrations); err != nil {
			return err
		}
	}
	if err := ensureMigrationTable(db, table, tableName); err != nil {
		return err
	}

	for _, migration := range migrations {
		if err := applyMigration(db, tableName, migration); err != nil {
			return err
		}
	}
	return nil
}

func validateMigrationList(migrations []Migration) (map[string]struct{}, error) {
	knownMigrations := make(map[string]struct{}, len(migrations))
	for _, migration := range migrations {
		if err := validateMigration(migration); err != nil {
			return nil, err
		}
		if _, ok := knownMigrations[migration.Name]; ok {
			return nil, fmt.Errorf("sqlite migration %q is duplicated", migration.Name)
		}
		knownMigrations[migration.Name] = struct{}{}
	}
	return knownMigrations, nil
}

func validateMigration(migration Migration) error {
	if migration.Name == "" {
		return fmt.Errorf("sqlite migration name must not be empty")
	}
	if migration.Up == "" {
		return fmt.Errorf("sqlite migration %q has empty SQL", migration.Name)
	}
	return nil
}

func ensureMigrationTable(db *sql.DB, table, tableName string) error {
	if _, err := db.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		name TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	);`, tableName)); err != nil {
		return fmt.Errorf("create migration table %q: %w", table, err)
	}
	return nil
}

func applyMigration(db *sql.DB, tableName string, migration Migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration %q: %w", migration.Name, err)
	}
	applied, err := migrationApplied(tx, tableName, migration.Name)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if applied {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit already-applied migration %q: %w", migration.Name, err)
		}
		return nil
	}
	if _, err := tx.Exec(migration.Up); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("apply migration %q: %w", migration.Name, err)
	}
	if migration.ValidateTx != nil {
		if err := migration.ValidateTx(tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("validate migration %q: %w", migration.Name, err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf(`INSERT INTO %s (name, applied_at) VALUES (?, ?)`, tableName), migration.Name, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("record migration %q: %w", migration.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %q: %w", migration.Name, err)
	}
	return nil
}

func sqliteIdentifier(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("sqlite migration table name must not be empty")
	}
	for i := range len(name) {
		char := name[i]
		if (char >= 'a' && char <= 'z') || char == '_' || (i > 0 && char >= '0' && char <= '9') {
			continue
		}
		return "", fmt.Errorf("invalid sqlite migration table name %q", name)
	}
	return `"` + name + `"`, nil
}

func rejectUnknownAppliedMigrations(db *sql.DB, tableName string, knownMigrations map[string]struct{}) error {
	rows, err := db.Query(fmt.Sprintf(`SELECT name FROM %s ORDER BY name`, tableName))
	if err != nil {
		return fmt.Errorf("query applied migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("scan applied migration: %w", err)
		}
		if _, ok := knownMigrations[name]; !ok {
			return fmt.Errorf("sqlite database has unknown applied migration %q", name)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate applied migrations: %w", err)
	}
	return nil
}

func migrationApplied(tx *sql.Tx, tableName, name string) (bool, error) {
	var existing string
	err := tx.QueryRow(fmt.Sprintf(`SELECT name FROM %s WHERE name = ?`, tableName), name).Scan(&existing)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query migration %q: %w", name, err)
	}
	return true, nil
}
