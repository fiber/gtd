package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/fiber/gtd/core"
)

var update = flag.Bool("update", false, "rewrite the golden export in testdata/export")

// deterministic installs a clock that advances one second per call, in a +01:00
// zone, and sequential ULID-shaped IDs.
func deterministic(s *Store) {
	zone := time.FixedZone("", 3600)
	tick, n := 0, 0
	var mu sync.Mutex
	s.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		tick++
		return time.Date(2026, 11, 19, 8, 0, tick, 123000000, zone)
	}
	s.NewID = func() string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return fmt.Sprintf("01JD8X4Q9V3T6M2K7P5R%06d", n)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func check0(err error) {
	if err != nil {
		panic(err)
	}
}

func raw(v any) json.RawMessage { return mustJSON(v) }

func createOp(it core.Item) core.Op {
	return core.Op{Via: "cli", Type: core.OpCreate, Payload: raw(it)}
}

func moveOp(it *core.Item, state string) core.Op {
	return core.Op{Via: "cli", Type: core.OpTransition, Item: it.ID, BaseRev: it.Rev,
		Changes: []core.FieldChange{{Field: "state", New: raw(state)}}}
}

func patchOp(it *core.Item, field string, value any) core.Op {
	return core.Op{Via: "cli", Type: core.OpPatch, Item: it.ID, BaseRev: it.Rev,
		Changes: []core.FieldChange{{Field: field, New: raw(value)}}}
}

// nasty strings that a Markdown-with-frontmatter export must carry unchanged.
const (
	nastyTitle = `NET/Boat: Engine — "session reset"? #1 [[x]] @dana: {a: b} | > ' ` + "`" + ` é 日本 🚀`
	nastyBody  = "# Heading\r\nCRLF line\r\n\ttab  trailing spaces   \n---\nkey: value\n\n\n- [ ] todo\n```\ncode\n```\nno newline at end"
	nastyOrig  = "  leading spaces\nline two: with colon\n---\n\ttab\n\n"
)

// fixture builds a database that uses every record type: two users, a personal
// and a shared space, every kind, dependencies with derived ops, notifications,
// comments with a tombstone, attachments on items and comments.
func fixture(t testing.TB, dir string) *Store {
	t.Helper()
	s := must(create(dir, "test", deterministic))
	check0(s.AddUser(core.User{ID: "robin", Name: "Robin", Lang: "de",
		SSHKeys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFixtureKeyFixtureKeyFixtureKeyFixtureKeyAAAA"}, PersonalContexts: []string{"boat"}, ReviewWeekday: 5}))
	check0(s.AddUser(core.User{ID: "dana", Name: "Dana"}))
	check0(s.AddUser(core.User{ID: "jo", Name: "Jo"}))
	check0(s.CreateSpace("net", "NET", []string{"robin", "dana"}))
	// People and templates have no admin command in M0. Set them on the record.
	sp := s.spaces["net"]
	rec := *sp.rec
	rec.People = []string{"Kim"}
	rec.Templates = []core.Template{{Key: "edge_hypothesis", Kind: "project",
		Label: map[string]string{"en": "Edge hypothesis", "de": "Edge-Hypothese"},
		Fields: []core.TemplateField{
			{Key: "stage", Type: "enum", Options: []string{"idea", "testing"}, Label: map[string]string{"en": "Stage"}},
			{Key: "expires", Type: "date", Tickler: true}},
		Body: "## Hypothesis\n\n## Result\n"}}
	check0(s.db.Set(spaceKey("net"), mustJSON(rec), pebble.Sync))
	sp.rec = &rec

	robin, dana := must(s.View("robin")), must(s.View("dana"))
	png := must2(s.PutBlob(strings.NewReader("\x89PNG fake image bytes")))
	pdf := must2(s.PutBlob(strings.NewReader("%PDF-1.7 fake report")))

	// Personal space: captures, one trashed, one clarified into an action.
	c1 := must(robin.Capture("boat engine reset thing ask dana", "", core.Source{Via: "cli"}, nil))
	c2 := must(robin.Capture(nastyOrig+"multi-line capture", "", core.Source{Via: "share", Ref: "https://example.org/a?b=c"},
		[]core.Attachment{{SHA256: png.sha, Name: "whiteboard.png", MIME: "image/png", Size: png.size}}))
	must(robin.Apply("robin", moveOp(c2, core.StTrashed)))
	must(robin.Apply("robin",
		core.Op{Via: "web", Type: core.OpTransition, Item: c1.ID, BaseRev: c1.Rev, Changes: []core.FieldChange{
			{Field: "kind", New: raw(core.KindAction)}, {Field: "state", New: raw(core.StNext)}}},
		core.Op{Via: "web", Type: core.OpPatch, Item: c1.ID, BaseRev: c1.Rev + 1, Changes: []core.FieldChange{
			{Field: "title", New: raw("Ask Dana about Boat engine session resets")},
			{Field: "context", New: raw("boat")}, {Field: "energy", New: raw("low")}, {Field: "time", New: raw("15m")}}}))

	// Shared space: area, project tree, actions with dependencies, a note.
	id := func(n int) string { return fmt.Sprintf("01JD8X4Q9V3T6M2K7P5RZZ%04d", n) }
	area, proj, sub, a, b, c, d, note := id(1), id(2), id(3), id(4), id(5), id(6), id(7), id(8)
	must(robin.Apply("net",
		createOp(core.Item{ID: area, Kind: core.KindArea, Title: "Engines"}),
		createOp(core.Item{ID: proj, Kind: core.KindProject, Title: nastyTitle, Area: area, Target: "2027-01-15",
			Template: "edge_hypothesis", Fields: map[string]string{"stage": "testing", "odd": "- yes: null ~ 1e3", "empty": ""}}),
		createOp(core.Item{ID: sub, Kind: core.KindProject, Title: "Sub-outcome", Parent: proj, State: core.StSomeday}),
		createOp(core.Item{ID: a, Kind: core.KindAction, Title: "Clarify reset semantics", Project: proj, Area: area,
			Owner: "robin", Context: "agenda:dana", Due: "2026-12-01"}),
		createOp(core.Item{ID: b, Kind: core.KindAction, Title: "Implement reset handling", Project: proj,
			Owner: "dana", DelegatedBy: "robin", BlockedBy: []string{a}, State: core.StPlanned, DeferUntil: "2026-12-03"}),
		createOp(core.Item{ID: c, Kind: core.KindAction, Title: "Write the test", Project: sub, BlockedBy: []string{a, b}}),
		createOp(core.Item{ID: d, Kind: core.KindAction, Title: "Wait for Kim", State: core.StWaiting, WaitingOn: "Kim"}),
	))
	must(dana.Apply("net", createOp(core.Item{ID: note, Kind: core.KindNote, Title: "Design notes", Project: proj,
		Original: nastyOrig, Body: nastyBody, Source: core.Source{Via: "import", Ref: "obsidian:NET/Design notes.md"},
		DerivedFrom: []string{a}, Generated: true, AIGenerated: true,
		Imported: map[string]any{"folder": "NET/Boat", "tags": []any{"boat", "todo"}, "nested": map[string]any{"n": 1.5, "ok": true}},
		Conflict: raw(map[string]any{"by": "dana", "base_rev": 1, "ours": "a\n", "theirs": "b\n"}),
	})))
	must(dana.Apply("net", core.Op{Via: "service", Type: core.OpAttach, Item: note,
		Payload: raw(core.Attachment{SHA256: pdf.sha, Name: "report (final).pdf", MIME: "application/pdf", Size: pdf.size})}))
	must(robin.Apply("net", moveOp(must(robin.Item("net", note)), core.StFiled)))

	// Thread: a mention, an attachment, a deleted comment.
	cm := must(dana.Apply("net", core.Op{Via: "web", Type: core.OpComment, Item: a,
		Payload: raw(core.Comment{Body: "see @[user:robin]\n\nsecond paragraph", Mentions: []string{"robin"},
			Attachments: []core.Attachment{{SHA256: png.sha, Name: "curve.png", MIME: "image/png", Size: png.size}}})}))
	del := must(robin.Apply("net", core.Op{Via: "web", Type: core.OpComment, Item: a, Payload: raw(core.Comment{Body: "oops"})}))
	var delC core.Comment
	check0(json.Unmarshal(del[0].Payload, &delC))
	must(robin.Apply("net", core.Op{Via: "web", Type: core.OpDeleteComment, Item: a, Payload: raw(map[string]string{"comment": delC.ID})}))
	_ = cm

	// Dependencies: a done unblocks b (c still waits for b), then b dropped
	// unblocks c with a warning, then a reopened.
	must(robin.Apply("net", moveOp(must(robin.Item("net", a)), core.StDone)))
	must(dana.Apply("net", moveOp(must(dana.Item("net", b)), core.StDropped)))
	must(robin.Apply("net", moveOp(must(robin.Item("net", a)), core.StNext)))
	must(robin.Apply("net", patchOp(must(robin.Item("net", d)), "target", "2026-12-24")))
	return s
}

type blobInfo struct {
	sha  string
	size int64
}

func must2(sha string, size int64, err error) blobInfo {
	check0(err)
	return blobInfo{sha, size}
}

// dump returns every key and value of the database.
func dump(t testing.TB, s *Store) map[string]string {
	t.Helper()
	out := map[string]string{}
	it := must(s.db.NewIter(nil))
	defer it.Close()
	for it.First(); it.Valid(); it.Next() {
		out[string(it.Key())] = string(it.Value())
	}
	return out
}

// tree returns every file below dir, by slash-separated relative path.
func tree(t testing.TB, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		out[filepath.ToSlash(rel)] = string(must(os.ReadFile(path)))
		return nil
	})
	check0(err)
	return out
}

func diffMaps(t testing.TB, what string, got, want map[string]string) {
	t.Helper()
	keys := map[string]bool{}
	for k := range got {
		keys[k] = true
	}
	for k := range want {
		keys[k] = true
	}
	n := 0
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range keys {
			if !yield(k) {
				return
			}
		}
	}) {
		g, gok := got[k]
		w, wok := want[k]
		switch {
		case !gok:
			t.Errorf("%s: %q is missing", what, k)
		case !wok:
			t.Errorf("%s: %q is unexpected", what, k)
		case g != w:
			t.Errorf("%s: %q differs\n got: %q\nwant: %q", what, k, g, w)
		default:
			continue
		}
		if n++; n >= 10 {
			t.Fatalf("%s: more differences not shown", what)
		}
	}
}

