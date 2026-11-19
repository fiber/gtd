# AI_CONTRACT.md — Embeddings, similarity, clustering, generation

Everything here is optional at runtime (invariants 2 and 7). If a backend is down,
jobs back off and retry, and the UI works without suggestions. An indicator shows
"AI: idle / 12 queued / offline".

## 1. Job model
- One in-process queue with separate worker pools (default: embedding 2,
  generation 1). Jobs are keyed by `(type, item, content hash)`. A newer hash for the
  same item supersedes queued older ones.
- Results are cached in Pebble `x/ai/<type>/<hash+prompt_version>`, so identical input
  is never recomputed.
- Timeouts per config, exponential backoff on connection errors.
- **Context is always space-scoped** (invariant 4): a job only ever receives items
  from the space of the item it's about, or for user-level features (Clarify step 2,
  search), from spaces the acting user belongs to.

## 2. Embeddings

### 2.1 Backends (`embed.Embedder`, the only interface in `embed/`)
```go
type Embedder interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    Model() string
    Dim() int
}
```
Three adapters (D-24), chosen per server in `config.yaml`:
- **internal**: in-process through `github.com/fiber/ai/models/gemma`. `gemma.Load(dir)`
  reads the EmbeddingGemma files from `embed.model_dir` (config, tokenizer, safetensors,
  1.2 GB; about 1.6 s to load on an M2). `Embed(texts, gemma.Prompt("document"))` for
  stored items, `gemma.Prompt("query")` for searches. One forward pass at a time. The
  model reads at most 2048 tokens. gtd never downloads model files.
- **gtt-nora**: the NIS embedding service (DECISIONS Q-2). The server does not add task
  prompts: the adapter wraps the texts (`"title: none | text: "` for documents,
  `"task: search result | query: "` for queries), respects the limits the server
  announces, and sends one request at a time.
- **ollama**: `POST <embed.url>/api/embed` with `{"model", "input": [...]}`. Ollama does
  not add task prompts either (measured): the adapter wraps the texts the same way.
