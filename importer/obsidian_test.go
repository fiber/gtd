package importer_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/fiber/gtd/core"
	"github.com/fiber/gtd/importer"
	"github.com/fiber/gtd/store"
)

var update = flag.Bool("update", false, "rewrite the golden export in testdata/golden")

// vaultFS loads testdata/vault with a fixed modification time: git does not
// keep file times, and a note's time must not depend on the checkout.
func vaultFS(t *testing.T) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	root := os.DirFS("testdata/vault")
	err := fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(root, p)
		out[p] = &fstest.MapFile{Data: data, ModTime: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func newStore(t *testing.T) (*store.Store, *store.View) {
	t.Helper()
	s, err := store.Create(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	tick, n := 0, 0
	var mu sync.Mutex
	s.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		tick++
		return time.Date(2026, 11, 19, 8, 0, tick, 0, time.UTC)
	}
	s.NewID = func() string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return fmt.Sprintf("01JD8X4Q9V3T6M2K7P5R%06d", n)
	}
	if err := s.AddUser(core.User{ID: "robin"}); err != nil {
		t.Fatal(err)
	}
	v, err := s.View("robin")
	if err != nil {
		t.Fatal(err)
	}
	return s, v
}

// run imports a vault the way the CLI does and returns the report.
func run(t *testing.T, s *store.Store, v *store.View, fsys fs.FS) importer.Report {
	t.Helper()
	vault, err := importer.Read(fsys)
	if err != nil {
		t.Fatal(err)
	}
	// Known paths keep their IDs. Without that knowledge the IDs come out
	// the same, so this test leaves the lookup to the store.
	vault.AssignIDs("robin", func(string) string { return "" })
	if err := v.ImportMark("robin", core.ImportMarker{Source: "obsidian", Phase: "begin", Notes: len(vault.Notes)}, nil); err != nil {
		t.Fatal(err)
	}
	vault.Resolve(func(file string) (string, error) {
		data, err := fs.ReadFile(fsys, file)
		if err != nil {
			return "", err
		}
		sha, _, err := s.PutBlob(bytes.NewReader(data))
		return sha, err
	})
	var notes []store.ImportNote
	for _, n := range vault.Notes {
		notes = append(notes, store.ImportNote{Path: n.Path, Hash: n.Hash, Item: n.Item()})
	}
	res, err := v.Import("robin", notes)
	if err != nil {
		t.Fatal(err)
	}
	rep := vault.Report
	rep.Imported, rep.Updated, rep.Unchanged = res.Created, res.Updated, res.Unchanged
	data, _ := json.Marshal(rep)
	if err := v.ImportMark("robin", core.ImportMarker{Source: "obsidian", Phase: "end", Notes: res.Created + res.Updated}, data); err != nil {
		t.Fatal(err)
	}
	return rep
}

func export(t *testing.T, s *store.Store) map[string]string {
	t.Helper()
	var buf bytes.Buffer
	if err := s.Export(&buf, nil); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := store.Untar(&buf, dir); err != nil {
		t.Fatal(err)
	}
	return tree(t, filepath.Join(dir, "spaces", "robin"))
}

func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return out
}

