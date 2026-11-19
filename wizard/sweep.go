package wizard

import (
	"slices"
	"sort"
	"strings"

	"github.com/fiber/gtd/core"
)

// The unsorted sweep (SPEC §5.7): one cluster of unsorted notes per screen,
// and one decision for all of its checked notes.

const StepSweep = "cluster"

// SweepNote is a member of the cluster.
type SweepNote struct {
	ID     string `json:"id"`
	Rev    uint64 `json:"rev"`
	Title  string `json:"title"`
	Folder string `json:"folder,omitempty"` // imported.folder
	Todos  int    `json:"todos,omitempty"`  // open task lines
}

// Sweep handles one cluster.
type Sweep struct {
	Notes    []SweepNote `json:"notes"`
	Loose    bool        `json:"loose,omitempty"`
	Cohesion float32     `json:"cohesion"`
	Index    int         `json:"index"` // 1-based position among the clusters
	Total    int         `json:"total"`
}

// StartSweep begins with the notes of a cluster, nearest to its centre first.
func StartSweep(notes []*core.Item, cohesion float32, loose bool, index, total int) Sweep {
	s := Sweep{Loose: loose, Cohesion: cohesion, Index: index, Total: total}
	for _, it := range notes {
		folder, _ := it.Imported["folder"].(string)
		s.Notes = append(s.Notes, SweepNote{ID: it.ID, Rev: it.Rev, Title: it.Title, Folder: folder, Todos: len(TaskLines(it.Body))})
	}
	return s
}

// Step describes the cluster and what can be done with it.
func (s Sweep) Step(env Env) Step {
	var titles, ids []string
	members := make([]Option, len(s.Notes))
	folders := map[string]int{}
	for i, n := range s.Notes {
		if i < 3 {
			titles = append(titles, n.Title)
		}
		ids = append(ids, n.ID)
		members[i] = Option{Value: n.ID, Text: n.Title}
		if n.Folder != "" {
			folders[n.Folder]++
		}
	}
	// The folders most of the notes came from: a hint, not a decision.
	var hint []string
	for f := range folders {
		hint = append(hint, f)
	}
	sort.Slice(hint, func(i, j int) bool {
		if folders[hint[i]] != folders[hint[j]] {
			return folders[hint[i]] > folders[hint[j]]
		}
		return hint[i] < hint[j]
	})
	hint = hint[:min(3, len(hint))]

	question := "sweep.cluster"
	if s.Loose {
		question = "sweep.loose"
	}
	return Step{Type: TypeForm, Key: StepSweep, Question: question, Subject: strings.Join(titles, " · "),
		Fields: []Field{
			{Key: "members", Type: "checks", Label: "field.members", Value: strings.Join(ids, ","), Options: members},
			{Key: "project", Type: "select", Label: "field.project", Options: refOptions("option.none", env.Projects), Suggest: env.suggest()},
			{Key: "new_project", Type: "text", Label: "field.new_project"},
			{Key: "area", Type: "select", Label: "field.area", Options: refOptions("option.none", env.Areas)},
		},
		Options: []Option{{Value: "file", Label: "sweep.opt.file"}, {Value: "project", Label: "sweep.opt.project"},
			{Value: "theme", Label: "sweep.opt.theme"}, {Value: "area", Label: "sweep.opt.area"},
			{Value: "archive", Label: "sweep.opt.archive"}, {Value: "skip", Label: "sweep.opt.skip"}},
		Data: map[string]any{"notes": s.Notes, "folders": hint, "cohesion": s.Cohesion, "index": s.Index, "total": s.Total},
	}
}

// Next applies the decision to the checked notes. Every answer ends this
// cluster; unchecked notes stay unsorted.
func (s Sweep) Next(env Env, in Input, newID func() string) (next Sweep, ops []core.Op, done bool, err error) {
	step := s.Step(env)
	if err := choice(in, step.Options); err != nil {
		return s, nil, false, err
	}
	if in.Choice == "skip" {
		return s, nil, true, nil
	}
	var picked []SweepNote
	for _, id := range strings.Split(in.Values["members"], ",") {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		i := slices.IndexFunc(s.Notes, func(n SweepNote) bool { return n.ID == id })
		if i < 0 || slices.ContainsFunc(picked, func(n SweepNote) bool { return n.ID == id }) {
			return s, nil, false, &InputError{"members", "err.choice"}
		}
		picked = append(picked, s.Notes[i])
	}
	if len(picked) == 0 {
		return s, nil, false, required("members")
	}
	project, err := oneOf(in, "project", step.Fields[1].Options)
	if err != nil {
		return s, nil, false, err
	}
	area, err := oneOf(in, "area", step.Fields[3].Options)
	if err != nil {
		return s, nil, false, err
	}
	title := strings.TrimSpace(in.Values["new_project"])
	state := core.StFiled
	var set []core.FieldChange
	switch in.Choice {
	case "file":
		if project == "" {
			return s, nil, false, required("project")
		}
		set = []core.FieldChange{{Field: "project", New: quote(project)}}
	case "project", "theme":
		if title == "" {
			return s, nil, false, required("new_project")
		}
		p := core.Item{ID: newID(), Kind: core.KindProject, Title: title, Area: area, State: core.StActive}
		if in.Choice == "theme" {
			p.State = core.StSomeday
		}
		ops = append(ops, core.Op{Via: "web", Type: core.OpCreate, Payload: mustJSON(p)})
		set = []core.FieldChange{{Field: "project", New: quote(p.ID)}}
	case "area":
		if area == "" {
			return s, nil, false, required("area")
		}
		set = []core.FieldChange{{Field: "area", New: quote(area)}}
	case "archive":
		state = core.StArchived
	}
	for _, n := range picked {
		ops = append(ops, core.Op{Via: "web", Type: core.OpTransition, Item: n.ID, BaseRev: n.Rev,
			Changes: []core.FieldChange{{Field: "state", New: quote(state)}}})
		if set != nil {
			ops = append(ops, core.Op{Via: "web", Type: core.OpPatch, Item: n.ID, BaseRev: n.Rev + 1, Changes: slices.Clone(set)})
		}
	}
	return s, ops, true, nil
}
