package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// world is one space with committed state and its op log.
type world struct {
	t     *testing.T
	st    *State
	n     int
	log   []Op
	notes []Notification
	cms   map[string]*Comment
}

func shared(t *testing.T) *world {
	return &world{t: t, cms: map[string]*Comment{}, st: &State{
		Space: &Space{ID: "net", Kind: SpaceShared, Members: []string{"robin", "dana"}, People: []string{"Kim"}},
		Items: map[string]*Item{},
	}}
}

func personal(t *testing.T) *world {
	return &world{t: t, cms: map[string]*Comment{}, st: &State{
		Space:            &Space{ID: "robin", Kind: SpacePersonal, Members: []string{"robin"}},
		Items:            map[string]*Item{},
		PersonalContexts: []string{"boat"},
	}}
}

func (w *world) id() string { w.n++; return fmt.Sprintf("x%03d", w.n) }

// apply runs one op and commits its result.
func (w *world) apply(op Op) (*Result, error) {
	op.ID = w.id()
	op.Space = w.st.Space.ID
	op.TS = time.Date(2026, 11, 19, 8, 0, w.n, 0, time.UTC)
	if op.Via == "" {
		op.Via = "cli"
	}
	res, err := Apply(w.st, op, w.id)
	if err != nil {
		return nil, err
	}
	for _, it := range res.Items {
		w.st.Items[it.ID] = it
	}
	for _, c := range res.Comments {
		w.cms[c.ID] = c
	}
	w.log = append(w.log, res.Ops...)
	w.notes = append(w.notes, res.Notifications...)
	return res, nil
}

func (w *world) create(actor string, it Item) error {
	if it.Title == "" {
		it.Title = "title of " + it.ID
	}
	_, err := w.apply(Op{Actor: actor, Type: OpCreate, Payload: mustJSON(it)})
	return err
}

func (w *world) must(err error) {
	w.t.Helper()
	if err != nil {
		w.t.Fatal(err)
	}
}

// action creates an action owned by owner with the given blockers.
func (w *world) action(id, owner string, blockedBy ...string) {
	w.t.Helper()
	w.must(w.create("robin", Item{ID: id, Kind: KindAction, Owner: owner, BlockedBy: blockedBy}))
}

func (w *world) move(actor, id, state string) error {
	_, err := w.apply(Op{Actor: actor, Type: OpTransition, Item: id, BaseRev: w.st.Items[id].Rev,
		Changes: []FieldChange{{Field: "state", New: mustJSON(state)}}})
	return err
}

func (w *world) patch(actor, id, field string, value any) error {
	_, err := w.apply(Op{Actor: actor, Type: OpPatch, Item: id, BaseRev: w.st.Items[id].Rev,
		Changes: []FieldChange{{Field: field, New: mustJSON(value)}}})
	return err
}

func (w *world) state(id string) string { return w.st.Items[id].State }

// noteList renders notifications as "type:item>user".
func (w *world) noteList() []string {
	var out []string
	for _, n := range w.notes {
		out = append(out, n.Type+":"+n.Item+">"+n.User)
	}
	return out
}

func TestTransitionTable(t *testing.T) {
	// Every allowed user transition, written out. Every other pair must be rejected.
	allowed := map[string]string{
		KindCapture: "inbox>trashed trashed>inbox",
		KindAction: "next>waiting next>someday next>done next>dropped next>trashed " +
			"waiting>next waiting>someday waiting>done waiting>dropped waiting>trashed " +
			"someday>next someday>waiting someday>done someday>dropped someday>trashed " +
			"planned>done planned>dropped planned>trashed " +
			"done>next dropped>next trashed>next",
		KindProject: "active>someday active>done active>dropped active>trashed " +
			"someday>active someday>done someday>dropped someday>trashed " +
			"done>active dropped>active trashed>active",
		KindNote: "unsorted>filed unsorted>archived unsorted>trashed " +
			"filed>unsorted filed>archived filed>trashed " +
			"archived>unsorted archived>filed archived>trashed trashed>unsorted",
		KindArea: "active>retired retired>active",
	}
	states := map[string][]string{
		KindCapture: {StInbox, StTrashed},
		KindAction:  {StNext, StPlanned, StWaiting, StSomeday, StDone, StDropped, StTrashed},
		KindProject: {StActive, StSomeday, StDone, StDropped, StTrashed},
		KindNote:    {StUnsorted, StFiled, StArchived, StTrashed},
		KindArea:    {StActive, StRetired},
	}
	for kind, all := range states {
		ok := strings.Fields(allowed[kind])
		for _, from := range all {
			for _, to := range append(slices.Clone(all), "bogus") {
				t.Run(kind+"/"+from+">"+to, func(t *testing.T) {
					w := shared(t)
					// Put the item into `from` directly: not every state is reachable by create.
					w.st.Items["i"] = &Item{V: 1, ID: "i", Space: "net", Kind: kind, Title: "t", State: from, Rev: 1}
					err := w.move("robin", "i", to)
					want := slices.Contains(ok, from+">"+to)
					if want && err != nil {
						t.Fatalf("rejected: %v", err)
					}
					if !want && err == nil {
						t.Fatalf("allowed, state is now %s", w.state("i"))
					}
					if want && w.state("i") != to {
						t.Fatalf("state = %s", w.state("i"))
					}
				})
			}
		}
	}
}

