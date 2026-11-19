package web

import (
	"context"
	"hash/fnv"
	"math"
	"math/rand/v2"
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
	sven, eric, grit := s.login("sven"), s.login("eric"), s.login("grit")

	// Without an embedder everything answers, and says that similarity is off.
	var res SearchResult
	sven.ok("GET", "/api/v1/search?q=collector", nil, &res)
	if res.Semantic != embed.StateOff || len(res.Hits) != 0 {
		t.Fatalf("search without embeddings: %+v", res)
	}
	var status embed.Status
	sven.ok("GET", "/api/v1/ai/status", nil, &status)
	if status.State != embed.StateOff {
		t.Fatalf("status = %+v", status)
	}

	project := sven.create("sven", map[string]any{"kind": "project", "title": "BGP collector survives session resets"})
	sven.create("sven", map[string]any{"kind": "action", "title": "Ask about reset semantics of the BGP collector", "project": project})
	loose := sven.create("sven", map[string]any{"kind": "note", "title": "The collector loses BGP session state on every reset"})
	sven.create("sven", map[string]any{"kind": "note", "title": "Apple cake recipe"})
	shared := sven.create("nis", map[string]any{"kind": "note", "title": "BGP collector reset design in NIS"})
	grit.create("grit", map[string]any{"kind": "note", "title": "BGP collector reset, Grit's secret note"})
	var cap struct{ ID string }
	sven.ok("POST", "/api/v1/capture", map[string]any{"text": "the bgp collector loses session state on reset"}, &cap)

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
	// Search by meaning: "router" is in no title, the neighbours of the words are.
	sven.ok("GET", "/api/v1/search?q=session+state+lost", nil, &res)
	if res.Semantic != embed.StateOK || !strings.Contains(titles(res.Hits), "loses BGP session state") || strings.Contains(titles(res.Hits), "Grit") {
		t.Errorf("sven's search: %s (%s)", titles(res.Hits), res.Semantic)
	}
	for _, h := range res.Hits {
		if !h.Similar && !strings.Contains(strings.ToLower(h.Title), "session") {
			t.Errorf("hit %q is neither exact nor marked similar", h.Title)
		}
	}
	// Eric shares nis with sven, nothing else. Grit shares nothing.
	eric.ok("GET", "/api/v1/search?q=bgp+collector+reset", nil, &res)
	if got := titles(res.Hits); got != "BGP collector reset design in NIS" {
		t.Errorf("eric's search: %s", got)
	}
	grit.ok("GET", "/api/v1/search?q=bgp+collector+reset", nil, &res)
	if got := titles(res.Hits); got != "BGP collector reset, Grit's secret note" {
		t.Errorf("grit's search: %s", got)
	}

	// Related items on an item's page.
	var iv ItemView
	sven.ok("GET", "/api/v1/items/"+shared, nil, &iv)
	if iv.RelatedState != embed.StateOK || len(iv.Related) == 0 {
		t.Fatalf("related: %+v (%s)", iv.Related, iv.RelatedState)
	}
	for _, r := range iv.Related {
		if strings.Contains(r.Title, "Grit") || r.Kind == core.KindCapture {
			t.Errorf("related shows %q", r.Title)
		}
	}
	eric.ok("GET", "/api/v1/items/"+shared, nil, &iv)
	if len(iv.Related) != 0 {
		t.Errorf("eric sees related items from sven's personal space: %+v", iv.Related)
	}

	// "Might belong here": the loose note, not the cake, not the filed action.
	var pl Plan
	sven.ok("GET", "/api/v1/plan/"+project, nil, &pl)
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
	sven.ok("POST", "/api/v1/wizard/clarify/start", map[string]string{"item": cap.ID}, &w)
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
	sven.ok("POST", "/api/v1/wizard/clarify/"+w.Session+"/step", map[string]any{"choice": dup}, &done)
	if !done.Done || len(done.Ops) != 2 {
		t.Fatalf("duplicate: %+v", done)
	}
	sven.ok("GET", "/api/v1/items/"+loose, nil, &iv)
	if iv.Comments != 1 {
		t.Errorf("the capture's text was not appended to the item: %d comments", iv.Comments)
	}
	sven.ok("GET", "/api/v1/items/"+cap.ID, nil, &iv)
	if iv.State != core.StTrashed || iv.Original != "the bgp collector loses session state on reset" {
		t.Errorf("capture after duplicate: %s, original %q", iv.State, iv.Original)
	}

	// Reindex is an admin command: not with a web session.
	if code, _ := sven.do("POST", "/api/v1/admin/reindex", nil, nil); code != 403 {
		t.Errorf("reindex with a web session: %d", code)
	}
}
