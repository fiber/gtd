package core

import (
	"slices"
	"sort"
)

// The read-side rules: which actions Engage shows and in what order, what
// counts as Waiting For, and the roll-ups of the plan view. Pure, like Apply.

var (
	timeRank   = map[string]int{"5m": 1, "15m": 2, "30m": 3, "1h": 4, "2h+": 5}
	energyRank = map[string]int{"low": 1, "med": 2, "high": 3}
)

// EngageFilter holds the answers to Engage's questions. Empty means "skipped".
type EngageFilter struct {
	Contexts []string // where are you?
	Time     string   // how much time?
	Energy   string   // energy?
}

// Deferred reports whether the item is hidden from working views today.
func Deferred(it *Item, today string) bool { return it.DeferUntil > today }

// Engage returns the actions user can do now in one space (SPEC §5.3): `next`,
// owned by user or unassigned in a shared space, not deferred, matching the
// filter. Order: due, then target, then recently unblocked or resurfaced
// (last changed by the system), then age.
func Engage(st *State, user, today string, f EngageFilter) []*Item {
	var out []*Item
	st.Each(func(it *Item) {
		if it.Kind != KindAction || it.State != StNext || Deferred(it, today) {
			return
		}
		if it.Owner != user && !(it.Owner == "" && st.Space.Kind == SpaceShared) {
			return
		}
		// With places selected, only actions for those places show, plus the ones
		// that can be done anywhere. An action without a context is not one of them.
		if len(f.Contexts) > 0 && it.Context != "anywhere" && !slices.Contains(f.Contexts, it.Context) {
			return
		}
		if f.Time != "" && timeRank[it.Time] > timeRank[f.Time] {
			return
		}
		if f.Energy != "" && energyRank[it.Energy] > energyRank[f.Energy] {
			return
		}
		out = append(out, it)
	})
	SortEngage(out, today)
	return out
}

// Active says which answers to Engage's questions have something lined up,
// so that the choices can show it before one is picked.
type Active struct {
	Contexts []string `json:"context"`
	Time     []string `json:"time"`
	Energy   []string `json:"energy"`
}

// EngageActive marks the answers that lead to an action in items (the
// unfiltered Engage list). Each question is judged with the other two as
// answered in f: a place counts when an action is for exactly that place
// (what can be done anywhere would mark every place), a time or an energy
// when an action with an estimate fits into it.
func EngageActive(items []*Item, f EngageFilter, into *Active) {
	add := func(list *[]string, v string) {
		if !slices.Contains(*list, v) {
			*list = append(*list, v)
		}
	}
	for _, it := range items {
		place := len(f.Contexts) == 0 || it.Context == "anywhere" || slices.Contains(f.Contexts, it.Context)
		time := f.Time == "" || timeRank[it.Time] <= timeRank[f.Time]
		energy := f.Energy == "" || energyRank[it.Energy] <= energyRank[f.Energy]
		if it.Context != "" && time && energy {
			add(&into.Contexts, it.Context)
		}
		if it.Time != "" && place && energy {
			for t, rank := range timeRank {
				if timeRank[it.Time] <= rank {
					add(&into.Time, t)
				}
			}
		}
		if it.Energy != "" && place && time {
			for e, rank := range energyRank {
				if energyRank[it.Energy] <= rank {
					add(&into.Energy, e)
				}
			}
		}
	}
}

// Today returns what the day asks of the user: their next actions and
// waiting-fors that are due or have their target today or earlier, and the
// actions that came back today (a deferred or repeating action whose day it
// is). No filters apply: it is a short list to look at first.
func Today(st *State, user, today string) []*Item {
	var out []*Item
	st.Each(func(it *Item) {
		if it.Kind != KindAction || (it.State != StNext && it.State != StWaiting) || Deferred(it, today) {
			return
		}
		if it.Owner != user && !(it.Owner == "" && st.Space.Kind == SpaceShared) {
			return
		}
		if ForToday(it, today) {
			out = append(out, it)
		}
	})
	SortEngage(out, today)
	return out
}

// ForToday says whether an open action is one of the day's: due or with its
// target today or earlier, or a next action that came back today from a
// deferral or a repeat.
func ForToday(it *Item, today string) bool {
	if today == "" {
		return false
	}
	dated := it.Due != "" && it.Due <= today || it.Target != "" && it.Target <= today
	return dated || it.State == StNext && (it.DeferUntil == today || Resurfaced(it, today))
}

// SortEngage orders actions for Engage: first what the day asks for (ForToday),
// then by due date, target, what the system just brought back, and age. It is
// exported so that lists merged from several spaces are ordered the same way.
func SortEngage(items []*Item, today string) {
	last := func(s string) string { // empty dates sort last
		if s == "" {
			return "9999-99-99"
		}
		return s
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		// What the day asks for comes first: the Today tab's items lead the list.
		if ta, tb := ForToday(a, today), ForToday(b, today); ta != tb {
			return ta
		}
		if da, db := last(a.Due), last(b.Due); da != db {
			return da < db
		}
		if ta, tb := last(a.Target), last(b.Target); ta != tb {
			return ta < tb
		}
		if sa, sb := a.UpdatedBy == System, b.UpdatedBy == System; sa != sb {
			return sa
		}
		if !a.Created.Equal(b.Created) {
			return a.Created.Before(b.Created)
		}
		return a.ID < b.ID
	})
}

