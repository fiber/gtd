package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"gopkg.in/yaml.v3"

	"github.com/fiber/gtd/core"
)

// Restore builds a new database in dir from a full export in src (invariant 5:
// identical items, comments, attachments and op history). The data directory
// must not hold a database yet. Users come back without password hashes.
func Restore(dir, src string) (*Manifest, error) {
	var man Manifest
	if err := readJSONFile(filepath.Join(src, "manifest.json"), &man); err != nil {
		return nil, err
	}
	if man.Partial {
		return nil, fmt.Errorf("%s is a partial export (--space), which cannot be restored", src)
	}
	if man.Schema != core.Version {
		return nil, fmt.Errorf("%s has schema v%d, this binary needs v%d", src, man.Schema, core.Version)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if entries, _ := os.ReadDir(dbDir(dir)); len(entries) > 0 {
		return nil, fmt.Errorf("%s already holds a database: restore needs an empty data directory", dir)
	}
	db, err := pebble.Open(dbDir(dir), &pebble.Options{ErrorIfExists: true, Logger: quietLogger{}})
	if err != nil {
		return nil, err
	}
	r := &restorer{db: db, b: db.NewBatch(), src: src}
	err = r.run(&man)
	if err == nil {
		err = r.b.Commit(pebble.Sync) // syncs the WAL, and with it every earlier batch
	}
	if cerr := db.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = restoreBlobs(dir, src, man.Blobs)
	}
	if err != nil {
		os.RemoveAll(dbDir(dir)) // we created it: leave nothing half-restored
		return nil, fmt.Errorf("restore from %s: %w", src, err)
	}
	return &man, nil
}

type restorer struct {
	db  *pebble.DB
	b   *pebble.Batch
	src string
	n   int
}

func (r *restorer) set(key, value []byte) error {
	if err := r.b.Set(key, value, nil); err != nil {
		return err
	}
	if r.n++; r.n%2000 == 0 {
		if err := r.b.Commit(pebble.NoSync); err != nil {
			return err
		}
		r.b = r.db.NewBatch()
	}
	return nil
}

func (r *restorer) run(man *Manifest) error {
	if err := r.set([]byte(keySchema), []byte(fmt.Sprintf(`{"v":%d}`, core.Version))); err != nil {
		return err
	}
	if err := r.set([]byte(keyServer), mustJSON(man.Server)); err != nil {
		return err
	}

	var users []core.User
	if err := readJSONFile(filepath.Join(r.src, "users.json"), &users); err != nil {
		return err
	}
	if len(users) != man.Users {
		return fmt.Errorf("users.json has %d users, manifest says %d", len(users), man.Users)
	}
	for _, u := range users {
		if !core.ValidID(u.ID) {
			return fmt.Errorf("users.json: bad user id %q", u.ID)
		}
		u.V = core.Version
		if err := r.set(userKey(u.ID), mustJSON(u)); err != nil {
			return err
		}
		err := eachLine(filepath.Join(r.src, "users", u.ID, "notifications.jsonl"), func(line []byte) error {
			var n core.Notification
			if err := json.Unmarshal(line, &n); err != nil {
				return err
			}
			if n.User != u.ID || n.Seq == 0 {
				return fmt.Errorf("notification for user %q seq %d in the file of user %q", n.User, n.Seq, u.ID)
			}
			return r.set(notifKey(u.ID, n.Seq), line)
		})
		if err != nil {
			return err
		}
	}
	for _, ms := range man.Spaces {
		if err := r.space(ms); err != nil {
			return fmt.Errorf("space %q: %w", ms.ID, err)
		}
	}
	return nil
}

