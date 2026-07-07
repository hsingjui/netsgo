package svcmgr

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestUpdateClientKeyPreservesClientEnvSettings(t *testing.T) {
	stubLookupSystemUser(t, strconv.Itoa(os.Getgid()))

	layout := NewLayout(RoleClient)
	layout.EnvPath = filepath.Join(t.TempDir(), "client.env")
	fixture := strings.Join([]string{
		"# keep this comment",
		"NETSGO_SERVER=wss://panel.example.com",
		"NETSGO_KEY=sk-old",
		"NETSGO_TLS_SKIP_VERIFY=true",
		"NETSGO_TLS_FINGERPRINT=sha256:old",
		"",
	}, "\n")
	if err := os.WriteFile(layout.EnvPath, []byte(fixture), 0o640); err != nil {
		t.Fatalf("write env fixture: %v", err)
	}

	if err := UpdateClientKey(layout, " sk-new "); err != nil {
		t.Fatalf("UpdateClientKey() failed: %v", err)
	}

	got, err := ReadClientEnv(layout)
	if err != nil {
		t.Fatalf("ReadClientEnv() failed: %v", err)
	}
	want := ClientEnv{
		Server:         "wss://panel.example.com",
		Key:            "sk-new",
		TLSSkipVerify:  true,
		TLSFingerprint: "sha256:old",
	}
	if got != want {
		t.Fatalf("UpdateClientKey() env = %#v, want %#v", got, want)
	}
}

func TestUpdateClientKeyRejectsEmptyKey(t *testing.T) {
	layout := NewLayout(RoleClient)
	layout.EnvPath = filepath.Join(t.TempDir(), "client.env")
	if err := os.WriteFile(layout.EnvPath, []byte("NETSGO_KEY=sk-old\n"), 0o640); err != nil {
		t.Fatalf("write env fixture: %v", err)
	}

	if err := UpdateClientKey(layout, " \t "); err == nil {
		t.Fatal("UpdateClientKey should reject an empty key")
	}

	got, err := ReadClientEnv(layout)
	if err != nil {
		t.Fatalf("ReadClientEnv() failed: %v", err)
	}
	if got.Key != "sk-old" {
		t.Fatalf("empty key should not mutate env, got %#v", got)
	}
}