func exportTo(t testing.TB, s *Store, dir string, only ...string) {
	t.Helper()
	var buf bytes.Buffer
	check0(s.Export(&buf, only))
	check0(Untar(&buf, dir))
}

// TestRoundTrip is the invariant 5 test: export → restore into an empty data
// directory → export again gives identical files, identical Pebble content and
// the checked-in golden tree.
func TestRoundTrip(t *testing.T) {
	dir1, dir2, exp1, exp2 := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	s1 := fixture(t, dir1)
	defer s1.Close()
	for _, p := range check(s1) {
		t.Errorf("fixture: %s", p)
	}
	before := dump(t, s1)

	// A password hash must never reach the export.
	check0(s1.UpdateUser("robin", func(u *core.User) error { u.PasswordHash = "$2a$10$SECRETHASH"; return nil }))
	exportTo(t, s1, exp1)
	files := tree(t, exp1)
	for name, data := range files {
		if strings.Contains(data, "SECRETHASH") || strings.Contains(data, "password_hash") {
			t.Errorf("%s contains the password hash", name)
		}
	}

	man, err := Restore(dir2, exp1)
	if err != nil {
		t.Fatal(err)
	}
	s2 := must(Open(dir2))
	defer s2.Close()
	diffMaps(t, "pebble content after restore", dump(t, s2), before)
	for _, p := range check(s2) {
		t.Errorf("restored: %s", p)
	}
	exportTo(t, s2, exp2)
	diffMaps(t, "second export", tree(t, exp2), files)
	diffMaps(t, "restored blobs", tree(t, filepath.Join(dir2, "blobs")), tree(t, filepath.Join(dir1, "blobs")))

	if len(man.Spaces) != 4 || man.Users != 3 || man.Blobs != 2 {
		t.Errorf("manifest: %d spaces, %d users, %d blobs", len(man.Spaces), man.Users, man.Blobs)
	}
	for _, want := range []string{"users/dana/notifications.jsonl", "users/robin/notifications.jsonl",
		"spaces/net/space.yaml", "spaces/net/ops.jsonl", "spaces/robin/ops.jsonl", "spaces/jo/space.yaml"} {
		if _, ok := files[want]; !ok {
			t.Errorf("export has no %s", want)
		}
	}

	golden := filepath.Join("testdata", "export")
	if *update {
		check0(os.RemoveAll(golden))
		check0(os.CopyFS(golden, os.DirFS(exp1)))
	}
	diffMaps(t, "golden export (run go test ./store -update after an intended format change)", files, tree(t, golden))

	// The golden tree alone restores to the same database.
	dir3 := t.TempDir()
	if _, err := Restore(dir3, golden); err != nil {
		t.Fatal(err)
	}
	s3 := must(Open(dir3))
	defer s3.Close()
	diffMaps(t, "pebble content restored from golden", dump(t, s3), before)

	// Restore refuses a data directory that already has a database.
	if _, err := Restore(dir2, exp1); err == nil || !strings.Contains(err.Error(), "already holds a database") {
		t.Errorf("restore into a used directory: %v", err)
	}
}

