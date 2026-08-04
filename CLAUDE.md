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

- **Aligned to:** web repo `ledgerline` Store v3, re-verified 2026-08-01 against
  web HEAD `b88ba9d0`. The web repo briefly detoured to plaintext-relational then
  ROLLED BACK to zero-knowledge (`779978ea`, 2026-07-31) and stacked a
  store-merge-safety contract + additive endpoints on top; those are re-audited
  in this section. Prior baseline was `6f3c8f2e` (2026-07-22, §17 fixtures
  byte-identical). Post-rollback contract audit (2026-08-01):
  - **Client store-merge-safety contract** (web `ca72a61c`/`376575aa`/`4e642cf9`):
    on a 409 the client must rebase-merge (fetch winner, replay its own delta,
    never re-PUT a stale whole copy). CLI already reloads the winner and replays
    session adds (gallery) / staged ops (files) — the no-wholesale-clobber
    property holds. The `shards[]` complete-ref guard is sent on both sharded
    PUTs. The optional `counts` anomaly-scan map is now sent COMPLETE-or-omitted
    (§13). `seq`-collision / invoice-random-id / numeric-vector-LWW rules are N/A
    (CLI writes no invoices, merges no embeddings, canonical-JSON rejects floats).
    Recursive nested deep-merge is deliberately NOT built (§12 — no CLI surface).
  - **Crypto-revocation / identity write-once** (web `2044c7df`): N/A — the CLI
    never publishes an identity keypair, never KEM-wraps in the live path
    (personal stores are symmetric under VK), and the vault is READ-ONLY
    (`GET /api/v1/vault` only; no provision/rotate). No client action needed.
  - Web crypto commits from the initial align, still assessed:
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
  - `a6e21ec3`/`34e4ce4f`/`4fff782b` sharded-store SAFETY (2026-07-24): stop
    eager freed-blob deletion (data-loss race), send `shards[]` integrity guard on
    every store PUT, tolerate a 404 shard as degraded read-only. ALL ADOPTED
    (§12). Logging commits (`efe94e01`/`96c2a409`/`5f3ebc39`/`e8a22112`/`847430ca`)
    are server-side admin audit — no CLI/client component; the server logs the
    CLI's requests automatically.
- **Divergence from spec:** none currently known.
- **Contract elements this client owns:** `internal/canonicaljson` (canonical
  JSON §5.2), `internal/crypto` FileCrypto blob frame + manifest seal + hybrid
  KEM, `internal/shard` content-addressed id-bucketing (§5.1), the partial-record
  shape (§8.1). Any change to these is "contract impact: yes — spec update
  required" and must be reported.
