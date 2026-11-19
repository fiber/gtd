package core

import "sort"

// Tickle returns the ops that resurface deferred items (SPEC §5.6): every
// open item whose defer_until is today or earlier gets it cleared. The caller
// applies them as the system actor. Someday items are left alone: they come up
// in the Someday step of the weekly review instead.
func Tickle(st *State, today string) []Op {
	var due []*Item
	st.Each(func(it *Item) {
		if it.DeferUntil != "" && it.DeferUntil <= today && it.State != StSomeday && !resolved(it.State) {
			due = append(due, it)
		}
	})
	sort.Slice(due, func(i, j int) bool { return due[i].ID < due[j].ID })
	ops := make([]Op, 0, len(due))
	for _, it := range due {
		ops = append(ops, Op{Via: System, Type: OpPatch, Item: it.ID, BaseRev: it.Rev,
			Changes: []FieldChange{{Field: "defer_until", Old: mustJSON(it.DeferUntil)}}})
	}
	return ops
}

// Resurfaced reports whether the tickler brought the item back on or after
// the date since: its last change was the system clearing defer_until, not
// the dependency rule unblocking it.
func Resurfaced(it *Item, since string) bool {
	return it.UpdatedBy == System && len(it.BlockedBy) == 0 && it.DeferUntil == "" &&
		!resolved(it.State) && it.Updated.Format("2006-01-02") >= since
}

// Unblocked reports whether the dependency rule made the action next on or
// after the date since.
func Unblocked(it *Item, since string) bool {
	return it.Kind == KindAction && it.State == StNext && it.UpdatedBy == System && len(it.BlockedBy) > 0 &&
		it.Updated.Format("2006-01-02") >= since
}
