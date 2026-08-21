//go:build windows

package deskintegrate

import (
	"errors"
	"fmt"
	"syscall"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// TestIsNotFoundReadsTheCodeNotTheMessage is the bug this function exists for.
//
// The first version compared err.Error() against "cannot find". Windows returns
// its errors in the user's display language, so on a German install the very
// first registration aborted: deleting a command store that did not exist yet
// returned "Das System kann die angegebene Datei nicht finden", which did not
// match, so a normal first run looked like a failure and no verbs were written.
//
// Matching on the code makes the test meaningful on an English CI runner too —
// a string comparison would pass there and hide the defect, which is exactly
// what happened.
func TestIsNotFoundReadsTheCodeNotTheMessage(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"file not found", syscall.ERROR_FILE_NOT_FOUND, true},
		{"path not found", syscall.ERROR_PATH_NOT_FOUND, true},
		{"registry sentinel", registry.ErrNotExist, true},
		{"wrapped", fmt.Errorf("delete store: %w", syscall.ERROR_FILE_NOT_FOUND), true},
		{"access denied is a real failure", syscall.ERROR_ACCESS_DENIED, false},
		{"an unrelated error", errors.New("something else"), false},
		{"no error", nil, false},
	}
	for _, c := range cases {
		if got := isNotFound(c.err); got != c.want {
			t.Errorf("%s: isNotFound = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestIsNotFoundOnARealMissingKey exercises the same path through the registry
// API rather than through a hand-made error value, so a change in how
// golang.org/x/sys wraps its failures cannot slip past.
func TestIsNotFoundOnARealMissingKey(t *testing.T) {
	const missing = `Software\Ledgerline\a-key-that-does-not-exist`

	_, err := registry.OpenKey(registry.CURRENT_USER, missing, registry.QUERY_VALUE)
	if err == nil {
		t.Skip("the key unexpectedly exists")
	}
	if !isNotFound(err) {
		t.Fatalf("opening a missing key produced %#v, which was not recognised", err)
	}

	if err := registry.DeleteKey(registry.CURRENT_USER, missing); err == nil {
		t.Fatal("deleting a missing key succeeded")
	} else if !isNotFound(err) {
		t.Fatalf("deleting a missing key produced %#v, which was not recognised", err)
	}
}
