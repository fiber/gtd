package store

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/fiber/gtd/core"
)

const (
	loadItems    = 50_000
	loadComments = 200_000
)

// bulk writes items and comments straight into Pebble. Going through Apply
// would fsync 50,000 times. Only this test bypasses the op path, and only to
// build its fixture.
func bulk(t testing.TB, dir string) {
	t.Helper()
	s := must(Create(dir, "load"))
	check0(s.AddUser(core.User{ID: "sven"}))
	check0(s.AddUser(core.User{ID: "eric"}))
	check0(s.CreateSpace("nis", "", []string{"sven", "eric"}))
	rng := rand.New(rand.NewPCG(1, 2))
	words := strings.Fields("collector session reset bgp nora atlas curation automation review deploy fix draft plan test write call ask check migrate import export snapshot index query")
	text := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(words[rng.IntN(len(words))])
			b.WriteByte(' ')
		}
		return b.String()
	}
	kinds := []struct{ kind, state string }{
		{core.KindAction, core.StNext}, {core.KindAction, core.StDone}, {core.KindAction, core.StPlanned},
		{core.KindNote, core.StFiled}, {core.KindNote, core.StUnsorted}, {core.KindProject, core.StActive},
	}
	now := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
	b := s.db.NewBatch()
	flush := func() {
		check0(b.Commit(pebble.NoSync))
		b = s.db.NewBatch()
	}
	bytes := 0
	for i := 0; i < loadItems; i++ {
		k := kinds[i%len(kinds)]
		space := []string{"nis", "nis", "nis", "sven"}[i%4]
		it := core.Item{V: 1, ID: fmt.Sprintf("01JD8X4Q9V3T6M2K7P5R%06d", i), Space: space, Kind: k.kind, State: k.state,
			Title: text(6), Rev: 3, Created: now, Updated: now, UpdatedBy: "sven", Owner: []string{"sven", "eric"}[(i/7)%2],
			Original: text(10), Body: text(100), Source: core.Source{Via: "import", Ref: "obsidian:x.md"}}
		if k.kind == core.KindAction {
			it.Context = "computer"
		}
		if space == "sven" {
			it.Owner = "sven"
		}
		v := mustJSON(it)
		bytes += len(v)
		b.Set(itemKey(space, it.ID), v, nil)
		for c := 0; c < loadComments/loadItems; c++ {
			cm := core.Comment{V: 1, ID: fmt.Sprintf("01JD8X4Q9V3T6M2K7P5S%05d%d", i, c), Item: it.ID, Author: "eric", TS: now, Body: text(20)}
			b.Set(commentKey(space, it.ID, cm.ID), mustJSON(cm), nil)
		}
		if i%2000 == 1999 {
			flush()
		}
	}
	flush()
	check0(s.db.Flush())
	check0(s.Close())
	t.Logf("fixture: %d items (%.0f bytes of JSON each on average), %d comments", loadItems, float64(bytes)/loadItems, loadComments)
}

// TestLoad50k: opening a database with 50k items and 200k comments, index
// included, takes under 3 s. Typical queries take under 5 ms (STORAGE §4).
func TestLoad50k(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 50k item database")
	}
	dir := t.TempDir()
	bulk(t, dir)

	var best time.Duration
	var s *Store
	for run := 1; run <= 3; run++ {
		start := time.Now()
		s = must(Open(dir))
		d := time.Since(start)
		t.Logf("open run %d: %v", run, d.Round(time.Millisecond))
		if best == 0 || d < best {
			best = d
		}
		if run < 3 {
			check0(s.Close())
		}
	}
	defer s.Close()
	n, comments := 0, 0
	for _, sp := range s.spaces {
		n += len(sp.items)
		for _, c := range sp.comments {
			comments += c
		}
	}
	if n != loadItems || comments != loadComments {
		t.Fatalf("loaded %d items and %d comment counts", n, comments)
	}

	v := must(s.View("eric"))
	timeIt := func(name string, f func() int) time.Duration {
		best := time.Hour
		hits := 0
		for i := 0; i < 20; i++ {
			start := time.Now()
			hits = f()
			if d := time.Since(start); d < best {
				best = d
			}
		}
		t.Logf("query %-46s %8v  (%d hits)", name, best.Round(time.Microsecond), hits)
		return best
	}
	queries := []time.Duration{
		timeIt("next actions owned by eric in nis", func() int {
			return len(must(v.Items("nis", func(it *core.Item) bool {
				return it.Kind == core.KindAction && it.State == core.StNext && it.Owner == "eric"
			})))
		}),
		timeIt("active projects in nis", func() int {
			return len(must(v.Items("nis", func(it *core.Item) bool { return it.Kind == core.KindProject && it.State == core.StActive })))
		}),
		timeIt("one item by id", func() int { must(v.Item("nis", "01JD8X4Q9V3T6M2K7P5R000001")); return 1 }),
	}
	grep := timeIt("grep, two words, 37,500 items scanned", func() int { return len(v.Grep("collector zzz")) })
	timeIt("grep, common word (sorting 37,500 hits)", func() int { return len(v.Grep("bgp")) })

	if raceEnabled {
		t.Log("timing limits are not enforced under the race detector")
		return
	}
	if best > 3*time.Second {
		t.Errorf("open took %v, limit is 3s", best)
	}
	for _, d := range queries {
		if d > 5*time.Millisecond {
			t.Errorf("query took %v, limit is 5ms", d)
		}
	}
	_ = grep
}
