# CLAUDE.md — Agent constitution for `github.com/fiber/gtd`

You are building `gtd`, a self-hosted, multi-user tool that guides people through
Getting Things Done (Capture → Clarify → Organize → Reflect → Engage), organizes
their notes (with similarity search and local AI assistance), and lets a small team
or family collaborate on shared spaces. One server process per trust domain (for
example one at work, one at home), with a PWA client for desktop and phone.

Read before writing any code, in this order:
1. `DECISIONS.md` — locked decisions and open questions. Do not relitigate. If one blocks you, stop and ask.
2. `SPEC.md` — domain model, workflows, UI.
3. `STORAGE.md` — Pebble keyspace, op log, daemon/CLI, auth, export, snapshots.
4. `COLLAB.md` — spaces, members, threads, mentions, delegation, service users, MCP.
5. `AI_CONTRACT.md` — embeddings, similarity, clustering, AI tasks and schemas.
6. `OFFLINE.md` — PWA offline snapshot and operation queue.
7. `IMPORT.md` — Obsidian vault importer.
8. `MILESTONES.md` — build order and acceptance criteria.
9. `NON_GOALS.md` — binding exclusions.

## Engineering principles

- **KISS above all.** Few packages, few interfaces, no layers for their own sake. An
  interface exists only where there are two real implementations or a test seam that
  is actually used. No "service", "repository", "manager", "factory" layers, no DI.
- **Go, stdlib first.** Allowed without asking: stdlib, `golang.org/x/...` (incl.
  `x/crypto/ssh`, `x/crypto/ssh/agent`, `x/crypto/bcrypt`, `x/image`),
  `github.com/cockroachdb/pebble/v2`, `github.com/oklog/ulid/v2`,
  `gopkg.in/yaml.v3`, and in-house packages under `github.com/fiber/*` (notably
  `github.com/fiber/ai`). Anything else: propose it with one paragraph of
  justification and wait.
- **No SQL, no ORM.** Pebble is the only database (STORAGE.md).
- **No frontend build chain.** `html/template` + vendored `htmx.min.js` + one CSS
  file + a hand-written service worker and a small offline JS module. No npm,
  bundler, TypeScript or SPA framework.
- **Shell out where it is the simple, robust choice:** `pdftotext` (optional) and
  `git merge-file` (text three-way merge; git is a runtime dependency only for this).
- **Pure core, thin edges.** Domain logic (state machine, op application, wizard
  steps, dependency rules, merge rules, permission checks) lives in plain functions
  over plain structs, with no I/O, and is table-tested. Storage and HTTP are thin.

## Invariants (never violate)

1. **The original capture text is never lost.** `original` is immutable. AI cleanup
   goes to other fields.
2. **AI and machines only suggest.** No AI output becomes a state change without a
   human action. Service users (e.g. nuro) may create captures and comments within
   their token scope, and nothing else. With AI unavailable, every workflow still works.
3. **Every mutation is an op.** Item/comment state and its op record are written in
   the same Pebble batch. No write path bypasses `Apply`.
4. **Space isolation.** No read or write path exists without a membership check.
   Similarity, search, AI context and MCP results never include items from spaces
   the acting user is not a member of.
5. **Export is lossless.** `export` → `import --restore` into an empty DB yields
   identical items, comments, attachments and op history.
6. **Attachments are immutable and content-addressed** (sha256).
7. **The UI never blocks on AI or embeddings.** They are asynchronous.
8. **No syntax in text** except `[[…]]` links and `@` mentions, both inserted via
   pickers and stored as tokens (SPEC §3.3). Contexts, dates, states and owners are
   only ever set through UI controls.

## Package layout (do not add top-level packages without asking)

```
cmd/gtd/     main: serve (daemon) and CLI subcommands
core/        types, state machine, op application (Apply), dependency rules,
             permission checks, text merge wrapper. Pure.
wizard/      Clarify / Engage / Review / Sweep step machines. Pure.
store/       Pebble keyspace, batches, in-memory index, blobs, export/import, snapshots
auth/        password+sessions, API tokens+scopes, SSH-key challenge auth
embed/       Embedder (fiber/ai or norad), vector cache, similarity, clustering
ai/          generation client, embedded prompt files, schema validation, @ai worker
mcp/         MCP server endpoint
web/         HTTP handlers, templates, i18n catalogs, static (htmx, css, sw.js, offline.js, manifest)
importer/    Obsidian vault import
```

## Working rules

- **One milestone at a time** (MILESTONES.md). Done means every acceptance criterion
  passes. Show the evidence: test output, timings, screenshots where it's UI.
- Tests alongside code. Table tests for `core/` and `wizard/` are mandatory. So are
  golden tests for export format and import.
- Run `go vet`, `staticcheck` (if available) and `go test -race ./...` before
  declaring a milestone done.
- Ambiguity: pick the simplest reading, implement it, and record it in
  `DECISIONS.md` › "Agent decisions" (date, decision, one-line rationale). For real
  product questions, stop and ask.
- No speculative generality: no flags, options or extension points that no
  milestone needs.
- Error messages are for an expert operator: precise, with IDs and keys involved.
- Commit messages end with the attribution lines the session requires.
