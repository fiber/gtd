package wizard

import (
	"regexp"
	"strings"

	"github.com/fiber/gtd/core"
)

// Note is the "Process note" wizard: it walks the open task lines (`- [ ]`) of
// a note, one per step, and turns the ones the user confirms into actions.
// Each action links back to the note (derived_from), and its line in the note
// becomes a link to the action, so a processed line is never offered again.
type Note struct {
	Item    string `json:"item"`
	Rev     uint64 `json:"rev"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Project string `json:"project,omitempty"` // the note's project: preselected

	Lines []TaskLine   `json:"lines"`
	At    int          `json:"at"` // index into Lines
	Made  []NoteAction `json:"made,omitempty"`
}

// TaskLine is one open task line of the note body.
type TaskLine struct {
	Line   int    `json:"line"`   // index into the body's lines
	Prefix string `json:"prefix"` // indentation and bullet, e.g. "  - "
	Text   string `json:"text"`
}

// NoteAction is an action the user confirmed.
type NoteAction struct {
	Line    int    `json:"line"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	Project string `json:"project,omitempty"`
	Context string `json:"context,omitempty"`
	Defer   string `json:"defer,omitempty"`
	Target  string `json:"target,omitempty"`
}

var taskLine = regexp.MustCompile(`^(\s*[-*+] )\[ \] +(\S.*)$`)

// TaskLines returns the open task lines of a Markdown body. Lines inside
// fenced code blocks are not tasks.
func TaskLines(body string) []TaskLine {
	var out []TaskLine
	fenced := false
	for i, line := range strings.Split(body, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if m := taskLine.FindStringSubmatch(line); m != nil && !fenced {
			out = append(out, TaskLine{Line: i, Prefix: m[1], Text: strings.TrimSpace(m[2])})
		}
	}
	return out
}

// StartNote begins processing a note. ok is false if it has no open task lines.
func StartNote(note *core.Item) (n Note, ok bool) {
	n = Note{Item: note.ID, Rev: note.Rev, Title: note.Title, Body: note.Body, Project: note.Project, Lines: TaskLines(note.Body)}
	return n, len(n.Lines) > 0
}

// Step describes the current task line.
func (n Note) Step(env Env) Step {
	line := n.Lines[n.At]
	return Step{Type: TypeForm, Key: "task", Question: "note.task", Subject: n.Title,
		Fields: []Field{
			{Key: "action", Type: "text", Label: "field.action", Value: line.Text, Required: true, Hint: "hint.action"},
			{Key: "project", Type: "select", Label: "field.project", Value: n.Project, Options: refOptions("option.no_project", env.Projects)},
			{Key: "context", Type: "chips", Label: "field.context", Options: ContextOptions(env.Contexts)},
			{Key: "defer_until", Type: "date", Label: "field.defer_until", Options: DatePresets(env.Today)},
			{Key: "target", Type: "date", Label: "field.target", Options: DatePresets(env.Today)},
		},
		Options: []Option{{Value: "create", Label: "note.opt.create"}, {Value: "skip", Label: "note.opt.skip"}, {Value: "stop", Label: "note.opt.stop"}},
		Data:    map[string]any{"index": n.At + 1, "total": len(n.Lines), "made": len(n.Made)},
	}
}

// Next applies the answer. "create" confirms the line as an action, "skip"
// leaves it in the note, "stop" ends early. The ops are returned at the end
// and keep everything confirmed so far.
func (n Note) Next(env Env, in Input, newID func() string) (next Note, ops []core.Op, done bool, err error) {
	step := n.Step(env)
	if err := choice(in, step.Options); err != nil {
		return n, nil, false, err
	}
	if in.Choice == "create" {
		a := NoteAction{Line: n.Lines[n.At].Line, Title: strings.TrimSpace(in.Values["action"])}
		if a.Title == "" {
			return n, nil, false, required("action")
		}
		if a.Project, err = oneOf(in, "project", step.Fields[1].Options); err != nil {
			return n, nil, false, err
		}
		if a.Context, err = oneOf(in, "context", step.Fields[2].Options); err != nil {
			return n, nil, false, err
		}
		if a.Defer, err = date(in, "defer_until"); err != nil {
			return n, nil, false, err
		}
		if a.Target, err = date(in, "target"); err != nil {
			return n, nil, false, err
		}
		a.ID = newID()
		n.Made = append(n.Made[:len(n.Made):len(n.Made)], a)
	}
	n.At++
	if in.Choice == "stop" || n.At >= len(n.Lines) {
		return n, n.ops(env), true, nil
	}
	return n, nil, false, nil
}

func (n Note) ops(env Env) []core.Op {
	if len(n.Made) == 0 {
		return nil
	}
	var ops []core.Op
	lines := strings.Split(n.Body, "\n")
	for _, a := range n.Made {
		area := ""
		if p, ok := env.project(a.Project); ok {
			area = p.Area
		}
		ops = append(ops, core.Op{Via: "web", Type: core.OpCreate, Payload: mustJSON(core.Item{
			ID: a.ID, Kind: core.KindAction, Title: a.Title, Project: a.Project, Area: area, Owner: env.User,
			Context: a.Context, DeferUntil: a.Defer, Target: a.Target,
			Source: core.Source{Via: "web"}, DerivedFrom: []string{n.Item}})})
		for _, l := range n.Lines {
			if l.Line == a.Line {
				cr := ""
				if strings.HasSuffix(lines[l.Line], "\r") {
					cr = "\r"
				}
				lines[l.Line] = l.Prefix + "[[" + a.ID + "]]" + cr
			}
		}
	}
	return append(ops, core.Op{Via: "web", Type: core.OpPatch, Item: n.Item, BaseRev: n.Rev,
		Changes: []core.FieldChange{{Field: "body", New: quote(strings.Join(lines, "\n"))}}})
}