func TestClarifyCapture(t *testing.T) {
	// A capture in the inbox may become these kind/state pairs and nothing else.
	allowed := "action/next action/waiting action/someday project/active project/someday note/unsorted note/filed"
	for _, kind := range []string{KindCapture, KindAction, KindProject, KindNote, KindArea} {
		for _, state := range []string{StInbox, StNext, StPlanned, StWaiting, StSomeday, StDone, StActive, StUnsorted, StFiled, StArchived, StTrashed} {
			if kind == KindCapture {
				continue // same kind: covered by TestTransitionTable
			}
			t.Run(kind+"/"+state, func(t *testing.T) {
				w := personal(t)
				w.must(w.create("robin", Item{ID: "c", Kind: KindCapture, Original: "raw"}))
				_, err := w.apply(Op{Actor: "robin", Type: OpTransition, Item: "c", BaseRev: 1, Changes: []FieldChange{
					{Field: "kind", New: mustJSON(kind)}, {Field: "state", New: mustJSON(state)}}})
				want := slices.Contains(strings.Fields(allowed), kind+"/"+state)
				if want != (err == nil) {
					t.Fatalf("allowed = %v, err = %v", err == nil, err)
				}
				if err != nil {
					return
				}
				it := w.st.Items["c"]
				if it.Kind != kind || it.State != state || it.Original != "raw" {
					t.Fatalf("got %s/%s original %q", it.Kind, it.State, it.Original)
				}
				if kind == KindAction && it.Owner != "robin" {
					t.Fatalf("action in a personal space has owner %q", it.Owner)
				}
			})
		}
	}
	t.Run("only from inbox", func(t *testing.T) {
		w := personal(t)
		w.must(w.create("robin", Item{ID: "c", Kind: KindCapture}))
		w.must(w.move("robin", "c", StTrashed))
		_, err := w.apply(Op{Actor: "robin", Type: OpTransition, Item: "c", BaseRev: 2, Changes: []FieldChange{
			{Field: "kind", New: mustJSON(KindAction)}, {Field: "state", New: mustJSON(StNext)}}})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no other kind changes", func(t *testing.T) {
		w := shared(t)
		w.action("a", "robin")
		_, err := w.apply(Op{Actor: "robin", Type: OpTransition, Item: "a", BaseRev: 1, Changes: []FieldChange{
			{Field: "kind", New: mustJSON(KindNote)}, {Field: "state", New: mustJSON(StFiled)}}})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDependencies(t *testing.T) {
	cases := []struct {
		name  string
		run   func(w *world) error
		err   error             // expected error of run
		state map[string]string // expected states afterwards
		notes []string          // expected notifications
	}{
		{name: "created with an open blocker is planned",
			run:   func(w *world) error { w.action("a", "robin"); w.action("b", "dana", "a"); return nil },
			state: map[string]string{"a": StNext, "b": StPlanned}},
		{name: "created as planned without a blocker becomes next",
			run: func(w *world) error {
				return w.create("robin", Item{ID: "a", Kind: KindAction, State: StPlanned})
			},
			state: map[string]string{"a": StNext}},
		{name: "created with a finished blocker stays next",
			run: func(w *world) error {
				w.action("a", "robin")
				w.must(w.move("robin", "a", StDone))
				w.action("b", "dana", "a")
				return nil
			},
			state: map[string]string{"b": StNext}},
		{name: "adding a blocker makes a next action planned",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "dana")
				return w.patch("robin", "b", "blocked_by", []string{"a"})
			},
			state: map[string]string{"b": StPlanned}},
		{name: "adding a blocker makes waiting and someday actions planned",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "dana")
				w.action("c", "dana")
				w.must(w.move("dana", "b", StWaiting))
				w.must(w.move("dana", "c", StSomeday))
				w.must(w.patch("robin", "b", "blocked_by", []string{"a"}))
				return w.patch("robin", "c", "blocked_by", []string{"a"})
			},
			state: map[string]string{"b": StPlanned, "c": StPlanned}},
		{name: "removing the last blocker makes it next, without notification",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "dana", "a")
				return w.patch("robin", "b", "blocked_by", []string{})
			},
			state: map[string]string{"b": StNext}},
		{name: "unblock: last blocker done",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "dana", "a")
				return w.move("robin", "a", StDone)
			},
			state: map[string]string{"a": StDone, "b": StNext},
			notes: []string{"unblocked:b>dana"}},
		{name: "unblock: owner is notified even if they completed the blocker",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "robin", "a")
				return w.move("robin", "a", StDone)
			},
			state: map[string]string{"b": StNext},
			notes: []string{"unblocked:b>robin"}},
		{name: "unblock: unassigned action notifies every member",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "", "a")
				return w.move("robin", "a", StDone)
			},
			state: map[string]string{"b": StNext},
			notes: []string{"unblocked:b>dana", "unblocked:b>robin"}},
		{name: "one of two blockers done: stays planned",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "robin")
				w.action("c", "dana", "a", "b")
				return w.move("robin", "a", StDone)
			},
			state: map[string]string{"c": StPlanned}},
		{name: "both blockers done: unblocked once",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "robin")
				w.action("c", "dana", "a", "b")
				w.must(w.move("robin", "a", StDone))
				return w.move("robin", "b", StDone)
			},
			state: map[string]string{"c": StNext},
			notes: []string{"unblocked:c>dana"}},
		{name: "one blocker unblocks several dependents",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "dana", "a")
				w.action("c", "robin", "a")
				return w.move("robin", "a", StDone)
			},
			state: map[string]string{"b": StNext, "c": StNext},
			notes: []string{"unblocked:b>dana", "unblocked:c>robin"}},
		{name: "chain: only the direct dependent is unblocked",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "dana", "a")
				w.action("c", "dana", "b")
				return w.move("robin", "a", StDone)
			},
			state: map[string]string{"b": StNext, "c": StPlanned},
			notes: []string{"unblocked:b>dana"}},
		{name: "drop: counts as completed, plus a warning",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "dana", "a")
				return w.move("robin", "a", StDropped)
			},
			state: map[string]string{"a": StDropped, "b": StNext},
			notes: []string{"unblocked:b>dana", "blocker_dropped:b>dana"}},
		{name: "drop one of two blockers: still planned, warning only",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "robin")
				w.action("c", "dana", "a", "b")
				return w.move("robin", "a", StDropped)
			},
			state: map[string]string{"c": StPlanned},
			notes: []string{"blocker_dropped:c>dana"}},
		{name: "trash: counts as completed, no warning",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "dana", "a")
				return w.move("robin", "a", StTrashed)
			},
			state: map[string]string{"b": StNext},
			notes: []string{"unblocked:b>dana"}},
		{name: "reopen a done blocker: open dependent goes back to planned",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "dana", "a")
				w.must(w.move("robin", "a", StDone))
				return w.move("robin", "a", StNext)
			},
			state: map[string]string{"a": StNext, "b": StPlanned},
			notes: []string{"unblocked:b>dana"}},
		{name: "reopen a dropped blocker: dependent goes back to planned",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "dana", "a")
				w.must(w.move("robin", "a", StDropped))
				return w.move("robin", "a", StNext)
			},
			state: map[string]string{"b": StPlanned},
			notes: []string{"unblocked:b>dana", "blocker_dropped:b>dana"}},
		{name: "reopen: a dependent that is done stays done",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "dana", "a")
				w.must(w.move("robin", "a", StDone))
				w.must(w.move("dana", "b", StDone))
				return w.move("robin", "a", StNext)
			},
			state: map[string]string{"a": StNext, "b": StDone},
			notes: []string{"unblocked:b>dana"}},
		{name: "reopening a dependent with an open blocker lands in planned",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "dana", "a")
				w.must(w.move("dana", "b", StDropped))
				return w.move("dana", "b", StNext)
			},
			state: map[string]string{"b": StPlanned}},
		{name: "a planned action can be completed directly",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "dana", "a")
				return w.move("dana", "b", StDone)
			},
			state: map[string]string{"a": StNext, "b": StDone}},
		{name: "a user cannot set planned",
			run: func(w *world) error { w.action("a", "robin"); return w.move("robin", "a", StPlanned) },
			err: ErrConflict},
		{name: "a user cannot move a planned action to next",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "dana", "a")
				return w.move("dana", "b", StNext)
			},
			err: ErrConflict},
		{name: "cycle: self",
			run: func(w *world) error {
				w.action("a", "robin")
				return w.patch("robin", "a", "blocked_by", []string{"a"})
			},
			err: ErrInvalid},
		{name: "cycle: two actions",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "robin", "a")
				return w.patch("robin", "a", "blocked_by", []string{"b"})
			},
			err: ErrInvalid},
		{name: "cycle: three actions",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "robin", "a")
				w.action("c", "robin", "b")
				return w.patch("robin", "a", "blocked_by", []string{"c"})
			},
			err: ErrInvalid},
		{name: "cycle: through a finished action is still a cycle",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "robin", "a")
				w.must(w.move("robin", "a", StDone))
				return w.patch("robin", "a", "blocked_by", []string{"b"})
			},
			err: ErrInvalid, notes: []string{"unblocked:b>robin"}},
		{name: "diamond is not a cycle",
			run: func(w *world) error {
				w.action("a", "robin")
				w.action("b", "robin", "a")
				w.action("c", "robin", "a")
				w.action("d", "robin", "b", "c")
				return nil
			},
			state: map[string]string{"d": StPlanned}},
		{name: "blocker must exist in the same space",
			run: func(w *world) error {
				return w.create("robin", Item{ID: "b", Kind: KindAction, BlockedBy: []string{"elsewhere"}})
			},
			err: ErrInvalid},
		{name: "blocker must be an action",
			run: func(w *world) error {
				w.must(w.create("robin", Item{ID: "p", Kind: KindProject}))
				return w.create("robin", Item{ID: "b", Kind: KindAction, BlockedBy: []string{"p"}})
			},
			err: ErrInvalid},
		{name: "blocker listed twice",
			run: func(w *world) error {
				w.action("a", "robin")
				return w.create("robin", Item{ID: "b", Kind: KindAction, BlockedBy: []string{"a", "a"}})
			},
			err: ErrInvalid},
		{name: "only actions have blockers",
			run: func(w *world) error {
				w.action("a", "robin")
				return w.create("robin", Item{ID: "n", Kind: KindNote, BlockedBy: []string{"a"}})
			},
			err: ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := shared(t)
			err := c.run(w)
			if !errors.Is(err, c.err) || (c.err == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			for id, want := range c.state {
				if got := w.state(id); got != want {
					t.Errorf("%s is %s, want %s", id, got, want)
				}
			}
			if got := w.noteList(); !slices.Equal(got, c.notes) {
				t.Errorf("notifications = %v, want %v", got, c.notes)
			}
			checkReplay(t, w)
		})
	}
}

