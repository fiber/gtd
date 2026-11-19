package core

import (
	"errors"
	"slices"
	"testing"
)

func ids(items []*Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func TestEngage(t *testing.T) {
	w := shared(t)
	add := func(it Item) {
		it.Kind = KindAction
		w.must(w.create("robin", it))
	}
	add(Item{ID: "old", Owner: "robin"})
	add(Item{ID: "target", Owner: "robin", Target: "2026-12-01"})
	add(Item{ID: "due", Owner: "robin", Due: "2026-12-24", Target: "2026-11-20"})
	add(Item{ID: "duesoon", Owner: "robin", Due: "2026-11-25"})
	add(Item{ID: "unassigned"})
	add(Item{ID: "erics", Owner: "dana"})
	add(Item{ID: "deferred", Owner: "robin", DeferUntil: "2026-11-20"})
	add(Item{ID: "resurfaced", Owner: "robin", DeferUntil: "2026-11-19"})
	add(Item{ID: "someday", Owner: "robin", State: StSomeday})
	add(Item{ID: "calls", Owner: "robin", Context: "calls", Time: "5m", Energy: "low"})
	add(Item{ID: "office", Owner: "robin", Context: "office", Time: "2h+", Energy: "high"})
	add(Item{ID: "anywhere", Owner: "robin", Context: "anywhere", Time: "30m", Energy: "med"})
	add(Item{ID: "blocker", Owner: "dana"})
	add(Item{ID: "unblocked", Owner: "robin", BlockedBy: []string{"blocker"}})
	w.must(w.move("dana", "blocker", StDone)) // the system moves "unblocked" to next

	const today = "2026-11-19"
	for _, c := range []struct {
		name string
		user string
		f    EngageFilter
		want []string
	}{
		{"no filter: due, target, unblocked, age", "robin", EngageFilter{},
			[]string{"duesoon", "due", "target", "unblocked", "old", "unassigned", "resurfaced", "calls", "office", "anywhere"}},
		{"dana sees his and the unassigned", "dana", EngageFilter{}, []string{"unassigned", "erics"}},
		{"context: only that place, plus 'anywhere'; actions without a context are left out", "robin", EngageFilter{Contexts: []string{"calls"}},
			[]string{"calls", "anywhere"}},
		{"two contexts", "robin", EngageFilter{Contexts: []string{"calls", "office"}}, []string{"calls", "office", "anywhere"}},
		{"a place with nothing but 'anywhere'", "robin", EngageFilter{Contexts: []string{"errands"}}, []string{"anywhere"}},
		{"15 minutes", "robin", EngageFilter{Time: "15m"},
			[]string{"duesoon", "due", "target", "unblocked", "old", "unassigned", "resurfaced", "calls"}},
		{"low energy", "robin", EngageFilter{Energy: "low"},
			[]string{"duesoon", "due", "target", "unblocked", "old", "unassigned", "resurfaced", "calls"}},
		{"medium energy, one hour", "robin", EngageFilter{Energy: "med", Time: "1h"},
			[]string{"duesoon", "due", "target", "unblocked", "old", "unassigned", "resurfaced", "calls", "anywhere"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := ids(Engage(w.st, c.user, today, c.f)); !slices.Equal(got, c.want) {
				t.Fatalf("got  %v\nwant %v", got, c.want)
			}
		})
	}
	t.Run("personal space: no unassigned rule needed", func(t *testing.T) {
		p := personal(t)
		p.must(p.create("robin", Item{ID: "a", Kind: KindAction}))
		if got := ids(Engage(p.st, "robin", today, EngageFilter{})); !slices.Equal(got, []string{"a"}) {
			t.Fatalf("got %v", got)
		}
	})
}

func TestWaitingForAndDated(t *testing.T) {
	w := shared(t)
	w.must(w.create("robin", Item{ID: "delegated", Kind: KindAction, Owner: "dana", DelegatedBy: "robin"}))
	w.must(w.create("robin", Item{ID: "person", Kind: KindAction, Owner: "robin", State: StWaiting, WaitingOn: "Kim", Due: "2026-11-21"}))
	w.must(w.create("dana", Item{ID: "ericsblocker", Kind: KindAction, Owner: "dana", Target: "2026-11-30"})) // dana's own, not delegated
	w.must(w.create("robin", Item{ID: "blockedonEric", Kind: KindAction, Owner: "robin", BlockedBy: []string{"ericsblocker"}}))
	w.must(w.create("robin", Item{ID: "myblocker", Kind: KindAction, Owner: "robin"}))
	w.must(w.create("robin", Item{ID: "blockedonme", Kind: KindAction, Owner: "robin", BlockedBy: []string{"myblocker"}}))
	w.must(w.create("robin", Item{ID: "delegateddone", Kind: KindAction, Owner: "dana", DelegatedBy: "robin"}))
	w.must(w.move("dana", "delegateddone", StDone))
	w.must(w.create("robin", Item{ID: "proj", Kind: KindProject, Target: "2026-12-03"}))
	w.must(w.create("robin", Item{ID: "far", Kind: KindAction, Target: "2027-06-01"}))

	if got, want := ids(WaitingFor(w.st, "robin")), []string{"delegated", "person", "blockedonEric"}; !slices.Equal(got, want) {
		t.Errorf("waiting for robin = %v, want %v", got, want)
	}
	if got := ids(WaitingFor(w.st, "dana")); len(got) != 0 {
		t.Errorf("waiting for dana = %v", got)
	}
	if got, want := ids(Dated(w.st, "2026-12-03")), []string{"person", "ericsblocker", "proj"}; !slices.Equal(got, want) {
		t.Errorf("dated = %v, want %v", got, want)
	}
	if got := ids(OpenBlockers(w.st, w.st.Items["blockedonEric"])); !slices.Equal(got, []string{"ericsblocker"}) {
		t.Errorf("open blockers = %v", got)
	}
}

func TestRollups(t *testing.T) {
	w := shared(t)
	w.must(w.create("robin", Item{ID: "p", Kind: KindProject, Target: "2027-01-31"}))
	w.must(w.create("robin", Item{ID: "c", Kind: KindProject, Parent: "p"}))
	w.must(w.create("robin", Item{ID: "gone", Kind: KindProject, Parent: "p"}))
	w.must(w.create("robin", Item{ID: "empty", Kind: KindProject}))
	act := func(id, project, state string, it Item) {
		it.ID, it.Kind, it.Project, it.Owner = id, KindAction, project, "robin"
		w.must(w.create("robin", it))
		if state != "" {
			w.must(w.move("robin", id, state))
		}
	}
	act("a1", "p", "", Item{Target: "2026-12-10"})
	act("a2", "p", StDone, Item{Target: "2026-01-01"}) // finished: its old target does not count
	act("a3", "p", StDropped, Item{})                  // dropped: not counted at all
	act("a4", "p", StWaiting, Item{})
	act("b1", "c", "", Item{Target: "2026-11-01"}) // slipping
	act("b2", "c", "", Item{BlockedBy: []string{"b1"}})
	act("g1", "gone", "", Item{})
	w.must(w.move("robin", "gone", StDropped))

	r := Rollups(w.st, "2026-11-19")
	if got, want := r["p"], (Rollup{Done: 1, Total: 5, Blocked: 1, Waiting: 1, EarliestTarget: "2026-11-01", Slipping: true, Alive: true}); got != want {
		t.Errorf("p = %+v\nwant %+v", got, want)
	}
	if got, want := r["c"], (Rollup{Total: 2, Blocked: 1, EarliestTarget: "2026-11-01", Slipping: true, Alive: true}); got != want {
		t.Errorf("c = %+v\nwant %+v", got, want)
	}
	if got := r["empty"]; got != (Rollup{}) {
		t.Errorf("empty = %+v", got)
	}
}

func TestTemplatesAndTickler(t *testing.T) {
	w := shared(t)
	w.st.Space.Templates = []Template{{Key: "trade_setup", Kind: KindNote, Fields: []TemplateField{
		{Key: "ticker", Type: "text"}, {Key: "setup", Type: "enum", Options: []string{"S1", "S2"}},
		{Key: "entry", Type: "number"}, {Key: "expires", Type: "date", Tickler: true}}}}
	if err := ValidateTemplates(w.st.Space.Templates); err != nil {
		t.Fatal(err)
	}
	note := func(id string, fields map[string]string) error {
		return w.create("robin", Item{ID: id, Kind: KindNote, Template: "trade_setup", Fields: fields})
	}
	for name, c := range map[string]struct {
		fields map[string]string
		err    error
	}{
		"valid":             {map[string]string{"ticker": "NVDA", "setup": "S1", "entry": "101.5"}, nil},
		"unknown key kept":  {map[string]string{"comment": "free"}, nil},
		"empty values":      {map[string]string{"setup": "", "entry": ""}, nil},
		"enum not in list":  {map[string]string{"setup": "S9"}, ErrInvalid},
		"number not number": {map[string]string{"entry": "abc"}, ErrInvalid},
		"date not a date":   {map[string]string{"expires": "soon"}, ErrInvalid},
	} {
		if err := note("n-"+name, c.fields); !errors.Is(err, c.err) || (c.err == nil && err != nil) {
			t.Errorf("%s: %v, want %v", name, err, c.err)
		}
	}
	if err := w.create("robin", Item{ID: "x", Kind: KindNote, Template: "nope"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown template: %v", err)
	}
	if err := w.create("robin", Item{ID: "y", Kind: KindProject, Template: "trade_setup"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("template of another kind: %v", err)
	}

	// A tickler date field sets defer_until, on create and on patch.
	w.must(note("tick", map[string]string{"expires": "2026-12-19"}))
	if got := w.st.Items["tick"].DeferUntil; got != "2026-12-19" {
		t.Errorf("defer_until after create = %q", got)
	}
	w.must(w.patch("robin", "tick", "fields", map[string]string{"expires": "2027-01-15"}))
	if got := w.st.Items["tick"].DeferUntil; got != "2027-01-15" {
		t.Errorf("defer_until after patch = %q", got)
	}
	last := w.log[len(w.log)-1]
	if len(last.Changes) != 2 || last.Changes[1].Field != "defer_until" || string(last.Changes[1].Old) != `"2026-12-19"` {
		t.Errorf("the patch op should record the defer_until change: %+v", last.Changes)
	}
	w.must(w.patch("robin", "tick", "title", "renamed")) // unrelated patch: defer_until untouched
	if got := len(w.log[len(w.log)-1].Changes); got != 1 {
		t.Errorf("unrelated patch has %d changes", got)
	}
	checkReplay(t, w)

	for name, tpls := range map[string][]Template{
		"duplicate key":   {{Key: "a", Kind: KindNote}, {Key: "a", Kind: KindNote}},
		"empty key":       {{Kind: KindNote}},
		"bad kind":        {{Key: "a", Kind: KindArea}},
		"bad field type":  {{Key: "a", Kind: KindNote, Fields: []TemplateField{{Key: "f", Type: "color"}}}},
		"enum no options": {{Key: "a", Kind: KindNote, Fields: []TemplateField{{Key: "f", Type: "enum"}}}},
		"tickler on text": {{Key: "a", Kind: KindNote, Fields: []TemplateField{{Key: "f", Type: "text", Tickler: true}}}},
		"duplicate field": {{Key: "a", Kind: KindNote, Fields: []TemplateField{{Key: "f", Type: "text"}, {Key: "f", Type: "text"}}}},
	} {
		if err := ValidateTemplates(tpls); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestContextsAndTransitions(t *testing.T) {
	w := shared(t)
	if got, want := Contexts(w.st, "robin"), []string{"errands", "calls", "computer", "home", "office", "anywhere", "agenda:dana", "agenda:Kim"}; !slices.Equal(got, want) {
		t.Errorf("shared contexts = %v", got)
	}
	p := personal(t)
	if got, want := Contexts(p.st, "robin"), []string{"errands", "calls", "computer", "home", "office", "anywhere", "boat"}; !slices.Equal(got, want) {
		t.Errorf("personal contexts = %v", got)
	}
	if got := Transitions(&Item{Kind: KindArea, State: StActive}); !slices.Equal(got, []string{StRetired}) {
		t.Errorf("transitions = %v", got)
	}
}

func TestToday(t *testing.T) {
	w := shared(t)
	add := func(it Item) {
		it.Kind = KindAction
		w.must(w.create("robin", it))
	}
	const today = "2026-11-19"
	add(Item{ID: "due-today", Owner: "robin", Due: today})
	add(Item{ID: "overdue", Owner: "robin", Due: "2026-11-10"})
	add(Item{ID: "target-today", Owner: "robin", Target: today})
	add(Item{ID: "target-past", Owner: "robin", Target: "2026-11-18"})
	add(Item{ID: "due-tomorrow", Owner: "robin", Due: "2026-11-20"})
	add(Item{ID: "undated", Owner: "robin"})
	add(Item{ID: "back-today", Owner: "robin", DeferUntil: today})
	add(Item{ID: "back-tomorrow", Owner: "robin", DeferUntil: "2026-11-20", Due: today})
	add(Item{ID: "waiting-due", Owner: "robin", State: StWaiting, WaitingOn: "Lee", Target: today})
	add(Item{ID: "waiting-undated", Owner: "robin", State: StWaiting, WaitingOn: "Lee"})
	add(Item{ID: "someday-due", Owner: "robin", State: StSomeday, Due: today})
	add(Item{ID: "danas", Owner: "dana", Due: today})
	add(Item{ID: "unassigned", Due: today})
	add(Item{ID: "done-due", Owner: "robin", Due: today})
	w.must(w.move("robin", "done-due", StDone))
	w.must(w.create("robin", Item{ID: "project-due", Kind: KindProject, Due: today}))

	// Overdue first, then due today, then targets; the one that is back last.
	if got, want := ids(Today(w.st, "robin", today)), []string{"overdue", "due-today", "unassigned", "target-past", "target-today", "waiting-due", "back-today"}; !slices.Equal(got, want) {
		t.Errorf("robin's today:\n got %v\nwant %v", got, want)
	}
	if got, want := ids(Today(w.st, "dana", today)), []string{"danas", "unassigned"}; !slices.Equal(got, want) {
		t.Errorf("dana's today: %v, want %v", got, want)
	}
	if got := Today(w.st, "robin", "2026-11-01"); len(got) != 0 {
		t.Errorf("an earlier day: %v", ids(got))
	}
	// The tickler clears defer_until on the day; the action stays on the list.
	res := Tickle(w.st, today)
	for _, op := range res {
		op.Actor = System
		if _, err := w.apply(op); err != nil {
			t.Fatal(err)
		}
	}
	if got := ids(Today(w.st, "robin", today)); !slices.Contains(got, "back-today") {
		t.Errorf("after the tickler ran: %v", got)
	}
}
