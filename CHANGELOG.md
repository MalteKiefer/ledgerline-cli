# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.4.0] - 2026-07-13

### Added

- Parallel gallery uploads: `gallery upload --jobs N` (`-j`, default 4) uploads N
  items concurrently. Since most of an upload is spent waiting on the network and
  the server's transform step, this is a large speed-up for big libraries. The
  manifest is still saved once per `--batch` at a barrier, so a save never races
  an in-flight upload; duplicate detection, `--delete` verification and Live Photo
  pairing are unchanged.
- `gallery upload --ml-local <url>` runs the CLIP-embedding and face-detection
  pass on a local [immich-machine-learning](https://immich.app/) instance instead
  of the server, offloading the most expensive part of an upload to a box you can
  put on a GPU and tune. The client asks the server only for the cheap
  derivations, sends the medium rendition to the local instance's `/predict`
  endpoint, crops faces locally and folds the results into the photo's metadata
  exactly as a server-ML upload would; blobs are still encrypted on the client
  first. Tunable via `--ml-clip-model`, `--ml-face-model` and `--ml-min-score`
  (which must match the server's configured models for cross-client consistency).
  `--ml` and `--ml-local` are mutually exclusive.
- `internal/ml` package: a client for the immich-machine-learning `/predict` API.

### Changed

- The gallery `Store` is now safe for concurrent uploads (its added-record list
  and signature index are mutex-guarded); a new race test covers the parallel
  path.

## [0.3.1] - 2026-07-13

### Security

- Sync heartbeat no longer discloses metadata: it previously sent remote folder
  names and progress counts to the server; it now reports only a generic module
  tag, as a zero-knowledge client must.
- Path-traversal containment on `files download` and `files sync`: local write
  destinations built from manifest-controlled names are now confined to the
  target directory, so a hostile record name cannot escape it.
- Decrypted output is written `0600` and its directories `0700`; both
  `files`/`gallery` writers refuse to follow a pre-existing symlink at the target.
- The cached vault key is never written as plaintext: caching is refused when no
  OS keychain is available, and `session` logout clears both stored secrets.
- Server-supplied Argon2id parameters are clamped to a safe range, and blob
  downloads are size-bounded, preventing a hostile server from weakening the key
  derivation or exhausting memory.
- Transport hardening: TLS 1.2 minimum, redirects that refuse scheme downgrades
  and cross-host hops, and a version-less User-Agent to the server.
- The build is pinned to Go 1.26.5, clearing GO-2026-5856 (crypto/tls Encrypted
  Client Hello privacy leak); `govulncheck` reports no reachable vulnerabilities.

### Changed

- Introduced the shared `internal/manifeststore` engine and reduced the Files and
  Todos stores to thin wrappers over it, removing the duplicated conflict-safe
  save logic (no behavioural change).
- The sync state key uses SHA-256, and `github.com/spf13/pflag` is updated to
  v1.0.10.
- Added a CI workflow (build, vet, gofmt, tests, vulnerability scan) and a
  release workflow that cross-compiles the Linux and macOS binaries, publishes
  SHA-256 checksums, and creates the GitHub release from the changelog.

## [0.3.0] - 2026-07-12

### Added

- `files` command group — a full sync client for the zero-knowledge Files module
  (which lives in the shared workspace manifest; other modules like notes and
  bookmarks are always preserved verbatim).
  - `files ls [path]` — list a folder's subfolders and files (colour-coded with a
    monochrome per-type Nerd Font icon), `-R` to recurse; `--color` and `--icons`
    control styling.
  - `files upload` — upload a local folder tree, recreating subfolders; a changed
    file adds a version, an unchanged one is skipped. `--hidden` includes dotfiles.
  - `files download` — decrypt files to a local folder, preserving the tree;
    `--remote` limits to a subtree, `--force` overwrites.
  - `files sync` — **bidirectional** sync between local folders and the encrypted
    files, with a local sync-state database to detect which side changed:
    - Map one or more remote folders to local directories (`--map remote:local`,
      repeatable) or map the whole store into one folder; mappings can also live
      in the settings file.
    - Deletion propagation via `--delete` (`both` | `additive` | `to-remote`).
    - Conflict handling via `--conflict` (`keep-both` | `newest` | `skip`).
    - `--hidden` for dotfiles, ignore patterns from the settings file plus
      `--ignore`, and `--dry-run` to preview.
- `todo` command group — manage encrypted todos and lists (also in the shared
  manifest; other modules preserved): `ls` (filter by list/tag, open/done/marked/
  trash), `add` (title, `--desc/--url/--due/--priority/--list/--tags/--mark`),
  `done`/`undone`, `mark`/`unmark`, `edit`, `rm` (`--force` to delete),
  `restore`, and `lists` (`add`/`rm`/`rename`). Todos are referenced by a short
  id prefix.
- `files rm <path>` — delete a file or folder: trash by default (restorable in
  the web app) or `--force` to erase permanently and reclaim blobs; a folder
  needs `--recursive`.
- `files open <path>` — decrypt a file to a private temp copy and open it with
  the OS default app (`--wait` deletes the copy after the app closes).
- `auth unlock` / `auth lock` — cache the unlocked vault key (default 24h, e.g.
  `--remember 7d`) so gallery/files/todo run without re-entering the passphrase;
  the key lives in the OS keychain (or a 0600 file with a warning). A logout or a
  server-side device revoke clears it, and any 401 wipes the local credential and
  cached key cleanly. `auth status` shows the vault lock state.
- Remote kill switch + sync heartbeat: the CLI reports sync activity to the
  server (so the web shows whether a client is syncing), and when the owner
  requests a remote wipe from the web the client erases all local state
  (credential, cached key, sync state, settings) on its next contact.
- User-editable settings file (`settings.json` in the config dir): `ignore`
  patterns, sync `sync` mappings, and a `hidden` default.
- `gallery upload --batch N` — flush (save, and with `--delete` remove verified
  local files) after every N uploads instead of a fixed 50.
- `internal/files` and `internal/settings` packages; shared blob helpers.

## [0.2.0] - 2026-07-12

### Added

- `gallery upload` — end-to-end-encrypted photo/video upload (folder and Google
  Photos Takeout modes). Files are encrypted on the client before upload; the
  server only ever stores ciphertext.
  - Reproduces the web vault's cryptography in Go (crypto_secretbox, Argon2id,
    and a from-scratch XChaCha20-Poly1305 secretstream), verified byte-for-byte
    against libsodium known-answer tests, so uploads interoperate with the web
    and Android clients.
  - Derives and seals thumbnails, medium renditions, EXIF, location, perceptual
    hash and — with `--ml` — face crops and search embeddings, exactly as the
    web client stores them, into the sharded v2 gallery manifest.
  - Handles Live / Motion photos from any vendor: a same-named video is paired as
    the motion clip, Apple Live Photos split across two files are paired by
    content id, and Google/Samsung embedded Motion Photos are extracted.
  - Accepts all common image and video formats (including RAW and HEIC/HEIF/AVIF)
    and skips byte-identical duplicates already in the gallery.
  - `-d`/`--delete` removes each local file only after its upload is saved and the
    stored copy has been re-downloaded, decrypted and verified byte-for-byte.
