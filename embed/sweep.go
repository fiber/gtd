package embed

import (
	"math"
	"sort"

	"github.com/fiber/ai/cluster"
	"github.com/fiber/ai/tensor"

	"github.com/fiber/gtd/core"
	"github.com/fiber/gtd/store"
)

// Clustering for the unsorted sweep (AI_CONTRACT §2.5).

const (
	clusterSize     = 15   // k = ceil(n / clusterSize)
	clusterIters    = 50   // at most
	clusterSeed     = 1    // fixed: the same notes give the same clusters
	clusterCohesion = 0.45 // below this a cluster is not one: its notes are loose
)

// Cluster is a group of unsorted notes that are about the same thing.
type Cluster struct {
	IDs      []string // nearest to the centre first
	Cohesion float32  // mean cosine similarity to the centre
	Loose    bool     // the notes that fit nowhere, shown last
}

// Clusters groups the unsorted notes of one space of the view: spherical
// k-means with k = ceil(n/15), ordered by cohesion × size. Clusters with a
// cohesion under 0.45 or with a single note are merged into one "loose"
// cluster at the end. Notes without a vector yet are left out.
func (s *Service) Clusters(v *store.View, space string) ([]Cluster, string) {
	state := s.usable()
	if state != StateOK {
		return nil, state
	}
	notes, err := v.Items(space, func(it *core.Item) bool { return it.Kind == core.KindNote && it.State == core.StUnsorted })
	if err != nil {
		return nil, StateOK
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].ID < notes[j].ID })
	var ids []string
	var data []float32
	dim := 0
	s.mu.RLock()
	if s.building[space] {
		s.mu.RUnlock()
		return nil, StateBuilding
	}
	if x := s.spaces[space]; x != nil {
		dim = x.dim
		for _, it := range notes {
			if vec := x.vector(it.ID); vec != nil {
				ids = append(ids, it.ID)
				data = append(data, vec...)
			}
		}
	}
	s.mu.RUnlock()
	n := len(ids)
	if n == 0 {
		return nil, StateOK
	}
	if n == 1 {
		return []Cluster{{IDs: ids, Cohesion: 1, Loose: true}}, StateOK
	}
	k := int(math.Ceil(float64(n) / clusterSize))
	res := cluster.KMeans(tensor.FromSlice(data, n, dim), k, clusterIters, clusterSeed)

	type member struct {
		id  string
		sim float32
	}
	groups := make([][]member, k)
	for i, c := range res.Assignment {
		groups[c] = append(groups[c], member{ids[i], res.Similarity[i]})
	}
	var out []Cluster
	var loose []member
	for _, g := range groups {
		if len(g) == 0 {
			continue
		}
		var sum float32
		for _, m := range g {
			sum += m.sim
		}
		cohesion := sum / float32(len(g))
		if len(g) < 2 || cohesion < clusterCohesion {
			loose = append(loose, g...)
			continue
		}
		out = append(out, Cluster{IDs: order(g, func(m member) (string, float32) { return m.id, m.sim }), Cohesion: cohesion})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Cohesion*float32(len(out[i].IDs)), out[j].Cohesion*float32(len(out[j].IDs))
		if a != b {
			return a > b
		}
		return out[i].IDs[0] < out[j].IDs[0]
	})
	if len(loose) > 0 {
		var sum float32
		for _, m := range loose {
			sum += m.sim
		}
		out = append(out, Cluster{IDs: order(loose, func(m member) (string, float32) { return m.id, m.sim }), Cohesion: sum / float32(len(loose)), Loose: true})
	}
	return out, StateOK
}

// order returns the IDs of members, best score first.
func order[T any](list []T, f func(T) (string, float32)) []string {
	sort.SliceStable(list, func(i, j int) bool {
		ai, as := f(list[i])
		bi, bs := f(list[j])
		if as != bs {
			return as > bs
		}
		return ai < bi
	})
	ids := make([]string, len(list))
	for i, m := range list {
		ids[i], _ = f(m)
	}
	return ids
}