func ops(t *testing.T, v *store.View) []core.Op {
	t.Helper()
	list, err := v.Ops("robin", 100000)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// TestGolden imports the synthetic vault and compares the export of the
// space with testdata/golden (IMPORT.md §7).
func TestGolden(t *testing.T) {
	s, v := newStore(t)
	rep := run(t, s, v, vaultFS(t))
	got := export(t, s)
	const golden = "testdata/golden"
	if *update {
		os.RemoveAll(golden)
		for name, data := range got {
			p := filepath.Join(golden, filepath.FromSlash(name))
			os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	want := tree(t, golden)
	for _, name := range slices.Sorted(maps.Keys(want)) {
		if got[name] != want[name] {
			t.Errorf("%s differs from the golden file (go test ./importer -update after an intended change):\n%s", name, got[name])
		}
	}
	if len(got) != len(want) {
		t.Errorf("%d files exported, golden has %d", len(got), len(want))
	}

	want2 := importer.Report{Found: 17, Imported: 15, Empty: 2,
		AttachmentsReferenced: 5, AttachmentsIngested: 4, BytesDeduplicated: int64(len(vaultFS(t)["assets/plan copy.png"].Data)),
		LinksResolved: 20, LinksAmbiguous: 2, LinksMissing: 3, AnchorsDropped: 3, Todos: 3}
	if rep.Found != want2.Found || rep.Imported != want2.Imported || rep.Empty != want2.Empty ||
		rep.AttachmentsReferenced != want2.AttachmentsReferenced || rep.AttachmentsIngested != want2.AttachmentsIngested ||
		rep.AttachmentsMissing != 0 || rep.BytesDeduplicated != want2.BytesDeduplicated ||
		rep.LinksResolved != want2.LinksResolved || rep.LinksAmbiguous != want2.LinksAmbiguous || rep.LinksMissing != want2.LinksMissing ||
		rep.AnchorsDropped != want2.AnchorsDropped || rep.Todos != want2.Todos {
		t.Errorf("report:\n%+v\nwant:\n%+v", rep, want2)
	}
	if len(rep.DuplicateGroups) != 1 || strings.Join(rep.DuplicateGroups[0], "|") != "Journal/duplicate one.md|Journal/duplicate two.md" {
		t.Errorf("duplicate groups: %v", rep.DuplicateGroups)
	}
	if strings.Join(rep.Unreferenced, "|") != "assets/unused.png" || strings.Join(rep.BadMetadata, "|") != "Journal/2024/bad frontmatter.md" {
		t.Errorf("unreferenced %v, bad frontmatter %v", rep.Unreferenced, rep.BadMetadata)
	}

	// Every note: unsorted, owned by the importing user, original = the file.
	items, _ := v.Items("robin", nil)
	files := vaultFS(t)
	for _, it := range items {
		p := strings.TrimPrefix(it.Source.Ref, "obsidian:")
		if it.Kind != core.KindNote || it.State != core.StUnsorted || it.Owner != "robin" || it.Source.Via != "import" ||
			files[p] == nil || it.Original != string(files[p].Data) {
			t.Errorf("item %s (%s): kind %s, state %s, owner %s, original kept: %v", it.ID, it.Source.Ref, it.Kind, it.State, it.Owner, files[p] != nil && it.Original == string(files[p].Data))
		}
	}
	// The export, import markers included, restores to the same data.
	var buf bytes.Buffer
	if err := s.Export(&buf, nil); err != nil {
		t.Fatal(err)
	}
	expDir, dataDir := t.TempDir(), filepath.Join(t.TempDir(), "restored")
	if err := store.Untar(&buf, expDir); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Restore(dataDir, expDir); err != nil {
		t.Fatalf("restore of an export with import ops: %v", err)
	}
	s2, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if again := export(t, s2); !maps.Equal(again, got) {
		t.Error("export → restore → export differs")
	}

	log := ops(t, v)
	if log[0].Type != core.OpImport || log[len(log)-1].Type != core.OpImport || len(log) != 17 {
		t.Errorf("%d ops, first %s, last %s; want begin marker, 15 creates, end marker", len(log), log[0].Type, log[len(log)-1].Type)
	}
	for _, op := range log {
		if op.Via != "import" || op.Actor != "robin" {
			t.Errorf("op %s: via %s actor %s", op.ID, op.Via, op.Actor)
		}
	}
}

// TestTwice: importing again writes nothing but the two marker ops; a
// changed file updates its note; a lost path map does not duplicate notes.
func TestTwice(t *testing.T) {
	s, v := newStore(t)
	files := vaultFS(t)
	run(t, s, v, files)
	before := export(t, s)
	n := len(ops(t, v))

	rep := run(t, s, v, files)
	log := ops(t, v)
	if rep.Imported != 0 || rep.Updated != 0 || rep.Unchanged != 15 || len(log) != n+2 || log[n].Type != core.OpImport || log[n+1].Type != core.OpImport {
		t.Fatalf("second run: %d new, %d updated, %d unchanged, %d new ops", rep.Imported, rep.Updated, rep.Unchanged, len(log)-n)
	}
	after := export(t, s)
	delete(before, "ops.jsonl")
	delete(after, "ops.jsonl")
	if !maps.Equal(before, after) {
		t.Error("items changed in the second run")
	}

	// One file changes.
	const p = "Projects/Garden/Notes.md"
	files[p] = &fstest.MapFile{Data: []byte("Garden notes, extended.\n"), ModTime: files[p].ModTime}
	rep = run(t, s, v, files)
	if rep.Imported != 0 || rep.Updated != 1 || rep.Unchanged != 14 {
		t.Fatalf("after a change: %+v", rep)
	}
	var changed *core.Item
	items, _ := v.Items("robin", nil)
	for _, it := range items {
		if it.Source.Ref == "obsidian:"+p {
			changed = it
		}
	}
	if changed == nil || changed.Body != "Garden notes, extended." || changed.Imported["reimported_at"] == nil || changed.Original != "Garden notes.\n" || changed.Rev != 2 {
		t.Errorf("changed note: %+v", changed)
	}
	if len(items) != 15 {
		t.Errorf("%d items after three runs, want 15", len(items))
	}

	// A database restored from an export has no path map. The same files
	// still map to the same items.
	s2, v2 := newStore(t)
	run(t, s2, v2, vaultFS(t))
	entries, _ := v2.ImportEntries("robin")
	if len(entries) != 15 {
		t.Fatalf("%d path map entries", len(entries))
	}
	vault, _ := importer.Read(vaultFS(t))
	vault.AssignIDs("robin", func(string) string { return "" })
	ids := map[string]bool{}
	items2, _ := v2.Items("robin", nil)
	for _, it := range items2 {
		ids[it.ID] = true
	}
	for _, note := range vault.Notes {
		if !ids[note.ID] {
			t.Errorf("%s: the ID is not stable: %s", note.Path, note.ID)
		}
	}
}

// TestDryRun5000: reading, numbering and resolving 5,000 notes takes well
// under 30 seconds and touches no store.
func TestDryRun5000(t *testing.T) {
	files := fstest.MapFS{}
	mod := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	para := strings.Repeat("Some text about one thing and another. ", 40)
	for i := range 5000 {
		body := fmt.Sprintf("---\ntags: [t%d]\n---\n# Note %d\n\n%s\n\nSee [[note-%05d]] and [[note-%05d|that]] and ![[img-%03d.png]].\n- [ ] todo %d\n",
			i%7, i, para, (i+1)%5000, (i*31)%5000, i%100, i)
		files[fmt.Sprintf("dir%02d/sub%d/note-%05d.md", i%40, i%3, i)] = &fstest.MapFile{Data: []byte(body), ModTime: mod}
	}
	for i := range 100 {
		files[fmt.Sprintf("assets/img-%03d.png", i)] = &fstest.MapFile{Data: bytes.Repeat([]byte{byte(i)}, 20_000), ModTime: mod}
	}
	start := time.Now()
	vault, err := importer.Read(files)
	if err != nil {
		t.Fatal(err)
	}
	vault.AssignIDs("robin", func(string) string { return "" })
	vault.Resolve(vault.Hash256)
	took := time.Since(start)
	r := vault.Report
	if r.Found != 5000 || len(vault.Notes) != 5000 || r.LinksResolved != 15000 || r.AttachmentsReferenced != 100 || r.LinksMissing != 0 || r.Todos != 5000 {
		t.Errorf("report: %+v", r)
	}
	t.Logf("dry run over 5,000 notes and 100 images: %s", took.Round(time.Millisecond))
	if took > 30*time.Second {
		t.Errorf("dry run took %s, limit 30 s", took)
	}
}
