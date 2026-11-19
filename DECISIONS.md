# DECISIONS.md — Locked decisions

Settled with the product owner (Sven). The agent must not change them.

## Product

| ID | Decision |
|----|----------|
| D-01 | **The workflow is the product.** The tool guides users through GTD with wizards (taps and buttons). Capture is free text, and everything after that is guided. No input syntax (no `@context`, no `tickler: 2w`). |
| D-02 | **Minimal distraction.** Capture returns immediately and shows no counts. While the user types, it may quietly list up to four existing items that say the same, each with "Add to this instead" (appends the text to that item's thread); Enter always captures at once. A **privacy mode** switch on the capture screen (per device, stays on until switched off) turns that list off, for when the screen is shared. Engage has a focus mode: one action on screen, one capture key. No push notifications. *(Changed 2026-11-19 by Sven; was "shows nothing else (no counts, no suggestions)".)* |
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
| D-23 | **Offline: capture first** (OFFLINE.md, rewritten 2026-11-19, Q-7). Offline the phone captures, shows the lists as last seen, ticks actions off and adds actions to a list; all of it is queued and sent on reconnect. No offline editing or planning, no merge, no conflict view. (Was "level 2": a synced copy of pinned spaces with a merge view.) |
| D-24 | **Embeddings:** `embeddinggemma` (768-dim unit vectors), behind one `Embedder` interface with **three adapters**: `internal` (in-process through `github.com/fiber/ai`, model files in a local directory: `/Users/sven/models/embeddinggemma-300m` on the Mac, under `/home/sven/models/` on shada), `gtt-nora` (the NIS embedding service described in Q-2; **deferred** by Sven on 2026-11-19 because of its `go.gtt.net` dependencies, so not built in M4), and `ollama` (HTTP, `/api/embed`). Exact brute-force similarity with `fiber/ai`'s `cluster` package. No ANN, no vector DB. *(Changed 2026-11-19; was two backends, fiber/ai and norad.)* |
| D-25 | **AI generation is configured per server and per user**, optional at runtime, structured JSON output validated against schemas. A server lists the backends it offers (the work server: only the local Gemma the workplace provides; the home server: a local model and/or a cloud provider, Gemini or Anthropic, as nuro does). Each user has a setting for which of them their requests use, or none. A job runs on the backend of the user who caused it. A server sends content only to its own configured backends, and a work server never offers a cloud provider. *(Changed 2026-11-19, twice; was "local only, no cloud providers".)* |
| D-26 | **Obsidian is abandoned.** No compatibility requirement. A one-time importer migrates the existing vault (thousands of notes) into an "unsorted" pool, which the sweep then works through. Titles may contain any characters. |

## Open questions (owner to answer; agent: ask, don't guess)

- **Q-1** (partly answered 2026-11-19: the home server uses Gemini, the work servers the internal Gemma 32B. The owner is "unsure about the value of this AI integration right now": M7 is not started. Still open when it is: which API serves Gemma at work.) The generation backend on each server and its API: at work, what serves the local Gemma (fiber/ai? norad? something else); at home, which provider and model (D-25). Needed before M7.
- **Q-2** (answered 2026-11-19 by Sven's brief "NIS embeddings: what exists and how to use it",
  read from go.gtt.net at 36c524f2.) At work, EmbeddingGemma (~300M, 768-dim unit vectors) runs
  in-process on CPU through `github.com/fiber/ai`; there is no HTTP embedding server. A NIS host
  can share one loaded model through `embedsrv` (`norad embed`, TLV frames over a Unix socket or
  the SSH proxy; limits announced in WELCOME). norad's `embed` assist op (1–16 texts of ≤ 2 KB,
  shared with live chat) is not for bulk work, and an atlas vector index would put gtd's texts
  into atlas: an atlas vector index is not used. See AI_CONTRACT §2.1. Still open for the `gtt-nora` adapter
  (D-24): which of the two NIS interfaces it speaks (`embedsrv`, built for bulk, or norad's assist
  op, which the brief rules out for bulk), and how the code gets the wire format: `go.gtt.net` does
  not resolve from the development machine, so either the repository may import
  `go.gtt.net/aiops/embedsrv` and `go.gtt.net/tlv` and is built where they resolve, or the adapter
  is written from `wire.go`, which the agent has not seen.