// checkReplay verifies that the op log alone reproduces the committed state.
func checkReplay(t *testing.T, w *world) {
	t.Helper()
	items, cms := map[string]*Item{}, map[string]*Comment{}
	for _, op := range w.log {
		if err := Replay(items, cms, op); err != nil {
			t.Fatalf("replay: %v", err)
		}
	}
	if got, want := string(mustJSON(items)), string(mustJSON(w.st.Items)); got != want {
		t.Fatalf("replayed items differ\n got %s\nwant %s", got, want)
	}
	if got, want := string(mustJSON(cms)), string(mustJSON(w.cms)); got != want {
		t.Fatalf("replayed comments differ\n got %s\nwant %s", got, want)
	}
}

func TestDerivedOps(t *testing.T) {
	w := shared(t)
	w.action("a", "robin")
	w.action("b", "dana", "a")
	res, err := w.apply(Op{Actor: "robin", Type: OpTransition, Item: "a", BaseRev: 1,
		Changes: []FieldChange{{Field: "state", New: mustJSON(StDone)}}})
	w.must(err)
	if len(res.Ops) != 2 || len(res.Items) != 2 {
		t.Fatalf("ops = %d, items = %d, want 2 and 2", len(res.Ops), len(res.Items))
	}
	d := res.Ops[1]
	if d.Actor != System || d.Via != System || d.Type != OpTransition || d.Item != "b" || d.ID == res.Ops[0].ID {
		t.Fatalf("derived op = %+v", d)
	}
	if string(d.Changes[0].Old) != `"planned"` || string(d.Changes[0].New) != `"next"` {
		t.Fatalf("derived changes = %s → %s", d.Changes[0].Old, d.Changes[0].New)
	}
	b := w.st.Items["b"]
	if b.UpdatedBy != System || b.Rev != 3 {
		t.Fatalf("b: updated_by %s rev %d", b.UpdatedBy, b.Rev)
	}
}

