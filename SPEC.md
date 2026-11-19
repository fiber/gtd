# SPEC.md — Domain model, workflows, UI

## 1. Concepts

| Kind | Meaning | States |
|------|---------|--------|
| `capture` | Raw inbox item, always in the capturing user's personal space | `inbox` |
| `action` | One physical next action | `next`, `planned`, `waiting`, `someday`, `done`, `dropped`, `trashed` |
| `project` | An outcome needing more than one action. May have a `parent` project (unlimited depth). | `active`, `someday`, `done`, `dropped`, `trashed` |
| `note` | Reference or project support material, with images and attachments | `unsorted`, `filed`, `archived`, `trashed` |
| `area` | Area of focus (flat per space), e.g. Nora, Atlas, Home, Trading | `active`, `retired` |

- `planned` = action has open blockers (`blocked_by`). It's set and cleared only by
  the dependency rule (§6.2), plus explicitly when the user adds follow-up steps.
- **Deferral (tickler):** `defer_until` (date) on actions, projects and notes. Items
  are hidden from working views until that date (§5.6).
- **Ownership:** actions have `owner` (user ID). In a personal space it's always the
  space's user. In a shared space it's a member or empty (empty = unassigned, done by
  whoever gets there first).
- A **comment thread** belongs to each item (COLLAB.md §2).

## 2. Item record

Stored as versioned JSON in Pebble (STORAGE.md). The export representation is
Markdown with YAML frontmatter, using the same field names:

```yaml
id: 01JD8X4Q9V3T6M2K7P5R1W0Y8Z     # ULID
space: nis                          # space id
kind: action
title: "NIS/BGP: Collector — clarify session-reset semantics?"   # any characters
state: next
rev: 7                              # monotonically increasing per item (ETag)
created: 2026-11-19T08:12:03+01:00
updated: 2026-11-19T08:15:40+01:00
updated_by: sven
original: "bgp collector reset thing ask eric"   # immutable
source: { via: web|share|cli|mcp|import|email|service, ref: "url or app", by: "user-id" }
ai_generated: false                 # capture created by an AI/service
area: 01JD…                         # item id of an area
project: 01JD…                      # owning project (actions, notes)
parent: 01JD…                       # parent project (projects only)
context: errands                    # actions only; canonical or personal key (D-06)
owner: sven
delegated_by: ""
waiting_on: ""                      # person name for non-member delegation (state waiting)
blocked_by: [01JD…, 01JD…]          # actions in same space
defer_until: 2026-12-03
target: 2027-01-15
due: ""
energy: ""                          # low|med|high
time: ""                            # 5m|15m|30m|1h|2h+
template: ""                        # template key from the space (§7)
fields: {}                          # template field values, string-encoded
attachments: [{sha256, name, mime, size}]
derived_from: []                    # forked notes and AI digests: source ids
generated: false                    # AI-generated note (digest)
conflict: null                      # pending merge conflict (OFFLINE.md §4)
imported: {}                        # opaque foreign metadata from import
body: |
  Markdown body…
```

Rules:
- `id`, `space` (changed only by the move op), `created` and `original` are
  immutable. `rev`, `updated` and `updated_by` are set by `Apply` on every op.
- Unknown keys under `imported` and `fields` are preserved.
- Zero values are omitted in export. Export field order is fixed as above (golden tests).

## 3. Body content

### 3.1 Markdown
CommonMark plus tables and task lists. Rendered server-side with a small, safe
renderer. If stdlib isn't enough, propose a dependency (e.g. goldmark) per CLAUDE.md.
No raw HTML passthrough.

### 3.2 Attachments
- Ingest via upload, paste, drag and drop, the Android share target, camera
  (`<input type=file accept="image/*" capture>`), the capture API, or MCP/service
  `append`. Server computes sha256 and stores the blob (STORAGE.md §6) if absent
  (dedup), then adds an `attachments` entry.
- An image is embedded at the cursor as a token (§3.3). A non-image becomes a
  download chip.
- Limits: 25 MB per file (config). HEIC is stored but gets no preview (stdlib can't
  decode it). Thumbnails (max 1024 px) are generated for JPEG/PNG/WebP and cached.
