//go:build reauthqa

package manage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clientstate "netsgo/internal/client"
	"netsgo/internal/storage"
	"netsgo/internal/svcmgr"
)

type reauthQAEvidence struct {
	Scenario          string                     `json:"scenario"`
	Operations        []string                   `json:"operations"`
	EnvBefore         svcmgr.ClientEnv           `json:"env_before"`
	EnvAfter          svcmgr.ClientEnv           `json:"env_after"`
	IdentityBefore    clientstate.ClientIdentity `json:"identity_before"`
	IdentityBeforeOK  bool                       `json:"identity_before_ok"`
	IdentityAfter     clientstate.ClientIdentity `json:"identity_after"`
	IdentityAfterOK   bool                       `json:"identity_after_ok"`
	KeyChanged        bool                       `json:"key_changed"`
	SettingsPreserved bool                       `json:"settings_preserved"`
	TokenCleared      bool                       `json:"token_cleared"`
	IdentityPreserved bool                       `json:"identity_preserved"`
}

func wireReauthQADeps(
	t *testing.T,
	deps clientDeps,
	layout svcmgr.ServiceLayout,
	dbPath string,
) (clientDeps, *reauthQAOperations) {
	t.Helper()
	operations := &reauthQAOperations{}
	deps.DisableAndStop = func() error {
		operations.append("stop")
		return nil
	}
	deps.UpdateClientKey = func(key string) error {
		operations.append("update")
		return svcmgr.UpdateClientKey(layout, key)
	}
	deps.ClearClientToken = func() (clientstate.ClientIdentity, bool, error) {
		operations.append("clear")
		return clientstate.ClearClientToken(dbPath)
	}
	deps.EnableAndStart = func() error {
		operations.append("start")
		return nil
	}
	return deps, operations
}

type reauthQAOperations struct {
	steps []string
}

func (o *reauthQAOperations) append(step string) {
	o.steps = append(o.steps, step)
}

func (o *reauthQAOperations) snapshot() []string {
	if len(o.steps) == 0 {
		return []string{}
	}
	return append([]string(nil), o.steps...)
}

func seedReauthQAState(t *testing.T, scenario string) (svcmgr.ServiceLayout, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), scenario)
	layout := svcmgr.NewLayout(svcmgr.RoleClient)
	layout.EnvPath = filepath.Join(dir, "client.env")
	if err := svcmgr.WriteClientEnv(layout, svcmgr.ClientEnv{
		Server:         "wss://panel.example.com",
		Key:            "sk-old",
		TLSSkipVerify:  true,
		TLSFingerprint: "sha256:old",
	}); err != nil {
		t.Fatalf("seed client env: %v", err)
	}
	dbPath := filepath.Join(dir, "client", clientstate.ClientDBFileName)
	db, err := storage.Open(dbPath, clientQAMigrations())
	if err != nil {
		t.Fatalf("open client state fixture: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`INSERT INTO client_identity (id, install_id, token, tls_fingerprint)
VALUES (1, 'install-qa', 'tk-old', 'sha256:old')
ON CONFLICT(id) DO UPDATE SET
	install_id = excluded.install_id,
	token = excluded.token,
	tls_fingerprint = excluded.tls_fingerprint`); err != nil {
		t.Fatalf("insert client identity fixture: %v", err)
	}
	return layout, dbPath
}

func clientQAMigrations() []storage.Migration {
	return []storage.Migration{{
		Name: "001_client_identity",
		Up: `
CREATE TABLE client_identity (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	install_id TEXT NOT NULL,
	token TEXT NOT NULL DEFAULT '',
	tls_fingerprint TEXT NOT NULL DEFAULT ''
);
`,
	}}
}

func readReauthQABefore(t *testing.T, scenario string, layout svcmgr.ServiceLayout, dbPath string) reauthQAEvidence {
	t.Helper()
	envBefore, err := svcmgr.ReadClientEnv(layout)
	if err != nil {
		t.Fatalf("read env before: %v", err)
	}
	identityBefore, ok, err := clientstate.LoadClientIdentity(dbPath)
	if err != nil {
		t.Fatalf("read identity before: %v", err)
	}
	return reauthQAEvidence{
		Scenario:         scenario,
		Operations:       []string{},
		EnvBefore:        envBefore,
		IdentityBefore:   identityBefore,
		IdentityBeforeOK: ok,
	}
}

func (ev *reauthQAEvidence) refreshAfter(
	t *testing.T,
	layout svcmgr.ServiceLayout,
	dbPath string,
	operations []string,
) {
	t.Helper()
	envAfter, err := svcmgr.ReadClientEnv(layout)
	if err != nil {
		t.Fatalf("read env after: %v", err)
	}
	identityAfter, ok, err := clientstate.LoadClientIdentity(dbPath)
	if err != nil {
		t.Fatalf("read identity after: %v", err)
	}
	ev.Operations = []string{}
	ev.Operations = append(ev.Operations, operations...)
	ev.EnvAfter = envAfter
	ev.IdentityAfter = identityAfter
	ev.IdentityAfterOK = ok
	ev.KeyChanged = ev.EnvBefore.Key != ev.EnvAfter.Key
	ev.SettingsPreserved = ev.EnvBefore.Server == ev.EnvAfter.Server &&
		ev.EnvBefore.TLSSkipVerify == ev.EnvAfter.TLSSkipVerify &&
		ev.EnvBefore.TLSFingerprint == ev.EnvAfter.TLSFingerprint
	ev.TokenCleared = ev.IdentityBefore.Token != "" && ev.IdentityAfter.Token == ""
	ev.IdentityPreserved = ev.IdentityBeforeOK == ev.IdentityAfterOK &&
		ev.IdentityBefore.InstallID == ev.IdentityAfter.InstallID &&
		ev.IdentityBefore.TLSFingerprint == ev.IdentityAfter.TLSFingerprint
}

func reauthQAEvidenceDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("NETSGO_REAUTH_QA_EVIDENCE_DIR")
	if dir == "" {
		t.Fatal("NETSGO_REAUTH_QA_EVIDENCE_DIR is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create evidence dir: %v", err)
	}
	return dir
}

func writeReauthQAEvidence(t *testing.T, path string, ev reauthQAEvidence) {
	t.Helper()
	data, err := json.MarshalIndent(ev, "", "  ")
	if err != nil {
		t.Fatalf("marshal evidence: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write evidence: %v", err)
	}
}

func printReauthQATranscript(scenario string, ui *fakeUI, operations []string) {
	fmt.Printf("QA scenario=%s actual_entry=ManageClientWith option=重新认证 operations=%s\n", scenario, strings.Join(operations, ","))
	for _, summary := range ui.summaries {
		fmt.Printf("SUMMARY %s\n", summary.title)
		for _, row := range summary.rows {
			fmt.Printf("%s=%s\n", row[0], row[1])
		}
	}
}

func printReauthQAValidation(err error, operations []string, ev reauthQAEvidence) {
	fmt.Printf(
		"QA scenario=empty-key validation_error=%q operations=%v key_changed=%v token_cleared=%v identity_preserved=%v\n",
		err.Error(),
		operations,
		ev.KeyChanged,
		ev.TokenCleared,
		ev.IdentityPreserved,
	)
}