func TestApplyIsPure(t *testing.T) {
	w := shared(t)
	w.action("a", "robin")
	w.action("b", "dana", "a")
	before := string(mustJSON(w.st))
	op := Op{ID: "o", Space: "net", TS: time.Now(), Actor: "robin", Via: "cli", Type: OpTransition, Item: "a", BaseRev: 1,
		Changes: []FieldChange{{Field: "state", New: mustJSON(StDone)}}}
	if _, err := Apply(w.st, op, w.id); err != nil {
		t.Fatal(err)
	}
	if after := string(mustJSON(w.st)); after != before {
		t.Fatalf("Apply modified its input state\nbefore %s\nafter  %s", before, after)
	}
}

func TestPendingOverridesItems(t *testing.T) {
	// Two ops of one batch: the second sees the first through Pending.
	w := shared(t)
	ts := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	st := &State{Space: w.st.Space, Items: map[string]*Item{}, Pending: map[string]*Item{}}
	r1, err := Apply(st, Op{ID: "o1", Space: "net", TS: ts, Actor: "robin", Via: "cli", Type: OpCreate,
		Payload: mustJSON(Item{ID: "a", Kind: KindAction, Title: "first"})}, w.id)
	w.must(err)
	st.Pending["a"] = r1.Items[0]
	r2, err := Apply(st, Op{ID: "o2", Space: "net", TS: ts, Actor: "robin", Via: "cli", Type: OpCreate,
		Payload: mustJSON(Item{ID: "b", Kind: KindAction, Title: "second", BlockedBy: []string{"a"}})}, w.id)
	w.must(err)
	if got := r2.Items[0].State; got != StPlanned {
		t.Fatalf("b is %s, want planned", got)
	}
}

