package storage

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenCreatesParentDirectoryAndAppliesPragmas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "netsgo.db")
	db, err := Open(path, []Migration{{
		Name: "001_create_widgets",
		Up:   `CREATE TABLE widgets (id TEXT PRIMARY KEY, name TEXT NOT NULL);`,
	}})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	assertPragmaValue(t, db, "journal_mode", "wal")
	assertPragmaValue(t, db, "foreign_keys", "1")

	if _, err := db.Exec(`INSERT INTO widgets (id, name) VALUES ('w1', 'Widget')`); err != nil {
		t.Fatalf("insert into migrated table failed: %v", err)
	}
}

func TestOpenRunsMigrationsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netsgo.db")
	migrations := []Migration{{
		Name: "001_create_counter",
		Up:   `CREATE TABLE counter (id INTEGER PRIMARY KEY, value INTEGER NOT NULL); INSERT INTO counter (id, value) VALUES (1, 1);`,
	}}

	db1, err := Open(path, migrations)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("db1.Close() error = %v", err)
	}

	db2, err := Open(path, migrations)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer func() { _ = db2.Close() }()

	var value int
	if err := db2.QueryRow(`SELECT value FROM counter WHERE id = 1`).Scan(&value); err != nil {
		t.Fatalf("query migrated row failed: %v", err)
	}
	if value != 1 {
		t.Fatalf("migration should run once, value = %d", value)
	}
}

func TestOpenRejectsUnknownAppliedMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netsgo.db")
	db, err := Open(path, []Migration{{
		Name: "001_create_counter",
		Up:   `CREATE TABLE counter (id INTEGER PRIMARY KEY, value INTEGER NOT NULL);`,
	}})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations (name, applied_at) VALUES ('999_future_schema', '2026-05-15T00:00:00Z')`); err != nil {
		t.Fatalf("insert unknown migration: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	db, err = Open(path, []Migration{{
		Name: "001_create_counter",
		Up:   `CREATE TABLE counter (id INTEGER PRIMARY KEY, value INTEGER NOT NULL);`,
	}})
	if err == nil {
		_ = db.Close()
		t.Fatal("Open() error = nil")
	}
	if !strings.Contains(err.Error(), `unknown applied migration "999_future_schema"`) {
		t.Fatalf("Open() error = %q, want unknown applied migration", err)
	}
}

func TestOpenWithNoMigrationsAllowsUnknownAppliedMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netsgo.db")
	db, err := Open(path, []Migration{{
		Name: "001_create_counter",
		Up:   `CREATE TABLE counter (id INTEGER PRIMARY KEY, value INTEGER NOT NULL);`,
	}})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations (name, applied_at) VALUES ('999_future_schema', '2026-05-15T00:00:00Z')`); err != nil {
		t.Fatalf("insert unknown migration: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	db, err = Open(path, nil)
	if err != nil {
		t.Fatalf("Open(path, nil) error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestApplyCompatibleMigrationsToleratesUnknownLedgerRows(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "netsgo.db"), nil)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	migrations := []Migration{{
		Name: "001_add_compatible_value",
		Up:   `CREATE TABLE compatible_value (id INTEGER PRIMARY KEY);`,
	}}
	if err := ApplyCompatibleMigrations(db, "compatible_migrations", migrations); err != nil {
		t.Fatalf("first ApplyCompatibleMigrations() error = %v", err)
	}
	if _, err := db.Exec(`INSERT INTO compatible_migrations (name, applied_at) VALUES ('999_future_compatible', '2026-07-21T00:00:00Z')`); err != nil {
		t.Fatalf("insert future compatible migration: %v", err)
	}
	if err := ApplyCompatibleMigrations(db, "compatible_migrations", migrations); err != nil {
		t.Fatalf("future compatible ledger row should be tolerated: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM compatible_value`).Scan(&count); err != nil {
		t.Fatalf("query compatible table: %v", err)
	}
}

func TestApplyCompatibleMigrationsRejectsInvalidLedgerName(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "netsgo.db"), nil)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	err = ApplyCompatibleMigrations(db, "schema_migrations; DROP TABLE schema_migrations", []Migration{{
		Name: "001_invalid_table",
		Up:   `SELECT 1;`,
	}})
	if err == nil || !strings.Contains(err.Error(), "invalid sqlite migration table name") {
		t.Fatalf("ApplyCompatibleMigrations() error = %v", err)
	}
}