func TestExportIsDeterministic(t *testing.T) {
	s := fixture(t, t.TempDir())
	defer s.Close()
	var a, b bytes.Buffer
	check0(s.Export(&a, nil))
	time.Sleep(10 * time.Millisecond)
	check0(s.Export(&b, nil))
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("two exports of the same data differ")
	}
	// Untar replaces a previous export and leaves other files alone.
	dir := t.TempDir()
	check0(os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	check0(os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref"), 0o644))
	check0(os.MkdirAll(filepath.Join(dir, "spaces", "gone", "items"), 0o755))
	check0(os.WriteFile(filepath.Join(dir, "spaces", "gone", "items", "stale.md"), []byte("x"), 0o644))
	check0(Untar(&a, dir))
	files := tree(t, dir)
	if _, ok := files[".git/HEAD"]; !ok {
		t.Error(".git was removed")
	}
	if _, ok := files["spaces/gone/items/stale.md"]; ok {
		t.Error("stale file of a previous export survived")
	}
}

func TestPartialExport(t *testing.T) {
	s := fixture(t, t.TempDir())
	defer s.Close()
	dir := t.TempDir()
	exportTo(t, s, dir, "net")
	files := tree(t, dir)
	for name := range files {
		if strings.HasPrefix(name, "spaces/") && !strings.HasPrefix(name, "spaces/net/") || strings.HasPrefix(name, "users/") {
			t.Errorf("partial export contains %s", name)
		}
	}
	if strings.Contains(files["users.json"], `"jo"`) || !strings.Contains(files["users.json"], `"dana"`) {
		t.Errorf("users.json should list exactly the members: %s", files["users.json"])
	}
	if _, err := Restore(t.TempDir(), dir); err == nil || !strings.Contains(err.Error(), "partial") {
		t.Errorf("restore of a partial export: %v", err)
	}
}

func TestItemMarkdownRoundTrip(t *testing.T) {
	stamp := time.Date(2026, 11, 19, 8, 12, 3, 0, time.FixedZone("", 3600))
	base := core.Item{V: 1, ID: "01JD8X4Q9V3T6M2K7P5R1W0Y8Z", Space: "net", Kind: core.KindNote, Title: "t", State: core.StFiled, Repeat: "mon,wed,fri",
		Rev: 7, Created: stamp, Updated: stamp.Add(time.Hour + 5*time.Nanosecond).UTC(), UpdatedBy: "robin"}
	for i, str := range []string{
		"plain", nastyTitle, nastyBody, nastyOrig, "", " ", "\n", "\n\n\n", "---", "---\n", "\n---\n", "a\n---\nb\n---\n",
		"trailing space \n", "\ttab", "x\r", "\r\n", "null", "~", "true", "123", "1e3", "0x1F", "2026-11-19", "- item", "? q",
		"key: value", "# not a comment", "'single'", `"double"`, `back\slash`, "|", ">", "%", "@", "`", "!tag", "&anchor", "*alias",
		"[a, b]", "{a: b}", "ünïcödé 日本語 🚀", "\u0085next line", "\u2028line sep", "\x00nul", "\x07bell", string(rune(0xFEFF)) + "bom", strings.Repeat("long ", 500),
	} {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			it := base
			it.Title = str + "x" // a title is never empty
			it.Original, it.Body, it.WaitingOn = str, str, str
			it.Fields = map[string]string{"f": str, "g": "x"}
			it.Imported = map[string]any{"s": str, "list": []any{str, 1.0, true, nil}}
			md := must(itemToMD(&it))
			back, err := mdToItem(md)
			if err != nil {
				t.Fatalf("%v\n%s", err, md)
			}
			if got, want := string(mustJSON(back)), string(mustJSON(&it)); got != want {
				t.Fatalf("round trip changed the item\n got %s\nwant %s\nfile:\n%s", got, want, md)
			}
		})
	}
}

// TestIsolation: a user who is not a member of a space gets "not found" from
// every View method, exactly as for a space that does not exist.
func TestIsolation(t *testing.T) {
	s := fixture(t, t.TempDir())
	defer s.Close()
	dana, jo := must(s.View("dana")), must(s.View("jo"))
	items := must(dana.Items("net", nil))
	item := items[0]

	calls := map[string]func(v *View, space string) error{
		"Item":          func(v *View, sp string) error { _, err := v.Item(sp, item.ID); return err },
		"Items":         func(v *View, sp string) error { _, err := v.Items(sp, nil); return err },
		"Comments":      func(v *View, sp string) error { _, err := v.Comments(sp, item.ID); return err },
		"CommentCount":  func(v *View, sp string) error { _, err := v.CommentCount(sp, item.ID); return err },
		"State":         func(v *View, sp string) error { _, err := v.State(sp); return err },
		"Ops":           func(v *View, sp string) error { _, err := v.Ops(sp, 10); return err },
		"ImportEntries": func(v *View, sp string) error { _, err := v.ImportEntries(sp); return err },
		"ImportReport":  func(v *View, sp string) error { _, err := v.ImportReport(sp); return err },
		"ImportMark": func(v *View, sp string) error {
			return v.ImportMark(sp, core.ImportMarker{Source: "obsidian", Phase: "begin"}, []byte(`{}`))
		},
		"Import": func(v *View, sp string) error {
			_, err := v.Import(sp, []ImportNote{{Path: "a.md", Hash: "h", Item: core.Item{ID: v.s.NewID(), Title: "intruder"}}})
			return err
		},
		"Move": func(v *View, sp string) error { // a capture from the own inbox into the space
			id := v.s.NewID()
			if _, err := v.Apply(v.user, createOp(core.Item{ID: id, Kind: core.KindCapture, Title: "to move"})); err != nil {
				return err
			}
			_, err := v.Move(v.user, sp, id)
			return err
		},
		"Apply": func(v *View, sp string) error {
			_, err := v.Apply(sp, createOp(core.Item{ID: v.s.NewID(), Kind: core.KindNote, Title: "intruder"}))
			return err
		},
	}
	// Methods that take no space: they must only ever return the user's own data.
	unscoped := []string{"User", "Spaces", "Grep", "Notifications", "Capture", "CaptureOnce", "Find", "Blob", "Dismiss"}
	typ := reflect.TypeOf(&View{})
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		if calls[name] == nil && !slices.Contains(unscoped, name) {
			t.Errorf("View.%s has no isolation test", name)
		}
	}
	seq := s.spaces["net"].seq
	for name, call := range calls {
		if err := call(dana, "net"); err != nil {
			t.Errorf("%s as a member: %v", name, err)
		}
		errNonMember := call(jo, "net")
		errNoSpace := call(jo, "no-such-space")
		if !errors.Is(errNonMember, core.ErrNotFound) {
			t.Errorf("%s as a non-member: %v, want not found", name, errNonMember)
		}
		if errNonMember == nil || errNoSpace == nil || strings.ReplaceAll(errNonMember.Error(), `"net"`, `"no-such-space"`) != errNoSpace.Error() {
			t.Errorf("%s leaks existence: non-member %q, missing space %q", name, errNonMember, errNoSpace)
		}
	}
	if got := s.spaces["net"].seq; got != seq+4 {
		t.Errorf("space seq moved from %d to %d, want exactly dana's four ops (create, import marker, imported note, arriving move)", seq, got)
	}
	for _, sp := range jo.Spaces() {
		if sp.ID != "jo" {
			t.Errorf("jo sees space %q", sp.ID)
		}
	}
	if hits := jo.Grep("reset"); len(hits) != 0 {
		t.Errorf("jo's grep found %d items of other spaces", len(hits))
	}
	if hits := dana.Grep("reset"); len(hits) == 0 {
		t.Error("dana's grep found nothing in net")
	}
	if _, err := jo.Find(item.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("jo finds an item of net by id: %v", err)
	}
	if _, err := dana.Find(item.ID); err != nil {
		t.Errorf("dana cannot find an item of net: %v", err)
	}
	blob := "a02a057e737c31e8442a34db35f889a5013733b5e1adf7769bbc878ff36e0c3a" // the PDF on the net note
	if _, ok := jo.Blob(blob); ok {
		t.Error("jo reads a blob attached only in net")
	}
	if a, ok := dana.Blob(blob); !ok || a.Name != "report (final).pdf" {
		t.Errorf("dana's blob lookup: %+v %v", a, ok)
	}
	if err := jo.Dismiss(1); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("jo dismisses a notification she does not have: %v", err)
	}
	if n := must(jo.Notifications()); len(n) != 0 {
		t.Errorf("jo has %d notifications", len(n))
	}
	// Robin's personal space is closed to Dana, a co-member of net.
	if _, err := dana.Items("robin", nil); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("dana reads robin's personal space: %v", err)
	}
	// Membership is checked on every call: removing Dana takes effect at once.
	check0(s.SetMember("net", "dana", false))
	if _, err := dana.Item("net", item.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("removed member still reads: %v", err)
	}
	check0(s.SetMember("net", "jo", true))
	if _, err := jo.Item("net", item.ID); err != nil {
		t.Errorf("new member cannot read: %v", err)
	}
	if err := s.SetMember("robin", "jo", true); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("adding a member to a personal space: %v", err)
	}
	if _, err := s.View("nobody"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("view of an unknown user: %v", err)
	}
}

