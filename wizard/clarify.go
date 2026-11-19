package wizard

import (
	"encoding/json"
	"strings"

	"github.com/fiber/gtd/core"
)

// Clarify step keys, in the order of SPEC §5.2. Step 2 ("which space?") needs
// the move op and arrives with M6: until then an item stays in the inbox's space.
const (
	StepWhat       = "what"        // 1. what is it?
	StepActionable = "actionable"  // 3. actionable?
	StepSomeday    = "someday"     //    no → someday: not before?
	StepReference  = "reference"   //    no → reference: where does it belong?
	StepMulti      = "multi"       // 4. more than one step?
	StepProject    = "project"     //    yes → which project, or a new one
	StepAction     = "action"      // 5. next physical action?
	StepTwoMinutes = "two_minutes" // 6. under 2 minutes?
	StepDoNow      = "do_now"      //    yes → do it now
	StepWho        = "who"         // 7. who?
	StepPerson     = "person"      //    someone who is not a member
	StepWhen       = "when"        // 8. when and where?
	StepAfter      = "after"       // 9. anything after that? (projects)
)

// NewProject is the project a capture turns into.
type NewProject struct {
	Title  string `json:"title"`
	Parent string `json:"parent,omitempty"`
	Area   string `json:"area,omitempty"`
	Target string `json:"target,omitempty"`
}

// Followup is a planned action after the next action.
type Followup struct {
	Title  string `json:"title"`
	Target string `json:"target,omitempty"`
}

// Clarify processes one capture. The zero value is not usable: StartClarify.
type Clarify struct {
	At string `json:"at"`

	// The capture as it was when the wizard started.
	Item        string            `json:"item"`
	Rev         uint64            `json:"rev"`
	Original    string            `json:"original"`
	OldTitle    string            `json:"old_title"`
	OldBody     string            `json:"old_body"`
	Attachments []core.Attachment `json:"attachments,omitempty"`

	// Answers so far.
	Title       string      `json:"title"`
	Body        string      `json:"body"`
	Outcome     string      `json:"outcome,omitempty"`     // trash | someday | reference | action
	Project     string      `json:"project,omitempty"`     // existing project
	NewProject  *NewProject `json:"new_project,omitempty"` // the capture becomes this project
	Area        string      `json:"area,omitempty"`
	Template    string      `json:"template,omitempty"`
	Action      string      `json:"action,omitempty"`
	DoneActions []string    `json:"done_actions,omitempty"` // done in two minutes, before Action
	Complete    bool        `json:"complete,omitempty"`     // the project is finished with DoneActions
	WaitingOn   string      `json:"waiting_on,omitempty"`
	Context     string      `json:"context,omitempty"`
	Defer       string      `json:"defer,omitempty"`
	Target      string      `json:"target,omitempty"`
	Due         string      `json:"due,omitempty"`
	Energy      string      `json:"energy,omitempty"`
	Time        string      `json:"time,omitempty"`
	Followups   []Followup  `json:"followups,omitempty"`
}

// StartClarify begins with a capture from the inbox.
func StartClarify(capture *core.Item) Clarify {
	return Clarify{At: StepWhat, Item: capture.ID, Rev: capture.Rev, Original: capture.Original,
		OldTitle: capture.Title, OldBody: capture.Body, Attachments: capture.Attachments,
		Title: capture.Title, Body: capture.Body}
}

func (c Clarify) inProject() bool { return c.Project != "" || c.NewProject != nil }

func (c Clarify) projectTitle(env Env) string {
	if c.NewProject != nil {
		return c.NewProject.Title
	}
	p, _ := env.project(c.Project)
	return p.Title
}

var (
	yesNo     = []Option{{Value: "yes", Label: "answer.yes"}, {Value: "no", Label: "answer.no"}}
	cont      = []Option{{Value: "continue", Label: "answer.continue"}}
	energies  = labels("energy.", "low", "med", "high")
	durations = labels("time.", "5m", "15m", "30m", "1h", "2h+")
)

