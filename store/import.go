package store

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/fiber/gtd/core"
)

// Import of notes from another system (IMPORT.md). The importer reads the
// files; this is the write side: every note becomes a create op (or, when its
// file changed, a patch op), `via: import`, and the path→item map is written
// in the batch of those ops.

// ImportEntry says which item a file was imported as, and from which content.
type ImportEntry struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
}

// ImportNote is one file to import. Item carries the note as it should be
// created; its ID is the entry's ID if the path was imported before.
type ImportNote struct {
	Path string    `json:"path"` // relative to the vault
	Hash string    `json:"hash"` // of the file content
	Item core.Item `json:"item"`
}

// ImportResult counts what an import batch did.
type ImportResult struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
}

func importPrefix(space string) string { return "x/import/" + space + "/" }
func importKey(space, path string) []byte {
	sum := sha1.Sum([]byte(path))
	return []byte(importPrefix(space) + hex.EncodeToString(sum[:]))
}

// ImportEntries returns the path map of a space, keyed by the hex SHA-1 of
// the path.
func (v *View) ImportEntries(spaceID string) (map[string]ImportEntry, error) {
	if err := v.check(spaceID); err != nil {
		return nil, err
	}
	out := map[string]ImportEntry{}
	err := scan(v.s.db, importPrefix(spaceID), func(key string, val []byte) error {
		name := key[len(importPrefix(spaceID)):]
		if name == "report" {
			return nil
		}
		var e ImportEntry
		if err := json.Unmarshal(val, &e); err != nil {
			return fmt.Errorf("key %q: %v", key, err)
		}
		out[name] = e
		return nil
	})
	return out, err
}

// ImportReport returns the stored report of the last import into a space.
func (v *View) ImportReport(spaceID string) (json.RawMessage, error) {
	if err := v.check(spaceID); err != nil {
		return nil, err
	}
	val, closer, err := v.s.db.Get([]byte(importPrefix(spaceID) + "report"))
	if err != nil {
		return nil, nil
	}
	defer closer.Close()
	return append(json.RawMessage(nil), val...), nil
}

// ImportMark logs the marker op that starts or ends an import run. The end
// marker stores the run's report.
func (v *View) ImportMark(spaceID string, m core.ImportMarker, report json.RawMessage) error {
	v.s.mu.RLock()
	sp, err := v.space(spaceID)
	v.s.mu.RUnlock()
	if err != nil {
		return err
	}
	_, err = v.s.applyWith(sp, spaceID, v.user, []core.Op{{Via: "import", Type: core.OpImport, Payload: mustJSON(m)}}, func(b *pebble.Batch) {
		if len(report) > 0 {
			b.Set([]byte(importPrefix(spaceID)+"report"), report, nil)
		}
	})
	return err
}

// Import writes a batch of notes into a space as the view's user. A path that
// was not imported before is created as an unsorted note. A path whose file
// changed gets its title, body and foreign metadata updated, and
// imported.reimported_at set. An unchanged file is skipped. Running the same
// import twice therefore writes nothing the second time.
func (v *View) Import(spaceID string, notes []ImportNote) (ImportResult, error) {
	var res ImportResult
	v.s.mu.RLock()
	sp, err := v.space(spaceID)
	v.s.mu.RUnlock()
	if err != nil {
		return res, err
	}
	entries, err := v.ImportEntries(spaceID)
	if err != nil {
		return res, err
	}
	now := v.s.Now()
	var ops []core.Op
	put := map[string]ImportEntry{}
	v.s.mu.RLock()
	for _, n := range notes {
		key := string(importKey(spaceID, n.Path))
		name := key[len(importPrefix(spaceID)):]
		it := n.Item
		it.Kind, it.Source.Via, it.Owner = core.KindNote, "import", v.user
		if e, ok := entries[name]; ok {
			it.ID = e.ID
			if e.Hash == n.Hash {
				res.Unchanged++
				continue
			}
		}
		if it.ID == "" {
			v.s.mu.RUnlock()
			return res, fmt.Errorf("%w: import of %q: item id %q", core.ErrInvalid, n.Path, it.ID)
		}
		put[key] = ImportEntry{ID: it.ID, Hash: n.Hash}
		cur := sp.items[it.ID]
		if cur == nil {
			it.State = core.StUnsorted
			ops = append(ops, core.Op{Via: "import", Type: core.OpCreate, Payload: mustJSON(it)})
			res.Created++
			continue
		}
		// Known item: the file changed, or the path map was lost (after a
		// restore from an export, which does not carry it).
		op := core.Op{Via: "import", Type: core.OpPatch, Item: cur.ID, BaseRev: cur.Rev}
		change := func(field string, old, new any) {
			if !reflect.DeepEqual(old, new) {
				op.Changes = append(op.Changes, core.FieldChange{Field: field, Old: mustJSON(old), New: mustJSON(new)})
			}
		}
		change("title", cur.Title, it.Title)
		change("body", cur.Body, it.Body)
		if len(op.Changes) == 0 {
			res.Unchanged++
			continue
		}
		imported := maps.Clone(it.Imported)
		if imported == nil {
			imported = map[string]any{}
		}
		imported["reimported_at"] = now.UTC().Format(time.RFC3339)
		change("imported", cur.Imported, imported)
		ops = append(ops, op)
		have := map[string]bool{}
		for _, a := range cur.Attachments {
			have[a.SHA256] = true
		}
		for _, a := range it.Attachments {
			if !have[a.SHA256] {
				have[a.SHA256] = true
				ops = append(ops, core.Op{Via: "import", Type: core.OpAttach, Item: cur.ID, Payload: mustJSON(a)})
			}
		}
		res.Updated++
	}
	v.s.mu.RUnlock()
	_, err = v.s.applyWith(sp, spaceID, v.user, ops, func(b *pebble.Batch) {
		for key, e := range put {
			b.Set([]byte(key), mustJSON(e), nil)
		}
	})
	if err != nil {
		return ImportResult{}, fmt.Errorf("import into space %q (first path %q): %w", spaceID, firstPath(notes), err)
	}
	return res, nil
}

func firstPath(notes []ImportNote) string {
	if len(notes) == 0 {
		return ""
	}
	return strings.TrimSpace(notes[0].Path)
}