- **Consumed API surface (openapi.yaml, verified 2026-08-01):**
  `GET/PUT /store/{module}` (todos), `GET/PUT /gallery/store` + `/gallery/upload`
  + `/gallery/raw/{blob}` + `POST /gallery/raw-batch` + `/gallery/process`,
  `GET/PUT /files/store` + `/files/upload` + `/files/raw/{blob}` +
  `POST /files/raw-batch` + `DELETE /files/blob/{blob}` (blob delete — NOT a GET;
  a prior note here mis-stated the method). Upload → `{id}`
  (IdResponse); store GET/PUT → `{ciphertext,version}` / `{version}`; 409 =
  version_conflict. Monolith `/store` is removed. Additive endpoints the spec now
  exposes but the CLI deliberately does NOT consume: `/{store}/history[/{ver}]`
  (sealed-root recovery), `/blobs/reconcile` (§12/§13), `/{store}/usage`, chunked
  upload (`/upload/init|part|complete|abort`), public `/shares`, and the gallery
  ML/geo endpoints (`/analyze`,`/embed-text`,`/reverse`,`/geocode`). Store GET
  also supports ETag/304, not used (§12). Gallery/files store PUT bodies
  also carry an OPTIONAL `counts` map (`{"photos","albums","people"}` /
  `{"files","fileFolders"}`) feeding the server's anomaly-scan (silent data-loss
  detection) — COMPLETE-or-omitted only (§13 safety rule; never partial).

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
internal/audit/         local JSONL operation audit trail (0600, rotated, no secrets)
internal/conformance/   §17 cross-client fixtures + KATs (release gate)
internal/gallery/        v3 sharded gallery store + upload pipeline            [sharded engine]
internal/files/          v3 sharded files store (same engine shape) + tree/sync + continuous sync service (service.go, watch.go)
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
- `github.com/fsnotify/fsnotify v1.9.0` — **justified**: no stdlib OS file-event API; cross-platform recursive filesystem watch (inotify/kqueue/ReadDirectoryChangesW) for `files sync --service`. Pinned + checksum-verified (GOFLAGS=-mod=readonly in CI).

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
  memory-constrained mobile — do NOT raise above the floor). Host/cgroup memory
  guard runs BEFORE derivation (`vault.checkMemoryFor`, fail-closed against an OOM
  kill on a constrained host); server params safe-band-validated (`validateKDF`).
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
- **Audit trail:** LOCAL-only JSONL at `<config>/audit.log` (0600, size-rotated to
  one `.1` backup). One line per operation: `{ts,event,outcome,target,count,
  duration_ms,detail,error,pid}` — operation METADATA only. NEVER a key/VK/token/
  passphrase or decrypted content (§18/§29); the `audit.Event` struct has no field
  that could carry one, and command errors are logged as a generic "command
  failed" (details go to stderr, not the trail). Never shipped (§7). Managed with
  `audit show|path|purge`. Uniform command-level entry via `cmd.Execute`
  (ExecuteC) + explicit domain events (auth.login/logout, gallery/files.degraded).
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

- Blob crypto is chunked (4 MiB) and streamed; sharded reads windowed; cold loads
  batch-fetched (`/raw-batch`) with a ciphertext shard cache. Memory target
  O(worker pool × 4 MiB) at 18k/100k. Gallery AND files upload worker pools bound
  by `--jobs` (default 4). No crypto/network at init. A formal constrained-cgroup
  benchmark is a CI-infra item (§12).

## 11. Security register  [LIVING]

| Item | Owner | Rationale | Compensating control | Review by |
|---|---|---|---|---|
| Classical passkey signatures (platform) | platform | WebAuthn RP ecosystem lacks PQ COSE; auth not HNDL-confidentiality | private JWK sealed under VK (PQ-safe at rest) | 2026-10-01 |
| Classical TLS beyond the ZK boundary | maintainer | only ciphertext transits | ZK payload + cert pinning (certpin) | 2026-10-01 |
| Plaintext token 0600-file fallback when no OS keyring | maintainer | headless/SSH hosts have no Secret Service | 0600 in 0700 dir; `auth status` reports backend; logout shreds | 2026-10-01 |

No entry is past review. An expired entry blocks new work.

## 12. Open items  [LIVING]

None are correctness/interop defects — conformance is green and the shared
contract is fully met. What remains is CI-infra, org-policy, or a deliberate
capability-floor scope decision:

- **CI-infra (not finishable from the repo tree):** a memory-ceiling INTEGRATION
  test under a real constrained cgroup at 18k. The guard's parsers +
  `checkMemoryFor` logic are unit-tested (`internal/vault`); a live-cgroup run
  needs a Linux container CI job.
- **Org/branch-protection policy (not in the repo tree):** signed-commits/tags
  enforcement + a two-person-review gate on the crypto/store/CI paths (§2/§20).
  SBOM diff + reproducible-build verify ARE in CI (`make sbom-verify`,
  `make repro-verify`).
