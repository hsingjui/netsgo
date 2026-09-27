package updater

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"netsgo/internal/svcmgr"
)

var serverDatabaseSuffixes = [...]string{"", "-wal", "-shm", "-journal"}

type databaseFileSnapshot struct {
	targetPath string
	backupPath string
	perm       os.FileMode
	uid        int
	gid        int
	hasOwner   bool
}

type serverDatabaseSnapshot struct {
	runtimeDir string
	files      []databaseFileSnapshot
}

func snapshotServerDatabase(units []string, tmpDir string) (*serverDatabaseSnapshot, error) {
	serverUnit := svcmgr.UnitName(svcmgr.RoleServer)
	hasServer := false
	for _, unit := range units {
		if unit == serverUnit {
			hasServer = true
			break
		}
	}
	if !hasServer {
		return nil, nil
	}

	layout := newServiceLayoutFunc(svcmgr.RoleServer)
	databasePath := filepath.Join(layout.RuntimeDir, "netsgo.db")
	snapshot := &serverDatabaseSnapshot{
		runtimeDir: layout.RuntimeDir,
		files:      make([]databaseFileSnapshot, 0, len(serverDatabaseSuffixes)),
	}
	mainInfo, err := os.Lstat(databasePath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect server database: %w", err)
	}
	if err == nil {
		if err := validateDatabaseFile(databasePath, mainInfo); err != nil {
			return nil, err
		}
	}

	backupDir := filepath.Join(tmpDir, "server-database")
	if err := os.Mkdir(backupDir, 0o700); err != nil {
		return nil, fmt.Errorf("create server database snapshot directory: %w", err)
	}
	for _, suffix := range serverDatabaseSuffixes {
		targetPath := databasePath + suffix
		entry := databaseFileSnapshot{targetPath: targetPath}
		info, err := os.Lstat(targetPath)
		if err != nil {
			if os.IsNotExist(err) {
				snapshot.files = append(snapshot.files, entry)
				continue
			}
			return nil, fmt.Errorf("inspect server database file %s: %w", targetPath, err)
		}
		if err := validateDatabaseFile(targetPath, info); err != nil {
			return nil, err
		}
		entry.backupPath = filepath.Join(backupDir, filepath.Base(targetPath))
		entry.perm = info.Mode().Perm()
		entry.uid, entry.gid, entry.hasOwner = fileOwner(info)
		if err := copyDatabaseFile(targetPath, entry.backupPath, entry.perm); err != nil {
			return nil, fmt.Errorf("snapshot server database file %s: %w", targetPath, err)
		}
		snapshot.files = append(snapshot.files, entry)
	}
	if err := syncDirectory(backupDir); err != nil {
		return nil, fmt.Errorf("sync server database snapshot: %w", err)
	}
	return snapshot, nil
}

func (snapshot *serverDatabaseSnapshot) restore() error {
	if snapshot == nil {
		return nil
	}
	for _, entry := range snapshot.files {
		info, err := os.Lstat(entry.targetPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("inspect current server database file %s: %w", entry.targetPath, err)
		}
		if err := validateDatabaseFile(entry.targetPath, info); err != nil {
			return err
		}
	}
	for _, entry := range snapshot.files {
		if err := os.Remove(entry.targetPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove upgraded server database file %s: %w", entry.targetPath, err)
		}
	}
	for _, entry := range snapshot.files {
		if entry.backupPath == "" {
			continue
		}
		if err := restoreDatabaseFile(entry); err != nil {
			return fmt.Errorf("restore server database file %s: %w", entry.targetPath, err)
		}
	}
	if err := syncDirectory(snapshot.runtimeDir); err != nil {
		return fmt.Errorf("sync restored server database directory: %w", err)
	}
	return nil
}

func validateDatabaseFile(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("server database file must not be a symlink: %s", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("server database file is not regular: %s", path)
	}
	return nil
}

func copyDatabaseFile(srcPath, dstPath string, perm os.FileMode) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()

	dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		_ = dst.Close()
		if cleanup {
			_ = os.Remove(dstPath)
		}
	}()
	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	if err := dst.Sync(); err != nil {
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func restoreDatabaseFile(entry databaseFileSnapshot) error {
	src, err := os.Open(entry.backupPath)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(entry.targetPath), ".netsgo.db.rollback-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(entry.perm); err != nil {
		return err
	}
	if entry.hasOwner {
		if err := tmp.Chown(entry.uid, entry.gid); err != nil {
			return err
		}
	}
	if _, err := io.Copy(tmp, src); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, entry.targetPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}
