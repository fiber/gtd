// Package importer reads an Obsidian vault and turns its notes into gtd
// notes (IMPORT.md). It only reads: the caller writes the result through the
// store, so an import is ops like every other change.
package importer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"gopkg.in/yaml.v3"

	"github.com/fiber/gtd/core"
)

// Note is one Markdown file of the vault.
type Note struct {
	Path     string // relative to the vault, with forward slashes
	Hash     string // sha256 of the file
	Created  time.Time
	Title    string
	Body     string // without frontmatter and without the title heading; links as in the file until Resolve
	Original string // the file as it is
	Imported map[string]any
	Todos    int // open "- [ ]" lines

	ID          string // set by AssignIDs
	Attachments []core.Attachment
	Unresolved  []string
}

// Vault is what Read found.
type Vault struct {
	Notes  []*Note          // not empty ones, by path
	Empty  []string         // notes without any content
	Files  map[string]int64 // every other file: path → size
	Report Report

	fsys     fs.FS
	byPath   map[string]*Note   // lower-case path without .md
	byName   map[string][]*Note // lower-case basename without .md
	fileName map[string][]string
	used     map[string]bool // files referenced by a note
}

// Report is IMPORT.md §6.
type Report struct {
	Found     int `json:"notes_found"`
	Imported  int `json:"notes_imported"`
	Updated   int `json:"notes_updated"`
	Unchanged int `json:"notes_unchanged"`
	Empty     int `json:"notes_empty"`

	DuplicateGroups [][]string `json:"duplicate_groups,omitempty"` // notes with identical content

	AttachmentsReferenced int   `json:"attachments_referenced"`
	AttachmentsIngested   int   `json:"attachments_ingested"`
	AttachmentsMissing    int   `json:"attachments_missing"`
	BytesDeduplicated     int64 `json:"bytes_deduplicated"`

	LinksResolved  int      `json:"links_resolved"`
	LinksAmbiguous int      `json:"links_ambiguous"`
	LinksMissing   int      `json:"links_missing"`
	AnchorsDropped int      `json:"anchors_dropped"`
	Unresolved     []string `json:"unresolved,omitempty"` // "source: link", at most 50

	Todos        int      `json:"open_todo_lines"`
	BadMetadata  []string `json:"bad_frontmatter,omitempty"`
	Unreferenced []string `json:"unreferenced_files,omitempty"`
	Largest      []string `json:"largest_notes,omitempty"` // "path (bytes)", the 10 largest
}

var (
	reH1   = regexp.MustCompile(`^\s*# +(.+?)\s*#*\s*(?:\n|$)`)
	reTodo = regexp.MustCompile(`(?m)^\s*[-*+] \[ \] `)
)

// Read walks the vault: all *.md files except under dot directories
// (.obsidian, .trash). Every other file is remembered for links to it.
func Read(fsys fs.FS) (*Vault, error) {
	v := &Vault{Files: map[string]int64{}, fsys: fsys, byPath: map[string]*Note{}, byName: map[string][]*Note{},
		fileName: map[string][]string{}, used: map[string]bool{}}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !strings.EqualFold(path.Ext(p), ".md") {
			v.Files[p] = info.Size()
			v.fileName[strings.ToLower(path.Base(p))] = append(v.fileName[strings.ToLower(path.Base(p))], p)
			return nil
		}
		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		v.Report.Found++
		if len(bytes.TrimSpace(raw)) == 0 {
			v.Empty = append(v.Empty, p)
			return nil
		}
		n := v.parse(p, raw, info.ModTime())
		v.Notes = append(v.Notes, n)
		key := strings.ToLower(strings.TrimSuffix(p, path.Ext(p)))
		v.byPath[key] = n
		v.byName[path.Base(key)] = append(v.byName[path.Base(key)], n)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(v.Notes, func(i, j int) bool { return v.Notes[i].Path < v.Notes[j].Path })
	v.Report.Empty = len(v.Empty)

	// Exact duplicates are imported and flagged for the sweep.
	same := map[string][]string{}
	for _, n := range v.Notes {
		same[n.Hash] = append(same[n.Hash], n.Path)
		v.Report.Todos += n.Todos
	}
	for _, group := range same {
		if len(group) > 1 {
			v.Report.DuplicateGroups = append(v.Report.DuplicateGroups, group)
		}
	}
	sort.Slice(v.Report.DuplicateGroups, func(i, j int) bool { return v.Report.DuplicateGroups[i][0] < v.Report.DuplicateGroups[j][0] })
	bySize := slices.Clone(v.Notes)
	sort.SliceStable(bySize, func(i, j int) bool { return len(bySize[i].Original) > len(bySize[j].Original) })
	for _, n := range bySize[:min(10, len(bySize))] {
		v.Report.Largest = append(v.Report.Largest, fmt.Sprintf("%s (%d bytes)", n.Path, len(n.Original)))
	}
	return v, nil
}