- **Deliberately NOT built (documented decisions, not gaps):**
  - Full-live-set `/blobs/reconcile` — data-loss-critical; escalate before adding
    (§13). The CLI reclaims nothing eagerly; the server's grace-gated reconcile
    handles orphans.
  - On-device derivation (local JPEG/PNG thumb, exiftool EXIF) — the CLI floor
    writes partial records for GUI backfill (spec-optional §8.1).
  - ETag/304 on the tiny root GET, and a DECRYPTED-plaintext cache — the latter
    would keep plaintext at rest (§7); the ciphertext shard cache already captures
    the network win with no plaintext at rest. Marginal; not built.
  - Statistical timing-distribution test for the failure floor — flaky; the
    deterministic floor + uniform-error behaviour is tested (§28).
  - Recursive nested manifest deep-merge on 409 (web store-merge-safety-spec
    `376575aa`: deep-merge a record both sides changed, field-by-field) — NOT
    built (2026-08-01). It has no CLI surface: the files store's only update patch
    (`UpdateFile`, `upload.go`) carries flat top-level keys, `patchRecord` already
    merges them onto the reloaded winner (other fields preserved), and the sync
    engine resolves same-file conflicts at reconcile time via
    `--conflict newest|keep-both|skip` BEFORE staging an op. Gallery is
    append-only (new-id records only) so it never edits a shared record. Building
    web-style recursive merge would be speculative dead code on the
    contract-critical sealed-store path. Revisit only if the CLI ever writes
    partial patches to nested record fields.

Everything else the operating manual's Definition of Done calls for is DONE and
recorded in the Changelog (§15) — Argon2 host-guard, uniform decryption failure,
fuzz parsers, no-secret-in-output, constant-time failure floor, TLS 1.3, ciphertext
shard cache, SBOM + reproducible build, parallel uploads, sharded-store data-loss
safety (revert eager delete + `shards[]` guard + degraded read-only), and the local
JSON audit trail.

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
- **Float handling (updated 2026-08-04):** the HASHED path — `canonicaljson.Marshal`,
  used for shard buckets + collection blobs — still REJECTS non-integer numbers,
  so hot/hashed records stay integer-only and byte-stable across clients (the
  guard is intact). NEW: `canonicaljson.CanonicalizeAllowFloat` tolerates floats
  (emits shortest round-trip, matching the web's `String(n)`), and
  `crypto.SealManifest` now uses it. Rationale: a sealed manifest's ciphertext is
  OPAQUE (no cross-client hash), and the `health` single-row module legitimately
  carries decimal measurements (`healthEntries[].v/v2`, profile
  `heightCm`/`weightGoalKg` — openapi `type: number`). Sharded roots are
  float-free so they're unaffected. Conformance/§17 KATs are integer-only → the
  change is additive and left them green. **Contract impact: none to the byte
  contract** (opaque ciphertext only; no hashed-record or shard-hash change).
- **ML-KEM KAT split:** Go stdlib `Encapsulate()` is not seedable and
  `dk.Bytes()` returns the 64-B seed (not the 2400-B expanded key), so the
  deterministic `ct/dk` KAT values are validated JS-side; Go pins `ekSha256`
  (seed→ek, matches @noble exactly) + a live encaps→decaps round-trip.

- **Gallery/files store-PUT `counts` map is COMPLETE-or-omitted, never partial**
  (2026-08-01). The server's `store:anomaly-scan` sums a version's `counts` map
  and compares it against the previous version to flag a silent-data-loss
  regression; a key the map omits reads as a false 0. Files' two slices (file
  records + folders) are always fully known to this client, so its map is always
  complete. Gallery's albums/people are opaque collection blobs this client
  carries but never edits; counting them requires an extra fetch+decrypt per
  save. If that fetch/decrypt fails for a PRESENT collection, the CLI sends NO
  `counts` key at all that round (never `{photos:N, albums:0, people:0}`) — a
  missing map is silently skipped by the scan, but a wrong map would fire a false
  alarm against a concurrent web-client write. The save itself still succeeds;
  only the counts metadata is skipped. Successful collection counts are memoized
  by ref (content-addressed + never edited by this client, so immutable).
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

