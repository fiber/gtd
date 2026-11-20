package web

import (
	"context"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fiber/gtd/core"
	"github.com/fiber/gtd/embed"
)

// wordEmbedder is a deterministic stand-in for the model: texts that share
// words get similar vectors.
type wordEmbedder struct{}

func (wordEmbedder) Identity() string { return "words:1" }
func (wordEmbedder) Close() error     { return nil }
func (wordEmbedder) Embed(_ context.Context, texts []string, _ embed.Task) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 64)
		for _, w := range strings.FieldsFunc(strings.ToLower(t), func(r rune) bool { return !('a' <= r && r <= 'z') }) {
			h := fnv.New64a()
			h.Write([]byte(w))
			rng := rand.New(rand.NewPCG(h.Sum64(), 1))
			for d := range v {
				v[d] += float32(rng.NormFloat64())
			}
		}
		var norm float64
		for _, x := range v {
			norm += float64(x) * float64(x)
		}
		for d := range v {
			v[d] /= float32(math.Sqrt(norm) + 1e-9)
		}
		out[i] = v
	}
	return out, nil
}

// TestSimilarityOverHTTP: semantic search, related items, "might belong
// here" and Clarify's similar items, each limited to the asking user's spaces
// (M4 acceptance: no similarity result from a non-member space).
func TestSimilarityOverHTTP(t *testing.T) {
	s := newSite(t)
	robin, dana, jo := s.login("robin"), s.login("dana"), s.login("jo")

	// Without an embedder everything answers, and says that similarity is off.
	var res SearchResult
	robin.ok("GET", "/api/v1/search?q=engine", nil, &res)
	if res.Semantic != embed.StateOff || len(res.Hits) != 0 {
		t.Fatalf("search without embeddings: %+v", res)
	}
	var status embed.Status
	robin.ok("GET", "/api/v1/ai/status", nil, &status)
	if status.State != embed.StateOff {
		t.Fatalf("status = %+v", status)
	}

	project := robin.create("robin", map[string]any{"kind": "project", "title": "Boat engine survives session resets"})
	robin.create("robin", map[string]any{"kind": "action", "title": "Ask about reset semantics of the Boat engine", "project": project})
	loose := robin.create("robin", map[string]any{"kind": "note", "title": "The engine loses Boat session state on every reset"})
	robin.create("robin", map[string]any{"kind": "note", "title": "Apple cake recipe"})
	shared := robin.create("net", map[string]any{"kind": "note", "title": "Boat engine reset design in NET"})
	jo.create("jo", map[string]any{"kind": "note", "title": "Boat engine reset, Jo's secret note"})
	var cap struct{ ID string }
	robin.ok("POST", "/api/v1/capture", map[string]any{"text": "the boat engine loses session state on reset"}, &cap)

	svc := embed.New(s.st, wordEmbedder{})
	svc.SuggestThreshold = 0.3
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svc.Start(ctx)
	s.srv.Embed = svc
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if st := svc.Status(); st.State == embed.StateOK && st.Queued == 0 && st.Vectors == 7 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("index did not build: %+v", svc.Status())
		}
	}

	titles := func(hits []Hit) string {
		var out []string
		for _, h := range hits {
			out = append(out, h.Title)
		}
		return strings.Join(out, " | ")
	}
	// The Notes list by similarity to one note: nearest first, with scores,
	// only notes, only from the asking user's spaces.
	var nl struct {
		Notes   []Brief
		Similar struct{ ID, Title, State string }
	}
	robin.ok("GET", "/api/v1/notes?similar="+loose, nil, &nl)
	var simTitles []string
	for _, n := range nl.Notes {
		simTitles = append(simTitles, n.Title)
		if n.Kind != core.KindNote || n.ID == loose || n.Score <= 0 {
			t.Errorf("similar notes: %s %q score %v", n.Kind, n.Title, n.Score)
		}
	}
	if nl.Similar.ID != loose || nl.Similar.State != embed.StateOK || len(nl.Notes) == 0 || nl.Notes[0].Title != "Boat engine reset design in NET" ||
		strings.Contains(strings.Join(simTitles, "|"), "Jo") || !sort.SliceIsSorted(nl.Notes, func(i, j int) bool { return nl.Notes[i].Score > nl.Notes[j].Score }) {
		t.Errorf("similar to the loose note: %+v, %v", nl.Similar, simTitles)
	}
	if code, _ := jo.do("GET", "/api/v1/notes?similar="+loose, nil, nil); code != 404 {
		t.Errorf("similar to a note of another user: %d", code)
	}

	// The Notes filter by meaning: no note has all three words, so the words
	// alone find nothing; by meaning the notes about it follow, only notes,
	// never another user's, and the state filter still holds.
	var ml struct {
		Notes   []Brief
		Meaning struct{ State string }
	}
	robin.ok("GET", "/api/v1/notes?q=engine+session+lost", nil, &ml)
	if len(ml.Notes) != 0 {
		t.Errorf("by words: %d notes", len(ml.Notes))
	}
	robin.ok("GET", "/api/v1/notes?meaning=1&q=engine+session+lost", nil, &ml)
	var found []string
	for _, n := range ml.Notes {
		found = append(found, n.Title)
		if n.Kind != core.KindNote || n.Score <= 0 {
			t.Errorf("by meaning: %s %q score %v", n.Kind, n.Title, n.Score)
		}
	}
	if got := strings.Join(found, "|"); ml.Meaning.State != embed.StateOK || !strings.Contains(got, "loses Boat session state") ||
		strings.Contains(got, "Jo") || strings.Contains(got, "Apple") {
		t.Errorf("notes by meaning: %q (%s)", got, ml.Meaning.State)
	}
	robin.ok("GET", "/api/v1/notes?meaning=1&state=archived&q=engine+session+lost", nil, &ml)
	if len(ml.Notes) != 0 {
		t.Errorf("by meaning among archived notes: %d", len(ml.Notes))
	}
	// A note that has the words comes first and is not repeated below.
	robin.ok("GET", "/api/v1/notes?meaning=1&q=engine+session", nil, &ml)
	seen := map[string]int{}
	for _, n := range ml.Notes {
		seen[n.ID]++
	}
	if len(ml.Notes) == 0 || ml.Notes[0].ID != loose || ml.Notes[0].Score != 0 || seen[loose] != 1 {
		t.Errorf("words first, then meaning: %+v", found)
	}

	// Search by meaning: "router" is in no title, the neighbours of the words are.
	robin.ok("GET", "/api/v1/search?q=session+state+lost", nil, &res)
	if res.Semantic != embed.StateOK || !strings.Contains(titles(res.Hits), "loses Boat session state") || strings.Contains(titles(res.Hits), "Jo") {
		t.Errorf("robin's search: %s (%s)", titles(res.Hits), res.Semantic)
	}
	for _, h := range res.Hits {
		if !h.Similar && !strings.Contains(strings.ToLower(h.Title), "session") {
			t.Errorf("hit %q is neither exact nor marked similar", h.Title)
		}
	}
	// Archived notes can be left out, among the exact hits and the similar ones.
	var lv ItemView
	robin.ok("GET", "/api/v1/items/"+loose, nil, &lv)
	robin.ok("POST", "/api/v1/ops", map[string]any{"space": "robin", "ops": []map[string]any{{"type": "transition", "item": loose, "base_rev": lv.Rev,
		"changes": []map[string]any{{"field": "state", "new": "archived"}}}}}, nil)
	robin.ok("GET", "/api/v1/search?q=session+state+lost", nil, &res)
	if !strings.Contains(titles(res.Hits), "loses Boat session state") {
		t.Errorf("an archived note is not found any more: %s", titles(res.Hits))
	}
	robin.ok("GET", "/api/v1/search?archived=0&q=session+state+lost", nil, &res)
	if strings.Contains(titles(res.Hits), "loses Boat session state") || len(res.Hits) == 0 {
		t.Errorf("search without archived: %s", titles(res.Hits))
	}
	robin.ok("GET", "/api/v1/search?archived=0&q=loses", nil, &res)
	if len(res.Hits) != 0 && strings.Contains(titles(res.Hits), "loses Boat") {
		t.Errorf("exact search without archived: %s", titles(res.Hits))
	}
	robin.ok("POST", "/api/v1/ops", map[string]any{"space": "robin", "ops": []map[string]any{{"type": "transition", "item": loose, "base_rev": lv.Rev + 1,
		"changes": []map[string]any{{"field": "state", "new": "unsorted"}}}}}, nil) // as it was, for what follows
	// Dana shares net with robin, nothing else. Jo shares nothing.
	dana.ok("GET", "/api/v1/search?q=boat+engine+reset", nil, &res)
	if got := titles(res.Hits); got != "Boat engine reset design in NET" {
		t.Errorf("dana's search: %s", got)
	}
	jo.ok("GET", "/api/v1/search?q=boat+engine+reset", nil, &res)
	if got := titles(res.Hits); got != "Boat engine reset, Jo's secret note" {
		t.Errorf("jo's search: %s", got)
	}

	// Related items on an item's page.
	var iv ItemView
	robin.ok("GET", "/api/v1/items/"+shared, nil, &iv)
	if iv.RelatedState != embed.StateOK || len(iv.Related) == 0 {
		t.Fatalf("related: %+v (%s)", iv.Related, iv.RelatedState)
	}
	for _, r := range iv.Related {
		if strings.Contains(r.Title, "Jo") || r.Kind == core.KindCapture {
			t.Errorf("related shows %q", r.Title)
		}
	}
	dana.ok("GET", "/api/v1/items/"+shared, nil, &iv)
	if len(iv.Related) != 0 {
		t.Errorf("dana sees related items from robin's personal space: %+v", iv.Related)
	}

	// "Might belong here": the loose note, not the cake, not the filed action.
	var pl Plan
	robin.ok("GET", "/api/v1/plan/"+project, nil, &pl)
	if len(pl.Suggested) != 1 || pl.Suggested[0].ID != loose {
		t.Errorf("suggested = %+v", pl.Suggested)
	}

	// Clarify: similar items, a duplicate option for those in the same space,
	// and the nearest project as a suggestion.
	var w struct {
		Session string
		Step    struct {
			Options []struct{ Value, Text string }
			Data    struct {
				Similar []struct {
					ID, Space, Title string
					Score            float32
				}
			}
		}
	}
	robin.ok("POST", "/api/v1/wizard/clarify/start", map[string]string{"item": cap.ID}, &w)
	if len(w.Step.Data.Similar) == 0 || len(w.Step.Data.Similar) > 3 {
		t.Fatalf("clarify shows %d similar items", len(w.Step.Data.Similar))
	}
	dup := ""
	for _, o := range w.Step.Options {
		if strings.HasPrefix(o.Value, "dup:") {
			if o.Value == "dup:"+shared {
				t.Error("a duplicate option for an item in another space")
			}
			if o.Value == "dup:"+loose {
				dup = o.Value
			}
		}
	}
	if dup == "" {
		t.Fatalf("no duplicate option for the loose note: %+v", w.Step.Options)
	}
	var done struct {
		Done bool
		Ops  []core.Op
	}
	robin.ok("POST", "/api/v1/wizard/clarify/"+w.Session+"/step", map[string]any{"choice": dup}, &done)
	if !done.Done || len(done.Ops) != 2 {
		t.Fatalf("duplicate: %+v", done)
	}
	robin.ok("GET", "/api/v1/items/"+loose, nil, &iv)
	if iv.Comments != 1 {
		t.Errorf("the capture's text was not appended to the item: %d comments", iv.Comments)
	}
	robin.ok("GET", "/api/v1/items/"+cap.ID, nil, &iv)
	if iv.State != core.StTrashed || iv.Original != "the boat engine loses session state on reset" {
		t.Errorf("capture after duplicate: %s, original %q", iv.State, iv.Original)
	}

	// Reindex is an admin command: not with a web session.
	if code, _ := robin.do("POST", "/api/v1/admin/reindex", nil, nil); code != 403 {
		t.Errorf("reindex with a web session: %d", code)
	}
}
