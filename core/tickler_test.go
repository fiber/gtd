package core

import (
	"slices"
	"testing"
)

func TestTickle(t *testing.T) {
	w := shared(t)
	add := func(id, state, deferUntil string, blockedBy ...string) {
		w.must(w.create("robin", Item{ID: id, Kind: KindAction, State: state, DeferUntil: deferUntil, BlockedBy: blockedBy, Owner: "robin"}))
	}
	add("past", "", "2026-11-18")
	add("today", "", "2026-11-19")
	add("tomorrow", "", "2026-11-20")
	add("never", "", "")
	add("someday", StSomeday, "2026-11-01") // comes up in the Someday review instead
	add("waiting", StWaiting, "2026-11-10")
	add("finished", "", "2026-11-10")
	w.must(w.move("robin", "finished", StDone))
	w.must(w.create("robin", Item{ID: "note", Kind: KindNote, DeferUntil: "2026-11-19"}))
	w.must(w.create("robin", Item{ID: "proj", Kind: KindProject, DeferUntil: "2026-11-01"}))

	ops := Tickle(w.st, "2026-11-19")
	var got []string
	for _, op := range ops {
		got = append(got, op.Item)
	}
	if want := []string{"note", "past", "proj", "today", "waiting"}; !slices.Equal(got, want) {
		t.Fatalf("tickled %v, want %v", got, want)
	}
	for _, op := range ops {
		op.Actor = System
		if _, err := w.apply(op); err != nil {
			t.Fatalf("tickler op on %s rejected: %v", op.Item, err)
		}
	}
	for _, id := range got {
		it := w.st.Items[id]
		if it.DeferUntil != "" || it.UpdatedBy != System || !Resurfaced(it, "2026-11-16") {
			t.Errorf("%s after the tickler: defer %q, updated by %s, resurfaced %v", id, it.DeferUntil, it.UpdatedBy, Resurfaced(it, "2026-11-16"))
		}
	}
	if w.st.Items["tomorrow"].DeferUntil != "2026-11-20" || w.st.Items["someday"].DeferUntil != "2026-11-01" || w.st.Items["finished"].DeferUntil == "" {
		t.Error("the tickler touched an item that is not due, is someday, or is finished")
	}
	// Nothing left to do today; one more tomorrow.
	if ops := Tickle(w.st, "2026-11-19"); len(ops) != 0 {
		t.Errorf("a second run finds %d more", len(ops))
	}
	if ops := Tickle(w.st, "2026-11-20"); len(ops) != 1 || ops[0].Item != "tomorrow" {
		t.Errorf("the next day: %+v", ops)
	}
	// "Resurfaced" lasts three days and is not the same as "unblocked".
	if Resurfaced(w.st.Items["past"], "2099-01-01") || Resurfaced(w.st.Items["never"], "2026-11-16") {
		t.Error("resurfaced is reported for an old or untouched item")
	}
	add("blocker", "", "")
	add("dep", "", "", "blocker")
	w.must(w.move("robin", "blocker", StDone))
	if dep := w.st.Items["dep"]; !Unblocked(dep, "2026-11-16") || Resurfaced(dep, "2026-11-16") {
		t.Error("an unblocked action should count as unblocked, not as resurfaced")
	}
	checkReplay(t, w)
}