- PDFs: if `pdftotext` is on PATH, extracted text feeds search and embeddings. View
  uses the browser's viewer.
- A photo-only capture gets the title "Photo <localized timestamp>". A typical case is
  a whiteboard photo from a design session.

### 3.3 Tokens in text (invariant 8)
| Shown to user | Stored as | Inserted by |
|---|---|---|
| Link chip with current title | `[[id]]` or `[[id\|label]]` | typing `[[` → title search picker (semantic fallback); or pasting an item URL |
| Embedded image | `![[att:<sha256>]]` | upload/paste/camera |
| File chip | `[[att:<sha256>]]` | upload/paste |
| Mention chip | `@[user:<id>]` | typing `@` → picker of the item's space members and service users |

Unresolvable link (no access, trashed) renders as "unavailable item". An unknown
`@foo` typed without picking stays plain text.

## 4. Capture

- **Screen:** one text field, 📷 camera, 📎 file. Enter submits. Returns at once to an
  empty field with a brief "captured" confirmation and nothing else (D-02).
- **Paste of multiple lines** asks once: "1 item or N items?".
- **Sources:** web/PWA, Android share target (text/URL/files), bookmarklet (captures
  selection plus page URL plus title, `source.ref` = URL), `POST /api/v1/capture`
  (token, used by an iOS Shortcut), `gtd add`, MCP `capture`, service users, email-in
  (optional, later).
- **Result:** `kind: capture, state: inbox` in the user's personal space, with
  `original` and `source` set. Embedding and AI `clarify` jobs are enqueued.
- **Target:** under 3 s from app open to captured. The capture page must work offline
  (OFFLINE.md).

## 5. Workflows

All wizards live in `wizard/` as pure step machines:
`Step(state, input) → (newState, ops []core.Op)`. Every user-confirmed step produces
ops applied in one batch. The web layer only renders states and posts inputs.

### 5.1 Inbox
Two sections: **Captures** (oldest first) and **From others** (notifications:
mentions, delegations, unblocked actions, AI replies, service posts; COLLAB.md §4).
Plus a "Conflicts (n)" link when any exist.

### 5.2 Clarify (process captures, oldest first, one at a time)
No skip and no "back to inbox". Leaving mid-item is allowed, and the item stays at
the head. At every step the AI suggestion (if any) preselects an answer, and the
user confirms with one tap or overrides.

1. **What is it?** Original, attachment previews, AI-cleaned title and body as a diff,
   top-3 similar items with "duplicate of…" (append the capture to that item).