// Step describes the current question.
func (c Clarify) Step(env Env) Step {
	s := Step{Key: c.At, Question: "clarify." + c.At, Subject: c.Title, Type: TypeForm, Options: cont}
	switch c.At {
	case StepWhat:
		s.Subject = ""
		s.Fields = []Field{
			{Key: "title", Type: "text", Label: "field.title", Value: c.Title, Required: true},
			{Key: "body", Type: "textarea", Label: "field.body", Value: c.Body},
		}
		s.Data = map[string]any{"original": c.Original, "attachments": c.Attachments}
	case StepActionable:
		s.Type = TypeChoice
		s.Options = []Option{{Value: "yes", Label: "answer.yes"}, {Value: "reference", Label: "clarify.opt.reference"},
			{Value: "someday", Label: "clarify.opt.someday"}, {Value: "trash", Label: "clarify.opt.trash"}}
	case StepSomeday:
		s.Fields = []Field{{Key: "defer_until", Type: "date", Label: "field.defer_until", Options: DatePresets(env.Today)}}
	case StepReference:
		s.Fields = []Field{
			{Key: "project", Type: "select", Label: "field.project", Options: refOptions("option.unsorted", env.Projects)},
			{Key: "new_project", Type: "text", Label: "field.new_project"},
			{Key: "area", Type: "select", Label: "field.area", Options: refOptions("option.none", env.Areas)},
		}
		if tpls := c.templates(env, core.KindNote); len(tpls) > 1 {
			s.Fields = append(s.Fields, Field{Key: "template", Type: "select", Label: "field.template", Options: tpls})
		}
	case StepMulti:
		s.Type, s.Options, s.Preselect = TypeChoice, yesNo, "no"
	case StepProject:
		s.Fields = []Field{
			{Key: "project", Type: "select", Label: "field.add_to_project", Options: refOptions("option.new_project", env.Projects)},
			{Key: "title", Type: "text", Label: "field.outcome", Value: c.Title, Hint: "hint.outcome"},
			{Key: "parent", Type: "select", Label: "field.parent", Options: refOptions("option.none", env.Projects)},
			{Key: "area", Type: "select", Label: "field.area", Options: refOptions("option.none", env.Areas)},
			{Key: "target", Type: "date", Label: "field.target", Options: DatePresets(env.Today)},
		}
	case StepAction:
		value := c.Action
		if value == "" && !c.inProject() {
			value = c.Title
		}
		s.Fields = []Field{
			{Key: "action", Type: "text", Label: "field.action", Value: value, Required: true, Hint: "hint.action"},
			{Key: "area", Type: "select", Label: "field.area", Value: c.Area, Options: refOptions("option.none", env.Areas)},
		}
		if c.inProject() {
			s.Subject = c.projectTitle(env)
		}
		if len(c.DoneActions) > 0 {
			// SPEC §5.3: "Next action for <project>, or is it complete?"
			s.Question = "clarify.action_next"
			s.Options = append(s.Options[:1:1], Option{Value: "complete", Label: "clarify.opt.complete"})
			s.Data = map[string]any{"done": c.DoneActions}
		}
	case StepTwoMinutes:
		s.Type, s.Options, s.Preselect, s.Subject = TypeChoice, yesNo, "no", c.Action
	case StepDoNow:
		s.Type, s.Subject = TypeChoice, c.Action
		s.Options = []Option{{Value: "done", Label: "clarify.opt.done"}, {Value: "longer", Label: "clarify.opt.longer"}}
		s.Data = map[string]any{"timer": 120}
	case StepWho:
		s.Type, s.Preselect, s.Subject = TypeChoice, "me", c.Action
		s.Options = []Option{{Value: "me", Label: "clarify.opt.me"}, {Value: "person", Label: "clarify.opt.person"}}
	case StepPerson:
		s.Subject = c.Action
		s.Fields = []Field{{Key: "waiting_on", Type: "text", Label: "field.waiting_on", Value: c.WaitingOn, Required: true}}
		s.Data = map[string]any{"people": env.Space.People}
	case StepWhen:
		s.Subject = c.Action
		s.Fields = []Field{
			{Key: "context", Type: "chips", Label: "field.context", Options: ContextOptions(env.Contexts)},
			{Key: "defer_until", Type: "date", Label: "field.defer_until", Options: DatePresets(env.Today)},
			{Key: "target", Type: "date", Label: "field.target", Options: DatePresets(env.Today)},
			{Key: "due", Type: "date", Label: "field.due", Hint: "hint.due"},
			{Key: "energy", Type: "chips", Label: "field.energy", Options: energies},
			{Key: "time", Type: "chips", Label: "field.time", Options: durations},
		}
	case StepAfter:
		s.Subject = c.projectTitle(env)
		s.Fields = []Field{
			{Key: "title", Type: "text", Label: "field.followup"},
			{Key: "target", Type: "date", Label: "field.target", Options: DatePresets(env.Today)},
		}
		s.Options = []Option{{Value: "add", Label: "clarify.opt.add"}, {Value: "finish", Label: "clarify.opt.finish"}}
		after := []string{c.Action}
		for _, f := range c.Followups {
			after = append(after, f.Title)
		}
		s.Data = map[string]any{"after": after}
	}
	return s
}

