# DECISIONS.md — Locked decisions

Settled with the product owner (Sven). The agent must not change them.

## Product

| ID | Decision |
|----|----------|
| D-01 | **The workflow is the product.** The tool guides users through GTD with wizards (taps and buttons). Capture is free text, and everything after that is guided. No input syntax (no `@context`, no `tickler: 2w`). |
| D-02 | **Minimal distraction.** Capture returns immediately and shows nothing else (no counts, no suggestions). Engage has a focus mode: one action on screen, one capture key. No push notifications. |
| D-03 | **Hierarchy:** Area → Project tree (**unlimited nesting** via `parent`) → Actions + Notes. A project is an outcome and may span areas. Each action carries its own `area`. A project has a home area for display only. |
| D-04 | **Dependencies:** actions may have `blocked_by` (other actions in the same space, any owner). A blocked action is in state `planned` and is never shown in Engage. When its last blocker completes, it becomes `next` automatically (deterministic rule, not AI) and its owner gets an "unblocked" notification. Cycles are rejected. |
| D-05 | **Dates:** `target` (soft goal, the normal case) and `due` (hard deadline, rare, highlighted). Plausibility warnings (child target after parent target, action target before blocker target, slipping targets) are warnings, never blocks. No Gantt chart in v1. |
| D-06 | **Contexts** are a frontmatter-level field only (`context: <key>`, one per action). A canonical built-in key set with localized labels and icons: `errands`, `calls`, `computer`, `home`, `office`, `anywhere`, `agenda:<user-or-person>`. Users may add personal context keys, which are usable only in their personal space. Shared spaces accept canonical keys only. Contexts are displayed as chips without `@`. |
| D-07 | **`@` means a participant** (a user or service user) and only appears in text. It's inserted via a picker and stored as a token. `[[…]]` links are inserted via a picker and stored as ID tokens. Users never see or type IDs. Each item has a stable URL, and pasting such a URL into a text field converts it to a link token. |
| D-08 | **Templates:** a space may define templates (structured key/value fields plus a body skeleton). They're generic. Trading ("edge hypothesis", "trade setup") is the first user, but the core knows nothing about trading. |
| D-09 | **i18n:** UI languages `de` and `en`, chosen per user (default from Accept-Language). Embedded JSON catalogs (`web/ui/i18n/{de,en}.json`) used by the client, with dates formatted via `Intl` (weeks start on Monday for `de`). The server uses the same catalogs for the text it generates. *(Changed 2026-11-19.)* |

## Topology and collaboration

| ID | Decision |
|----|----------|
| D-10 | **One server per trust domain**, for example work on company infrastructure (a GTT server, potentially `edlrdev3` or `prg-nis-gerrit1`), home on shada (Sven's and Grit's family things). Same binary. **Multi-user per server.** There is no sharing between servers in v1. |
| D-11 | **Spaces:** every user has one personal space. Shared spaces have members. **Membership is the only permission** (role: member, read and write). Users have a server-wide `admin` flag for user/space management only, and admin does not grant content access. |
| D-12 | **Work layout:** one shared space per product, for example `NIS` with areas Nora, Atlas, Collectors, Data curation, Automation. Function blocks are projects. **Home layout:** personal spaces for Sven, Grit, the kids, plus shared spaces like "Sven & Grit", "Family", "Trading" (Sven plus the service user nuro). |
| D-13 | **Collaboration happens on items, not in a chat.** Every item has a comment thread. `@user` in a comment notifies that user ("From others" section of their inbox). Delegation sets `owner`. Waiting For is a query. Unassigned actions in shared spaces show up for every member with a matching context. |
| D-14 | **Machines are participants.** Service users (built-in `@ai`, external ones like `@nuro`) authenticate with scoped tokens. `@ai` in a thread runs the server's AI on that item's context. Its proposals are chips a human must tap. |
| D-15 | **MCP:** each server exposes an MCP endpoint. Token scopes: `capture` (write to own inbox only, read nothing), `read`, `propose` (read plus comments/proposals), `append` (capture plus comment and attach on existing items). Scopes are restricted to listed spaces. External/cloud AIs get `capture` only. |
| D-16 | **Client and multiple servers:** each server serves its own PWA (separate install per server). A later milestone (M9) adds "linked servers" to the client: account switcher, capture destination switch, combined Engage. **Capture destination is chosen by rules (last used per device, device default), never by similarity**, because that would leak text across servers. |

