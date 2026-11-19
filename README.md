# github.com/fiber/gtd — work pack v2

The agent brief for a self-hosted, multi-user GTD tool:
- guided Capture → Clarify → Engage → Weekly Review
- cross-area projects with subproject trees, dependencies and target dates
- notes with images and PDFs
- similarity and clustering via embeddinggemma (in-process, or Ollama)
- AI suggestions (local or, at home, a cloud model) and `@ai` in item threads
- shared spaces with mentions and delegation
- service users and MCP
- PWA with offline operation queue
- Pebble v2 storage with lossless export and snapshots

To use the tool, read `web/TUTORIAL.md` (the app shows it under Help). To build it, start with `CLAUDE.md`. Answer the open questions in `DECISIONS.md` (Q-1 to Q-5)
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
- **Home:** the family; personal spaces and shared ones such as "Family".
- **Work:** the team; personal spaces and a shared team space with its areas.

## Going live

Build one binary and copy it to the server. It needs nothing else there, except the
embedding model's files if you want similarity.

```sh
GOOS=linux GOARCH=amd64 go build -o gtd ./cmd/gtd     # arm64 works the same way
scp gtd server:/usr/local/bin/gtd
```

On the server, as the user gtd runs as:

```sh
export GTD_DATA=/var/lib/gtd      # keep the path short: the daemon's Unix socket lives in it
gtd user add -init -name Robin -ssh-key ~/.ssh/id_ed25519.pub robin   # -init creates the database
gtd user passwd robin             # the first web password; afterwards everyone changes their own in Settings
gtd user add -name Dana dana && gtd user passwd dana
gtd space create -name Family family robin dana       # a shared space, optional
```

`$GTD_DATA/config.yaml`:

```yaml
admins: [robin]                   # who may add users and spaces and take backups
server:
  name: home
  base_url: https://example.org/gtd/   # https makes the session cookie Secure
  listen: 127.0.0.1:8640
snapshots: { schedule: daily, keep: { daily: 14, weekly: 8 } }
embed: { provider: internal, model_dir: /srv/models/embeddinggemma-300m }
# or: embed: { provider: ollama, url: "http://127.0.0.1:11434", model: embeddinggemma }
```

An admin is whoever the config lists. Admin commands also need that user's SSH key
(`gtd user key-add`), or direct access to the data directory while the daemon is
stopped: a web login alone never administers anything.

Run it under systemd (`/etc/systemd/system/gtd.service`):

```ini
[Unit]
Description=gtd
After=network.target

[Service]
User=gtd
Environment=GTD_DATA=/var/lib/gtd
ExecStart=/usr/local/bin/gtd serve
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

Put nginx with TLS in front. gtd lives entirely under `/gtd/`, so it can share a host
with other things; the proxy forwards that one path unchanged:

```nginx
location /gtd/ {
    proxy_pass http://127.0.0.1:8640;
    proxy_set_header Host $host;
    client_max_body_size 30m;    # attachments are up to 25 MB
}
```

Then, with the daemon running and your SSH key in the agent:

```sh
gtd import obsidian -dry-run ~/vault    # report only; without -dry-run: import as unsorted notes
gtd snapshot create && gtd snapshot verify
gtd export /backup/gtd-$(date +%F)      # a full, lossless export; `gtd import -restore` reads it
```

To upgrade, replace the binary and restart the service.

Tests: `go test -race ./...`. `go test ./store -update` rewrites the golden export after an
intended format change.
