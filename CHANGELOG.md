# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/MalteKiefer/ledgerline-cli/releases/tag/v0.1.0