func TestValidation(t *testing.T) {
	type tc struct {
		name string
		run  func(w *world) error
		err  error
	}
	item := func(it Item) func(w *world) error {
		return func(w *world) error { return w.create("robin", it) }
	}
	setup := func(w *world) {
		w.must(w.create("robin", Item{ID: "area", Kind: KindArea}))
		w.must(w.create("robin", Item{ID: "p1", Kind: KindProject}))
		w.must(w.create("robin", Item{ID: "p2", Kind: KindProject, Parent: "p1"}))
		w.must(w.create("robin", Item{ID: "p3", Kind: KindProject, Parent: "p2"}))
		w.action("act", "robin")
	}
	for _, c := range []tc{
		{"full action", item(Item{ID: "n", Kind: KindAction, Area: "area", Project: "p3", Context: "calls", Owner: "dana",
			DelegatedBy: "robin", Target: "2027-01-15", Due: "2027-02-01", DeferUntil: "2026-12-03", Energy: "low", Time: "2h+"}), nil},
		{"unassigned action in a shared space", item(Item{ID: "n", Kind: KindAction}), nil},
		{"agenda context for a member", item(Item{ID: "n", Kind: KindAction, Context: "agenda:dana"}), nil},
		{"agenda context for a person", item(Item{ID: "n", Kind: KindAction, Context: "agenda:Kim"}), nil},
		{"agenda context for a stranger", item(Item{ID: "n", Kind: KindAction, Context: "agenda:nobody"}), ErrInvalid},
		{"personal context in a shared space", item(Item{ID: "n", Kind: KindAction, Context: "boat"}), ErrInvalid},
		{"unknown context", item(Item{ID: "n", Kind: KindAction, Context: "moon"}), ErrInvalid},
		{"context on a note", item(Item{ID: "n", Kind: KindNote, Context: "calls"}), ErrInvalid},
		{"unknown kind", item(Item{ID: "n", Kind: "idea"}), ErrInvalid},
		{"state of another kind", item(Item{ID: "n", Kind: KindNote, State: StNext}), ErrInvalid},
		{"created as done", item(Item{ID: "n", Kind: KindAction, State: StDone}), ErrInvalid},
		{"capture outside the inbox", item(Item{ID: "n", Kind: KindCapture, State: StTrashed}), ErrInvalid},
		{"duplicate id", item(Item{ID: "act", Kind: KindAction}), ErrConflict},
		{"no id", item(Item{Kind: KindAction, Title: "x"}), ErrInvalid},
		{"area is not an area", item(Item{ID: "n", Kind: KindAction, Area: "p1"}), ErrInvalid},
		{"area on an area", item(Item{ID: "n", Kind: KindArea, Area: "area"}), ErrInvalid},
		{"project is not a project", item(Item{ID: "n", Kind: KindNote, Project: "act"}), ErrInvalid},
		{"project on a project", item(Item{ID: "n", Kind: KindProject, Project: "p1"}), ErrInvalid},
		{"parent on an action", item(Item{ID: "n", Kind: KindAction, Parent: "p1"}), ErrInvalid},
		{"parent missing", item(Item{ID: "n", Kind: KindProject, Parent: "nope"}), ErrInvalid},
		{"owner is not a member", item(Item{ID: "n", Kind: KindAction, Owner: "jo"}), ErrInvalid},
		{"delegated_by is not a member", item(Item{ID: "n", Kind: KindAction, DelegatedBy: "jo"}), ErrInvalid},
		{"bad date", item(Item{ID: "n", Kind: KindAction, Target: "15.01.2027"}), ErrInvalid},
		{"impossible date", item(Item{ID: "n", Kind: KindAction, Due: "2027-02-30"}), ErrInvalid},
		{"bad energy", item(Item{ID: "n", Kind: KindAction, Energy: "huge"}), ErrInvalid},
		{"bad time", item(Item{ID: "n", Kind: KindAction, Time: "3h"}), ErrInvalid},
		{"bad attachment hash", item(Item{ID: "n", Kind: KindNote, Attachments: []Attachment{{SHA256: "abc"}}}), ErrInvalid},
		{"empty title", func(w *world) error {
			_, err := w.apply(Op{Actor: "robin", Type: OpCreate, Payload: mustJSON(Item{ID: "n", Kind: KindNote})})
			return err
		}, ErrInvalid},
		{"unknown field in payload", func(w *world) error {
			_, err := w.apply(Op{Actor: "robin", Type: OpCreate, Payload: json.RawMessage(`{"id":"n","kind":"note","title":"t","tags":["a"]}`)})
			return err
		}, ErrInvalid},
		{"parent cycle: self", func(w *world) error { return w.patch("robin", "p1", "parent", "p1") }, ErrInvalid},
		{"parent cycle: through grandchild", func(w *world) error { return w.patch("robin", "p1", "parent", "p3") }, ErrInvalid},
		{"reparent without cycle", func(w *world) error { return w.patch("robin", "p3", "parent", "p1") }, nil},
		{"patch state", func(w *world) error { return w.patch("robin", "act", "state", StDone) }, ErrInvalid},
		{"patch kind", func(w *world) error { return w.patch("robin", "act", "kind", KindNote) }, ErrInvalid},
		{"patch original", func(w *world) error { return w.patch("robin", "act", "original", "rewritten") }, ErrInvalid},
		{"patch id", func(w *world) error { return w.patch("robin", "act", "id", "other") }, ErrInvalid},
		{"patch space", func(w *world) error { return w.patch("robin", "act", "space", "other") }, ErrInvalid},
		{"patch created", func(w *world) error { return w.patch("robin", "act", "created", "2020-01-01T00:00:00Z") }, ErrInvalid},
		{"patch rev", func(w *world) error { return w.patch("robin", "act", "rev", 99) }, ErrInvalid},
		{"patch with a wrong type", func(w *world) error { return w.patch("robin", "act", "blocked_by", "act") }, ErrInvalid},
		{"patch a missing item", func(w *world) error {
			_, err := w.apply(Op{Actor: "robin", Type: OpPatch, Item: "nope", Changes: []FieldChange{{Field: "title", New: mustJSON("x")}}})
			return err
		}, ErrNotFound},
		{"patch without changes", func(w *world) error {
			_, err := w.apply(Op{Actor: "robin", Type: OpPatch, Item: "act", BaseRev: 1})
			return err
		}, ErrInvalid},
		{"transition with another field", func(w *world) error {
			_, err := w.apply(Op{Actor: "robin", Type: OpTransition, Item: "act", BaseRev: 1, Changes: []FieldChange{
				{Field: "state", New: mustJSON(StDone)}, {Field: "title", New: mustJSON("x")}}})
			return err
		}, ErrInvalid},
		{"unknown op type", func(w *world) error {
			_, err := w.apply(Op{Actor: "robin", Type: "move", Item: "act"})
			return err
		}, ErrInvalid},
		{"non-member", func(w *world) error {
			return w.create("jo", Item{ID: "n", Kind: KindNote})
		}, ErrNotFound},
		{"system may act", func(w *world) error { return w.move(System, "act", StDone) }, nil},
		{"clear a field", func(w *world) error {
			w.must(w.patch("robin", "act", "target", "2027-01-01"))
			w.must(w.patch("robin", "act", "target", ""))
			if got := w.st.Items["act"].Target; got != "" {
				return fmt.Errorf("target = %q", got)
			}
			return nil
		}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := shared(t)
			setup(w)
			err := c.run(w)
			if !errors.Is(err, c.err) || (c.err == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			checkReplay(t, w)
		})
	}
}

