package cmd

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestFilesShareCreateAndList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	var postBody string
	mux.HandleFunc("/api/v1/files/rel-shares", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			buf, _ := io.ReadAll(r.Body)
			postBody = string(buf)
			_, _ = w.Write([]byte(`{"share":{"id":4,"token":"tok123","kind":"file","file_id":5,"allow_download":true,"version":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"shares":[{"id":4,"token":"tok123","kind":"file","file_id":5,"name":"a.txt","allow_download":true,"needs_password":true,"version":1}]}`))
	})
	loggedInRoot(t, mux)

	out := run(t, "files", "share", "create", "5", "--password", "hunter2", "--no-download")
	if !strings.Contains(out, "Created share 4 for file 5 (token tok123)") {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(postBody, `"file_id":5`) || !strings.Contains(postBody, `"kind":"file"`) {
		t.Fatalf("POST body = %q", postBody)
	}
	if !strings.Contains(postBody, `"allow_download":false`) || !strings.Contains(postBody, `"password":"hunter2"`) {
		t.Fatalf("POST body = %q", postBody)
	}

	out = run(t, "files", "share", "ls")
	if !strings.Contains(out, "token=tok123") || !strings.Contains(out, "password") {
		t.Fatalf("list output = %q", out)
	}
}

func TestFilesShareCreateRequiresExactlyOneTarget(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	loggedInRoot(t, mux)

	// Neither target, and both targets, must both fail before any request.
	if out, err := runErr(t, "files", "share", "create"); err == nil {
		t.Fatalf("no target succeeded: %q", out)
	}
	if out, err := runErr(t, "files", "share", "create", "5", "--folder", "3"); err == nil {
		t.Fatalf("two targets succeeded: %q", out)
	}
}

func TestFilesSharePasswordFromStdin(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	var postBody string
	mux.HandleFunc("/api/v1/files/rel-shares", func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		postBody = string(buf)
		_, _ = w.Write([]byte(`{"share":{"id":4,"token":"tok","kind":"folder","file_folder_id":3,"version":1}}`))
	})
	loggedInRoot(t, mux)

	out := runStdin(t, "s3cret-from-pipe\n", "files", "share", "create", "--folder", "3", "--password-stdin")
	if !strings.Contains(out, "Created share 4") {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(postBody, `"password":"s3cret-from-pipe"`) {
		t.Fatalf("POST body = %q", postBody)
	}
}

func TestFilesShareUpdateSendsCurrentVersion(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	mux.HandleFunc("/api/v1/files/rel-shares", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"shares":[{"id":4,"token":"t","kind":"file","file_id":5,"name":"a.txt","version":7}]}`))
	})
	var putBody string
	mux.HandleFunc("/api/v1/files/rel-shares/4", func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		putBody = string(buf)
		_, _ = w.Write([]byte(`{"share":{"id":4,"token":"t","kind":"file","file_id":5,"version":8}}`))
	})
	loggedInRoot(t, mux)

	out := run(t, "files", "share", "update", "4", "--clear-expires", "--no-download")
	if !strings.Contains(out, "Updated share 4 (v8)") {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(putBody, `"version":7`) {
		t.Fatalf("PUT body = %q (want the listed version)", putBody)
	}
	if !strings.Contains(putBody, `"expires_at":null`) || !strings.Contains(putBody, `"allow_download":false`) {
		t.Fatalf("PUT body = %q", putBody)
	}
}

func TestFilesShareFolderAddValidatesRole(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	mux.HandleFunc("/api/v1/files/folder-shares", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"share":{"id":9,"kind":"folder","file_folder_id":3,"folder_name":"docs","members":[{"id":1,"user_id":42,"role":"editor","email":"bob@example.com"}]}}`))
	})
	loggedInRoot(t, mux)

	if out, err := runErr(t, "files", "share", "folder", "add", "bob@example.com", "--folder", "3", "--role", "owner"); err == nil {
		t.Fatalf("bogus role accepted: %q", out)
	}
	out := run(t, "files", "share", "folder", "add", "bob@example.com", "--folder", "3", "--role", "editor")
	if !strings.Contains(out, "Shared folder as editor (share 9, 1 member(s))") {
		t.Fatalf("output = %q", out)
	}
}

func TestFilesUploadLinkCreateRequiresFolderAndExpiry(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	var postBody string
	mux.HandleFunc("/api/v1/files/upload-links", func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		postBody = string(buf)
		_, _ = w.Write([]byte(`{"link":{"id":3,"token":"ul-tok","file_folder_id":3,"folder_name":"inbox"}}`))
	})
	loggedInRoot(t, mux)

	if out, err := runErr(t, "files", "upload-link", "create", "--expires", "2026-12-31T23:59:59Z"); err == nil {
		t.Fatalf("missing --folder accepted: %q", out)
	}
	if out, err := runErr(t, "files", "upload-link", "create", "--folder", "3"); err == nil {
		t.Fatalf("missing --expires accepted: %q", out)
	}
	out := run(t, "files", "upload-link", "create", "--folder", "3", "--expires", "2026-12-31T23:59:59Z", "--label", "Inbox")
	if !strings.Contains(out, "Created upload link 3 (token ul-tok)") {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(postBody, `"label":"Inbox"`) {
		t.Fatalf("POST body = %q", postBody)
	}
}

