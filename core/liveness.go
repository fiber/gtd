package core

// Alive reports whether a project is alive (SPEC §6.3): it or a descendant
// project that is not finished has an action that is in `next` or `waiting`, or
// is deferred, or is `planned` behind an open blocker owned by someone other
// than the action's owner. A project that is not alive is stuck.
func Alive(st *State, project string) bool {
	children := map[string][]string{}
	actions := map[string][]*Item{}
	st.Each(func(it *Item) {
		switch {
		case it.Kind == KindProject && it.Parent != "":
			children[it.Parent] = append(children[it.Parent], it.ID)
		case it.Kind == KindAction && it.Project != "":
			actions[it.Project] = append(actions[it.Project], it)
		}
	})
	seen := map[string]bool{}
	var walk func(id string) bool
	walk = func(id string) bool {
		if seen[id] {
			return false
		}
		seen[id] = true
		for _, a := range actions[id] {
			if actionKeepsAlive(st, a) {
				return true
			}
		}
		for _, c := range children[id] {
			if p := st.Item(c); p != nil && !resolved(p.State) && walk(c) {
				return true
			}
		}
		return false
	}
	return walk(project)
}

func actionKeepsAlive(st *State, a *Item) bool {
	switch a.State {
	case StNext, StWaiting:
		return true
	case StPlanned:
		if a.DeferUntil != "" {
			return true
		}
		for _, b := range a.BlockedBy {
			if x := st.Item(b); x != nil && !resolved(x.State) && x.Owner != a.Owner {
				return true
			}
		}
	}
	return false
}