func (r *restorer) space(ms ManifestSpace) error {
	if !core.ValidID(ms.ID) {
		return fmt.Errorf("bad space id")
	}
	dir := filepath.Join(r.src, "spaces", ms.ID)
	data, err := os.ReadFile(filepath.Join(dir, "space.yaml"))
	if err != nil {
		return err
	}
	var y spaceYAML
	if err := yaml.Unmarshal(data, &y); err != nil {
		return fmt.Errorf("space.yaml: %v", err)
	}
	created, err := time.Parse(time.RFC3339Nano, y.Created)
	if err != nil || y.ID != ms.ID {
		return fmt.Errorf("space.yaml: id %q, created %q", y.ID, y.Created)
	}
	rec := core.Space{V: core.Version, ID: y.ID, Name: y.Name, Kind: y.Kind, Members: y.Members,
		People: y.People, Contexts: y.Contexts, Templates: y.Templates, Created: created}
	if err := r.set(spaceKey(ms.ID), mustJSON(rec)); err != nil {
		return err
	}

	items, err := dirNames(filepath.Join(dir, "items"))
	if err != nil {
		return err
	}
	for _, name := range items {
		data, err := os.ReadFile(filepath.Join(dir, "items", name))
		if err != nil {
			return err
		}
		it, err := mdToItem(data)
		if err != nil {
			return fmt.Errorf("items/%s: %v", name, err)
		}
		if it.ID+".md" != name || it.Space != ms.ID {
			return fmt.Errorf("items/%s holds item %s of space %q", name, it.ID, it.Space)
		}
		if err := r.set(itemKey(ms.ID, it.ID), mustJSON(it)); err != nil {
			return err
		}
	}
	if len(items) != ms.Items {
		return fmt.Errorf("%d item files, manifest says %d", len(items), ms.Items)
	}

	threads, err := dirNames(filepath.Join(dir, "comments"))
	if err != nil {
		return err
	}
	comments := 0
	for _, name := range threads {
		err := eachLine(filepath.Join(dir, "comments", name), func(line []byte) error {
			var c core.Comment
			if err := json.Unmarshal(line, &c); err != nil {
				return err
			}
			if c.Item+".jsonl" != name || c.ID == "" {
				return fmt.Errorf("comment %q of item %q", c.ID, c.Item)
			}
			comments++
			return r.set(commentKey(ms.ID, c.Item, c.ID), line)
		})
		if err != nil {
			return fmt.Errorf("comments/%s: %w", name, err)
		}
	}
	if comments != ms.Comments {
		return fmt.Errorf("%d comments, manifest says %d", comments, ms.Comments)
	}

	var seq uint64
	err = eachLine(filepath.Join(dir, "ops.jsonl"), func(line []byte) error {
		var op core.Op
		if err := json.Unmarshal(line, &op); err != nil {
			return err
		}
		if op.Seq != seq+1 || op.Space != ms.ID || op.ID == "" {
			return fmt.Errorf("op %q has seq %d in space %q, want seq %d", op.ID, op.Seq, op.Space, seq+1)
		}
		seq = op.Seq
		if err := r.set(opKey(ms.ID, seq), line); err != nil {
			return err
		}
		return r.set(opidKey(op.ID), mustJSON(opRef{ms.ID, seq}))
	})
	if err != nil {
		return fmt.Errorf("ops.jsonl: %w", err)
	}
	if seq != ms.Seq {
		return fmt.Errorf("ops.jsonl ends at seq %d, manifest says %d", seq, ms.Seq)
	}
	if seq > 0 {
		return r.set(seqKey(ms.ID), []byte(be64(seq)))
	}
	return nil
}

// restoreBlobs copies every blob of the export, checking its hash.
func restoreBlobs(dir, src string, want int) error {
	n := 0
	root := filepath.Join(src, "blobs")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && path == root {
				return filepath.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !core.ValidSHA256(d.Name()) {
			return fmt.Errorf("unexpected file %s", path)
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		n++
		_, _, err = putBlob(dir, f, d.Name())
		return err
	})
	if err == nil && n != want {
		err = fmt.Errorf("%d blobs, manifest says %d", n, want)
	}
	return err
}

func readJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	return nil
}

// eachLine calls f for every line of a .jsonl file. A missing file has no lines.
// The line is a fresh copy and may be retained.
func eachLine(path string, f func(line []byte) error) error {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	rd := bufio.NewReader(file)
	for n := 1; ; n++ {
		line, err := rd.ReadBytes('\n')
		if len(line) > 0 {
			if ferr := f(bytes.TrimSuffix(line, []byte("\n"))); ferr != nil {
				return fmt.Errorf("%s line %d: %w", path, n, ferr)
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// dirNames returns the sorted file names in dir. A missing directory is empty.
func dirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	return names, nil
}
