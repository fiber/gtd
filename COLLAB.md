# COLLAB.md — Spaces, threads, mentions, delegation, service users, MCP

## 1. Spaces and members
- Each user has one personal space (`kind: personal`, sole member). Shared spaces
  (`kind: shared`) have `members[]` (user IDs, incl. service users) and `people[]`
  (non-user names for waiting-for and agenda contexts).
- Membership is the only permission (D-11). Admins manage users and spaces but don't
  see content of spaces they aren't members of.
- **Move to space:** an item and its comments move between the actor's personal space
  and a shared space, or between two shared spaces the actor belongs to. Atomic (one
  batch). It needs confirmation listing what becomes visible to whom (attachments,
  comments). Links from other items stay valid. Readers without access see
  "unavailable item".
- **Operator note:** the server's operator can technically read everything. The admin
  UI's user-creation page states this. On the home server, guests (Grit, the kids)
  are told so.

## 2. Threads
- Every item has a comment thread (append-only, `sp/<space>/cm/<item>/<id>`). A
  comment can be deleted by its author (op `delete_comment` leaves a tombstone), but not
  edited, which keeps it simple.
- Comments support Markdown, mention and link tokens, and attachments.
- No global chat, no channels, no DMs (non-goal).

## 3. Delegation, unassigned work, Waiting For
- **Delegate to a member:** `owner = member`, `delegated_by = me`, state unchanged
  (`next` or `planned`). The member gets a `delegated` notification.
- **Accept** (from the notification): a mini-Clarify (context, defer, target).
  **Hand back:** `owner = delegated_by` plus a required comment.
- **Unassigned** (shared spaces): `owner` empty. Visible in Engage for every member
  whose context selection matches. Whoever completes it is recorded in the op log.
  "Take it" sets `owner = me`.
- **Person** (non-member): `state: waiting`, `waiting_on: <person>`.
- **Waiting For** (per user) = actions with `delegated_by == me`, `owner != me`, not
  terminal, **plus** `state: waiting` items I own, **plus** my `planned` actions whose
  open blockers are owned by others ("blocked on Eric"). Sorted by age. A "Recently
  resolved" section covers the last 7 days.

## 4. Notifications ("From others")
- Types: `mention`, `delegated`, `handed_back`, `unblocked`, `blocker_dropped`,
  `ai_reply`, `service` (a comment by a service user that mentions me).
- They're stored per user (`u/<user>/n/<seq>`) and created by `Apply` in the same
  batch as the op that caused them. No push, no email: they're inbox-only (D-02).
- In the inbox: open → shows the item and thread, with actions **Reply**, **Create
  action from this** (enters Clarify from step 3 with the comment as original),
  **Dismiss**.
- Mentions of a user who isn't a member of the item's space are rejected by the picker
  and by `Apply`.

## 5. Service users
- `service: true` users have no password and no web login. They act only via tokens.
- **`@ai` (built-in, one per server):** a member of every space by default (it's
  removable per space). When a comment mentions `@ai`:
  1. A job is enqueued with context: the item, its thread, its project subtree
     summary, and the top-5 related items **from the same space only**.
  2. The answer is posted as a comment by `ai` with `ai: true`. Structured proposals
     (`create_action`, `add_followup`, `file_note`, `set_target`) are attached as
     `proposals[]` and rendered as chips. Tapping a chip produces normal ops by the
     tapping user. Nothing is applied by `ai` itself.
  3. The requester gets an `ai_reply` notification.
- **External services** (e.g. `@nuro`): created via `gtd user add --service nuro`,
  given a token with scope `append` restricted to listed spaces (e.g. `trading`),
  and added as a member of those spaces.
  - They may: capture into the inbox of a named member (`POST /capture` with
    `for: <user>`, which lands in that user's personal inbox with
    `source.by = nuro`); comment on and attach to existing items in their spaces,
    mentioning members.
  - They may not: change fields or states, create projects or actions, read anything
    except items they're told the ID of (they get `read` only if explicitly granted).
  - **nuro integration pattern:** a finished backtest posts a comment on the
    linked edge-hypothesis item with metrics plus an equity curve PNG and report
    PDF, mentioning `@sven`. Pipeline failures post a capture for Sven. Daily
    signals are **not** posted (they're noise for GTD).

## 6. Tokens and scopes
| Scope | May |
|---|---|
| `capture` | `POST /capture` into the token owner's own inbox. Nothing else, no reads. |
| `read` | GET items, comments, search, engage in allowed spaces |
| `propose` | `read` + comment (incl. proposals) in allowed spaces |
| `append` | `capture` (also `for:` members of allowed spaces) + comment + attach in allowed spaces |
| `ops` | submit any op as the token owner in allowed spaces (used by the CLI remote mode and future clients) |

Tokens list allowed spaces explicitly. A capture-only token for a cloud AI is the
recommended way for external assistants to drop follow-ups without reading anything.

## 7. MCP server (`mcp/`)
- Streamable HTTP transport at `/mcp`, bearer token auth, tools filtered by the token's
  scopes.
- Tools:
  - `capture(text, source_ref?)`
  - `search(query, space?)`
  - `get_item(id)`
  - `get_project(id)` (subtree, plan, roll-ups)
  - `list_next_actions(context?, space?)`
  - `list_waiting_for()`
  - `comment(item_id, text)`
  - `propose_actions(project_id, actions[])` (posts a comment with proposals, never
    creates actions)
- Every MCP-originated write carries `via: mcp`, `ai_generated: true` for captures,
  and appears in the op history.
- Intended use: Sven's own assistant on the work server gets `propose` on `nis`. A
  cloud assistant gets `capture`, and at the end of a session it calls `capture` per
  open follow-up with the conversation link as `source_ref`.