// templates returns the space's templates for a kind as options, led by "none".
func (c Clarify) templates(env Env, kind string) []Option {
	out := []Option{{Value: "", Label: "option.none"}}
	for _, t := range env.Templates {
		if t.Kind == kind {
			out = append(out, Option{Value: t.Key, Text: t.Key})
		}
	}
	return out
}

// Next applies the answer to the current step. It returns the next state, or
// done with the ops that turn the capture into what was decided. newID
// supplies IDs for the items those ops create.
func (c Clarify) Next(env Env, in Input, newID func() string) (next Clarify, ops []core.Op, done bool, err error) {
	step := c.Step(env)
	if err := choice(in, step.Options); err != nil {
		return c, nil, false, err
	}
	val := func(k string) string { return strings.TrimSpace(in.Values[k]) }
	field := func(k string) Field {
		for _, f := range step.Fields {
			if f.Key == k {
				return f
			}
		}
		return Field{}
	}
	finish := func() (Clarify, []core.Op, bool, error) { return c, c.ops(env, newID), true, nil }

	switch c.At {
	case StepWhat:
		if c.Title = val("title"); c.Title == "" {
			return c, nil, false, required("title")
		}
		c.Body = strings.TrimRight(in.Values["body"], " \t\r\n")
		c.At = StepActionable
	case StepActionable:
		switch in.Choice {
		case "trash":
			c.Outcome = "trash"
			return finish()
		case "someday":
			c.Outcome, c.At = "someday", StepSomeday
		case "reference":
			c.Outcome, c.At = "reference", StepReference
		default:
			c.Outcome, c.At = "action", StepMulti
		}
	case StepSomeday:
		if c.Defer, err = date(in, "defer_until"); err != nil {
			return c, nil, false, err
		}
		return finish()
	case StepReference:
		if c.Project, err = oneOf(in, "project", field("project").Options); err != nil {
			return c, nil, false, err
		}
		if c.Area, err = oneOf(in, "area", field("area").Options); err != nil {
			return c, nil, false, err
		}
		if c.Template, err = oneOf(in, "template", field("template").Options); err != nil {
			return c, nil, false, err
		}
		if title := val("new_project"); title != "" {
			// "New project" for a reference note: the note is filed into it.
			c.Project, c.NewProject = "", &NewProject{Title: title, Area: c.Area}
		}
		return finish()
	case StepMulti:
		c.At = StepAction
		if in.Choice == "yes" {
			c.At = StepProject
		}
	case StepProject:
		if c.Project, err = oneOf(in, "project", field("project").Options); err != nil {
			return c, nil, false, err
		}
		c.NewProject = nil
		if p, ok := env.project(c.Project); ok {
			c.Area = p.Area // preselect the action's area from the project
		} else {
			np := &NewProject{Title: val("title")}
			if np.Title == "" {
				return c, nil, false, required("title")
			}
			if np.Parent, err = oneOf(in, "parent", field("parent").Options); err != nil {
				return c, nil, false, err
			}
			if np.Area, err = oneOf(in, "area", field("area").Options); err != nil {
				return c, nil, false, err
			}
			if np.Target, err = date(in, "target"); err != nil {
				return c, nil, false, err
			}
			c.NewProject, c.Area = np, np.Area
		}
		c.At = StepAction
	case StepAction:
		if in.Choice == "complete" {
			c.Complete, c.Action = true, ""
			return finish()
		}
		if c.Action = val("action"); c.Action == "" {
			return c, nil, false, required("action")
		}
		if c.Area, err = oneOf(in, "area", field("area").Options); err != nil {
			return c, nil, false, err
		}
		c.At = StepTwoMinutes
	case StepTwoMinutes:
		c.At = StepWho
		if in.Choice == "yes" {
			c.At = StepDoNow
		}
	case StepDoNow:
		if in.Choice == "longer" {
			c.At = StepWho
			break
		}
		c.DoneActions, c.Action = append(c.DoneActions, c.Action), ""
		if !c.inProject() {
			return finish()
		}
		c.At = StepAction // a project needs a next action, or it is complete
	case StepWho:
		c.WaitingOn, c.At = "", StepWhen
		if in.Choice == "person" {
			c.At = StepPerson
		}
	case StepPerson:
		if c.WaitingOn = val("waiting_on"); c.WaitingOn == "" {
			return c, nil, false, required("waiting_on")
		}
		c.At = StepWhen
	case StepWhen:
		if c.Context, err = oneOf(in, "context", field("context").Options); err != nil {
			return c, nil, false, err
		}
		if c.Energy, err = oneOf(in, "energy", energies); err != nil {
			return c, nil, false, err
		}
		if c.Time, err = oneOf(in, "time", durations); err != nil {
			return c, nil, false, err
		}
		if c.Defer, err = date(in, "defer_until"); err != nil {
			return c, nil, false, err
		}
		if c.Target, err = date(in, "target"); err != nil {
			return c, nil, false, err
		}
		if c.Due, err = date(in, "due"); err != nil {
			return c, nil, false, err
		}
		if !c.inProject() {
			return finish()
		}
		c.At = StepAfter
	case StepAfter:
		title := val("title")
		if title == "" && in.Choice == "add" {
			return c, nil, false, required("title")
		}
		if title != "" {
			target, err := date(in, "target")
			if err != nil {
				return c, nil, false, err
			}
			c.Followups = append(c.Followups, Followup{Title: title, Target: target})
		}
		if in.Choice == "finish" {
			return finish()
		}
	}
	return c, nil, false, nil
}