func TestOpenRejectsUnknownStrictMigrationBeforeRunningAnyUpSQL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netsgo.db")
	newerDB, err := Open(path, []Migration{{
		Name: "001_initial",
		Up:   `CREATE TABLE initial_value (id INTEGER PRIMARY KEY);`,
	}})
	if err != nil {
		t.Fatalf("create newer DB: %v", err)
	}
	if _, err := newerDB.Exec(`INSERT INTO schema_migrations (name, applied_at) VALUES ('012_multi_user_ownership', '2026-08-04T00:00:00Z')`); err != nil {
		t.Fatalf("record newer strict migration: %v", err)
	}
	if err := newerDB.Close(); err != nil {
		t.Fatalf("close newer DB: %v", err)
	}

	olderDB, err := Open(path, []Migration{
		{Name: "001_initial", Up: `CREATE TABLE initial_value (id INTEGER PRIMARY KEY);`},
		{Name: "002_side_effect", Up: `CREATE TABLE must_not_exist (id INTEGER PRIMARY KEY);`},
	})
	if err == nil {
		_ = olderDB.Close()
		t.Fatal("Open() error = nil")
	}
	if !strings.Contains(err.Error(), `unknown applied migration "012_multi_user_ownership"`) {
		t.Fatalf("Open() error = %q, want unknown strict migration", err)
	}

	verifyDB, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly() error = %v", err)
	}
	defer func() { _ = verifyDB.Close() }()
	exists, err := TableExists(verifyDB, "must_not_exist")
	if err != nil {
		t.Fatalf("TableExists() error = %v", err)
	}
	if exists {
		t.Fatal("unknown strict migration should stop before any pending Up SQL")
	}
}

func TestApplyMigrationPlanKeepsGlobalOrderAndSeparateLedgers(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "netsgo.db"), nil)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	if err := ApplyMigrationPlan(db, []MigrationPlanItem{
		{
			Migration: Migration{Name: "001_create_order", Up: `CREATE TABLE migration_order (position INTEGER PRIMARY KEY, name TEXT NOT NULL); INSERT INTO migration_order (position, name) VALUES (1, 'strict-001');`},
			Ledger:    "schema_migrations",
			Strict:    true,
		},
		{
			Migration: Migration{Name: "010_compatible", Up: `INSERT INTO migration_order (position, name) VALUES (2, 'compatible-010');`},
			Ledger:    "schema_compatible_migrations",
			Strict:    false,
		},
		{
			Migration: Migration{Name: "012_strict", Up: `INSERT INTO migration_order (position, name) VALUES (3, 'strict-012');`},
			Ledger:    "schema_migrations",
			Strict:    true,
		},
	}); err != nil {
		t.Fatalf("ApplyMigrationPlan() error = %v", err)
	}

	var names []string
	rows, err := db.Query(`SELECT name FROM migration_order ORDER BY position`)
	if err != nil {
		t.Fatalf("query migration order: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan migration order: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migration order: %v", err)
	}
	if got, want := strings.Join(names, ","), "strict-001,compatible-010,strict-012"; got != want {
		t.Fatalf("migration execution order = %q, want %q", got, want)
	}

	assertMigrationLedgerNames(t, db, "schema_migrations", []string{"001_create_order", "012_strict"})
	assertMigrationLedgerNames(t, db, "schema_compatible_migrations", []string{"010_compatible"})
}

func TestApplyMigrationPlanRunsValidationBeforeRecordingLedger(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "netsgo.db"), nil)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	err = ApplyMigrationPlan(db, []MigrationPlanItem{{
		Migration: Migration{
			Name: "012_validation_failure",
			Up:   `CREATE TABLE must_roll_back (id INTEGER PRIMARY KEY);`,
			ValidateTx: func(*sql.Tx) error {
				return errors.New("forced validation failure")
			},
		},
		Ledger: "schema_migrations",
		Strict: true,
	}})
	if err == nil || !strings.Contains(err.Error(), "forced validation failure") {
		t.Fatalf("ApplyMigrationPlan() error = %v, want validation failure", err)
	}
	exists, err := TableExists(db, "must_roll_back")
	if err != nil {
		t.Fatalf("TableExists() error = %v", err)
	}
	if exists {
		t.Fatal("failed migration SQL should roll back")
	}
	assertMigrationLedgerNames(t, db, "schema_migrations", nil)
}

func TestApplyMigrationPlanRejectsUnknownStrictBeforeCompatibleSideEffect(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "netsgo.db"), nil)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`INSERT INTO schema_migrations (name, applied_at) VALUES ('999_future_schema', '2026-08-04T00:00:00Z')`); err != nil {
		t.Fatalf("insert unknown strict migration: %v", err)
	}

	err = ApplyMigrationPlan(db, []MigrationPlanItem{
		{
			Migration: Migration{Name: "001_known", Up: `CREATE TABLE strict_known (id INTEGER PRIMARY KEY);`},
			Ledger:    "schema_migrations",
			Strict:    true,
		},
		{
			Migration: Migration{Name: "010_compatible_side_effect", Up: `CREATE TABLE compatible_side_effect (id INTEGER PRIMARY KEY);`},
			Ledger:    "schema_compatible_migrations",
			Strict:    false,
		},
	})
	if err == nil || !strings.Contains(err.Error(), `unknown applied migration "999_future_schema"`) {
		t.Fatalf("ApplyMigrationPlan() error = %v, want unknown strict migration", err)
	}
	exists, err := TableExists(db, "compatible_side_effect")
	if err != nil {
		t.Fatalf("TableExists() error = %v", err)
	}
	if exists {
		t.Fatal("unknown strict migration should stop before compatible side effects")
	}
}