func (v *Vault) parse(p string, raw []byte, mtime time.Time) *Note {
	sum := sha256.Sum256(raw)
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	n := &Note{Path: p, Hash: hex.EncodeToString(sum[:]), Original: string(raw), Created: mtime.UTC().Truncate(time.Second),
		Imported: map[string]any{"filename": path.Base(p)}}
	if dir := path.Dir(p); dir != "." {
		n.Imported["folder"] = dir
	}
	body := text
	if rest, ok := strings.CutPrefix(text, "---\n"); ok {
		if end := strings.Index(rest, "\n---"); end >= 0 && (len(rest) == end+4 || rest[end+4] == '\n') {
			var meta map[string]any
			if err := yaml.Unmarshal([]byte(rest[:end]), &meta); err != nil {
				// Not YAML: the note keeps the block in its body.
				v.Report.BadMetadata = append(v.Report.BadMetadata, p)
			} else {
				body = strings.TrimPrefix(rest[end+4:], "\n")
				for k, val := range meta {
					if k == "filename" || k == "folder" || k == "reimported_at" || k == "unresolved_links" {
						k = "frontmatter_" + k
					}
					n.Imported[k] = plain(val)
				}
				for _, k := range []string{"created", "date"} {
					if t, ok := parseTime(meta[k]); ok {
						n.Created = t
						break
					}
				}
			}
		}
	}
	n.Title = strings.TrimSuffix(path.Base(p), path.Ext(p))
	if m := reH1.FindStringSubmatch(body); m != nil {
		n.Title = strings.TrimSpace(m[1])
		body = body[len(m[0]):]
	}
	n.Body = strings.TrimRight(strings.TrimLeft(body, "\n"), " \t\n")
	n.Todos = len(reTodo.FindAllString(n.Body, -1))
	return n
}

// plain makes a YAML value JSON-compatible: times become strings, map keys
// become strings.
func plain(v any) any {
	switch x := v.(type) {
	case time.Time:
		if x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 && x.Nanosecond() == 0 {
			return x.Format(time.DateOnly)
		}
		return x.Format(time.RFC3339)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = plain(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[fmt.Sprint(k)] = plain(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = plain(val)
		}
		return out
	case float64:
		if x != x || x > 1e300 || x < -1e300 { // NaN and infinities are not JSON
			return fmt.Sprint(x)
		}
	}
	return v
}

func parseTime(v any) (time.Time, bool) {
	switch x := v.(type) {
	case time.Time:
		return x.UTC(), !x.IsZero()
	case string:
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04", time.DateOnly} {
			if t, err := time.Parse(layout, strings.TrimSpace(x)); err == nil {
				return t.UTC(), true
			}
		}
	}
	return time.Time{}, false
}

