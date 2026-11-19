package wizard

import (
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
}

func (w *world) newID() string { w.n++; return fmt.Sprintf("n%d", w.n) }

// apply commits ops in one batch, the way the store does.
func (w *world) apply(ops []core.Op) error {
	pending := map[string]*core.Item{}
	st := &core.State{Space: w.st.Space, Items: w.st.Items, Pending: pending, PersonalContexts: w.st.PersonalContexts}
	for i, op := range ops {
		op.ID = fmt.Sprintf("op%d-%d", w.n, i)
		op.Space, op.Actor = w.st.Space.ID, "sven"
		op.TS = time.Date(2026, 11, 19, 9, 0, i, 0, time.UTC)
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
		Space: &core.Space{ID: "sven", Kind: core.SpacePersonal, Members: []string{"sven"}, People: []string{"Brent"},
			Templates: []core.Template{{Key: "trade_setup", Kind: core.KindNote}, {Key: "meeting", Kind: core.KindNote}}},
		Items:            map[string]*core.Item{},
		PersonalContexts: []string{"boat"},
	}}
	create := func(it core.Item) core.Op { return core.Op{Via: "cli", Type: core.OpCreate, Payload: mustJSON(it)} }
	err := w.apply([]core.Op{
		create(core.Item{ID: "area", Kind: core.KindArea, Title: "Collectors"}),
		create(core.Item{ID: "p1", Kind: core.KindProject, Title: "BGP collector handles resets", Area: "area"}),
		create(core.Item{ID: "p2", Kind: core.KindProject, Title: "Equinix API integrated"}),
		create(core.Item{ID: "pdone", Kind: core.KindProject, Title: "Old project"}),
		create(core.Item{ID: "c", Kind: core.KindCapture, Title: "bgp collector reset thing", Original: "bgp collector reset thing"}),
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
		if it.Original != "" && it.Original != "bgp collector reset thing" {
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
	what = in("continue", "title", "BGP collector: clarify reset semantics", "body", "")
	when = in("continue")
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
		{"trash", []Input{what, in("trash")}, "what actionable",
			[]string{`c: capture/trashed "bgp collector reset thing"`}},

		{"someday", []Input{what, in("someday"), in("continue")}, "what actionable someday",
			[]string{`c: action/someday "BGP collector: clarify reset semantics" owner=sven`}},
		{"someday, not before next week", []Input{what, in("someday"), in("continue", "defer_until", "2026-11-23")}, "what actionable someday",
			[]string{`c: action/someday "BGP collector: clarify reset semantics" owner=sven defer=2026-11-23`}},

		{"reference, unsorted", []Input{what, in("reference"), in("continue")}, "what actionable reference",
			[]string{`c: note/unsorted "BGP collector: clarify reset semantics"`}},
		{"reference into a project, edited body", []Input{in("continue", "title", "Reset semantics", "body", "RFC 4271 says…\n"), in("reference"), in("continue", "project", "p1")},
			"what actionable reference",
			[]string{`c: note/filed "Reset semantics" project=p1 body=RFC 4271 says…`}},
		{"reference into an area with a template", []Input{what, in("reference"), in("continue", "area", "area", "template", "meeting")}, "what actionable reference",
			[]string{`c: note/filed "BGP collector: clarify reset semantics" area=area template=meeting`}},
		{"reference into a new project", []Input{what, in("reference"), in("continue", "new_project", "Collector docs written", "area", "area")}, "what actionable reference",
			[]string{`c: note/filed "BGP collector: clarify reset semantics" project=n1 area=area`, `n1: project/active "Collector docs written" area=area from=c`}},

		{"single action, me, no details", []Input{what, in("yes"), in("no"), in("continue", "action", "Ask Eric about session resets"), in("no"), in("me"), when},
			"what actionable multi action two_minutes who when",
			[]string{`c: action/next "Ask Eric about session resets" owner=sven`}},
		{"single action with every detail", []Input{what, in("yes"), in("no"), in("continue", "action", "Call Brent about the Equinix API", "area", "area"), in("no"), in("me"),
			in("continue", "context", "calls", "defer_until", "2026-11-20", "target", "2026-12-03", "due", "2026-12-19", "energy", "low", "time", "15m")},
			"what actionable multi action two_minutes who when",
			[]string{`c: action/next "Call Brent about the Equinix API" area=area owner=sven context=calls defer=2026-11-20 target=2026-12-03 due=2026-12-19 energy=low time=15m`}},
		{"single action with a personal context and an agenda", []Input{what, in("yes"), in("no"), in("continue", "action", "Check the mooring"), in("no"), in("me"), in("continue", "context", "boat")},
			"what actionable multi action two_minutes who when",
			[]string{`c: action/next "Check the mooring" owner=sven context=boat`}},
		{"agenda context for a person", []Input{what, in("yes"), in("no"), in("continue", "action", "Discuss API limits"), in("no"), in("me"), in("continue", "context", "agenda:Brent")},
			"what actionable multi action two_minutes who when",
			[]string{`c: action/next "Discuss API limits" owner=sven context=agenda:Brent`}},
		{"delegated to a person: waiting", []Input{what, in("yes"), in("no"), in("continue", "action", "Get the API key"), in("no"), in("person"), in("continue", "waiting_on", "Brent"), in("continue", "target", "2026-11-26")},
			"what actionable multi action two_minutes who person when",
			[]string{`c: action/waiting "Get the API key" owner=sven waiting_on=Brent target=2026-11-26`}},
		{"two minutes, done", []Input{what, in("yes"), in("no"), in("continue", "action", "Reply to Eric"), in("yes"), in("done")},
			"what actionable multi action two_minutes do_now",
			[]string{`c: action/done "Reply to Eric" owner=sven`}},
		{"two minutes, took longer", []Input{what, in("yes"), in("no"), in("continue", "action", "Reply to Eric"), in("yes"), in("longer"), in("me"), when},
			"what actionable multi action two_minutes do_now who when",
			[]string{`c: action/next "Reply to Eric" owner=sven`}},

		{"existing project: the capture becomes its next action", []Input{what, in("yes"), in("yes"), in("continue", "project", "p1"), in("continue", "action", "Read RFC 4271 section 6", "area", "area"), in("no"), in("me"), when, in("finish")},
			"what actionable multi project action two_minutes who when after",
			[]string{`c: action/next "Read RFC 4271 section 6" project=p1 area=area owner=sven`}},
		{"existing project with follow-ups", []Input{what, in("yes"), in("yes"), in("continue", "project", "p2"), in("continue", "action", "Call Brent"), in("no"), in("me"), when,
			in("add", "title", "Write the client", "target", "2026-12-10"), in("add", "title", "Test against sandbox"), in("finish", "title", "Go live")},
			"what actionable multi project action two_minutes who when after after after",
			[]string{`c: action/next "Call Brent" project=p2 owner=sven`,
				`n1: action/planned "Write the client" project=p2 owner=sven target=2026-12-10 blocked_by=c from=c`,
				`n2: action/planned "Test against sandbox" project=p2 owner=sven blocked_by=n1 from=c`,
				`n3: action/planned "Go live" project=p2 owner=sven blocked_by=n2 from=c`}},
		{"new project: the capture becomes the project", []Input{what, in("yes"), in("yes"),
			in("continue", "project", "", "title", "Collector survives session resets", "parent", "p1", "area", "area", "target", "2027-01-15"),
			in("continue", "action", "Ask Eric about reset semantics", "area", "area"), in("no"), in("me"), in("continue", "context", "agenda:Brent"), in("finish")},
			"what actionable multi project action two_minutes who when after",
			[]string{`c: project/active "Collector survives session resets" parent=p1 area=area target=2027-01-15`,
				`n1: action/next "Ask Eric about reset semantics" project=c area=area owner=sven context=agenda:Brent from=c`}},
		{"new project, first action waits for a person, one follow-up", []Input{what, in("yes"), in("yes"), in("continue", "title", "Equinix ports ordered"),
			in("continue", "action", "Get a quote"), in("no"), in("person"), in("continue", "waiting_on", "Brent"), when, in("finish", "title", "Sign the order")},
			"what actionable multi project action two_minutes who person when after",
			[]string{`c: project/active "Equinix ports ordered"`,
				`n1: action/waiting "Get a quote" project=c owner=sven waiting_on=Brent from=c`,
				`n2: action/planned "Sign the order" project=c owner=sven blocked_by=n1 from=c`}},
		{"project, first action done in two minutes: asks for the next one", []Input{what, in("yes"), in("yes"), in("continue", "project", "p1"),
			in("continue", "action", "Reply to Eric"), in("yes"), in("done"), in("continue", "action", "Read his answer"), in("no"), in("me"), when, in("finish")},
			"what actionable multi project action two_minutes do_now action two_minutes who when after",
			[]string{`c: action/done "Reply to Eric" project=p1 owner=sven`,
				`n1: action/next "Read his answer" project=p1 owner=sven from=c`}},
		{"existing project complete after a two-minute action", []Input{what, in("yes"), in("yes"), in("continue", "project", "p2"),
			in("continue", "action", "Send the signed form"), in("yes"), in("done"), in("complete")},
			"what actionable multi project action two_minutes do_now action",
			[]string{`c: action/done "Send the signed form" project=p2 owner=sven`, `p2: project/done "Equinix API integrated"`}},
		{"new project complete after two two-minute actions", []Input{what, in("yes"), in("yes"), in("continue", "title", "Form sent"),
			in("continue", "action", "Fill in the form"), in("yes"), in("done"), in("continue", "action", "Send it"), in("yes"), in("done"), in("complete")},
			"what actionable multi project action two_minutes do_now action two_minutes do_now action",
			[]string{`c: project/done "Form sent"`, `n1: action/done "Fill in the form" project=c owner=sven from=c`, `n2: action/done "Send it" project=c owner=sven from=c`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			env := NewEnv(w.st, "sven", today)
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
			if w.st.Items["c"].Original != "bgp collector reset thing" {
				t.Error("the original capture text changed (invariant 1)")
			}
		})
	}
}

func TestClarifyRejectsBadAnswers(t *testing.T) {
	// at walks to a step with valid answers, then tries a bad one.
	action := []Input{what, in("yes"), in("no")}
	whenStep := append(slices.Clone(action), in("continue", "action", "Do it"), in("no"), in("me"))
	project := []Input{what, in("yes"), in("yes")}
	for _, c := range []struct {
		name  string
		path  []Input
		bad   Input
		field string
		key   string
	}{
		{"empty title", nil, in("continue", "title", "  "), "title", "err.required"},
		{"unknown choice", []Input{what}, in("maybe"), "choice", "err.choice"},
		{"no choice", []Input{what}, in(""), "choice", "err.choice"},
		{"bad someday date", []Input{what, in("someday")}, in("continue", "defer_until", "next week"), "defer_until", "err.date"},
		{"reference: unknown project", []Input{what, in("reference")}, in("continue", "project", "nope"), "project", "err.choice"},
		{"reference: finished project", []Input{what, in("reference")}, in("continue", "project", "pdone"), "project", "err.choice"},
		{"reference: unknown template", []Input{what, in("reference")}, in("continue", "template", "nope"), "template", "err.choice"},
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
			env := NewEnv(w.st, "sven", today)
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
	env := NewEnv(w.st, "sven", today)
	cl := StartClarify(w.st.Items["c"])

	s := cl.Step(env)
	if s.Data["original"] != "bgp collector reset thing" || s.Fields[0].Value != "bgp collector reset thing" {
		t.Errorf("step what: %+v", s)
	}
	cl, _, _, _ = cl.Next(env, what, w.newID)
	cl, _, _, _ = cl.Next(env, in("yes"), w.newID)
	if s := cl.Step(env); s.Preselect != "no" {
		t.Errorf("multi preselects %q", s.Preselect)
	}
	cl, _, _, _ = cl.Next(env, in("yes"), w.newID)
	s = cl.Step(env)
	var projects []string
	for _, o := range s.Fields[0].Options {
		projects = append(projects, o.Value+"="+o.Text+o.Label)
	}
	// Open projects only, by title, led by "new project".
	if want := []string{"=option.new_project", "p1=BGP collector handles resets", "p2=Equinix API integrated"}; !slices.Equal(projects, want) {
		t.Errorf("project options = %v", projects)
	}
	cl, _, _, _ = cl.Next(env, in("continue", "project", "p1"), w.newID)
	s = cl.Step(env)
	if s.Subject != "BGP collector handles resets" || s.Fields[1].Value != "area" {
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
	if want := []string{"errands", "calls", "computer", "home", "office", "anywhere", "boat", "agenda:Brent"}; !slices.Equal(contexts, want) {
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
	const body = "# Call with Brent\r\n\r\n- [ ] call Brent about Equinix API\r\n- [x] already done\n" +
		"  * [ ] order the cross-connect\n- [ ]   \n- [ ] write up the notes\n```\n- [ ] not a task, code\n```\nplain line\n- [[n9]]\n"
	setup := func(t *testing.T) (*world, Env, Note) {
		w := newWorld(t)
		err := w.apply([]core.Op{{Via: "cli", Type: core.OpCreate, Payload: mustJSON(core.Item{
			ID: "note", Kind: core.KindNote, Title: "Call with Brent", Body: body, Project: "p2"})}})
		if err != nil {
			t.Fatal(err)
		}
		n, ok := StartNote(w.st.Items["note"])
		if !ok {
			t.Fatal("no task lines found")
		}
		return w, NewEnv(w.st, "sven", today), n
	}

	t.Run("task lines", func(t *testing.T) {
		_, _, n := setup(t)
		var got []string
		for _, l := range n.Lines {
			got = append(got, fmt.Sprintf("%d|%s|%s", l.Line, l.Prefix, l.Text))
		}
		want := []string{"2|- |call Brent about Equinix API", "4|  * |order the cross-connect", "6|- |write up the notes"}
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
		if s.Fields[0].Value != "call Brent about Equinix API" || s.Fields[1].Value != "p2" || s.Data["total"] != 3 {
			t.Fatalf("first step: %+v", s)
		}
		n, _, _, err := n.Next(env, in("create", "action", "Call Brent about the Equinix API", "project", "p2", "context", "calls", "target", "2026-11-26"), w.newID)
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
		if a.Title != "Call Brent about the Equinix API" || a.Project != "p2" || a.Context != "calls" || a.Target != "2026-11-26" ||
			a.State != core.StNext || a.Owner != "sven" || !slices.Equal(a.DerivedFrom, []string{"note"}) {
			t.Errorf("first action: %+v", a)
		}
		// p1 has an area: the action inherits it.
		if b.Project != "p1" || b.Area != "area" || b.DeferUntil != "2026-11-20" {
			t.Errorf("second action: %+v", b)
		}
		want := strings.NewReplacer("- [ ] call Brent about Equinix API", "- [[n1]]", "- [ ] write up the notes", "- [[n2]]").Replace(body)
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
		n, _, _, _ = n.Next(env, in("create", "action", "Call Brent", "project", ""), w.newID)
		_, ops, done, _ := n.Next(env, in("stop"), w.newID)
		if !done || len(ops) != 2 {
			t.Fatalf("done = %v, %d ops", done, len(ops))
		}
		if err := w.apply(ops); err != nil {
			t.Fatal(err)
		}
		if it := w.st.Items["n1"]; it.Project != "" || it.Title != "Call Brent" {
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