func TestApplyBatch(t *testing.T) {
	s := fixture(t, t.TempDir())
	defer s.Close()
	robin := must(s.View("robin"))
	before := dump(t, s)

	// One invalid op: nothing of the batch is stored.
	_, err := robin.Apply("net",
		createOp(core.Item{ID: "01JD8X4Q9V3T6M2K7P5RYY0001", Kind: core.KindNote, Title: "fine"}),
		createOp(core.Item{ID: "01JD8X4Q9V3T6M2K7P5RYY0002", Kind: core.KindNote, Title: "bad", Project: "nope"}))
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	diffMaps(t, "after a failed batch", dump(t, s), before)
	if _, err := robin.Item("net", "01JD8X4Q9V3T6M2K7P5RYY0001"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("item of a failed batch is in the index: %v", err)
	}

	// The second op of a batch sees the first. Replaying an op ID is a no-op.
	a := createOp(core.Item{ID: "01JD8X4Q9V3T6M2K7P5RYY0003", Kind: core.KindAction, Title: "first"})
	a.ID = "01JD8X4Q9V3T6M2K7P5RYYOP01"
	b := createOp(core.Item{ID: "01JD8X4Q9V3T6M2K7P5RYY0004", Kind: core.KindAction, Title: "second", BlockedBy: []string{"01JD8X4Q9V3T6M2K7P5RYY0003"}})
	b.ID = "01JD8X4Q9V3T6M2K7P5RYYOP02"
	ops := must(robin.Apply("net", a, b, a))
	if len(ops) != 3 { // create, create, derived next→planned
		t.Fatalf("logged %d ops, want 3", len(ops))
	}
	for i := 1; i < len(ops); i++ {
		if ops[i].Seq != ops[i-1].Seq+1 {
			t.Fatalf("op seqs are not consecutive: %d then %d", ops[i-1].Seq, ops[i].Seq)
		}
	}
	if it := must(robin.Item("net", "01JD8X4Q9V3T6M2K7P5RYY0004")); it.State != core.StPlanned {
		t.Fatalf("second item is %s", it.State)
	}
	again := must(robin.Apply("net", a, b))
	if len(again) != 0 {
		t.Fatalf("replay logged %d ops", len(again))
	}
	for _, p := range check(s) {
		t.Error(p)
	}
}

func TestReopen(t *testing.T) {
	dir := t.TempDir()
	s := fixture(t, dir)
	before := dump(t, s)
	robin := must(s.View("robin"))
	wantItems := mustJSON(must(robin.Items("net", nil)))
	nseq := s.nseq["dana"]
	if nseq == 0 {
		t.Fatal("fixture should notify dana")
	}

	if _, err := Open(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second open: %v, want ErrLocked", err)
	}
	check0(s.Close())
	if _, err := Open(t.TempDir()); !errors.Is(err, ErrNoDB) {
		t.Fatalf("open of an empty directory: %v, want ErrNoDB", err)
	}
	if _, err := Create(dir, "x"); err == nil {
		t.Fatal("create over an existing database succeeded")
	}

	s = must(Open(dir))
	defer s.Close()
	diffMaps(t, "after reopen", dump(t, s), before)
	robin = must(s.View("robin"))
	if got := mustJSON(must(robin.Items("net", nil))); !bytes.Equal(got, wantItems) {
		t.Error("index differs after reopen")
	}
	if s.nseq["dana"] != nseq || s.Server().Name != "test" {
		t.Errorf("nseq %d (want %d), server %+v", s.nseq["dana"], nseq, s.Server())
	}
	if n := must(robin.CommentCount("net", "01JD8X4Q9V3T6M2K7P5RZZ0004")); n != 2 {
		t.Errorf("comment count = %d, want 2", n)
	}
	if cs := must(robin.Comments("net", "01JD8X4Q9V3T6M2K7P5RZZ0004")); len(cs) != 2 || !cs[1].Deleted || cs[1].Body != "" {
		t.Errorf("comments = %+v", cs)
	}
	ops := must(robin.Ops("net", 3))
	if len(ops) != 3 || ops[2].Seq != s.spaces["net"].seq || ops[0].Seq != ops[2].Seq-2 {
		t.Errorf("ops tail = %+v", ops)
	}
	for _, p := range check(s) {
		t.Error(p)
	}
}

