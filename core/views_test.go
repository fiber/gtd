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
		w.must(w.create("sven", it))
	}
	add(Item{ID: "old", Owner: "sven"})
	add(Item{ID: "target", Owner: "sven", Target: "2026-12-01"})
	add(Item{ID: "due", Owner: "sven", Due: "2026-12-24", Target: "2026-11-20"})
	add(Item{ID: "duesoon", Owner: "sven", Due: "2026-11-25"})
	add(Item{ID: "unassigned"})
	add(Item{ID: "erics", Owner: "eric"})
	add(Item{ID: "deferred", Owner: "sven", DeferUntil: "2026-11-20"})
	add(Item{ID: "resurfaced", Owner: "sven", DeferUntil: "2026-11-19"})
	add(Item{ID: "someday", Owner: "sven", State: StSomeday})
	add(Item{ID: "calls", Owner: "sven", Context: "calls", Time: "5m", Energy: "low"})
	add(Item{ID: "office", Owner: "sven", Context: "office", Time: "2h+", Energy: "high"})
	add(Item{ID: "anywhere", Owner: "sven", Context: "anywhere", Time: "30m", Energy: "med"})
	add(Item{ID: "blocker", Owner: "eric"})
	add(Item{ID: "unblocked", Owner: "sven", BlockedBy: []string{"blocker"}})
	w.must(w.move("eric", "blocker", StDone)) // the system moves "unblocked" to next

	const today = "2026-11-19"
	for _, c := range []struct {
		name string
		user string
		f    EngageFilter
		want []string
	}{
		{"no filter: due, target, unblocked, age", "sven", EngageFilter{},
			[]string{"duesoon", "due", "target", "unblocked", "old", "unassigned", "resurfaced", "calls", "office", "anywhere"}},
		{"eric sees his and the unassigned", "eric", EngageFilter{}, []string{"unassigned", "erics"}},
		{"context: actions without context and 'anywhere' always match", "sven", EngageFilter{Contexts: []string{"calls"}},
			[]string{"duesoon", "due", "target", "unblocked", "old", "unassigned", "resurfaced", "calls", "anywhere"}},
		{"two contexts", "sven", EngageFilter{Contexts: []string{"calls", "office"}},
			[]string{"duesoon", "due", "target", "unblocked", "old", "unassigned", "resurfaced", "calls", "office", "anywhere"}},
		{"15 minutes", "sven", EngageFilter{Time: "15m"},
			[]string{"duesoon", "due", "target", "unblocked", "old", "unassigned", "resurfaced", "calls"}},
		{"low energy", "sven", EngageFilter{Energy: "low"},
			[]string{"duesoon", "due", "target", "unblocked", "old", "unassigned", "resurfaced", "calls"}},
		{"medium energy, one hour", "sven", EngageFilter{Energy: "med", Time: "1h"},
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
		p.must(p.create("sven", Item{ID: "a", Kind: KindAction}))
		if got := ids(Engage(p.st, "sven", today, EngageFilter{})); !slices.Equal(got, []string{"a"}) {
			t.Fatalf("got %v", got)
		}
	})
}

