# github.com/fiber/gtd — work pack v2

The agent brief for a self-hosted, multi-user GTD tool:
- guided Capture → Clarify → Engage → Weekly Review
- cross-area projects with subproject trees, dependencies and target dates
- notes with images and PDFs
- similarity and clustering via embeddinggemma (fiber/ai at home, norad at work)
- AI suggestions (local or, at home, a cloud model) and `@ai` in item threads
- shared spaces with mentions and delegation
- service users (nuro) and MCP
- PWA with offline operation queue
- Pebble v2 storage with lossless export and snapshots

To use the tool, read `TUTORIAL.md`. To build it, start with `CLAUDE.md`. Answer the open questions in `DECISIONS.md` (Q-1 to Q-5)
when the milestone that needs them comes up. The agent is instructed to ask.

| File | Purpose |
|------|---------|
| CLAUDE.md | Agent constitution: principles, invariants, package layout, working rules |
| DECISIONS.md | Locked decisions D-01–D-26, open questions, agent decision log |
| SPEC.md | Domain model, item record, text tokens, workflows, rules, templates, UI, API |
| STORAGE.md | Daemon/CLI, Pebble keyspace, ops, index, blobs, SSH-key auth, export, snapshots |
| COLLAB.md | Spaces, threads, notifications, delegation, service users, scopes, MCP |
| AI_CONTRACT.md | Embedding backends, similarity uses, clustering, AI tasks and schemas |
| OFFLINE.md | PWA pinned spaces, sync protocol, offline ops, conflicts |
| IMPORT.md | Obsidian importer |
| MILESTONES.md | M0–M9 with acceptance criteria |
| NON_GOALS.md | Binding exclusions |

Deployment shape: two servers running the same binary.
- **Home (shada):** users Sven, Grit, the kids; spaces personal, "Sven & Grit",
  "Family", "Trading" (with service user nuro).
- **Work (company infrastructure):** users Sven, Eric, contractors; space `NIS`
  (areas Nora, Atlas, Collectors, Data curation, Automation).

## Running it

```sh
go build -o gtd ./cmd/gtd
export GTD_DATA=/var/lib/gtd            # keep the path short: the daemon's Unix socket lives in it
./gtd user add -admin -name Sven -ssh-key ~/.ssh/id_ed25519.pub sven   # creates the database
./gtd user passwd sven                  # web password
./gtd serve                             # CLI socket, and the web UI on http://127.0.0.1:8640
```

`config.yaml` in the data directory sets `server.name`, `server.listen`, `server.base_url`
and `snapshots` (STORAGE.md §10). Put nginx with TLS in front for anything but localhost.

Tests: `go test -race ./...`. `go test ./store -update` rewrites the golden export after an
intended format change.