func TestPersonalSpace(t *testing.T) {
	w := personal(t)
	w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Context: "boat"}))
	if got := w.st.Items["a"].Owner; got != "robin" {
		t.Fatalf("owner = %q, want the space's user", got)
	}
	if err := w.patch("robin", "a", "owner", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("clearing the owner: %v", err)
	}
	if err := w.create("robin", Item{ID: "n", Kind: KindNote}); err != nil {
		t.Fatal(err)
	}
	if got := w.st.Items["n"].Owner; got != "" {
		t.Fatalf("note owner = %q", got)
	}
	checkReplay(t, w)
}

func TestImmutableAndBookkeeping(t *testing.T) {
	w := shared(t)
	w.must(w.create("robin", Item{ID: "c", Kind: KindNote, Original: "the original text", Rev: 40, UpdatedBy: "mallory", Space: "other"}))
	it := w.st.Items["c"]
	if it.Rev != 1 || it.UpdatedBy != "robin" || it.Space != "net" || it.Created.IsZero() || !it.Created.Equal(it.Updated) {
		t.Fatalf("create bookkeeping: %+v", it)
	}
	w.must(w.patch("dana", "c", "title", "better title"))
	w.must(w.patch("dana", "c", "body", "cleaned up"))
	got := w.st.Items["c"]
	if got.Original != "the original text" || got.Rev != 3 || got.UpdatedBy != "dana" || !got.Created.Equal(it.Created) || !got.Updated.After(it.Updated) {
		t.Fatalf("after patches: %+v", got)
	}
}

