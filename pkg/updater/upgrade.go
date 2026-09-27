package updater

import (
	"errors"
	"fmt"
	"netsgo/pkg/flock"
	"os"
	"path/filepath"
)

var osMkdirTempFunc = os.MkdirTemp
var upgradeLockPath = "/run/netsgo-upgrade.lock"
var upgradeLockPathFor = func() string { return upgradeLockPath }

func Upgrade(srcPath, oldVersion, newVersion string) (*Result, error) {
	result := &Result{OldVersion: oldVersion, NewVersion: newVersion}
	unlock, err := flock.TryLock(upgradeLockPathFor())
	if err != nil {
		return result, fmt.Errorf("another upgrade is already running: %w", err)
	}
	defer unlock()

	units := detectInstalledUnitsFunc()
	if len(units) == 0 {
		return result, fmt.Errorf("no installed services")
	}

	orch := &Orchestrator{
		DisableAndStop: disableAndStopFunc,
		EnableAndStart: enableAndStartFunc,
	}

	stopped := make([]string, 0, len(units))
	stopPhaseArmed := true
	defer recoverStoppedServicesOnPanic(orch, &stopped, &stopPhaseArmed)
	err = orch.StopServices(units, &stopped)
	if err != nil {
		if rollbackErr := orch.RestartStoppedServices(stopped); rollbackErr != nil {
			return result, fmt.Errorf("%w; %v", err, rollbackErr)
		}
		return result, err
	}
	result.Stopped = stopped

	tmpDir, err := osMkdirTempFunc(filepath.Dir(installedBinaryPath), ".netsgo-upgrade-*")
	if err != nil {
		if rollbackErr := orch.RestartStoppedServices(stopped); rollbackErr != nil {
			return result, errors.Join(fmt.Errorf("temp dir: %w", err), rollbackErr)
		}
		return result, fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	started := make([]string, 0, len(units))
	originalBinary := filepath.Join(tmpDir, filepath.Base(installedBinaryPath)+".backup")
	backupAvailable := false
	var envSnapshots []serviceEnvSnapshot
	var databaseSnapshot *serverDatabaseSnapshot
	defer recoverUpdateOrUpgradeOnPanic(orch, &started, &stopped, &originalBinary, &backupAvailable, &envSnapshots, &databaseSnapshot)
	stopPhaseArmed = false
	databaseSnapshot, err = snapshotServerDatabase(units, tmpDir)
	if err != nil {
		if rollbackErr := orch.RestartStoppedServices(stopped); rollbackErr != nil {
			return result, errors.Join(err, rollbackErr)
		}
		return result, err
	}

	if err := replaceBinaryFunc(installedBinaryPath, originalBinary); err != nil {
		if rollbackErr := orch.RestartStoppedServices(stopped); rollbackErr != nil {
			return result, errors.Join(fmt.Errorf("backup: %w", err), rollbackErr)
		}
		return result, fmt.Errorf("backup: %w", err)
	}
	backupAvailable = true
	result.BackupPath, err = persistUpgradeBackup(originalBinary, oldVersion)
	if err != nil {
		rollbackErr := rollbackUpdateOrUpgrade(orch, nil, stopped, originalBinary, true, nil, databaseSnapshot)
		if rollbackErr != nil {
			return result, errors.Join(fmt.Errorf("retain backup: %w", err), rollbackErr)
		}
		return result, fmt.Errorf("retain backup: %w", err)
	}

	envSnapshots, err = snapshotServiceEnvFiles(units)
	if err != nil {
		rollbackErr := rollbackUpdateOrUpgrade(orch, nil, stopped, originalBinary, true, nil, databaseSnapshot)
		if rollbackErr != nil {
			return result, errors.Join(err, rollbackErr)
		}
		return result, err
	}

	if err := replaceBinaryFunc(srcPath, installedBinaryPath); err != nil {
		rollbackErr := rollbackUpdateOrUpgrade(orch, nil, stopped, originalBinary, true, envSnapshots, databaseSnapshot)
		if rollbackErr != nil {
			return result, errors.Join(fmt.Errorf("replace: %w", err), rollbackErr)
		}
		return result, fmt.Errorf("replace: %w", err)
	}

	if err := repairServiceEnvFilesFunc(units); err != nil {
		rollbackErr := rollbackUpdateOrUpgrade(orch, nil, stopped, originalBinary, true, envSnapshots, databaseSnapshot)
		if rollbackErr != nil {
			return result, errors.Join(err, rollbackErr)
		}
		return result, err
	}

	err = orch.StartServices(units, &started)
	if err != nil {
		rollbackErr := rollbackUpdateOrUpgrade(orch, started, stopped, originalBinary, true, envSnapshots, databaseSnapshot)
		if rollbackErr != nil {
			return result, errors.Join(err, rollbackErr)
		}
		return result, err
	}
	if err := verifyStartedServicesFunc(units); err != nil {
		rollbackErr := rollbackUpdateOrUpgrade(orch, started, stopped, originalBinary, true, envSnapshots, databaseSnapshot)
		return result, startupHealthFailure(units, result.BackupPath, databaseSnapshot != nil, err, rollbackErr)
	}
	result.Started = started
	return result, nil
}
