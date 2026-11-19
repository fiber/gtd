package wizard

import (
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/fiber/gtd/core"
)

// The weekly review (SPEC §5.4) as a step machine. Unlike Clarify, every
// answer takes effect at once: Next returns the ops for that one decision.
// The state is small and is stored per user between answers, so a review can
// be continued on any device.

// Review phases, in order.
const (
	PhaseClear    = "clear"    // 1. inbox to zero, from others
	PhaseUnsorted = "unsorted" // 2. unsorted notes (the clustered sweep arrives with M5)
	PhaseProjects = "projects" // 3. every active project: still alive?
	PhaseStale    = "stale"    // 4. next actions untouched for two weeks
	PhaseWaiting  = "waiting"  // 5. waiting for longer than a week
	PhaseDates    = "dates"    // 6. slipping targets, warnings, what is coming up
	PhaseSomeday  = "someday"  // 7. a rotating subset of someday/maybe
	PhaseAreas    = "areas"    // 8. areas of focus
)

var reviewPhases = []string{PhaseClear, PhaseUnsorted, PhaseProjects, PhaseStale, PhaseWaiting, PhaseDates, PhaseSomeday, PhaseAreas}

const (
	staleDays    = 14
	waitingDays  = 7
	somedayBatch = 10
)

// Review is the progress of one weekly review.
type Review struct {
	Phase   string         `json:"phase"`
	Started string         `json:"started"`         // date
	Seen    []string       `json:"seen,omitempty"`  // items decided in this run
	Stats   map[string]int `json:"stats,omitempty"` // "projects.keep" → 3
}

// ReviewEnv is what the review knows about the user's world. The spaces are
// the ones the user is a member of, nothing else.
type ReviewEnv struct {
	User       string
	Today      string
	Spaces     []*core.State
	Inbox      int // captures in the inbox
	FromOthers int // open notifications
	// SomedayReviewed says when each someday item was last looked at, so that
	// the least recently reviewed come first.
	SomedayReviewed map[string]string
	// FollowUp is the localized pattern for a nudge action, with two %s: the
	// person and the title. Nudge is the localized text of a nudge comment.
	FollowUp string
	Nudge    string
}

// StartReview begins a review.
func StartReview(today string) Review {
	return Review{Phase: PhaseClear, Started: today, Stats: map[string]int{}}
}

func addDays(date string, n int) string {
	t, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return date
	}
	return t.AddDate(0, 0, n).Format(time.DateOnly)
}

// card is one item the current phase asks about.
type card struct {
	it *core.Item
	st *core.State
}

func (e ReviewEnv) spaces() []*core.State {
	out := slices.Clone(e.Spaces)
	sort.Slice(out, func(i, j int) bool { return out[i].Space.ID < out[j].Space.ID })
	return out
}

