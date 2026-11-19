# STORAGE.md — Pebble, op log, daemon/CLI, auth, export, snapshots

## 1. Process model
- `gtd serve` is the daemon. It opens the Pebble DB (exclusive lock), serves HTTP
  (behind nginx for TLS) and listens on a Unix socket
  (`<data_dir>/gtd.sock`, mode 0660) for CLI and admin traffic.
- Every other subcommand is a client:
  1. Try to connect to the socket (or `--server https://…` for remote use).
  2. If no daemon is running and the command is local, open the DB directly (taking
     the lock) and run the same code path.
  The behaviour and output are identical either way. Commands that need the daemon
  (`sync-status`, live `snapshot` scheduling) say so clearly when it's not running.
- Subcommands: `serve`, `add`, `grep`, `export`, `import`, `snapshot
  {create|list|prune|verify}`, `restore`, `reindex`, `user {add|key-add|key-rm|passwd|token}`,
  `space {create|add-member|rm-member|templates}`, `ops tail`, `migrate`.

## 2. Pebble (`github.com/cockroachdb/pebble/v2`)
One DB per server: `<data_dir>/db/`. Values are versioned JSON (`{"v":1,…}`), which
is debuggable, and schema evolution is handled by on-read upgrade plus `gtd migrate`.

### 2.1 Keyspace
```
m/schema                         → {"v": N}
m/server                         → server id, name, created
u/<user>                         → user: name, lang, admin, service, password_hash,
                                   ssh_keys[], personal_contexts[], review_weekday,
                                   personal_space
u/<user>/tok/<tokid>             → token: hash, scopes[], spaces[], label, created, last_used
u/<user>/st/<key>                → per-user state (engage prefs, review progress,
                                   last-seen notification seq, device defaults)
u/<user>/n/<seq:be64>            → notification {type, space, item, comment, from, ts, dismissed}
sess/<id>                        → session {user, created, expires, device}
sp/<space>                       → space: name, kind (personal|shared), members[],
                                   people[], templates[], created
sp/<space>/it/<itemid>           → item (SPEC §2)
sp/<space>/cm/<itemid>/<cmtid>   → comment {id, item, author, ts, body, mentions[], attachments[], ai, proposals[]}
sp/<space>/op/<seq:be64>         → op record (§3)
sp/<space>/seq                   → last op seq for the space
opid/<opid>                      → {space, seq} idempotency index (offline replay)
x/vec/<space>/<itemid>           → embedding row (derived; excluded from export)
x/vecid                          → identity of the stored vectors (model + prompt set)
x/ai/<type>/<contenthash>        → cached AI result (derived; excluded from export)
x/pdf/<sha256>                   → extracted PDF text (derived)
```
`be64` = big-endian uint64, so iteration order is the sequence order. ULIDs sort
by time. Cross-space operations (move) are single batches, which is why it's one DB.

