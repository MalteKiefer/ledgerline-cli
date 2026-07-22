# CLAUDE.md — ledgerline-cli

Operating manual and single source of truth for the state of THIS repo, for the
next agent (human or model). Re-read the LIVING sections at every session
bootstrap and reconcile them at close-out. Keep it current in the SAME commit as
any change that affects it. Never put a real secret/key/token/VK here — use
obvious placeholders.

---

## 1. Identity & scope

`ledgerline-cli` is the command-line client of the **Ledgerline** zero-knowledge,
post-quantum sealed-store platform. It is one of four clients (Laravel/web, iOS,
this CLI, Android) that share ONE crypto + serialization contract.

**The CLI is the capability FLOOR of the platform.** It must upload/download and
produce valid, readable v3 records *without* plaintext ever leaving the host, and
without a GPU/ML stack/image codecs. It writes **partial records** (original +
basics, marking `thumbPending`/`mlPending`) that richer clients backfill and
re-seal. Everything it writes must be a first-class v3 artifact readable by every
other client.

**Cross-client authority:** the v3 spec `docs/GALLERY-ARCHITECTURE.md` in the web
(Laravel) repo. Where this repo and the spec disagree, the spec's latest resolved
decision wins and the disagreement is logged here (§13), not silently resolved.

## 2. Shared-contract status  [LIVING]

- **Aligned to:** web repo `ledgerline` @ branch `develop`, Store v3 through
  commit `6f3c8f2e` (2026-07-22). Verified 2026-07-22: §17 fixtures byte-identical
  to the web copies; openapi endpoint shapes match. Web crypto commits since the
  initial align, assessed:
  - `6c4b4eb7` ML-KEM identity secret = 64-byte FIPS-203 seed — CLI already
    matches (`Identity.MLKEMSeed()` = `dk.Bytes()`; `NewIdentityFromSecrets` uses
    `NewDecapsulationKey768(seed)`). No change.
  - `863fd306` Argon2id — server-side public-share-password gate only
    (`config/hashing.php`), not the vault KDF/KEM. No CLI impact. (HKDF per-vault
    `context` stays `""` platform-wide — coordinated open item; CLI uses `""`.)
  - `6f3c8f2e` `/gallery/process`+`/analyze` return `model` (CLIP name) so native
    clients tag `embModel` authoritatively (§8.5) — ADOPTED: `ProcessResult.Model`
    parsed; `pickEmbModel` uses the server model for a server-produced embedding,
    the local model for a local-analyzer embedding, fallback to configured.
- **Divergence from spec:** none currently known.
- **Contract elements this client owns:** `internal/canonicaljson` (canonical
  JSON §5.2), `internal/crypto` FileCrypto blob frame + manifest seal + hybrid
  KEM, `internal/shard` content-addressed id-bucketing (§5.1), the partial-record
  shape (§8.1). Any change to these is "contract impact: yes — spec update
  required" and must be reported.
- **Consumed API surface (openapi.yaml, verified 2026-07-22):**
  `GET/PUT /store/{module}` (todos), `GET/PUT /gallery/store` + `/gallery/upload`
  + `/gallery/raw/{blob}` + `/gallery/process`, `GET/PUT /files/store` +
  `/files/upload` + `/files/raw/{blob}` + `/files/blob/{blob}`. Upload → `{id}`
  (IdResponse); store GET/PUT → `{ciphertext,version}` / `{version}`; 409 =
  version_conflict. Monolith `/store` is removed.

## 3. Threat model & trust boundaries

STRIDE + LINDDUN model, versioned with the architecture (full artifact §14).

Adversaries: nation-state (legal compulsion of the server operator, network
interception + harvest-now-decrypt-later, Go-module supply-chain influence),
corporate/insider (server operator, backup/backend access, coercion),
criminal/host-level (compromised or shared multi-user host, another user, malware
reading process memory/files, shoulder-surfing, poisoned dependency).

CLI-specific host assumptions: the host may be multi-user; **argv, environment,
shell history, temp paths, and core dumps are leak channels** and are treated as
hostile to the CLI's secrets. The server is untrusted (sees only ciphertext +
opaque sealed root + blob-size ledger). The network is hostile and recorded. The
binary is fully disclosed — no compiled-in secrets.

