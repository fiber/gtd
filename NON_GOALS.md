# NON_GOALS.md — Do not build these (v1)

If a task seems to need one of these, stop and ask.

**Storage and infrastructure**
- SQL, SQLite, ORMs, any second database. Pebble is the only one.
- Search engines (Bleve etc.), ANN or vector indexes, vector databases
- Git as storage or a sync protocol. Git appears only as `git merge-file` and as an
  optional destination for nightly exports.
- Cloud AI providers or any outbound call except the configured embedding and
  generation backends
- Op-log compaction, sharding, replication, HA

**Collaboration**
- Sharing between servers or federation (work ↔ home, Sven's server ↔ someone else's)
- Roles beyond "member" (no read-only, no per-item ACLs)
- Global chat, channels, direct messages, presence, typing indicators, real-time
  co-editing, CRDTs
- Push notifications, email notifications
- Editing comments (delete-and-repost is enough)

**Product**
- Native apps (the PWA covers it). A TUI is later, not v1.
- Calendar integration. `due` and `target` are dates, nothing more.
- Gantt charts, timeline views, critical-path or resource planning
- Recurring actions or checklists
- Horizons above areas as special objects (goals and vision are plain notes)
- Free-form tags or a tag UI
- Obsidian compatibility, graph view, canvas, plugins, themes
- Note transclusion
- WYSIWYG or rich-text editing (Markdown textarea plus preview)
- OCR, PDF annotation, HEIC decoding
- Offline editing beyond OFFLINE.md (no local server or replica)
- Import from anything other than Obsidian
- Trading-specific logic in the core (templates and service users cover it). gtd
  never starts backtests in nuro (v2 at the earliest).
- Gamification, statistics dashboards, streaks

**Engineering**
- Frontend build tooling (npm, bundlers, TypeScript, CSS frameworks)
- Any frontend framework or library besides the vendored Lit bundle. No
  state-management library, no router library, no htmx, no server-rendered
  `html/template` pages (the app shell is the only HTML the server sends).
- GTD logic in JS: no state, dependency, wizard-flow or permission rules in the
  client, and no client-side Markdown rendering
- DI containers, plugin systems, generic repository or service layers
- Flags, options or extension points not required by a milestone
