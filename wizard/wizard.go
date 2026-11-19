// Package wizard holds gtd's guided workflows as pure step machines
// (SPEC §5). A machine is a plain struct. Step describes its current
// question as data, Next takes the user's answer and returns the next state
// or, at the end, the ops to apply in one batch. No I/O, no rendering, no
// localized text: labels and questions are i18n keys.
package wizard

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/fiber/gtd/core"
)

// Step types the client renders generically.
const (
	TypeChoice = "choice" // one tap on an option answers
	TypeForm   = "form"   // fields, then one of the option buttons
)

// Option is an answer button, a select entry or a chip. Label is an i18n key.
// Text is literal (an item title, a name, a date) and is used when set.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
	Text  string `json:"text,omitempty"`
}

// Field is one input of a form step.
type Field struct {
	Key      string   `json:"key"`
	Type     string   `json:"type"`  // text | textarea | date | select | chips
	Label    string   `json:"label"` // i18n key
	Value    string   `json:"value,omitempty"`
	Options  []Option `json:"options,omitempty"` // select and chips: choices; date: presets
	Required bool     `json:"required,omitempty"`
	Hint     string   `json:"hint,omitempty"` // i18n key
	// Suggest are choices worth a second look, e.g. the projects nearest to
	// the capture. They are offered, never preselected: a suggestion becomes a
	// choice only by a tap (invariant 2).
	Suggest []Option `json:"suggest,omitempty"`
}

