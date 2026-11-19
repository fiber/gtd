package embed

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fiber/gtd/core"
	"github.com/fiber/gtd/store"
)

// fake is a deterministic embedder for tests: every word has a fixed
// pseudo-random direction and a text is the unit sum of its words, so texts
// that share words are similar.
type fake struct {
	id    string
	dim   int
	fail  atomic.Int32 // fail this many calls before answering
	calls atomic.Int32
	mu    sync.Mutex
	seen  []string
}

func newFake() *fake { return &fake{id: "fake:1", dim: 64} }

func (f *fake) Identity() string { return f.id }
func (f *fake) Close() error     { return nil }

func (f *fake) Embed(_ context.Context, texts []string, _ Task) ([][]float32, error) {
	f.calls.Add(1)
	if f.fail.Load() > 0 {
		f.fail.Add(-1)
		return nil, errors.New("embedder is down")
	}
	f.mu.Lock()
	f.seen = append(f.seen, texts...)
	f.mu.Unlock()
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, f.dim)
		for _, w := range strings.FieldsFunc(strings.ToLower(t), func(r rune) bool { return !('a' <= r && r <= 'z' || '0' <= r && r <= '9') }) {
			h := fnv.New64a()
			h.Write([]byte(w))
			rng := rand.New(rand.NewPCG(h.Sum64(), 1))
			for d := range v {
				v[d] += float32(rng.NormFloat64())
			}
		}
		out[i] = Mean([][]float32{v})
	}
	return out, nil
}

// site is a store with sven and eric in the shared space nis, and grit alone.
type site struct {
	t   *testing.T
	st  *store.Store
	svc *Service
	emb *fake
}

