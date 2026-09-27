//go:build windows

package updater

import "os"

func fileOwner(_ os.FileInfo) (uid, gid int, ok bool) {
	return 0, 0, false
}
