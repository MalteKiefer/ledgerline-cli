# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/MalteKiefer/ledgerline-cli/releases/tag/v0.1.0
