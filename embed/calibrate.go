package embed

import (
	"context"
	"sort"
	"time"

	"github.com/fiber/gtd/core"
	"github.com/fiber/gtd/store"
)

// Calibration is what `gtd calibrate` prints: how similarity scores are
// distributed over the notes of a space, to choose thresholds from
// (AI_CONTRACT §2.4). It holds numbers only, no titles or text.
type Calibration struct {
	State    string    `json:"state"`
	Identity string    `json:"identity"`
	Items    int       `json:"items"`
	Nearest  []float32 `json:"nearest"`   // score of each item's nearest neighbour: percentiles 5 25 50 75 90 95 99
	AllPairs []float32 `json:"all_pairs"` // every pair: the same percentiles
	Above    []int     `json:"above"`     // pairs at or above 0.95 0.92 0.90 0.85 0.80
	// The same sentences in German and English, and unrelated sentences,
	// through the same embedder.
	Translated []float32 `json:"translated"` // percentiles 5 25 50 75 95 over the pairs
	SameLang   []float32 `json:"same_language_paraphrase"`
	Unrelated  []float32 `json:"unrelated"`

	Unsorted int            `json:"unsorted"`
	Clusters []ClusterStats `json:"clusters"`
}

// ClusterStats describes one cluster of the unsorted sweep.
type ClusterStats struct {
	Size     int     `json:"size"`
	Cohesion float32 `json:"cohesion"`
	Loose    bool    `json:"loose,omitempty"`
}

// Everyday sentences: German, the same in English, and an English paraphrase.
var calibrationPairs = [][3]string{
	{"Der Wasserhahn in der Küche tropft, den Vermieter fragen.", "The kitchen tap is dripping, ask the landlord.", "Ask the landlord about the leaking tap in the kitchen."},
	{"Flüge für die Sommerreise buchen.", "Book flights for the summer trip.", "Reserve plane tickets for the holiday in summer."},
	{"Den Reisepass verlängern, bevor er abläuft.", "Renew the passport before it expires.", "Get a new passport in time."},
	{"Idee: jede Woche einen Essensplan machen.", "Idea: make a meal plan every week.", "Plan the week's meals in advance."},
	{"Angebote von drei Handwerkern für die Küche einholen.", "Get quotes from three contractors for the kitchen.", "Ask three builders what the kitchen would cost."},
	{"Die Heizkosten steigen: liegt es an der Dämmung?", "The heating bill keeps rising: is it the insulation?", "Is poor insulation why heating costs more every year?"},
	{"Notizen vom Elternabend in der Schule.", "Notes from the parents' evening at school.", "What was said at the school meeting for parents."},
	{"Das Fahrrad zur Inspektion bringen.", "Take the bicycle in for a service.", "The bike needs a check-up at the shop."},
	{"Geschenk für den Geburtstag finden.", "Find a present for the birthday.", "Need to buy a birthday gift."},
	{"Steuererklärung bis Ende Juli abgeben.", "File the tax return by the end of July.", "The tax declaration is due in July."},
	{"Sauerteigbrot backen lernen.", "Learn to bake sourdough bread.", "Try making bread with a sourdough starter."},
	{"Den Arzttermin auf nächste Woche verschieben.", "Move the doctor's appointment to next week.", "Reschedule the visit to the doctor for next week."},
}

// Calibrate measures score distributions over all items of one space.
func (s *Service) Calibrate(ctx context.Context, v *store.View, space string) (Calibration, error) {
	c := Calibration{State: s.usable()}
	if c.State != StateOK {
		return c, nil
	}
	items, err := v.Items(space, func(it *core.Item) bool { return it.State != core.StTrashed })
	if err != nil {
		return c, err
	}
	var vecs [][]float32
	for _, it := range items {
		if vec := s.vector(space, it.ID); vec != nil {
			vecs = append(vecs, vec)
		}
		if it.Kind == core.KindNote && it.State == core.StUnsorted {
			c.Unsorted++
		}
	}
	s.mu.RLock()
	c.Identity = s.identity
	s.mu.RUnlock()
	c.Items = len(vecs)
	steps := []float32{0.95, 0.92, 0.90, 0.85, 0.80}
	c.Above = make([]int, len(steps))
	var all, nearest []float32
	best := make([]float32, len(vecs))
	for i := range best {
		best[i] = -1
	}
	for i := range vecs {
		for j := i + 1; j < len(vecs); j++ {
			score := dot(vecs[i], vecs[j])
			all = append(all, score)
			best[i], best[j] = max(best[i], score), max(best[j], score)
			for k, t := range steps {
				if score >= t {
					c.Above[k]++
				}
			}
		}
	}
	if len(vecs) > 1 {
		nearest = best
	}
	wide := []float64{5, 25, 50, 75, 90, 95, 99}
	c.Nearest, c.AllPairs = percentiles(nearest, wide), percentiles(all, wide)

	var texts []string
	for _, p := range calibrationPairs {
		texts = append(texts, p[0], p[1], p[2])
	}
	ectx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	pv, err := s.emb.Embed(ectx, texts, Document)
	if err != nil {
		return c, err
	}
	var translated, same, unrelated []float32
	for i := range calibrationPairs {
		translated = append(translated, dot(pv[3*i], pv[3*i+1]))
		same = append(same, dot(pv[3*i+1], pv[3*i+2]))
		for j := range calibrationPairs {
			if j != i {
				unrelated = append(unrelated, dot(pv[3*i], pv[3*j+1]))
			}
		}
	}
	narrow := []float64{5, 25, 50, 75, 95}
	c.Translated, c.SameLang, c.Unrelated = percentiles(translated, narrow), percentiles(same, narrow), percentiles(unrelated, narrow)

	clusters, _ := s.Clusters(v, space)
	for _, cl := range clusters {
		c.Clusters = append(c.Clusters, ClusterStats{Size: len(cl.IDs), Cohesion: cl.Cohesion, Loose: cl.Loose})
	}
	return c, nil
}

func dot(a, b []float32) float32 {
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

func percentiles(values []float32, ps []float64) []float32 {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]float32(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	out := make([]float32, len(ps))
	for i, p := range ps {
		out[i] = sorted[int(p/100*float64(len(sorted)-1)+0.5)]
	}
	return out
}
