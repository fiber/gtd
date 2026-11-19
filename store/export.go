package store

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"gopkg.in/yaml.v3"

	"github.com/fiber/gtd/core"
)

// Manifest is manifest.json of an export.
type Manifest struct {
	Server Server `json:"server"`
	Schema int    `json:"schema"`
	// ExportedAt is the time of the newest exported op (the server's creation
	// time if there is none), so the same data always exports to the same bytes.
	ExportedAt time.Time       `json:"exported_at"`
	Partial    bool            `json:"partial,omitempty"` // --space export: not restorable
	Spaces     []ManifestSpace `json:"spaces"`
	Users      int             `json:"users"`
	Blobs      int             `json:"blobs"`
}

type ManifestSpace struct {
	ID       string `json:"id"`
	Items    int    `json:"items"`
	Comments int    `json:"comments"`
	Ops      int    `json:"ops"`
	Seq      uint64 `json:"seq"`
}

// itemFM is the frontmatter of an exported item: the fields of core.Item in the
// order of SPEC §2, without the body. Its JSON names equal core.Item's, so the
// two convert through JSON. Times are strings so that YAML cannot reformat them.
type itemFM struct {
	ID          string            `yaml:"id" json:"id"`
	Space       string            `yaml:"space" json:"space"`
	Kind        string            `yaml:"kind" json:"kind"`
	Title       string            `yaml:"title,omitempty" json:"title,omitempty"`
	State       string            `yaml:"state" json:"state"`
	Rev         uint64            `yaml:"rev" json:"rev"`
	Created     string            `yaml:"created" json:"created"`
	Updated     string            `yaml:"updated" json:"updated"`
	UpdatedBy   string            `yaml:"updated_by" json:"updated_by"`
	Original    string            `yaml:"original,omitempty" json:"original,omitempty"`
	Source      core.Source       `yaml:"source,omitempty" json:"source,omitzero"`
	AIGenerated bool              `yaml:"ai_generated,omitempty" json:"ai_generated,omitempty"`
	Area        string            `yaml:"area,omitempty" json:"area,omitempty"`
	Project     string            `yaml:"project,omitempty" json:"project,omitempty"`
	Parent      string            `yaml:"parent,omitempty" json:"parent,omitempty"`
	Context     string            `yaml:"context,omitempty" json:"context,omitempty"`
	Owner       string            `yaml:"owner,omitempty" json:"owner,omitempty"`
	DelegatedBy string            `yaml:"delegated_by,omitempty" json:"delegated_by,omitempty"`
	WaitingOn   string            `yaml:"waiting_on,omitempty" json:"waiting_on,omitempty"`
	BlockedBy   []string          `yaml:"blocked_by,omitempty" json:"blocked_by,omitempty"`
	DeferUntil  string            `yaml:"defer_until,omitempty" json:"defer_until,omitempty"`
	Target      string            `yaml:"target,omitempty" json:"target,omitempty"`
	Due         string            `yaml:"due,omitempty" json:"due,omitempty"`
	Energy      string            `yaml:"energy,omitempty" json:"energy,omitempty"`
	Time        string            `yaml:"time,omitempty" json:"time,omitempty"`
	Repeat      string            `yaml:"repeat,omitempty" json:"repeat,omitempty"`
	Template    string            `yaml:"template,omitempty" json:"template,omitempty"`
	Fields      map[string]string `yaml:"fields,omitempty" json:"fields,omitempty"`
	Attachments []core.Attachment `yaml:"attachments,omitempty" json:"attachments,omitempty"`
	DerivedFrom []string          `yaml:"derived_from,omitempty" json:"derived_from,omitempty"`
	Generated   bool              `yaml:"generated,omitempty" json:"generated,omitempty"`
	Conflict    any               `yaml:"conflict,omitempty" json:"conflict,omitempty"`
	Imported    map[string]any    `yaml:"imported,omitempty" json:"imported,omitempty"`
}

const fmSep = "---\n"

// fmOrder is the key order of the frontmatter (SPEC §2) and of the maps nested
// in it. Keys not listed (fields, imported) follow in alphabetical order.
var fmOrder = func() map[string]int {
	keys := strings.Fields(`id space kind title state rev created updated updated_by original source ai_generated
		area project parent context owner delegated_by waiting_on blocked_by defer_until target due energy time repeat
		template fields attachments derived_from generated conflict imported
		via ref by sha256 name mime size`)
	m := map[string]int{}
	for i, k := range keys {
		m[k] = i
	}
	return m
}()

// itemToMD renders an item as frontmatter plus the body, verbatim, up to the
// end of the file.
func itemToMD(it *core.Item) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(mustJSON(it)))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("item %s: %v", it.ID, err)
	}
	delete(m, "v")
	delete(m, "body")
	y, err := yaml.Marshal(yamlNode(m, true))
	if err != nil {
		return nil, fmt.Errorf("item %s: %v", it.ID, err)
	}
	return slices.Concat([]byte(fmSep), y, []byte(fmSep), []byte(it.Body)), nil
}