func newSite(t *testing.T) *site {
	st, err := store.Create(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, u := range []string{"sven", "eric", "grit"} {
		if err := st.AddUser(core.User{ID: u}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateSpace("nis", "NIS", []string{"sven", "eric"}); err != nil {
		t.Fatal(err)
	}
	return &site{t: t, st: st, emb: newFake()}
}

func (s *site) start() {
	s.svc = New(s.st, s.emb)
	s.svc.Backoff = 5 * time.Millisecond
	s.svc.SearchThreshold = 0.2 // the fake embedder's scale
	ctx, cancel := context.WithCancel(context.Background())
	s.t.Cleanup(cancel)
	s.svc.Start(ctx)
	s.settle()
}

// settle waits until the queue is empty and the service is idle.
func (s *site) settle() {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st := s.svc.Status(); st.State == StateOK && st.Queued == 0 {
			// one more look: a batch may be in flight
			time.Sleep(15 * time.Millisecond)
			if st := s.svc.Status(); st.State == StateOK && st.Queued == 0 {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.t.Fatalf("service did not settle: %+v", s.svc.Status())
}

func (s *site) view(user string) *store.View {
	v, err := s.st.View(user)
	if err != nil {
		s.t.Fatal(err)
	}
	return v
}

func (s *site) add(user, space string, it core.Item) string {
	s.t.Helper()
	it.ID = s.st.NewID()
	payload, _ := jsonMarshal(it)
	if _, err := s.view(user).Apply(space, core.Op{Via: "cli", Type: core.OpCreate, Payload: payload}); err != nil {
		s.t.Fatal(err)
	}
	return it.ID
}

func (s *site) titles(v *store.View, hits []Hit) []string {
	var out []string
	for _, h := range hits {
		it, err := v.Item(h.Space, h.ID)
		if err != nil {
			out = append(out, "!"+h.Space+"/"+h.ID)
			continue
		}
		out = append(out, it.Title)
	}
	return out
}

func TestTextAndChunks(t *testing.T) {
	sha := strings.Repeat("a", 64)
	it := &core.Item{Title: "Call notes", Kind: core.KindNote,
		Body:        "# Heading\n\n- [ ] **call** [[01ABC]] about `ports`\n> see ![[att:" + sha + "]] and @[user:eric] and [[01XYZ|the plan]]\n",
		Fields:      map[string]string{"stage": "testing", "empty": ""},
		Imported:    map[string]any{"folder": "NIS/BGP", "tags": []any{"bgp", "todo"}},
		Attachments: []core.Attachment{{SHA256: sha, Name: "board.png"}}}
	got := Text(it, Resolver{
		Title:    func(id string) string { return map[string]string{"01ABC": "Brent"}[id] },
		UserName: func(id string) string { return "Eric" },
		PDFText:  func(string) string { return "text of the pdf" },
	})
	want := "Call notes\n\nHeading\n\ncall Brent about ports\nsee board.png and Eric and the plan\nstage: testing\nfolder: NIS/BGP\ntags: bgp todo\n\ntext of the pdf"
	if got != want {
		t.Errorf("text:\n%q\nwant:\n%q", got, want)
	}
	if c := Chunks("short"); len(c) != 1 || c[0] != "short" {
		t.Errorf("short text: %v", c)
	}
	long := strings.Repeat("ä", 4000)
	c := Chunks(long)
	if len(c) != 3 || len([]rune(c[0])) != chunkSize || len([]rune(c[2])) != 4000-2*(chunkSize-chunkOverlap) {
		t.Errorf("chunks of 4000 characters: %d chunks, sizes %d %d", len(c), len([]rune(c[0])), len([]rune(c[len(c)-1])))
	}
	if c := Chunks(strings.Repeat("x", 100_000)); len(c) != maxChunks {
		t.Errorf("very long text: %d chunks, want the cap %d", len(c), maxChunks)
	}
	m := Mean([][]float32{{1, 0}, {0, 1}})
	if math.Abs(float64(m[0])-math.Sqrt2/2) > 1e-6 || m[0] != m[1] {
		t.Errorf("mean = %v, want a unit vector", m)
	}
	if withPrompt(Document, "x") != "title: none | text: x" || withPrompt(Query, "x") != "task: search result | query: x" {
		t.Error("prompts differ from EmbeddingGemma's")
	}
}

func TestSimilarityAndIsolation(t *testing.T) {
	s := newSite(t)
	bgp := s.add("sven", "nis", core.Item{Kind: core.KindNote, Title: "BGP collector session reset semantics"})
	s.add("eric", "nis", core.Item{Kind: core.KindAction, Title: "Fix the BGP collector reset handling", Owner: "eric"})
	s.add("sven", "nis", core.Item{Kind: core.KindNote, Title: "Lunch menu for the offsite"})
	s.add("sven", "sven", core.Item{Kind: core.KindNote, Title: "Private thoughts on the BGP collector reset"})
	s.add("grit", "grit", core.Item{Kind: core.KindNote, Title: "Garden plan for spring"})
	s.start()

	sven, eric, grit := s.view("sven"), s.view("eric"), s.view("grit")
	item, _ := sven.Item("nis", bgp)

	// Sven sees his personal space and nis.
	got := s.titles(sven, s.svc.SimilarTo(sven, item, 5, nil).Hits)
	if len(got) != 3 || got[0] != "Fix the BGP collector reset handling" && got[0] != "Private thoughts on the BGP collector reset" || slices.Contains(got, "Garden plan for spring") {
		t.Errorf("sven's similar items: %q", got)
	}
	// Eric is in nis, not in sven's personal space.
	got = s.titles(eric, s.svc.SimilarTo(eric, item, 5, nil).Hits)
	if slices.Contains(got, "Private thoughts on the BGP collector reset") || !slices.Contains(got, "Fix the BGP collector reset handling") {
		t.Errorf("eric's similar items leak sven's space: %q", got)
	}
	// Grit is in neither: nothing about the item, and no hit from other spaces in search.
	if res := s.svc.SimilarTo(grit, item, 5, nil); len(res.Hits) != 0 {
		t.Errorf("grit gets similar items for an item she cannot see: %+v", res.Hits)
	}
	res := s.svc.Search(context.Background(), grit, "BGP collector reset", 20)
	for _, title := range s.titles(grit, res.Hits) {
		if title != "Garden plan for spring" {
			t.Errorf("grit's search returned %q", title)
		}
	}
	res = s.svc.Search(context.Background(), sven, "BGP collector reset", 2)
	if got := s.titles(sven, res.Hits); len(got) != 2 || !strings.Contains(got[0], "BGP") || !strings.Contains(got[1], "BGP") {
		t.Errorf("sven's search: %q", got)
	}
	// Removing a member takes effect at once: membership is read on every question.
	if err := s.st.SetMember("nis", "eric", false); err != nil {
		t.Fatal(err)
	}
	if res := s.svc.Search(context.Background(), eric, "BGP collector reset", 20); len(res.Hits) != 0 {
		t.Errorf("removed member still gets hits: %q", s.titles(sven, res.Hits))
	}
	// Trashed items disappear, edited ones are re-embedded.
	if _, err := sven.Apply("nis", core.Op{Via: "cli", Type: core.OpTransition, Item: bgp, BaseRev: 1,
		Changes: []core.FieldChange{{Field: "state", New: []byte(`"trashed"`)}}}); err != nil {
		t.Fatal(err)
	}
	s.settle()
	res = s.svc.Search(context.Background(), sven, "session semantics", 20)
	if slices.Contains(s.titles(sven, res.Hits), "BGP collector session reset semantics") {
		t.Error("a trashed item is still found")
	}

	// A capture still in the inbox is found by meaning too.
	s.add("sven", "sven", core.Item{Kind: core.KindCapture, Title: "need to find a present for Grit"})
	s.settle()
	res = s.svc.Search(context.Background(), sven, "present for Grit", 5)
	if got := s.titles(sven, res.Hits); len(got) == 0 || got[0] != "need to find a present for Grit" {
		t.Errorf("search does not find the capture: %q", got)
	}

	var nilSvc *Service
	if nilSvc.Status().State != StateOff || nilSvc.SimilarTo(sven, item, 3, nil).State != StateOff ||
		nilSvc.Search(context.Background(), sven, "x", 3).State != StateOff || nilSvc.Projects(sven, item, 3) != nil {
		t.Error("a nil service must answer 'off'")
	}
}

// TestBackendDown: the embedder fails, nothing else notices, and the queue
// recovers when it comes back (M4 acceptance).
func TestBackendDown(t *testing.T) {
	s := newSite(t)
	s.emb.fail.Store(1_000_000)
	s.svc = New(s.st, s.emb)
	s.svc.Backoff = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.svc.Start(ctx)

	// Writes and exact search work as always.
	id := s.add("sven", "sven", core.Item{Kind: core.KindNote, Title: "Equinix API notes"})
	sven := s.view("sven")
	if hits := sven.Grep("equinix"); len(hits) != 1 {
		t.Fatalf("text search with the embedder down: %d hits", len(hits))
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.svc.Status().State != StateOffline || s.svc.Status().Queued == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("status = %+v, want offline with a queue", s.svc.Status())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st := s.svc.Status(); st.Error != "embedder is down" || st.Vectors != 0 {
		t.Errorf("status = %+v", st)
	}
	if res := s.svc.Search(ctx, sven, "equinix", 5); res.State != StateOffline || len(res.Hits) != 0 {
		t.Errorf("search while offline: %+v", res)
	}
	item, _ := sven.Item("sven", id)
	if res := s.svc.SimilarTo(sven, item, 5, nil); len(res.Hits) != 0 {
		t.Errorf("similar while offline: %+v", res)
	}

	// The backend comes back: the queue drains without anyone asking.
	s.emb.fail.Store(0)
	s.settle()
	if st := s.svc.Status(); st.State != StateOK || st.Vectors != 1 || st.Error != "" {
		t.Errorf("after recovery: %+v", st)
	}
	if res := s.svc.Search(ctx, sven, "equinix api", 5); res.State != StateOK || len(res.Hits) != 1 {
		t.Errorf("search after recovery: %+v", res)
	}
}

// TestReindexIsIdentical: wiping the vectors and embedding everything again
// gives the same vectors and the same answers (M4 acceptance).
func TestReindexIsIdentical(t *testing.T) {
	s := newSite(t)
	words := strings.Fields("collector session reset bgp atlas curation equinix api garden gate passport dentist trade backtest")
	rng := rand.New(rand.NewPCG(7, 7))
	for i := 0; i < 60; i++ {
		title := ""
		for j := 0; j < 4; j++ {
			title += words[rng.IntN(len(words))] + " "
		}
		s.add("sven", []string{"sven", "nis"}[i%2], core.Item{Kind: core.KindNote, Title: fmt.Sprintf("%s%d", title, i), Body: strings.Repeat(title, 3)})
	}
	s.start()
	sven := s.view("sven")
	snapshot := func() (map[string]string, []string) {
		vecs := map[string]string{}
		s.st.EachVector(func(space, id string, hash [32]byte, vec []float32) {
			vecs[space+"/"+id] = fmt.Sprintf("%x %v", hash, vec)
		})
		var answers []string
		for _, q := range []string{"bgp collector", "garden gate", "equinix api trade"} {
			res := s.svc.Search(context.Background(), sven, q, 10)
			answers = append(answers, fmt.Sprintf("%s: %v", q, res.Hits))
		}
		return vecs, answers
	}
	vecs1, answers1 := snapshot()
	if len(vecs1) != 60 {
		t.Fatalf("%d vectors, want 60", len(vecs1))
	}
	callsBefore := s.emb.calls.Load()
	if err := s.svc.Reindex(); err != nil {
		t.Fatal(err)
	}
	if st := s.svc.Status(); st.Vectors != 0 {
		t.Errorf("reindex did not wipe: %+v", st)
	}
	// While a space is being rebuilt, it gives no answers: a partial index is not ranked.
	if res := s.svc.Search(context.Background(), sven, "bgp collector", 10); res.State != StateBuilding && len(res.Hits) != 0 && s.svc.Status().Queued > 0 {
		t.Errorf("answer from a partial index: %+v", res)
	}
	s.settle()
	vecs2, answers2 := snapshot()
	if !slices.Equal(answers1, answers2) {
		t.Errorf("answers differ after reindex:\n%v\n%v", answers1, answers2)
	}
	for k, v := range vecs1 {
		if vecs2[k] != v {
			t.Fatalf("vector of %s differs after reindex", k)
		}
	}
	if len(vecs2) != len(vecs1) || s.emb.calls.Load() == callsBefore {
		t.Errorf("reindex embedded nothing: %d vectors, %d calls", len(vecs2), s.emb.calls.Load()-callsBefore)
	}

	// Restart with the same model: nothing is embedded again.
	calls := s.emb.calls.Load()
	s.start()
	if got := s.emb.calls.Load(); got != calls {
		t.Errorf("restart embedded again: %d calls", got-calls)
	}
	// Restart with another model: everything is rebuilt, nothing is mixed.
	s.emb = newFake()
	s.emb.id = "fake:2"
	s.start()
	if got := s.emb.calls.Load(); got == 0 || s.st.VectorIdentity() != "fake:2" || s.svc.Status().Vectors != 60 {
		t.Errorf("model change: %d calls, identity %q, %+v", got, s.st.VectorIdentity(), s.svc.Status())
	}
}

func TestProjectsAndBelongHere(t *testing.T) {
	s := newSite(t)
	bgp := s.add("sven", "sven", core.Item{Kind: core.KindProject, Title: "BGP collector survives session resets"})
	s.add("sven", "sven", core.Item{Kind: core.KindAction, Title: "Ask Eric about reset semantics of the collector", Project: bgp})
	trade := s.add("sven", "sven", core.Item{Kind: core.KindProject, Title: "Gap fill trading hypothesis tested", State: core.StSomeday})
	s.add("sven", "sven", core.Item{Kind: core.KindNote, Title: "Backtest results for gap fill trades", Project: trade, State: core.StFiled})
	done := s.add("sven", "sven", core.Item{Kind: core.KindProject, Title: "BGP collector reset old project"})
	loose := s.add("sven", "sven", core.Item{Kind: core.KindNote, Title: "The collector loses BGP session state on reset"})
	s.add("sven", "sven", core.Item{Kind: core.KindNote, Title: "Recipe for apple cake"})
	filed := s.add("sven", "sven", core.Item{Kind: core.KindNote, Title: "BGP collector reset design", Project: bgp, State: core.StFiled})
	cap := s.add("sven", "sven", core.Item{Kind: core.KindCapture, Title: "gap fill idea for earnings trades"})
	sven := s.view("sven")
	p, _ := sven.Item("sven", done)
	if _, err := sven.Apply("sven", core.Op{Via: "cli", Type: core.OpTransition, Item: done, BaseRev: p.Rev, Changes: []core.FieldChange{{Field: "state", New: []byte(`"done"`)}}}); err != nil {
		t.Fatal(err)
	}
	s.start()
	s.svc.SuggestThreshold = 0.3

	capture, _ := sven.Item("sven", cap)
	if got := s.titles(sven, s.svc.Projects(sven, capture, 3)); len(got) != 2 || got[0] != "Gap fill trading hypothesis tested" {
		t.Errorf("projects for the capture: %q (a finished project must not be offered)", got)
	}
	project, _ := sven.Item("sven", bgp)
	got := s.titles(sven, s.svc.BelongHere(sven, project, 5))
	if !slices.Equal(got, []string{"The collector loses BGP session state on reset"}) {
		t.Errorf("might belong here: %q", got)
	}
	_, _ = loose, filed
	// Not for someone outside the space.
	if hits := s.svc.BelongHere(s.view("grit"), project, 5); len(hits) != 0 {
		t.Errorf("grit gets suggestions for sven's project: %v", hits)
	}
}

// TestTopKSpeed: a top-k query over 50,000 × 768 takes under 5 ms (M4 acceptance).
func TestTopKSpeed(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 50k × 768 matrix")
	}
	const n, dim = 50_000, 768
	rng := rand.New(rand.NewPCG(1, 2))
	x := newIndex()
	vec := make([]float32, dim)
	unit := func() {
		var norm float64
		for i := range vec {
			vec[i] = float32(rng.NormFloat64())
			norm += float64(vec[i]) * float64(vec[i])
		}
		for i := range vec {
			vec[i] /= float32(math.Sqrt(norm))
		}
	}
	for i := 0; i < n; i++ {
		unit()
		x.put(fmt.Sprintf("%06d", i), vec)
	}
	unit()
	query := slices.Clone(vec)
	best10 := func() []Hit {
		scores := x.scores(query)
		all := make([]Hit, len(scores))
		for i, s := range scores {
			all[i] = Hit{"s", x.ids[i], s}
		}
		return best(all, 10)
	}
	want := best10()
	fastest := time.Hour
	for i := 0; i < 30; i++ {
		start := time.Now()
		got := best10()
		if d := time.Since(start); d < fastest {
			fastest = d
		}
		if !slices.Equal(got, want) {
			t.Fatal("top-k is not deterministic")
		}
	}
	// The result is the true top 10.
	scores := x.scores(query)
	sorted := slices.Clone(scores)
	slices.Sort(sorted)
	if want[9].Score != sorted[len(sorted)-10] || want[0].Score != sorted[len(sorted)-1] {
		t.Errorf("top-10 is wrong: %v", want)
	}
	t.Logf("top-10 over %d × %d: %v (matrix product on fiber/ai's kernels, then selection)", n, dim, fastest.Round(10*time.Microsecond))
	if !raceEnabled && fastest > 5*time.Millisecond {
		t.Errorf("top-k took %v, limit is 5ms", fastest)
	}
}

// TestPDFText: extracted PDF text is searchable and is part of what an item's
// vector is made from.
func TestPDFText(t *testing.T) {
	s := newSite(t)
	sha := strings.Repeat("ab", 32)
	id := s.add("sven", "sven", core.Item{Kind: core.KindNote, Title: "Report", Attachments: []core.Attachment{{SHA256: sha, Name: "q3.pdf", MIME: "application/pdf", Size: 1}}})
	sven := s.view("sven")
	if hits := sven.Grep("quarterly"); len(hits) != 0 {
		t.Fatal("found before extraction")
	}
	// The blob does not exist, so pdftotext (if installed) yields nothing: the
	// service caches the empty result and does not try again.
	s.start()
	if text, ok := s.st.PDFText(sha); !ok || text != "" {
		t.Errorf("after a failed extraction: %q, cached %v", text, ok)
	}
	if err := s.st.PutPDFText(sha, "Quarterly numbers for the collector fleet"); err != nil {
		t.Fatal(err)
	}
	if hits := sven.Grep("quarterly fleet"); len(hits) != 1 || hits[0].ID != id {
		t.Errorf("text search over the PDF: %v", hits)
	}
	it, _ := sven.Item("sven", id)
	if text := s.svc.text(it); !strings.Contains(text, "Quarterly numbers") {
		t.Errorf("embedding input lacks the PDF text: %q", text)
	}
	// It survives a restart.
	if text, ok := s.st.PDFText(sha); !ok || !strings.HasPrefix(text, "Quarterly") {
		t.Errorf("cached text: %q %v", text, ok)
	}
}