func TestOpenRejectsDuplicateMigrationNames(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "netsgo.db"), []Migration{
		{Name: "001_create_counter", Up: `CREATE TABLE counter (id INTEGER PRIMARY KEY);`},
		{Name: "001_create_counter", Up: `CREATE TABLE other_counter (id INTEGER PRIMARY KEY);`},
	})
	if err == nil {
		t.Fatal("Open() error = nil")
	}
	if !strings.Contains(err.Error(), `migration "001_create_counter" is duplicated`) {
		t.Fatalf("Open() error = %q, want duplicated migration", err)
	}
}

func TestOpenCreatesPrivateDatabaseFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix owner-only permission bits")
	}

	path := filepath.Join(t.TempDir(), "netsgo.db")
	db, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	assertPrivateFileMode(t, path)
}

func TestOpenTightensExistingDatabaseFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix owner-only permission bits")
	}

	path := filepath.Join(t.TempDir(), "netsgo.db")
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatalf("write existing DB file: %v", err)
	}

	db, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	assertPrivateFileMode(t, path)
}

func TestOpenCreatesPrivateSQLiteSidecarFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix owner-only permission bits")
	}

	path := filepath.Join(t.TempDir(), "netsgo.db")
	db, err := Open(path, []Migration{{
		Name: "001_create_widgets",
		Up:   `CREATE TABLE widgets (id TEXT PRIMARY KEY, name TEXT NOT NULL);`,
	}})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(`INSERT INTO widgets (id, name) VALUES ('w1', 'Widget')`); err != nil {
		t.Fatalf("insert into migrated table failed: %v", err)
	}

	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if _, err := os.Stat(sidecar); err == nil {
			assertPrivateFileMode(t, sidecar)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat sidecar %s: %v", sidecar, err)
		}
	}
}

func TestOpenReadOnlyDoesNotCreateDatabaseOrRunMigrations(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing", "netsgo.db")
	if _, err := OpenReadOnly(missing); !os.IsNotExist(err) {
		t.Fatalf("OpenReadOnly(missing) error = %v, want os.IsNotExist", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("OpenReadOnly should not create missing DB, stat error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "netsgo.db")
	db, err := Open(path, nil)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly(existing) error = %v", err)
	}
	defer func() { _ = ro.Close() }()

	exists, err := TableExists(ro, "widgets")
	if err != nil {
		t.Fatalf("TableExists() error = %v", err)
	}
	if exists {
		t.Fatal("OpenReadOnly should not run migrations or create unrelated tables")
	}
	if _, err := ro.Exec(`CREATE TABLE widgets (id TEXT PRIMARY KEY)`); err == nil {
		t.Fatal("OpenReadOnly should reject write statements")
	}
}

func TestOpenReadOnlyReadsExistingDatabaseAndRejectsWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netsgo.db")
	db, err := Open(path, []Migration{{
		Name: "001_create_widgets",
		Up:   `CREATE TABLE widgets (id TEXT PRIMARY KEY, name TEXT NOT NULL); INSERT INTO widgets (id, name) VALUES ('w1', 'Widget');`,
	}})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly(existing) error = %v", err)
	}
	defer func() { _ = ro.Close() }()

	var name string
	if err := ro.QueryRow(`SELECT name FROM widgets WHERE id = 'w1'`).Scan(&name); err != nil {
		t.Fatalf("read existing row through read-only DB: %v", err)
	}
	if name != "Widget" {
		t.Fatalf("read-only row name = %q, want Widget", name)
	}
	if _, err := ro.Exec(`INSERT INTO widgets (id, name) VALUES ('w2', 'Other')`); err == nil {
		t.Fatal("OpenReadOnly should reject writes")
	}
}

func TestReadOnlyDSNFormatsWindowsPaths(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "drive absolute path",
			path: `C:\Users\alice\AppData\Local\netsgo\netsgo.db`,
			want: "file:///C:/Users/alice/AppData/Local/netsgo/netsgo.db?mode=ro",
		},
		{
			name: "unc path",
			path: `\\server\share\netsgo\netsgo.db`,
			want: "file://server/share/netsgo/netsgo.db?mode=ro",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := readOnlyDSNForOS(tt.path, "windows"); got != tt.want {
				t.Fatalf("readOnlyDSNForOS() = %q, want %q", got, tt.want)
			}
		})
	}
}

func assertPragmaValue(t *testing.T, db *sql.DB, name, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`PRAGMA ` + name).Scan(&got); err != nil {
		t.Fatalf("PRAGMA %s failed: %v", name, err)
	}
	if got != want {
		t.Fatalf("PRAGMA %s = %q, want %q", name, got, want)
	}
}

func assertPrivateFileMode(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != privateFileMode {
		t.Fatalf("%s mode = %o, want %o", path, got, privateFileMode)
	}
}

func assertMigrationLedgerNames(t *testing.T, db *sql.DB, table string, want []string) {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM ` + table + ` ORDER BY name`)
	if err != nil {
		t.Fatalf("query migration ledger %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan migration ledger %s: %v", table, err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migration ledger %s: %v", table, err)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("migration ledger %s = %#v, want %#v", table, got, want)
	}
}
