//go:build !linux

package svcmgr

func RepairClientRuntimeOwnership(ServiceLayout) error {
	return nil
}