// AssignIDs gives every note its item ID: the one it was imported as before
// (known: path → id), else a ULID made of the note's time and of the space
// and path, so that the same file always gets the same ID.
func (v *Vault) AssignIDs(space string, known func(path string) string) {
	for _, n := range v.Notes {
		if n.ID = known(n.Path); n.ID != "" {
			continue
		}
		t := n.Created
		if t.Before(time.Unix(0, 0)) || t.UnixMilli() > int64(ulid.MaxTime()) {
			t = time.Unix(0, 0)
		}
		sum := sha256.Sum256([]byte(space + "\x00" + n.Path))
		id, _ := ulid.New(ulid.Timestamp(t), bytes.NewReader(sum[:]))
		n.ID = id.String()
	}
}

var (
	reFence = regexp.MustCompile("(?m)^(```|~~~)")
	// ![[x]] and [[x]], with optional #anchor and |alias; ![alt](dest) and [text](dest).
	reLink = regexp.MustCompile(`(!?)\[\[([^\]\[|#\n]*)(#[^\]\[|\n]*)?(?:\|([^\]\[\n]*))?\]\]|(!?)\[([^\]\[\n]*)\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"\n]*")?\s*\)`)
)

var imageExt = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".svg": "image/svg+xml", ".bmp": "image/bmp", ".heic": "image/heic", ".avif": "image/avif"}

func mimeOf(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if m, ok := imageExt[ext]; ok {
		return m
	}
	if ext == ".pdf" {
		return "application/pdf"
	}
	return "application/octet-stream"
}

// Resolve rewrites the links and embeds of every note (IMPORT.md §4), after
// AssignIDs. ingest stores a file of the vault as a blob and returns its
// hash; it is called once per referenced file.
func (v *Vault) Resolve(ingest func(path string) (sha string, err error)) {
	blobs := map[string]string{} // path → sha, "" if it could not be stored
	seenSHA := map[string]bool{}
	for _, n := range v.Notes {
		dir := path.Dir(n.Path)
		attach := func(file string) (string, bool) {
			sha, done := blobs[file]
			if !done {
				v.Report.AttachmentsReferenced++
				var err error
				if sha, err = ingest(file); err != nil || sha == "" {
					sha = ""
					v.Report.AttachmentsMissing++
					v.Report.Unresolved = appendMax(v.Report.Unresolved, fmt.Sprintf("%s: %s (%v)", n.Path, file, err))
				} else if seenSHA[sha] {
					v.Report.BytesDeduplicated += v.Files[file]
				} else {
					seenSHA[sha] = true
					v.Report.AttachmentsIngested++
				}
				blobs[file] = sha
			}
			if sha == "" {
				return "", false
			}
			v.used[file] = true
			if !slices.ContainsFunc(n.Attachments, func(a core.Attachment) bool { return a.SHA256 == sha }) {
				n.Attachments = append(n.Attachments, core.Attachment{SHA256: sha, Name: path.Base(file), MIME: mimeOf(file), Size: v.Files[file]})
			}
			return sha, true
		}
		keep := func(link, why string) string {
			n.Unresolved = append(n.Unresolved, link)
			if why == "ambiguous" {
				v.Report.LinksAmbiguous++
			} else {
				v.Report.LinksMissing++
			}
			v.Report.Unresolved = appendMax(v.Report.Unresolved, fmt.Sprintf("%s: %s (%s)", n.Path, link, why))
			return link
		}
		rewrite := func(m []string) string {
			whole := m[0]
			wiki := m[5] == "" && m[6] == "" && m[7] == ""
			embed, target, anchor, alias := m[1] == "!", strings.TrimSpace(m[2]), m[3], m[4]
			if !wiki {
				embed, alias = m[5] == "!", m[6]
				dest := m[7]
				if u, err := url.Parse(dest); err != nil || u.Scheme != "" || strings.HasPrefix(dest, "//") || strings.HasPrefix(dest, "#") {
					return whole // an external URL or an anchor in the same note
				}
				dest, anchor, _ = strings.Cut(dest, "#")
				if d, err := url.PathUnescape(dest); err == nil {
					dest = d
				}
				target = dest
				if anchor != "" {
					anchor = "#" + anchor
				}
			}
			if target == "" {
				return whole
			}
			isNote := path.Ext(target) == "" || strings.EqualFold(path.Ext(target), ".md")
			if isNote || wiki {
				if to, why := v.note(dir, target); to != nil {
					v.Report.LinksResolved++
					if anchor != "" {
						v.Report.AnchorsDropped++
					}
					if alias != "" && !embed {
						return "[[" + to.ID + "|" + strings.ReplaceAll(alias, "|", "/") + "]]"
					}
					return "[[" + to.ID + "]]"
				} else if why == "ambiguous" {
					return keep(whole, why)
				} else if isNote && wiki && path.Ext(target) == "" {
					// [[Name]] may also name a file without its extension: no.
					return keep(whole, "missing")
				}
			}
			file, why := v.file(dir, target)
			if file == "" {
				return keep(whole, why)
			}
			sha, ok := attach(file)
			if !ok {
				return whole
			}
			v.Report.LinksResolved++
			if _, image := imageExt[strings.ToLower(path.Ext(file))]; image && embed {
				return "![[att:" + sha + "]]"
			}
			return "[[att:" + sha + "]]"
		}
		// Links inside fenced code are text.
		var out strings.Builder
		rest, inFence := n.Body, false
		for {
			loc := reFence.FindStringIndex(rest)
			part := rest
			if loc != nil {
				part = rest[:loc[1]]
			}
			if inFence {
				out.WriteString(part)
			} else {
				out.WriteString(reLink.ReplaceAllStringFunc(part, func(s string) string { return rewrite(reLink.FindStringSubmatch(s)) }))
			}
			if loc == nil {
				break
			}
			rest, inFence = rest[loc[1]:], !inFence
		}
		n.Body = out.String()
		if len(n.Unresolved) > 0 {
			n.Imported["unresolved_links"] = n.Unresolved
		}
	}
	for file := range v.Files {
		if !v.used[file] {
			v.Report.Unreferenced = append(v.Report.Unreferenced, file)
		}
	}
	sort.Strings(v.Report.Unreferenced)
}

