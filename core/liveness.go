package core

// Alive reports whether a project is alive (SPEC §6.3): it or a descendant
// project that is not finished has an action that is in `next` or `waiting`, or
// is deferred, or is `planned` behind an open blocker owned by someone other
// than the action's owner. A project that is not alive is stuck.
func Alive(st *State, project string) bool {
	return Rollups(st, "")[project].Alive
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