func TestAdmin(t *testing.T) {
	s := must(Create(t.TempDir(), "test"))
	defer s.Close()
	check0(s.AddUser(core.User{ID: "robin"}))
	for _, c := range []struct {
		name string
		err  error
		want error
	}{
		{"duplicate user", s.AddUser(core.User{ID: "robin"}), core.ErrConflict},
		{"reserved user", s.AddUser(core.User{ID: "system"}), core.ErrInvalid},
		{"reserved ai", s.AddUser(core.User{ID: "ai"}), core.ErrInvalid},
		{"bad user id", s.AddUser(core.User{ID: "Robin/x"}), core.ErrInvalid},
		{"space with a user's id", s.CreateSpace("robin", "", nil), core.ErrConflict},
		{"space with unknown member", s.CreateSpace("net", "", []string{"dana"}), core.ErrNotFound},
		{"space", s.CreateSpace("net", "", []string{"robin"}), nil},
		{"duplicate space", s.CreateSpace("net", "", nil), core.ErrConflict},
		{"user with a space's id", s.AddUser(core.User{ID: "net"}), core.ErrConflict},
		{"unknown member", s.SetMember("net", "dana", true), core.ErrNotFound},
		{"unknown space", s.SetMember("nope", "robin", true), core.ErrNotFound},
		{"update unknown user", s.UpdateUser("dana", func(*core.User) error { return nil }), core.ErrNotFound},
	} {
		if !errors.Is(c.err, c.want) || (c.want == nil && c.err != nil) {
			t.Errorf("%s: %v, want %v", c.name, c.err, c.want)
		}
	}
	u := s.User("robin")
	if u.PersonalSpace != "robin" || u.Name != "robin" || s.UserCount() != 1 {
		t.Errorf("user = %+v", u)
	}
	v := must(s.View("robin"))
	if sp := v.Spaces(); len(sp) != 2 || sp[1].Kind != core.SpacePersonal || sp[0].Kind != core.SpaceShared {
		t.Errorf("spaces = %+v", sp)
	}
}

func TestBlobs(t *testing.T) {
	s := must(Create(t.TempDir(), "test"))
	defer s.Close()
	sha1, n1, err := s.PutBlob(strings.NewReader("hello"))
	check0(err)
	sha2, _, err := s.PutBlob(strings.NewReader("hello"))
	check0(err)
	if sha1 != sha2 || n1 != 5 || sha1 != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("sha %s / %s, size %d", sha1, sha2, n1)
	}
	if got := must(os.ReadFile(s.BlobPath(sha1))); string(got) != "hello" {
		t.Fatalf("blob content %q", got)
	}
	files := tree(t, filepath.Join(s.Dir(), "blobs"))
	if len(files) != 1 {
		t.Fatalf("blob directory holds %v", files)
	}
}

// TestConcurrent runs writers on two spaces and readers at once. Its value is
// under -race.
func TestConcurrent(t *testing.T) {
	s := fixture(t, t.TempDir())
	defer s.Close()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for _, w := range []struct{ user, space string }{{"robin", "robin"}, {"robin", "net"}, {"dana", "net"}, {"dana", "dana"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v := must(s.View(w.user))
			for i := 0; i < 25; i++ {
				a, b := s.NewID(), s.NewID()
				must(v.Apply(w.space,
					createOp(core.Item{ID: a, Kind: core.KindAction, Title: "a", Owner: w.user}),
					createOp(core.Item{ID: b, Kind: core.KindAction, Title: "b", Owner: w.user, BlockedBy: []string{a}, State: core.StPlanned})))
				must(v.Apply(w.space, moveOp(must(v.Item(w.space, a)), core.StDone)))
				must(v.Apply(w.space, core.Op{Via: "cli", Type: core.OpComment, Item: b, Payload: raw(core.Comment{Body: "done"})}))
			}
		}()
	}
	var readers sync.WaitGroup
	for _, user := range []string{"robin", "dana", "jo"} {
		readers.Add(1)
		go func() {
			defer readers.Done()
			v := must(s.View(user))
			for {
				select {
				case <-stop:
					return
				default:
				}
				v.Grep("a")
				for _, sp := range v.Spaces() {
					must(v.Items(sp.ID, func(it *core.Item) bool { return it.State == core.StNext }))
					must(v.Ops(sp.ID, 5))
				}
				must(v.Notifications())
			}
		}()
	}
	wg.Wait()
	close(stop)
	readers.Wait()
	for _, p := range check(s) {
		t.Error(p)
	}
}

func TestSnapshots(t *testing.T) {
	dir := t.TempDir()
	s := fixture(t, dir)
	robin := must(s.View("robin"))
	var atSnapshot bytes.Buffer
	check0(s.Export(&atSnapshot, nil))
	stateAtSnapshot := dump(t, s)

	man, err := s.Snapshot("before-change")
	check0(err)
	if !strings.HasSuffix(man.Name, "-before-change") || len(man.Blobs) != 2 || man.Seqs["net"] != s.spaces["net"].seq {
		t.Fatalf("manifest = %+v", man)
	}
	if _, err := s.Snapshot("Bad Label"); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("bad label: %v", err)
	}
	// The live database moves on. The snapshot does not.
	must(robin.Capture("after the snapshot", "", core.Source{Via: "cli"}, nil))
	second := must(s.Snapshot(""))

	list := must(ListSnapshots(dir))
	if len(list) != 2 || list[0].Name != man.Name || list[1].Name != second.Name {
		t.Fatalf("list = %+v", list)
	}
	if err := VerifySnapshot(dir, man.Name); err != nil {
		t.Fatalf("verify of an intact snapshot: %v", err)
	}

	t.Run("export from a snapshot", func(t *testing.T) {
		snap := must(OpenSnapshot(dir, man.Name))
		defer snap.Close()
		var buf bytes.Buffer
		check0(snap.Export(&buf, nil))
		if !bytes.Equal(buf.Bytes(), atSnapshot.Bytes()) {
			t.Fatal("export from the snapshot differs from the export taken at snapshot time")
		}
	})

	t.Run("verify detects a corrupted blob", func(t *testing.T) {
		blob := s.BlobPath(man.Blobs[0].SHA256)
		orig := must(os.ReadFile(blob))
		flipped := slices.Clone(orig)
		flipped[len(flipped)/2] ^= 0x01
		check0(os.Chmod(blob, 0o600))
		check0(os.WriteFile(blob, flipped, 0o600))
		err := VerifySnapshot(dir, man.Name)
		if err == nil || !strings.Contains(err.Error(), "blob "+man.Blobs[0].SHA256+" is corrupt") {
			t.Fatalf("verify with one flipped bit: %v", err)
		}
		t.Logf("verify reports: %v", err)
		check0(os.WriteFile(blob, orig[:len(orig)-1], 0o600))
		if err := VerifySnapshot(dir, man.Name); err == nil || !strings.Contains(err.Error(), "is corrupt") {
			t.Fatalf("verify with a truncated blob: %v", err)
		}
		check0(os.Remove(blob))
		if err := VerifySnapshot(dir, man.Name); err == nil || !strings.Contains(err.Error(), "no such file") {
			t.Fatalf("verify with a missing blob: %v", err)
		}
		check0(os.WriteFile(blob, orig, 0o400))
		if err := VerifySnapshot(dir, man.Name); err != nil {
			t.Fatalf("verify after repair: %v", err)
		}
	})

	t.Run("verify detects a manifest that does not match", func(t *testing.T) {
		path := filepath.Join(snapshotDir(dir, second.Name), "manifest.json")
		orig := must(os.ReadFile(path))
		defer os.WriteFile(path, orig, 0o600)
		m := *second
		m.Seqs = map[string]uint64{"net": 1, "robin": 1, "dana": 0, "jo": 0}
		check0(os.WriteFile(path, indentJSON(m), 0o600))
		if err := VerifySnapshot(dir, second.Name); err == nil || !strings.Contains(err.Error(), "op seq") {
			t.Fatalf("verify with wrong seqs: %v", err)
		}
	})

	t.Run("restore", func(t *testing.T) {
		if _, err := RestoreSnapshot(dir, man.Name, s.Now()); !errors.Is(err, ErrLocked) {
			t.Fatalf("restore with the database open: %v, want ErrLocked", err)
		}
		check0(s.Close())
		moved, err := RestoreSnapshot(dir, man.Name, time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC))
		check0(err)
		if filepath.Base(moved) != "db.replaced-20261120T000000Z" {
			t.Fatalf("old database moved to %s", moved)
		}
		s = must(Open(dir))
		diffMaps(t, "after snapshot restore", dump(t, s), stateAtSnapshot)
		for _, p := range check(s) {
			t.Error(p)
		}
		// The replaced database is kept and still opens.
		old := must(pebble.Open(moved, &pebble.Options{ErrorIfNotExists: true, Logger: quietLogger{}}))
		check0(old.Close())
	})
	s.Close()
}

