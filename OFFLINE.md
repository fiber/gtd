# OFFLINE.md — PWA offline (capture first)

Rewritten 2026-11-19 (D-23, Q-7). The earlier design ("level 2": a synced copy of
pinned spaces, every change possible offline, three-way merge, a conflict resolver)
is dropped. The owner: "I do want to take offline notes (in IN) and be able to
retrieve my shopping list (and also add to it) on the go. I don't need all the
planning features while offline."

Goal: on the go, without a connection, with the phone:

1. **Capture** anything into the inbox: a line, a long note, photos, files.
2. **See your lists** as they were when the phone last had a connection: next
   actions (also per place, such as errands), Today, waiting for, the inbox, the
   projects and each project's plan.
3. **Tick off** an action, and **add** an action to the list you are looking at.

Everything else waits until you are online: Clarify, planning, editing notes and
fields, comments, the weekly review, search. Those screens say so offline.

No local server, no install beyond the PWA, and no GTD rules in the client.

## 1. What the phone keeps
- **The app itself** (service worker, §5).
- **The last answer to every list request**, in IndexedDB, keyed by request: the
  client asks the server as always, keeps each successful answer, and shows the kept
  one when the server cannot be reached. A kept answer is shown with its age ("as of
  14:05").
- So that the lists are there without having been opened: after the app starts, and
  when the connection comes back, the client fetches in the background the inbox,
  Engage (unfiltered, each tab, and once per context the user has), the projects, and
  the plan of every open project.
- **The queue** (§2, §3) of things done offline.
- Items are not kept one by one, and attachments are not kept. An item page that was
  opened online can be read offline like any other kept answer.

The kept answers and the queue belong to the signed-in user. Signing out deletes
both. (A queue that is not empty asks first.)

## 2. Offline capture
The capture box works without a connection and without anything kept. Text, photos
and files go into the queue (files as Blobs in IndexedDB). When the connection is
back, each is sent as an ordinary capture (`POST /capture`, multipart), in order,
with a client-generated op ID so that a retry cannot create it twice. The
navigation shows how many are waiting ("3 queued").

The same queue takes what the phone shares into gtd (§6).

## 3. Offline ticks and additions
On a kept list, offline:

- **Done** on an action queues a transition op (`state: done`).
- **Add** (one line, on Engage and on a project's plan) queues a create op for an
  action: on a plan with that project, on Engage with the context that is selected
  when exactly one is. Nothing else can be set offline.

Each queued op has a **client-generated op ID**, `via: offline`, and for a transition
the `base_rev` and old state of the kept item. The kept list is not rewritten: the
client draws queued ticks as done-pending and queued additions as pending rows at the
end of the list they were added to. It computes nothing else: what completing an
action unblocks, where a new action sorts, all of that arrives with the next answer
from the server.

On reconnect the queue is sent in order, one op per request (`POST /ops`). The
server deduplicates by op ID (`opid/` index), so sending twice is harmless. For each:

- **Applied:** removed from the queue.
- **Rejected** (the action was changed or completed by someone else meanwhile, or no
  longer exists): removed from the queue and shown once: "Your offline change to
  <title> was not applied: <reason>". A completion of an action that is already done
  is not an error.
- **No connection after all:** it stays, and sending stops until the next try.

Sending is tried when the app starts, on `online`, and every 60 s while the app is
open and the queue is not empty.

## 4. What is not offline
No offline editing of titles, bodies or fields; no comments; no Clarify; no defer;
no merge and no conflict view. `conflict` (SPEC §3) and the three-way text merge
(STORAGE §5) are not needed for offline any more.

## 5. Service worker
Hand-written (`web/ui/sw.js`), scope `/gtd/`. It caches the app: the shell HTML, the
vendored Lit bundle, the JS modules, the CSS file, the i18n catalogs, the manifest
and the icons, under a cache name that carries the server's build version; an old
cache is deleted when a new version is active. Shell and files: network first,
cache when the network fails, and every UI route falls back to the cached shell.
The service worker does not cache API data: components read through `store.js`,
which asks the API and falls back to what it kept (§1). One data layer.

## 6. Getting things in from other apps
- **Share target** (installed PWA, Android): text, a URL and files shared to gtd are
  posted to `/gtd/share`. The service worker takes that request, puts it into the
  capture queue (§2) and opens the app, online or offline.
- **Bookmarklet:** opens `/gtd/capture?title=…&url=…&text=…` in a small window with
  the capture box filled in.
- **`POST /gtd/api/v1/capture` with a token** of scope `capture` (COLLAB §6): for an
  Apple Shortcut and scripts. Such a token can do nothing else.

## 7. Acceptance (automated in a browser where possible)
1. Offline, with the app opened once before: capture two things (one with a photo).
   Online again: both are in the inbox exactly once, the photo attached.
2. Offline: the Engage list for one context and a project's plan show what they showed
   online, marked with their age.
3. Offline: complete two actions and add one to the context list and one to a plan.
   Online again: all four applied exactly once; an action that was blocked by a
   completed one is next (the server's rule).
4. The queue sent twice (simulated retry) creates nothing twice.
5. Offline completion of an action that was dropped online meanwhile: reported as not
   applied, the queue is empty afterwards, nothing else is lost.
6. A reload while offline shows the app, not the browser's error page.
7. A token of scope `capture` can capture and can read nothing.
8. Sharing text and a file to the installed app creates a capture with the attachment
   (verified on a real Android phone by the owner; in the browser the share request is
   simulated).