## Platform

| ID | Decision |
|----|----------|
| D-17 | **Single Go binary.** `gtd serve` is the daemon. Every other subcommand is a CLI that talks to the daemon, or opens the DB directly if no daemon holds the lock. Repo: `github.com/fiber/gtd`. |
| D-18 | **Storage: Pebble v2** (`github.com/cockroachdb/pebble/v2`), one DB per server, keys prefixed by space. Every mutation is written together with an op record in one batch, and the op log is the history, the audit trail, the undo source and the offline replay format. In-memory index for queries. |
| D-19 | **Attachments** live in a content-addressed blob directory on the filesystem (sha256, immutable), not in Pebble. Images are embedded inline in notes, PDFs and other binaries attached. `pdftotext` (if present) feeds search and embeddings. |
| D-20 | **Export and snapshots:** `gtd export` writes lossless Markdown plus frontmatter, attachments, comments and op log (works with or without the daemon, or from a snapshot). `gtd snapshot` creates a Pebble checkpoint plus a blob manifest, with retention, `verify` and `restore`. |
| D-21 | **Auth:** web uses password plus long-lived session cookie. API and MCP use bearer tokens with scopes. **CLI and admin use SSH key challenge-response** (keys registered per user, signing via ssh-agent) over the local Unix socket, or remotely over HTTPS. |
| D-22 | **Web UI:** Lit web components, buildless (vendored Lit ES module bundle, import map, plain JS modules, one CSS file, hand-written service worker, all embedded via `embed.FS`; no npm, bundler, TypeScript, other framework or state-management library), installed as a PWA. The client is a pure renderer: Markdown is rendered server-side (`body_html`), wizards are Go step machines behind `/api/v1/wizard/…`, there is no GTD logic in JS, and routing is client-side on a server-delivered app shell. *(Changed 2026-11-19; was `html/template` + htmx.)* Android is first-class (share target for text, URLs, files, camera capture). iOS via Safari install plus an Apple Shortcut. No native apps. |
| D-23 | **Offline: level 2** (OFFLINE.md). A cached snapshot of pinned spaces in IndexedDB, with offline mutations as ops replayed on reconnect. Conflicts go to a merge view. |
| D-24 | **Embeddings:** `embeddinggemma`. Home uses `github.com/fiber/ai` (see its embeddinggemma example; the agent determines whether that's in-process or HTTP). Work uses the existing **norad** embedding server. One `Embedder` interface. Exact brute-force similarity with in-house SIMD kernels (from `github.com/fiber/ai` or vektor, whichever provides SGEMM/dot). No ANN, no vector DB. |
| D-25 | **AI generation is configured per server and per user**, optional at runtime, structured JSON output validated against schemas. A server lists the backends it offers (the work server: only the local Gemma the workplace provides; the home server: a local model and/or a cloud provider, Gemini or Anthropic, as nuro does). Each user has a setting for which of them their requests use, or none. A job runs on the backend of the user who caused it. A server sends content only to its own configured backends, and a work server never offers a cloud provider. *(Changed 2026-11-19, twice; was "local only, no cloud providers".)* |
| D-26 | **Obsidian is abandoned.** No compatibility requirement. A one-time importer migrates the existing vault (thousands of notes) into an "unsorted" pool, which the sweep then works through. Titles may contain any characters. |

## Open questions (owner to answer; agent: ask, don't guess)

- **Q-1** The generation backend on each server and its API: at work, what serves the local Gemma (fiber/ai? norad? something else); at home, which provider and model (D-25). Needed before M7.
- **Q-2** The norad embedding server's API (endpoint, request/response, batch limit, max input, model and dim). Needed before M4 for the work server. Check whether it matches fiber/ai's protocol.
- **Q-3** Which package provides the SIMD SGEMM/dot kernels: `github.com/fiber/ai`, vektor (import path?), or both. Needed before M4.
- **Q-4** nuro side of the integration: which events nuro will post (backtest finished, pipeline failure, …). Needed before M8. gtd only provides the generic `append` mechanism.
- **Q-5** Sven's personal extra contexts and review weekday (config, any time).
- **Q-6** (answered 2026-11-19: generation is per server and per user, see D-25.) Still open
  inside it: in a shared space, a job started by one member sends the other members' text to
  that member's backend. Accept that, or let a shared space restrict the backends? Before M7.
- **Q-7** Proposed change to D-23 (offline), raised by Sven 2026-11-19: "focus offline on capture
  mostly, plan when you're at your desk". Agent proposal: offline means capture (text, photos,
  files), reading the cached lists, and marking actions done or deferred. Offline note editing,
  three-way body merge and the conflict resolver are dropped. This also removes the conflict
  between OFFLINE §3 and "no GTD logic in JS". Not applied yet: confirm before M3, then
  OFFLINE.md and M3 are rewritten.

