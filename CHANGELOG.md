# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/MalteKiefer/ledgerline-cli/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/MalteKiefer/ledgerline-cli/releases/tag/v0.1.0