## 4. Architecture & module map

```
cmd/                    command tree (root, status, auth, gallery, files, todo)
internal/api/           typed /api/v1 HTTP client (per-module + sharded store, blobs)
internal/crypto/        VK hierarchy, blob frame, manifest seal, hybrid KEM   [reuse seam]
internal/canonicaljson/ Store v3 canonical JSON — ONLY path to sealed/hashed bytes [seam]
internal/shard/         content-addressed id-bucketing (§5.1)                  [seam]
internal/blobcache/     on-disk CIPHERTEXT shard cache (ref-addressed, 0600)
internal/conformance/   §17 cross-client fixtures + KATs (release gate)
internal/gallery/        v3 sharded gallery store + upload pipeline            [sharded engine]
internal/files/          v3 sharded files store (same engine shape) + tree/sync
internal/manifeststore/  per-module sealed-row engine (todos)
internal/todo/ ml/ vault/ session/ settings/ certpin/ config/ version/ ui/
```

Reuse seams (do not duplicate): `canonicaljson` (sealed/hashed bytes),
`crypto` (VK/frame/seal/KEM), `shard` (bucketing). Gallery and files each hold a
sharded store; they are structurally the same engine (see §12 open item on
extraction).

## 5. Dependency inventory  [LIVING]

Policy: **standard library first.** Every third-party module is justified, pinned
in `go.mod`+`go.sum`, checksum-verified (`GOFLAGS=-mod=readonly` in CI).

Direct:
- `github.com/spf13/cobra v1.10.2` — CLI command tree. Maintained.
- `github.com/zalando/go-keyring v0.2.8` — OS keyring for token/VK-cache storage.
- `golang.org/x/crypto v0.54.0` — **justified**: Argon2id, XChaCha20-Poly1305
  secretstream (built on chacha20+poly1305), nacl/secretbox, blake2b. No stdlib
  equivalent for Argon2id or secretstream. (curve25519/hkdf subpackages are NO
  longer used — migrated to stdlib crypto/ecdh + crypto/hkdf, 2026-07-22.)
- `golang.org/x/term v0.45.0` — no-echo passphrase reads + TTY detection.

Indirect: wincred, godbus/dbus/v5, mousetrap, spf13/pflag, x/sys.

Stdlib crypto in use: `crypto/mlkem` (ML-KEM-768), `crypto/ecdh` (X25519),
`crypto/hkdf` (HKDF-SHA256), `crypto/rand`, `crypto/subtle`, `crypto/sha256`.

Updates available (2026-07-22, all INDIRECT, none security-critical/uncalled):
go-md2man v2.0.6→2.0.7, objx v0.5.2→0.5.3, x/net v0.56.0→0.57.0, check.v1.
`govulncheck`: 0 vulns affecting code; 1 in a required (indirect) module, not
called. Toolchain: go 1.25.0 directive, toolchain pinned go1.26.5 (GO-2026-5856).

## 6. Cryptographic inventory

- **Key hierarchy:** passphrase → Argon2id → KEK → unwrap VK (32 B, process-only,
  never on disk in plaintext). Argon2id params from the server (`ops`,`mem`) via
  `crypto.DeriveKEK`; contract floor is ops=4/mem=256 MiB (shared with
  memory-constrained mobile — do NOT raise above the floor). **Host/cgroup memory
  check before derivation is NOT yet implemented — §12 open item.**
- **Blobs:** fresh 32-B per-blob key (crypto/rand); XChaCha20-Poly1305
  secretstream, 4 MiB chunks, frame `[header(24)]([u32le len][cipher len+17])*`
  final tag last, then Padmé. Per-blob key secretbox-wrapped under VK → `{c,n}`.
  **No suite byte on blobs** (agility at manifest level).
- **Manifest seal:** `secretbox(canonicalJSON(obj), VK)`, Padmé, 4 KiB floor,
  suite-tagged → `{suite:1,c,n}`. `OpenManifest` fails closed on unknown suite.