func TestPrunable(t *testing.T) {
	at := func(s string) time.Time { return must(time.Parse("2006-01-02 15:04", s)) }
	var all []SnapshotManifest
	add := func(s string) { all = append(all, SnapshotManifest{Name: s, Created: at(s)}) }
	// Every 30 minutes for three days: Monday 2026-11-16 to Wednesday 2026-11-18.
	for d := 16; d <= 18; d++ {
		for h := 0; h < 24; h++ {
			add(fmt.Sprintf("2026-11-%02d %02d:00", d, h))
			add(fmt.Sprintf("2026-11-%02d %02d:30", d, h))
		}
	}
	add("2026-11-08 12:00") // the Sunday of the week before
	add("2026-10-01 12:00") // long ago

	for _, c := range []struct {
		name string
		keep Keep
		want []string // kept, newest first
	}{
		{"no policy keeps all", Keep{}, nil},
		{"hourly 3", Keep{Hourly: 3}, []string{"2026-11-18 23:30", "2026-11-18 22:30", "2026-11-18 21:30"}},
		{"daily 2", Keep{Daily: 2}, []string{"2026-11-18 23:30", "2026-11-17 23:30"}},
		{"weekly 2", Keep{Weekly: 2}, []string{"2026-11-18 23:30", "2026-11-08 12:00"}},
		{"weekly 5 has only three weeks", Keep{Weekly: 5}, []string{"2026-11-18 23:30", "2026-11-08 12:00", "2026-10-01 12:00"}},
		{"combined", Keep{Hourly: 2, Daily: 3, Weekly: 2},
			[]string{"2026-11-18 23:30", "2026-11-18 22:30", "2026-11-17 23:30", "2026-11-16 23:30", "2026-11-08 12:00"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			del := prunable(all, c.keep)
			if c.want == nil {
				if len(del) != 0 {
					t.Fatalf("deleted %d", len(del))
				}
				return
			}
			var kept []string
			for _, m := range all {
				if !slices.Contains(del, m.Name) {
					kept = append(kept, m.Name)
				}
			}
			sort.Sort(sort.Reverse(sort.StringSlice(kept)))
			if !slices.Equal(kept, c.want) {
				t.Fatalf("kept %v\nwant %v", kept, c.want)
			}
		})
	}
}

func TestPruneSnapshots(t *testing.T) {
	dir := t.TempDir()
	s := fixture(t, dir)
	defer s.Close()
	base := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	var names []string
	for i := 0; i < 4; i++ {
		s.Now = func() time.Time { return base.Add(time.Duration(i) * time.Hour) }
		names = append(names, must(s.Snapshot("")).Name)
	}
	del := must(PruneSnapshots(dir, Keep{Hourly: 2}))
	if !slices.Equal(del, names[:2]) {
		t.Fatalf("deleted %v, want %v", del, names[:2])
	}
	left := must(ListSnapshots(dir))
	if len(left) != 2 || left[0].Name != names[2] {
		t.Fatalf("left %+v", left)
	}
}

// TestTickler: deferred items resurface on the right day, by a system op in
// the log (M2 acceptance, with an injected clock).
func TestTickler(t *testing.T) {
	s := fixture(t, t.TempDir())
	defer s.Close()
	robin := must(s.View("robin"))
	a := s.NewID()
	must(robin.Apply("net", createOp(core.Item{ID: a, Kind: core.KindAction, Title: "Deferred", Owner: "robin", DeferUntil: "2026-12-24"})))
	personal := s.NewID()
	must(robin.Apply("robin", createOp(core.Item{ID: personal, Kind: core.KindAction, Title: "Also deferred", DeferUntil: "2026-12-24"})))

	// The fixture has an action deferred until 2026-12-03, but it was dropped.
	for _, day := range []string{"2026-12-02", "2026-12-23"} {
		if n := must(s.Tickle(day)); n != 0 {
			t.Fatalf("%s: %d items resurfaced too early", day, n)
		}
	}
	seq := s.spaces["net"].seq
	if n := must(s.Tickle("2026-12-24")); n != 2 {
		t.Fatalf("on the day: %d items resurfaced, want 2", n)
	}
	it := must(robin.Item("net", a))
	if it.DeferUntil != "" || it.UpdatedBy != core.System {
		t.Errorf("after the tickler: %+v", it)
	}
	ops := must(robin.Ops("net", 1))
	if s.spaces["net"].seq != seq+1 || ops[0].Actor != core.System || ops[0].Type != core.OpPatch || ops[0].Item != a {
		t.Errorf("the tickler's op: %+v", ops[0])
	}
	if n := must(s.Tickle("2026-12-25")); n != 0 {
		t.Errorf("the day after: %d more", n)
	}
	for _, p := range check(s) {
		t.Error(p)
	}

	// Per-user state survives a restart and is separate per user.
	check0(s.PutUserState("robin", "review", map[string]string{"phase": "projects"}))
	var got map[string]string
	if !s.GetUserState("robin", "review", &got) || got["phase"] != "projects" || s.GetUserState("dana", "review", &got) {
		t.Errorf("user state: %v", got)
	}
	check0(s.DeleteUserState("robin", "review"))
	if s.GetUserState("robin", "review", &got) {
		t.Error("user state survived its deletion")
	}
}