func TestFilesSharedLsAndBrowse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	mux.HandleFunc("/api/v1/shared-with-me", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"shares":[{"id":2,"kind":"folder","folder_name":"team","role":"editor","owner":{"id":1,"name":"Ada","email":"ada@example.com"}}]}`))
	})
	mux.HandleFunc("/api/v1/shared-with-me/2", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"share_id":2,"role":"editor","kind":"folder","root_id":5,"folders":[{"id":6,"name":"sub"}],"files":[{"id":9,"name":"plan.md","size":120}]}`))
	})
	loggedInRoot(t, mux)

	out := run(t, "files", "shared", "ls")
	if !strings.Contains(out, "team") || !strings.Contains(out, "ada@example.com") {
		t.Fatalf("ls output = %q", out)
	}
	out = run(t, "files", "shared", "browse", "2")
	if !strings.Contains(out, "<dir>  sub") || !strings.Contains(out, "plan.md") {
		t.Fatalf("browse output = %q", out)
	}
}

func TestFilesArchiveCreateRejectsPasswordForTar(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	var postBody string
	mux.HandleFunc("/api/v1/files/archive", func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		postBody = string(buf)
		_, _ = w.Write([]byte(`{"file":{"id":30,"name":"bundle.zip","size":2048,"version":1}}`))
	})
	loggedInRoot(t, mux)

	if out, err := runErr(t, "files", "archive", "create", "5", "--format", "tar.gz", "--password", "x"); err == nil {
		t.Fatalf("password on tar accepted: %q", out)
	}
	if out, err := runErr(t, "files", "archive", "create", "5", "--format", "rar"); err == nil {
		t.Fatalf("unknown format accepted: %q", out)
	}
	out := run(t, "files", "archive", "create", "5", "--format", "zip", "--level", "9")
	if !strings.Contains(out, "Created bundle.zip (id 30") {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(postBody, `"level":9`) || !strings.Contains(postBody, `"ids":[5]`) {
		t.Fatalf("POST body = %q", postBody)
	}
}

func TestFilesArchiveExtractHereFlag(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	var postBody string
	mux.HandleFunc("/api/v1/files/entries/8/extract", func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		postBody = string(buf)
		_, _ = w.Write([]byte(`{"folder":null}`))
	})
	loggedInRoot(t, mux)

	out := run(t, "files", "archive", "extract", "8", "--here")
	if !strings.Contains(out, "Extracting into the target folder") {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(postBody, `"into_new_folder":false`) {
		t.Fatalf("POST body = %q", postBody)
	}
}

func TestFilesZipWritesLocalFile(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	mux.HandleFunc("/api/v1/files/zip", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("PK\x03\x04zipbytes"))
	})
	loggedInRoot(t, mux)

	dir := t.TempDir()
	dest := dir + "/out.zip"
	out := run(t, "files", "zip", "5", "6", "--out", dest)
	if !strings.Contains(out, "Wrote "+dest) {
		t.Fatalf("output = %q", out)
	}
	body, err := readFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body, "PK") {
		t.Fatalf("zip body = %q", body)
	}
}

func TestFilesKeysListsOwnAndRecipients(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	mux.HandleFunc("/api/v1/crypto/keyring", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[{"id":1,"type":"pgp","label":"me","fingerprint":"AAAA","has_private":true,"is_own":true}],"recipients":[{"id":2,"type":"pgp","label":"bob","fingerprint":"BBBB"}]}`))
	})
	loggedInRoot(t, mux)

	out := run(t, "files", "keys")
	if !strings.Contains(out, "Own keys:") || !strings.Contains(out, "AAAA") {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(out, "Recipients:") || !strings.Contains(out, "bob") {
		t.Fatalf("output = %q", out)
	}
}

func TestFilesEncryptRequiresKey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	var postBody string
	mux.HandleFunc("/api/v1/files/entries/5/encrypt", func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		postBody = string(buf)
		_, _ = w.Write([]byte(`{"file":{"id":31,"name":"a.txt.gpg","size":10,"version":1}}`))
	})
	loggedInRoot(t, mux)

	if out, err := runErr(t, "files", "encrypt", "5"); err == nil {
		t.Fatalf("missing --key accepted: %q", out)
	}
	out := run(t, "files", "encrypt", "5", "--key", "1", "--recipient", "2", "--recipient", "3")
	if !strings.Contains(out, "Encrypted to a.txt.gpg (id 31)") {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(postBody, `"key_id":1`) || !strings.Contains(postBody, `"recipient_ids":[2,3]`) {
		t.Fatalf("POST body = %q", postBody)
	}
}

func TestFilesDecryptReadsPassphraseFromStdinOnly(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	var postBody string
	mux.HandleFunc("/api/v1/files/entries/9/decrypt", func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		postBody = string(buf)
		_, _ = w.Write([]byte(`{"file":{"id":32,"name":"a.txt","size":10,"version":1}}`))
	})
	loggedInRoot(t, mux)

	// There is deliberately no --passphrase flag: a private-key passphrase must
	// never reach argv.
	if out, err := runErr(t, "files", "decrypt", "9", "--key", "1", "--passphrase", "x"); err == nil {
		t.Fatalf("--passphrase accepted: %q", out)
	}
	out := runStdin(t, "unlock-me\n", "files", "decrypt", "9", "--key", "1", "--passphrase-stdin")
	if !strings.Contains(out, "Decrypted to a.txt (id 32)") {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(postBody, `"passphrase":"unlock-me"`) {
		t.Fatalf("POST body = %q", postBody)
	}
}

func TestFilesWebdavRefusesRemoteBindWithoutOptIn(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/me", meHandler)
	loggedInRoot(t, mux)

	out, err := runErr(t, "files", "webdav", "--addr", "0.0.0.0:9800")
	if err == nil {
		t.Fatalf("non-loopback bind accepted: %q", out)
	}
	if !strings.Contains(err.Error(), "allow-remote") {
		t.Fatalf("error = %v", err)
	}
	if out, err := runErr(t, "files", "webdav", "--addr", "192.0.2.10:9800", "--allow-remote", "--no-auth"); err == nil {
		t.Fatalf("remote bind without auth accepted: %q", out)
	}
}
