//go:build linux

package svcmgr

import "os"

func RepairClientRuntimeOwnership(layout ServiceLayout) error {
	return repairClientRuntimeOwnership(layout, runtimeOwnershipOps{
		lookup: lookupSystemUser,
		lstat:  os.Lstat,
		chown:  os.Chown,
		chmod:  os.Chmod,
	})
}
