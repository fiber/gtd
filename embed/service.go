package embed

import (
	"context"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fiber/ai/cluster"

	"github.com/fiber/gtd/core"
	"github.com/fiber/gtd/store"
)

// States a similarity answer can be in. Anything but StateOK means "no
// similarity now": callers fall back, and say so.
const (
	StateOK       = "ok"
	StateOff      = "off"      // no embedder configured
	StateOffline  = "offline"  // the embedder does not answer
	StateBuilding = "building" // a space's vectors are being (re)built
)

type key struct{ space, id string }

// Service keeps one vector per item current in the background and answers
// similarity questions. All its methods work on a nil *Service and then
// report StateOff.
//
// Isolation (invariant 4): every question is asked through a store.View. Only
// the spaces that View lists are searched, and every hit is read back through
// the View before it is returned.
type Service struct {
	st  *store.Store
	emb Embedder

	// DupThreshold and SuggestThreshold are the config values of
	// similarity.dup_threshold and similarity.suggest_threshold.
	DupThreshold, SuggestThreshold float32
	// SearchThreshold is the lowest score a search hit by meaning may have.
	// A query and a document score lower against each other than two
	// documents do, so this is its own, lower value.
	SearchThreshold float32
	// Backoff is the first wait after an embedder error. It doubles up to a minute.
	Backoff time.Duration

	mu       sync.RWMutex
	spaces   map[string]*index
	hashes   map[key][32]byte
	building map[string]bool // spaces whose first build after start or reset is not finished
	identity string
	state    string
	lastErr  string

	qmu    sync.Mutex
	queue  []key
	queued map[key]bool
	wake   chan struct{}
}

// connector is implemented by adapters that must reach a server before their
// identity is known.
type connector interface {
	Connect(ctx context.Context) error
}

// New returns a service. Start runs it.
func New(st *store.Store, emb Embedder) *Service {
	return &Service{st: st, emb: emb, DupThreshold: 0.92, SuggestThreshold: 0.55, SearchThreshold: 0.30, Backoff: time.Second,
		spaces: map[string]*index{}, hashes: map[key][32]byte{}, building: map[string]bool{},
		state: StateOffline, queued: map[key]bool{}, wake: make(chan struct{}, 1)}
}

// Start loads the stored vectors, queues what is missing or stale, and embeds
// in the background until ctx ends. It returns at once: the UI never waits
// for embeddings (invariant 7).
func (s *Service) Start(ctx context.Context) {
	s.st.OnCommit = func(space string, items []*core.Item) {
		for _, it := range items {
			s.enqueue(key{space, it.ID})
		}
	}
	go s.run(ctx)
}

// prepare makes the stored vectors match the embedder's identity and queues
// every item whose vector is missing or stale.
func (s *Service) prepare(ctx context.Context) error {
	if c, ok := s.emb.(connector); ok {
		if err := c.Connect(ctx); err != nil {
			return err
		}
	}
	id := s.emb.Identity()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.identity == id {
		return nil // reconnected to the same model
	}
	s.spaces, s.hashes, s.building = map[string]*index{}, map[key][32]byte{}, map[string]bool{}
	if s.st.VectorIdentity() != id {
		// Another model, another prompt set, or the first run: the old vectors
		// are not comparable with new ones. Drop them all.
		if err := s.st.ResetVectors(id); err != nil {
			return err
		}
	} else {
		err := s.st.EachVector(func(space, itemID string, hash [32]byte, vec []float32) {
			s.index(space).put(itemID, vec)
			s.hashes[key{space, itemID}] = hash
		})
		if err != nil {
			return err
		}
	}
	s.identity = id
	// Reconcile with the items as they are now.
	var items []*core.Item
	s.st.EachItem(func(it *core.Item) { items = append(items, it) })
	wanted := map[key]bool{}
	for _, it := range items {
		k := key{it.Space, it.ID}
		if it.State == core.StTrashed {
			continue
		}
		wanted[k] = true
		if hash, ok := s.hashes[k]; !ok || hash != Hash(s.text(it)) {
			if !ok {
				s.building[it.Space] = true
			}
			s.enqueue(k)
		}
	}
	for k := range s.hashes {
		if !wanted[k] {
			s.enqueue(k) // trashed or gone: the worker removes the vector
		}
	}
	return nil
}

func (s *Service) index(space string) *index {
	x := s.spaces[space]
	if x == nil {
		x = newIndex()
		s.spaces[space] = x
	}
	return x
}