### 2.2 Isolation
`store` exposes access only through a `View(user)` that carries the user's
membership set. Every read and write function takes a space ID and checks it against
that set. There's no unscoped accessor outside `store` internals, export and
snapshots. A dedicated test calls every HTTP route, MCP tool and CLI command as a
non-member and expects 404 (not 403, so existence isn't leaked).

## 3. Ops (the only write path)

```go
type Op struct {
    ID      string            // ULID; client-generated for offline/CLI, server-generated otherwise
    Space   string
    Seq     uint64            // assigned by the server on apply
    TS      time.Time
    Actor   string            // user or service user id; "system" for rules/tickler
    Via     string            // web | cli | mcp | offline | service | system | import
    Type    string            // create | patch | transition | comment | attach | move | delete_comment
    Item    string
    BaseRev uint64            // item rev the client based this on (0 for create)
    Changes []FieldChange     // {Field, Old, New}, with Old for conflict detection and undo
    Payload json.RawMessage   // type-specific (comment body, move target, …)
}
```
- `core.Apply(state, op) → (newState, derivedOps, notifications, error)` is pure.
  Derived ops (dependency unblocking, project rules, tickler) are applied in the
  **same batch** and logged as their own ops with `Actor: system`.
- The `store` writes items, comments, op records, notifications, `opid/` and the
  seq counter in one `pebble.Batch` with `Sync: true`.
- A per-space mutex serializes `Apply` and commit for that space. A move op locks both
  spaces in ID order.
- **Undo:** the UI offers undo for the last op of the user (inverse from `Changes.Old`),
  applied as a normal op.
- **History:** the item view shows its op history (who, when, which fields), which
  covers Eric, nuro and `@ai` too.
- Op log retention: forever by default (it's small). `gtd ops compact --before` is a
  non-goal for v1.

## 4. In-memory index
- At startup, scan `sp/*/it/*`, `sp/*/cm/*` (counts only) and build:
  by-space, by kind+state, by project (incl. subtree), parent tree, by area, by
  owner, by context, blocked_by forward and reverse edges, backlinks, a full-text
  token index (or a plain lowercase corpus for brute-force scan, whichever is simpler
  and meets the target).
- Updated incrementally after each committed batch. Readers use an RWMutex.
- Target: 50k items plus 200k comments load in under 3 s. Typical queries in under 5 ms.

## 5. Text merge
Used for body conflicts (OFFLINE.md). Shell out to `git merge-file -p --diff3`
with base/ours/theirs temp files. Exit 0 means clean. Greater than 0 means a conflict:
the result with markers is stored in `conflict` (see OFFLINE.md §4) and never
silently picked.

## 6. Blobs (attachments)
- `<data_dir>/blobs/<aa>/<sha256>`, immutable. Write to temp, fsync, rename. The MIME
  type and name live on the item's `attachments` entry, not on the blob.
- Thumbnails: `<data_dir>/cache/thumbs/<sha256>.jpg` (derived).
- GC: `gtd blobs gc` lists blobs referenced by no item, comment or retained
  snapshot manifest. Dry-run by default.

## 7. Auth (`auth/`)
- **Web:** password (bcrypt) → session in `sess/`, cookie HttpOnly, Secure,
  SameSite=Lax, 90-day sliding. Login rate limit.
- **Tokens:** `Authorization: Bearer gtd_<tokid>_<secret>`, with the secret's
  sha256 stored. Scopes and allowed spaces per token (COLLAB.md §6). Revocable,
  `last_used` tracked.
- **SSH key challenge (CLI and admin):**
  1. The client requests a challenge (socket, or `POST /api/v1/auth/ssh/challenge`
     remotely), naming a user.
  2. The server returns a random nonce bound to the connection or a short TTL.
  3. The client signs `"gtd-auth-v1\n" + server_id + "\n" + nonce` with a key from
     ssh-agent (`x/crypto/ssh/agent`). The user can specify a key or let it try
     all the keys in the agent.
  4. The server verifies against `u/<user>.ssh_keys` and issues a short-lived CLI
     session (socket connection or 12 h token).
  - `admin` commands additionally require `admin: true`. Bootstrapping:
    `gtd user add --admin --ssh-key <pubkey>` works only via direct DB access (no
    daemon running) or by a user who is already admin.
  - Socket file permissions are a second barrier, not the only one.

## 8. Export and import (lossless)
- `gtd export [--space s]… [--from <snapshot>] <dir>` writes:
  ```
  <dir>/manifest.json           server id, schema version, export time, spaces, counts
  <dir>/users.json              users without secrets (no hashes, no tokens)
  <dir>/users/<user>/notifications.jsonl      the "From others" inbox
  <dir>/spaces/<space>/space.yaml
  <dir>/spaces/<space>/items/<id>.md          frontmatter + body (SPEC §2)
  <dir>/spaces/<space>/comments/<itemid>.jsonl
  <dir>/spaces/<space>/ops.jsonl
  <dir>/blobs/<aa>/<sha256>                   referenced blobs only
  ```
  The export time is the timestamp of the newest exported op, not the wall clock.
  It's deterministic (sorted, fixed field order), so a nightly export into a git repo
  gives a clean human-readable history. It works via the daemon (consistent: export
  reads from a Pebble snapshot), directly, or from a checkpoint. A full export,
  like snapshot and restore, requires admin. `--space` works for members of those
  spaces and yields a partial export that `import --restore` refuses.
- `gtd import --restore <dir>` into an empty DB reproduces everything (invariant 5).
  A round-trip golden test is mandatory. Derived data (`x/`) is rebuilt afterwards.

## 9. Snapshots
- `gtd snapshot create [--label]`: Pebble `Checkpoint` into
  `<data_dir>/snapshots/<ts>[-label]/db` (hard links, cheap and consistent), plus
  `manifest.json` (server id, schema, op seq per space, list of referenced blob
  hashes and sizes).
- The daemon schedules snapshots per config. Retention, e.g.
  `{hourly: 24, daily: 14, weekly: 8}`; `prune` applies it.
- `verify` opens the checkpoint read-only, checks the schema, iterates all keys and
  checks every manifest blob exists with a matching hash.
- `restore <snapshot>` requires a stopped daemon, moves the current DB aside
  (it's kept, never deleted) and copies the checkpoint in.
- Off-site backup = rsync/restic of `snapshots/` plus `blobs/`. Blobs are immutable, so
  backups are incremental by nature. This is documented in the README, not
  implemented as a feature.

## 10. Config (`<data_dir>/config.yaml`, server-level only)
```yaml
server: { name: "home", base_url: "https://gtd.example.org", listen: "127.0.0.1:8640" }
embed:  { provider: internal | gtt-nora | ollama, model_dir: "/home/sven/models/embeddinggemma-300m",
          url: "…", model: embeddinggemma }   # model_dir for internal, url for the other two
gen:    { provider: "<Q-1>", url: "…", model: "…", timeout: 120s }   # optional
snapshots: { schedule: hourly, keep: { hourly: 24, daily: 14, weekly: 8 } }
limits: { attachment_mb: 25 }
similarity: { dup_threshold: 0.92, suggest_threshold: 0.55, search_threshold: 0.30 }
```
Users, spaces, tokens and templates live in Pebble, managed via CLI or the admin UI.