func TestBaseRev(t *testing.T) {
	change := func(field string, old, nw any) FieldChange {
		return FieldChange{Field: field, Old: mustJSON(old), New: mustJSON(nw)}
	}
	for _, c := range []struct {
		name    string
		baseRev uint64
		change  FieldChange
		err     error
	}{
		{"current rev", 2, change("title", "ignored", "new"), nil},
		{"stale rev, field untouched since", 1, change("body", "", "text"), nil},
		{"stale rev, field untouched, old given as null", 1, FieldChange{Field: "body", Old: json.RawMessage("null"), New: mustJSON("text")}, nil},
		{"stale rev, same field changed meanwhile", 1, change("title", "first", "mine"), ErrConflict},
		{"stale rev, old matches current", 1, change("title", "theirs", "mine"), nil},
		{"no rev and no old", 0, FieldChange{Field: "title", New: mustJSON("mine")}, ErrConflict},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := shared(t)
			w.must(w.create("robin", Item{ID: "n", Kind: KindNote, Title: "first"}))
			w.must(w.patch("dana", "n", "title", "theirs")) // rev 2
			res, err := w.apply(Op{Actor: "robin", Type: OpPatch, Item: "n", BaseRev: c.baseRev, Changes: []FieldChange{c.change}})
			if !errors.Is(err, c.err) || (c.err == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			if err == nil && c.change.Field == "title" {
				if old := string(res.Ops[0].Changes[0].Old); old != `"theirs"` {
					t.Fatalf("logged old = %s, want the actual previous value", old)
				}
			}
		})
	}
}

func TestComments(t *testing.T) {
	w := shared(t)
	w.action("a", "robin")
	comment := func(actor string, c Comment) (*Result, error) {
		return w.apply(Op{Actor: actor, Type: OpComment, Item: "a", Payload: mustJSON(c)})
	}
	res, err := comment("dana", Comment{Body: "see @[user:robin]", Mentions: []string{"robin"}, Author: "mallory"})
	w.must(err)
	c := res.Comments[0]
	if c.Author != "dana" || c.Item != "a" || c.ID == "" || c.TS.IsZero() {
		t.Fatalf("comment = %+v", c)
	}
	if len(res.Items) != 0 || w.st.Items["a"].Rev != 1 {
		t.Fatal("a comment must not change the item")
	}
	if _, err := comment("dana", Comment{Body: "  "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty comment: %v", err)
	}
	if _, err := comment("dana", Comment{Body: "hi", Mentions: []string{"jo"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mention of a non-member: %v", err)
	}
	if _, err := w.apply(Op{Actor: "dana", Type: OpComment, Item: "nope", Payload: mustJSON(Comment{Body: "x"})}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("comment on a missing item: %v", err)
	}

	del := func(actor string, target *Comment, id string) error {
		w.st.Comment = target
		defer func() { w.st.Comment = nil }()
		_, err := w.apply(Op{Actor: actor, Type: OpDeleteComment, Item: "a", Payload: mustJSON(map[string]string{"comment": id})})
		return err
	}
	if err := del("robin", c, c.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("delete by someone else: %v", err)
	}
	if err := del("dana", nil, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete of a missing comment: %v", err)
	}
	w.must(del("dana", c, c.ID))
	tomb := w.cms[c.ID]
	if !tomb.Deleted || tomb.Body != "" || tomb.Mentions != nil || tomb.Author != "dana" || !tomb.TS.Equal(c.TS) {
		t.Fatalf("tombstone = %+v", tomb)
	}
	if err := del("dana", tomb, c.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("second delete: %v", err)
	}
	checkReplay(t, w)
}

func TestAttach(t *testing.T) {
	w := shared(t)
	w.must(w.create("robin", Item{ID: "n", Kind: KindNote}))
	sha := strings.Repeat("ab", 32)
	attach := func(a Attachment) error {
		_, err := w.apply(Op{Actor: "dana", Type: OpAttach, Item: "n", Payload: mustJSON(a)})
		return err
	}
	w.must(attach(Attachment{SHA256: sha, Name: "board.png", MIME: "image/png", Size: 12}))
	it := w.st.Items["n"]
	if len(it.Attachments) != 1 || it.Rev != 2 || it.UpdatedBy != "dana" {
		t.Fatalf("after attach: %+v", it)
	}
	if err := attach(Attachment{SHA256: sha, Name: "again.png"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("same blob twice: %v", err)
	}
	if err := attach(Attachment{SHA256: "zz", Name: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad hash: %v", err)
	}
	checkReplay(t, w)
}

func TestAlive(t *testing.T) {
	// p has child project c. Each case adds actions and says whether p is alive.
	for _, c := range []struct {
		name  string
		setup func(w *world)
		alive bool
	}{
		{"no actions", func(w *world) {}, false},
		{"next action", func(w *world) { w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Project: "p"})) }, true},
		{"waiting action", func(w *world) {
			w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Project: "p", State: StWaiting, WaitingOn: "Kim"}))
		}, true},
		{"only a someday action", func(w *world) {
			w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Project: "p", State: StSomeday}))
		}, false},
		{"only a done action", func(w *world) {
			w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Project: "p"}))
			w.must(w.move("robin", "a", StDone))
		}, false},
		{"next action in a child project", func(w *world) {
			w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Project: "c"}))
		}, true},
		{"next action in a dropped child project", func(w *world) {
			w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Project: "c"}))
			w.must(w.move("robin", "c", StDropped))
		}, false},
		{"next action in another project", func(w *world) {
			w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Project: "other"}))
		}, false},
		{"planned, blocker owned by someone else", func(w *world) {
			w.must(w.create("robin", Item{ID: "x", Kind: KindAction, Project: "other", Owner: "dana", State: StSomeday}))
			w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Project: "p", Owner: "robin", BlockedBy: []string{"x"}}))
		}, true},
		{"planned, blocker owned by the same user", func(w *world) {
			w.must(w.create("robin", Item{ID: "x", Kind: KindAction, Project: "other", Owner: "robin", State: StSomeday}))
			w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Project: "p", Owner: "robin", BlockedBy: []string{"x"}}))
		}, false},
		{"planned and deferred", func(w *world) {
			w.must(w.create("robin", Item{ID: "x", Kind: KindAction, Project: "other", Owner: "robin", State: StSomeday}))
			w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Project: "p", Owner: "robin", BlockedBy: []string{"x"}, DeferUntil: "2026-12-24"}))
		}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := shared(t)
			w.must(w.create("robin", Item{ID: "p", Kind: KindProject}))
			w.must(w.create("robin", Item{ID: "c", Kind: KindProject, Parent: "p"}))
			w.must(w.create("robin", Item{ID: "other", Kind: KindProject}))
			c.setup(w)
			if got := Alive(w.st, "p"); got != c.alive {
				t.Fatalf("alive = %v, want %v", got, c.alive)
			}
		})
	}
}

