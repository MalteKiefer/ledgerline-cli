# ledgerline-cli

[![CI](https://github.com/MalteKiefer/ledgerline-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/MalteKiefer/ledgerline-cli/actions/workflows/ci.yml)
[![Release](https://github.com/MalteKiefer/ledgerline-cli/actions/workflows/release.yml/badge.svg)](https://github.com/MalteKiefer/ledgerline-cli/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A desktop client for a self-hosted [Ledgerline](https://github.com/MalteKiefer/Ledgerline)
server, written in Go: a command-line tool for **Linux**, **macOS** and
**Windows**, plus a **Windows tray application** that shows your account at a
glance. Both share one credential and one set of certificate pins.

The client covers two modules — **files** and **gallery** — plus the
authentication, device and audit plumbing around them. Files is covered in full
(browse, sync, versions, trash, labels, search, sharing, archives, encryption and
a mountable WebDAV endpoint); gallery is a deliberate minimum (upload, list,
download, trash).

Content is stored **plaintext on the server**, which computes checksums,
thumbnails, EXIF and video renditions itself. This client does no content
encryption; what it protects is the transport (TLS 1.3 + certificate pinning) and
your bearer token. The server can, on request, encrypt a stored file with a
public key — see [`files encrypt`](#encryption).

## Contents

- [Install](#install)
  - [Windows installer](#windows-installer)
  - [Linux packages (.deb / .rpm)](#linux-packages-deb--rpm)
  - [Binaries (Linux, macOS, Windows)](#binaries-linux-macos-windows)
  - [Verifying a release](#verifying-a-release)
- [Build from source](#build-from-source)
- [Configuration](#configuration)
- [Usage](#usage)
  - [`status`](#status)
  - [`auth`](#auth)
  - [`gallery`](#gallery)
  - [`files` — browsing and transfer](#files--browsing-and-transfer)
  - [`files sync`](#files-sync)
  - [`files webdav` — mount as a network drive](#files-webdav--mount-as-a-network-drive)
  - [Tray application (Windows)](#tray-application-windows)
  - [Organising: rename, move, trash, versions, labels](#organising-rename-move-trash-versions-labels)
  - [Sharing](#sharing)
  - [Archives](#archives)
  - [Encryption](#encryption)
  - [`audit`](#audit)
- [How authentication works](#how-authentication-works)
- [Security notes](#security-notes)
- [Development](#development)
- [Versioning](#versioning)
- [License](#license)

## Install

Every release ships a plain binary per target, Debian and RPM packages for
Linux, and a Windows setup .exe that installs the CLI together with the tray
application. `checksums.txt` carries the SHA-256 of every artefact.

Supported targets: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`,
`windows/amd64`, `windows/arm64`.

### Windows installer

Download `ledgerline-cli-setup-<version>-amd64.exe` (or `-arm64`) from the
releases page and run it. It installs both programs into
`C:\Program Files\Ledgerline`:

- **ledgerline-cli.exe** — the command-line client
- **ledgerline-gui.exe** — the [tray application](#tray-application-windows)

and creates Start-menu shortcuts. Two options are offered during setup: adding
the install directory to `PATH` (on by default, so `ledgerline-cli` works in any
terminal) and starting the tray icon when you sign in (off by default).

It also creates `C:\Program Files\Ledgerline\logs`, where the tray writes what
it has been doing — refreshes, sign-outs, every sync and every failure — because
a program with no window has nowhere else to say it. The directory is made
writable for the machine's users so a normal account can actually write there;
on a shared machine that means another user can read those lines, which is why
the log records events and never a token, a password or a code. **Settings →
About → Open log folder** takes you there.

Uninstalling removes the programs and their logs, and leaves your credential and
configuration untouched.

The installer is not Authenticode-signed, so SmartScreen warns on first run —
verify the download against `checksums.txt` as shown below.

### Linux packages (.deb / .rpm)

```sh
VERSION=0.7.5
base=https://github.com/MalteKiefer/ledgerline-cli/releases/download/v$VERSION

# Debian / Ubuntu
curl -LO "$base/ledgerline-cli_${VERSION}_amd64.deb"
sudo apt install "./ledgerline-cli_${VERSION}_amd64.deb"

# Fedora / RHEL / openSUSE
curl -LO "$base/ledgerline-cli-${VERSION}.x86_64.rpm"
sudo dnf install "./ledgerline-cli-${VERSION}.x86_64.rpm"
```

The packages install the binary to `/usr/bin/ledgerline-cli`, shell completions
for bash/zsh/fish, and the licence and changelog under
`/usr/share/doc/ledgerline-cli/`. A keyring (`libsecret`/gnome-keyring, kwallet)
is *recommended*, not required: without one the client falls back to a `0600`
credential file.

### Binaries (Linux, macOS, Windows)

```sh
# Example for macOS on Apple silicon; pick the version and asset for your system.
VERSION=0.7.5
ARCH=darwin-arm64
base=https://github.com/MalteKiefer/ledgerline-cli/releases/download/v$VERSION
curl -LO "$base/ledgerline-cli-$VERSION-$ARCH"
curl -LO "$base/checksums.txt"

# Verify the download before installing.
grep " ledgerline-cli-$VERSION-$ARCH\$" checksums.txt | shasum -a 256 -c -

chmod +x "ledgerline-cli-$VERSION-$ARCH"
sudo mv "ledgerline-cli-$VERSION-$ARCH" /usr/local/bin/ledgerline-cli
```

On Windows, download `ledgerline-cli-<version>-windows-amd64.exe` (or
`-arm64.exe`), verify it and put it somewhere on `%PATH%`:

```powershell
$Version = "0.7.5"
$base = "https://github.com/MalteKiefer/ledgerline-cli/releases/download/v$Version"
Invoke-WebRequest "$base/ledgerline-cli-$Version-windows-amd64.exe" -OutFile ledgerline-cli.exe
Invoke-WebRequest "$base/checksums.txt" -OutFile checksums.txt

# Compare the hash against the checksums file before running it.
(Get-FileHash ledgerline-cli.exe -Algorithm SHA256).Hash.ToLower()
Select-String -Path checksums.txt -Pattern "windows-amd64.exe"
```

The plain `.exe` is the CLI on its own — no CGO, no dependencies — for anyone
who does not want the [installer](#windows-installer). The tray application
ships as `ledgerline-gui-<version>-windows-amd64.exe` next to it. The bearer
token is stored in the Windows Credential Manager.

### Verifying a release

`checksums.txt` is signed keyless with [cosign](https://docs.sigstore.dev/) via
the release workflow's GitHub OIDC identity, and every artefact carries a build
provenance attestation:

```sh
curl -LO "$base/checksums.txt.sig"
curl -LO "$base/checksums.txt.pem"

cosign verify-blob checksums.txt \
  --signature checksums.txt.sig \
  --certificate checksums.txt.pem \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/MalteKiefer/ledgerline-cli/\.github/workflows/release\.yml@'

# Or verify one artefact's provenance with the GitHub CLI:
gh attestation verify "ledgerline-cli-$VERSION-$ARCH" --repo MalteKiefer/ledgerline-cli
```

Verify the install and check for updates:

```sh
ledgerline-cli status
```

## Build from source

Requires Go 1.25 or newer. The module pins the build toolchain (see `go.mod`); an
older `go` command fetches it automatically on first build.

```sh
git clone https://github.com/MalteKiefer/ledgerline-cli.git
cd ledgerline-cli
make build             # ./bin/ledgerline-cli, version-stamped from git
make release           # cross-compile every target (CLI + Windows tray) into ./dist
make package           # .deb + .rpm (amd64, arm64) into ./dist
make installer-windows # Windows setup .exe (needs makensis)
```

The build embeds version, commit hash and build date via `-ldflags`, so the
binary reports its own provenance (`ledgerline-cli status`). `make repro-verify`
proves the build is byte-reproducible from a given commit.

## Configuration

State lives in the per-user config directory:

- Linux: `$XDG_CONFIG_HOME/ledgerline-cli` (default `~/.config/ledgerline-cli`)
- macOS: `~/Library/Application Support/ledgerline-cli`
- Windows: `%AppData%\ledgerline-cli`

`config.json` (`0600`) holds non-secret data: the server URL and your identity.
The bearer token is stored separately in the OS credential store (see
[Security notes](#security-notes)). Override the directory with
`LEDGERLINE_CLI_CONFIG_DIR`.

## Usage

Every command has `--help`:

```sh
ledgerline-cli --help
ledgerline-cli files --help
ledgerline-cli files share --help
```

### `status`

Prints the build metadata of the running binary and checks GitHub for a newer
release.

### `auth`

Two ways in; both end in the same device-scoped token.

```sh
# credentials, with your second factor when the account has one
ledgerline-cli auth login --server https://ledger.example.com --email you@example.com
ledgerline-cli auth login --server … --email … --password-stdin < pw.txt --otp 123456
ledgerline-cli auth login --server … --email … --recovery-code <code>

# or a one-time code from the web profile, approved there
ledgerline-cli auth pair --server https://ledger.example.com --code <one-time-code>

ledgerline-cli auth status     # identity, storage usage, credential backend
ledgerline-cli auth logout     # revoke server-side and clear local state
```

The password is read from the terminal without echoing it, or from stdin with
`--password-stdin`. `--password` exists but puts the secret in argv, where other
users on the machine can read it. If the account has two-factor authentication
and you pass no code, the command asks for one — the server will not issue a
token before it is right.

Device management (the same list the web profile shows):

```sh
ledgerline-cli devices ls
ledgerline-cli devices rm <id>
ledgerline-cli devices wipe <id>
```

### `gallery`

```sh
ledgerline-cli gallery upload <path...> [--jobs 4] [--batch 50] [--force]
ledgerline-cli gallery list
ledgerline-cli gallery download <id...> [--out DIR] [--variant original|edited]
ledgerline-cli gallery rm <id...>          # trash (bulk when more than one)
```

`gallery upload` walks directories, hashes each file (SHA-256) and skips bytes
already sent — via a per-server ledger in the config directory. `--force`
bypasses it, `--batch N` checkpoints the ledger every N uploads. Uploads show a
progress bar on a TTY and plain per-line output when piped.

### `files` — browsing and transfer

```sh
ledgerline-cli files ls                                  # whole tree + usage
ledgerline-cli files upload <file...> [--folder ID] [--jobs 4] [--no-chunked]
ledgerline-cli files download <id...> [--out DIR]
ledgerline-cli files mkdir <name> [--parent ID]
ledgerline-cli files rm <id...>                           # trash
ledgerline-cli files search <query>                       # full-text / OCR
ledgerline-cli files stats                                # usage by type, duplicates
ledgerline-cli files activity [--file ID]
```

`files upload` skips a file whose bytes the server already has (its `sha256`), so
a re-run is cheap and cross-host. Files over 64 MiB automatically go through the
chunked-upload session, where a failed transfer only costs the current part;
`--no-chunked` forces a single multipart body.

### `files sync`

```sh
ledgerline-cli files sync <dir> [--direction both|up|down] [--conflict …]
ledgerline-cli files sync <dir> --interval 5m         # poll
ledgerline-cli files sync <dir> --service             # watch the filesystem
```

The sync compares content by SHA-256 (local, computed on the fly) against the
server's `sha256`. It keeps **no last-seen state**, which means **deletions are
never propagated**: a file missing on one side is treated as missing, never as a
delete. That is a deliberate safety choice.

### `files webdav` — mount as a network drive

Serves the remote Files module over WebDAV on a local address so the operating
system can mount it. Every operation is a REST call against your server; the only
local state is the temp file of a body currently being read or written, removed
when the handle closes.

```sh
ledgerline-cli files webdav                       # 127.0.0.1:9800, generated password
ledgerline-cli files webdav --read-only           # refuse every write
ledgerline-cli files webdav --addr 127.0.0.1:9999
```

The command prints the generated Basic-auth credentials and the mount command for
your platform, then runs until interrupted:

```sh
# Linux
gio mount http://127.0.0.1:9800/
sudo mount -t davfs http://127.0.0.1:9800/ /mnt/ledgerline

# macOS — Finder: Go > Connect to Server, or
mount_webdav -i http://127.0.0.1:9800/ /Volumes/ledgerline
```

```powershell
# Windows
net use Z: http://127.0.0.1:9800 /user:ledgerline
net use Z: /delete
```

| Flag | Description |
| --- | --- |
| `--addr` | Address to serve on (default `127.0.0.1:9800`). |
| `--read-only` | Refuse every write through the mount. |
| `--user` | Basic-auth user name (default `ledgerline`). |
| `--no-auth` | Serve without authentication. Loopback only, and it means every local user can read and write your files. |
| `--allow-remote` | Required to bind a non-loopback address. |

A delete through the mount trashes the file or folder server-side; nothing is
force-deleted, so a stray delete by a file manager stays recoverable in the
trash.

### Tray application (Windows)

`ledgerline-gui.exe` sits in the notification area. Signed out it offers one
thing — **Sign in…** — because a storage figure or a "synced folders" entry with
no session behind it reads as broken rather than as waiting.

Signed in, the menu is two rows and their submenus, so it stays readable at a
glance:

- **the account**, with its profile picture as the row's icon; its submenu has
  your e-mail, the server, and storage (`Files:`, `Gallery:`, `Total:` when the
  server reports the split, one `Storage:` line when it does not)
- **the sync state** — `Sync: up to date (4 min ago)`, `Sync: syncing 2
  folders…`, `Sync: last run failed`; its submenu lists each folder pair and
  what happened to it

plus **Settings…**, **Sign out**, **Open web app**, **Refresh**, **Open log
folder** and **Quit**.

The tray icon has three states, told apart by shape and not only colour: muted
while you are signed out or the server cannot be reached, the plain mark when
everything is current, and the mark with a green dot while a sync is running.

It reads the same credential as the CLI, so signing in through either one signs
in both, and it honours the same remote kill switch: a revoked device clears its
credential on the next refresh.

**Settings…** opens one window with three tabs: **Profile** (who you are, the
server, storage, and sign-out), **Synced folders** (below), and **About** (the
build, and where its settings and logs live).

**Sign in…** opens a proper window — no browser, no console — and lets you pick
how you sign in:

- **e-mail and password**, plus your two-factor code when the account has one.
  The server refuses to issue a token until the code is right, so this route is
  as gated as the web app; the code field can also take a recovery code.
- **a one-time code** generated in your web profile and approved there. Nothing
  but that code leaves the web app, so your password is never typed into this
  program at all.



The state refreshes every five minutes, on demand via **Refresh**, and right
after a sign-in or sign-out.

### Synced folders

One directory, once, is `files sync <dir>`. For a standing arrangement — several
folders, each against its own remote folder, on a schedule — use the `sync`
group, which the tray reads and writes as well:

```sh
ledgerline-cli sync add ~/Documents --remote Documents --interval 15m
ledgerline-cli sync add ~/Pictures  --remote Photos --direction push --no-watch
ledgerline-cli sync ls
ledgerline-cli sync set 2 --disable        # pause it; nothing is deleted
ledgerline-cli sync set 2 --interval 5m --watch
ledgerline-cli sync run 1                  # sync one pair now
ledgerline-cli sync run --all              # every enabled pair
ledgerline-cli sync service                # keep running: schedule + file changes
```

Each pair syncs **when a local file changes** (within a few seconds, after the
writes settle) and **on its interval**, and either can be turned off: `--no-watch`
for interval only, `--interval 0` for change detection only. The tray runs the
same loop while it is open, so a laptop that is simply on stays up to date.

In **Settings → Synced folders** the same list has **Add folder…**, **Edit…**,
**Pause/Resume**, **Sync now**, **Sync all** and **Remove**. Adding or editing
opens a form where **both ends are chosen**: the local folder with the shell
picker, the remote folder from the server's own tree (**Browse…**), plus the
direction, the conflict policy, the interval in minutes and whether to watch for
changes. The remote folder is never guessed from the local folder's name.

Two things worth knowing:

- **Removing a pair deletes nothing.** It stops the arrangement; the files stay
  where they are, locally and on the server.
- **Deletions are never propagated.** A file that is missing on one side is
  treated as absent, not as an instruction to delete it on the other. Each pair
  records what its last run did, including a failure, so a pair that quietly
  stopped working is visible in `sync ls` and in the window.

### Organising: rename, move, trash, versions, labels

```sh
ledgerline-cli files rename <id> <new-name>
ledgerline-cli files mv <id...> --to <folder-id> | --root
ledgerline-cli files copy <id> [--to <folder-id>]

ledgerline-cli files folder rename|mv|rm|restore <id> …
ledgerline-cli files trash ls|restore|rm|empty            # rm/empty are permanent

ledgerline-cli files versions ls <file-id>
ledgerline-cli files versions download <file-id> <version> [--out DIR]
ledgerline-cli files versions restore <file-id> <version>

ledgerline-cli files labels ls|create|rm
ledgerline-cli files labels set <file-id> <label-id...>   # no ids clears the set
```

### Sharing

Public links (a token in the URL, optionally password-gated and expiring):

```sh
ledgerline-cli files share create <file-id> [--password-stdin] [--expires …] [--no-download]
ledgerline-cli files share create --folder <id> [--expires …]
ledgerline-cli files share ls
ledgerline-cli files share update <id> [--remove-password] [--clear-expires] [--no-download]
ledgerline-cli files share rm <id...>
```

Internal shares, granting another Ledgerline account access:

```sh
ledgerline-cli files share folder add <email> --folder <id> --role viewer|editor
ledgerline-cli files share folder ls
ledgerline-cli files share folder role <share-id> <user-id> viewer|editor
ledgerline-cli files share folder remove <share-id> <user-id>
ledgerline-cli files share folder rm <share-id...>
```

Inbound upload links, letting an outsider drop files into one of your folders
(both a destination folder and an expiry are required):

```sh
ledgerline-cli files upload-link create --folder <id> --expires 2026-12-31T23:59:59Z [--label …] [--password-stdin]
ledgerline-cli files upload-link ls
ledgerline-cli files upload-link rm <id...>
```

The receiving side of an internal share:

```sh
ledgerline-cli files shared ls
ledgerline-cli files shared browse <share-id>
ledgerline-cli files shared download <share-id> <file-id...> [--out DIR]
ledgerline-cli files shared upload <share-id> <file...>      # editor role
ledgerline-cli files shared rename <share-id> <file-id> <name>
ledgerline-cli files shared rm <share-id> <file-id...>
```

Every password flag has a `--password-stdin` variant that reads the secret from a
pipe, so it never appears in argv or your shell history.

### Archives

```sh
# Stream a ZIP of a selection and/or a folder subtree to a local file.
ledgerline-cli files zip <file-id...> [--folder <id>] --out bundle.zip

# Build an archive server-side and store it as a normal file.
ledgerline-cli files archive create <file-id...> [--folder <id>] \
    --format zip|tar.gz|tar.xz|7z [--level 0-9] [--password-stdin] [--name …] [--into <folder-id>]

# Extract a stored archive (a server-side worker job).
ledgerline-cli files archive extract <file-id> [--password-stdin] [--into <folder-id>] [--here]
```

A password only applies to `zip` and `7z`; the tar family has no encryption.
Extraction creates a folder named after the archive unless `--here` is given.

### Encryption

The server can encrypt a stored file (or a folder subtree, as one archive) to a
public key — PGP or S/MIME — and decrypt it again. Keys and recipients are
managed in the web app; the CLI reads the keyring to resolve ids:

```sh
ledgerline-cli files keys                                    # own keys + recipients
ledgerline-cli files encrypt <file-id> --key <id> [--recipient <id>…]
ledgerline-cli files encrypt --folder <id> --key <id>
ledgerline-cli files decrypt <file-id> --key <id> [--passphrase-stdin]
```

Your own key is always among the recipients, so an encrypted file stays readable
by you. A private-key passphrase has **no** command-line flag — it is read from
stdin only.

### `audit`

```sh
ledgerline-cli audit show [-n N] [--raw]
ledgerline-cli audit path
ledgerline-cli audit purge --yes
```

## How authentication works

**`auth login`** posts your e-mail and password to the server. If the account
has a second factor, the server answers "not without the code" and issues
nothing until a valid TOTP or recovery code arrives — the factor is enforced
there, not here, so reaching the API directly does not skip it. Neither the
password nor the code is written to disk.

**`auth pair`** reuses the mechanism the Ledgerline mobile app uses. The app
pairs by scanning a QR code; the CLI, having no camera, uses the same one-time
code shown as copyable text.

1. The web session (the trust anchor) generates a one-time code, valid for 60
   seconds, and shows it to you.
2. `auth pair` submits the code to the server, naming this device.
3. You approve the named device in the web app.
4. The CLI collects the token.

Either way the result is a durable [Laravel Sanctum](https://laravel.com/docs/sanctum)
bearer token, verified against `/me` before it is stored, and sent as
`Authorization: Bearer <token>` on every later request. Each sign-in also
carries a stable per-installation id, so signing in again replaces this
machine's entry in the device list instead of adding another one. The token can
be revoked at any time from the web profile's device list, or with
`auth logout`.

## Security notes

- **Transport:** HTTPS is required for all remote servers, with **TLS 1.3** as the
  floor. Plain HTTP is accepted only for loopback hosts (`localhost`,
  `127.0.0.0/8`, `::1`) to ease local development. A redirect that would downgrade
  the scheme or cross to another host is refused, so the bearer never follows a
  request off-origin.
- **Certificate pinning (TOFU):** the first time the CLI connects to an https
  server it records that certificate's public-key hash in `pins.json` (config
  directory, `0600`). Later connections whose key differs are refused — defence in
  depth on top of normal CA validation. If your certificate legitimately changes,
  the error names the `pins.json` path; remove the entry to trust the new key.
- **Token storage:** the bearer lives in the OS credential store (macOS Keychain,
  Windows Credential Manager, Linux Secret Service over D-Bus). Without one — a
  headless server, an SSH session with no session keyring — it falls back to a
  `0600` file in a `0700` directory, and `auth status` reports which backend is in
  use. The token never appears in argv, the environment or any log.
- **Remote kill switch:** every files/gallery command starts by calling `/me`. A
  401 clears the local credential; a pending remote wipe erases all local state.
- **Content is plaintext:** the server stores your bytes in the clear (this is the
  platform's model). What the client guarantees is that they only travel over a
  pinned TLS 1.3 connection. `files encrypt` is an explicit, server-side
  public-key operation on top of that, not a client-side end-to-end scheme.
- **Local WebDAV endpoint:** `files webdav` binds loopback and requires generated
  Basic-auth credentials by default, because any local user can reach a local
  socket. `--no-auth` is an explicit opt-out and is refused off loopback. Bodies
  in flight live in temp files that are removed when the handle closes.
- **Audit log:** every operation is recorded to a local, append-only JSON-lines
  log (`audit.log`, `0600`, size-rotated): command, outcome, duration, timestamp
  — **metadata only, never tokens or content**, and never sent anywhere. View with
  `audit show`, locate with `audit path`, delete with `audit purge --yes`.
- **No telemetry:** the CLI contacts only your configured server and (for
  `status`) GitHub's public release API.

## Development

```sh
make test          # unit tests
make test-race     # race detector (the client is concurrent)
make check         # vet + gofmt + tests + race
make lint          # golangci-lint: staticcheck, errcheck, gosec, bodyclose, noctx…
make tidy-verify   # go.mod tidiness + module checksum verification
make sbom-verify   # CycloneDX SBOM drift check
make repro-verify  # byte-reproducible build check
```

CI runs the suite and the race detector on Linux, macOS and Windows, plus four
gates: lint, security (govulncheck + a gitleaks history scan), supply chain
(tidiness, checksums, SBOM drift, reproducibility, dependency review) and a
cross-compile of every release target. A version tag re-runs all of it before
building and publishing.

Layout:

```
cmd/                    command tree: root, status, auth, devices, audit, gallery, files
cmd/ledgerline-gui/     Windows tray application (systray wiring only)
internal/api/           typed /api/v1 client; files split by feature area
internal/files/         local helpers: tree render, two-way sync, watch service
internal/webdavfs/      webdav.FileSystem over the Files API (the mount backend)
internal/trayui/        tray menu model, avatar and brand icons (platform-free, tested)
internal/deskui/        the desktop windows: a WebView2 host styled with the web app's tokens
internal/win32ui/       the one native dialog left: the shell folder chooser
internal/syncrunner/    the loop both front ends run: interval + file-change detection
internal/applog/        the desktop client's log file (rotating, next to the programs)
internal/authflow/      password (+2FA) and one-time-code sign-in, shared by CLI and GUI
internal/syncconfig/    the folder pairs, shared by the CLI and the tray
internal/installid/     stable per-installation id, so a re-login replaces its device
internal/clientset/     stored session -> pinned, authenticated API client
internal/gallery/       media-file walking, upload naming
internal/uploadledger/  per-server SHA-256 dedup ledger
internal/session/       durable credential (OS keychain + 0600 file fallback)
internal/certpin/       TOFU certificate pinning
internal/audit/         local JSONL operation audit trail
internal/config/ ui/ version/
packaging/nfpm.yaml     .deb / .rpm recipe
packaging/windows/      NSIS installer for the CLI + tray GUI
```

`internal/api` wraps the server's entire `Files` API surface, not only what the
CLI itself calls, so it can serve as the Go library for a future desktop sync
client.

## Versioning

This project follows [Semantic Versioning](https://semver.org/). Notable changes
are recorded in [CHANGELOG.md](CHANGELOG.md). Releases are tagged
`vMAJOR.MINOR.PATCH` and the running binary reports its version, commit and build
date via `ledgerline-cli status`.

## License

[MIT](LICENSE) © 2026 Malte Kiefer
