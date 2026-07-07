//go:build reauthqa

package manage

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestQAManageClientReauthenticateHappyPath(t *testing.T) {
	evidenceDir := reauthQAEvidenceDir(t)
	layout, dbPath := seedReauthQAState(t, "happy")
	ev := readReauthQABefore(t, "happy", layout, dbPath)
	ui := &fakeUI{passwords: []string{"sk-new-key"}, confirms: []bool{true}}
	deps, _ := newInstalledClientDeps(t, ui)
	ui.selects = []int{clientServiceActionIndex(t, deps, "重新认证"), clientServiceActionIndex(t, deps, "返回")}
	deps, operations := wireReauthQADeps(t, deps, layout, dbPath)

	err := ManageClientWith(deps)
	assertSelectionExit(t, err)

	ev.refreshAfter(t, layout, dbPath, operations.snapshot())
	if strings.Join(ev.Operations, ",") != "stop,update,clear,start" || !ev.KeyChanged || !ev.SettingsPreserved || !ev.TokenCleared || !ev.IdentityPreserved {
		t.Fatalf("happy reauth evidence = %+v", ev)
	}
	writeReauthQAEvidence(t, filepath.Join(evidenceDir, "reauth-c001-state.json"), ev)
	printReauthQATranscript("happy", ui, operations.snapshot())
}

func TestQAManageClientReauthenticateCancelPath(t *testing.T) {
	evidenceDir := reauthQAEvidenceDir(t)
	layout, dbPath := seedReauthQAState(t, "cancel")
	ev := readReauthQABefore(t, "cancel", layout, dbPath)
	ui := &fakeUI{passwords: []string{"sk-new-key"}, confirms: []bool{false}}
	deps, _ := newInstalledClientDeps(t, ui)
	ui.selects = []int{clientServiceActionIndex(t, deps, "重新认证"), clientServiceActionIndex(t, deps, "返回")}
	deps, operations := wireReauthQADeps(t, deps, layout, dbPath)

	err := ManageClientWith(deps)
	assertSelectionExit(t, err)

	ev.refreshAfter(t, layout, dbPath, operations.snapshot())
	if len(ev.Operations) != 0 || ev.KeyChanged || ev.TokenCleared || !ev.SettingsPreserved || !ev.IdentityPreserved {
		t.Fatalf("cancel reauth evidence = %+v", ev)
	}
	writeReauthQAEvidence(t, filepath.Join(evidenceDir, "reauth-c002-state.json"), ev)
	printReauthQATranscript("cancel", ui, operations.snapshot())
}

func TestQAManageClientReauthenticateRejectsEmptyKey(t *testing.T) {
	evidenceDir := reauthQAEvidenceDir(t)
	layout, dbPath := seedReauthQAState(t, "empty")
	ev := readReauthQABefore(t, "empty-key", layout, dbPath)
	ui := &fakeUI{passwords: []string{" \t "}, confirms: []bool{true}}
	deps, _ := newInstalledClientDeps(t, ui)
	ui.selects = []int{clientServiceActionIndex(t, deps, "重新认证")}
	deps, operations := wireReauthQADeps(t, deps, layout, dbPath)

	err := ManageClientWith(deps)
	if err == nil || !strings.Contains(err.Error(), "client key") {
		t.Fatalf("empty key error = %v, want client key validation error", err)
	}
	if got := operations.snapshot(); len(got) != 0 {
		t.Fatalf("empty key should not mutate service state, operations=%v", got)
	}
	ev.refreshAfter(t, layout, dbPath, operations.snapshot())
	if len(ev.Operations) != 0 || ev.KeyChanged || ev.TokenCleared || !ev.SettingsPreserved || !ev.IdentityPreserved {
		t.Fatalf("empty-key reauth evidence = %+v", ev)
	}
	writeReauthQAEvidence(t, filepath.Join(evidenceDir, "reauth-c002-empty-state.json"), ev)
	printReauthQAValidation(err, operations.snapshot(), ev)
}