func quote(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// ops builds the batch for the decided outcome. The capture itself becomes the
// thing it describes, so `original` and its history stay with it: the note,
// the someday action, the new project, or (with no new project) the action.
func (c Clarify) ops(env Env, newID func() string) []core.Op {
	var ops []core.Op
	rev := c.Rev
	// become turns the capture into kind/state and sets its fields.
	become := func(kind, state string, changes []core.FieldChange) {
		ops = append(ops, core.Op{Via: "web", Type: core.OpTransition, Item: c.Item, BaseRev: rev, Changes: []core.FieldChange{
			{Field: "kind", New: quote(kind)}, {Field: "state", New: quote(state)}}})
		rev++
		changes = change(changes, "body", c.OldBody, c.Body)
		if len(changes) > 0 {
			ops = append(ops, core.Op{Via: "web", Type: core.OpPatch, Item: c.Item, BaseRev: rev, Changes: changes})
			rev++
		}
	}
	create := func(it core.Item) string {
		it.ID = newID()
		it.Source = core.Source{Via: "web"}
		it.DerivedFrom = []string{c.Item}
		ops = append(ops, core.Op{Via: "web", Type: core.OpCreate, Payload: mustJSON(it)})
		return it.ID
	}
	done := func(id string, baseRev uint64) {
		ops = append(ops, core.Op{Via: "web", Type: core.OpTransition, Item: id, BaseRev: baseRev,
			Changes: []core.FieldChange{{Field: "state", New: quote(core.StDone)}}})
	}
	title := func(t string) []core.FieldChange { return change(nil, "title", c.OldTitle, t) }

	switch c.Outcome {
	case "trash":
		return []core.Op{{Via: "web", Type: core.OpTransition, Item: c.Item, BaseRev: rev,
			Changes: []core.FieldChange{{Field: "state", New: quote(core.StTrashed)}}}}
	case "someday":
		become(core.KindAction, core.StSomeday, change(title(c.Title), "defer_until", "", c.Defer))
		return ops
	case "reference":
		project, state := c.Project, core.StUnsorted
		if c.NewProject != nil {
			project = newID()
			ops = append(ops, core.Op{Via: "web", Type: core.OpCreate, Payload: mustJSON(core.Item{
				ID: project, Kind: core.KindProject, Title: c.NewProject.Title, Area: c.NewProject.Area,
				Source: core.Source{Via: "web"}, DerivedFrom: []string{c.Item}})})
		}
		if project != "" || c.Area != "" {
			state = core.StFiled
		}
		ch := change(title(c.Title), "project", "", project)
		ch = change(ch, "area", "", c.Area)
		become(core.KindNote, state, change(ch, "template", "", c.Template))
		return ops
	}

	// Actionable. The action's fields, shared by "the capture becomes the
	// action" and "a new action is created".
	state := core.StNext
	if c.WaitingOn != "" {
		state = core.StWaiting
	}
	project := c.Project
	firstIsCapture := c.NewProject == nil
	if c.NewProject != nil {
		project = c.Item
		ch := change(title(c.NewProject.Title), "parent", "", c.NewProject.Parent)
		ch = change(ch, "area", "", c.NewProject.Area)
		become(core.KindProject, core.StActive, change(ch, "target", "", c.NewProject.Target))
	}
	// Actions done in two minutes, in order. The first may be the capture.
	for _, t := range c.DoneActions {
		if firstIsCapture {
			firstIsCapture = false
			become(core.KindAction, core.StNext, change(change(title(t), "project", "", project), "area", "", c.Area))
			done(c.Item, rev)
			continue
		}
		done(create(core.Item{Kind: core.KindAction, Title: t, Project: project, Area: c.Area, Owner: env.User}), 1)
	}
	if c.Complete {
		// The project is finished with what was done in two minutes.
		base := rev // the capture, now the new project
		if c.NewProject == nil {
			p, _ := env.project(c.Project)
			base = p.Rev
		}
		return append(ops, core.Op{Via: "web", Type: core.OpTransition, Item: project, BaseRev: base,
			Changes: []core.FieldChange{{Field: "state", New: quote(core.StDone)}}})
	}
	if c.Action == "" {
		return ops // single action, done in two minutes
	}
	next := c.Item
	if firstIsCapture {
		ch := change(title(c.Action), "project", "", project)
		ch = change(ch, "area", "", c.Area)
		ch = change(ch, "waiting_on", "", c.WaitingOn)
		ch = change(ch, "context", "", c.Context)
		ch = change(ch, "defer_until", "", c.Defer)
		ch = change(ch, "target", "", c.Target)
		ch = change(ch, "due", "", c.Due)
		ch = change(ch, "energy", "", c.Energy)
		become(core.KindAction, state, change(ch, "time", "", c.Time))
	} else {
		next = create(core.Item{Kind: core.KindAction, State: state, Title: c.Action, Project: project, Area: c.Area,
			Owner: env.User, WaitingOn: c.WaitingOn, Context: c.Context, DeferUntil: c.Defer, Target: c.Target,
			Due: c.Due, Energy: c.Energy, Time: c.Time})
	}
	// Follow-ups: planned, each after the previous one (GTD's project planning).
	for _, f := range c.Followups {
		next = create(core.Item{Kind: core.KindAction, State: core.StPlanned, Title: f.Title, Target: f.Target,
			Project: project, Area: c.Area, Owner: env.User, BlockedBy: []string{next}})
	}
	return ops
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