- **Hybrid KEM (sharing/identity only):** X25519 (crypto/ecdh) + ML-KEM-768
  (crypto/mlkem); `wrapKey = HKDF-SHA256(ss_ec‖ss_pq, info="ledgerline/kem/v1"‖
  context, 32)` (crypto/hkdf); envelope `{suite:1,epk,kem_ct,c,n}`. crypto/ecdh
  ECDH rejects low-order (all-zero) results → fail-closed. Personal gallery/files
  do NOT use it (symmetric, already PQ-safe at rest).
- **Hashing:** SHA-256 only for shard bucket hash = `SHA-256(canonicalJSON(recs))`.
- **Randomness:** crypto/rand only (keys, nonces, ids, padding); ids = 128-bit hex.
- **Comparison:** secret comparisons via crypto/subtle (secretstream MAC compare).
- **Transport:** TLS 1.3 floor (`MinVersion: VersionTLS13`), cert verification
  always on, TOFU pinning (certpin). No InsecureSkipVerify. Loopback http allowed
  for local dev only.

RFCs: 8446 (TLS), 9106 (Argon2), 5869 (HKDF), FIPS 203 (ML-KEM), 7748 (X25519),
8259 (JSON).

## 7. Data classification & flows

- **Passphrase:** secret. No-echo terminal read (`x/term`), never argv/env/history.
  In memory only; derives KEK→VK then dropped.
- **VK / per-blob keys:** secret. Process memory only; never serialized plaintext.
- **Bearer token:** secret. OS keyring where available, else 0600 file fallback
  (§11 register). Never logged, never argv/env.
- **Shard cache:** CIPHERTEXT only (encrypted shard blobs, same bytes the server
  holds), under `<config>/cache`, 0600 files in a 0700 dir, purged on logout. No
  plaintext at rest (§7) — decryption stays in memory.
- **Content:** plaintext local only. Flow: read file → seal locally → upload
  ciphertext. Download: fetch ciphertext → decrypt locally. Default upload sends
  NO plaintext to the server; `--process`/`--ml` explicitly opt into the
  transient-plaintext `/process` path (logged intent).

## 8. Metadata leakage table

| Observable | Adversary | Reveals | Mitigation / residual |
|---|---|---|---|
| blob count ≈ item count | server | library size | inherent ZK residual (accepted) |
| blob size | server/network | rough item size | Padmé bucketing |
| upload timing | server | activity time | server snaps created_at to the hour |
| access pattern | server | which refs read | immutable ref-addressed cache + 404-hiding |
| TLS metadata (SNI, size, timing) | network | endpoint/volume | classical TLS residual (§11) |
| CLI request pattern | server/network | "a CLI client" | keep aligned with other clients (§26) |

## 9. Compliance mapping

- RFCs: implemented per §6 (TLS 8446, Argon2 9106, HKDF 5869, ML-KEM FIPS 203,
  X25519 7748, JSON 8259). OWASP ASVS client controls: input validation at trust
  boundaries, no secrets in logs, fail-closed crypto. SOC2/CIS: change mgmt via
  git; encryption in transit + client-side at rest; no user data in logs.
- Gaps vs the full manual (SBOM diff, reproducible-build verification in CI,
  signed commits, two-person review enforcement) tracked in §12.

## 10. Performance budgets & measured results

- Blob crypto is chunked (4 MiB) and streamed; sharded reads windowed. Memory
  target O(worker pool × 4 MiB) at 18k/100k. **Not yet formally benchmarked under
  a constrained cgroup — §12 open item.** Gallery upload worker pool is bounded
  by `--jobs` (default 4). No crypto/network at init.

## 11. Security register  [LIVING]

| Item | Owner | Rationale | Compensating control | Review by |
|---|---|---|---|---|
| Classical passkey signatures (platform) | platform | WebAuthn RP ecosystem lacks PQ COSE; auth not HNDL-confidentiality | private JWK sealed under VK (PQ-safe at rest) | 2026-10-01 |
| Classical TLS beyond the ZK boundary | maintainer | only ciphertext transits | ZK payload + cert pinning (certpin) | 2026-10-01 |
| Plaintext token 0600-file fallback when no OS keyring | maintainer | headless/SSH hosts have no Secret Service | 0600 in 0700 dir; `auth status` reports backend; logout shreds | 2026-10-01 |

