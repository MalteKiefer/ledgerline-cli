package cmd

import "testing"

func TestAcquireLockIsExclusive(t *testing.T) {
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", t.TempDir())
	rel, err := acquireLock("service")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := acquireLock("service"); err == nil {
		t.Fatal("second acquire should fail while held")
	}
	rel()
	rel2, err := acquireLock("service")
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	rel2()
}