// Step is what the client shows.
type Step struct {
	Type      string         `json:"type"`
	Key       string         `json:"key"`
	Question  string         `json:"question"`          // i18n key
	Subject   string         `json:"subject,omitempty"` // literal context line, e.g. the capture or project title
	Options   []Option       `json:"options"`
	Preselect string         `json:"preselect,omitempty"` // the option to highlight
	Fields    []Field        `json:"fields,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

// Input is the user's answer to a step.
type Input struct {
	Choice string            `json:"choice"`
	Values map[string]string `json:"values"`
}

// InputError is a rejected answer. Key is an i18n key.
type InputError struct {
	Field string
	Key   string
}

func (e *InputError) Error() string { return e.Key + ": " + e.Field }
func (e *InputError) Unwrap() error { return core.ErrInvalid }

func required(field string) error { return &InputError{field, "err.required"} }

// Ref is an item offered as a choice.
type Ref struct {
	ID    string
	Title string
	Area  string
	Rev   uint64
}

// Env is what a machine knows about the space it works in. NewEnv builds it.
type Env struct {
	// Spaces are the spaces the user may put a capture into: the one it is
	// in first, then the shared ones. Title names the space and its members.
	Spaces    []Ref
	User      string
	Today     string // YYYY-MM-DD
	Space     *core.Space
	Projects  []Ref // open projects, by title
	Areas     []Ref // active areas, by title
	Contexts  []string
	Templates []core.Template

	// Similar and SuggestProjects come from the embedding index, if there is
	// one. The wizard works the same without them.
	Similar         []Similar
	SuggestProjects []string // project IDs, nearest first
}

// Similar is an existing item that resembles the capture.
type Similar struct {
	ID        string  `json:"id"`
	Space     string  `json:"space"`
	Title     string  `json:"title"`
	Kind      string  `json:"kind"`
	State     string  `json:"state"`
	Score     float32 `json:"score"`
	Duplicate bool    `json:"duplicate"` // at or above the duplicate threshold
}

// NewEnv collects the choices a wizard offers from a space's state.
func NewEnv(st *core.State, user, today string) Env {
	env := Env{User: user, Today: today, Space: st.Space, Contexts: core.Contexts(st, user), Templates: st.Space.Templates}
	st.Each(func(it *core.Item) {
		switch {
		case it.Kind == core.KindProject && (it.State == core.StActive || it.State == core.StSomeday):
			env.Projects = append(env.Projects, Ref{it.ID, it.Title, it.Area, it.Rev})
		case it.Kind == core.KindArea && it.State == core.StActive:
			env.Areas = append(env.Areas, Ref{ID: it.ID, Title: it.Title})
		}
	})
	for _, refs := range [][]Ref{env.Projects, env.Areas} {
		sort.Slice(refs, func(i, j int) bool {
			if refs[i].Title != refs[j].Title {
				return refs[i].Title < refs[j].Title
			}
			return refs[i].ID < refs[j].ID
		})
	}
	return env
}

func (e Env) project(id string) (Ref, bool) {
	i := slices.IndexFunc(e.Projects, func(r Ref) bool { return r.ID == id })
	if i < 0 {
		return Ref{}, false
	}
	return e.Projects[i], true
}

// suggest returns the suggested projects that are among the options.
func (e Env) suggest() []Option {
	var out []Option
	for _, id := range e.SuggestProjects {
		if p, ok := e.project(id); ok {
			out = append(out, Option{Value: p.ID, Text: p.Title})
		}
	}
	return out
}

// refOptions turns refs into select options, with an empty first entry.
func refOptions(none string, refs []Ref) []Option {
	out := []Option{{Value: "", Label: none}}
	for _, r := range refs {
		out = append(out, Option{Value: r.ID, Text: r.Title})
	}
	return out
}

// ContextOptions turns context keys into chips: canonical keys get an i18n
// label, agenda keys the label "context.agenda" plus the name, personal keys
// their own text.
func ContextOptions(contexts []string) []Option {
	var out []Option
	for _, c := range contexts {
		if who, ok := strings.CutPrefix(c, "agenda:"); ok {
			out = append(out, Option{Value: c, Label: "context.agenda", Text: who})
		} else if slices.Contains([]string{"errands", "calls", "computer", "home", "office", "anywhere"}, c) {
			out = append(out, Option{Value: c, Label: "context." + c})
		} else {
			out = append(out, Option{Value: c, Text: c})
		}
	}
	return out
}

func labels(prefix string, values ...string) []Option {
	var out []Option
	for _, v := range values {
		out = append(out, Option{Value: v, Label: prefix + v})
	}
	return out
}

// DatePresets are the "not before" and target shortcuts (SPEC §5.2 step 8).
func DatePresets(today string) []Option {
	t, err := time.Parse(time.DateOnly, today)
	if err != nil {
		return nil
	}
	monday := t.AddDate(0, 0, (8-int(t.Weekday()))%7)
	if monday.Equal(t) {
		monday = t.AddDate(0, 0, 7)
	}
	d := func(x time.Time) string { return x.Format(time.DateOnly) }
	return []Option{
		{Value: d(t.AddDate(0, 0, 1)), Label: "date.tomorrow"},
		{Value: d(monday), Label: "date.next_week"},
		{Value: d(t.AddDate(0, 0, 14)), Label: "date.two_weeks"},
		{Value: d(t.AddDate(0, 1, 0)), Label: "date.one_month"},
	}
}

// date validates an optional date value.
func date(in Input, field string) (string, error) {
	v := strings.TrimSpace(in.Values[field])
	if v == "" {
		return "", nil
	}
	if _, err := time.Parse(time.DateOnly, v); err != nil {
		return "", &InputError{field, "err.date"}
	}
	return v, nil
}

// oneOf validates an optional value against options.
func oneOf(in Input, field string, opts []Option) (string, error) {
	v := in.Values[field]
	if v == "" || slices.ContainsFunc(opts, func(o Option) bool { return o.Value == v }) {
		return v, nil
	}
	return "", &InputError{field, "err.choice"}
}

func choice(in Input, opts []Option) error {
	if !slices.ContainsFunc(opts, func(o Option) bool { return o.Value == in.Choice }) {
		return &InputError{"choice", "err.choice"}
	}
	return nil
}

// change builds a field change for a patch op, or nothing if the value stays.
func change(changes []core.FieldChange, field string, old, new any) []core.FieldChange {
	o, n := fmt.Sprint(old), fmt.Sprint(new)
	if o == n {
		return changes
	}
	return append(changes, core.FieldChange{Field: field, New: quote(n)})
}
