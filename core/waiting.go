package core

// waitRule keeps an action's state and its "waiting for" in step, so that
// naming who you wait for is enough (it used to leave the action among the
// next actions):
//
//   - a patch that names someone on a next action makes it waiting;
//   - a patch that clears the name on a waiting action makes it next again;
//   - a transition from waiting back to next clears the name.
//
// The change is added to the op, so the log still explains the whole state.
// An op that sets both itself is left alone.
func waitRule(cur *Item, op *Op) {
	if cur == nil || cur.Kind != KindAction {
		return
	}
	has := func(field string) *FieldChange {
		for i := range op.Changes {
			if op.Changes[i].Field == field {
				return &op.Changes[i]
			}
		}
		return nil
	}
	switch op.Type {
	case OpPatch:
		c := has("waiting_on")
		if c == nil || has("state") != nil {
			return
		}
		switch name := str(c.New); {
		case name != "" && cur.State == StNext:
			op.Changes = append(op.Changes, FieldChange{Field: "state", Old: mustJSON(StNext), New: mustJSON(StWaiting)})
		case name == "" && cur.WaitingOn != "" && cur.State == StWaiting:
			op.Changes = append(op.Changes, FieldChange{Field: "state", Old: mustJSON(StWaiting), New: mustJSON(StNext)})
		}
	case OpTransition:
		c := has("state")
		if c != nil && cur.State == StWaiting && str(c.New) == StNext && cur.WaitingOn != "" && has("waiting_on") == nil {
			op.Changes = append(op.Changes, FieldChange{Field: "waiting_on", Old: mustJSON(cur.WaitingOn)})
		}
	}
}
