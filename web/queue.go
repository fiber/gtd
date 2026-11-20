package web

import (
	"maps"
	"slices"
	"sort"

	"github.com/fiber/gtd/core"
	"github.com/fiber/gtd/store"
)

// The inbox as a queue. Captures are processed oldest first, one at a time,
// and none is skipped (SPEC §5.2). There is one exception, Allen's
// "multitasking exception": a capture that cannot be decided this minute may
// be put back a few places. It stays in the inbox and comes up again after
// the next few captures; it is never out of sight.

// laterBy is how many places "Later" moves a capture back.
const laterBy = 3

// pushed records that a capture was put back: it comes right after the
// capture named After.
type pushed struct {
	Item  string `json:"item"`
	After string `json:"after"`
}

// inboxQueue returns the user's inbox captures in the order they are
// processed: oldest first, with the ones put back in their new places.
func (s *Server) inboxQueue(v *store.View, user string) ([]*core.Item, error) {
	caps, err := v.Items(user, func(it *core.Item) bool { return it.Kind == core.KindCapture && it.State == core.StInbox })
	if err != nil {
		return nil, err
	}
	sort.Slice(caps, func(i, j int) bool { return caps[i].ID < caps[j].ID }) // oldest first
	var moves []pushed
	s.st.GetUserState(user, "inbox-later", &moves)
	if len(moves) == 0 {
		return caps, nil
	}
	byID := map[string]*core.Item{}
	for _, c := range caps {
		byID[c.ID] = c
	}
	moved := map[string]bool{}
	for _, m := range moves {
		if byID[m.Item] != nil {
			moved[m.Item] = true
		}
	}
	queue := slices.DeleteFunc(slices.Clone(caps), func(c *core.Item) bool { return moved[c.ID] })
	for _, m := range moves { // in the order they were put back
		it := byID[m.Item]
		if it == nil {
			continue // clarified since
		}
		at := slices.IndexFunc(queue, func(c *core.Item) bool { return c.ID == m.After })
		// Its place is right after the capture it was put behind. If that
		// one is gone, the wait is over and it is next.
		queue = slices.Insert(queue, at+1, it)
	}
	return queue, nil
}

// putBack moves a capture laterBy places back in the user's queue. With
// nothing behind it there is nowhere to put it, and nothing happens.
func (s *Server) putBack(v *store.View, user, item string) error {
	queue, err := s.inboxQueue(v, user)
	if err != nil {
		return err
	}
	at := slices.IndexFunc(queue, func(c *core.Item) bool { return c.ID == item })
	if at < 0 || at == len(queue)-1 {
		return nil
	}
	after := queue[min(at+laterBy, len(queue)-1)].ID
	var moves []pushed
	s.st.GetUserState(user, "inbox-later", &moves)
	present := map[string]bool{}
	for _, c := range queue {
		present[c.ID] = true
	}
	// Forget what is no longer in the inbox, and this capture's earlier place.
	moves = slices.DeleteFunc(moves, func(m pushed) bool { return !present[m.Item] || m.Item == item })
	moves = append(moves, pushed{Item: item, After: after})
	return s.st.PutUserState(user, "inbox-later", moves)
}

// hintSpace remembers the space picked when a capture was made. Clarify
// proposes it; the capture itself stays in the inbox, in the personal space.
func (s *Server) hintSpace(v *store.View, user, item, space string) error {
	hints := map[string]string{}
	s.st.GetUserState(user, "capture-space", &hints)
	queue, err := s.inboxQueue(v, user)
	if err != nil {
		return err
	}
	waiting := map[string]bool{item: true}
	for _, c := range queue {
		waiting[c.ID] = true
	}
	maps.DeleteFunc(hints, func(id, _ string) bool { return !waiting[id] }) // forget what left the inbox
	hints[item] = space
	return s.st.PutUserState(user, "capture-space", hints)
}

// spaceHint is the space picked for a capture when it was made, or "".
func (s *Server) spaceHint(user, item string) string {
	hints := map[string]string{}
	s.st.GetUserState(user, "capture-space", &hints)
	return hints[item]
}
