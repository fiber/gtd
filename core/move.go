package core

import (
	"fmt"
	"sort"
)

// Moving items to another space (COLLAB §1). A move is two ops per item,
// written in one batch by the store: in the space the item leaves, a move op
// that names the target; in the space it arrives in, a move op that carries
// the item and its comments. Each space's log therefore still explains that
// space's state on its own, and the item's earlier history stays where it
// happened.
//
// What moves together: a project takes its sub-projects and every action and
// note in them along. Anything else moves alone. What cannot come along is
// let go: a moved item's project, parent, area and blockers stay only if
// they move too, and whatever stays behind stops waiting for it.
//
// Move is the payload of a move op. Exactly one of To and From is set.
type Move struct {
	To       string     `json:"to,omitempty"`       // leaving: the space the item goes to
	From     string     `json:"from,omitempty"`     // arriving: the space it comes from
	Item     *Item      `json:"item,omitempty"`     // arriving: the item as it was
	Comments []*Comment `json:"comments,omitempty"` // arriving: its thread
}

// Movable says why an item cannot move, or nil. An area is a part of one
// space's map of life, not a thing to hand over.
func Movable(it *Item) error {
	if it.Kind == KindArea {
		return fmt.Errorf("%w: an area cannot be moved to another space (item %s)", ErrInvalid, it.ID)
	}
	return nil
}

// MoveSet returns the items that move when the item id does, in an order in
// which they can arrive: projects before what is in them, blockers before
// what they block.
func MoveSet(st *State, id string) ([]*Item, error) {
	root := st.Item(id)
	if root == nil {
		return nil, fmt.Errorf("%w: item %s", ErrNotFound, id)
	}
	if err := Movable(root); err != nil {
		return nil, err
	}
	if root.Kind != KindProject {
		return []*Item{root}, nil
	}
	in := map[string]bool{id: true}
	projects := []*Item{root}
	for grew := true; grew; { // sub-projects, level by level
		grew = false
		st.Each(func(it *Item) {
			if it.Kind == KindProject && !in[it.ID] && in[it.Parent] {
				in[it.ID], grew = true, true
				projects = append(projects, it)
			}
		})
	}
	var rest []*Item
	st.Each(func(it *Item) {
		if it.Kind != KindProject && in[it.Project] {
			in[it.ID] = true
			rest = append(rest, it)
		}
	})
	sort.SliceStable(projects[1:], func(i, j int) bool { return projects[1+i].ID < projects[1+j].ID })
	// Parents before children.
	ordered := []*Item{root}
	placed := map[string]bool{id: true}
	for len(ordered) < len(projects) {
		for _, p := range projects[1:] {
			if !placed[p.ID] && placed[p.Parent] {
				placed[p.ID] = true
				ordered = append(ordered, p)
			}
		}
	}
	// Blockers before the blocked. Blockers outside the set do not count.
	sort.Slice(rest, func(i, j int) bool { return rest[i].ID < rest[j].ID })
	for len(rest) > 0 {
		var later []*Item
		for _, it := range rest {
			ready := true
			for _, b := range it.BlockedBy {
				if in[b] && !placed[b] {
					ready = false
				}
			}
			if ready {
				placed[it.ID] = true
				ordered = append(ordered, it)
			} else {
				later = append(later, it)
			}
		}
		if len(later) == len(rest) { // cannot happen: blockers have no cycles
			ordered = append(ordered, later...)
			break
		}
		rest = later
	}
	return ordered, nil
}

// Relocate returns the item as it can live in the target space: references
// to things that do not move with it are dropped, and owner, context and
// template are kept only where the target space knows them.
func Relocate(it *Item, target *State, moving func(id string) bool) *Item {
	n := *it
	keep := func(id string) string {
		if id != "" && moving(id) {
			return id
		}
		return ""
	}
	n.Project, n.Parent, n.Area = keep(it.Project), keep(it.Parent), ""
	n.BlockedBy = nil
	for _, b := range it.BlockedBy {
		if moving(b) {
			n.BlockedBy = append(n.BlockedBy, b)
		}
	}
	if n.Kind == KindAction && n.State == StPlanned && len(n.BlockedBy) == 0 {
		n.State = StNext // nothing it waited for comes along
	}
	sp := target.Space
	switch {
	case sp.Kind == SpacePersonal && n.Kind == KindAction:
		n.Owner, n.DelegatedBy = sp.Members[0], ""
	case sp.Kind == SpacePersonal:
		n.Owner, n.DelegatedBy = "", ""
		if it.Owner != "" && sp.HasMember(it.Owner) {
			n.Owner = it.Owner
		}
	default:
		if !sp.HasMember(n.Owner) {
			n.Owner, n.DelegatedBy = "", ""
		}
		if !sp.HasMember(n.DelegatedBy) {
			n.DelegatedBy = ""
		}
	}
	if n.Context != "" && !validContext(target, n.Context) {
		n.Context = ""
	}
	if n.Template != "" && template(sp, n.Template, n.Kind) == nil {
		n.Template = "" // its field values stay, as plain fields
	}
	return &n
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
