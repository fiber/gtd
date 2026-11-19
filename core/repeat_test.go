package core

import (
	"errors"
	"testing"
	"time"
)

func TestParseRepeat(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "1d": "1d", " 2D ": "2d", "3w": "3w", "365d": "365d",
		"fri,mon,wed": "mon,wed,fri", "Mon, Mon ,sun": "mon,sun", "sat": "sat",
	} {
		if got, err := ParseRepeat(in); err != nil || got != want {
			t.Errorf("ParseRepeat(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"0d", "-1d", "d", "2m", "366d", "daily", "mon,xyz", "1.5w", ","} {
		if _, err := ParseRepeat(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseRepeat(%q) accepted: %v", bad, err)
		}
	}
}

func TestNextRepeat(t *testing.T) {
	// 2026-11-19 is a Thursday.
	for _, c := range []struct{ repeat, done, want string }{
		{"1d", "2026-11-19", "2026-11-20"},
		{"3d", "2026-11-19", "2026-11-22"},
		{"1w", "2026-11-19", "2026-11-26"},
		{"2w", "2026-12-25", "2027-01-08"},
		{"mon,wed,fri", "2026-11-19", "2026-11-20"}, // Thursday → Friday
		{"mon,wed,fri", "2026-11-20", "2026-11-23"}, // Friday → Monday, across the weekend
		{"mon,wed,fri", "2026-11-21", "2026-11-23"}, // done on a Saturday
		{"thu", "2026-11-19", "2026-11-26"},         // done on its own day: next week
		{"sun", "2026-11-19", "2026-11-22"},
		{"1d", "2028-02-28", "2028-02-29"}, // leap year
	} {
		if got := NextRepeat(c.repeat, c.done); got != c.want {
			t.Errorf("NextRepeat(%q, %s) = %s, want %s", c.repeat, c.done, got, c.want)
		}
	}
}

func TestRepeatRule(t *testing.T) {
	w := shared(t)
	w.must(w.create("robin", Item{ID: "area", Kind: KindArea}))
	w.must(w.create("robin", Item{ID: "p", Kind: KindProject}))
	w.must(w.create("robin", Item{ID: "trash", Kind: KindAction, Title: "Take out the trash", Repeat: "1w", Owner: "dana",
		Project: "p", Area: "area", Context: "home", Energy: "low", Time: "5m", Body: "bins behind the house", Target: "2026-11-20"}))

	// successor returns the one open action with this title other than id.
	successor := func(title, not string) *Item {
		t.Helper()
		var found *Item
		for _, it := range w.st.Items {
			if it.Title == title && it.ID != not && it.State != StDone {
				if found != nil {
					t.Fatalf("two open instances of %q", title)
				}
				found = it
			}
		}
		return found
	}
	res, err := w.apply(Op{Actor: "dana", Type: OpTransition, Item: "trash", BaseRev: 1, Changes: []FieldChange{{Field: "state", New: mustJSON(StDone)}}})
	w.must(err)
	if len(res.Ops) != 2 || res.Ops[1].Type != OpCreate || res.Ops[1].Actor != System {
		t.Fatalf("ops = %+v", res.Ops)
	}
	next := successor("Take out the trash", "trash")
	if next == nil {
		t.Fatal("completing a repeating action created no successor")
	}
	doneOn := res.Ops[0].TS.Format(time.DateOnly)
	if next.State != StNext || next.DeferUntil != NextRepeat("1w", doneOn) || next.Repeat != "1w" || next.Owner != "dana" ||
		next.Project != "p" || next.Area != "area" || next.Context != "home" || next.Energy != "low" || next.Time != "5m" ||
		next.Body != "bins behind the house" || next.Target != "" || len(next.DerivedFrom) != 1 || next.DerivedFrom[0] != "trash" || next.UpdatedBy != System {
		t.Errorf("successor: %+v", next)
	}
	// It is hidden until its day.
	if list := Engage(w.st, "dana", doneOn, EngageFilter{}); len(list) != 0 {
		t.Errorf("the successor shows in Engage before its day: %v", ids(list))
	}
	// Completing the successor makes the next one: the chain goes on.
	w.must(w.move("dana", next.ID, StDone))
	if third := successor("Take out the trash", next.ID); third == nil || third.ID == "trash" {
		t.Error("the chain stopped after the second instance")
	}

	// Dropping or trashing ends it. So does clearing the setting.
	w.must(w.create("robin", Item{ID: "gym", Kind: KindAction, Title: "Exercise", Repeat: "mon,wed,fri"}))
	w.must(w.move("robin", "gym", StDropped))
	if successor("Exercise", "gym") != nil {
		t.Error("a dropped repeating action came back")
	}
	w.must(w.create("robin", Item{ID: "once", Kind: KindAction, Title: "Once", Repeat: "1d"}))
	w.must(w.patch("robin", "once", "repeat", ""))
	w.must(w.move("robin", "once", StDone))
	if successor("Once", "once") != nil {
		t.Error("an action whose repeat was cleared came back")
	}
	// A blocked repeating action: its successor is free of the old blockers.
	w.must(w.create("robin", Item{ID: "blocker", Kind: KindAction}))
	w.must(w.create("robin", Item{ID: "dep", Kind: KindAction, Title: "Dependent", Repeat: "2d", BlockedBy: []string{"blocker"}}))
	w.must(w.move("robin", "dep", StDone))
	if s := successor("Dependent", "dep"); s == nil || len(s.BlockedBy) != 0 || s.State != StNext {
		t.Errorf("successor of a blocked action: %+v", s)
	}

	// Validation.
	for name, c := range map[string]struct {
		it  Item
		err error
	}{
		"bad syntax":      {Item{ID: "x1", Kind: KindAction, Repeat: "daily"}, ErrInvalid},
		"not canonical":   {Item{ID: "x2", Kind: KindAction, Repeat: "fri,mon"}, ErrInvalid},
		"on a project":    {Item{ID: "x3", Kind: KindProject, Repeat: "1w"}, ErrInvalid},
		"on a note":       {Item{ID: "x4", Kind: KindNote, Repeat: "1w"}, ErrInvalid},
		"weekdays, valid": {Item{ID: "x5", Kind: KindAction, Repeat: "tue,thu"}, nil},
	} {
		if err := w.create("robin", c.it); !errors.Is(err, c.err) || (c.err == nil && err != nil) {
			t.Errorf("%s: %v", name, err)
		}
	}
	checkReplay(t, w)
}