// WaitingFor returns what user waits for in one space (COLLAB §3): actions
// user delegated to someone else, `waiting` actions user owns, and user's
// `planned` actions with an open blocker owned by someone else. Oldest first.
func WaitingFor(st *State, user string) []*Item {
	var out []*Item
	st.Each(func(it *Item) {
		if it.Kind != KindAction || resolved(it.State) {
			return
		}
		switch {
		case it.DelegatedBy == user && it.Owner != user:
		case it.State == StWaiting && it.Owner == user:
		case it.State == StPlanned && it.Owner == user && blockedByOthers(st, it):
		default:
			return
		}
		out = append(out, it)
	})
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Updated.Equal(out[j].Updated) {
			return out[i].Updated.Before(out[j].Updated)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func blockedByOthers(st *State, it *Item) bool {
	for _, b := range it.BlockedBy {
		if x := st.Item(b); x != nil && !resolved(x.State) && x.Owner != it.Owner {
			return true
		}
	}
	return false
}

// OpenBlockers returns the blockers of an action that are not finished.
func OpenBlockers(st *State, it *Item) []*Item {
	var out []*Item
	for _, b := range it.BlockedBy {
		if x := st.Item(b); x != nil && !resolved(x.State) {
			out = append(out, x)
		}
	}
	return out
}

// Dated returns the open actions and projects of a space with a due or target
// date up to and including until, earliest first.
func Dated(st *State, until string) []*Item {
	date := func(it *Item) string {
		if it.Due != "" && (it.Target == "" || it.Due < it.Target) {
			return it.Due
		}
		return it.Target
	}
	var out []*Item
	st.Each(func(it *Item) {
		if (it.Kind == KindAction || it.Kind == KindProject) && !resolved(it.State) && it.State != StSomeday {
			if d := date(it); d != "" && d <= until {
				out = append(out, it)
			}
		}
	})
	sort.Slice(out, func(i, j int) bool {
		if di, dj := date(out[i]), date(out[j]); di != dj {
			return di < dj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Rollup summarizes a project including its descendant projects (SPEC §6.4).
type Rollup struct {
	Done           int    `json:"done"`
	Total          int    `json:"total"` // actions that are not dropped or trashed
	Blocked        int    `json:"blocked"`
	Waiting        int    `json:"waiting"`
	EarliestTarget string `json:"earliest_target,omitempty"` // of open actions and projects
	Slipping       bool   `json:"slipping,omitempty"`
	Alive          bool   `json:"alive"`
}

// Rollups returns the roll-up of every project in the space.
func Rollups(st *State, today string) map[string]Rollup {
	children := map[string][]string{}
	own := map[string]Rollup{}
	var projects []string
	target := func(r *Rollup, it *Item) {
		if it.Target == "" || resolved(it.State) {
			return
		}
		if r.EarliestTarget == "" || it.Target < r.EarliestTarget {
			r.EarliestTarget = it.Target
		}
		if it.Target < today {
			r.Slipping = true
		}
	}
	st.Each(func(it *Item) {
		switch it.Kind {
		case KindProject:
			projects = append(projects, it.ID)
			if it.Parent != "" {
				children[it.Parent] = append(children[it.Parent], it.ID)
			}
			r := own[it.ID]
			target(&r, it)
			own[it.ID] = r
		case KindAction:
			if it.Project == "" || it.State == StDropped || it.State == StTrashed {
				return
			}
			r := own[it.Project]
			r.Total++
			switch it.State {
			case StDone:
				r.Done++
			case StPlanned:
				r.Blocked++
			case StWaiting:
				r.Waiting++
			}
			target(&r, it)
			if actionKeepsAlive(st, it) {
				r.Alive = true
			}
			own[it.Project] = r
		}
	})
	out := map[string]Rollup{}
	var sum func(id string, seen map[string]bool) Rollup
	sum = func(id string, seen map[string]bool) Rollup {
		r := own[id]
		seen[id] = true
		for _, c := range children[id] {
			p := st.Item(c)
			if seen[c] || p == nil || resolved(p.State) {
				continue
			}
			cr := sum(c, seen)
			r.Done, r.Total, r.Blocked, r.Waiting = r.Done+cr.Done, r.Total+cr.Total, r.Blocked+cr.Blocked, r.Waiting+cr.Waiting
			if cr.EarliestTarget != "" && (r.EarliestTarget == "" || cr.EarliestTarget < r.EarliestTarget) {
				r.EarliestTarget = cr.EarliestTarget
			}
			r.Slipping = r.Slipping || cr.Slipping
			r.Alive = r.Alive || cr.Alive
		}
		return r
	}
	for _, id := range projects {
		out[id] = sum(id, map[string]bool{})
	}
	return out
}
