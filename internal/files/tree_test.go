package files

import (
	"strings"
	"testing"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
)

func i64(v int64) *int64 { return &v }

func TestBuildTreeNestsChildrenAndFiles(t *testing.T) {
	folders := []api.FileFolder{
		{ID: 1, Name: "docs"},
		{ID: 2, Name: "sub", ParentID: i64(1)},
	}
	files := []api.FileEntry{
		{ID: 10, Name: "root.txt", Size: 3},
		{ID: 11, Name: "deep.txt", Size: 7, FileFolderID: i64(2)},
	}
	root := BuildTree(folders, files)

	var b strings.Builder
	root.Print(&b)
	out := b.String()

	// docs/ contains sub/, sub/ contains deep.txt; root.txt is top-level.
	if !strings.Contains(out, "docs/  [folder 1]") {
		t.Fatalf("missing docs folder:\n%s", out)
	}
	if !strings.Contains(out, "  sub/  [folder 2]") {
		t.Fatalf("sub not nested under docs:\n%s", out)
	}
	if !strings.Contains(out, "    deep.txt  [11, 7 B]") {
		t.Fatalf("deep.txt not nested under sub:\n%s", out)
	}
	if !strings.Contains(out, "root.txt  [10, 3 B]") {
		t.Fatalf("root.txt missing:\n%s", out)
	}
}