## Owner requests (2026-11-19)

Stated by Sven during M0. The wording after "Agent reading" is the agent's interpretation.
R-1 and R-2 are built in M1 and described in SPEC §5.5. R-3 waits for M7.

- **R-1 Project thread quick-add.** "I want to easily add a note to a project thread like
  'call Brent about Equinix API'", to support parallel conversations with people and AIs on several
  subjects. Agent reading: a one-line box on every project that posts to its thread, and a
  "make this an action" button on each thread entry. The storage side exists since M0 (`comment` op
  on any item). Planned for M1.
- **R-2 Call notes.** "In a call taking notes, I need to easily review the notes later and plan and
  create action items in various projects." Agent reading: a "New note" entry point, and a
  "Process note" wizard that walks the note's open `- [ ]` lines one per screen (project, wording,
  who, when), creates actions linked back to the note, and replaces the line with a link to the
  action. Planned for M1.
- **R-3 AI on notes.** "You said you'd do X, this needs an action item." Agent reading: the optional
  AI pass of `todo_detect` (AI_CONTRACT §3.5) feeds extra candidates into the Process-note wizard.
  M7, suggestions only (invariant 2).

## Agent decisions

(One line each: date — decision — rationale. "Owner" marks answers Sven gave to questions raised in the M0 plan.)

### M0

