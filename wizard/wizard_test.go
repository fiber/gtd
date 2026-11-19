package wizard

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fiber/gtd/core"
)

const today = "2026-11-19" // a Thursday

// world is a personal space with one area, two projects and a capture "c".
type world struct {
	t  *testing.T
	st *core.State
	n  int
	ts time.Time // when set, the time of the ops applied (to make old items)
}

func (w *world) newID() string { w.n++; return fmt.Sprintf("n%d", w.n) }

// apply commits ops in one batch, the way the store does.
func (w *world) apply(ops []core.Op) error {
	pending := map[string]*core.Item{}
	st := &core.State{Space: w.st.Space, Items: w.st.Items, Pending: pending, PersonalContexts: w.st.PersonalContexts}
	for i, op := range ops {
		op.ID = fmt.Sprintf("op%d-%d", w.n, i)
		op.Space, op.Actor = w.st.Space.ID, "robin"
		op.TS = time.Date(2026, 11, 19, 9, 0, i, 0, time.UTC)
		if !w.ts.IsZero() {
			op.TS = w.ts.Add(time.Duration(i) * time.Second)
		}
		res, err := core.Apply(st, op, w.newID)
		if err != nil {
			return fmt.Errorf("op %d (%s on %s): %w", i, op.Type, op.Item, err)
		}
		for _, it := range res.Items {
			pending[it.ID] = it
		}
	}
	for id, it := range pending {
		w.st.Items[id] = it
	}
	return nil
}