// TestMove: an inbox capture moves to another space with its thread, in one
// batch with the ops that follow, or not at all (COLLAB §1).
func TestMove(t *testing.T) {
	dir := t.TempDir()
	s := must(Create(dir, "test"))
	defer func() { s.Close() }()
	deterministic(s)
	for _, u := range []string{"robin", "dana", "jo"} {
		check0(s.AddUser(core.User{ID: u}))
	}
	check0(s.CreateSpace("net", "NET", []string{"robin", "dana"}))
	robin, dana := must(s.View("robin")), must(s.View("dana"))
	sha, _, err := s.PutBlob(strings.NewReader("photo"))
	check0(err)
	cap := must(robin.Capture("idea for the team\nsecond line", "", core.Source{Via: "web"}, []core.Attachment{{SHA256: sha, Name: "p.png", MIME: "image/png", Size: 5}}))
	must(robin.Apply("robin", core.Op{Via: "web", Type: core.OpComment, Item: cap.ID, Payload: raw(core.Comment{Body: "a thought on it"})}))
	other := must(robin.Capture("stays private", "", core.Source{Via: "web"}, nil))
	var changed []string
	s.OnCommit = func(space string, items []*core.Item) {
		for _, it := range items {
			changed = append(changed, space+"/"+it.ID)
		}
	}

	// A following op that fails takes the move with it: nothing changes anywhere.
	before := must(robin.Ops("robin", 100))
	if _, err := robin.Move("robin", "net", cap.ID, core.Op{Via: "web", Type: core.OpPatch, Item: cap.ID, BaseRev: 99,
		Changes: []core.FieldChange{{Field: "title", Old: raw("something else"), New: raw("x")}}}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("move with a failing op: %v", err)
	}
	if it, err := robin.Item("robin", cap.ID); err != nil || it.Rev != cap.Rev {
		t.Fatalf("after a failed move the capture is not where it was: %+v %v", it, err)
	}
	if _, err := dana.Item("net", cap.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("after a failed move the capture is in the target: %v", err)
	}
	if after := must(robin.Ops("robin", 100)); len(after) != len(before) || len(must(robin.Ops("net", 100))) != 0 || len(changed) != 0 {
		t.Fatalf("a failed move left ops or notifications behind")
	}
	for _, p := range check(s) {
		t.Errorf("after a failed move: %s", p)
	}

	// The move, and what Clarify does next, in one batch.
	logged, err := robin.Move("robin", "net", cap.ID,
		core.Op{Via: "web", Type: core.OpTransition, Item: cap.ID, BaseRev: cap.Rev + 1, Changes: []core.FieldChange{{Field: "kind", New: raw(core.KindNote)}, {Field: "state", New: raw(core.StUnsorted)}}})
	check0(err)
	if len(logged) != 2 || logged[0].Type != core.OpMove || logged[0].Space != "net" || logged[1].Type != core.OpTransition {
		t.Fatalf("logged: %+v", logged)
	}
	it, err := dana.Item("net", cap.ID)
	if err != nil || it.Space != "net" || it.Kind != core.KindNote || it.Rev != cap.Rev+2 || it.Original != cap.Original || it.Body != cap.Body ||
		len(it.Attachments) != 1 || !it.Created.Equal(cap.Created) || it.UpdatedBy != "robin" {
		t.Errorf("moved item: %+v %v", it, err)
	}
	if _, err := robin.Item("robin", cap.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("the item is still in the personal space: %v", err)
	}
	if c := must(dana.Comments("net", cap.ID)); len(c) != 1 || c[0].Body != "a thought on it" || c[0].Author != "robin" {
		t.Errorf("the thread did not move: %+v", c)
	}
	if n := must(robin.CommentCount("robin", cap.ID)); n != 0 {
		t.Errorf("%d comments left behind", n)
	}
	if att, ok := dana.Blob(sha); !ok || att.Name != "p.png" {
		t.Errorf("dana cannot open the attachment of the moved item: %+v %v", att, ok)
	}
	if hits := dana.Grep("team"); len(hits) != 1 || hits[0].ID != cap.ID {
		t.Errorf("dana's search: %v", hits)
	}
	if hits := must(s.View("jo")).Grep("team"); len(hits) != 0 {
		t.Errorf("a non-member finds the moved item: %v", hits)
	}
	slices.Sort(changed)
	if !slices.Equal(changed, []string{"net/" + cap.ID, "robin/" + cap.ID}) {
		t.Errorf("OnCommit saw %v", changed)
	}
	for _, p := range check(s) {
		t.Errorf("after the move: %s", p)
	}

	areaID := s.NewID()
	must(robin.Apply("robin", createOp(core.Item{ID: areaID, Kind: core.KindArea, Title: "Home"})))
	// What cannot move.
	for name, c := range map[string]struct {
		from, to, item string
		want           error
	}{
		"an area":                   {"robin", "net", areaID, core.ErrInvalid},
		"into the same space":       {"robin", "robin", other.ID, core.ErrInvalid},
		"an item that is not there": {"robin", "net", "01NOPE", core.ErrNotFound},
		"into a space of others":    {"robin", "jo", other.ID, core.ErrNotFound},
		"out of a space of others":  {"jo", "robin", other.ID, core.ErrNotFound},
	} {
		if _, err := robin.Move(c.from, c.to, c.item); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if _, err := robin.Apply("robin", core.Op{Via: "web", Type: core.OpMove, Item: other.ID, Payload: raw(core.Move{To: "net"})}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("a move submitted as a plain op: %v", err)
	}

	// After a restart, and after export and restore, everything is the same.
	exp1, exp2, dir2 := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "restored")
	var buf bytes.Buffer
	check0(s.Export(&buf, nil))
	check0(Untar(&buf, exp1))
	check0(s.Close())
	s = must(Open(dir))
	for _, p := range check(s) {
		t.Errorf("after reopening: %s", p)
	}
	must(Restore(dir2, exp1))
	s2 := must(Open(dir2))
	defer s2.Close()
	buf.Reset()
	check0(s2.Export(&buf, nil))
	check0(Untar(&buf, exp2))
	diffMaps(t, "export after restore", tree(t, exp2), tree(t, exp1))
	if it, err := must(s2.View("dana")).Item("net", cap.ID); err != nil || it.Kind != core.KindNote {
		t.Errorf("the moved item after restore: %+v %v", it, err)
	}
}