No entry is past review. An expired entry blocks new work.

## 12. Open items  [LIVING]

Gaps against the operating manual's Definition of Done (honestly logged; none are
correctness/interop defects — conformance is green):
- DONE 2026-07-22: Argon2 host/cgroup memory guard before derivation
  (`internal/vault/memguard*.go`, wired into `Unlock`); server-param safe-band
  validation already present (`validateKDF`). DONE: uniform decryption-failure
  test (§28) + credential-directory 0700 test.
- DONE 2026-07-22: fuzz tests for the hostile-input parsers — canonical JSON
  decode (`FuzzCanonicalize`, idempotent), blob-frame decode (`FuzzDecryptContent`,
  bounded/no-panic), sealed-manifest envelope (`FuzzOpenManifest`). 10s active
  runs clean (millions of execs). Share links: the CLI has no share-link parser
  (no sharing command) — N/A.
- DONE 2026-07-22: no-secret-in-output — crypto error strings carry no key
  material (`TestNoSecretInErrorStrings`); a real upload flow stores only
  ciphertext (`files.TestUploadStoresNoPlaintextOrKey`).
- DONE 2026-07-22: constant-time failure FLOOR — `vault.padFailure` pads a
  wrong-passphrase / wrong-recovery failure to a 750 ms monotonic floor
  (uniform-duration + brute-force speed bump, §23/§28); tested (floor applied,
  no over-sleep when already elapsed, prompt on context cancel). A full
  statistical timing-distribution test is still deferred (flaky).
- memory-ceiling cgroup INTEGRATION test at 18k (the guard's parsers are
  unit-tested; a real-cgroup run is CI infra) — open.
- DONE 2026-07-22: the CLI now reclaims its OWN freed blobs after a successful
  re-seal — freed record shards (both stores) + a replaced folders collection
  blob (files) are DELETEd, but only after the new root PUT succeeds (an
  interrupted/failed delete leaves a harmless orphan, never data loss). This is
  targeted and safe; the CLI still does NOT trigger the destructive full-live-set
  `/blobs/reconcile` sweep — that decision is in §13.
- DONE 2026-07-22: TLS floor raised to 1.3 (`MinVersion: VersionTLS13`).
- DONE 2026-07-22: content-addressed CIPHERTEXT shard cache
  (`internal/blobcache`, wired into gallery+files loads, purged on logout) so
  repeated/resumed loads skip re-fetching unchanged shards. Still open: ETag/304
  on the root GET (minor — root is tiny) and a DECRYPTED-plaintext cache (would
  be opt-in per §7; deliberately not built — the ciphertext cache keeps no
  plaintext at rest and captures the network win).
- DONE 2026-07-22: SBOM (CycloneDX, `make sbom` → committed `sbom.json`) diffed
  in CI (`make sbom-verify`); reproducible-build verification in CI
  (`make repro-verify`, BUILD_DATE pinned to the commit date). Still open:
  signed-commits/tags enforcement + two-person-review gate (org/branch-protection
  policy, not enforceable from the repo tree).
- On-device derivation (JPEG/PNG thumb, local exiftool EXIF) not implemented;
  the CLI floor writes partial records for GUI backfill (spec-optional §8.1).
- TLS MinVersion raise to 1.3 where deployments allow (§11 register item).

## 13. Conflicts & decisions  [LIVING]

- **Manual §5 mandates X25519 via crypto/ecdh + HKDF via stdlib.** C2 originally
  used `x/crypto/curve25519` + `x/crypto/hkdf`. Resolved 2026-07-22: migrated to
  `crypto/ecdh` + `crypto/hkdf` (Go 1.26). Byte-compatible (RFC 7748/5869);
  §17 ML-KEM KAT + hybrid wrap/unwrap conformance stayed green.
- **Spec §5.2 wording said "NFC".** The web canonical-json fixture keeps strings
  verbatim (no normalization). Fixture wins — `canonicaljson` does NOT normalize.
