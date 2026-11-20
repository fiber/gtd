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

An admin is whoever the config lists. In the browser an admin manages users and
shared spaces under Settings › Administration, after giving their password again.
On the command line, admin commands need that user's SSH key (`gtd user key-add`), or
direct access to the data directory while the daemon is stopped.

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

### AI (optional)

gtd asks a language model only when someone presses an AI button, and only with the
text of a space that allows it. The first such button is **Suggest task lines** on a
note: it proposes `- [ ]` lines for the commitments in the text, you tick the ones
you want, and Process note takes it from there.

The config is the same as nuro's: one provider and one key per server.

```yaml
ai:
  provider: anthropic        # or gemini
  api_key: "…"
  # model: claude-opus-5-5   # optional; that is the default for anthropic,
  #                          # gemini-3.8-flash for gemini
```

`config.yaml` then holds a secret: `chmod 600` it (the daemon says so at start if
others can read it). Every user of the server who presses an AI button uses this
key.

Each space has an AI setting (Settings › AI): **Never**, **Only a model on our own
machines** (the default) or **Also a cloud model**. Anthropic and Gemini are cloud
models, so until a space is set to "Also a cloud model", nothing of it is sent
anywhere and it shows no AI buttons.

### Capturing from outside the app

A capture-only token can put things into your inbox and can do nothing else: it
cannot read, search or change anything.

```sh
gtd token add -label phone > ~/.gtd-token     # prints the token once
gtd token list                                # ids, labels, last use
gtd token rm <id>                             # revoke
```

From a script, or an Apple Shortcut ("Get contents of URL": POST, header
`Authorization: Bearer <token>`, JSON body with `text`):

```sh
curl -H "Authorization: Bearer $(cat ~/.gtd-token)" -H 'Content-Type: application/json' \
     -d '{"text":"call the plumber about the quote"}' https://example.org/gtd/api/v1/capture
```

### Linked servers

If you have an account on two gtd servers (say home and work), a capture that
belongs on the other one can be sent there from Clarify ("Send to work"). It leaves
your inbox here and arrives in your inbox there.

You do not have to open the other server's app to get a thought into it: the
capture page shows a chip per linked server ("→ work"). Pick it and capture, and the
note goes to your inbox there without stopping in the one here. The choice lasts for
one capture; the next one is for here again. If the other server cannot be reached,
the note waits (Settings › Linked servers shows how many do) and is delivered when
the two next talk.

Only one of the two servers has to be able to reach the other. That one connects,
over SSH or over HTTPS. Captures going the other way wait until the connecting server
next comes by: once a minute over SSH, at once over HTTPS (see below).

**Over SSH.** Say home can reach work:

1. **On work**, in Settings › Linked servers: add a link named `home` and leave the
   SSH address empty. You get a token; copy it.
2. **On home**, Settings shows "This server's key". On the work host, put that line
   into `~/.ssh/authorized_keys` of an account that may open the port gtd listens on,
   and nothing else:

   ```
   restrict,port-forwarding,permitopen="127.0.0.1:8640" ssh-ed25519 AAAA… gtd-link-home
   ```
3. **On home**, add a link named `work` with the SSH address (`account@work-host`),
   where gtd listens on that host (`127.0.0.1:8640`) and the token from step 1.
   Home connects at once: the link then shows the host key's fingerprint, which you
   can compare with `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on work.

From then on home accepts only that host key, and the token lets home do two things
on work: capture into your inbox, and collect what you sent to `home` there. Links
are between two servers and one person; nothing is passed on to a third server.

**Over HTTPS**, for when SSH is not possible, for example from behind a company
proxy. Say home can reach `https://work.example/gtd/`:

1. **On work**, add a link named `home` and leave the address empty. You get a code
   (`gtd_…~…`); copy all of it.
2. **On home**, add a link named `work` with the address `https://work.example/gtd/`
   and the code.

The connection does not rely on TLS for secrecy, because a proxy may open TLS.
Every request and every answer is sealed between the two gtd servers themselves
(public-key encryption, NaCl box), token included, and sent as one binary POST to
`/gtd/api/v1/link`. The code you carry by hand contains work's public key, so
nothing in between can pose as work, read a capture, or change one unnoticed: a
changed message is refused and the capture stays in the outbox. What a proxy still
sees is that the two servers talk, when, and how much. Each server's key is in its
data directory (`link_box.key`); keep it as private as the database.

The code belongs to the first server that uses it. Work remembers home's key and from
then on accepts the code only from home, and only sealed; Settings shows the key's
fingerprint on both sides, so you can compare them. A code that leaks later is of no
use to anyone else. If home is set up anew (new key), remove the link on work and
add it again.

A link that this server connects to can be paused in Settings. While paused it makes
no connection and no check; what you send to it waits until you resume.

A proxy is taken from the environment of the daemon (`HTTPS_PROXY`, `NO_PROXY`).

Over HTTPS the connecting server also holds a WebSocket open to the other one, on
which that one says when something waits; it is then collected at once. No data
travels on it. For this, nginx in front of the reachable server must pass
WebSockets on for one path:

```
location /gtd/api/v1/outbox/wake {
    proxy_pass http://127.0.0.1:8640;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_read_timeout 120s;
}
```

Where a WebSocket cannot be opened, nothing is lost: captures are collected within
a minute, as over SSH.

**Mail in.** `gtd capture-mail` reads one message on stdin and captures it: the
subject becomes the title, then sender, date and text, with the attachments attached.
gtd runs no mail service; your mail server hands the message over. With Postfix, an
address that pipes into it (`/etc/aliases`, then `newaliases`):

```
gtd-robin: "|/usr/local/bin/gtd capture-mail -server http://127.0.0.1:8640 -token-file /etc/gtd/robin.token"
```

Forward a mail to that address and it is in your inbox. The token file must be
readable by the user the mail server runs the pipe as, and by nobody else.

Tests: `go test -race ./...`. `go test ./store -update` rewrites the golden export after an
intended format change.