- Not used: atlas vector indexes (gtd's texts stay in Pebble).

Rules, taken from the NIS embedding brief:
- **A vector's identity is the adapter, the model identity (file hashes, the server's
  identity, or Ollama's digest) and the prompt set.** It is stored with the vectors. Vectors of different identities are never compared: a changed identity
  invalidates `x/vec/` and triggers a background re-embed.
- **No answers from a partial index.** While a space's vectors are being built or
  rebuilt, similarity features for it say "building, N of M" instead of ranking what
  exists so far.
- **Fall back visibly.** When the embedder cannot answer, search uses the text match
  and says so; suggestions are hidden, not replaced by guesses.
- The model is `embeddinggemma`, 768 dimensions.

### 2.2 Input text
`title + "\n\n" + body` with Markdown stripped, link tokens replaced by the linked
item's title (if visible in the same space), image tokens by the attachment name,
mention tokens by the display name. Then template field values as `key: value`
lines, `imported.folder` and `imported.tags` (if any), and extracted PDF text. Chunks
are ~1,500 characters with 200 overlap. **Item vector = L2-normalized mean of its chunk
vectors.** Comments are not embedded in v1.

### 2.3 Storage and search
- `x/vec/<space>/<id>` → `{hash, vec}`. At startup, load into one contiguous
  `[]float32` matrix per space plus an ID slice, updated in place.
- Search = query vector(s) × matrix with `cluster.Similarities` from
  `github.com/fiber/ai` (SGEMM on its SIMD kernels), then top-k with filters (kind,
  state, exclude self), across the allowed spaces only.
- Target: a top-k query over 50k × 768 in under 5 ms (report it).

### 2.4 Uses
| Feature | Query | Candidates | k / threshold |
|---|---|---|---|
| Clarify 1: similar and duplicate | capture vector | all kinds, user's spaces | 3; dup ≥ `dup_threshold` |
| Clarify 2: which space | capture vector | per-space centroid of recent items | ranked |
| Clarify 3/4: project, parent, area | capture vector | project vectors, areas | 3 |
| Project "might belong here" | project vector | unsorted or unfiled notes, same space | 5; ≥ `suggest_threshold` |
| Related sidebar | item vector | all kinds, user's spaces | 5 |
| Search (semantic part) | query vector | user's spaces | 20 |

**Project vector** = normalized mean of the project's own vector and the vectors of its
notes and actions (direct only), recomputed when membership changes.

Calibration: the default thresholds are placeholders. In M5 the agent reports score
distributions on the real imported vault (incl. German↔English pairs, since
embeddinggemma is multilingual) and proposes values.

### 2.5 Clustering (unsorted sweep)
Spherical k-means over `note`/`unsorted` vectors per space: `k = ceil(n/15)`,
k-means++ with a fixed seed, at most 50 iterations (`cluster.KMeans` from fiber/ai if it
fits these rules, else our own loop on `cluster.Similarities`).
Cohesion = mean cosine to the centroid. Clusters with cohesion under 0.45 or fewer than
2 members go to "loose notes", shown last. Order: cohesion × size, descending.
Deterministic. Recomputed per sweep session.

## 3. Generation
Provider per server (D-25, Q-1): local at work, local or cloud (Gemini, Anthropic) at
home. Chat-style request with
JSON-schema-constrained output if the backend supports it, else instruction plus
strict parse. Temperature 0.

Rules for every task:
- Prompts are `ai/prompts/<task>.v<N>.txt` (`embed.FS`). The prompt version is part
  of the cache key.
- The model only receives the item content plus candidate lists the code supplies,
  and may only reference IDs from those lists. Any other ID makes the output invalid.
- Validate schema plus semantic checks. On failure, retry once, then "no suggestion".
- **Language:** cleaned text and next actions stay in the input's language.
  Tool-generated text (cluster labels, digest headings, `@ai` replies) uses the
  requesting user's UI language.

### 3.1 `clarify`
Input: `original`, attachment names, today's date, the user's spaces `{id,name}`,
contexts (allowed for the candidate space), the top-5 projects `{id,title}`, members
and people `{id,name}`. Output:
```json
{
  "title": "≤ 90 chars, verb-first if actionable",
  "cleaned_body": "meaning-preserving cleanup, may be empty",
  "space_id": "one of spaces[].id",
  "actionable": true,
  "multi_step": false,
  "project_id": "one of projects[].id | \"new\" | \"\"",
  "project_title": "outcome phrase if new",
  "next_action": "verb-first, ≤ 90 chars, \"\" if not actionable",
  "two_minutes": false,
  "owner": "me | anyone | one of members[].id | person:<name> | \"\"",
  "context": "one of contexts[] | \"\"",
  "defer_hint": "none|tomorrow|next_week|two_weeks|one_month",
  "target_hint": "none|this_week|next_week|this_month|next_month",
  "non_actionable_as": "trash|someday|reference|\"\"",
  "followups": ["≤ 5 verb-first follow-up actions, only if multi_step"]
}
```
Checks: `next_action` is empty iff not actionable; IDs and contexts are from the
lists. The fields map one-to-one to wizard preselections (SPEC §5.2).

### 3.2 `title` — restore a meaningful title
Input: current title, `imported.filename`, the first 2,000 characters of the body.
Output: `{"title": "≤ 90 chars", "changed": bool}`. The model is told that titles may
contain any characters and that old ones were bent for filesystem limits (e.g.
`NIS - BGP - Collector` → `NIS/BGP: Collector`).

### 3.3 `cluster_label`
Input: up to 30 members (title plus 300 characters each), the top-5 projects. Output:
`{"label": "≤ 60 chars", "summary": "one sentence", "project_id": "id|new|\"\"", "new_project_title": "…"}`.

### 3.4 `digest`
Input: all notes and actions of a project subtree or area (id, title, body,
truncated proportionally, with a truncation notice). Output:
```json
{"summary":"≤ 8 sentences",
 "decisions":[{"text":"…","sources":["id"]}],
 "open_questions":[{"text":"…","sources":["id"]}],
 "interfaces":[{"text":"…","sources":["id"]}],
 "contradictions":[{"text":"A says X, B says Y","sources":["idA","idB"]}]}
```
Every bullet cites at least 1 source (contradictions at least 2). It's rendered into
a note body with link tokens. More than ~60 inputs → map-reduce over clusters.

### 3.5 `todo_detect`
Deterministic first: `- [ ]` lines are candidates. The AI pass is optional:
`{"candidates":[{"text":"verb-first action","quote":"exact substring"}]}`, where
`quote` is verified as a substring. Shown as chips, never auto-created.

### 3.6 `thread_reply` (`@ai`, COLLAB.md §5)
Input: the item, thread (last 30 comments), project subtree summary, top-5 related
from the same space, the request comment. Output:
```json
{"reply":"markdown, ≤ 300 words",
 "proposals":[{"type":"create_action|add_followup|file_note|set_target",
               "title":"…","project_id":"…","after_id":"…","target":"YYYY-MM-DD","note_id":"…"}]}
```
IDs must come from the supplied context. Proposals become chips.

### 3.7 Image description (optional flag, M7+)
For a multimodal model only: a one-line description of images without text, used
**only** for embedding and search input (`x/`), never written into content.