// yamlNode builds the YAML tree by hand. yaml.v3's own choice of block scalars
// does not round-trip every string (leading blank lines, "---", CR), so any
// string that is not plain single-line printable text is double-quoted, which
// escapes everything.
func yamlNode(v any, ordered bool) *yaml.Node {
	switch x := v.(type) {
	case map[string]any:
		keys := slices.Sorted(maps.Keys(x))
		if ordered {
			slices.SortStableFunc(keys, func(a, b string) int {
				ra, oka := fmOrder[a]
				rb, okb := fmOrder[b]
				switch {
				case oka && okb:
					return ra - rb
				case oka:
					return -1
				case okb:
					return 1
				}
				return 0
			})
		}
		n := &yaml.Node{Kind: yaml.MappingNode}
		for _, k := range keys {
			// The values of fields and imported are user data: alphabetical keys.
			n.Content = append(n.Content, yamlNode(k, false), yamlNode(x[k], ordered && k != "fields" && k != "imported" && k != "conflict"))
		}
		return n
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode}
		for _, e := range x {
			n.Content = append(n.Content, yamlNode(e, ordered))
		}
		return n
	case string:
		n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: x}
		for _, r := range x {
			if r < 0x20 || r == 0x7f || r == 0x85 || r == 0xa0 || r == 0x2028 || r == 0x2029 || r == 0xfeff {
				n.Style = yaml.DoubleQuotedStyle
				break
			}
		}
		return n
	case json.Number:
		tag := "!!int"
		if strings.ContainsAny(x.String(), ".eE") {
			tag = "!!float"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: x.String()}
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(x)}
	default:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	}
}

// mdToItem is the inverse of itemToMD.
func mdToItem(data []byte) (*core.Item, error) {
	rest, ok := bytes.CutPrefix(data, []byte(fmSep))
	end := bytes.Index(rest, []byte("\n"+fmSep))
	if !ok || end < 0 {
		return nil, fmt.Errorf("no frontmatter")
	}
	var fm itemFM
	dec := yaml.NewDecoder(bytes.NewReader(rest[:end+1]))
	dec.KnownFields(true)
	if err := dec.Decode(&fm); err != nil {
		return nil, err
	}
	it := &core.Item{}
	if err := json.Unmarshal(mustJSON(fm), it); err != nil {
		return nil, err
	}
	it.V = core.Version
	it.Body = string(rest[end+1+len(fmSep):])
	return it, nil
}

// spaceYAML is space.yaml of an export.
type spaceYAML struct {
	ID        string          `yaml:"id"`
	Name      string          `yaml:"name"`
	Kind      string          `yaml:"kind"`
	Created   string          `yaml:"created"`
	Members   []string        `yaml:"members"`
	People    []string        `yaml:"people,omitempty"`
	Contexts  []string        `yaml:"contexts,omitempty"`
	Templates []core.Template `yaml:"templates,omitempty"`
}

// Export writes a lossless export (STORAGE §8) as a tar stream. With no spaces
// given it exports everything. With spaces it exports only those, plus their
// members' user records, and marks the export partial. It reads from a Pebble
// snapshot, so it is consistent while the daemon keeps writing. Entries have a
// fixed order and no timestamps: the same data gives the same bytes.
func (s *Store) Export(w io.Writer, only []string) error {
	snap := s.db.NewSnapshot()
	defer snap.Close()
	tw := tar.NewWriter(w)
	put := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}

	man := Manifest{Server: s.server, Schema: core.Version, ExportedAt: s.server.Created, Partial: len(only) > 0}
	spaces := slices.Compact(slices.Sorted(slices.Values(only)))
	if !man.Partial {
		var err error
		if spaces, err = spaceIDs(snap); err != nil {
			return err
		}
	}
	recs := map[string]*core.Space{}
	members := map[string]bool{}
	for _, id := range spaces {
		rec := &core.Space{}
		if err := getJSON(snap, spaceKey(id), rec); err != nil {
			return err
		}
		recs[id] = rec
		for _, m := range rec.Members {
			members[m] = true
		}
	}

	// users.json: no password hashes. Tokens and sessions live under other keys.
	users, err := userIDs(snap)
	if err != nil {
		return err
	}
	list := []*core.User{}
	for _, id := range users {
		if man.Partial && !members[id] {
			continue
		}
		u := &core.User{}
		if err := getJSON(snap, userKey(id), u); err != nil {
			return err
		}
		u.PasswordHash = ""
		list = append(list, u)
	}
	man.Users = len(list)
	if err := put("users.json", indentJSON(list)); err != nil {
		return err
	}
	if !man.Partial {
		for _, u := range list {
			var buf bytes.Buffer
			err := scan(snap, notifPrefix(u.ID), func(_ string, v []byte) error {
				buf.Write(v)
				buf.WriteByte('\n')
				return nil
			})
			if err != nil {
				return err
			}
			if buf.Len() > 0 {
				if err := put("users/"+u.ID+"/notifications.jsonl", buf.Bytes()); err != nil {
					return err
				}
			}
		}
	}

	blobs := map[string]bool{}
	for _, id := range spaces {
		ms, err := exportSpace(snap, recs[id], put, blobs, &man.ExportedAt)
		if err != nil {
			return fmt.Errorf("export space %q: %w", id, err)
		}
		man.Spaces = append(man.Spaces, ms)
	}

	man.Blobs = len(blobs)
	for _, sha := range slices.Sorted(maps.Keys(blobs)) {
		if err := exportBlob(tw, s.BlobPath(sha), sha); err != nil {
			return err
		}
	}
	man.ExportedAt = man.ExportedAt.UTC()
	if err := put("manifest.json", indentJSON(man)); err != nil {
		return err
	}
	return tw.Close()
}