func (s *Service) enqueue(k key) {
	s.qmu.Lock()
	if !s.queued[k] {
		s.queued[k] = true
		s.queue = append(s.queue, k)
	}
	s.qmu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// text builds an item's input text. Links resolve only inside the item's own
// space, so no text of another space leaks into a vector.
func (s *Service) text(it *core.Item) string {
	return Text(it, Resolver{
		Title: func(id string) string {
			if x := s.st.ItemOf(it.Space, id); x != nil {
				return x.Title
			}
			return ""
		},
		UserName: func(id string) string {
			if u := s.st.User(id); u != nil {
				return u.Name
			}
			return ""
		},
		PDFText: func(sha string) string {
			t, _ := s.st.PDFText(sha)
			return t
		},
	})
}

const batchItems = 8

func (s *Service) run(ctx context.Context) {
	wait := s.Backoff
	fail := func(err error) bool { // reports whether to go on
		s.mu.Lock()
		s.state, s.lastErr = StateOffline, err.Error()
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-time.After(wait):
		}
		wait = min(wait*2, time.Minute)
		return true
	}
	prepared := false
	for {
		if !prepared {
			if err := s.prepare(ctx); err != nil {
				if !fail(err) {
					return
				}
				continue
			}
			prepared = true
		}
		batch := s.take()
		if len(batch) == 0 {
			s.mu.Lock()
			s.state, s.lastErr = StateOK, ""
			s.building = map[string]bool{}
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			}
			continue
		}
		if err := s.embed(ctx, batch); err != nil {
			s.putBack(batch)
			if ctx.Err() != nil {
				return
			}
			// The server may have come back with another model: check again.
			if _, ok := s.emb.(connector); ok {
				prepared = false
			}
			if !fail(err) {
				return
			}
			continue
		}
		wait = s.Backoff
		s.mu.Lock()
		s.state, s.lastErr = StateOK, ""
		s.mu.Unlock()
	}
}

func (s *Service) take() []key {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	n := min(batchItems, len(s.queue))
	batch := append([]key(nil), s.queue[:n]...)
	s.queue = s.queue[n:]
	for _, k := range batch {
		delete(s.queued, k)
	}
	return batch
}

func (s *Service) putBack(batch []key) {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	var keep []key
	for _, k := range batch {
		if !s.queued[k] {
			s.queued[k] = true
			keep = append(keep, k)
		}
	}
	s.queue = append(keep, s.queue...)
}

// embed brings the vectors of a batch of items up to date.
func (s *Service) embed(ctx context.Context, batch []key) error {
	type job struct {
		k      key
		hash   [32]byte
		chunks int
	}
	var jobs []job
	var texts []string
	for _, k := range batch {
		it := s.st.ItemOf(k.space, k.id)
		if it == nil || it.State == core.StTrashed {
			s.mu.Lock()
			if x := s.spaces[k.space]; x != nil {
				x.delete(k.id)
			}
			delete(s.hashes, k)
			s.mu.Unlock()
			s.st.DeleteVector(k.space, k.id)
			continue
		}
		s.extractPDFs(ctx, it)
		text := s.text(it)
		hash := Hash(text)
		s.mu.RLock()
		same := s.hashes[k] == hash
		s.mu.RUnlock()
		if same {
			continue // the same input was embedded before
		}
		chunks := Chunks(text)
		jobs = append(jobs, job{k, hash, len(chunks)})
		texts = append(texts, chunks...)
	}
	if len(texts) == 0 {
		return nil
	}
	ectx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	vecs, err := s.emb.Embed(ectx, texts, Document)
	if err != nil {
		return err
	}
	at := 0
	for _, j := range jobs {
		vec := Mean(vecs[at : at+j.chunks])
		at += j.chunks
		if err := s.st.PutVector(j.k.space, j.k.id, j.hash, vec); err != nil {
			return err
		}
		s.mu.Lock()
		s.index(j.k.space).put(j.k.id, vec)
		s.hashes[j.k] = j.hash
		s.mu.Unlock()
	}
	return nil
}

