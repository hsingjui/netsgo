package updater

import (
	"fmt"
	"net"
	"net/http"
	"netsgo/internal/svcmgr"
	"netsgo/pkg/flock"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "netsgo-updater-tests-")
	if err != nil {
		panic(err)
	}
	upgradeLockPathFor = func() string {
		return filepath.Join(root, "upgrade.lock")
	}
	upgradeBackupDir = filepath.Join(root, "backups")
	verifyStartedServicesFunc = func([]string) error { return nil }
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}

func TestServerDatabaseSnapshotRestoresInitiallyMissingDatabase(t *testing.T) {
	originalNewServiceLayout := newServiceLayoutFunc
	t.Cleanup(func() { newServiceLayoutFunc = originalNewServiceLayout })

	runtimeDir := filepath.Join(t.TempDir(), "server")
	if err := os.MkdirAll(runtimeDir, 0o750); err != nil {
		t.Fatal(err)
	}
	newServiceLayoutFunc = func(role svcmgr.Role) svcmgr.ServiceLayout {
		layout := svcmgr.NewLayout(role)
		layout.RuntimeDir = runtimeDir
		return layout
	}

	snapshot, err := snapshotServerDatabase([]string{svcmgr.UnitName(svcmgr.RoleServer)}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || len(snapshot.files) != len(serverDatabaseSuffixes) {
		t.Fatalf("missing database snapshot = %+v", snapshot)
	}
	for _, entry := range snapshot.files {
		if entry.backupPath != "" {
			t.Fatalf("initially absent database file has backup path %q", entry.backupPath)
		}
		if err := os.WriteFile(entry.targetPath, []byte("created by new version"), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	if err := snapshot.restore(); err != nil {
		t.Fatal(err)
	}
	for _, entry := range snapshot.files {
		if _, err := os.Lstat(entry.targetPath); !os.IsNotExist(err) {
			t.Fatalf("upgrade-created database file still exists at %s: %v", entry.targetPath, err)
		}
	}
}

func TestUpgradeRollsBackWhenStartedServicesAreUnhealthy(t *testing.T) {
	origDisableAndStop := disableAndStopFunc
	origEnableAndStart := enableAndStartFunc
	origDetectInstalledUnits := detectInstalledUnitsFunc
	origBinaryPath := installedBinaryPath
	origBackupDir := upgradeBackupDir
	origVerifyStartedServices := verifyStartedServicesFunc
	origRepairServiceEnvFiles := repairServiceEnvFilesFunc
	origNewServiceLayout := newServiceLayoutFunc
	t.Cleanup(func() {
		disableAndStopFunc = origDisableAndStop
		enableAndStartFunc = origEnableAndStart
		detectInstalledUnitsFunc = origDetectInstalledUnits
		installedBinaryPath = origBinaryPath
		upgradeBackupDir = origBackupDir
		verifyStartedServicesFunc = origVerifyStartedServices
		repairServiceEnvFilesFunc = origRepairServiceEnvFiles
		newServiceLayoutFunc = origNewServiceLayout
	})

	tmpDir := t.TempDir()
	installedPath := filepath.Join(tmpDir, "installed-netsgo")
	newPath := filepath.Join(tmpDir, "new-netsgo")
	serverRuntimeDir := filepath.Join(tmpDir, "server")
	serverDBPath := filepath.Join(serverRuntimeDir, "netsgo.db")
	if err := os.WriteFile(installedPath, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(serverRuntimeDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serverDBPath, []byte("old database"), 0o640); err != nil {
		t.Fatal(err)
	}
	installedBinaryPath = installedPath
	upgradeBackupDir = filepath.Join(tmpDir, "backups")

	units := []string{svcmgr.UnitName(svcmgr.RoleServer), svcmgr.UnitName(svcmgr.RoleClient)}
	detectInstalledUnitsFunc = func() []string { return units }
	newServiceLayoutFunc = func(role svcmgr.Role) svcmgr.ServiceLayout {
		layout := svcmgr.NewLayout(role)
		layout.EnvPath = filepath.Join(tmpDir, string(role)+".env")
		if role == svcmgr.RoleServer {
			layout.RuntimeDir = serverRuntimeDir
		}
		return layout
	}
	repairServiceEnvFilesFunc = func([]string) error { return nil }
	verifyStartedServicesFunc = func(got []string) error {
		if fmt.Sprint(got) != fmt.Sprint(units) {
			t.Fatalf("health check units = %v, want %v", got, units)
		}
		if err := os.WriteFile(serverDBPath, []byte("migrated database"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(serverDBPath+"-wal", []byte("new wal"), 0o640); err != nil {
			t.Fatal(err)
		}
		return fmt.Errorf("server exited during startup")
	}

	var stopCalls []string
	var startCalls []string
	disableAndStopFunc = func(unit string) error {
		stopCalls = append(stopCalls, unit)
		return nil
	}
	enableAndStartFunc = func(unit string) error {
		startCalls = append(startCalls, unit)
		return nil
	}

	result, err := Upgrade(newPath, "v1.0.0", "v1.1.0")
	if err == nil {
		t.Fatal("expected health check error")
	}
	for _, want := range []string{"未通过健康检查", "已自动恢复旧二进制", "journalctl", "migration 012", "admin_users"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("health check error should contain %q, got %v", want, err)
		}
	}
	data, readErr := os.ReadFile(installedPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "old binary" {
		t.Fatalf("expected old binary restored, got %q", data)
	}
	databaseData, readErr := os.ReadFile(serverDBPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(databaseData) != "old database" {
		t.Fatalf("expected old database restored, got %q", databaseData)
	}
	if _, statErr := os.Stat(serverDBPath + "-wal"); !os.IsNotExist(statErr) {
		t.Fatalf("expected upgrade-created WAL to be removed, stat error = %v", statErr)
	}
	if len(stopCalls) != 4 || fmt.Sprint(stopCalls[2:]) != fmt.Sprint([]string{units[1], units[0]}) {
		t.Fatalf("expected started services stopped in reverse order during rollback, got %v", stopCalls)
	}
	wantStarts := append(append([]string{}, units...), units...)
	if fmt.Sprint(startCalls) != fmt.Sprint(wantStarts) {
		t.Fatalf("expected new start followed by old-version restart, got %v", startCalls)
	}
	backupData, readErr := os.ReadFile(result.BackupPath)
	if readErr != nil {
		t.Fatalf("read retained backup: %v", readErr)
	}
	if string(backupData) != "old binary" {
		t.Fatalf("retained backup = %q, want old binary", backupData)
	}
}

func TestUpgradeRetainsOnlyLatestOldBinaryBackupOnSuccess(t *testing.T) {
	origDisableAndStop := disableAndStopFunc
	origEnableAndStart := enableAndStartFunc
	origDetectInstalledUnits := detectInstalledUnitsFunc
	origBinaryPath := installedBinaryPath
	origBackupDir := upgradeBackupDir
	origRepairServiceEnvFiles := repairServiceEnvFilesFunc
	t.Cleanup(func() {
		disableAndStopFunc = origDisableAndStop
		enableAndStartFunc = origEnableAndStart
		detectInstalledUnitsFunc = origDetectInstalledUnits
		installedBinaryPath = origBinaryPath
		upgradeBackupDir = origBackupDir
		repairServiceEnvFilesFunc = origRepairServiceEnvFiles
	})

	tmpDir := t.TempDir()
	installedPath := filepath.Join(tmpDir, "installed-netsgo")
	newPath := filepath.Join(tmpDir, "new-netsgo")
	backupDir := filepath.Join(tmpDir, "backups")
	if err := os.WriteFile(installedPath, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, upgradeBackupPrefix+"v0.9.0"), []byte("stale binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	installedBinaryPath = installedPath
	upgradeBackupDir = backupDir
	detectInstalledUnitsFunc = func() []string { return []string{svcmgr.UnitName(svcmgr.RoleServer)} }
	disableAndStopFunc = func(string) error { return nil }
	enableAndStartFunc = func(string) error { return nil }
	repairServiceEnvFilesFunc = func([]string) error { return nil }

	result, err := Upgrade(newPath, "v1.0.0", "v1.1.0")
	if err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}
	wantPath := filepath.Join(backupDir, upgradeBackupPrefix+"v1.0.0")
	if result.BackupPath != wantPath {
		t.Fatalf("backup path = %q, want %q", result.BackupPath, wantPath)
	}
	backupData, err := os.ReadFile(result.BackupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backupData) != "old binary" {
		t.Fatalf("backup content = %q, want old binary", backupData)
	}
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(wantPath) {
		t.Fatalf("expected only latest backup, got %v", entries)
	}
}

func TestVerifyStartedServicesRequiresAllUnitsAndServerProbeToRemainStable(t *testing.T) {
	units := []string{svcmgr.UnitName(svcmgr.RoleServer), svcmgr.UnitName(svcmgr.RoleClient)}
	now := time.Unix(0, 0)
	checks := 0
	serverProbes := 0
	err := verifyStartedServices(units, serviceHealthCheckDeps{
		IsActive: func(unit string) (bool, error) {
			if unit == units[0] {
				checks++
			}
			return unit != units[1] || checks > 1, nil
		},
		ProbeServer: func() error {
			serverProbes++
			if serverProbes == 1 {
				return fmt.Errorf("connection refused")
			}
			return nil
		},
		Now: func() time.Time { return now },
		Sleep: func(duration time.Duration) {
			now = now.Add(duration)
		},
	}, 10*time.Second, 2*time.Second, time.Second)
	if err != nil {
		t.Fatalf("verifyStartedServices() error = %v", err)
	}
	if checks < 5 {
		t.Fatalf("expected repeated checks through stable window, got %d", checks)
	}
	if serverProbes < 3 {
		t.Fatalf("expected repeated server probes through stable window, got %d", serverProbes)
	}
}

func TestVerifyStartedServicesTimesOutWhenClientNeverBecomesActive(t *testing.T) {
	unit := svcmgr.UnitName(svcmgr.RoleClient)
	now := time.Unix(0, 0)
	err := verifyStartedServices([]string{unit}, serviceHealthCheckDeps{
		IsActive: func(string) (bool, error) { return false, nil },
		ProbeServer: func() error {
			t.Fatal("client-only health check must not probe the server port")
			return nil
		},
		Now: func() time.Time { return now },
		Sleep: func(duration time.Duration) {
			now = now.Add(duration)
		},
	}, 3*time.Second, time.Second, time.Second)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	for _, want := range []string{"3s", unit, "未处于 active 状态"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("timeout error should contain %q, got %v", want, err)
		}
	}
}

func TestProbeServerManagementAPIAcceptsInternalEndpointResponse(t *testing.T) {
	origNewServiceLayout := newServiceLayoutFunc
	t.Cleanup(func() {
		newServiceLayoutFunc = origNewServiceLayout
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws/data" {
			t.Errorf("probe path = %q, want /ws/data", r.URL.Path)
		}
		if got := r.Header.Get("Sec-WebSocket-Protocol"); got != "netsgo-data.v1" {
			t.Errorf("probe subprotocol = %q, want netsgo-data.v1", got)
		}
		w.WriteHeader(http.StatusUpgradeRequired)
	})}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		<-serverDone
	})

	port := listener.Addr().(*net.TCPAddr).Port
	envPath := filepath.Join(t.TempDir(), "server.env")
	if err := os.WriteFile(envPath, []byte(fmt.Sprintf("NETSGO_PORT=%d\nNETSGO_TLS_MODE=off\n", port)), 0o640); err != nil {
		t.Fatal(err)
	}
	newServiceLayoutFunc = func(role svcmgr.Role) svcmgr.ServiceLayout {
		layout := svcmgr.NewLayout(role)
		layout.EnvPath = envPath
		return layout
	}

	if err := probeServerManagementAPI(); err != nil {
		t.Fatalf("426 response should prove the server is ready, got %v", err)
	}
}

func TestReplaceBinary(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "netsgo")
	dstPath := filepath.Join(dstDir, "netsgo")

	_ = os.WriteFile(srcPath, []byte("new binary"), 0o755)
	_ = os.WriteFile(dstPath, []byte("old binary"), 0o755)

	err := replaceBinary(srcPath, dstPath)
	if err != nil {
		t.Fatalf("replaceBinary: %v", err)
	}

	data, _ := os.ReadFile(dstPath)
	if string(data) != "new binary" {
		t.Fatalf("binary not replaced")
	}
}

func TestReplaceBinaryCleansTempFileOnRenameError(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "netsgo")
	dstPath := filepath.Join(dstDir, "existing-dir")

	if err := os.WriteFile(srcPath, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dstPath, 0o755); err != nil {
		t.Fatal(err)
	}

	err := replaceBinary(srcPath, dstPath)
	if err == nil {
		t.Fatal("expected rename error")
	}
	matches, globErr := filepath.Glob(filepath.Join(dstDir, "."+filepath.Base(dstPath)+".tmp-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("expected temp files to be cleaned up, got %v", matches)
	}
}

func TestUpgradeRestartsStoppedServicesWhenReplaceFails(t *testing.T) {
	origDisableAndStop := disableAndStopFunc
	origEnableAndStart := enableAndStartFunc
	origDetectInstalledUnits := detectInstalledUnitsFunc
	origReplaceBinary := replaceBinaryFunc
	t.Cleanup(func() {
		disableAndStopFunc = origDisableAndStop
		enableAndStartFunc = origEnableAndStart
		detectInstalledUnitsFunc = origDetectInstalledUnits
		replaceBinaryFunc = origReplaceBinary
	})

	var restarted []string
	detectInstalledUnitsFunc = func() []string {
		return []string{"netsgo-server.service"}
	}
	disableAndStopFunc = func(unit string) error { return nil }
	enableAndStartFunc = func(unit string) error {
		restarted = append(restarted, unit)
		return nil
	}
	replaceBinaryFunc = func(srcPath, dstPath string) error {
		return fmt.Errorf("replace failed")
	}

	_, err := Upgrade("/tmp/netsgo", "1.0.0", "1.1.0")
	if err == nil {
		t.Fatal("expected error")
	}
	if len(restarted) != 1 || restarted[0] != "netsgo-server.service" {
		t.Fatalf("expected service restart rollback, got %v", restarted)
	}
}

func TestUpgradeReturnsProvidedVersionFields(t *testing.T) {
	origDisableAndStop := disableAndStopFunc
	origEnableAndStart := enableAndStartFunc
	origDetectInstalledUnits := detectInstalledUnitsFunc
	origBinaryPath := installedBinaryPath
	origRepairServiceEnvFiles := repairServiceEnvFilesFunc
	t.Cleanup(func() {
		disableAndStopFunc = origDisableAndStop
		enableAndStartFunc = origEnableAndStart
		detectInstalledUnitsFunc = origDetectInstalledUnits
		installedBinaryPath = origBinaryPath
		repairServiceEnvFilesFunc = origRepairServiceEnvFiles
	})

	tmpDir := t.TempDir()
	installedPath := filepath.Join(tmpDir, "installed-netsgo")
	newPath := filepath.Join(tmpDir, "new-netsgo")
	if err := os.WriteFile(installedPath, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	installedBinaryPath = installedPath

	detectInstalledUnitsFunc = func() []string { return []string{"netsgo-server.service"} }
	disableAndStopFunc = func(unit string) error { return nil }
	enableAndStartFunc = func(unit string) error { return nil }
	repairServiceEnvFilesFunc = func(units []string) error { return nil }

	result, err := Upgrade(newPath, "1.0.0", "1.1.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.OldVersion != "1.0.0" || result.NewVersion != "1.1.0" {
		t.Fatalf("unexpected versions: old=%q new=%q", result.OldVersion, result.NewVersion)
	}
	if len(result.Stopped) != 1 || result.Stopped[0] != "netsgo-server.service" {
		t.Fatalf("unexpected stopped services: %v", result.Stopped)
	}
	if len(result.Started) != 1 || result.Started[0] != "netsgo-server.service" {
		t.Fatalf("unexpected started services: %v", result.Started)
	}
}

func TestUpgradeRepairsServiceEnvFilesBeforeRestart(t *testing.T) {
	origDisableAndStop := disableAndStopFunc
	origEnableAndStart := enableAndStartFunc
	origDetectInstalledUnits := detectInstalledUnitsFunc
	origBinaryPath := installedBinaryPath
	origRepairServiceEnvFiles := repairServiceEnvFilesFunc
	t.Cleanup(func() {
		disableAndStopFunc = origDisableAndStop
		enableAndStartFunc = origEnableAndStart
		detectInstalledUnitsFunc = origDetectInstalledUnits
		installedBinaryPath = origBinaryPath
		repairServiceEnvFilesFunc = origRepairServiceEnvFiles
	})

	tmpDir := t.TempDir()
	installedPath := filepath.Join(tmpDir, "installed-netsgo")
	newPath := filepath.Join(tmpDir, "new-netsgo")
	if err := os.WriteFile(installedPath, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	installedBinaryPath = installedPath

	units := []string{"netsgo-server.service", "netsgo-client.service"}
	var events []string
	detectInstalledUnitsFunc = func() []string { return units }
	disableAndStopFunc = func(unit string) error {
		events = append(events, "stop:"+unit)
		return nil
	}
	repairServiceEnvFilesFunc = func(got []string) error {
		if fmt.Sprint(got) != fmt.Sprint(units) {
			t.Fatalf("repair units = %v, want %v", got, units)
		}
		events = append(events, "repair")
		return nil
	}
	enableAndStartFunc = func(unit string) error {
		events = append(events, "start:"+unit)
		return nil
	}

	if _, err := Upgrade(newPath, "1.0.0", "1.1.0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{
		"stop:netsgo-server.service",
		"stop:netsgo-client.service",
		"repair",
		"start:netsgo-server.service",
		"start:netsgo-client.service",
	}
	if fmt.Sprint(events) != fmt.Sprint(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestRepairServiceEnvFilesEnablesServerLoopbackManagementHost(t *testing.T) {
	origNewServiceLayout := newServiceLayoutFunc
	t.Cleanup(func() {
		newServiceLayoutFunc = origNewServiceLayout
	})

	tmpDir := t.TempDir()
	serverEnvPath := filepath.Join(tmpDir, "server.env")
	clientEnvPath := filepath.Join(tmpDir, "client.env")
	if err := os.WriteFile(serverEnvPath, []byte("NETSGO_PORT=9527\nNETSGO_ALLOW_LOOPBACK_MANAGEMENT_HOST=false\nNETSGO_CUSTOM=value\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clientEnvPath, []byte("NETSGO_SERVER=https://panel.example.com\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	newServiceLayoutFunc = func(role svcmgr.Role) svcmgr.ServiceLayout {
		layout := svcmgr.NewLayout(role)
		switch role {
		case svcmgr.RoleServer:
			layout.EnvPath = serverEnvPath
		case svcmgr.RoleClient:
			layout.EnvPath = clientEnvPath
		}
		return layout
	}

	err := repairServiceEnvFiles([]string{svcmgr.UnitName(svcmgr.RoleServer), svcmgr.UnitName(svcmgr.RoleClient)})
	if err != nil {
		t.Fatalf("repairServiceEnvFiles() failed: %v", err)
	}
	serverContent, err := os.ReadFile(serverEnvPath)
	if err != nil {
		t.Fatal(err)
	}
	serverText := string(serverContent)
	if !strings.Contains(serverText, "NETSGO_ALLOW_LOOPBACK_MANAGEMENT_HOST=true\n") {
		t.Fatalf("server env should enable loopback management Host after upgrade, got %q", serverText)
	}
	if !strings.Contains(serverText, "NETSGO_CUSTOM=value") {
		t.Fatalf("server env should preserve unrelated entries, got %q", serverText)
	}
	clientContent, err := os.ReadFile(clientEnvPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(clientContent) != "NETSGO_SERVER=https://panel.example.com\n" {
		t.Fatalf("client env should remain unchanged, got %q", string(clientContent))
	}
}

func TestUpgradeRollsBackWhenServiceEnvRepairFails(t *testing.T) {
	origDisableAndStop := disableAndStopFunc
	origEnableAndStart := enableAndStartFunc
	origDetectInstalledUnits := detectInstalledUnitsFunc
	origBinaryPath := installedBinaryPath
	origRepairServiceEnvFiles := repairServiceEnvFilesFunc
	origNewServiceLayout := newServiceLayoutFunc
	t.Cleanup(func() {
		disableAndStopFunc = origDisableAndStop
		enableAndStartFunc = origEnableAndStart
		detectInstalledUnitsFunc = origDetectInstalledUnits
		installedBinaryPath = origBinaryPath
		repairServiceEnvFilesFunc = origRepairServiceEnvFiles
		newServiceLayoutFunc = origNewServiceLayout
	})

	tmpDir := t.TempDir()
	installedPath := filepath.Join(tmpDir, "installed-netsgo")
	newPath := filepath.Join(tmpDir, "new-netsgo")
	serverEnvPath := filepath.Join(tmpDir, "server.env")
	if err := os.WriteFile(installedPath, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serverEnvPath, []byte("NETSGO_ALLOW_LOOPBACK_MANAGEMENT_HOST=false\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	installedBinaryPath = installedPath
	newServiceLayoutFunc = func(role svcmgr.Role) svcmgr.ServiceLayout {
		layout := svcmgr.NewLayout(role)
		if role == svcmgr.RoleServer {
			layout.EnvPath = serverEnvPath
		}
		return layout
	}

	var restarted []string
	detectInstalledUnitsFunc = func() []string { return []string{"netsgo-server.service"} }
	disableAndStopFunc = func(unit string) error { return nil }
	enableAndStartFunc = func(unit string) error {
		restarted = append(restarted, unit)
		return nil
	}
	repairServiceEnvFilesFunc = func([]string) error { return fmt.Errorf("repair failed") }

	_, err := Upgrade(newPath, "1.0.0", "1.1.0")
	if err == nil {
		t.Fatal("expected error")
	}
	data, err := os.ReadFile(installedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old binary" {
		t.Fatalf("expected old binary restored after repair failure, got %q", string(data))
	}
	envData, err := os.ReadFile(serverEnvPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(envData) != "NETSGO_ALLOW_LOOPBACK_MANAGEMENT_HOST=false\n" {
		t.Fatalf("expected service env restored after repair failure, got %q", string(envData))
	}
	if len(restarted) != 1 || restarted[0] != "netsgo-server.service" {
		t.Fatalf("expected stopped service to restart after repair failure, got %v", restarted)
	}
}

func TestUpgradeRestoresOldBinaryWhenStartFails(t *testing.T) {
	origDisableAndStop := disableAndStopFunc
	origEnableAndStart := enableAndStartFunc
	origDetectInstalledUnits := detectInstalledUnitsFunc
	origBinaryPath := installedBinaryPath
	origNewServiceLayout := newServiceLayoutFunc
	t.Cleanup(func() {
		disableAndStopFunc = origDisableAndStop
		enableAndStartFunc = origEnableAndStart
		detectInstalledUnitsFunc = origDetectInstalledUnits
		installedBinaryPath = origBinaryPath
		newServiceLayoutFunc = origNewServiceLayout
	})

	tmpDir := t.TempDir()
	installedPath := filepath.Join(tmpDir, "installed-netsgo")
	newPath := filepath.Join(tmpDir, "new-netsgo")
	serverEnvPath := filepath.Join(tmpDir, "server.env")
	if err := os.WriteFile(installedPath, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serverEnvPath, []byte("NETSGO_ALLOW_LOOPBACK_MANAGEMENT_HOST=false\nNETSGO_CUSTOM=value\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	installedBinaryPath = installedPath
	newServiceLayoutFunc = func(role svcmgr.Role) svcmgr.ServiceLayout {
		layout := svcmgr.NewLayout(role)
		if role == svcmgr.RoleServer {
			layout.EnvPath = serverEnvPath
		}
		return layout
	}

	detectInstalledUnitsFunc = func() []string { return []string{"netsgo-server.service"} }
	disableAndStopFunc = func(unit string) error { return nil }
	enableAndStartFunc = func(unit string) error { return fmt.Errorf("start failed") }

	_, err := Upgrade(newPath, "1.0.0", "1.1.0")
	if err == nil {
		t.Fatal("expected error")
	}

	data, err := os.ReadFile(installedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old binary" {
		t.Fatalf("expected old binary restored, got %q", string(data))
	}
	envData, err := os.ReadFile(serverEnvPath)
	if err != nil {
		t.Fatal(err)
	}
	envText := string(envData)
	if !strings.Contains(envText, "NETSGO_ALLOW_LOOPBACK_MANAGEMENT_HOST=false\n") {
		t.Fatalf("expected service env restored after start failure, got %q", envText)
	}
	if !strings.Contains(envText, "NETSGO_CUSTOM=value") {
		t.Fatalf("expected service env custom entries preserved after rollback, got %q", envText)
	}
}

func TestUpgradeStopsAlreadyStartedServicesBeforeRollback(t *testing.T) {
	origDisableAndStop := disableAndStopFunc
	origEnableAndStart := enableAndStartFunc
	origDetectInstalledUnits := detectInstalledUnitsFunc
	origBinaryPath := installedBinaryPath
	t.Cleanup(func() {
		disableAndStopFunc = origDisableAndStop
		enableAndStartFunc = origEnableAndStart
		detectInstalledUnitsFunc = origDetectInstalledUnits
		installedBinaryPath = origBinaryPath
	})

	tmpDir := t.TempDir()
	installedPath := filepath.Join(tmpDir, "installed-netsgo")
	newPath := filepath.Join(tmpDir, "new-netsgo")
	if err := os.WriteFile(installedPath, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	installedBinaryPath = installedPath

	var stoppedAgain []string
	var startCalls []string
	detectInstalledUnitsFunc = func() []string { return []string{"netsgo-server.service", "netsgo-client.service"} }
	disableAndStopFunc = func(unit string) error {
		stoppedAgain = append(stoppedAgain, unit)
		return nil
	}
	enableAndStartFunc = func(unit string) error {
		startCalls = append(startCalls, unit)
		if unit == "netsgo-client.service" {
			return fmt.Errorf("start failed")
		}
		return nil
	}

	_, err := Upgrade(newPath, "1.0.0", "1.1.0")
	if err == nil {
		t.Fatal("expected error")
	}
	if len(startCalls) < 2 || startCalls[0] != "netsgo-server.service" || startCalls[1] != "netsgo-client.service" {
		t.Fatalf("unexpected start order: %v", startCalls)
	}
	if len(stoppedAgain) != 3 {
		t.Fatalf("expected original stop plus rollback stop, got %v", stoppedAgain)
	}
	if stoppedAgain[2] != "netsgo-server.service" {
		t.Fatalf("expected already-started service to be stopped during rollback, got %v", stoppedAgain)
	}
}

func TestStopStartedServicesAttemptsEveryUnitAfterFailure(t *testing.T) {
	units := []string{"netsgo-server.service", "netsgo-client.service"}
	var calls []string
	orch := &Orchestrator{DisableAndStop: func(unit string) error {
		calls = append(calls, unit)
		if unit == units[1] {
			return fmt.Errorf("client stop failed")
		}
		return nil
	}}
	err := orch.StopStartedServices(units)
	if err == nil || !strings.Contains(err.Error(), "client stop failed") {
		t.Fatalf("StopStartedServices() error = %v", err)
	}
	want := []string{units[1], units[0]}
	if fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Fatalf("stop calls = %v, want %v", calls, want)
	}
}

func TestUpgradeRestartsStoppedServicesWhenPanicOccurs(t *testing.T) {
	origDisableAndStop := disableAndStopFunc
	origEnableAndStart := enableAndStartFunc
	origDetectInstalledUnits := detectInstalledUnitsFunc
	origReplaceBinary := replaceBinaryFunc
	origBinaryPath := installedBinaryPath
	t.Cleanup(func() {
		disableAndStopFunc = origDisableAndStop
		enableAndStartFunc = origEnableAndStart
		detectInstalledUnitsFunc = origDetectInstalledUnits
		replaceBinaryFunc = origReplaceBinary
		installedBinaryPath = origBinaryPath
	})

	tmpDir := t.TempDir()
	installedBinaryPath = filepath.Join(tmpDir, "installed-netsgo")
	if err := os.WriteFile(installedBinaryPath, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	var restarted []string
	detectInstalledUnitsFunc = func() []string { return []string{"netsgo-server.service", "netsgo-client.service"} }
	disableAndStopFunc = func(unit string) error { return nil }
	enableAndStartFunc = func(unit string) error {
		restarted = append(restarted, unit)
		return nil
	}
	replaceBinaryFunc = func(srcPath, dstPath string) error {
		panic("replace panic")
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		if len(restarted) != 2 || restarted[0] != "netsgo-server.service" || restarted[1] != "netsgo-client.service" {
			t.Fatalf("expected stopped services to be restarted in rollback order, got %v", restarted)
		}
	}()

	_, _ = Upgrade("/tmp/netsgo", "1.0.0", "1.1.0")
}

func TestUpgradeRestartsOnlyPartiallyStoppedServicesWhenStopPanics(t *testing.T) {
	origDisableAndStop := disableAndStopFunc
	origEnableAndStart := enableAndStartFunc
	origDetectInstalledUnits := detectInstalledUnitsFunc
	t.Cleanup(func() {
		disableAndStopFunc = origDisableAndStop
		enableAndStartFunc = origEnableAndStart
		detectInstalledUnitsFunc = origDetectInstalledUnits
	})

	var restarted []string
	detectInstalledUnitsFunc = func() []string { return []string{"netsgo-server.service", "netsgo-client.service"} }
	disableAndStopFunc = func(unit string) error {
		if unit == "netsgo-client.service" {
			panic("stop panic")
		}
		return nil
	}
	enableAndStartFunc = func(unit string) error {
		restarted = append(restarted, unit)
		return nil
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		if len(restarted) != 1 || restarted[0] != "netsgo-server.service" {
			t.Fatalf("expected only fully stopped service to be restarted, got %v", restarted)
		}
	}()

	_, _ = Upgrade("/tmp/netsgo", "1.0.0", "1.1.0")
}

func TestUpgradeRollsBackWhenStartPanics(t *testing.T) {
	origDisableAndStop := disableAndStopFunc
	origEnableAndStart := enableAndStartFunc
	origDetectInstalledUnits := detectInstalledUnitsFunc
	origBinaryPath := installedBinaryPath
	t.Cleanup(func() {
		disableAndStopFunc = origDisableAndStop
		enableAndStartFunc = origEnableAndStart
		detectInstalledUnitsFunc = origDetectInstalledUnits
		installedBinaryPath = origBinaryPath
	})

	tmpDir := t.TempDir()
	installedPath := filepath.Join(tmpDir, "installed-netsgo")
	newPath := filepath.Join(tmpDir, "new-netsgo")
	if err := os.WriteFile(installedPath, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	installedBinaryPath = installedPath

	var stopCalls []string
	var startCalls []string
	var startCallCount int
	detectInstalledUnitsFunc = func() []string { return []string{"netsgo-server.service", "netsgo-client.service"} }
	disableAndStopFunc = func(unit string) error {
		stopCalls = append(stopCalls, unit)
		return nil
	}
	enableAndStartFunc = func(unit string) error {
		startCallCount++
		startCalls = append(startCalls, unit)
		if startCallCount == 2 {
			panic("start panic")
		}
		return nil
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		if len(stopCalls) != 3 || stopCalls[2] != "netsgo-server.service" {
			t.Fatalf("expected rollback to stop already-started service, got %v", stopCalls)
		}
		if len(startCalls) != 4 || startCalls[0] != "netsgo-server.service" || startCalls[1] != "netsgo-client.service" || startCalls[2] != "netsgo-server.service" || startCalls[3] != "netsgo-client.service" {
			t.Fatalf("expected restart sequence after panic rollback, got %v", startCalls)
		}
		data, err := os.ReadFile(installedPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "old binary" {
			t.Fatalf("expected old binary restored after panic, got %q", string(data))
		}
	}()

	_, _ = Upgrade(newPath, "1.0.0", "1.1.0")
}

func TestUpgradeRestartsStoppedServicesWhenPanicOccursInProtectionGap(t *testing.T) {
	origDisableAndStop := disableAndStopFunc
	origEnableAndStart := enableAndStartFunc
	origDetectInstalledUnits := detectInstalledUnitsFunc
	origMkdirTemp := osMkdirTempFunc
	t.Cleanup(func() {
		disableAndStopFunc = origDisableAndStop
		enableAndStartFunc = origEnableAndStart
		detectInstalledUnitsFunc = origDetectInstalledUnits
		osMkdirTempFunc = origMkdirTemp
	})

	var restarted []string
	detectInstalledUnitsFunc = func() []string { return []string{"netsgo-server.service", "netsgo-client.service"} }
	disableAndStopFunc = func(unit string) error { return nil }
	enableAndStartFunc = func(unit string) error {
		restarted = append(restarted, unit)
		return nil
	}
	osMkdirTempFunc = func(dir, pattern string) (string, error) {
		panic("gap panic")
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		if len(restarted) != 2 || restarted[0] != "netsgo-server.service" || restarted[1] != "netsgo-client.service" {
			t.Fatalf("expected stopped services to be restarted from protection gap, got %v", restarted)
		}
	}()

	_, _ = Upgrade("/tmp/netsgo", "1.0.0", "1.1.0")
}

func TestUpgradeRejectsConcurrentRunBeforeStoppingServices(t *testing.T) {
	origLockPathFor := upgradeLockPathFor
	origDetectInstalledUnits := detectInstalledUnitsFunc
	origDisableAndStop := disableAndStopFunc
	t.Cleanup(func() {
		upgradeLockPathFor = origLockPathFor
		detectInstalledUnitsFunc = origDetectInstalledUnits
		disableAndStopFunc = origDisableAndStop
	})
	lockPath := filepath.Join(t.TempDir(), "upgrade.lock")
	upgradeLockPathFor = func() string { return lockPath }
	unlock, err := flock.TryLock(lockPath)
	if err != nil {
		t.Fatalf("hold upgrade lock: %v", err)
	}
	defer unlock()

	stopCalls := 0
	detectInstalledUnitsFunc = func() []string { return []string{"netsgo-server.service"} }
	disableAndStopFunc = func(unit string) error {
		stopCalls++
		return nil
	}

	_, err = Upgrade("/tmp/netsgo", "v1.0.0", "v1.1.0")
	if err == nil || !strings.Contains(err.Error(), "another upgrade is already running") {
		t.Fatalf("expected concurrent upgrade error, got %v", err)
	}
	if stopCalls != 0 {
		t.Fatalf("concurrent upgrade must not stop services, got %d stop calls", stopCalls)
	}
}
