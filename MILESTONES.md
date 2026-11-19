# MILESTONES.md

Build strictly in order. The data model is multi-user and multi-space from M0, even
though collaboration features arrive in M6. Goal: Sven can use the tool daily
(single user, home server) after M2.

## M0 — Core, storage, CLI
- `core/`: types, transition tables, `Apply` with dependency rules (§6.2), project
  liveness (§6.3), date warnings (§6.5), permission checks.
- `store/`: Pebble v2 keyspace, batched op commit, per-space locks, in-memory index,
  blobs, `View(user)` isolation.
- `auth/`: SSH-key challenge over the Unix socket, user and admin bootstrap.
- `cmd/gtd`: `serve` (socket only for now), `user`, `space`, `add`, `grep`,
  `export`, `import --restore`, `snapshot create|list|prune|verify`, `restore`,
  `ops tail`. CLI falls back to direct DB access when no daemon is running.
- **Accept:**
  - Table tests for every transition and dependency case (cycles, unblock, reopen,
    drop).
  - Export → import round-trip golden test (invariant 5).
  - 50k items load in under 3 s.
  - A crash test (kill -9 during a write loop) leaves a consistent DB.
  - Snapshot verify detects a corrupted blob.
  - The same `export` output with and without a running daemon.

## M1 — Web core (single user, GTD loop)
- Login and sessions, the JSON API (SPEC §9) incl. the wizard endpoints and
  server-rendered `body_html`.
- The Lit frontend in `web/ui/` (buildless, D-22): app shell with client-side
  routing and item URLs, `store.js` as the single data layer, one component per
  screen, generic step rendering for wizards, i18n `de`/`en` from JSON catalogs
  with dates via `Intl`, mobile-first.
- Capture (text, camera, file, multi-line split). Clarify wizard (all steps, no AI).
  Engage incl. focus mode. Project plan view (tree, dependencies via drag, roll-ups,
  warnings). Areas. Board. Note view (editor, images inline, attachment chips, link
  picker, URL-paste conversion, fork). Templates (YAML editing, fields, badges,
  tickler fields).
- `wizard/` pure and table-tested for every Clarify path.
- **Accept:**
  - Process a 20-item inbox on an Android phone with taps only (plus editing the
    next-action text).
  - Build a 3-level project with cross-area actions and dependencies. Completing a
    blocker unblocks the dependent action.
  - The capture page is interactive in under 1 s on a mid-range phone over 4G.
  - All UI strings exist in both catalogs (a test fails on missing keys).
  - The UI loads with no build step: only embedded files, and no request leaves the
    server's origin.

## M2 — Weekly Review and tickler
- The review wizard (SPEC §5.4), resumable across devices. Tickler resurfacing.
  Weekday banner.
- **Accept:** a scripted review over a fixture walks every step. Finishing a stuck
  kept project is impossible. Deferred items resurface on the right day (injected
  clock).

## M3 — PWA and offline (OFFLINE.md)
- Manifest, share target (text, URL, files), service worker, offline capture queue,
  pinned-space snapshot sync, op queue replay, conflict resolver. Bookmarklet.
  `POST /capture` with a token (plus README steps for an Apple Shortcut).
- **Accept:** OFFLINE.md §7 scenarios. Share a PDF from the Android Files app → it
  arrives as a capture with the attachment.

## M4 — Embeddings and similarity
- **First:** read the `github.com/fiber/ai` embeddinggemma example, and ask about
  Q-2 (norad) and Q-3 (SIMD kernels).
- Embedder backends, vector storage, job queue, AI status indicator, similarity in
  Clarify steps 1–4, "might belong here", related sidebar, semantic search, PDF text.
- **Accept:**
  - A top-k query over 50k × 768 in under 5 ms.
  - Embedding backend down → UI unaffected, and the queue recovers.
  - Wiping `x/vec` and running `reindex` gives identical results.
  - An isolation test: no similarity result from a non-member space.

## M5 — Obsidian import and unsorted sweep
- The importer (IMPORT.md), clustering, the sweep wizard without AI labels.
- **Accept:** IMPORT.md §7. A run on the real vault, with a report of cluster stats and
  score distributions (incl. de↔en pairs) and proposed thresholds. 5 clusters can be
  swept on a phone in under 5 minutes.

## M6 — Collaboration
- Shared spaces UI (create, members, people), move-to-space, threads, the mention
  picker, notifications ("From others"), delegation accept and hand-back, unassigned
  actions, Waiting For incl. "blocked on X", op history view, undo.
- **Accept:**
  - A two-user test (Sven, Eric) on one server covering delegation, mention, an
    unassigned action completed by the other, an unblock across owners, and a move from
    personal to shared.
  - The isolation suite: every route as a non-member → 404.

## M7 — AI generation
- **First:** ask about Q-1 (generation backend).
- `clarify` preselection, `title`, `cluster_label`, `todo_detect`, `digest`
  (map-reduce), `@ai` thread replies with proposal chips. Optional image
  descriptions.
- **Accept:**
  - 50 fixture captures (de and en): at least 98% schema-valid after retry, and
    injected bad IDs are always rejected.
  - Generation off → all wizards still work.
  - A digest of a 30-note fixture cites only valid sources and finds the planted
    contradictions.
  - `@ai` never sees items from other spaces (test).

## M8 — MCP and service users
- Token scopes, the MCP endpoint and tools, service users, `for:` captures, the
  `append` scope.
- **First:** ask about Q-4 (nuro events). Document a nuro integration example (curl).
- **Accept:**
  - Every tool is gated by scope (test matrix).
  - A capture-only token cannot read anything.
  - A simulated nuro posts a backtest comment with a PNG and a PDF on an
    edge-hypothesis item, and Sven gets the notification.

## M9 — Linked servers (client)
- In the PWA: add a linked server (base URL plus a token with `read` and `ops`
  scopes), account switcher, capture destination by rules (last used per device,
  device default), combined Engage across servers (CORS allow-list on each server).
- **Accept:** with the home PWA and the work server linked, the combined Engage shows
  both. Capture never sends text to a server other than the selected one (a
  network-log test).