- 2026-08-04 feat(modules): new module engines — `internal/health` (single-row:
  healthEntries + healthFasts + the singular healthProfile via new
  `manifeststore.RawKey/SetRawKey`; typed views + CRUD + single-active-fast
  invariant), plus earlier `internal/notes` + `internal/passwords` (sharded),
  `internal/bookmarks` (single-row), the generic per-module sharded API, and full
  `Me()` + Devices (openapi §6a). Health needed a float-tolerant seal:
  `canonicaljson.CanonicalizeAllowFloat` + `crypto.SealManifest` now allow decimals
  (health measurements are openapi `type: number`); the HASHED `canonicaljson.Marshal`
  stays STRICT so shard hashes/§17 KATs are unchanged (§13). Contract impact: none
  to the byte contract (opaque single-row ciphertext only). Full suite + conformance green.
- 2026-08-02 feat(gallery): `gallery import --immich` — direct Immich → gallery
  import. New `internal/gallery/immich.go` (Immich REST client: `x-api-key`,
  `search/metadata` paging, `/original` download, Live-Photo pairing via
  `livePhotoVideoId`), `import_ledger.go` (per-server resumable dedup ledger),
  `import.go` (batch-streaming driver: ~one batch staged on disk, bounded
  `--jobs` pool, `--batch` checkpoint save), and a metadata-injection seam
  (`Item.Imported`/`ImportedMeta` → cold meta blob without `/process` egress).
  ML is recompute-only (Immich embeddings are not API-retrievable) via the
  existing `--ml-local`/`--ml`. Contract impact: NONE — no canonical/sealed/
  shard/blob-frame byte change; the meta blob is cold + never hashed. Built via
  multi-agent workflow, then human-integrated; full suite + gallery/cmd
  `-race` green. Design: `docs/superpowers/specs/2026-08-02-gallery-immich-import-design.md`.
- 2026-08-01 docs(claude): re-align §2 to web HEAD `b88ba9d0` after the web
  ZK-rollback (`779978ea`) — audit the new client store-merge-safety contract
  (rebase-merge / `shards[]` / `counts` all met; seq/invoice/vector rules N/A;
  identity-write-once + KEM-revocation N/A, CLI vault is read-only), fix the
  consumed-surface note (`/files/blob` is DELETE, not GET) + list additive
  endpoints the CLI does not consume; record recursive nested deep-merge as a
  deliberate non-implementation (§12, no CLI surface).
- 2026-08-01 feat(store): send per-slice counts on gallery+files PUT for
  anomaly-scan (complete-or-omit). `internal/api` `SaveGalleryStore`/
  `SaveFilesStore` gained a `counts map[string]int` param sent as the body's
  `counts` key only when non-nil. Files' `{"files","fileFolders"}` map is always
  complete (both slices are fully known locally). Gallery's
  `{"photos","albums","people"}` map is complete-or-nil: albums/people are opaque
  collection blobs needing a fetch+decrypt to count, and ANY failure counting a
  present one aborts the whole map to nil rather than sending a false partial
  count (§13 safety rule) — the save itself still succeeds. No change to
  ciphertext/shards/canonical bytes or shard hashing.
- 2026-08-01 feat(files): files sync --service — continuous watch+interval sync client (fsnotify), per-mapping policy in settings.json, unattended sanity-guard (pause vs mass-delete), single-instance lock, graceful SIGINT/SIGTERM shutdown, auth-expiry stop.
- 2026-07-24 `e709de6` feat(audit): local JSONL operation audit trail
  (`internal/audit`, uniform command hook + domain events; 0600, rotated, no
  secrets); `audit show|path|purge`.
- 2026-07-24 `d974d0e` fix(sharded-store): align to web safety fixes — remove
  eager freed-blob delete (data-loss race), `shards[]` PUT integrity guard (422
  missing_shard), 404-tolerant degraded read-only load. Gallery+files.
- 2026-07-22 `f1e27e1` feat: content-addressed ciphertext shard cache
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