func TestWarnings(t *testing.T) {
	w := shared(t)
	w.must(w.create("robin", Item{ID: "p", Kind: KindProject, Target: "2027-01-31"}))
	w.must(w.create("robin", Item{ID: "late", Kind: KindProject, Parent: "p", Target: "2027-02-15"}))
	w.must(w.create("robin", Item{ID: "ok", Kind: KindProject, Parent: "p", Target: "2027-01-31"}))
	w.must(w.create("robin", Item{ID: "nodate", Kind: KindProject, Parent: "p"}))
	w.must(w.create("robin", Item{ID: "b1", Kind: KindAction, Target: "2027-01-20"}))
	w.must(w.create("robin", Item{ID: "b2", Kind: KindAction, Target: "2027-01-05"}))
	w.must(w.create("robin", Item{ID: "dep", Kind: KindAction, Target: "2027-01-10", BlockedBy: []string{"b1", "b2"}}))
	w.must(w.create("robin", Item{ID: "old", Kind: KindAction, Target: "2026-11-01"}))
	w.must(w.create("robin", Item{ID: "olddone", Kind: KindAction, Target: "2026-11-01"}))
	w.must(w.move("robin", "olddone", StDone))
	w.must(w.create("robin", Item{ID: "today", Kind: KindAction, Target: "2026-11-19"}))
	w.must(w.create("robin", Item{ID: "note", Kind: KindNote, Target: "2026-01-01"}))

	got := Warnings(w.st, "2026-11-19")
	want := []Warning{
		{WarnTargetBeforeBlocker, "dep", "b1"},
		{WarnChildAfterParent, "late", "p"},
		{WarnSlipping, "old", ""},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("warnings = %v\nwant %v", got, want)
	}
	// A finished blocker no longer warns.
	w.must(w.move("robin", "b1", StDone))
	if got := Warnings(w.st, "2026-11-19"); slices.Contains(got, want[0]) {
		t.Fatalf("finished blocker still warns: %v", got)
	}
}

func TestPermissions(t *testing.T) {
	sp := &Space{ID: "net", Members: []string{"robin", "dana"}}
	for user, want := range map[string]bool{"robin": true, "dana": true, "jo": false, "": false, System: false} {
		if got := sp.HasMember(user); got != want {
			t.Errorf("HasMember(%q) = %v", user, got)
		}
	}
	for user, want := range map[string]bool{"robin": true, "jo": false, System: true, "": false} {
		if got := CanAct(sp, user); got != want {
			t.Errorf("CanAct(%q) = %v", user, got)
		}
	}
	for id, want := range map[string]bool{"robin": true, "robin-jo": true, "n1s": true, "": false, "-a": false,
		"Robin": false, "a/b": false, "a b": false, "über": false, strings.Repeat("a", 65): false} {
		if got := ValidID(id); got != want {
			t.Errorf("ValidID(%q) = %v", id, got)
		}
	}
	if !Reserved("system") || !Reserved("ai") || Reserved("robin") {
		t.Error("Reserved")
	}
}