// TestCaptureOnce: a capture with a sender-chosen op ID is created once, in
// the sender's own inbox.
func TestCaptureOnce(t *testing.T) {
	s := must(Create(t.TempDir(), "test"))
	defer s.Close()
	check0(s.AddUser(core.User{ID: "robin"}))
	check0(s.AddUser(core.User{ID: "dana"}))
	robin, dana := must(s.View("robin")), must(s.View("dana"))
	first := must(robin.CaptureOnce("link-abc", "from the other server", "", core.Source{Via: "server"}, nil))
	if first == nil || first.Space != "robin" || first.State != core.StInbox {
		t.Fatalf("first: %+v", first)
	}
	if again := must(robin.CaptureOnce("link-abc", "from the other server", "", core.Source{Via: "server"}, nil)); again != nil {
		t.Errorf("the same op created a second capture: %+v", again)
	}
	// The op ID is taken for everyone: it names one event.
	if other := must(dana.CaptureOnce("link-abc", "x", "", core.Source{Via: "server"}, nil)); other != nil {
		t.Errorf("the same op ID created a capture for another user: %+v", other)
	}
	if n := len(must(robin.Items("robin", nil))) + len(must(dana.Items("dana", nil))); n != 1 {
		t.Errorf("%d captures, want 1", n)
	}
}

// TestMoveProject: a project moves with everything in it; what does not come
// along is let go on both sides.
func TestMoveProject(t *testing.T) {
	s := must(Create(t.TempDir(), "test"))
	defer s.Close()
	deterministic(s)
	for _, u := range []string{"robin", "dana"} {
		check0(s.AddUser(core.User{ID: u}))
	}
	check0(s.CreateSpace("net", "NET", []string{"robin", "dana"}))
	check0(s.UpdateUser("robin", func(u *core.User) error { u.PersonalContexts = []string{"boat"}; return nil }))
	robin, dana := must(s.View("robin")), must(s.View("dana"))
	mk := func(it core.Item) string {
		it.ID = s.NewID()
		must(robin.Apply("robin", createOp(it)))
		return it.ID
	}
	area := mk(core.Item{Kind: core.KindArea, Title: "Home"})
	other := mk(core.Item{Kind: core.KindProject, Title: "Another project"})
	p := mk(core.Item{Kind: core.KindProject, Title: "Kitchen renovated", Area: area, Parent: other})
	sub := mk(core.Item{Kind: core.KindProject, Title: "Tiles chosen", Parent: p})
	first := mk(core.Item{Kind: core.KindAction, Title: "Get quotes", Project: p, Context: "boat"})
	second := mk(core.Item{Kind: core.KindAction, Title: "Pick one", Project: sub, BlockedBy: []string{first}})
	outside := mk(core.Item{Kind: core.KindAction, Title: "Unrelated blocker"})
	third := mk(core.Item{Kind: core.KindAction, Title: "Order", Project: p, BlockedBy: []string{second, outside}})
	note := mk(core.Item{Kind: core.KindNote, Title: "The quote", Project: p, State: core.StFiled, Body: "see [[" + outside + "]]"})
	must(robin.Apply("robin", core.Op{Via: "web", Type: core.OpComment, Item: note, Payload: raw(core.Comment{Body: "a comment on the note"})}))
	waits := mk(core.Item{Kind: core.KindAction, Title: "Stays, waited for the project's action", BlockedBy: []string{first}})
	stays := mk(core.Item{Kind: core.KindNote, Title: "A loose note"})
	if st := must(robin.Item("robin", waits)).State; st != core.StPlanned {
		t.Fatalf("setup: the waiting action is %s", st)
	}

	logged, err := robin.Move("robin", "net", p)
	check0(err)
	if len(logged) != 6 {
		t.Errorf("%d ops in the target space, want 6 arrivals", len(logged))
	}
	// Everything in the project is in net now, for both members.
	want := map[string]struct {
		state, project, parent, area, context, owner string
		blocked                                      []string
	}{
		p:      {core.StActive, "", "", "", "", "", nil}, // its area and its parent stayed behind
		sub:    {core.StActive, "", p, "", "", "", nil},
		first:  {core.StNext, p, "", "", "", "robin", nil}, // "boat" is robin's own context: not known in net
		second: {core.StPlanned, sub, "", "", "", "robin", []string{first}},
		third:  {core.StPlanned, p, "", "", "", "robin", []string{second}}, // the blocker that stayed is let go
		note:   {core.StFiled, p, "", "", "", "", nil},
	}
	for id, w := range want {
		it, err := dana.Item("net", id)
		if err != nil {
			t.Errorf("%s did not arrive: %v", id, err)
			continue
		}
		if it.State != w.state || it.Project != w.project || it.Parent != w.parent || it.Area != w.area || it.Context != w.context ||
			it.Owner != w.owner || !slices.Equal(it.BlockedBy, w.blocked) || it.Space != "net" {
			t.Errorf("%q in net: %s project=%s parent=%s area=%s context=%s owner=%s blocked=%v", it.Title, it.State, it.Project, it.Parent, it.Area, it.Context, it.Owner, it.BlockedBy)
		}
		if _, err := robin.Item("robin", id); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("%q is still in the personal space", it.Title)
		}
	}
	if c := must(dana.Comments("net", note)); len(c) != 1 {
		t.Errorf("the note's thread: %+v", c)
	}
	// What stayed: it no longer waits for what left, and is next.
	if it := must(robin.Item("robin", waits)); it.State != core.StNext || len(it.BlockedBy) != 0 {
		t.Errorf("the action that waited for a moved one: %s blocked by %v", it.State, it.BlockedBy)
	}
	for _, id := range []string{area, other, outside, stays} {
		if _, err := robin.Item("robin", id); err != nil {
			t.Errorf("%s should have stayed: %v", id, err)
		}
	}
	for _, p := range check(s) {
		t.Errorf("after the move: %s", p)
	}

	// Back again, into the personal space: actions belong to its user.
	must(dana.Apply("net", core.Op{Via: "web", Type: core.OpPatch, Item: first, BaseRev: must(dana.Item("net", first)).Rev,
		Changes: []core.FieldChange{{Field: "owner", Old: raw("robin"), New: raw("dana")}}}))
	if _, err := dana.Move("net", "dana", sub); err != nil {
		t.Fatal(err)
	}
	if it := must(dana.Item("dana", second)); it.Owner != "dana" || it.State != core.StNext || it.Project != sub || len(it.BlockedBy) != 0 {
		t.Errorf("an action moved into dana's personal space: owner %s, %s, project %s, blocked %v", it.Owner, it.State, it.Project, it.BlockedBy)
	}
	if it := must(robin.Item("net", third)); it.State != core.StNext || len(it.BlockedBy) != 0 {
		t.Errorf("what waited in net for a moved action: %s blocked by %v", it.State, it.BlockedBy)
	}
	if _, err := robin.Item("net", p); err != nil {
		t.Errorf("the parent project should have stayed in net: %v", err)
	}
	for _, p := range check(s) {
		t.Errorf("after the second move: %s", p)
	}
}