- `gallery download` — decrypt and download the gallery to a local folder (a
  plaintext export). Restrict with `--from`/`--to` (date range), `--images` or
  `--videos`; existing files are skipped unless `--force`. Each file keeps its
  original name (id-suffixed on collisions) and capture time; trashed photos are
  excluded.
- `internal/crypto`, `internal/vault` and `internal/gallery` packages.

## [0.1.0] - 2026-07-12

### Added

- Initial release: a console client for a self-hosted Ledgerline server.
- `auth login` — authenticate with a one-time, 60-second code copied from the
  web profile's "Command-line client" card. The client claims the code, waits
  for the owner to approve the device in the web app, and collects a durable
  first-party token. Authentication is zero-knowledge: the token proves identity
  only and never unlocks a vault.
- `auth status` — show the current identity, target server, token storage
  backend, and storage usage; detects a revoked or expired credential.
- `auth logout` — revoke the token server-side and remove it locally.
- `status` — print the repository, installed version, commit hash, build date,
  platform, and whether a newer release is available on GitHub.
- Secure credential storage: the token is kept in the operating system keychain
  (macOS Keychain / Linux Secret Service) and falls back to a `0600` file when
  no keychain is available.
- `gallery upload` command surface (folder and Google Photos modes) with input
  validation; the upload pipeline itself lands in a later release.
- Cross-platform build tooling producing Linux and macOS binaries with embedded
  version metadata.

[Unreleased]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/MalteKiefer/ledgerline-cli/releases/tag/v0.1.0
