# ledgerline-cli

[![CI](https://github.com/MalteKiefer/ledgerline-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/MalteKiefer/ledgerline-cli/actions/workflows/ci.yml)
[![Release](https://github.com/MalteKiefer/ledgerline-cli/actions/workflows/release.yml/badge.svg)](https://github.com/MalteKiefer/ledgerline-cli/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A console client for a self-hosted [Ledgerline](https://github.com/MalteKiefer/Ledgerline)
server, written in Go. It runs on **Linux** and **macOS**.

The client is designed to grow into a full-featured tool. Today it covers secure
authentication and end-to-end-encrypted gallery upload; more commands will follow.

Authentication is **zero-knowledge by design**: the stored credential proves your
identity to the server and nothing more. It never unlocks your vault, and the CLI
collects no telemetry.

## Contents

- [Install](#install)
- [Build from source](#build-from-source)
- [Configuration](#configuration)
- [Usage](#usage)
  - [`status`](#status)
  - [`auth login`](#auth-login)
  - [`auth status`](#auth-status)
  - [`auth logout`](#auth-logout)
  - [`auth unlock` / `auth lock`](#auth-unlock--auth-lock)
  - [`gallery upload`](#gallery-upload)
  - [`gallery download`](#gallery-download)
  - [`files`](#files)
  - [Settings](#settings)
  - [`todo`](#todo)
- [How authentication works](#how-authentication-works)
- [Security notes](#security-notes)
- [Development](#development)
- [Versioning](#versioning)
- [License](#license)

## Install

Download the binary for your platform from the
[releases page](https://github.com/MalteKiefer/ledgerline-cli/releases), make it
executable, and place it on your `PATH`:

```sh
# Example for macOS on Apple silicon; pick the version and asset for your system.
VERSION=0.3.1
ARCH=darwin-arm64
base=https://github.com/MalteKiefer/ledgerline-cli/releases/download/v$VERSION
curl -LO "$base/ledgerline-cli-$VERSION-$ARCH"
curl -LO "$base/checksums.txt"

# Verify the download before installing.
grep " ledgerline-cli-$VERSION-$ARCH\$" checksums.txt | shasum -a 256 -c -

chmod +x "ledgerline-cli-$VERSION-$ARCH"
sudo mv "ledgerline-cli-$VERSION-$ARCH" /usr/local/bin/ledgerline-cli
```

Supported release targets: `linux/amd64`, `linux/arm64`, `darwin/amd64`,
`darwin/arm64`. Every release ships a `checksums.txt` with the SHA-256 of each
binary (use `sha256sum -c` on Linux). Releases are built and published from a
version tag by the [release workflow](.github/workflows/release.yml), which
runs the full test and vulnerability-scan suite first.

Verify the install and check for updates:

```sh
ledgerline-cli status
```

## Build from source

Requires Go 1.25 or newer. The module pins the build toolchain to Go 1.26.5 (see
`go.mod`); an older `go` command fetches it automatically on first build.

```sh
git clone https://github.com/MalteKiefer/ledgerline-cli.git
cd ledgerline-cli
make build           # produces ./bin/ledgerline-cli, version-stamped from git
```

Cross-compile all supported targets into `./dist`:

```sh
make release
```

The build embeds version, commit hash, and build date via `-ldflags`, so the
binary can report its own provenance (`ledgerline-cli status`).

## Configuration

State lives in the per-user config directory:

- Linux: `$XDG_CONFIG_HOME/ledgerline-cli` (default `~/.config/ledgerline-cli`)
- macOS: `~/Library/Application Support/ledgerline-cli`

`config.json` (permissions `0600`) holds non-secret data: the server URL and your
identity. The bearer token is stored separately in the OS keychain (see
[Security notes](#security-notes)).

Override the directory with `LEDGERLINE_CLI_CONFIG_DIR` if needed.

## Usage

Every command has `--help`:

```sh
ledgerline-cli --help
ledgerline-cli auth --help
ledgerline-cli gallery upload --help
```

### `status`

Print build metadata and check for a newer release:

```console
$ ledgerline-cli status
Repository:  https://github.com/MalteKiefer/ledgerline-cli
Version:     0.3.1
Commit:      a1b2c3d
Built:       2026-07-13T06:46:45Z
Go:          go1.26.5
Platform:    darwin/arm64
Update:      up to date (latest v0.3.1)
```

The update check is a single unauthenticated request to GitHub and degrades
gracefully when offline.

### `auth login`

Authenticate with a one-time code from the web app:

1. In the Ledgerline web profile, open the **Command-line client** card and
   generate a code. It is valid for **60 seconds**.
2. Run `ledgerline-cli auth login` and paste the code when prompted.
3. Back in the web app, **approve** the device that appears.
4. The CLI stores the resulting token and confirms your identity.

```console
$ ledgerline-cli auth login
Server URL: https://ledger.example.com
One-time code (from the web profile): abc…
Code accepted. Approve "ledgerline-cli@laptop" in the web app to continue.
Logged in as Ada Lovelace <ada@example.com> on https://ledger.example.com.
Token stored in the OS keychain.
```

Non-interactive use (for scripts):

```sh
ledgerline-cli auth login --server https://ledger.example.com --code "$CODE" \
  --device-name "ci-runner"
```

Flags:

| Flag | Description |
| --- | --- |
| `--server` | Server base URL (prompted if omitted). |
| `--code` | One-time code (prompted if omitted). |
| `--device-name` | Name shown for this device in the web app (default `ledgerline-cli@<hostname>`). |

### `auth status`

```console
$ ledgerline-cli auth status
Authenticated as Ada Lovelace <ada@example.com> (id 42)
Server:  https://ledger.example.com
Token:   stored in the OS keychain
Usage:   1.2 GiB in files, 8.4 GiB in gallery
```

If the token was revoked or expired, this command says so and points you to
`auth login`.

### `auth logout`

Revoke the token server-side and remove it locally:

```console
$ ledgerline-cli auth logout
Logged out.
```

### `auth unlock` / `auth lock`

Cache the vault key so `gallery`, `files` and `todo` don't prompt for the
passphrase each time:

```sh
ledgerline-cli auth unlock --remember 24h   # also: 12h, 7d, 4w
ledgerline-cli auth lock                    # clear the cached key
```

The key is stored in the OS keychain (or a `0600` file, with a warning, when no
keychain is available). Logout and a server-side device revoke clear it; any
revoked/expired token wipes the local credential and cached key on the next call.

### `gallery upload`

Upload photos and videos to the gallery. Everything is **encrypted on your
machine** before it leaves it — the server only ever sees ciphertext. Uploading
requires your vault passphrase (prompted, never echoed), which unlocks the vault
key locally; the passphrase and key never leave the machine.

Folder mode:

```sh
ledgerline-cli gallery upload -f /path/to/folder [-r]
```

Google Photos (Takeout) mode:

```sh
ledgerline-cli gallery upload --google-photos -z /path/to/takeout.zip
```

| Flag | Description |
| --- | --- |
| `-f`, `--folder` | Source folder to upload from. |
| `-r`, `--recursive` | Include subfolders. |
| `--google-photos` | Import from a Google Photos (Takeout) export. |
| `-z`, `--zip` | Path to the Google Photos export `.zip`. |
| `--ml` | Run face detection + search embeddings inline (needs the server's ML service). Without it, the web client analyses photos later. |
| `-d`, `--delete` | Delete each local file **after** its upload is saved and the stored copy has been re-downloaded, decrypted and verified byte-for-byte. |

What it handles, matching the web app:

- **All common image and video formats** (JPEG/PNG/HEIC/HEIF/AVIF/TIFF/…, RAW,
  and MOV/MP4/HEVC/…); unsupported files are reported and skipped, not fatal.
- **Live / Motion photos from any vendor.** A same-named video beside a photo, an
  Apple Live Photo split across two files (paired by its content id), and a
  Google/Samsung Motion Photo with an embedded clip are all stored as one photo
  with its motion clip.
- **Thumbnails and metadata.** Thumbnail, medium rendition, EXIF, location,
  perceptual hash (and, with `--ml`, face crops + embeddings) are derived and
  sealed exactly as the web client stores them.
- **Duplicate skipping.** A byte-identical file already in the gallery is skipped
  (matched by size + a hash of its head and tail).
- **Resumable & safe.** Progress is saved periodically; `--delete` only removes a
  local file once its encrypted copy is provably retrievable.

> The gallery is zero-knowledge, so uploads are only reversible from the web app
> (or by deleting the photo there). `--delete` removes local originals — keep a
> backup until you have verified a batch.

### `gallery download`

Download and decrypt the gallery to a local folder (a plaintext export/backup).
Requires the vault passphrase.

```sh
ledgerline-cli gallery download -o /path/to/folder
```

| Flag | Description |
| --- | --- |
| `-o`, `--output` | Destination folder (required). |
| `--from` | Only photos taken on or after this date (`YYYY-MM-DD`, inclusive). |
| `--to` | Only photos taken on or before this date (`YYYY-MM-DD`, inclusive). |
| `--images` | Only images. |
| `--videos` | Only videos. Pass both, or neither, for everything. |
| `--force` | Overwrite files that already exist in the target. |

Each photo is written under its original filename (a short id is appended when
two photos share a name), with its capture time set as the file's modification
time. Files already present are skipped unless `--force` is given, so the command
is resumable. Trashed photos are never downloaded.

### `files`

Work with the encrypted Files module. All commands need the vault passphrase.

```sh
ledgerline-cli files ls       [path] [-R]        # list folders/files (colour + icons)
ledgerline-cli files download -o /local/dir [--remote SubFolder] [--force]
ledgerline-cli files upload   -f /local/dir [--remote Target] [--hidden]
ledgerline-cli files open     <path>             # open with the OS default app
ledgerline-cli files rm       <path> [-r] [-f]   # trash, or --force to erase
ledgerline-cli files sync     --map remote:local [--map …] [flags]
```

`files rm` trashes by default (restore in the web app); `--force` deletes
permanently and reclaims blobs, and a folder needs `--recursive`.

`files ls` shows a folder's contents colour-coded with a monochrome per-type icon
(Nerd Font glyphs; use `--icons none` if your terminal font lacks them, and
`--color never` to disable colour).

**`files sync`** is a two-way sync. It keeps a local sync-state database (in the
config dir) so it can tell which side changed since the last run.

- **Mappings.** Repeatable `--map remote:local` maps a remote folder to a local
  directory (e.g. `--map Photos:/home/me/photos --map Docs:/home/me/docs`). A
  value with no colon maps the **whole store into one folder**
  (`--map /home/me/ledger`). With no `--map`, the `sync` list from the settings
  file is used.
- **Deletions** — `--delete both` (default, propagate both ways) | `additive`
  (never delete, recreate the missing side) | `to-remote` (local deletes trash
  remote; remote never deletes local).
- **Conflicts** (same file changed on both sides) — `--conflict keep-both`
  (default; the remote copy is saved as `name (conflict …).ext` on both sides) |
  `newest` | `skip`.
- `--hidden` includes dotfiles; `--ignore PATTERN` (repeatable) and the settings
  file's `ignore` list exclude paths (gitignore-style); `--dry-run` previews.

> Two-way sync with deletion propagation can remove files. Start with
> `--dry-run`, and consider `--delete additive` until you trust a mapping.

### Settings

`settings.json` in the config dir is user-editable and read by `files sync`:

```json
{
  "hidden": false,
  "ignore": ["*.tmp", "node_modules/", ".git/"],
  "sync": [
    { "remote": "Photos", "local": "/home/me/photos" },
    { "remote": "", "local": "/home/me/ledger-all" }
  ]
}
```

### `todo`

Manage encrypted todos and lists.

```sh
ledgerline-cli todo ls [--list NAME] [--tag T] [--all|--done|--marked|--trash]
ledgerline-cli todo add "Buy milk" --due 2026-07-20 --priority high --list Home
ledgerline-cli todo done <id>        # also: undone, mark, unmark, restore
ledgerline-cli todo edit <id> --title … --due … --list … --priority …
ledgerline-cli todo rm <id> [--force]
ledgerline-cli todo lists            # add <name> | rm <name> | rename <old> <new>
```

Todos are referenced by the short id shown in `todo ls` (a unique prefix is
enough). `--list` on `add`/`edit` creates the list if it does not exist.

## How authentication works

The CLI reuses the same server mechanism as the Ledgerline mobile app. The app
pairs by scanning a QR code; the CLI, having no camera, uses the same one-time
code shown as copyable text.

1. The web session (the trust anchor) generates a one-time code, valid for 60
   seconds, and shows it to you.
2. `auth login` submits the code to the server, naming this device.
3. You approve the named device in the web app.
4. The CLI collects a durable [Laravel Sanctum](https://laravel.com/docs/sanctum)
   bearer token — issued exactly once — and stores it.
5. Every later request sends `Authorization: Bearer <token>`.

The short-lived code is never written to disk. The token can be revoked at any
time from the web profile's device list, or with `auth logout`.

## Security notes

- **Transport:** HTTPS is required for all remote servers. Plain HTTP is accepted
  only for loopback hosts (`localhost`, `127.0.0.1`, `::1`) to ease local
  development.
- **Token storage:** the bearer is stored in the OS keychain (macOS Keychain via
  the Security framework; Linux Secret Service over D-Bus). When no keychain is
  available — for example a headless server or an SSH session without a session
  keyring — it falls back to a `0600` file in the config directory, and
  `auth status` reports which backend is in use.
- **Zero-knowledge:** the token authenticates API calls only. It does not derive,
  hold, or transmit any vault key.
- **No telemetry:** the CLI contacts only your configured server and (for
  `status`) GitHub's public release API. It collects nothing about you.

## Development

```sh
make test    # run the test suite
make check   # vet + gofmt verification + tests
make build   # host binary into ./bin
```

The project layout keeps shared concerns reusable so new commands stay
consistent:

```
cmd/                  command tree (root, status, auth, gallery, files, todo)
internal/api/         typed HTTP client for the /api/v1 surface
internal/crypto/      libsodium-compatible crypto (secretbox, Argon2id, secretstream)
internal/vault/       passphrase → vault key unlock
internal/manifeststore/ shared opaque-manifest engine (conflict-safe save, DRY)
internal/files/       Files module: tree, listing, upload, download, two-way sync
internal/todo/        Todos module: todos and lists over the shared manifest
internal/gallery/     manifest v2, upload pipeline, Live Photo pairing, sources
internal/session/     durable credential storage (keychain + file fallback)
internal/settings/    user-editable settings file (ignore list, sync mappings)
internal/config/      config-directory resolution
internal/version/     build metadata and update checks
internal/ui/          prompts and spinner
```

The Files and Todos modules both live in one sealed *workspace manifest* shared
with the web client (notes, bookmarks, contacts, …). `internal/manifeststore` is
the single engine that decrypts it, stages edits as operations, and re-seals with
optimistic-concurrency retry — always preserving keys owned by other modules
verbatim — so each module is a thin, consistent wrapper.

The gallery crypto reproduces the web vault (`resources/js/vault.js`) byte for
byte and is verified against libsodium-generated known-answer tests, so photos
uploaded by the CLI are readable in the web and Android clients and vice versa.

## Versioning

This project follows [Semantic Versioning](https://semver.org/). Notable changes
are recorded in [CHANGELOG.md](CHANGELOG.md). Releases are tagged `vMAJOR.MINOR.PATCH`
and the running binary reports its version, commit, and build date via
`ledgerline-cli status`.

## License

[MIT](LICENSE) © 2026 Malte Kiefer
