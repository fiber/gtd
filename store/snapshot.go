package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/fiber/gtd/core"
)

// SnapshotManifest is <data_dir>/snapshots/<name>/manifest.json (STORAGE §9).
// It is written last: a snapshot directory without it is incomplete.
type SnapshotManifest struct {
	Name     string            `json:"name"`
	Label    string            `json:"label,omitempty"`
	Created  time.Time         `json:"created"`
	ServerID string            `json:"server_id"`
	Schema   int               `json:"schema"`
	Seqs     map[string]uint64 `json:"seqs"`  // last op seq per space
	Blobs    []BlobRef         `json:"blobs"` // every blob the checkpoint references
}

type BlobRef struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Keep is the snapshot retention: the newest snapshot of each of the last N
// hours, days and ISO weeks that have one.
type Keep struct {
	Hourly int `yaml:"hourly"`
	Daily  int `yaml:"daily"`
	Weekly int `yaml:"weekly"`
}

func snapshotDir(dir, name string) string { return filepath.Join(dir, "snapshots", name) }

// Snapshot creates a Pebble checkpoint (hard links) plus a manifest of the
// blobs it references.
func (s *Store) Snapshot(label string) (*SnapshotManifest, error) {
	if label != "" && !core.ValidID(label) {
		return nil, fmt.Errorf("%w: snapshot label %q (want [a-z0-9][a-z0-9-]*)", core.ErrInvalid, label)
	}
	now := s.Now().UTC()
	name := now.Format("20060102T150405.000Z")
	if label != "" {
		name += "-" + label
	}
	dst := snapshotDir(s.dir, name)
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return nil, err
	}
	ckpt := filepath.Join(dst, "db")
	if err := s.db.Checkpoint(ckpt, pebble.WithFlushedWAL()); err != nil {
		os.RemoveAll(dst)
		return nil, fmt.Errorf("checkpoint to %s: %w", ckpt, err)
	}
	man := &SnapshotManifest{Name: name, Label: label, Created: now, ServerID: s.server.ID, Schema: core.Version}
	// Describe the checkpoint itself, not the live DB, which may have moved on.
	err := func() error {
		db, err := pebble.Open(ckpt, &pebble.Options{ReadOnly: true, Logger: quietLogger{}})
		if err != nil {
			return err
		}
		defer db.Close()
		var shas []string
		if man.Seqs, shas, err = describe(db); err != nil {
			return err
		}
		for _, sha := range shas {
			fi, err := os.Stat(s.BlobPath(sha))
			if err != nil {
				return fmt.Errorf("blob %s is referenced but missing: %w", sha, err)
			}
			man.Blobs = append(man.Blobs, BlobRef{sha, fi.Size()})
		}
		return nil
	}()
	if err == nil {
		err = os.WriteFile(filepath.Join(dst, "manifest.json"), indentJSON(man), 0o600)
	}
	if err != nil {
		os.RemoveAll(dst)
		return nil, fmt.Errorf("snapshot %s: %w", name, err)
	}
	return man, nil
}

// describe returns the last op seq per space and the sorted hashes of all
// blobs referenced by items and comments.
func describe(r pebble.Reader) (map[string]uint64, []string, error) {
	seqs := map[string]uint64{}
	blobs := map[string]bool{}
	ids, err := spaceIDs(r)
	if err != nil {
		return nil, nil, err
	}
	for _, id := range ids {
		if seqs[id], err = readSeq(r, id); err != nil {
			return nil, nil, err
		}
		for _, prefix := range []string{itemPrefix(id), commentPrefix(id)} {
			err := scan(r, prefix, func(key string, v []byte) error {
				var rec struct{ Attachments []core.Attachment }
				if err := json.Unmarshal(v, &rec); err != nil {
					return fmt.Errorf("key %q: %v", key, err)
				}
				for _, a := range rec.Attachments {
					blobs[a.SHA256] = true
				}
				return nil
			})
			if err != nil {
				return nil, nil, err
			}
		}
	}
	shas := make([]string, 0, len(blobs))
	for sha := range blobs {
		shas = append(shas, sha)
	}
	sort.Strings(shas)
	return seqs, shas, nil
}