func appendMax(list []string, s string) []string {
	if len(list) < 50 {
		list = append(list, s)
	}
	return list
}

// note resolves a link target to a note: the exact path relative to the
// linking note or to the vault, then a unique basename, ignoring case.
func (v *Vault) note(dir, target string) (*Note, string) {
	t := strings.ToLower(strings.TrimSuffix(target, path.Ext(target)))
	if !strings.EqualFold(path.Ext(target), ".md") {
		t = strings.ToLower(target)
	}
	for _, p := range []string{path.Join(strings.ToLower(dir), t), path.Clean(t)} {
		if n := v.byPath[strings.TrimPrefix(p, "/")]; n != nil {
			return n, ""
		}
	}
	switch c := v.byName[path.Base(t)]; {
	case len(c) == 1 && !strings.Contains(t, "/"):
		return c[0], ""
	case len(c) > 1 && !strings.Contains(t, "/"):
		return nil, "ambiguous"
	}
	return nil, "missing"
}

// file resolves a link target to another file of the vault, the same way.
func (v *Vault) file(dir, target string) (string, string) {
	for _, p := range []string{path.Join(dir, target), path.Clean(strings.TrimPrefix(target, "/"))} {
		if _, ok := v.Files[p]; ok {
			return p, ""
		}
	}
	switch c := v.fileName[strings.ToLower(path.Base(target))]; {
	case len(c) == 1:
		return c[0], ""
	case len(c) > 1:
		return "", "ambiguous"
	}
	return "", "missing"
}

// Item returns the note as the item to create.
func (n *Note) Item() core.Item {
	return core.Item{ID: n.ID, Kind: core.KindNote, State: core.StUnsorted, Title: n.Title, Body: n.Body, Original: n.Original,
		Created: n.Created, Imported: n.Imported, Attachments: n.Attachments,
		Source: core.Source{Via: "import", Ref: "obsidian:" + n.Path}}
}

// Hash256 returns the sha256 of a file of the vault, for a dry run.
func (v *Vault) Hash256(file string) (string, error) {
	raw, err := fs.ReadFile(v.fsys, file)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
