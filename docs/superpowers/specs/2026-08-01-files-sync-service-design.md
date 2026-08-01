# Files Sync Service (`files sync --service`) — Design

Date: 2026-08-01
Status: Approved (brainstorm), pending implementation plan
Branch: `feat/files-sync-service`

## Context

`ledgerline-cli` already has a one-shot bidirectional `files sync` command: it
maps remote store folders to local directories (`--map remote:local`), runs one
pass per mapping through `files.NewSyncer(...).Run`, tracks last-synced state in a
per-mapping state DB in the config dir, and reports an idle/syncing heartbeat to
the server. Mappings and policy defaults can be persisted in `settings.json`.

The user wants to turn this into a **continuous sync client**: a long-running
`--service` mode that reads the persisted mappings, watches for changes, and keeps
local folders and the encrypted files store in sync indefinitely ("infinite
syncs") without re-running the command by hand. Example the user gave:

```
files sync --map "Aleph Alpha":/Users/malte.kiefer/Backup/Aleph\ Alpha --service
```

The mapping must be cleanly persisted so the service picks it up on every start.

The goal: a robust, unattended, zero-knowledge-preserving sync daemon that reuses
the existing sync engine and does not weaken the crypto/threat model (§3/§6/§7 of
`CLAUDE.md`).

This design is **independent** of the openapi contract-drift items (counts /
deep-merge / doc realign) discussed in the same session; it introduces **no wire
contract change** (§2). Those are tracked separately.

## Decisions (from brainstorm)

1. **Runtime model:** foreground supervisor. The command prompts for the
   passphrase once, derives the VK, holds it in process memory only, and runs a
   foreground loop. VK never touches disk (§6). Boot/restart persistence is the
   user's responsibility (tmux/screen/`&`); optional systemd/launchd unit-file
   generation is **out of scope for v1** (future enhancement, noted below).
2. **Trigger model:** filesystem-watch + interval. `fsnotify` gives sub-second
   local change detection; a separate interval ticker polls for server-side
   changes (the server has no push channel).
3. **Config layout:** extend `settings.json` (single source of truth). No second
   config file.
4. **Mapping registration:** `--map … --service` upserts the mapping into
   `settings.json` and then starts the service over all configured mappings;
   `--service` alone runs from existing config.
5. **Unattended safety:** deletions are propagated as in manual sync
   (`delete=both`, `conflict=newest` defaults) **plus** a sanity-guard that pauses
   a mapping (instead of mass-deleting) when its local root vanishes or goes empty
   after previously holding files.

## Config schema (`internal/settings`)

`settings.json` stays 0600 JSON, pretty-printed for hand editing. Backward
compatible: an existing `{"remote","local"}` mapping and a file with no `service`
block both keep working.

```jsonc
{
  "hidden": false,
  "ignore": ["*.tmp", ".DS_Store"],
  "service": {
    "interval": "60s",   // default server-poll cadence
    "debounce": "2s"     // coalesce a burst of local FS events into one pass
  },
  "sync": [
    {
      "remote": "Aleph Alpha",
      "local":  "/Users/malte.kiefer/Backup/Aleph Alpha",
      "conflict": "newest",  // optional per-mapping override
      "delete":   "both",    // optional
      "interval": "30s",     // optional per-mapping poll override
      "hidden":   false,     // optional
      "ignore":   ["*.log"]  // optional, additive to global ignore
    }
  ]
}
```

- `Mapping` gains optional policy fields, all `omitempty`. Empty/absent → fall
  back to the global service defaults, then to the command defaults
  (`conflict=newest`, `delete=both`).
- New `Service` struct: `Interval`, `Debounce` stored as strings, parsed with
  `time.ParseDuration`. Defaults: interval `60s`, debounce `2s`.
- `settings.Upsert(Mapping)` helper: idempotent add/update keyed by the absolute
  local path; used by `--map … --service`.

## Command surface (`cmd/files_sync.go`)

- `files sync --service` — run the continuous service from the configured
  mappings. Error if no mappings configured.
- `files sync --map "R":/L --service` — upsert `{remote:R, local:L}` (with any
  supplied conflict/delete/hidden/ignore as its persisted policy) into
  `settings.json`, then run the service over **all** mappings.
- `files sync --map …` (no `--service`) — unchanged one-shot behaviour.
- `--service` reuses the existing policy flags as service/mapping defaults; no new
  flags required beyond `--service` itself.

## Architecture (`internal/files/service.go`, new)

```
 fsnotify (per mapping, recursive) ─▶ debouncer ─┐
                                                 ▼
 interval ticker (server poll) ───────▶  work queue (per mapping, coalesced)
                                                 │  (sequential; one shared store)
                                                 ▼
                                         sync worker ─▶ files.NewSyncer(...).Run
                                                 │        (existing engine, unchanged)
                                                 ▼
                                 sanity-guard · audit · stdout summary · heartbeat
```

### Supervisor — `Service.Run(ctx) error`
- Unlock VK once (passphrase prompt **before** the loop). Hold in memory only.
- Load the files store once (shared, reused across passes; each pass reloads as
  the syncer already requires).
- Start one watcher per mapping local root; start the interval ticker; process a
  coalescing work queue.
- Passes run **sequentially** — the files store is a single-writer engine, so
  concurrent passes would create artificial version conflicts. Sequential
  execution is simpler and matches the store model.

### Watcher
- `fsnotify` recursive: walk the local root and add a watch per directory; add
  watches for newly created directories, drop them on removal.
- Respect ignore patterns and the hidden-file policy (do not enqueue on ignored
  or hidden paths unless configured).
- Debounce per mapping: collect events for `debounce`, then enqueue a single pass
  for that mapping.
- The event source is abstracted behind a `watcher` interface so tests use a fake
  and only a thin integration test exercises real `fsnotify`.

### Ticker
- Every mapping's `interval` (or the global default), enqueue a pass for that
  mapping. This is the only way server-side changes are detected (no push).

### Work queue
- Per-mapping dedupe/coalesce: if a mapping is already queued or running, fold the
  new trigger in rather than queueing a second pass.

### Pass
- Runs the **existing** `files.NewSyncer(client, store, vk, local, remote, opts).Run(ctx)`.
- Options resolved per mapping: `conflict` (default newest), `delete` (default
  both), `hidden`, `ignore` (global + per-mapping), plus the sanity-guard.
- **Sanity-guard (pre-pass):** if the local root is missing, or is empty while the
  mapping's state DB records previously-synced files, **pause** the mapping — log a
  warning, emit `files.sync_paused`, and skip until the root reappears with
  content. This prevents the unmounted-drive mass-delete footgun.

### Resilience
- Transient errors (network down, HTTP 5xx, timeout) → log, keep the mapping
  active, retry on the next trigger with backoff.
- `ErrDegraded` (a record shard is missing) → pause the mapping (already
  read-only).
- Auth `401` / credential-wiped (`wipedError`) → stop the **whole** service
  cleanly and tell the user to log in again.
- Each pass is panic-safe: a panic in one pass is recovered, logged, and does not
  kill the service.

### Heartbeat
- Reuse `reportSync`: "syncing" during a pass, "idle" between passes and on
  shutdown. Carries only the generic module tag — never folder names or counts
  (existing rule, keeps sealed-manifest structure off the wire).

### Single instance
- Lockfile `<config>/service.lock`. A second `--service` refuses to start. A
  manual `files sync` prints a warning (but proceeds) when the lock is held, since
  both share the per-mapping state DB and running them together risks racy state;
  the warning lets the user decide to stop the service first.

### Shutdown
- On SIGINT/SIGTERM: cancel the context, let the in-flight pass finish, send the
  idle heartbeat, release the lock, exit 0.

## Observability
- stdout: startup banner (mappings + intervals), a one-line summary per pass
  (↑ up / ↓ down / unchanged / conflicts / failed — the existing format), and
  pause/resume warnings.
- Local audit trail (`internal/audit`): `files.service_start`,
  `files.service_stop`, `files.sync_pass` (metadata only — mapping tag, counts,
  duration), `files.sync_paused`. Never secrets/content (§18).

## Dependency
- Add `github.com/fsnotify/fsnotify` (pinned in `go.mod`/`go.sum`,
  checksum-verified). §5 justification: no stdlib OS file-event API exists;
  `fsnotify` is the de-facto Go standard, cross-platform (inotify / kqueue /
  ReadDirectoryChangesW), widely used and maintained. Record in the §5 dependency
  inventory.

## Testing
- Unit: debounce coalescing; work-queue dedupe; sanity-guard (missing/empty root
  vs. populated state DB); config parse (durations + legacy `{remote,local}`);
  `settings.Upsert` idempotency.
- Integration (mock server + temp dirs): local create/modify/delete → watcher →
  pass → store updated; server-side change → next tick pulls it down; SIGTERM
  graceful stop; `401` → service stops with the re-login message.
- A thin integration test exercises the real `fsnotify` adapter; all queue/guard
  logic is tested against the fake watcher.

## Out of scope (v1)
- systemd/launchd unit-file generation (future; foreground supervisor is the v1
  deliverable — the user backgrounds it themselves).
- Any wire-contract change or new server endpoint.
- The openapi contract-drift items (counts / recursive deep-merge / §2 doc
  realign) — tracked separately from this feature.

## Contract impact
None. Reuses existing `/files/store` GET/PUT + upload/raw endpoints and the
existing sync engine. `CLAUDE.md` §5 (dependency inventory) is the only living
section that changes (adding `fsnotify`).