- **Q-3** (answered 2026-11-19 by reading `github.com/fiber/ai` v0.1.2.) The SIMD kernels are
  internal to fiber/ai; its public `cluster` package is the way in: `Similarities(queries, docs)`
  (SGEMM), `Cosine`, `Nearest`, `KMeans`, `Normalize`. Embedding is `models/gemma`: `Load(dir)`,
  `Embed(texts, gemma.Prompt("document"|"query"))`, `Dim()`, `Close()`.
- **Q-4** nuro side of the integration: which events nuro will post (backtest finished, pipeline failure, …). Needed before M8. gtd only provides the generic `append` mechanism.
- **Q-5** Sven's personal extra contexts and review weekday (config, any time).
- **Q-6** (answered 2026-11-19: generation is per server and per user, see D-25.) Still open
  inside it: in a shared space, a job started by one member sends the other members' text to
  that member's backend. Accept that, or let a shared space restrict the backends? Before M7.
- **Q-8** (answered yes, 2026-11-19: repeating actions are built, SPEC §6.6.)
- **Q-7** (answered 2026-11-19: yes, capture first. "I do want to take offline notes (in IN) and probably be able to retrieve my shopping list (and also add to it) on the go. But I don't need all the planning features while offline." D-23 and OFFLINE.md are rewritten.)

- **Q-9** Passing notes between servers (2026-11-19): "I would want to make business notes on my personal account and vice versa (get IN out of my head in under 5 seconds)." Agent proposal: capture always goes to the inbox of the server you have open; a capture that belongs to the other server gets one more way out, "Send to <other server>", which posts it (text and attachments) to that server's inbox with a capture-only token and trashes it here. Open: may the work server send text to the home server and the reverse at all, and can the two reach each other (or must the browser do the sending)? This is a small part of M8 (capture tokens) and M9 (linked servers).
- **Q-10** Email (2026-11-19, "wondering if there should be some email integration"): Agent proposal, smallest first: (a) mail in: a message forwarded to a capture address becomes a capture with the mail as its text and its attachments attached, done as `gtd capture-mail` reading one message on stdin, so the mail server pipes into it and gtd runs no mail service of its own; (b) later, mail out for "From others" notifications. Not: reading a mailbox, or sending mail from actions.

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

- **R-4 Notes as an Obsidian replacement.** (2026-11-19) "Since we are replacing Obsidian, the
  note taking aspect may be coming up a little short in the current design", and: "I am being
  bombarded with random ideas during my day, most of them fairly immature. How can I transform
  them into a cohesive picture?" Agent reading of what is missing after M1: no list of notes at
  all (unsorted notes are reachable only through search), no way to start a note outside a
  project, no view of Someday items or ideas as such, a bare textarea as editor (no side-by-side
  preview, no autosave, no image paste), and nothing that relates a new idea to older ones before
  M4. Built on 2026-11-19 after M4, on Sven's word that notes and the tutorial matter most now
  (MILESTONES "Notes", SPEC §5.5).

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
- 2026-11-19 — Owner asked for a more modern look. The stylesheet was redone (still one file, custom properties, light and dark by system setting): floating bottom bar on phones and a sidebar on wide screens, inline SVG icons drawn in `widgets.js`, tinted chips, a state mark in front of every row, system fonts only — no font files, icon fonts or CSS framework were added.
- 2026-11-19 — Owner: in Engage, selecting places shows only the actions with one of those contexts, plus the ones marked `anywhere`. Actions without a context show only when no place is selected. (The first version let context-less actions match every place.)
- 2026-11-19 — Owner asked for a denser, more colourful look in the visual language of a reference he supplied (black bar, white page, heavy headings, violet accent, numbered circles, pill labels, dashed outlines), a light mode, and a full-screen mode. Done: a top bar replaces the sidebar on wide screens; light is the default theme with Light / Dark / System per device (`theme.js`, localStorage); full screen is the browser's own full screen with the app's bar kept (`f` toggles, Esc leaves; the first version also hid the bar, which the owner did not want); number keys answer wizard choices; the item page has two columns on wide screens.
- 2026-11-19 — Owner: pressing Trash leaves the item's page (back to where the user came from) with a "Moved to trash · Undo" toast; a trashed item's page says it is in the trash and offers "Restore"; done and dropped items offer "Reopen". The details panel of an item fits a 13-inch screen without scrolling: one line per field, date shortcuts only while the date field has focus.
- 2026-11-19 — Measured on Sven's Mac with `/Users/sven/models/embeddinggemma-300m`: the `internal` adapter loads in 1.6 s and embeds 6 short texts in 91 ms. Ollama's `embeddinggemma` (BF16) returns the same vectors (cosine 1.0000 per text) once the caller adds the task prompts; without the document prompt the same text scores 0.87 against itself, so Ollama does not add prompts and the adapter must. A German and an English phrasing of one action score 0.38 against an English query, unrelated actions 0.14 to 0.18. Vectors of different adapters are still never mixed: the adapter and its model digest are part of the identity.

