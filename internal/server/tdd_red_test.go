package server

import (
	"os"
	"testing"
)

func requireTDDRed(t *testing.T) {
	t.Helper()
	if os.Getenv("NETSGO_TDD_RED") != "1" {
		t.Skip("expected-red TDD guard; set NETSGO_TDD_RED=1 to enforce it")
	}
}