func items(st *core.State, keep func(*core.Item) bool) []*core.Item {
	var out []*core.Item
	st.Each(func(it *core.Item) {
		if keep(it) {
			out = append(out, it)
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// cards lists the items a phase asks about, in a fixed order.
func (r Review) cards(env ReviewEnv, phase string) []card {
	var out []card
	for _, st := range env.spaces() {
		switch phase {
		case PhaseProjects:
			// Tree order: a project before its sub-projects.
			active := items(st, func(it *core.Item) bool { return it.Kind == core.KindProject && it.State == core.StActive })
			var walk func(parent string)
			done := map[string]bool{}
			walk = func(parent string) {
				for _, p := range active {
					if !done[p.ID] && p.Parent == parent {
						done[p.ID] = true
						out = append(out, card{p, st})
						walk(p.ID)
					}
				}
			}
			walk("")
			for _, p := range active { // children of projects that are not active
				if !done[p.ID] {
					done[p.ID] = true
					out = append(out, card{p, st})
					walk(p.ID)
				}
			}
		case PhaseStale:
			before := addDays(env.Today, -staleDays)
			for _, it := range items(st, func(it *core.Item) bool {
				mine := it.Owner == env.User || it.Owner == "" && st.Space.Kind == core.SpaceShared
				return it.Kind == core.KindAction && it.State == core.StNext && mine && it.Updated.Format(time.DateOnly) < before
			}) {
				out = append(out, card{it, st})
			}
		case PhaseWaiting:
			before := addDays(env.Today, -waitingDays)
			for _, it := range core.WaitingFor(st, env.User) {
				if it.Updated.Format(time.DateOnly) < before {
					out = append(out, card{it, st})
				}
			}
		case PhaseSomeday:
			for _, it := range items(st, func(it *core.Item) bool {
				return it.State == core.StSomeday && (it.Kind == core.KindAction || it.Kind == core.KindProject)
			}) {
				out = append(out, card{it, st})
			}
		}
	}
	if phase == PhaseSomeday {
		// Least recently reviewed first, never reviewed before everything.
		sort.SliceStable(out, func(i, j int) bool {
			a, b := env.SomedayReviewed[out[i].it.ID], env.SomedayReviewed[out[j].it.ID]
			if a != b {
				return a < b
			}
			return out[i].it.ID < out[j].it.ID
		})
	}
	return out
}

// current returns the next undecided card of the phase, or nil.
func (r Review) current(env ReviewEnv) *card {
	if r.Phase == PhaseSomeday && r.Stats["someday.keep"]+r.Stats["someday.activate"]+r.Stats["someday.drop"] >= somedayBatch {
		return nil // this week's subset is done
	}
	for _, c := range r.cards(env, r.Phase) {
		if !slices.Contains(r.Seen, c.it.ID) {
			return &c
		}
	}
	return nil
}

// dated lists what the dates step shows: slipping items, plausibility
// warnings, and everything due or targeted in the next 14 days.
func dated(env ReviewEnv) []map[string]string {
	var out []map[string]string
	seen := map[string]bool{}
	add := func(it *core.Item, note string) {
		if it != nil && !seen[it.ID+note] {
			seen[it.ID+note] = true
			out = append(out, map[string]string{"id": it.ID, "title": it.Title, "note": note, "date": firstNonEmpty(it.Due, it.Target)})
		}
	}
	for _, st := range env.spaces() {
		for _, w := range core.Warnings(st, env.Today) {
			add(st.Item(w.Item), "warning.short."+w.Type)
		}
		for _, it := range core.Dated(st, addDays(env.Today, 14)) {
			if it.Target >= env.Today || it.Due != "" {
				add(it, "review.note.upcoming")
			}
		}
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func areas(env ReviewEnv) []map[string]any {
	var out []map[string]any
	for _, st := range env.spaces() {
		projects := map[string]int{}
		st.Each(func(it *core.Item) {
			if it.Kind == core.KindProject && it.State == core.StActive {
				projects[it.Area]++
			}
		})
		for _, a := range items(st, func(it *core.Item) bool { return it.Kind == core.KindArea && it.State == core.StActive }) {
			out = append(out, map[string]any{"id": a.ID, "title": a.Title, "projects": projects[a.ID]})
		}
	}
	return out
}

func unsorted(env ReviewEnv) int {
	n := 0
	for _, st := range env.Spaces {
		st.Each(func(it *core.Item) {
			if it.Kind == core.KindNote && it.State == core.StUnsorted {
				n++
			}
		})
	}
	return n
}

// empty reports whether a phase has nothing to show and can be passed over.
func (r Review) empty(env ReviewEnv) bool {
	switch r.Phase {
	case PhaseClear:
		return false // always shown: it is the gate
	case PhaseUnsorted:
		return unsorted(env) == 0
	case PhaseDates:
		return len(dated(env)) == 0
	case PhaseAreas:
		return len(areas(env)) == 0
	}
	return r.current(env) == nil
}

// At returns the review moved past phases that have nothing to ask. done is
// true when no phase is left.
func (r Review) At(env ReviewEnv) (at Review, done bool) {
	for {
		if r.Phase == "" {
			return r, true
		}
		if !r.empty(env) {
			return r, false
		}
		r.Phase = nextPhase(r.Phase)
	}
}

func nextPhase(p string) string {
	i := slices.Index(reviewPhases, p)
	if i < 0 || i+1 >= len(reviewPhases) {
		return ""
	}
	return reviewPhases[i+1]
}

func opt(values ...string) []Option {
	var out []Option
	for _, v := range values {
		out = append(out, Option{Value: v, Label: "review.opt." + v})
	}
	return out
}

// Step describes the current question. Call At first.
func (r Review) Step(env ReviewEnv) Step {
	s := Step{Type: TypeChoice, Key: r.Phase, Question: "review." + r.Phase, Options: opt("continue"),
		Data: map[string]any{"phase": slices.Index(reviewPhases, r.Phase) + 1, "phases": len(reviewPhases)}}
	c := r.current(env)
	if c != nil {
		s.Subject = c.it.Title
		s.Data["item"] = c.it.ID
		left := 0
		for _, x := range r.cards(env, r.Phase) {
			if !slices.Contains(r.Seen, x.it.ID) {
				left++
			}
		}
		s.Data["left"] = left
	}
	switch r.Phase {
	case PhaseClear:
		s.Data["inbox"], s.Data["from_others"] = env.Inbox, env.FromOthers
		s.Data["links"] = []map[string]string{{"href": "/clarify", "label": "review.link.clarify"}, {"href": "/inbox", "label": "review.link.inbox"}}
	case PhaseUnsorted:
		s.Data["count"] = unsorted(env)
		s.Data["links"] = []map[string]string{{"href": "/notes", "label": "review.link.unsorted"}}
	case PhaseProjects:
		roll := core.Rollups(c.st, env.Today)[c.it.ID]
		var open []string
		for _, a := range items(c.st, func(a *core.Item) bool {
			return a.Kind == core.KindAction && a.Project == c.it.ID && (a.State == core.StNext || a.State == core.StWaiting || a.State == core.StPlanned)
		}) {
			open = append(open, a.Title)
		}
		s.Type = TypeForm
		s.Options = opt("keep", "someday", "done", "drop")
		s.Fields = []Field{{Key: "next_action", Type: "text", Label: "field.action", Hint: "review.hint.next_action"}}
		s.Data["alive"], s.Data["rollup"], s.Data["actions"] = roll.Alive, roll, open
	case PhaseStale:
		s.Type = TypeForm
		s.Options = opt("keep", "defer", "someday", "drop")
		s.Fields = []Field{{Key: "defer_until", Type: "date", Label: "field.defer_until", Options: DatePresets(env.Today)}}
		s.Data["since"] = c.it.Updated.Format(time.DateOnly)
	case PhaseWaiting:
		s.Options = opt("nudge", "keep", "resolved")
		s.Data["since"] = c.it.Updated.Format(time.DateOnly)
		s.Data["waiting_on"] = firstNonEmpty(c.it.WaitingOn, c.it.Owner)
	case PhaseDates:
		s.Data["items"] = dated(env)
	case PhaseSomeday:
		s.Options = opt("activate", "keep", "drop")
		s.Data["kind"] = c.it.Kind
	case PhaseAreas:
		s.Data["areas"] = areas(env)
	}
	return s
}

// Next takes the answer for the current step and returns the review after it,
// the ops of that decision (all in one space) and whether the review is over.
func (r Review) Next(env ReviewEnv, in Input, newID func() string) (next Review, space string, ops []core.Op, done bool, err error) {
	step := r.Step(env)
	if err := choice(in, step.Options); err != nil {
		return r, "", nil, false, err
	}
	r.Seen = slices.Clone(r.Seen)
	r.Stats = cloneStats(r.Stats)
	c := r.current(env)
	move := func(state string) core.Op {
		return core.Op{Via: "web", Type: core.OpTransition, Item: c.it.ID, BaseRev: c.it.Rev,
			Changes: []core.FieldChange{{Field: "state", New: quote(state)}}}
	}
	decided := func() {
		r.Seen = append(r.Seen, c.it.ID)
		r.Stats[r.Phase+"."+in.Choice]++
		space = c.st.Space.ID
	}

	switch r.Phase {
	case PhaseClear:
		// GTD's "get clear": the review goes on when the inbox is empty.
		if env.Inbox > 0 {
			return r, "", nil, false, &InputError{"inbox", "err.inbox_not_empty"}
		}
		r.Phase = nextPhase(r.Phase)
	case PhaseUnsorted, PhaseDates, PhaseAreas:
		r.Phase = nextPhase(r.Phase)
	case PhaseProjects:
		switch in.Choice {
		case "keep":
			action := in.Values["next_action"]
			alive := core.Rollups(c.st, env.Today)[c.it.ID].Alive
			if !alive && len(action) == 0 {
				// A kept project must be alive: it needs a next action now.
				return r, "", nil, false, &InputError{"next_action", "err.stuck"}
			}
			if action != "" {
				ops = append(ops, core.Op{Via: "web", Type: core.OpCreate, Payload: mustJSON(core.Item{
					ID: newID(), Kind: core.KindAction, Title: action, Project: c.it.ID, Area: c.it.Area,
					Owner: env.User, Source: core.Source{Via: "web"}})})
			}
		case "someday":
			ops = append(ops, move(core.StSomeday))
		case "done":
			ops = append(ops, move(core.StDone))
		case "drop":
			ops = append(ops, move(core.StDropped))
		}
		decided()
	case PhaseStale:
		switch in.Choice {
		case "defer":
			until, err := date(in, "defer_until")
			if err != nil {
				return r, "", nil, false, err
			}
			if until == "" {
				return r, "", nil, false, required("defer_until")
			}
			ops = append(ops, core.Op{Via: "web", Type: core.OpPatch, Item: c.it.ID, BaseRev: c.it.Rev,
				Changes: []core.FieldChange{{Field: "defer_until", New: quote(until)}}})
		case "someday":
			ops = append(ops, move(core.StSomeday))
		case "drop":
			ops = append(ops, move(core.StDropped))
		}
		decided()
	case PhaseWaiting:
		switch in.Choice {
		case "nudge":
			if c.it.WaitingOn != "" || c.it.Owner == env.User {
				// A person outside the space: an action to follow up with them.
				who := firstNonEmpty(c.it.WaitingOn, c.it.Owner)
				ops = append(ops, core.Op{Via: "web", Type: core.OpCreate, Payload: mustJSON(core.Item{
					ID: newID(), Kind: core.KindAction, Title: fmt.Sprintf(env.FollowUp, who, c.it.Title), Project: c.it.Project,
					Area: c.it.Area, Context: "calls", Owner: env.User, DerivedFrom: []string{c.it.ID}, Source: core.Source{Via: "web"}})})
			} else {
				// A member: a comment on the item that mentions them.
				ops = append(ops, core.Op{Via: "web", Type: core.OpComment, Item: c.it.ID,
					Payload: mustJSON(core.Comment{Body: "@[user:" + c.it.Owner + "] " + env.Nudge, Mentions: []string{c.it.Owner}})})
			}
		case "resolved":
			ops = append(ops, move(core.StDone))
		}
		decided()
	case PhaseSomeday:
		switch in.Choice {
		case "activate":
			to := core.StNext
			if c.it.Kind == core.KindProject {
				to = core.StActive
			}
			ops = append(ops, move(to))
		case "drop":
			ops = append(ops, move(core.StDropped))
		}
		decided()
	}
	return r, space, ops, false, nil
}

func cloneStats(m map[string]int) map[string]int {
	out := make(map[string]int, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}