// extractPDFs runs pdftotext, if it is installed, on PDF attachments that have
// no cached text yet. The result (possibly empty) is cached by blob hash.
func (s *Service) extractPDFs(ctx context.Context, it *core.Item) {
	for _, a := range it.Attachments {
		if a.MIME != "application/pdf" && !strings.HasSuffix(strings.ToLower(a.Name), ".pdf") {
			continue
		}
		if _, ok := s.st.PDFText(a.SHA256); ok {
			continue
		}
		text := ""
		if path, err := exec.LookPath("pdftotext"); err == nil {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			out, err := exec.CommandContext(cctx, path, "-q", "-l", "50", s.st.BlobPath(a.SHA256), "-").Output()
			cancel()
			if err == nil {
				text = strings.TrimSpace(string(out))
				if len(text) > 200_000 {
					text = text[:200_000]
				}
			}
		}
		s.st.PutPDFText(a.SHA256, text)
	}
}

// Status is what the "AI: idle / 12 queued / offline" indicator shows.
type Status struct {
	State    string `json:"state"` // off | ok | offline | building
	Queued   int    `json:"queued"`
	Vectors  int    `json:"vectors"`
	Error    string `json:"error,omitempty"`
	Identity string `json:"identity,omitempty"`
}

func (s *Service) Status() Status {
	if s == nil {
		return Status{State: StateOff}
	}
	s.qmu.Lock()
	queued := len(s.queue)
	s.qmu.Unlock()
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Status{State: s.state, Queued: queued, Error: s.lastErr, Identity: s.identity}
	for _, x := range s.spaces {
		st.Vectors += len(x.ids)
	}
	if st.State == StateOK && len(s.building) > 0 {
		st.State = StateBuilding
	}
	return st
}

// Reindex drops every vector and embeds everything again.
func (s *Service) Reindex() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.identity == "" {
		return nil // nothing built yet: prepare will do it
	}
	if err := s.st.ResetVectors(s.identity); err != nil {
		return err
	}
	s.spaces, s.hashes, s.building = map[string]*index{}, map[key][32]byte{}, map[string]bool{}
	s.st.EachItem(func(it *core.Item) {
		if it.State != core.StTrashed {
			s.building[it.Space] = true
			s.enqueue(key{it.Space, it.ID})
		}
	})
	return nil
}

// Result is the answer to a similarity question.
type Result struct {
	State string `json:"state"`
	Hits  []Hit  `json:"hits"`
}

// usable says whether the service can answer at all right now.
func (s *Service) usable() string {
	if s == nil {
		return StateOff
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.identity == "" {
		return StateOffline
	}
	return StateOK
}

// similar finds the k items nearest to vec in the spaces of the view.
// keep filters by item (it may be nil). A space that is still being built is
// not searched, and the answer then says "building": a partial index is not
// ranked (AI_CONTRACT §2.1).
func (s *Service) similar(v *store.View, vec []float32, k int, exclude key, keep func(*core.Item) bool) Result {
	res := Result{State: s.usable(), Hits: []Hit{}}
	if res.State != StateOK || vec == nil {
		return res
	}
	var all []Hit
	s.mu.RLock()
	for _, sp := range v.Spaces() {
		if s.building[sp.ID] {
			res.State = StateBuilding
			continue
		}
		x := s.spaces[sp.ID]
		if x == nil {
			continue
		}
		for i, score := range x.scores(vec) {
			all = append(all, Hit{sp.ID, x.ids[i], score})
		}
	}
	s.mu.RUnlock()
	// Read candidates back through the view, best first, until k are kept.
	// Usually the first k+32 are enough; only if the filters reject them all
	// is the whole list ranked.
	for _, n := range []int{k + 32, len(all)} {
		res.Hits = res.Hits[:0]
		for _, h := range best(all, n) {
			if len(res.Hits) == k {
				break
			}
			if (key{h.Space, h.ID}) == exclude {
				continue
			}
			it, err := v.Item(h.Space, h.ID)
			if err != nil || it.State == core.StTrashed || keep != nil && !keep(it) {
				continue
			}
			res.Hits = append(res.Hits, h)
		}
		if len(res.Hits) == k || n >= len(all) {
			break
		}
	}
	return res
}

func better(a, b Hit) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.ID < b.ID
}

// best returns the n best hits in order, without sorting the rest.
func best(all []Hit, n int) []Hit {
	if n >= len(all) {
		out := append([]Hit(nil), all...)
		sort.Slice(out, func(i, j int) bool { return better(out[i], out[j]) })
		return out
	}
	out := make([]Hit, 0, n+1)
	for _, h := range all {
		if len(out) == n && !better(h, out[n-1]) {
			continue
		}
		i := sort.Search(len(out), func(i int) bool { return better(h, out[i]) })
		out = append(out, Hit{})
		copy(out[i+1:], out[i:])
		out[i] = h
		if len(out) > n {
			out = out[:n]
		}
	}
	return out
}

