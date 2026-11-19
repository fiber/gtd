package core

import "encoding/json"

// Delegation (COLLAB §3). In a shared space, giving an action to another
// member makes it theirs and records who gave it: owner = member,
// delegated_by = the one who did it. The state does not change. The member
// is told ("delegated"). Giving it back to the one who delegated it is a
// hand-back: delegated_by is cleared and that member is told ("handed_back").
// Taking an action, or leaving it to anyone, also clears delegated_by.
//
// delegated_by is never set by a client: the rule sets it, in the same op.

// Notification types of delegation.
const (
	NoteDelegated  = "delegated"
	NoteHandedBack = "handed_back"
)

// delegates says whether actor giving an action to owner is a delegation.
func delegates(ws *State, actor, kind, owner string) bool {
	return kind == KindAction && ws.Space.Kind == SpaceShared && owner != "" && owner != actor && actor != System
}

// delegatePatch completes a patch that changes an action's owner with the
// matching change of delegated_by. Whatever the client sent for delegated_by
// is replaced.
func delegatePatch(ws *State, cur *Item, op *Op) {
	owner, changes := "", op.Changes[:0:0]
	found := false
	for _, c := range op.Changes {
		switch c.Field {
		case "owner":
			found = true
			owner = str(c.New)
			changes = append(changes, c)
		case "delegated_by": // the rule's field
		default:
			changes = append(changes, c)
		}
	}
	op.Changes = changes
	if !found || cur.Kind != KindAction || owner == cur.Owner {
		return
	}
	by := ""
	if delegates(ws, op.Actor, cur.Kind, owner) && owner != cur.DelegatedBy {
		by = op.Actor
	}
	if by == cur.DelegatedBy {
		return
	}
	c := FieldChange{Field: "delegated_by"}
	if cur.DelegatedBy != "" {
		c.Old = mustJSON(cur.DelegatedBy)
	}
	if by != "" {
		c.New = mustJSON(by)
	}
	op.Changes = append(op.Changes, c)
}

// notifyDelegation tells the member who got an action, or got it back.
func notifyDelegation(ws *State, res *Result, before, after *Item, op Op) {
	if after.Kind != KindAction || after.Owner == "" || after.Owner == op.Actor || op.Actor == System ||
		ws.Space.Kind != SpaceShared || (before != nil && before.Owner == after.Owner) || resolved(after.State) {
		return
	}
	typ := NoteDelegated
	if before != nil && before.DelegatedBy == after.Owner {
		typ = NoteHandedBack
	}
	res.Notifications = append(res.Notifications, Notification{V: Version, User: after.Owner, Type: typ,
		Space: op.Space, Item: after.ID, From: op.Actor, TS: op.TS})
}

// str decodes a JSON string value; anything else, or nothing, is "".
func str(raw []byte) string {
	var s string
	if len(raw) > 0 {
		json.Unmarshal(raw, &s)
	}
	return s
}