func TestWaitingForAndDated(t *testing.T) {
	w := shared(t)
	w.must(w.create("sven", Item{ID: "delegated", Kind: KindAction, Owner: "eric", DelegatedBy: "sven"}))
	w.must(w.create("sven", Item{ID: "person", Kind: KindAction, Owner: "sven", State: StWaiting, WaitingOn: "Kim", Due: "2026-11-21"}))
	w.must(w.create("sven", Item{ID: "ericsblocker", Kind: KindAction, Owner: "eric", Target: "2026-11-30"}))
	w.must(w.create("sven", Item{ID: "blockedonEric", Kind: KindAction, Owner: "sven", BlockedBy: []string{"ericsblocker"}}))
	w.must(w.create("sven", Item{ID: "myblocker", Kind: KindAction, Owner: "sven"}))
	w.must(w.create("sven", Item{ID: "blockedonme", Kind: KindAction, Owner: "sven", BlockedBy: []string{"myblocker"}}))
	w.must(w.create("sven", Item{ID: "delegateddone", Kind: KindAction, Owner: "eric", DelegatedBy: "sven"}))
	w.must(w.move("eric", "delegateddone", StDone))
	w.must(w.create("sven", Item{ID: "proj", Kind: KindProject, Target: "2026-12-03"}))
	w.must(w.create("sven", Item{ID: "far", Kind: KindAction, Target: "2027-06-01"}))

	if got, want := ids(WaitingFor(w.st, "sven")), []string{"delegated", "person", "blockedonEric"}; !slices.Equal(got, want) {
		t.Errorf("waiting for sven = %v, want %v", got, want)
	}
	if got := ids(WaitingFor(w.st, "eric")); len(got) != 0 {
		t.Errorf("waiting for eric = %v", got)
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
	w.must(w.create("sven", Item{ID: "p", Kind: KindProject, Target: "2027-01-31"}))
	w.must(w.create("sven", Item{ID: "c", Kind: KindProject, Parent: "p"}))
	w.must(w.create("sven", Item{ID: "gone", Kind: KindProject, Parent: "p"}))
	w.must(w.create("sven", Item{ID: "empty", Kind: KindProject}))
	act := func(id, project, state string, it Item) {
		it.ID, it.Kind, it.Project, it.Owner = id, KindAction, project, "sven"
		w.must(w.create("sven", it))
		if state != "" {
			w.must(w.move("sven", id, state))
		}
	}
	act("a1", "p", "", Item{Target: "2026-12-10"})
	act("a2", "p", StDone, Item{Target: "2026-01-01"}) // finished: its old target does not count
	act("a3", "p", StDropped, Item{})                  // dropped: not counted at all
	act("a4", "p", StWaiting, Item{})
	act("b1", "c", "", Item{Target: "2026-11-01"}) // slipping
	act("b2", "c", "", Item{BlockedBy: []string{"b1"}})
	act("g1", "gone", "", Item{})
	w.must(w.move("sven", "gone", StDropped))

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
		return w.create("sven", Item{ID: id, Kind: KindNote, Template: "trade_setup", Fields: fields})
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
	if err := w.create("sven", Item{ID: "x", Kind: KindNote, Template: "nope"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown template: %v", err)
	}
	if err := w.create("sven", Item{ID: "y", Kind: KindProject, Template: "trade_setup"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("template of another kind: %v", err)
	}

	// A tickler date field sets defer_until, on create and on patch.
	w.must(note("tick", map[string]string{"expires": "2026-12-19"}))
	if got := w.st.Items["tick"].DeferUntil; got != "2026-12-19" {
		t.Errorf("defer_until after create = %q", got)
	}
	w.must(w.patch("sven", "tick", "fields", map[string]string{"expires": "2027-01-15"}))
	if got := w.st.Items["tick"].DeferUntil; got != "2027-01-15" {
		t.Errorf("defer_until after patch = %q", got)
	}
	last := w.log[len(w.log)-1]
	if len(last.Changes) != 2 || last.Changes[1].Field != "defer_until" || string(last.Changes[1].Old) != `"2026-12-19"` {
		t.Errorf("the patch op should record the defer_until change: %+v", last.Changes)
	}
	w.must(w.patch("sven", "tick", "title", "renamed")) // unrelated patch: defer_until untouched
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
	if got, want := Contexts(w.st, "sven"), []string{"errands", "calls", "computer", "home", "office", "anywhere", "agenda:eric", "agenda:Kim"}; !slices.Equal(got, want) {
		t.Errorf("shared contexts = %v", got)
	}
	p := personal(t)
	if got, want := Contexts(p.st, "sven"), []string{"errands", "calls", "computer", "home", "office", "anywhere", "boat"}; !slices.Equal(got, want) {
		t.Errorf("personal contexts = %v", got)
	}
	if got := Transitions(&Item{Kind: KindArea, State: StActive}); !slices.Equal(got, []string{StRetired}) {
		t.Errorf("transitions = %v", got)
	}
}
