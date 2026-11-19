package core

import (
	"fmt"
)

// Moving an item to another space (COLLAB §1). A move is two ops, written in
// one batch by the store: in the space the item leaves, a move op that names
// the target; in the space it arrives in, a move op that carries the item and
// its comments. Each space's log therefore still explains that space's state
// on its own, and the item's earlier history stays where it happened.
//
// For now only a capture in the inbox can move: it has no project, area,
// parent or blockers, and nothing refers to it, so nothing dangles. That is
// what Clarify's "which space?" needs (SPEC §5.2).

// Move is the payload of a move op. Exactly one of To and From is set.
type Move struct {
	To       string     `json:"to,omitempty"`       // leaving: the space the item goes to
	From     string     `json:"from,omitempty"`     // arriving: the space it comes from
	Item     *Item      `json:"item,omitempty"`     // arriving: the item as it was
	Comments []*Comment `json:"comments,omitempty"` // arriving: its thread
}

// Movable says why an item cannot move, or nil.
func Movable(it *Item) error {
	if it.Kind != KindCapture || it.State != StInbox {
		return fmt.Errorf("%w: only a capture in the inbox can be moved to another space (item %s is a %s, %s)", ErrInvalid, it.ID, it.Kind, it.State)
	}
	return nil
}

func applyMove(ws *State, op Op, res *Result) (*Result, error) {
	var m Move
	if err := strict(op.Payload, &m); err != nil || (m.To == "") == (m.From == "") || len(op.Changes) > 0 {
		return nil, fmt.Errorf("%w: move needs a target or a source space (%v)", ErrInvalid, err)
	}
	if m.To != "" { // leaving
		it := ws.Item(op.Item)
		if it == nil {
			return nil, fmt.Errorf("%w: item %s", ErrNotFound, op.Item)
		}
		if m.To == ws.Space.ID || m.Item != nil || m.Comments != nil {
			return nil, fmt.Errorf("%w: move of %s to its own space, or with a payload", ErrInvalid, it.ID)
		}
		if err := Movable(it); err != nil {
			return nil, err
		}
		if op.BaseRev != 0 && op.BaseRev != it.Rev {
			return nil, fmt.Errorf("%w: item %s is at rev %d, the move is based on rev %d", ErrConflict, it.ID, it.Rev, op.BaseRev)
		}
		op.BaseRev = it.Rev
		res.Ops, res.Removed = []Op{op}, []string{it.ID}
		return res, nil
	}
	// arriving
	if m.Item == nil || m.Item.ID != op.Item || m.From == ws.Space.ID {
		return nil, fmt.Errorf("%w: arriving move of %s without its item", ErrInvalid, op.Item)
	}
	if ws.Item(op.Item) != nil {
		return nil, fmt.Errorf("%w: item %s already exists in space %q", ErrConflict, op.Item, ws.Space.ID)
	}
	if err := Movable(m.Item); err != nil {
		return nil, err
	}
	for _, c := range m.Comments {
		if c.Item != op.Item {
			return nil, fmt.Errorf("%w: comment %s belongs to item %s, not %s", ErrInvalid, c.ID, c.Item, op.Item)
		}
	}
	it := arrive(m.Item, op)
	ws.Pending[it.ID] = it
	if err := validate(ws, it); err != nil {
		return nil, err
	}
	res.Ops, res.Items, res.Comments = []Op{op}, []*Item{it}, m.Comments
	return res, nil
}

// arrive returns the item as it is in its new space.
func arrive(from *Item, op Op) *Item {
	it := *from
	it.Space = op.Space
	it.Rev++
	it.Updated, it.UpdatedBy = op.TS, op.Actor
	return &it
}

func replayMove(items map[string]*Item, comments map[string]*Comment, op Op) error {
	var m Move
	if err := strict(op.Payload, &m); err != nil {
		return fmt.Errorf("op %s: %v", op.ID, err)
	}
	if m.To != "" {
		delete(items, op.Item)
		for id, c := range comments {
			if c.Item == op.Item {
				delete(comments, id)
			}
		}
		return nil
	}
	if m.Item == nil {
		return fmt.Errorf("%w: op %s: arriving move without an item", ErrInvalid, op.ID)
	}
	items[op.Item] = arrive(m.Item, op)
	for _, c := range m.Comments {
		comments[c.ID] = c
	}
	return nil
}