2. **Which space?** Personal (default) or a shared space the user belongs to.
   Preselected by similarity (only among the user's spaces on this server).
   Skipped if the user has no shared spaces.
3. **Actionable?**
   - No → **Trash** / **Someday** (with optional `defer_until`) / **Reference** (→ note;
     choose project/area from top-3 suggestions, "new project", or "unsorted"; offer
     a template if the space has any).
4. **More than one step?**
   - Yes → project. Offer the top-3 existing projects ("add to this project") or a
     new project: an outcome-phrased title, a parent project (optional, top-3
     suggestions), a home area, a target. A new project cannot be finished without
     a next action.
5. **Next physical action?** A verb-first sentence (AI-prefilled, editable). Area
   preselected from the project.
6. **Under 2 minutes?**
   - Yes → **Do it now** with a 2-minute timer → Done, or "took longer" → continue.
7. **Who?**
   - **Me**.
   - **Member** of the space: `owner` = member, `delegated_by` = me. If the item is
     in the personal space, offer to move it to a shared space they're both in
     (confirmation).
   - **Anyone** (shared space only: unassigned).
   - **Person** (non-member): `state: waiting`, `waiting_on`.
8. **When and where?**
   - Context chips: canonical keys first, personal keys only in the personal space.
     Choosing "Agenda · Eric" shows "or notify Eric now?" (mention instead).
   - "Not before" presets: Tomorrow / Next week / 2 weeks / 1 month / Pick → `defer_until`.
   - Target date (presets plus a picker), due date (with a hint: "only for real
     deadlines"), energy and time chips. All optional.
9. **Anything after that?** (Projects only, optional.) Quickly add follow-up actions as
   `planned`, each "after" a previous one (default: after the action just created).
   Each gets title, area, owner, optional target. This is GTD's project-planning step.

### 5.3 Engage
- **Questions** (each skippable, last answers remembered per device): Where are
  you? (multi-select contexts) · How much time? · Energy?
- **List:** actions in `next` where `owner == me` or (`owner` empty and the space is
  shared and I'm a member), not deferred. Order: due → target → recently unblocked
  or resurfaced → age. Show **at most 7**, then "show more".
- **Item actions:** Done, Defer (presets), Not now, Edit, Comment, Focus.
  - Done on a project's last open action prompts: "Next action for <project>, or is it
    complete?"
- **Focus mode:** one action fullscreen, notes and attachments visible, a timer, a
  single capture key (captures go to the inbox silently). Done or Exit.
- **Tabs:** **Waiting for** (COLLAB.md §3) and **Due and targets** (next 14 days).

### 5.4 Weekly Review (guided, resumable on any device)
Progress is stored per user. Steps:
1. **Get clear:** captures to zero (embedded Clarify), From others, conflicts.
2. **Unsorted sweep:** N clusters (default 5, §5.7).
3. **Projects** (tree order, one project per screen with its subtree summary):
   still alive? (keep / someday / done / drop). Is the next action right? A kept
   project must be **alive** (§6.3), or the wizard forces a decision.
4. **Stale next actions** (unchanged for more than 14 days): keep / defer / someday / drop.
5. **Waiting for** (older than 7 days): nudge (adds a comment with an `@mention`, or for
   persons creates an `agenda:<person>` or `calls` action) / keep / resolved.
6. **Dates:** slipping targets (target passed, not done), plausibility warnings
   (D-05), and due/target dates in the next 14 days.
7. **Someday/Maybe:** a rotating subset (default 10, least recently reviewed first):
   activate / keep / drop.
8. **Areas:** active project counts. Flag areas with none.

A progress bar, then a summary. A banner on the configured weekday.

### 5.5 Collate and organize notes
- **Note view:** Markdown editor (textarea plus preview toggle, images inline), related
  sidebar (top 5 similar in the user's spaces, space shown), backlinks, thread.
- **Fork:** "New note from this" creates a note with `derived_from: [source]` and a
  link back, for ideas that turn into something else.
- **Project view = plan view** (§6.4). It also has a "might belong here" strip: the
  5 nearest unsorted or unfiled notes, one tap to file.
- **Digest** ("Collate" on a project, subtree or area): an AI-generated note
  (`generated: true`, `derived_from`) with summary, decisions, open questions,
  interfaces and contradictions, each citing sources. Originals are untouched.
  Regenerating archives the previous digest.

### 5.6 Tickler
At startup and at local midnight, items with `defer_until <= today` get
`defer_until` cleared (op by `system`) and are marked "resurfaced" for 3 days
(highlighted in Engage and Review). Deferred `someday` items resurface in the
Someday review instead.

### 5.7 Unsorted sweep
Clusters of `note`/`unsorted` items (AI_CONTRACT.md §2.5), one per screen: label
(AI, or the top-3 titles), members with checkboxes (all checked), and the
`imported.folder` hint. Actions: **File all into <suggested project>** / **New
project from cluster** / **Into area…** / **Someday** / **Archive** / **Skip**.
Per note: accept the AI-restored title (one tap), and "create action" chips from
todo detection.

### 5.8 Board
A Trello-style overview, filterable by space, area and project (incl. subtree).
Columns: Inbox | Next | Planned | Waiting | Deferred | Someday | Done (14 days).
Drag applies the same validated ops. It's an overview and quick-fix tool; the process
runs in the wizards.

### 5.9 Search
One box: exact token search over title, body, fields and extracted PDF text
(in-memory scan), then semantic hits (embedding). Restricted to the user's spaces,
deduplicated, exact hits first. `gtd grep` does the same on the CLI.

## 6. Rules (`core/`)

### 6.1 Transitions
A table per kind of allowed `state` transitions. Invalid → error (409 at the API).
`capture` leaves `inbox` only through Clarify ops or a duplicate merge.

### 6.2 Dependencies
- `blocked_by` only references actions in the same space. Adding an edge that
  creates a cycle → error.
- An action with any blocker not in {`done`, `dropped`, `trashed`} must be `planned`.
  When the last open blocker completes, the dependent action becomes `next` in the
  same batch, and an `unblocked` notification goes to its owner (or to all members if
  unassigned). Dropping a blocker counts as completing it, plus a warning
  notification ("blocker dropped").
- Reopening a done blocker moves dependents that aren't done back to `planned`.

### 6.3 Project liveness
A project is **alive** if it or any descendant project has an action in `next` or
`waiting`, a deferred action, or a `planned` action whose open blockers are owned by
someone else ("stalled on Eric: X"). Otherwise it's **stuck**: a badge everywhere,
and it's forced in Review.

### 6.4 Plan view and roll-ups
Project tree, collapsible. Per node: done/total actions (incl. descendants), blocked
count, waiting count, earliest open target, a slipping flag. Actions grouped by area,
each showing owner, state, target and "waits for: X (owner)". Drag one action onto
another sets `blocked_by`. Dragging a project onto another sets `parent`.

### 6.5 Date plausibility (warnings only)
Child project target later than its parent's target. Action target earlier than any
blocker's target. Target passed while not done → slipping.

## 7. Templates
Defined per space (space record):
```yaml
templates:
  - key: edge_hypothesis
    kind: project
    label: { en: "Edge hypothesis", de: "Edge-Hypothese" }
    fields:
      - { key: stage, type: enum, options: [idea, testing, kept, falsified], label: {en: Stage, de: Phase} }
      - { key: nuro_test, type: text, label: {en: "nuro test", de: "nuro-Test"} }
    body: "## Hypothesis\n\n## Why it could work\n\n## How to falsify\n\n## Result\n"
  - key: trade_setup
    kind: note
    fields:
      - { key: ticker, type: text }
      - { key: setup, type: enum, options: [S1, S2, S3, other] }
      - { key: entry, type: number }
      - { key: stop, type: number }
      - { key: target_price, type: number }
      - { key: expires, type: date, tickler: true }   # sets defer_until on the expiry date
    body: "## Thesis\n\n## Invalidation\n"
```
- Field types: `text`, `number`, `date`, `enum`. An `enum` field is shown as a badge
  and is filterable in views and on the board. `tickler: true` on a date field sets
  `defer_until` so the item resurfaces with "still alive?".
- Templates don't change GTD state semantics.
- Editing templates is admin-ish UI: a YAML textarea with validation is fine for v1.

## 8. UI shell
- Mobile-first. Bottom nav: **Capture · Inbox (n) · Engage · Projects · More**
  (Review, Board, Search, Areas, Settings). Desktop: the same as a left rail.
- Language per user (D-09). All dates are formatted via the locale.
- Every item has a stable URL `/i/<id>`.
- Keyboard on desktop: `c` capture, `/` search, `e` engage, `j/k` lists, `x` done.

## 9. HTTP API (JSON, `/api/v1`, stable)
- `POST /capture` (JSON `{text, source?}` or multipart with files) → `{id}`
- `GET /items/{id}` (`ETag: rev`) · `GET /items?space=&kind=&state=&project=&owner=&context=`
- `POST /ops` — submit one or more ops (web, offline replay and CLI all use this).
  Returns applied ops or conflicts (OFFLINE.md §3).
- `GET /items/{id}/comments` · comments are created via ops
- `POST /attachments` (multipart) → `{sha256, mime, size}` · `GET /att/{sha}` · `GET /att/{sha}/thumb`
- `GET /search?q=` · `GET /engage?...` · `GET /sync?cursor=` (OFFLINE.md)
- `GET /notifications` · `POST /notifications/{id}/dismiss`
- Auth: session cookie or `Authorization: Bearer <token>` (COLLAB.md §6).
