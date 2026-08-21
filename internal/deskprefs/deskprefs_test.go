package deskprefs

import (
	"os"
	"path/filepath"
	"testing"
)

// withConfigDir points config.Dir at a temporary directory for the test.
func withConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LEDGERLINE_CLI_CONFIG_DIR", dir)
	return dir
}

func TestLoadOnAFreshInstallReturnsWorkingDefaults(t *testing.T) {
	withConfigDir(t)

	p, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !p.LaunchAtLogin || !p.ExplorerMenu {
		t.Fatalf("a fresh install should start with the client integrated: %+v", p)
	}
	if p.Notify != NotifyProblems {
		t.Fatalf("notify = %q, want problems only", p.Notify)
	}
	if len(p.Exclude) == 0 {
		t.Fatal("the exclusion list is empty, so scratch files would sync")
	}
}

// TestLoadFillsFieldsAnOlderFileDoesNotHave is the upgrade path: a file written
// before a preference existed must not read as that preference being off.
func TestLoadFillsFieldsAnOlderFileDoesNotHave(t *testing.T) {
	dir := withConfigDir(t)
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(`{"language":"de"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Language != "de" {
		t.Fatalf("language = %q", p.Language)
	}
	if !p.ExplorerMenu {
		t.Fatal("a field absent from the file was read as off instead of its default")
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	withConfigDir(t)

	want := Defaults()
	want.LaunchAtLogin = false
	want.Language = "ru"
	want.Theme = ThemeDark
	want.UploadKBps = 512
	want.CameraFolder = `C:\Users\x\Pictures`

	if err := Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LaunchAtLogin != false || got.Language != "ru" || got.Theme != ThemeDark ||
		got.UploadKBps != 512 || got.CameraFolder != want.CameraFolder {
		t.Fatalf("round trip lost a value: %+v", got)
	}
}

func TestNormaliseRepairsAHandEditedFile(t *testing.T) {
	dir := withConfigDir(t)
	body := `{"theme":"neon","notify":"always","language":"fr","upload_kbps":-5,
	          "exclude":["*.tmp","  ","*.TMP","*.part"]}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Theme != ThemeSystem {
		t.Fatalf("theme = %q, want the system default for an unknown value", p.Theme)
	}
	if p.Notify != NotifyProblems {
		t.Fatalf("notify = %q", p.Notify)
	}
	if p.Language != "" {
		t.Fatalf("language = %q, want empty for an unsupported one", p.Language)
	}
	if p.UploadKBps != 0 {
		t.Fatalf("upload = %d, want unlimited rather than negative", p.UploadKBps)
	}
	// "*.tmp" and "*.TMP" are one pattern, and the blank is none.
	if len(p.Exclude) != 2 {
		t.Fatalf("exclude = %v", p.Exclude)
	}
}

func TestExcludedMatchesTheBaseNameAnywhere(t *testing.T) {
	p := Defaults()
	cases := map[string]bool{
		`~$report.docx`:               true,
		`C:\deep\path\Thumbs.db`:      true,
		`holiday.jpg.crdownload`:      true,
		`notes.txt`:                   false,
		`C:\work\thumbs.db`:           true, // patterns are case-insensitive
		`my.tmp.folder\document.docx`: false,
	}
	for name, want := range cases {
		if got := p.Excluded(name); got != want {
			t.Fatalf("Excluded(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestUpdateAppliesOneChangeAndKeepsTheRest(t *testing.T) {
	withConfigDir(t)

	if _, err := Update(func(p *Prefs) { p.Paused = true }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.Paused {
		t.Fatal("the change was not written")
	}
	if !got.ExplorerMenu || len(got.Exclude) == 0 {
		t.Fatalf("Update dropped unrelated settings: %+v", got)
	}
}

// TestExcludedUnderstandsBothSeparators is the portability bug this shape
// exists for: filepath.Base leaves a Windows path whole on Linux, because a
// backslash is a legal character in a POSIX file name. The client reads
// configuration written on Windows and syncs from Linux, so both conventions
// have to match the same way on every platform.
func TestExcludedUnderstandsBothSeparators(t *testing.T) {
	p := Defaults()
	for _, path := range []string{
		`C:\deep\path\Thumbs.db`,
		"/home/user/deep/Thumbs.db",
		`mixed/path\Thumbs.db`,
	} {
		if !p.Excluded(path) {
			t.Fatalf("Excluded(%q) = false", path)
		}
	}
	if p.Excluded(`C:\work\notes.txt`) {
		t.Fatal("a path with no matching base name was excluded")
	}
}