func exportSpace(snap pebble.Reader, rec *core.Space, put func(string, []byte) error, blobs map[string]bool, newest *time.Time) (ManifestSpace, error) {
	ms := ManifestSpace{ID: rec.ID}
	dir := "spaces/" + rec.ID + "/"
	y, err := yaml.Marshal(spaceYAML{ID: rec.ID, Name: rec.Name, Kind: rec.Kind,
		Created: rec.Created.Format(time.RFC3339Nano), Members: rec.Members, People: rec.People, Contexts: rec.Contexts, Templates: rec.Templates})
	if err != nil {
		return ms, err
	}
	if err := put(dir+"space.yaml", y); err != nil {
		return ms, err
	}

	err = scan(snap, itemPrefix(rec.ID), func(key string, v []byte) error {
		it := &core.Item{}
		if err := json.Unmarshal(v, it); err != nil {
			return fmt.Errorf("key %q: %v", key, err)
		}
		for _, a := range it.Attachments {
			blobs[a.SHA256] = true
		}
		md, err := itemToMD(it)
		if err != nil {
			return err
		}
		ms.Items++
		return put(dir+"items/"+it.ID+".md", md)
	})
	if err != nil {
		return ms, err
	}

	// Comments: one .jsonl per item. Keys are sorted by item, then comment ID.
	var (
		cur string
		buf bytes.Buffer
	)
	flush := func() error {
		if buf.Len() == 0 {
			return nil
		}
		defer buf.Reset()
		return put(dir+"comments/"+cur+".jsonl", buf.Bytes())
	}
	p := commentPrefix(rec.ID)
	err = scan(snap, p, func(key string, v []byte) error {
		item, _, _ := strings.Cut(key[len(p):], "/")
		if item != cur {
			if err := flush(); err != nil {
				return err
			}
			cur = item
		}
		var c core.Comment
		if err := json.Unmarshal(v, &c); err != nil {
			return fmt.Errorf("key %q: %v", key, err)
		}
		for _, a := range c.Attachments {
			blobs[a.SHA256] = true
		}
		buf.Write(v)
		buf.WriteByte('\n')
		ms.Comments++
		return nil
	})
	if err == nil {
		err = flush()
	}
	if err != nil {
		return ms, err
	}

	var ops bytes.Buffer
	err = scan(snap, opPrefix(rec.ID), func(key string, v []byte) error {
		var op struct {
			Seq uint64
			TS  time.Time
		}
		if err := json.Unmarshal(v, &op); err != nil {
			return fmt.Errorf("key %q: %v", key, err)
		}
		if op.TS.After(*newest) {
			*newest = op.TS
		}
		ops.Write(v)
		ops.WriteByte('\n')
		ms.Ops++
		ms.Seq = op.Seq
		return nil
	})
	if err != nil {
		return ms, err
	}
	return ms, put(dir+"ops.jsonl", ops.Bytes())
}

func exportBlob(tw *tar.Writer, path, sha string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("blob %s is referenced but missing: %w", sha, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: "blobs/" + sha[:2] + "/" + sha, Mode: 0o644, Size: fi.Size()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func indentJSON(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}

// exportEntries are the top-level names an export consists of.
var exportEntries = []string{"manifest.json", "users.json", "users", "spaces", "blobs"}

// Untar unpacks an export stream into dir. It first removes a previous export
// there (and nothing else, so a .git directory survives), which makes the
// result independent of what was exported before.
func Untar(r io.Reader, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, e := range exportEntries {
		if err := os.RemoveAll(filepath.Join(dir, e)); err != nil {
			return err
		}
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read export stream: %w", err)
		}
		if !filepath.IsLocal(h.Name) || !slices.Contains(exportEntries, strings.Split(h.Name, "/")[0]) {
			return fmt.Errorf("export stream has unexpected entry %q", h.Name)
		}
		path := filepath.Join(dir, filepath.FromSlash(h.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, tr)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
}