// ListSnapshots returns the complete snapshots in dir, oldest first.
func ListSnapshots(dir string) ([]SnapshotManifest, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "snapshots"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []SnapshotManifest
	for _, e := range entries {
		var m SnapshotManifest
		err := readJSONFile(filepath.Join(snapshotDir(dir, e.Name()), "manifest.json"), &m)
		if errors.Is(err, fs.ErrNotExist) {
			continue // incomplete
		}
		if err != nil {
			return nil, err
		}
		if m.Name != e.Name() {
			return nil, fmt.Errorf("snapshot directory %q has the manifest of %q", e.Name(), m.Name)
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out, nil
}

// VerifySnapshot opens the checkpoint read-only, checks schema and server,
// reads every key, compares the op sequence numbers with the manifest and
// checks that every manifest blob exists with the right size and hash. It
// returns all problems found, joined.
func VerifySnapshot(dir, name string) error {
	var man SnapshotManifest
	if err := readJSONFile(filepath.Join(snapshotDir(dir, name), "manifest.json"), &man); err != nil {
		return fmt.Errorf("snapshot %s: %w", name, err)
	}
	var problems []error
	bad := func(format string, a ...any) {
		problems = append(problems, fmt.Errorf("snapshot %s: "+format, append([]any{name}, a...)...))
	}
	db, err := pebble.Open(filepath.Join(snapshotDir(dir, name), "db"), &pebble.Options{ReadOnly: true, ErrorIfNotExists: true, Logger: quietLogger{}})
	if err != nil {
		return fmt.Errorf("snapshot %s: open checkpoint: %w", name, err)
	}
	defer db.Close()
	if err := checkSchema(db); err != nil {
		bad("%v", err)
	}
	var srv Server
	if err := getJSON(db, []byte(keyServer), &srv); err != nil {
		bad("%v", err)
	} else if srv.ID != man.ServerID {
		bad("checkpoint is of server %s, manifest says %s", srv.ID, man.ServerID)
	}
	// Read every key and value. Pebble verifies block checksums on the way.
	it, err := db.NewIter(nil)
	if err != nil {
		return err
	}
	for it.First(); it.Valid(); it.Next() {
		key := string(it.Key())
		if v := it.Value(); !strings.HasSuffix(key, "/seq") && !strings.HasPrefix(key, "x/") && !json.Valid(v) {
			bad("key %q does not hold JSON", key)
		}
	}
	if err := it.Error(); err != nil {
		bad("reading keys: %v", err)
	}
	it.Close()
	seqs, shas, err := describe(db)
	if err != nil {
		bad("%v", err)
	}
	for id, seq := range seqs {
		if man.Seqs[id] != seq {
			bad("space %q is at op seq %d, manifest says %d", id, seq, man.Seqs[id])
		}
	}
	listed := map[string]bool{}
	for _, b := range man.Blobs {
		listed[b.SHA256] = true
		sha, size, err := hashFile(blobPath(dir, b.SHA256))
		switch {
		case err != nil:
			bad("blob %s: %v", b.SHA256, err)
		case sha != b.SHA256 || size != b.Size:
			bad("blob %s is corrupt: content has sha256 %s and %d bytes, manifest says %d bytes", b.SHA256, sha, size, b.Size)
		}
	}
	for _, sha := range shas {
		if !listed[sha] {
			bad("blob %s is referenced by the checkpoint but not in the manifest", sha)
		}
	}
	return errors.Join(problems...)
}

// PruneSnapshots deletes the snapshots the retention does not keep and returns
// their names.
func PruneSnapshots(dir string, keep Keep) ([]string, error) {
	all, err := ListSnapshots(dir)
	if err != nil {
		return nil, err
	}
	del := prunable(all, keep)
	for _, name := range del {
		if err := os.RemoveAll(snapshotDir(dir, name)); err != nil {
			return nil, err
		}
	}
	return del, nil
}

// prunable returns the names of the snapshots that keep does not retain, oldest
// first. A zero Keep retains everything.
func prunable(all []SnapshotManifest, keep Keep) []string {
	if keep == (Keep{}) {
		return nil
	}
	sorted := slices.Clone(all)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Created.After(sorted[j].Created) })
	kept := map[string]bool{}
	retain := func(n int, bucket func(time.Time) string) {
		seen := map[string]bool{}
		for _, m := range sorted {
			b := bucket(m.Created.UTC())
			if seen[b] {
				continue
			}
			if len(seen) == n {
				return
			}
			seen[b] = true
			kept[m.Name] = true
		}
	}
	retain(keep.Hourly, func(t time.Time) string { return t.Format("2006-01-02T15") })
	retain(keep.Daily, func(t time.Time) string { return t.Format("2006-01-02") })
	retain(keep.Weekly, func(t time.Time) string { y, w := t.ISOWeek(); return fmt.Sprintf("%d-W%02d", y, w) })
	var del []string
	for i := len(sorted) - 1; i >= 0; i-- {
		if !kept[sorted[i].Name] {
			del = append(del, sorted[i].Name)
		}
	}
	return del
}

// RestoreSnapshot replaces the database with the checkpoint of a snapshot. The
// daemon must be stopped. The current database is moved aside, never deleted;
// the returned path says where (empty if there was none).
func RestoreSnapshot(dir, name string, now time.Time) (movedTo string, err error) {
	if err := VerifySnapshot(dir, name); err != nil {
		return "", err
	}
	cur := dbDir(dir)
	if _, err := os.Stat(cur); err == nil {
		// Opening takes the lock: this fails while a daemon runs.
		db, err := pebble.Open(cur, &pebble.Options{Logger: quietLogger{}})
		if err != nil {
			return "", fmt.Errorf("%w: stop the daemon before restoring (%v)", ErrLocked, err)
		}
		if err := db.Close(); err != nil {
			return "", err
		}
	}
	tmp := cur + ".restoring"
	if err := os.RemoveAll(tmp); err != nil {
		return "", err
	}
	if err := os.CopyFS(tmp, os.DirFS(filepath.Join(snapshotDir(dir, name), "db"))); err != nil {
		return "", fmt.Errorf("copy checkpoint: %w", err)
	}
	if _, err := os.Stat(cur); err == nil {
		movedTo = cur + ".replaced-" + now.UTC().Format("20060102T150405Z")
		if err := os.Rename(cur, movedTo); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tmp, cur); err != nil {
		return movedTo, err
	}
	return movedTo, syncDir(dir)
}