- **Spec §6.1 draft said blobs carry a 1-byte suite prefix.** The shipped web +
  §17 blob-frame fixture have NO blob suite byte (spec doc `c48351b` corrected
  this). CLI matches: suite only in the manifest/KEM envelope.
- **Float handling:** `canonicaljson` rejects all non-integer numbers (stricter
  than the web's `String(n)`). Safe under §5.2 (dec-string coords, int duration);
  CLI-authored records are float-free. If the web ever writes a float into a hot
  record, shard hashes would diverge — noted, contract says it won't.
- **ML-KEM KAT split:** Go stdlib `Encapsulate()` is not seedable and
  `dk.Bytes()` returns the 64-B seed (not the 2400-B expanded key), so the
  deterministic `ct/dk` KAT values are validated JS-side; Go pins `ekSha256`
  (seed→ek, matches @noble exactly) + a live encaps→decaps round-trip.

- **Full-live-set reconcile is intentionally NOT triggered by the CLI**
  (2026-07-22). `/gallery|files/blobs/reconcile` GC-sweeps every blob NOT in a
  caller-supplied live-set; a live-set missing any ref class (incl. face-crop
  refs that live in cold meta blobs) = server-side data loss (§17/§4a). The CLI
  has no need for it — it reclaims its own freed shard/collection blobs directly
  (§12), and orphaned blobs are harmless. Implementing a full reconcile would add
  a destructive path whose correctness depends on decrypting every meta blob to
  gather crop refs; the risk outweighs the benefit for the capability floor.
  Escalate before adding it: it needs the complete live-set + the interrupted-run
  blocking test the spec mandates.

## 14. Deviations  [LIVING]

- ML-KEM KAT verification is split (see §13) — a documented, spec-consistent
  consequence of the stdlib API, not a weakening.
- No deviations from the byte contract. (If any partial-record/sharding/canonical
  change is made, mark "contract impact: yes" in §2 and report.)

## 15. Changelog

- 2026-07-22 `<pending>` feat: content-addressed ciphertext shard cache
  (`internal/blobcache`), purged on logout; SBOM verify ignores the module's own
  git pseudo-version.
- 2026-07-22 `0312239` feat(files): parallel `files upload --jobs` (bounded
  worker pool; slow network step unlocked, tree+ops staging serialized via
  Uploader.stageMu; batch-barrier save). Race-clean.
- 2026-07-22 `8839273` gallery/files: reclaim freed shard/collection blobs
  after a successful re-seal (no orphan accumulation; no full-reconcile).
- 2026-07-22 `72e5aa9` supply-chain: CycloneDX SBOM (committed + CI diff),
  reproducible-build verification (deterministic commit-date BUILD_DATE).
- 2026-07-22 `db03e22` perf/sec: raw-batch cold shard load (gallery+files); TLS
  1.3 floor; constant-time failure floor on unlock/recovery.
- 2026-07-22 `04b4161` fix(gallery): tag embModel from the server-returned CLIP
  `model` (/process) per web `6f3c8f2e` (§8.5 cross-client search coherence).
- 2026-07-22 `<merged>` test: fuzz parsers + no-secret-in-output; fix(files):
  quiet per-batch checkpoint.
- 2026-07-22 `<merged>` feat(files): `files upload --batch N` — periodic
  progress save (crash-safe/resumable), matching gallery.
- 2026-07-22 `0d8657f` sec(vault): Argon2id host/cgroup memory guard (fail
  closed before an OOM kill on a memory-constrained host); uniform decryption
  failure (§28) — truncated blob now returns ErrDecrypt like wrong-key/corrupt;
  config-dir 0700 test.
- 2026-07-22 `ddb44d3` sec(crypto): hybrid KEM migrated to stdlib crypto/ecdh +
  crypto/hkdf (manual §5, byte-compatible); add CLAUDE.md operating manual.
- 2026-07-22 `a83121f` Store v3 Track C (C1–C6): canonical JSON, suite envelope +
  PQ hybrid KEM, content-addressed sharded gallery, partial records, per-module +
  sharded files stores. Released v0.7.0.
