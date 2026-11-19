package embed

import (
	"sort"

	"github.com/fiber/ai/cluster"
	"github.com/fiber/ai/tensor"
)

// index holds the vectors of one space in one contiguous matrix, one row per
// item, so that a query is a single matrix product (AI_CONTRACT §2.3).
type index struct {
	dim  int
	ids  []string
	pos  map[string]int
	data []float32
}

func newIndex() *index { return &index{pos: map[string]int{}} }

func (x *index) put(id string, vec []float32) {
	if x.dim == 0 {
		x.dim = len(vec)
	}
	if len(vec) != x.dim {
		return // a vector of another model: never mixed in
	}
	if i, ok := x.pos[id]; ok {
		copy(x.data[i*x.dim:], vec)
		return
	}
	x.pos[id] = len(x.ids)
	x.ids = append(x.ids, id)
	x.data = append(x.data, vec...)
}

func (x *index) delete(id string) {
	i, ok := x.pos[id]
	if !ok {
		return
	}
	last := len(x.ids) - 1
	if i != last { // move the last row into the gap
		copy(x.data[i*x.dim:(i+1)*x.dim], x.data[last*x.dim:])
		x.ids[i] = x.ids[last]
		x.pos[x.ids[i]] = i
	}
	x.ids = x.ids[:last]
	x.data = x.data[:last*x.dim]
	delete(x.pos, id)
}

func (x *index) vector(id string) []float32 {
	i, ok := x.pos[id]
	if !ok {
		return nil
	}
	return x.data[i*x.dim : (i+1)*x.dim]
}

// scores returns the cosine similarity of the query to every row. The rows
// and the query are unit vectors, so this is one SGEMM on fiber/ai's kernels.
func (x *index) scores(query []float32) []float32 {
	if len(x.ids) == 0 || len(query) != x.dim {
		return nil
	}
	q := tensor.FromSlice(query, 1, x.dim)
	docs := tensor.FromSlice(x.data, len(x.ids), x.dim)
	return cluster.Similarities(q, docs).Float32s()
}

// Hit is one similarity result.
type Hit struct {
	Space string  `json:"space"`
	ID    string  `json:"id"`
	Score float32 `json:"score"`
}

// topK keeps the k best hits, best first. Ties go to the smaller ID, so the
// result does not depend on the order of rows.
func topK(hits []Hit, k int) []Hit {
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID < hits[j].ID
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}