- 2026-11-19 — Owner: the export manifest's `exported_at` is the timestamp of the newest exported op (the server's creation time if there is none) — a wall-clock time would break the deterministic export.
- 2026-11-19 — Owner: the "From others" inbox is restored. Export adds `users/<user>/notifications.jsonl`. Per-user state (`u/<user>/st/`) does not exist before M1 and is not exported yet.
- 2026-11-19 — Owner: full export, snapshot and restore need admin; `export --space` works for members of those spaces and yields a partial export (only the members' user records, no notifications) that `import --restore` refuses — a backup is an operator task, D-11 still keeps admins out of Views.
- 2026-11-19 — Owner: no SSH challenge in direct-DB mode; the acting user comes from `-user`, `$GTD_USER` or `$USER` — whoever can open the files can read them anyway.
- 2026-11-19 — Owner: captures have the states `inbox` and `trashed`; a capture becomes an action, project or note through a `transition` op that changes `kind` and `state`. No other kind change exists.
- 2026-11-19 — Owner: snapshot scheduling is in M0: `snapshots.schedule` (`hourly` or `daily`) and `snapshots.keep` in `config.yaml`; the daemon checks once a minute and prunes after each snapshot.
- 2026-11-19 — Owner: the `move` op is left out until M6 — its consequences (cross-space blockers, where the history lives) are collaboration questions.
- 2026-11-19 — Owner: SPEC §6.2 is applied literally: `next`, `waiting` and `someday` actions with an open blocker become `planned`, and come back as `next` with an `unblocked` notification — an unblocked idea deserves a fresh look.
- 2026-11-19 — Owner: a project can be completed while it has open actions; nothing cascades — the spec defines no cascade.
- 2026-11-19 — Transition tables as in `core/transitions.go`: open action states interchange; any open state → done/dropped/trashed; those reopen to `next`; `planned` is never a user target and can only leave to done/dropped/trashed; projects active↔someday and finish/reopen alike; notes unsorted/filed/archived interchange, trash restores to `unsorted`; areas active↔retired — SPEC §6.1 asks for tables but gives none.
- 2026-11-19 — Every state change made by the dependency rule is its own `system` op, including the correction right after a user op (create as `next` with an open blocker → system op to `planned`) — then the op log alone reproduces the state (`core.Replay`), which the crash test and the consistency check rely on.
- 2026-11-19 — The owner of an unblocked action is always notified, also when they completed the blocker themselves; unassigned actions notify every member — D-04 says "its owner gets a notification" without exception.
- 2026-11-19 — `blocker_dropped` goes to dependents that are still open; trashing a blocker unblocks without that warning — §6.2 names the warning only for dropping.
- 2026-11-19 — Reopening a blocker sends only open dependents (`next`, `waiting`, `someday`) back to `planned`; done, dropped and trashed ones stay.
- 2026-11-19 — M0 creates only the `unblocked` and `blocker_dropped` notifications; `mention`, `delegated` and `handed_back` come with M6. Mentions are already validated against membership.
- 2026-11-19 — Users and spaces are not ops (invariant 3 names item and comment state, and the op log is per space). They are restored from `users.json` and `space.yaml`.
- 2026-11-19 — User and space IDs match `[a-z0-9][a-z0-9-]*` (max 64) and share one namespace; a personal space has its user's ID; `system` and `ai` are reserved — keeps `/` free as key separator and IDs usable as directory names.
- 2026-11-19 — Liveness (§6.3): "open blockers owned by someone else" means someone other than the planned action's owner; "a deferred action" counts when the action is `planned` (next and waiting are alive anyway, someday is not a commitment); finished descendant projects are ignored.
- 2026-11-19 — Comments do not change the item record or its rev; `attach` does. A deleted comment keeps id, item, author and time, nothing else. Attaching the same blob twice to one item is rejected.
- 2026-11-19 — Conflict check in M0: an op applies if `base_rev` equals the item's rev, or if every field it changes still has the value given as `old`. The logged `old` is always the actual previous value. Body merge and "terminal wins" (OFFLINE §3) wait for M3.
- 2026-11-19 — `parent` cycles are rejected like blocker cycles; `area`, `project`, `parent` and `blocked_by` must reference items of the right kind in the same space; `context` only on actions; `agenda:<x>` needs x to be a member or in `people`.
- 2026-11-19 — `owner` and `delegated_by` are allowed on every kind (IMPORT §3 sets `owner` on notes) and must be members. Actions in a personal space always get the space's user as owner.
- 2026-11-19 — A patch cannot touch `id`, `space`, `kind`, `state`, `created`, `original`, `source`, `ai_generated`, `attachments` or the rev/updated bookkeeping.
- 2026-11-19 — Export item files: frontmatter keys in SPEC §2 order, then the body verbatim after the closing `---` (not as a `body:` key) so it round-trips byte for byte; timestamps and dates are quoted strings; any frontmatter string that is not plain single-line text is double-quoted — yaml.v3 block scalars do not round-trip leading blank lines, `---` or CR (covered by a table test of awkward strings).
- 2026-11-19 — `export` replaces `manifest.json`, `users.json`, `users/`, `spaces/` and `blobs/` in the target directory and leaves everything else (e.g. `.git`) alone — otherwise stale files of deleted items would survive in a nightly git export.
- 2026-11-19 — Restored users have no password hash (and later no tokens); SSH public keys are exported so that someone can log in after a restore. The restored server keeps the exported server ID.
- 2026-11-19 — `import -restore` and `restore` work on the data directory directly and need a stopped daemon (restore) or an empty data directory (import).
- 2026-11-19 — A database is created only by `user add -admin` and `import -restore`; the first user must be an admin; the data directory comes from `-data` or `$GTD_DATA` with no default — a typo must not silently create a database.
- 2026-11-19 — The CLI speaks HTTP/JSON to the daemon on the Unix socket, and to the same handlers in-process when it opened the database itself; export is a tar stream unpacked by the CLI in both modes — one code path, so behaviour and output are identical by construction.
- 2026-11-19 — CLI sessions from the SSH challenge live in the daemon's memory (12 h, nonce 60 s, single use). The client sends one signature per agent key (max 16) or only the key named with `-key`. A failed attempt says only "authentication failed".
- 2026-11-19 — User and space management commands need admin, including `key-add` for one's own user — simplest rule; revisit with the M1 settings page.
- 2026-11-19 — The in-memory index is the item map per space plus a lowercase text corpus; queries are filtered scans. No per-field maps (by state, owner, context, …) yet — measured on 50k items: filtered scans 0.7 ms, grep 4.5 ms, load 0.24 s, all inside the STORAGE §4 targets. Add maps when a milestone needs one.
- 2026-11-19 — `grep` matches items containing every query word (case-insensitive substring) in title, body or field values. PDF text and semantic hits are M4.
- 2026-11-19 — A capture's title is its first line; `original` holds the full text; `body` is set only for multi-line captures.
- 2026-11-19 — Snapshot names are `<UTC time with ms>[-label]`; retention keeps the newest snapshot of each of the last N hours, days and ISO weeks that have one and treats labelled snapshots like any other; `restore` verifies the snapshot first and moves the old database to `db.replaced-<time>`.
- 2026-11-19 — The attachment limit is a constant 25 MB in M0; `limits.attachment_mb` is read from config when the web upload arrives in M1.
- 2026-11-19 — `ops tail` prints the last N ops (default 20, own personal space) and does not follow.
- 2026-11-19 — staticcheck is run with `go run honnef.co/go/tools/cmd/staticcheck@latest`; it is not a module dependency.

### M1

- 2026-11-19 — Owner: Markdown is rendered by goldmark (full CommonMark plus GFM), in safe mode, with the gtd tokens as an inline extension. External images become links; external links open in a new tab without referrer; line breaks are kept. It replaced a hand-written subset renderer the same day.
- 2026-11-19 — Clarify step 2 ("which space?") and the "member" answer of step 7 are left out until M6: both need the move op and delegation. Clarify works in the personal space, where captures live.
- 2026-11-19 — What a capture becomes in Clarify: the note, the someday action, or the action. With a new project it becomes the project, and the next action is a new item with `derived_from` — `original` and the history stay with the thing the capture described.
- 2026-11-19 — Someday in Clarify makes an action in `someday`. A project done in two minutes loops back to "next action, or is it complete?" — a project needs a next action or an end.
- 2026-11-19 — Wizard sessions live in the daemon's memory (dropped after a day, or on restart). Ops are applied by the server on the last step, in one batch — leaving midway changes nothing and the capture stays at the head of the inbox, as SPEC §5.2 says. Review progress (M2) will be stored per user instead.
- 2026-11-19 — The body editor is a textarea that shows tokens as labels (`[[Title]]`, `![[name.png]]`). The server sends the label form and the label→token map; the client substitutes on save. Two items with the same title get `(2)` — D-07 says users never see IDs, and a textarea cannot show chips.
- 2026-11-19 — An uploaded file is attached to the item first (`attach` op), then its token is inserted — a blob is readable only through an item of a space the reader is a member of.
- 2026-11-19 — Attachments are served with `Content-Security-Policy: sandbox`, `nosniff`, and as downloads unless they are images or PDFs. Thumbnails are not generated yet: images are shown at original size, scaled by CSS.
- 2026-11-19 — Owner: dragging in the plan view: an action A dropped on action B depends on B (`blocked_by`); a project dropped on a project becomes its child. HTML5 drag and drop does not work on touch screens, so the item page has the same settings as selects ("waits for", "part of"), and the board's moves are the item page's state buttons.
- 2026-11-19 — The board shows actions and inbox captures. A deferred item appears only in "Deferred". Dropping a card there asks for a date.
- 2026-11-19 — Engage merges all of the user's spaces into one list in one order. "Not now" hides an action until the page is left. The answers to the three questions are kept in localStorage (per device).
- 2026-11-19 — Templates are edited by any member of the space (space records are not ops). A template's body skeleton is not applied yet when a template is chosen.
- 2026-11-19 — Web sessions: 32 random bytes as cookie value and `sess/` key, 90 days sliding with at most one write a day; five failed logins lock that user name for a minute; the cookie is `Secure` when `server.base_url` is https. Requests that a browser marks as cross-site (`Sec-Fetch-Site`) do not count as logged in.
- 2026-11-19 — Admin routes refuse web sessions (D-21). `gtd user passwd <id>` sets the web password over the SSH-authenticated socket or by direct access.
- 2026-11-19 — The daemon serves HTTP on `server.listen` (default `127.0.0.1:8640`, `off` disables it). UI files are served gzipped with content-hash ETags; the shell preloads all modules; the CSP allows scripts from the origin plus the hash of the import map.
- 2026-11-19 — Components use the light DOM so that the one stylesheet applies. `store.js` is the only module that calls `fetch`.
- 2026-11-19 — The vendored Lit bundle is `lit-core.min.js` 3.2.1 from the lit/dist release (BSD-3-Clause), 15 kB.
- 2026-11-19 — The user's "today" is the server's local date.
