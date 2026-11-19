package core

import (
	"slices"
	"testing"
)

// TestDelegation: owner, delegated_by and the notifications, case by case
// (COLLAB §3).
func TestDelegation(t *testing.T) {
	type want struct {
		owner, by string
		notes     []string
	}
	for _, c := range []struct {
		name string
		run  func(w *world) error
		want want
	}{
		{"created for another member", func(w *world) error {
			return w.create("robin", Item{ID: "a", Kind: KindAction, Owner: "dana"})
		}, want{"dana", "robin", []string{"delegated:a>dana"}}},
		{"created for oneself", func(w *world) error {
			return w.create("robin", Item{ID: "a", Kind: KindAction, Owner: "robin"})
		}, want{"robin", "", nil}},
		{"created unassigned", func(w *world) error {
			return w.create("robin", Item{ID: "a", Kind: KindAction})
		}, want{"", "", nil}},
		{"a client cannot claim who delegated", func(w *world) error {
			return w.create("robin", Item{ID: "a", Kind: KindAction, Owner: "robin", DelegatedBy: "dana"})
		}, want{"robin", "", nil}},
		{"given to another member", func(w *world) error {
			w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Owner: "robin"}))
			return w.patch("robin", "a", "owner", "dana")
		}, want{"dana", "robin", []string{"delegated:a>dana"}}},
		{"handed back", func(w *world) error {
			w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Owner: "dana"}))
			return w.patch("dana", "a", "owner", "robin")
		}, want{"robin", "", []string{"delegated:a>dana", "handed_back:a>robin"}}},
		{"taken back by the one who delegated", func(w *world) error {
			w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Owner: "dana"}))
			return w.patch("robin", "a", "owner", "robin")
		}, want{"robin", "", []string{"delegated:a>dana"}}},
		{"an unassigned action taken", func(w *world) error {
			w.must(w.create("robin", Item{ID: "a", Kind: KindAction}))
			return w.patch("dana", "a", "owner", "dana")
		}, want{"dana", "", nil}},
		{"left to anyone again", func(w *world) error {
			w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Owner: "dana"}))
			return w.patch("dana", "a", "owner", "")
		}, want{"", "", []string{"delegated:a>dana"}}},
		{"another field changed: nothing about delegation", func(w *world) error {
			w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Owner: "dana"}))
			return w.patch("dana", "a", "title", "renamed")
		}, want{"dana", "robin", []string{"delegated:a>dana"}}},
		{"delegated_by cannot be patched away or forged", func(w *world) error {
			w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Owner: "dana"}))
			return w.patch("dana", "a", "delegated_by", "dana")
		}, want{"dana", "robin", []string{"delegated:a>dana"}}},
		{"a done action given away tells nobody", func(w *world) error {
			w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Owner: "robin"}))
			w.must(w.move("robin", "a", StDone))
			return w.patch("robin", "a", "owner", "dana")
		}, want{"dana", "robin", nil}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := shared(t)
			if err := c.run(w); err != nil {
				t.Fatal(err)
			}
			it := w.st.Items["a"]
			if it.Owner != c.want.owner || it.DelegatedBy != c.want.by {
				t.Errorf("owner %q, delegated_by %q; want %q, %q", it.Owner, it.DelegatedBy, c.want.owner, c.want.by)
			}
			if got := w.noteList(); !slices.Equal(got, c.want.notes) {
				t.Errorf("notifications %v, want %v", got, c.want.notes)
			}
			checkReplay(t, w)
		})
	}

	// In a personal space there is nobody to delegate to.
	p := personal(t)
	p.must(p.create("robin", Item{ID: "a", Kind: KindAction}))
	if it := p.st.Items["a"]; it.Owner != "robin" || it.DelegatedBy != "" || len(p.notes) != 0 {
		t.Errorf("personal space: owner %q, delegated_by %q, %d notifications", it.Owner, it.DelegatedBy, len(p.notes))
	}

	// Waiting For: what robin delegated is on robin's list, not on dana's.
	w := shared(t)
	w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Owner: "dana"}))
	if got := ids(WaitingFor(w.st, "robin")); !slices.Equal(got, []string{"a"}) {
		t.Errorf("robin waits for %v", got)
	}
	if got := ids(WaitingFor(w.st, "dana")); len(got) != 0 {
		t.Errorf("dana waits for %v", got)
	}
	if got := ids(Engage(w.st, "dana", "2026-11-19", EngageFilter{})); !slices.Equal(got, []string{"a"}) {
		t.Errorf("dana's Engage: %v", got)
	}
}

// TestSpaceContexts: a shared space's own contexts are valid in it, and only
// in it.
func TestSpaceContexts(t *testing.T) {
	w := shared(t)
	w.st.Space.Contexts = []string{"calls-us"}
	w.must(w.create("robin", Item{ID: "a", Kind: KindAction, Context: "calls-us"}))
	if err := w.create("robin", Item{ID: "b", Kind: KindAction, Context: "calls-asia"}); err == nil {
		t.Error("a context the space does not have was accepted")
	}
	if !slices.Contains(Contexts(w.st, "robin"), "calls-us") {
		t.Errorf("contexts offered: %v", Contexts(w.st, "robin"))
	}
	p := personal(t)
	p.st.Space.Contexts = []string{"calls-us"} // not read for a personal space
	if err := p.create("robin", Item{ID: "a", Kind: KindAction, Context: "calls-us"}); err == nil {
		t.Error("a personal space took a space context")
	}
}