func newWorld(t *testing.T) *world {
	w := &world{t: t, st: &core.State{
		Space: &core.Space{ID: "robin", Kind: core.SpacePersonal, Members: []string{"robin"}, People: []string{"Lee"},
			Templates: []core.Template{{Key: "trade_setup", Kind: core.KindNote}, {Key: "meeting", Kind: core.KindNote}}},
		Items:            map[string]*core.Item{},
		PersonalContexts: []string{"boat"},
	}}
	create := func(it core.Item) core.Op { return core.Op{Via: "cli", Type: core.OpCreate, Payload: mustJSON(it)} }
	err := w.apply([]core.Op{
		create(core.Item{ID: "area", Kind: core.KindArea, Title: "Engines"}),
		create(core.Item{ID: "p1", Kind: core.KindProject, Title: "Boat engine handles resets", Area: "area"}),
		create(core.Item{ID: "p2", Kind: core.KindProject, Title: "Example API integrated"}),
		create(core.Item{ID: "pdone", Kind: core.KindProject, Title: "Old project"}),
		create(core.Item{ID: "c", Kind: core.KindCapture, Title: "boat engine reset thing", Original: "boat engine reset thing"}),
		{Via: "cli", Type: core.OpTransition, Item: "pdone", BaseRev: 1, Changes: []core.FieldChange{{Field: "state", New: quote(core.StDone)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// describe renders the items the wizard touched or created, one per line.
func (w *world) describe() []string {
	var out []string
	for id, it := range w.st.Items {
		if slices.Contains([]string{"area", "pdone"}, id) || (strings.HasPrefix(id, "p") && it.Rev == 1) {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s: %s/%s %q", id, it.Kind, it.State, it.Title)
		for _, f := range []struct{ k, v string }{
			{"project", it.Project}, {"parent", it.Parent}, {"area", it.Area}, {"owner", it.Owner}, {"waiting_on", it.WaitingOn},
			{"context", it.Context}, {"defer", it.DeferUntil}, {"target", it.Target}, {"due", it.Due},
			{"energy", it.Energy}, {"time", it.Time}, {"template", it.Template},
			{"blocked_by", strings.Join(it.BlockedBy, ",")}, {"from", strings.Join(it.DerivedFrom, ",")}, {"body", it.Body},
		} {
			if f.v != "" {
				fmt.Fprintf(&b, " %s=%s", f.k, f.v)
			}
		}
		if it.Original != "" && it.Original != "boat engine reset thing" {
			b.WriteString(" ORIGINAL-CHANGED")
		}
		out = append(out, b.String())
	}
	sort.Strings(out)
	return out
}

func in(choice string, kv ...string) Input {
	v := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		v[kv[i]] = kv[i+1]
	}
	return Input{Choice: choice, Values: v}
}

var (
	title       = []string{"title", "Boat engine: clarify reset semantics", "body", ""}
	whatAction  = in("action", title...)
	whatProject = in("project", title...)
	when        = in("continue")
)

// TestClarifyPaths walks every path through Clarify (SPEC §5.2). Each case
// lists the answers, the steps they must lead through, and the items the
// resulting ops produce when applied by core.Apply.
func TestClarifyPaths(t *testing.T) {
	for _, c := range []struct {
		name    string
		answers []Input
		steps   string // the step keys visited, in order
		want    []string
	}{
		{"trash", []Input{in("trash", title...)}, "what",
			[]string{`c: capture/trashed "boat engine reset thing"`}},

		{"someday", []Input{in("someday", title...), in("continue")}, "what someday",
			[]string{`c: action/someday "Boat engine: clarify reset semantics" owner=robin`}},
		{"someday, not before next week", []Input{in("someday", title...), in("continue", "defer_until", "2026-11-23")}, "what someday",
			[]string{`c: action/someday "Boat engine: clarify reset semantics" owner=robin defer=2026-11-23`}},

		{"reference, unsorted", []Input{in("reference", title...), in("continue")}, "what reference",
			[]string{`c: note/unsorted "Boat engine: clarify reset semantics"`}},
		{"reference into a project, edited body", []Input{in("reference", "title", "Reset semantics", "body", "the manual says…\n"), in("continue", "project", "p1")},
			"what reference",
			[]string{`c: note/filed "Reset semantics" project=p1 body=the manual says…`}},
		{"reference into an area with a template", []Input{in("reference", title...), in("continue", "area", "area", "template", "meeting")}, "what reference",
			[]string{`c: note/filed "Boat engine: clarify reset semantics" area=area template=meeting`}},
		{"reference into a new project", []Input{in("reference", title...), in("continue", "new_project", "Engine docs written", "area", "area")}, "what reference",
			[]string{`c: note/filed "Boat engine: clarify reset semantics" project=n1 area=area`, `n1: project/active "Engine docs written" area=area from=c`}},

		{"single action, me, no details", []Input{whatAction, in("continue", "action", "Ask Dana about session resets"), in("no"), in("me"), when},
			"what action two_minutes who when",
			[]string{`c: action/next "Ask Dana about session resets" owner=robin`}},
		{"single action with every detail", []Input{whatAction, in("continue", "action", "Call Lee about the Example API", "area", "area"), in("no"), in("me"),
			in("continue", "context", "calls", "defer_until", "2026-11-20", "target", "2026-12-03", "due", "2026-12-19", "energy", "low", "time", "15m")},
			"what action two_minutes who when",
			[]string{`c: action/next "Call Lee about the Example API" area=area owner=robin context=calls defer=2026-11-20 target=2026-12-03 due=2026-12-19 energy=low time=15m`}},
		{"single action with a personal context and an agenda", []Input{whatAction, in("continue", "action", "Check the mooring"), in("no"), in("me"), in("continue", "context", "boat")},
			"what action two_minutes who when",
			[]string{`c: action/next "Check the mooring" owner=robin context=boat`}},
		{"agenda context for a person", []Input{whatAction, in("continue", "action", "Discuss API limits"), in("no"), in("me"), in("continue", "context", "agenda:Lee")},
			"what action two_minutes who when",
			[]string{`c: action/next "Discuss API limits" owner=robin context=agenda:Lee`}},
		{"delegated to a person: waiting", []Input{whatAction, in("continue", "action", "Get the API key"), in("no"), in("person"), in("continue", "waiting_on", "Lee"), in("continue", "target", "2026-11-26")},
			"what action two_minutes who person when",
			[]string{`c: action/waiting "Get the API key" owner=robin waiting_on=Lee target=2026-11-26`}},
		{"two minutes, done", []Input{whatAction, in("continue", "action", "Reply to Dana"), in("yes"), in("done")},
			"what action two_minutes do_now",
			[]string{`c: action/done "Reply to Dana" owner=robin`}},
		{"two minutes, took longer", []Input{whatAction, in("continue", "action", "Reply to Dana"), in("yes"), in("longer"), in("me"), when},
			"what action two_minutes do_now who when",
			[]string{`c: action/next "Reply to Dana" owner=robin`}},

		{"existing project: the capture becomes its next action", []Input{whatProject, in("continue", "project", "p1"), in("continue", "action", "Read the manual section 6", "area", "area"), in("no"), in("me"), when, in("finish")},
			"what project action two_minutes who when after",
			[]string{`c: action/next "Read the manual section 6" project=p1 area=area owner=robin`}},
		{"existing project with follow-ups", []Input{whatProject, in("continue", "project", "p2"), in("continue", "action", "Call Lee"), in("no"), in("me"), when,
			in("add", "title", "Write the client", "target", "2026-12-10"), in("add", "title", "Test against sandbox"), in("finish", "title", "Go live")},
			"what project action two_minutes who when after after after",
			[]string{`c: action/next "Call Lee" project=p2 owner=robin`,
				`n1: action/planned "Write the client" project=p2 owner=robin target=2026-12-10 blocked_by=c from=c`,
				`n2: action/planned "Test against sandbox" project=p2 owner=robin blocked_by=n1 from=c`,
				`n3: action/planned "Go live" project=p2 owner=robin blocked_by=n2 from=c`}},
		{"new project: the capture becomes the project", []Input{whatProject,
			in("continue", "project", "", "title", "Engine survives session resets", "parent", "p1", "area", "area", "target", "2027-01-15"),
			in("continue", "action", "Ask Dana about reset semantics", "area", "area"), in("no"), in("me"), in("continue", "context", "agenda:Lee"), in("finish")},
			"what project action two_minutes who when after",
			[]string{`c: project/active "Engine survives session resets" parent=p1 area=area target=2027-01-15`,
				`n1: action/next "Ask Dana about reset semantics" project=c area=area owner=robin context=agenda:Lee from=c`}},
		{"new project, first action waits for a person, one follow-up", []Input{whatProject, in("continue", "title", "Example ports ordered"),
			in("continue", "action", "Get a quote"), in("no"), in("person"), in("continue", "waiting_on", "Lee"), when, in("finish", "title", "Sign the order")},
			"what project action two_minutes who person when after",
			[]string{`c: project/active "Example ports ordered"`,
				`n1: action/waiting "Get a quote" project=c owner=robin waiting_on=Lee from=c`,
				`n2: action/planned "Sign the order" project=c owner=robin blocked_by=n1 from=c`}},
		{"project, first action done in two minutes: asks for the next one", []Input{whatProject, in("continue", "project", "p1"),
			in("continue", "action", "Reply to Dana"), in("yes"), in("done"), in("continue", "action", "Read his answer"), in("no"), in("me"), when, in("finish")},
			"what project action two_minutes do_now action two_minutes who when after",
			[]string{`c: action/done "Reply to Dana" project=p1 owner=robin`,
				`n1: action/next "Read his answer" project=p1 owner=robin from=c`}},
		{"existing project complete after a two-minute action", []Input{whatProject, in("continue", "project", "p2"),
			in("continue", "action", "Send the signed form"), in("yes"), in("done"), in("complete")},
			"what project action two_minutes do_now action",
			[]string{`c: action/done "Send the signed form" project=p2 owner=robin`, `p2: project/done "Example API integrated"`}},
		{"new project complete after two two-minute actions", []Input{whatProject, in("continue", "title", "Form sent"),
			in("continue", "action", "Fill in the form"), in("yes"), in("done"), in("continue", "action", "Send it"), in("yes"), in("done"), in("complete")},
			"what project action two_minutes do_now action two_minutes do_now action",
			[]string{`c: project/done "Form sent"`, `n1: action/done "Fill in the form" project=c owner=robin from=c`, `n2: action/done "Send it" project=c owner=robin from=c`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			env := NewEnv(w.st, "robin", today)
			cl := StartClarify(w.st.Items["c"])
			var visited []string
			var ops []core.Op
			done := false
			for i, a := range c.answers {
				if done {
					t.Fatalf("finished before answer %d", i)
				}
				step := cl.Step(env)
				visited = append(visited, step.Key)
				if step.Question == "" || len(step.Options) == 0 || (step.Type == TypeChoice) != (len(step.Fields) == 0) {
					t.Fatalf("step %s is malformed: %+v", step.Key, step)
				}
				var err error
				if cl, ops, done, err = cl.Next(env, a, w.newID); err != nil {
					t.Fatalf("answer %d at %s: %v", i, step.Key, err)
				}
			}
			if !done {
				t.Fatalf("not finished, at %s", cl.At)
			}
			if got := strings.Join(visited, " "); got != c.steps {
				t.Errorf("steps: %s\n want: %s", got, c.steps)
			}
			w.n = 0 // the IDs in ops were n1, n2, …: keep derived op IDs apart
			if err := w.apply(ops); err != nil {
				t.Fatalf("the ops are rejected by core.Apply: %v", err)
			}
			if got := w.describe(); !slices.Equal(got, c.want) {
				t.Errorf("result:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(c.want, "\n  "))
			}
			if w.st.Items["c"].Original != "boat engine reset thing" {
				t.Error("the original capture text changed (invariant 1)")
			}
		})
	}
}

func TestClarifyRejectsBadAnswers(t *testing.T) {
	// at walks to a step with valid answers, then tries a bad one.
	action := []Input{whatAction}
	whenStep := append(slices.Clone(action), in("continue", "action", "Do it"), in("no"), in("me"))
	project := []Input{whatProject}
	for _, c := range []struct {
		name  string
		path  []Input
		bad   Input
		field string
		key   string
	}{
		{"empty title", nil, in("action", "title", "  "), "title", "err.required"},
		{"empty title for a project", nil, in("project", "title", ""), "title", "err.required"},
		{"unknown choice", nil, in("maybe", title...), "choice", "err.choice"},
		{"the old continue is gone", nil, in("continue", title...), "choice", "err.choice"},
		{"no choice", nil, in("", title...), "choice", "err.choice"},
		{"bad someday date", []Input{in("someday", title...)}, in("continue", "defer_until", "next week"), "defer_until", "err.date"},
		{"reference: unknown project", []Input{in("reference", title...)}, in("continue", "project", "nope"), "project", "err.choice"},
		{"reference: finished project", []Input{in("reference", title...)}, in("continue", "project", "pdone"), "project", "err.choice"},
		{"reference: unknown template", []Input{in("reference", title...)}, in("continue", "template", "nope"), "template", "err.choice"},
		{"project: neither existing nor title", project, in("continue", "title", ""), "title", "err.required"},
		{"project: unknown parent", project, in("continue", "title", "X done", "parent", "nope"), "parent", "err.choice"},
		{"project: bad target", project, in("continue", "title", "X done", "target", "2027-13-01"), "target", "err.date"},
		{"empty action", action, in("continue", "action", ""), "action", "err.required"},
		{"action: unknown area", action, in("continue", "action", "Do it", "area", "p1"), "area", "err.choice"},
		{"complete is not offered before something is done", action, in("complete"), "choice", "err.choice"},
		{"person without a name", append(slices.Clone(action), in("continue", "action", "Do it"), in("no"), in("person")), in("continue"), "waiting_on", "err.required"},
		{"when: context not allowed in this space", whenStep, in("continue", "context", "agenda:nobody"), "context", "err.choice"},
		{"when: bad energy", whenStep, in("continue", "energy", "huge"), "energy", "err.choice"},
		{"when: bad time", whenStep, in("continue", "time", "3h"), "time", "err.choice"},
		{"when: bad due date", whenStep, in("continue", "due", "soon"), "due", "err.date"},
		{"after: add without a title", append(slices.Clone(project), in("continue", "project", "p1"), in("continue", "action", "Do it"), in("no"), in("me"), when), in("add"), "title", "err.required"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			env := NewEnv(w.st, "robin", today)
			cl := StartClarify(w.st.Items["c"])
			for _, a := range c.path {
				var err error
				if cl, _, _, err = cl.Next(env, a, w.newID); err != nil {
					t.Fatal(err)
				}
			}
			next, ops, done, err := cl.Next(env, c.bad, w.newID)
			var ie *InputError
			if !errors.As(err, &ie) || ie.Field != c.field || ie.Key != c.key || !errors.Is(err, core.ErrInvalid) {
				t.Fatalf("err = %v, want %s on %s", err, c.key, c.field)
			}
			if done || ops != nil || next.At != cl.At {
				t.Fatalf("a rejected answer moved the wizard: at %s, done %v", next.At, done)
			}
		})
	}
}

func TestClarifySteps(t *testing.T) {
	w := newWorld(t)
	env := NewEnv(w.st, "robin", today)
	cl := StartClarify(w.st.Items["c"])

	s := cl.Step(env)
	if s.Data["original"] != "boat engine reset thing" || s.Fields[0].Value != "boat engine reset thing" {
		t.Errorf("step what: %+v", s)
	}
	var first []string
	for _, o := range s.Options {
		first = append(first, o.Value)
	}
	if want := []string{"action", "project", "reference", "someday", "trash"}; !slices.Equal(first, want) || s.Type != TypeForm {
		t.Errorf("the first screen offers %v (%s), want %v", first, s.Type, want)
	}
	// Trash needs no title.
	if next, ops, done, err := cl.Next(env, in("trash", "title", ""), w.newID); err != nil || !done || len(ops) != 1 || next.Outcome != "trash" {
		t.Errorf("trash without a title: %v %v %v", err, done, ops)
	}
	cl, _, _, _ = cl.Next(env, whatProject, w.newID)
	s = cl.Step(env)
	var projects []string
	for _, o := range s.Fields[0].Options {
		projects = append(projects, o.Value+"="+o.Text+o.Label)
	}
	// Open projects only, by title, led by "new project".
	if want := []string{"=option.new_project", "p1=Boat engine handles resets", "p2=Example API integrated"}; !slices.Equal(projects, want) {
		t.Errorf("project options = %v", projects)
	}
	cl, _, _, _ = cl.Next(env, in("continue", "project", "p1"), w.newID)
	s = cl.Step(env)
	if s.Subject != "Boat engine handles resets" || s.Fields[1].Value != "area" {
		t.Errorf("action step should name the project and preselect its area: %+v", s)
	}
	cl, _, _, _ = cl.Next(env, in("continue", "action", "Do it"), w.newID)
	cl, _, _, _ = cl.Next(env, in("yes"), w.newID)
	if s := cl.Step(env); s.Data["timer"] != 120 {
		t.Errorf("do_now has no two-minute timer: %+v", s.Data)
	}
	cl, _, _, _ = cl.Next(env, in("longer"), w.newID)
	cl, _, _, _ = cl.Next(env, in("me"), w.newID)
	s = cl.Step(env)
	var contexts, presets []string
	for _, o := range s.Fields[0].Options {
		contexts = append(contexts, o.Value)
	}
	for _, o := range s.Fields[1].Options {
		presets = append(presets, o.Label+"="+o.Value)
	}
	if want := []string{"errands", "calls", "computer", "home", "office", "anywhere", "boat", "agenda:Lee"}; !slices.Equal(contexts, want) {
		t.Errorf("contexts = %v", contexts)
	}
	// Today is Thursday 2026-11-19: next week starts Monday the 23rd.
	if want := []string{"date.tomorrow=2026-11-20", "date.next_week=2026-11-23", "date.two_weeks=2026-12-03", "date.one_month=2026-12-19"}; !slices.Equal(presets, want) {
		t.Errorf("date presets = %v", presets)
	}
	// On a Monday, "next week" is the following Monday.
	if got := DatePresets("2026-11-23")[1].Value; got != "2026-11-30" {
		t.Errorf("next week from a Monday = %s", got)
	}
}

func TestProcessNote(t *testing.T) {
	const body = "# Call with Lee\r\n\r\n- [ ] call Lee about Example API\r\n- [x] already done\n" +
		"  * [ ] order the cross-connect\n- [ ]   \n- [ ] write up the notes\n```\n- [ ] not a task, code\n```\nplain line\n- [[n9]]\n"
	setup := func(t *testing.T) (*world, Env, Note) {
		w := newWorld(t)
		err := w.apply([]core.Op{{Via: "cli", Type: core.OpCreate, Payload: mustJSON(core.Item{
			ID: "note", Kind: core.KindNote, Title: "Call with Lee", Body: body, Project: "p2"})}})
		if err != nil {
			t.Fatal(err)
		}
		n, ok := StartNote(w.st.Items["note"])
		if !ok {
			t.Fatal("no task lines found")
		}
		return w, NewEnv(w.st, "robin", today), n
	}

	t.Run("task lines", func(t *testing.T) {
		_, _, n := setup(t)
		var got []string
		for _, l := range n.Lines {
			got = append(got, fmt.Sprintf("%d|%s|%s", l.Line, l.Prefix, l.Text))
		}
		want := []string{"2|- |call Lee about Example API", "4|  * |order the cross-connect", "6|- |write up the notes"}
		if !slices.Equal(got, want) {
			t.Fatalf("lines = %q", got)
		}
		if _, ok := StartNote(&core.Item{Body: "no tasks\n- [x] done"}); ok {
			t.Fatal("a note without open tasks should not start the wizard")
		}
	})

	t.Run("create, skip, create", func(t *testing.T) {
		w, env, n := setup(t)
		s := n.Step(env)
		if s.Fields[0].Value != "call Lee about Example API" || s.Fields[1].Value != "p2" || s.Data["total"] != 3 {
			t.Fatalf("first step: %+v", s)
		}
		n, _, _, err := n.Next(env, in("create", "action", "Call Lee about the Example API", "project", "p2", "context", "calls", "target", "2026-11-26"), w.newID)
		if err != nil {
			t.Fatal(err)
		}
		n, _, done, _ := n.Next(env, in("skip"), w.newID)
		if done {
			t.Fatal("done too early")
		}
		_, ops, done, err := n.Next(env, in("create", "action", "Write up the call notes", "project", "p1", "defer_until", "2026-11-20"), w.newID)
		if err != nil || !done {
			t.Fatalf("done = %v, err = %v", done, err)
		}
		w.n = 100
		if err := w.apply(ops); err != nil {
			t.Fatalf("ops rejected: %v", err)
		}
		a, b := w.st.Items["n1"], w.st.Items["n2"]
		if a.Title != "Call Lee about the Example API" || a.Project != "p2" || a.Context != "calls" || a.Target != "2026-11-26" ||
			a.State != core.StNext || a.Owner != "robin" || !slices.Equal(a.DerivedFrom, []string{"note"}) {
			t.Errorf("first action: %+v", a)
		}
		// p1 has an area: the action inherits it.
		if b.Project != "p1" || b.Area != "area" || b.DeferUntil != "2026-11-20" {
			t.Errorf("second action: %+v", b)
		}
		want := strings.NewReplacer("- [ ] call Lee about Example API", "- [[n1]]", "- [ ] write up the notes", "- [[n2]]").Replace(body)
		if got := w.st.Items["note"].Body; got != want {
			t.Errorf("note body:\n%q\nwant:\n%q", got, want)
		}
		// The processed lines are gone: only the skipped one is left.
		if left := TaskLines(w.st.Items["note"].Body); len(left) != 1 || left[0].Text != "order the cross-connect" {
			t.Errorf("left = %+v", left)
		}
	})

	t.Run("stop keeps what was confirmed", func(t *testing.T) {
		w, env, n := setup(t)
		n, _, _, _ = n.Next(env, in("create", "action", "Call Lee", "project", ""), w.newID)
		_, ops, done, _ := n.Next(env, in("stop"), w.newID)
		if !done || len(ops) != 2 {
			t.Fatalf("done = %v, %d ops", done, len(ops))
		}
		if err := w.apply(ops); err != nil {
			t.Fatal(err)
		}
		if it := w.st.Items["n1"]; it.Project != "" || it.Title != "Call Lee" {
			t.Errorf("action: %+v", it)
		}
	})

	t.Run("skip everything: no ops", func(t *testing.T) {
		w, env, n := setup(t)
		for i := 0; i < 3; i++ {
			var ops []core.Op
			var done bool
			n, ops, done, _ = n.Next(env, in("skip"), w.newID)
			if done != (i == 2) || ops != nil {
				t.Fatalf("step %d: done %v, ops %v", i, done, ops)
			}
		}
	})

	t.Run("bad answers", func(t *testing.T) {
		w, env, n := setup(t)
		for _, bad := range []Input{in("create", "action", " "), in("create", "action", "x", "project", "nope"),
			in("create", "action", "x", "context", "moon"), in("create", "action", "x", "target", "soon"), in("later")} {
			if _, _, _, err := n.Next(env, bad, w.newID); !errors.Is(err, core.ErrInvalid) {
				t.Errorf("%+v: %v", bad, err)
			}
		}
	})
}

func TestClarifyWithSimilarItems(t *testing.T) {
	w := newWorld(t)
	env := NewEnv(w.st, "robin", today)
	env.Similar = []Similar{
		{ID: "p1", Space: "robin", Title: "Boat engine handles resets", Kind: core.KindProject, State: core.StActive, Score: 0.95, Duplicate: true},
		{ID: "far", Space: "net", Title: "In another space", Kind: core.KindNote, Score: 0.7},
	}
	env.SuggestProjects = []string{"p2", "gone", "p1"}
	cl := StartClarify(w.st.Items["c"])
	s := cl.Step(env)
	// Both similar items are shown; only the one in this space can take the capture.
	if sim, _ := s.Data["similar"].([]Similar); len(sim) != 2 {
		t.Fatalf("similar = %+v", s.Data["similar"])
	}
	var opts []string
	for _, o := range s.Options {
		opts = append(opts, o.Value)
	}
	if !slices.Equal(opts, []string{"action", "project", "reference", "someday", "trash", "dup:p1"}) {
		t.Fatalf("options = %v", opts)
	}
	if _, _, _, err := cl.Next(env, in("dup:far"), w.newID); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("duplicate of an item in another space: %v", err)
	}
	_, ops, done, err := cl.Next(env, in("dup:p1"), w.newID)
	if err != nil || !done || len(ops) != 2 || ops[0].Type != core.OpComment || ops[0].Item != "p1" {
		t.Fatalf("duplicate: done %v, err %v, ops %+v", done, err, ops)
	}
	var cm core.Comment
	json.Unmarshal(ops[0].Payload, &cm)
	if cm.Body != "boat engine reset thing" {
		t.Errorf("the comment should carry the capture's text: %q", cm.Body)
	}
	if err := w.apply(ops); err != nil {
		t.Fatal(err)
	}
	if got := w.st.Items["c"]; got.State != core.StTrashed || got.Kind != core.KindCapture || got.Original != "boat engine reset thing" {
		t.Errorf("capture after duplicate: %+v", got)
	}

	// Suggested projects are offered on the project fields, in order, and only
	// those that are real choices. Nothing is preselected by a suggestion.
	w2 := newWorld(t)
	env2 := NewEnv(w2.st, "robin", today)
	env2.SuggestProjects = env.SuggestProjects
	cl = StartClarify(w2.st.Items["c"])
	cl, _, _, _ = cl.Next(env2, in("reference", title...), w2.newID)
	f := cl.Step(env2).Fields[0]
	if len(f.Suggest) != 2 || f.Suggest[0].Value != "p2" || f.Suggest[1].Value != "p1" || f.Value != "" {
		t.Errorf("suggestions = %+v, value %q", f.Suggest, f.Value)
	}
	// Without an index the wizard is unchanged.
	if s := StartClarify(w2.st.Items["c"]).Step(NewEnv(w2.st, "robin", today)); len(s.Options) != 5 || s.Data["similar"] != nil {
		t.Errorf("step without similarity: %+v", s)
	}
}

// TestSweep walks every decision for a cluster (SPEC §5.7).
func TestSweep(t *testing.T) {
	setup := func(t *testing.T) (*world, Env, Sweep) {
		w := newWorld(t)
		var notes []*core.Item
		for i, title := range []string{"Tile samples", "Plumber quote", "Paint colours", "Old receipt"} {
			id := fmt.Sprintf("s%d", i)
			it := core.Item{ID: id, Kind: core.KindNote, State: core.StUnsorted, Title: title, Imported: map[string]any{"folder": "Home/Kitchen"}}
			if i == 1 {
				it.Body = "- [ ] call back\n- [x] done\n- [ ] compare"
				it.Imported = map[string]any{"folder": "Inbox"}
			}
			if err := w.apply([]core.Op{{Via: "cli", Type: core.OpCreate, Payload: mustJSON(it)}}); err != nil {
				t.Fatal(err)
			}
			notes = append(notes, w.st.Items[id])
		}
		return w, NewEnv(w.st, "robin", today), StartSweep(notes, 0.71, false, 2, 5)
	}
	_, env, sw := setup(t)
	s := sw.Step(env)
	if s.Subject != "Tile samples · Plumber quote · Paint colours" || s.Fields[0].Type != "checks" || s.Fields[0].Value != "s0,s1,s2,s3" ||
		len(s.Fields[0].Options) != 4 || s.Data["index"] != 2 || s.Data["total"] != 5 || s.Question != "sweep.cluster" {
		t.Errorf("step: %+v", s)
	}
	if f := s.Data["folders"].([]string); !slices.Equal(f, []string{"Home/Kitchen", "Inbox"}) {
		t.Errorf("folder hint: %v", f)
	}
	if n := s.Data["notes"].([]SweepNote); n[1].Todos != 2 || n[1].Folder != "Inbox" {
		t.Errorf("notes: %+v", n)
	}
	sw.Loose = true
	if sw.Step(env).Question != "sweep.loose" {
		t.Error("the loose cluster is not announced as such")
	}

	for _, c := range []struct {
		name string
		in   Input
		want []string
	}{
		{"skip changes nothing", in("skip"),
			[]string{`s0: note/unsorted "Tile samples"`, `s1: note/unsorted "Plumber quote"`, `s2: note/unsorted "Paint colours"`, `s3: note/unsorted "Old receipt"`}},
		{"file the checked ones into a project", in("file", "members", "s0,s2", "project", "p1"),
			[]string{`s0: note/filed "Tile samples" project=p1`, `s1: note/unsorted "Plumber quote"`, `s2: note/filed "Paint colours" project=p1`, `s3: note/unsorted "Old receipt"`}},
		{"new project from the cluster", in("project", "members", "s0,s1,s2", "new_project", "Kitchen renovated", "area", "area"),
			[]string{`n1: project/active "Kitchen renovated" area=area`, `s0: note/filed "Tile samples" project=n1`, `s1: note/filed "Plumber quote" project=n1`, `s2: note/filed "Paint colours" project=n1`, `s3: note/unsorted "Old receipt"`}},
		{"new theme", in("theme", "members", "s3", "new_project", "Paperwork"),
			[]string{`n1: project/someday "Paperwork"`, `s0: note/unsorted "Tile samples"`, `s1: note/unsorted "Plumber quote"`, `s2: note/unsorted "Paint colours"`, `s3: note/filed "Old receipt" project=n1`}},
		{"into an area", in("area", "members", "s0,s1,s2,s3", "area", "area"),
			[]string{`s0: note/filed "Tile samples" area=area`, `s1: note/filed "Plumber quote" area=area`, `s2: note/filed "Paint colours" area=area`, `s3: note/filed "Old receipt" area=area`}},
		{"archive", in("archive", "members", "s3"),
			[]string{`s0: note/unsorted "Tile samples"`, `s1: note/unsorted "Plumber quote"`, `s2: note/unsorted "Paint colours"`, `s3: note/archived "Old receipt"`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, env, sw := setup(t)
			_, ops, done, err := sw.Next(env, c.in, w.newID)
			if err != nil || !done {
				t.Fatalf("done %v, err %v", done, err)
			}
			if err := w.apply(ops); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, line := range w.describe() {
				if strings.HasPrefix(line, "s") || strings.HasPrefix(line, "n") {
					line, _, _ = strings.Cut(line, " body=")
					got = append(got, line)
				}
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("got\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(c.want, "\n  "))
			}
		})
	}
	for _, c := range []struct {
		name       string
		in         Input
		field, key string
	}{
		{"nothing checked", in("archive", "members", ""), "members", "err.required"},
		{"a note that is not in the cluster", in("archive", "members", "s0,p1"), "members", "err.choice"},
		{"a note twice", in("archive", "members", "s0,s0"), "members", "err.choice"},
		{"file without a project", in("file", "members", "s0"), "project", "err.required"},
		{"file into an unknown project", in("file", "members", "s0", "project", "nope"), "project", "err.choice"},
		{"new project without a title", in("project", "members", "s0"), "new_project", "err.required"},
		{"area without an area", in("area", "members", "s0"), "area", "err.required"},
		{"unknown choice", in("delete", "members", "s0"), "choice", "err.choice"},
	} {
		w, env, sw := setup(t)
		_, ops, done, err := sw.Next(env, c.in, w.newID)
		var ie *InputError
		if !errors.As(err, &ie) || ie.Field != c.field || ie.Key != c.key || done || ops != nil {
			t.Errorf("%s: err %v, done %v", c.name, err, done)
		}
	}
}

// TestClarifySpace: with a shared space to choose, Clarify's first screen
// also asks where the capture belongs, preset to "stays" (SPEC §5.2 step 2).
func TestClarifySpace(t *testing.T) {
	w := newWorld(t)
	env := NewEnv(w.st, "robin", today)
	if s := StartClarify(w.st.Items["c"]).Step(env); len(s.Fields) != 2 {
		t.Fatalf("without shared spaces the first screen has %d fields", len(s.Fields))
	}
	env.Spaces = []Ref{{ID: "robin"}, {ID: "net", Title: "NET · Robin, Dana"}}
	s := StartClarify(w.st.Items["c"]).Step(env)
	f := s.Fields[len(s.Fields)-1]
	if s.Key != StepWhat || f.Key != "space" || f.Type != "chips" || f.Value != "robin" || len(f.Options) != 2 ||
		f.Options[0].Label != "clarify.opt.personal" || f.Options[1].Text != "NET · Robin, Dana" || f.Hint != "" {
		t.Fatalf("space field: %+v", f)
	}
	start := func(choice, space string) (Clarify, error) {
		cl, _, _, err := StartClarify(w.st.Items["c"]).Next(env, in(choice, append([]string{"space", space}, title...)...), w.newID)
		return cl, err
	}
	if _, err := start("action", "elsewhere"); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("a space that is not offered: %v", err)
	}
	// No extra step: each outcome goes straight on, staying or moving.
	for choice, next := range map[string]string{"action": StepAction, "project": StepProject, "reference": StepReference, "someday": StepSomeday} {
		for space, want := range map[string]string{"robin": "", "": "", "net": "net"} {
			if cl, err := start(choice, space); err != nil || cl.At != next || cl.Space != want {
				t.Errorf("%s with space %q: at %s, space %q, %v", choice, space, cl.At, cl.Space, err)
			}
		}
	}
	// Trash does not move anything.
	if cl, ops, done, err := StartClarify(w.st.Items["c"]).Next(env, in("trash", "space", "net"), w.newID); err != nil || !done || len(ops) != 1 || cl.Space != "" {
		t.Errorf("trash with a space chosen: space %q, %v %v", cl.Space, done, err)
	}
	// Moving: the ops are based on the revision after the arrival, and the
	// action gets the one who clarified it as its owner.
	walk := func(space string) []core.Op {
		cl, err := start("action", space)
		if err != nil {
			t.Fatal(err)
		}
		var ops []core.Op
		for _, a := range []Input{in("continue", "action", "Do it"), in("no"), in("me"), in("continue")} {
			if cl, ops, _, err = cl.Next(env, a, w.newID); err != nil {
				t.Fatal(err)
			}
		}
		return ops
	}
	stay, move := walk("robin"), walk("net")
	rev := w.st.Items["c"].Rev
	if stay[0].BaseRev != rev || move[0].BaseRev != rev+1 {
		t.Errorf("base revisions: staying %d, moving %d, capture at %d", stay[0].BaseRev, move[0].BaseRev, rev)
	}
	owner := func(ops []core.Op) bool {
		for _, op := range ops {
			for _, c := range op.Changes {
				if c.Field == "owner" && string(c.New) == `"robin"` {
					return true
				}
			}
		}
		return false
	}
	if owner(stay) || !owner(move) {
		t.Errorf("owner set: staying %v (want false), moving %v (want true)", owner(stay), owner(move))
	}
}

// TestClarifySend: with a linked server, the first screen offers to send the
// capture there. It is then trashed here, with a line that says where it went.
func TestClarifySend(t *testing.T) {
	w := newWorld(t)
	env := NewEnv(w.st, "robin", today)
	if s := StartClarify(w.st.Items["c"]).Step(env); len(s.Options) != 5 {
		t.Fatalf("without links: %d options", len(s.Options))
	}
	env.Links = []string{"work"}
	s := StartClarify(w.st.Items["c"]).Step(env)
	if last := s.Options[len(s.Options)-1]; last.Value != "send:work" || last.Label != "clarify.opt.send" || last.Text != "work" {
		t.Fatalf("options: %+v", s.Options)
	}
	cl, ops, done, err := StartClarify(w.st.Items["c"]).Next(env, in("send:work", "title", ""), w.newID)
	if err != nil || !done || cl.SendTo != "work" || cl.Outcome != "send" {
		t.Fatalf("send: %+v done %v err %v", cl, done, err)
	}
	if len(ops) != 2 || ops[0].Type != core.OpComment || ops[1].Type != core.OpTransition || !strings.Contains(string(ops[1].Changes[0].New), core.StTrashed) {
		t.Errorf("ops: %+v", ops)
	}
	if err := w.apply(ops); err != nil {
		t.Fatal(err)
	}
	if it := w.st.Items["c"]; it.State != core.StTrashed || it.Original != "boat engine reset thing" {
		t.Errorf("the capture here: %s, original %q", it.State, it.Original)
	}
	if _, _, _, err := StartClarify(w.st.Items["c"]).Next(env, in("send:elsewhere", title...), w.newID); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("a link that does not exist: %v", err)
	}
}