func (s *Service) vector(space, id string) []float32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if x := s.spaces[space]; x != nil {
		if vec := x.vector(id); vec != nil {
			return append([]float32(nil), vec...)
		}
	}
	return nil
}

// SimilarTo returns the items most similar to one item of the view.
func (s *Service) SimilarTo(v *store.View, it *core.Item, k int, keep func(*core.Item) bool) Result {
	if s == nil {
		return Result{State: StateOff, Hits: []Hit{}}
	}
	if _, err := v.Item(it.Space, it.ID); err != nil {
		return Result{State: StateOK, Hits: []Hit{}}
	}
	return s.similar(v, s.vector(it.Space, it.ID), k, key{it.Space, it.ID}, keep)
}

// Search embeds a query and returns the nearest items. It gives the embedder
// two seconds: search must not wait for a slow model.
func (s *Service) Search(ctx context.Context, v *store.View, query string, k int) Result {
	if state := s.usable(); state != StateOK {
		return Result{State: state, Hits: []Hit{}}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	vecs, err := s.emb.Embed(ctx, []string{query}, Query)
	if err != nil || len(vecs) != 1 {
		return Result{State: StateOffline, Hits: []Hit{}}
	}
	res := s.similar(v, vecs[0], k, key{}, func(it *core.Item) bool { return it.Kind != core.KindCapture })
	for i, h := range res.Hits { // best first: cut at the floor
		if h.Score < s.SearchThreshold {
			res.Hits = res.Hits[:i]
			break
		}
	}
	return res
}

// projectVectors returns, for every open project of a space, the unit mean of
// its own vector and the vectors of the notes and actions directly in it
// (AI_CONTRACT §2.4).
func (s *Service) projectVectors(v *store.View, space string) map[string][]float32 {
	items, err := v.Items(space, func(it *core.Item) bool { return it.State != core.StTrashed })
	if err != nil {
		return nil
	}
	members := map[string][][]float32{}
	s.mu.RLock()
	x := s.spaces[space]
	if x == nil || s.building[space] {
		s.mu.RUnlock()
		return nil
	}
	for _, it := range items {
		vec := x.vector(it.ID)
		if vec == nil {
			continue
		}
		switch {
		case it.Kind == core.KindProject && (it.State == core.StActive || it.State == core.StSomeday):
			members[it.ID] = append(members[it.ID], vec)
		case it.Project != "" && (it.Kind == core.KindNote || it.Kind == core.KindAction):
			members[it.Project] = append(members[it.Project], vec)
		}
	}
	out := map[string][]float32{}
	for _, it := range items {
		if it.Kind == core.KindProject && (it.State == core.StActive || it.State == core.StSomeday) && len(members[it.ID]) > 0 {
			out[it.ID] = Mean(members[it.ID])
		}
	}
	s.mu.RUnlock()
	return out
}

// Projects suggests up to k open projects of the item's space for an item,
// nearest first.
func (s *Service) Projects(v *store.View, it *core.Item, k int) []Hit {
	if s.usable() != StateOK {
		return nil
	}
	vec := s.vector(it.Space, it.ID)
	if vec == nil {
		return nil
	}
	var hits []Hit
	for id, pv := range s.projectVectors(v, it.Space) {
		if id != it.ID {
			hits = append(hits, Hit{it.Space, id, cluster.Cosine(vec, pv)})
		}
	}
	return topK(hits, k)
}

// BelongHere returns up to k notes of the project's space that are unsorted or
// in no project and are close to the project ("might belong here", SPEC §5.5).
func (s *Service) BelongHere(v *store.View, project *core.Item, k int) []Hit {
	if s.usable() != StateOK {
		return nil
	}
	pv := s.projectVectors(v, project.Space)[project.ID]
	if pv == nil {
		return nil
	}
	notes, err := v.Items(project.Space, func(it *core.Item) bool {
		return it.Kind == core.KindNote && it.State != core.StTrashed && it.State != core.StArchived && (it.State == core.StUnsorted || it.Project == "")
	})
	if err != nil {
		return nil
	}
	var hits []Hit
	for _, n := range notes {
		if vec := s.vector(n.Space, n.ID); vec != nil {
			if score := cluster.Cosine(pv, vec); score >= s.SuggestThreshold {
				hits = append(hits, Hit{n.Space, n.ID, score})
			}
		}
	}
	return topK(hits, k)
}
