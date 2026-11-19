# OFFLINE.md — PWA offline (level 2)

Goal: work on a train or plane with phone or laptop. Read your lists, complete,
defer, capture, comment and edit notes, then reconcile on reconnect. No local server,
no install beyond the PWA.

## 1. Pinned spaces
- Per device, the user pins spaces for offline use (Settings). Default: the personal
  space. The phone shouldn't cache everything.
- Cached per pinned space: all non-trashed items (without derived data), comment
  threads of open actions and projects (the last 20 per item), templates, members.
- Attachments are cached only when opened while online (and the thumbnails of cached
  items). There's a quota with LRU eviction.

## 2. Sync protocol
- `GET /api/v1/sync?space=<id>&cursor=<seq>` → `{items:[…changed…], comments:[…],
  removed:[ids], cursor:<new seq>}`. It's derived from the op log since `cursor`.
  The first sync is cursor 0 (full).
- The client stores items, comments and the cursor in IndexedDB.
- Sync runs on app open, on `online`, every 60 s while open and online, and on manual
  "Sync now".

## 3. Offline ops
- Every offline mutation becomes an `Op` (STORAGE.md §3) with a **client-generated
  ULID** `ID`, `BaseRev` = the cached item rev, and `Changes` with old and new values.
  It's queued in IndexedDB and applied optimistically to the local cache. The UI marks
  it "pending".
- The client runs a **JS port of only the trivial rules** needed for display
  (state labels, list membership). The authoritative rules run on the server. The
  client never computes unblocking or project rules. The server results arrive with
  the next sync.
- On reconnect: `POST /api/v1/ops` in queue order (batches of 50). The server
  deduplicates by op ID (`opid/` index), so replay is idempotent.
- Per op, the server decides:
  - **Apply cleanly** if `BaseRev == rev`, or if every changed field's current value
    equals its `Old` (field-level: unrelated concurrent edits don't conflict).
  - **Commutative always-apply:** `capture`, `comment`, `attach` (blobs uploaded first).
  - **Terminal wins:** a transition to `done`/`dropped` applies even if other fields
    changed meanwhile.
  - **Body text:** if the body changed on both sides, three-way merge (STORAGE.md §5)
    with base = the body at `BaseRev` (from the op log). Clean → apply. Conflict → §4.
  - **Otherwise:** reject with the current item. The client shows the conflict.
- Response: `[{op_id, status: applied|conflict|rejected, item}]`.

## 4. Conflicts
- A body conflict is stored on the item: `conflict: {by, ts, base_rev, ours, theirs, merged_with_markers}`.
  The item gets a badge, and it appears in Inbox › Conflicts and the Review's
  "Get clear" step.
- The resolver shows mine and theirs side by side: keep mine / keep theirs / edit the
  merged result. Saving clears `conflict` (op).
- Rejected field ops (rare) are shown to the user who made them as "Your offline change
  to X could not be applied: Y changed it meanwhile", with retry-on-current or discard.

## 5. Offline capture
Works even with nothing pinned. Text, photos and files are queued (photos stored as
Blobs in IndexedDB). Attachments upload first (`POST /attachments`), then the capture
op. The badge shows "3 queued".

## 6. Service worker
Hand-written (`web/ui/sw.js`). Caches the app shell with versioned cache names: the
shell HTML, the vendored Lit bundle, the JS modules, the CSS file, the i18n catalogs,
the manifest and icons. Network-first for the shell when online, cache fallback
offline, and every UI route falls back to the cached shell. The service worker does
not cache API data: components read through `store.js`, which uses the API online
and IndexedDB offline (one data layer, two sources). Attachments are cache-first by
hash (immutable).

## 7. Acceptance (automated where possible)
1. Offline: complete 3 actions, defer 1, capture 2 (1 photo), edit a note body. Online:
   everything applied exactly once, and the dependents of completed actions are
   unblocked by the server.
2. Offline on device A, B edits the same note's title online: clean (field-level).
3. Both edit the same body line: conflict surfaced on A, resolved, and B sees the result.
4. Replaying the same queue twice (simulated retry) produces no duplicates.
5. Offline completion of an action that B dropped meanwhile: rejected with a clear message.
