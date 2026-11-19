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
| D-09 | **i18n:** UI languages `de` and `en`, chosen per user (default from Accept-Language). Embedded catalogs, `t "key"` in templates, locale-aware date formats, weeks start on Monday for `de`. |

## Topology and collaboration

| ID | Decision |
|----|----------|
| D-10 | **One server per trust domain**, for example work on company infrastructure, home on shada. Same binary. **Multi-user per server.** There is no sharing between servers in v1. |
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
| D-22 | **Web UI:** server-rendered `html/template` + htmx, installed as a PWA. Android is first-class (share target for text, URLs, files, camera capture). iOS via Safari install plus an Apple Shortcut. No native apps. |
| D-23 | **Offline: level 2** (OFFLINE.md). A cached snapshot of pinned spaces in IndexedDB, with offline mutations as ops replayed on reconnect. Conflicts go to a merge view. |
| D-24 | **Embeddings:** `embeddinggemma`. Home uses `github.com/fiber/ai` (see its embeddinggemma example; the agent determines whether that's in-process or HTTP). Work uses the existing **norad** embedding server. One `Embedder` interface. Exact brute-force similarity with in-house SIMD kernels (from `github.com/fiber/ai` or vektor, whichever provides SGEMM/dot). No ANN, no vector DB. |
| D-25 | **AI generation is local only**, configured per server, optional at runtime, structured JSON output validated against schemas. No cloud providers. |
| D-26 | **Obsidian is abandoned.** No compatibility requirement. A one-time importer migrates the existing vault (thousands of notes) into an "unsorted" pool, which the sweep then works through. Titles may contain any characters. |

## Open questions (owner to answer; agent: ask, don't guess)

- **Q-1** What serves Gemma *generation* on each server (fiber/ai? norad? something else), and its API? Needed before M7.
- **Q-2** The norad embedding server's API (endpoint, request/response, batch limit, max input, model and dim). Needed before M4 for the work server. Check whether it matches fiber/ai's protocol.
- **Q-3** Which package provides the SIMD SGEMM/dot kernels: `github.com/fiber/ai`, vektor (import path?), or both. Needed before M4.
- **Q-4** nuro side of the integration: which events nuro will post (backtest finished, pipeline failure, …). Needed before M8. gtd only provides the generic `append` mechanism.
- **Q-5** Sven's personal extra contexts and review weekday (config, any time).

## Agent decisions

(One line each: date — decision — rationale.)