### M4

- 2026-11-19 — Owner: M4 is built before M2, with the `internal` and `ollama` adapters. `gtt-nora` is deferred (D-24).
- 2026-11-19 — The `Embedder` interface is `Embed(ctx, texts, task)`, `Identity()` and `Close()`: the task (document or query) is needed for EmbeddingGemma's prompts, and the identity replaces `Model()` and `Dim()` from the first draft of AI_CONTRACT.
- 2026-11-19 — Identity of the `internal` adapter: a hash of `config.json`, the size of `model.safetensors` and its first megabyte, plus the prompt set. Of `ollama`: model name, quantization and the digest the server reports. A changed identity drops all vectors and rebuilds.
- 2026-11-19 — The embedding service lives in the daemon only. A CLI that opened the database itself has no similarity and `gtd reindex` says that it needs the daemon.
- 2026-11-19 — One background worker embeds up to 8 items per call (the model runs one forward pass at a time anyway). Jobs are keyed by item; an item changed again while queued is embedded once, from its latest text. Unchanged text (same hash) is not embedded again, also across restarts. Backoff from 1 s to 1 min while the embedder fails.
- 2026-11-19 — An item's text is capped at 16 chunks (24,000 characters); the rest does not reach the vector. Trashed items have no vector.
- 2026-11-19 — A space counts as "building" from start or reindex until its queue is empty the first time. During that time it gives no similarity answers; a new item later does not put its space back into "building".
- 2026-11-19 — Similarity only suggests (invariant 2): suggested projects appear as chips above the choice and are never preselected; "Duplicate of…" is an extra button on Clarify's first step and is offered only for items in the capture's own space, because a batch of ops stays in one space. It appends the capture's text and files as a thread entry on that item and trashes the capture.
- 2026-11-19 — Three thresholds, all placeholders until the M5 calibration on the real vault: `dup_threshold` 0.92, `suggest_threshold` 0.55 (item to item: Clarify's similar list, "might belong here"), and a new `search_threshold` 0.30 for query to item. Measured on the demo data with the real model: a relevant query scores 0.38 to 0.55 against an item, an unrelated one below 0.2; related items score 0.5 to 0.73 against each other; a reworded duplicate scored 0.89, so 0.92 is probably too strict.
- 2026-11-19 — Search answers `{hits, semantic}`: exact hits first, then hits by meaning marked `similar` with their score. `semantic` is `ok`, `off`, `offline` or `building`; the UI and `gtd grep` say when the answer is a text match only.
- 2026-11-19 — PDF text: `pdftotext -l 50` if installed, at most 200 kB, cached by blob hash under `x/pdf/` (also when empty, so a broken PDF is tried once). It feeds both the text search and the item's vector.
- 2026-11-19 — Vectors are stored as 32 bytes of text hash plus little-endian float32s, written without fsync: they are derived data.
- 2026-11-19 — Search by meaning covers every kind, inbox captures included (the first version left captures out, so "Geschenk für Grit" did not find the capture "need to find a present for Grit"). Clarify's similar list and an item's related list still leave captures out: a capture should be compared with what is already organized.

### Notes

- 2026-11-19 — Owner: the order of work is the agent's; notes and the tutorial first. Order chosen: notes, then M2, then M5 (starting with per-passage embeddings, if the owner agrees), then M3.
- 2026-11-19 — A new note made outside a project starts `unsorted` in the personal space, titled "Notes <date and time>"; the title is the first thing to change.
- 2026-11-19 — Autosave writes a patch op 1.5 s after the last keystroke, so a long writing session leaves many small ops in the log. Accepted: the op log is the history, and it stays small (STORAGE §3). One save runs at a time; the editor keeps its own rev so that typing does not wait for a reload.
- 2026-11-19 — Every patch from the UI now carries the old value of each field, so a change still applies when another op (for example an attachment) raised the item's rev in between.
- 2026-11-19 — An image pasted from the clipboard is named `pasted-<time>.<ext>`: clipboards call every screenshot "image.png".
- 2026-11-19 — The counts on the Notes tabs follow the word and project filters, not the selected tab, so they say what each tab would show.
- 2026-11-19 — The tutorial moved to `web/TUTORIAL.md` so that the binary can embed it; the app renders it under Help. It is English only for now.
- 2026-11-19 — Sven's Obsidian vaults are in `/Users/sven/Documents/Obsidian/`: two with notes (124 and 166, about 153,000 words together) and one empty. Surveyed read-only: median note 0.5 to 2 kB, 13 notes over 24,000 characters (the largest 98 kB and 168 kB), only 33 `[[links]]` in 290 notes, 76 open task lines, 145 images or embeds, and 36 `.base` and 12 `.canvas` files, which the importer cannot convert. IMPORT.md's "thousands of notes" is an overestimate; its rules stand. For M5 this means: per-passage embeddings matter (today's 16-chunk cap would leave most of the two largest notes out of their vectors), link conversion is a small job, and "Process note" has real work waiting.

### M2

- 2026-11-19 — The tickler clears `defer_until` on open items whose date has come, as one system patch op per item, at daemon start and when the local date changes (checked once a minute). Someday items keep their date: they come up in the review's Someday step. A CLI without a daemon does not run the tickler.
- 2026-11-19 — "Resurfaced" and "unblocked" are derived, not stored: the item's last change was by the system within three days, without (resurfaced) or with (unblocked) blockers.
- 2026-11-19 — The review's first step is a gate: it does not continue while a capture is in the inbox ("captures to zero", SPEC §5.4). "From others" is shown but does not block.
- 2026-11-19 — A kept project that is stuck needs a next action typed into the same step; otherwise the answer is rejected. That is how "finishing a stuck kept project is impossible" is enforced.
- 2026-11-19 — Review thresholds: stale = next and unchanged for more than 14 days; waiting = more than 7 days; someday = 10 per review, least recently reviewed first (remembered per user). "Nudge" on someone outside the space creates a "Follow up with X: …" action with context calls; on a member it posts a comment that mentions them. "Resolved" completes the waiting action.
- 2026-11-19 — The dates and areas steps are overviews with links, not one decision per item: the decisions are edits on the items themselves.
- 2026-11-19 — Review progress and "last reviewed" dates are per-user state under `u/<user>/st/`. They are not ops and not exported: after a restore a review starts from the beginning.
- 2026-11-19 — Review ops span spaces: each decision's ops go to the space of the item it is about.
- 2026-11-19 — Owner asked for "a cute little logo": a violet rounded tile with two eyes and a check mark for a smile (`web/ui/logo.svg`), used as favicon, in the bar and on the login page.

### Path prefix

- 2026-11-19 — Owner: the whole web side lives under `/gtd/` (UI routes, `/gtd/ui/`, `/gtd/api/v1/`), "so I can home this on different servers". The prefix is the constant `web.Prefix`, not a setting: the shell carries `<base href="/gtd/">`, every link written by the client or the server is relative to it, the client reads the base from the page, and the session cookie is scoped to `/gtd/`. Outside the prefix the HTTP listener answers only a redirect from `/`. The CLI's Unix socket serves the bare API, so CLI paths did not change. The CSP's `base-uri` is `'self'` (it was `'none'`, which blocked the base tag).
- 2026-11-19 — A link a user writes in Markdown as `[x](/i/…)` is left as written and now points outside the app. Links belong in `[[…]]` tokens, which follow the prefix.
- 2026-11-19 — Owner said yes to repeating actions (Q-8) and to per-passage embeddings; both are next, then M5.
- 2026-11-19 — Owner: examples use neutral names, no real people, companies or products. Done in the tutorial, the UI texts, the README's commands and all test fixtures including the sample export (users robin, dana and jo, the shared space net, a person called Lee, "Example API"). The names were chosen so that every ordering the tests rely on stays the same. The brief documents (SPEC, COLLAB, DECISIONS and the others) still describe the real deployment and its names: they are the specification, not samples.

### Repeating actions

- 2026-11-19 — Owner: repeating actions are wanted; the non-goal is lifted. `repeat` is a string on actions: `<n>d`, `<n>w` or weekday names. The successor is created by a system op when the action is done, deferred to its date. Intervals count from the day of completion, not from a fixed schedule, and only one instance is open at a time — simplest rule, and it does not punish a skipped day.
- 2026-11-19 — Reopening a completed repeating action leaves its successor in place: there are then two open instances until one is dropped. Not handled specially.
- 2026-11-19 — "Three times a week" is expressed as three weekdays. A count without days ("any three days") is not supported.

### Clarify in fewer steps

- 2026-11-19 — Owner: "What is it?", "Actionable?" and "More than one step?" are one screen. The title and notes fields are followed by Action, Project, Reference, Someday, Trash (and "Duplicate of…"). Enter means Action. Trash does not require a title. The steps `actionable` and `multi` are gone. Alt+number presses a step's nth button, also while typing; my addition, so the first screen needs no mouse.
- 2026-11-19 — Clarify has Cancel (back to the inbox; nothing is changed, the capture stays at the head) and, after the first screen, Start over. Both only leave or restart the wizard, which SPEC §5.2 already allows. There is no Skip: SPEC §5.2 rules it out, and the owner confirmed it on 2026-11-19 (Allen: never put anything back into "in"; "not now" is Someday with a date).

### Passages

- 2026-11-19 — Owner said yes to per-passage embeddings. Long texts are split between paragraphs (a short single line counts as a heading and stays with the next paragraph). Passage vectors are stored in the item's `x/vec` value and indexed in a second matrix per space; only search by meaning uses them (max of item and best passage). The stored identity carries a layout tag (`|passages-1`), so existing vectors are rebuilt once on the first start.
- 2026-11-19 — A search hit does not say which passage matched, and the item page does not jump to it. Not asked for.

### Agent decisions (M5)

- 2026-11-19 — The importer runs in the CLI, where the files are, and writes through the server: `GET/POST /api/v1/import`. `gtd import obsidian [-space id] [-dry-run] <vault>`; flags come before the path (Go's flag package). There is no `--as`: the importing user is the CLI's user (`-user`), as for every other command.
- 2026-11-19 — The start and end markers are ops of a new type `import` (payload: source, phase, note count). They touch no item; replay skips them. The path map and the report are written in the same Pebble batch as the ops they belong to.
- 2026-11-19 — A note's ID is a ULID of its time (frontmatter `created` or `date`, else the file's modification time) and of a hash of space and path, so the same file always gets the same ID. An export does not carry the path map; after a restore a re-import therefore still finds its notes instead of duplicating them.
- 2026-11-19 — Batches are at most 500 notes or about 4 MB.
- 2026-11-19 — Empty files are counted and skipped. Frontmatter that is not YAML stays in the body and is reported. Frontmatter keys named like the importer's own (`filename`, `folder`, `reimported_at`, `unresolved_links`) are kept as `frontmatter_<name>`.
- 2026-11-19 — Beyond IMPORT.md §4: a Markdown link to another note (`[text](other.md)`) becomes `[[id|text]]`; a link to any other file of the vault (`[[board.canvas]]`) becomes an attachment link. Links inside fenced code stay text. A `[[Name]]` never resolves to a non-Markdown file without its extension.
- 2026-11-19 — A changed file updates title, body and `imported` (with `reimported_at`), adds new attachments, and keeps `original` as first imported (invariant 1). A file whose links resolve differently but whose content is unchanged is not updated.
- 2026-11-19 — Excalidraw drawings, kanban boards and other plugin files that end in `.md` are imported like any note. `.base` and `.canvas` files are imported only where a note links to them.
- 2026-11-19 — Sweep: one cluster per wizard session; the clusters are computed again for each (filing changes them), and Skip passes a cluster over for the rest of the visit. The decision applies to the ticked notes; the others stay unsorted. "Someday" in SPEC §5.7 is read as "new theme": a someday project that takes the notes, as on the Ideas tab. "New project" does not ask for a next action here; the project shows as stuck until it has one. No AI labels or AI titles (M7); the per-note "create action" chips are the existing Process note page, one tap away through the note's link. The sweep is offered on the Notes page and in step 2 of the weekly review, for the personal space.
- 2026-11-19 — `cluster.KMeans` from fiber/ai is used as it is (k-means++, fixed seed, 50 iterations). All clusters under the cohesion floor or with one note are shown together as one "loose notes" screen at the end.
- 2026-11-19 — `gtd calibrate` (and `GET /api/v1/ai/calibration`) reports score distributions for one space, numbers only. Run on the two real vaults (268 notes, EmbeddingGemma in-process): nearest neighbour per note 0.51 / 0.71 / 0.99 (5th / 50th / 95th percentile); all pairs 0.16 / 0.34 / 0.57; pairs at or above 0.95: 65, 0.92: 66, 0.90: 71, 0.85: 131. Twelve everyday test sentences: German against its English translation 0.82 to 0.90, English paraphrase 0.80 to 0.91, unrelated sentences 0.29 to 0.47. 18 clusters, cohesion 0.65 to 0.94, one single loose note.
- 2026-11-19 — Proposed thresholds from that: `dup_threshold` stays 0.92 (there is a clear gap: almost no pairs between 0.90 and 0.95); `suggest_threshold` 0.60 instead of 0.55 (unrelated sentences reach 0.47, and 5% of all real pairs are above 0.57); the cohesion floor 0.45 stays (no real cluster came near it). `search_threshold` was not calibrated: queries use another prompt and need real queries. The defaults are unchanged until the owner says so.

### Today, thresholds, calendar

- 2026-11-19 — Owner: `suggest_threshold` is 0.60 by default (from the M5 calibration). Calendar integration stays a non-goal. The real vaults are imported once a real server exists, not into the demo.
- 2026-11-19 — Owner: Engage gets a **Today** tab. It lists the user's next actions and waiting-fors with a due date or target of today or earlier, and the next actions that came back today (deferred until today, or a repeating action whose day it is). The where/time/energy answers do not filter it. Overdue items are included: they are today's problem.

### Agent decisions (M3)

- 2026-11-19 — "My shopping list" has no kind of its own: it is the Engage list for a context (errands) or a project's plan. Both are kept for offline, and both take additions offline.
- 2026-11-19 — The phone keeps the last answer to each list request instead of a synced copy of items. No sync endpoint, no pinned spaces, no rules in the client. The cost: offline lists are as old as the last connection, and a filter combination never asked for online is not available offline (the per-context lists are fetched in the background so that the usual ones are).
- 2026-11-19 — Offline changes are limited to done and add. Defer was in my first proposal; it is planning, so it is out.
- 2026-11-19 — With the merge view gone, nothing shells out to `git merge-file` and git is not a runtime dependency. `conflict` stays in the item schema unused. CLAUDE.md still names both; it is the owner's file and unchanged.
- 2026-11-19 — M3 builds API tokens only as far as it needs them: scope `capture`. The other scopes of COLLAB §6 come with M6 and M8.
- 2026-11-19 — Owner, after the rewrite: "we have bigger fish to fry than optimizing for the offline corner case. Not having cell reception while in a supermarket is probably the biggest manifestation." M3 is deferred: nothing of it is built. OFFLINE.md stays as the design for when it is. The app needs a connection until then.

### Going live

- 2026-11-19 — Owner: go live with a version: several users, who change their passwords themselves; "admin user names are set in the config". `admins: [ids]` in config.yaml is the only source: the stored `admin` flag on users is gone (an old record or export that has it still loads; the field is ignored). `gtd user add -admin` became `gtd user add -init`, which only creates the database for the first user. An admin still needs the SSH-key challenge for admin routes (D-21); direct access to the data directory stays operator-level.
- 2026-11-19 — Password change: Settings asks for the current and the new password (8 to 72 characters). Wrong attempts count like failed logins (same lock). A change ends the user's other web sessions. A user's first password is set by an admin with `gtd user passwd`; there is no self-registration and no "forgot password" (an admin sets a new one).
