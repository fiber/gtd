package core

import "sort"

// Date plausibility warning types (SPEC §6.5). They never block an op.
const (
	WarnChildAfterParent    = "child_after_parent"    // project target later than its parent's
	WarnTargetBeforeBlocker = "target_before_blocker" // action target earlier than an open blocker's
	WarnSlipping            = "slipping"              // target passed, not finished
)

type Warning struct {
	Type  string
	Item  string
	Other string // the parent project or the blocker
}

// Warnings returns the date warnings of a space, sorted by item, type and other.
// today is a YYYY-MM-DD date. Dates in that form compare correctly as strings.
func Warnings(st *State, today string) []Warning {
	var out []Warning
	st.Each(func(it *Item) {
		if it.Target == "" || resolved(it.State) {
			return
		}
		switch it.Kind {
		case KindProject:
			if p := st.Item(it.Parent); p != nil && p.Target != "" && it.Target > p.Target {
				out = append(out, Warning{WarnChildAfterParent, it.ID, p.ID})
			}
		case KindAction:
			for _, b := range it.BlockedBy {
				if x := st.Item(b); x != nil && !resolved(x.State) && x.Target != "" && it.Target < x.Target {
					out = append(out, Warning{WarnTargetBeforeBlocker, it.ID, x.ID})
				}
			}
		default:
			return
		}
		if it.Target < today {
			out = append(out, Warning{WarnSlipping, it.ID, ""})
		}
	})
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Item != b.Item {
			return a.Item < b.Item
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.Other < b.Other
	})
	return out
}
