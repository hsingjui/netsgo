package manage

import (
	"strings"
	"testing"

	clientstate "netsgo/internal/client"
)

func TestManageClientReauthenticateHappyPath(t *testing.T) {
	ui := &fakeUI{
		passwords: []string{" sk-new-key "},
		confirms:  []bool{true},
	}
	deps, _ := newInstalledClientDeps(t, ui)
	ui.selects = []int{clientServiceActionIndex(t, deps, "重新认证"), clientServiceActionIndex(t, deps, "返回")}
	order := []string{}
	updatedKey := ""
	deps.DisableAndStop = func() error {
		order = append(order, "stop")
		return nil
	}
	deps.UpdateClientKey = func(key string) error {
		updatedKey = key
		order = append(order, "update:"+key)
		return nil
	}
	deps.ClearClientToken = func() (clientstate.ClientIdentity, bool, error) {
		order = append(order, "clear")
		return clientstate.ClientIdentity{
			InstallID:      "install-1",
			TLSFingerprint: "sha256:old",
		}, true, nil
	}
	deps.EnableAndStart = func() error {
		order = append(order, "start")
		return nil
	}

	err := ManageClientWith(deps)
	assertSelectionExit(t, err)

	if updatedKey != "sk-new-key" {
		t.Fatalf("reauth should trim and update client key, got %q", updatedKey)
	}
	if got := strings.Join(order, ","); got != "stop,update:sk-new-key,clear,start" {
		t.Fatalf("reauth operation order = %s", got)
	}
	if len(ui.passwordCalls) != 1 || ui.passwordCalls[0].prompt != "新的 client key" {
		t.Fatalf("reauth should prompt for the new key with a password input, got %#v", ui.passwordCalls)
	}
	if ui.passwordCalls[0].opts.Validate == nil {
		t.Fatal("reauth password prompt should validate the key before mutating state")
	}
	assertConfirmPhrase(t, ui.confirmCalls, "继续重新认证 client？", "reauth client")

	if len(ui.summaries) < 2 {
		t.Fatalf("reauth should print plan and completion summaries, got %#v", ui.summaries)
	}
	completed := ui.summaries[len(ui.summaries)-1]
	if completed.title != "Client 重新认证完成" {
		t.Fatalf("reauth completion summary title = %q", completed.title)
	}
	assertSummaryCallRow(t, completed, "状态", "重新认证成功")
	assertSummaryCallRow(t, completed, "本地 token", "已清空")
	assertSummaryCallRow(t, completed, "服务", "已重启")
	for _, summary := range ui.summaries {
		assertSummaryCallDoesNotContain(t, summary, "sk-new-key")
	}
}

func TestManageClientReauthenticateRejectsEmptyKey(t *testing.T) {
	ui := &fakeUI{
		passwords: []string{" \t "},
		confirms:  []bool{true},
	}
	deps, _ := newInstalledClientDeps(t, ui)
	ui.selects = []int{clientServiceActionIndex(t, deps, "重新认证")}
	mutated := false
	deps.DisableAndStop = func() error { mutated = true; return nil }
	deps.UpdateClientKey = func(string) error { mutated = true; return nil }
	deps.ClearClientToken = func() (clientstate.ClientIdentity, bool, error) {
		mutated = true
		return clientstate.ClientIdentity{}, false, nil
	}
	deps.EnableAndStart = func() error { mutated = true; return nil }

	err := ManageClientWith(deps)
	if err == nil || !strings.Contains(err.Error(), "client key") {
		t.Fatalf("empty reauth key should fail validation, got %v", err)
	}
	if mutated {
		t.Fatal("empty reauth key should not mutate service state")
	}
	if len(ui.confirmCalls) != 0 {
		t.Fatalf("empty reauth key should fail before confirmation, got %#v", ui.confirmCalls)
	}
}

func TestManageClientReauthenticateCancelDoesNotMutate(t *testing.T) {
	ui := &fakeUI{
		passwords: []string{"sk-new-key"},
		confirms:  []bool{false},
	}
	deps, _ := newInstalledClientDeps(t, ui)
	ui.selects = []int{clientServiceActionIndex(t, deps, "重新认证"), clientServiceActionIndex(t, deps, "返回")}
	mutated := false
	deps.DisableAndStop = func() error { mutated = true; return nil }
	deps.UpdateClientKey = func(string) error { mutated = true; return nil }
	deps.ClearClientToken = func() (clientstate.ClientIdentity, bool, error) {
		mutated = true
		return clientstate.ClientIdentity{}, false, nil
	}
	deps.EnableAndStart = func() error { mutated = true; return nil }

	err := ManageClientWith(deps)
	assertSelectionExit(t, err)

	if mutated {
		t.Fatal("canceling reauth should not mutate service state")
	}
	if len(ui.summaries) < 2 || ui.summaries[len(ui.summaries)-1].title != "已取消" {
		t.Fatalf("canceling reauth should print the standard cancellation summary, got %#v", ui.summaries)
	}
	assertConfirmPhrase(t, ui.confirmCalls, "继续重新认证 client？", "reauth client")
}

func clientServiceActionIndex(t *testing.T, deps clientDeps, label string) int {
	t.Helper()
	for i, option := range serviceActionOptions(clientServiceMenuActions(deps)) {
		if option.Label == label {
			return i
		}
	}
	t.Fatalf("client action %q not found", label)
	return -1
}
