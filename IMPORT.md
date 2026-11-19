# IMPORT.md — Obsidian vault importer

Sven's vault has **thousands of notes**, organized messily. The import must be
safe, idempotent, and leave everything in the `unsorted` pool for the sweep
(SPEC §5.7). Nothing goes to the inbox (processing thousands of items one by one would
defeat it).

## 1. Command
`gtd import obsidian <vault-path> --space <space> --as <user> [--dry-run]`

Like every write, it runs as ops (`via: import`). It's batched (e.g. 500 items per
Pebble batch) and logged with a single `import` marker op at the start and the end.
`--dry-run` writes nothing and prints the report. The path→ID map is stored in
Pebble (`x/import/<space>/<sha1(path)>` → `{id, content_hash}`). Re-running skips
unchanged files. A changed file updates the body (op) and sets
`imported.reimported_at`.

## 2. Walk
All `*.md` except under `.obsidian/`, `.trash/` and dot directories. Non-Markdown
files are imported only when referenced. Unreferenced ones are listed in the report.

## 3. Per note
- `id`: a ULID whose time comes from frontmatter `created`/`date` if parseable, else
  the file mtime. `created` is the same time.
- `kind: note`, `state: unsorted`, `owner` and `updated_by` = the `--as` user.
- `title`: the first-line H1 if present (removed from the body), else the filename
  without `.md`. `imported.filename` and `imported.folder` (vault-relative directory)
  are kept.
- `original`: the raw file content (invariant 1).
- Foreign frontmatter → `imported` verbatim (incl. `tags`; there's no tag UI).
- `- [ ]` lines become `todo_detect` candidates (not converted).
- `source: {via: import, ref: "obsidian:<vault-relative path>"}`.

## 4. Links and embeds
| Obsidian | Becomes |
|---|---|
| `[[Name]]`, `[[Name\|alias]]` | `[[id]]`, `[[id\|alias]]` |
| `[[Name#Heading]]`, `[[Name#^block]]` | `[[id]]` (anchor dropped, counted) |
| `![[image.png]]`, `![](rel/path.png)` | blob ingest → `![[att:<sha>]]` plus `attachments` entry |
| `![[file.pdf]]`, `[x](rel/file.pdf)` | blob ingest → `[[att:<sha>]]` |
| `![[Other note]]` (transclusion) | `[[id]]` |
| external URLs | unchanged |

Resolution: exact relative path, then a case-insensitive unique basename. Ambiguous or
missing targets keep the original text and are listed in `imported.unresolved_links`.
Links are resolved in a second pass after all IDs are assigned.

## 5. After import
- Enqueue embeddings for all imported notes (background, progress, resumable via the
  content-hash cache, batched).
- AI title restoration is lazy (during the sweep), not bulk.
- Report the cluster stats and score distributions for threshold calibration (M5).

## 6. Report (stdout and `x/import/<space>/report`)
Notes found / imported / unchanged / empty · exact duplicate groups (imported, flagged
for the sweep) · attachments referenced / ingested / missing / bytes deduplicated · links
resolved / ambiguous / missing (top 50 with source) · unreferenced binaries ·
largest notes.

## 7. Acceptance
- A synthetic vault in `importer/testdata/` covering every link and embed form,
  ambiguous names, unicode titles, frontmatter variants and nested folders produces
  golden output (via `export`).
- Importing twice: no new items or ops beyond the marker ops.
- A dry run over 5,000 notes takes under 30 s (excluding embedding).
- A run on Sven's real vault plus a report.
