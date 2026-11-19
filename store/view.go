package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble/v2"

	"github.com/fiber/gtd/core"
)

// View is the only way to content. It acts as one user and checks that user's
// membership on every call (invariant 4). A space the user is not a member of
// is reported as not found, the same as a space that does not exist.
type View struct {
	s    *Store
	user string
}

// View returns the view of an existing user.
func (s *Store) View(user string) (*View, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.users[user] == nil {
		return nil, fmt.Errorf("%w: user %q", core.ErrNotFound, user)
	}
	return &View{s: s, user: user}, nil
}

// User returns the acting user's ID.
func (v *View) User() string { return v.user }

// space returns a space the user is a member of. Callers hold s.mu.
func (v *View) space(id string) (*space, error) {
	sp := v.s.spaces[id]
	if sp == nil || !sp.rec.HasMember(v.user) {
		return nil, fmt.Errorf("%w: space %q", core.ErrNotFound, id)
	}
	return sp, nil
}

// Spaces returns the spaces the user is a member of, sorted by ID.
func (v *View) Spaces() []*core.Space {
	v.s.mu.RLock()
	defer v.s.mu.RUnlock()
	var out []*core.Space
	for _, sp := range v.s.spaces {
		if sp.rec.HasMember(v.user) {
			out = append(out, sp.rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Item returns one item. The result must not be modified.
func (v *View) Item(spaceID, id string) (*core.Item, error) {
	v.s.mu.RLock()
	defer v.s.mu.RUnlock()
	sp, err := v.space(spaceID)
	if err != nil {
		return nil, err
	}
	it := sp.items[id]
	if it == nil {
		return nil, fmt.Errorf("%w: item %s in space %q", core.ErrNotFound, id, spaceID)
	}
	return it, nil
}

// Find returns an item by ID from whichever of the user's spaces holds it.
func (v *View) Find(id string) (*core.Item, error) {
	v.s.mu.RLock()
	defer v.s.mu.RUnlock()
	for _, sp := range v.s.spaces {
		if it := sp.items[id]; it != nil && sp.rec.HasMember(v.user) {
			return it, nil
		}
	}
	return nil, fmt.Errorf("%w: item %s", core.ErrNotFound, id)
}

// Blob returns the attachment entry for a blob that an item in one of the
// user's spaces carries. A blob nobody attached, or one attached only in
// spaces the user is not a member of, is not found.
func (v *View) Blob(sha string) (core.Attachment, bool) {
	v.s.mu.RLock()
	defer v.s.mu.RUnlock()
	for _, sp := range v.s.spaces {
		if !sp.rec.HasMember(v.user) {
			continue
		}
		for _, it := range sp.items {
			for _, a := range it.Attachments {
				if a.SHA256 == sha {
					return a, true
				}
			}
		}
	}
	return core.Attachment{}, false
}

// State returns a snapshot of a space for the pure functions in core and
// wizard (roll-ups, liveness, warnings, step machines). It must not be modified.
func (v *View) State(spaceID string) (*core.State, error) {
	v.s.mu.RLock()
	defer v.s.mu.RUnlock()
	sp, err := v.space(spaceID)
	if err != nil {
		return nil, err
	}
	st := &core.State{Space: sp.rec, Items: maps.Clone(sp.items)}
	if sp.rec.Kind == core.SpacePersonal {
		if u := v.s.users[sp.rec.Members[0]]; u != nil {
			st.PersonalContexts = u.PersonalContexts
		}
	}
	return st, nil
}

// Dismiss marks one of the user's notifications as dismissed. Notifications
// are per-user state, not ops.
func (v *View) Dismiss(seq uint64) error {
	var n core.Notification
	if err := getJSON(v.s.db, notifKey(v.user, seq), &n); err != nil {
		return fmt.Errorf("%w: notification %d", core.ErrNotFound, seq)
	}
	n.Dismissed = true
	return v.s.db.Set(notifKey(v.user, seq), mustJSON(n), pebble.Sync)
}

// Items returns the items of a space that match, sorted by ID. A nil match
// returns all. The results must not be modified.
func (v *View) Items(spaceID string, match func(*core.Item) bool) ([]*core.Item, error) {
	v.s.mu.RLock()
	defer v.s.mu.RUnlock()
	sp, err := v.space(spaceID)
	if err != nil {
		return nil, err
	}
	var out []*core.Item
	for _, it := range sp.items {
		if match == nil || match(it) {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Grep returns the items in the user's spaces whose title, body or field values
// contain every word of the query, ignoring case. Sorted by space, then ID.
func (v *View) Grep(query string) []*core.Item {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil
	}
	v.s.mu.RLock()
	defer v.s.mu.RUnlock()
	var out []*core.Item
	for _, sp := range v.s.spaces {
		if !sp.rec.HasMember(v.user) {
			continue
		}
	next:
		for id, text := range sp.text {
			for _, w := range words {
				if !strings.Contains(text, w) {
					continue next
				}
			}
			out = append(out, sp.items[id])
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Space != out[j].Space {
			return out[i].Space < out[j].Space
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// CommentCount returns the number of comments on an item, tombstones included.
func (v *View) CommentCount(spaceID, item string) (int, error) {
	v.s.mu.RLock()
	defer v.s.mu.RUnlock()
	sp, err := v.space(spaceID)
	if err != nil {
		return 0, err
	}
	return sp.comments[item], nil
}

// Comments returns the thread of an item, oldest first.
func (v *View) Comments(spaceID, item string) ([]*core.Comment, error) {
	if err := v.check(spaceID); err != nil {
		return nil, err
	}
	var out []*core.Comment
	err := scan(v.s.db, commentPrefix(spaceID)+item+"/", func(key string, val []byte) error {
		c := &core.Comment{}
		if err := json.Unmarshal(val, c); err != nil {
			return fmt.Errorf("key %q: %v", key, err)
		}
		out = append(out, c)
		return nil
	})
	return out, err
}

// Ops returns the last n ops of a space, oldest first.
func (v *View) Ops(spaceID string, n int) ([]core.Op, error) {
	if err := v.check(spaceID); err != nil {
		return nil, err
	}
	p := opPrefix(spaceID)
	it, err := v.s.db.NewIter(&pebble.IterOptions{LowerBound: []byte(p), UpperBound: prefixEnd(p)})
	if err != nil {
		return nil, err
	}
	defer it.Close()
	var out []core.Op
	for ok := it.Last(); ok && len(out) < n; ok = it.Prev() {
		var op core.Op
		if err := json.Unmarshal(it.Value(), &op); err != nil {
			return nil, fmt.Errorf("key %q: %v", it.Key(), err)
		}
		out = append(out, op)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, it.Error()
}

// Notifications returns the user's "From others" inbox, oldest first.
func (v *View) Notifications() ([]core.Notification, error) {
	var out []core.Notification
	err := scan(v.s.db, notifPrefix(v.user), func(key string, val []byte) error {
		var n core.Notification
		if err := json.Unmarshal(val, &n); err != nil {
			return fmt.Errorf("key %q: %v", key, err)
		}
		out = append(out, n)
		return nil
	})
	return out, err
}

func (v *View) check(spaceID string) error {
	v.s.mu.RLock()
	defer v.s.mu.RUnlock()
	_, err := v.space(spaceID)
	return err
}

// opRef is the value of the opid/ idempotency index.
type opRef struct {
	Space string `json:"space"`
	Seq   uint64 `json:"seq"`
}

// Apply applies ops to one space, in order, as the view's user. All of them,
// their derived ops, the changed items and comments, the notifications, the
// opid index entries and the sequence counter are written in one synced batch:
// either everything is stored or nothing is. An op whose ID was applied before
// is skipped, which makes replay idempotent. It returns the ops as logged.
func (v *View) Apply(spaceID string, ops ...core.Op) ([]core.Op, error) {
	s := v.s
	s.mu.RLock()
	sp, err := v.space(spaceID)
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	sp.wmu.Lock()
	defer sp.wmu.Unlock()

	// Under wmu nobody else changes this space, so its record and item map can
	// be read without s.mu. Membership is checked again by core.Apply.
	st := &core.State{Space: sp.rec, Items: sp.items, Pending: map[string]*core.Item{}}
	if sp.rec.Kind == core.SpacePersonal {
		if u := s.User(sp.rec.Members[0]); u != nil {
			st.PersonalContexts = u.PersonalContexts
		}
	}
	var (
		logged   []core.Op
		comments []*core.Comment
		notes    []core.Notification
		seen     = map[string]bool{}
	)
	b := s.db.NewBatch()
	defer b.Close()
	seq := sp.seq
	for _, op := range ops {
		if op.ID == "" {
			op.ID = s.NewID()
		}
		if seen[op.ID] {
			continue
		}
		seen[op.ID] = true
		if _, closer, err := s.db.Get(opidKey(op.ID)); err == nil {
			closer.Close()
			continue
		} else if !errors.Is(err, pebble.ErrNotFound) {
			return nil, err
		}
		op.Space, op.Actor = spaceID, v.user
		if op.TS.IsZero() {
			op.TS = s.Now()
		}
		st.Comment = nil
		if op.Type == core.OpDeleteComment {
			st.Comment = s.targetComment(spaceID, op)
		}
		res, err := core.Apply(st, op, s.NewID)
		if err != nil {
			return nil, fmt.Errorf("op %s (%s): %w", op.ID, op.Type, err)
		}
		for _, it := range res.Items {
			st.Pending[it.ID] = it
		}
		for _, o := range res.Ops {
			seq++
			o.Seq = seq
			b.Set(opKey(spaceID, seq), mustJSON(o), nil)
			b.Set(opidKey(o.ID), mustJSON(opRef{spaceID, seq}), nil)
			logged = append(logged, o)
		}
		comments = append(comments, res.Comments...)
		notes = append(notes, res.Notifications...)
	}
	if len(logged) == 0 {
		return nil, nil
	}
	for _, it := range st.Pending {
		b.Set(itemKey(spaceID, it.ID), mustJSON(it), nil)
	}
	for _, c := range comments {
		b.Set(commentKey(spaceID, c.Item, c.ID), mustJSON(c), nil)
	}
	b.Set(seqKey(spaceID), []byte(be64(seq)), nil)
	s.nmu.Lock()
	for i := range notes {
		s.nseq[notes[i].User]++
		notes[i].Seq = s.nseq[notes[i].User]
		b.Set(notifKey(notes[i].User, notes[i].Seq), mustJSON(notes[i]), nil)
	}
	s.nmu.Unlock()
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, fmt.Errorf("commit %d ops to space %q: %w", len(logged), spaceID, err)
	}

	s.mu.Lock()
	for _, it := range st.Pending {
		sp.items[it.ID] = it
		sp.text[it.ID] = s.searchText(it)
	}
	for _, c := range comments {
		if !c.Deleted {
			sp.comments[c.Item]++
		}
	}
	sp.seq = seq
	s.mu.Unlock()
	if s.OnCommit != nil && len(st.Pending) > 0 {
		changed := make([]*core.Item, 0, len(st.Pending))
		for _, it := range st.Pending {
			changed = append(changed, it)
		}
		s.OnCommit(spaceID, changed)
	}
	return logged, nil
}

// targetComment loads the comment a delete_comment op refers to, or nil.
func (s *Store) targetComment(spaceID string, op core.Op) *core.Comment {
	var p struct{ Comment string }
	if json.Unmarshal(op.Payload, &p) != nil || p.Comment == "" {
		return nil
	}
	c := &core.Comment{}
	if getJSON(s.db, commentKey(spaceID, op.Item, p.Comment), c) != nil {
		return nil
	}
	return c
}

// Capture creates a capture in the user's personal inbox (SPEC §4). The first
// line becomes the title. The full text is kept in `original`, and in `body`
// when there is more than one line.
//
// A capture without text (a photo) gets fallbackTitle, or the name of its
// first attachment.
func (v *View) Capture(text, fallbackTitle string, src core.Source, atts []core.Attachment) (*core.Item, error) {
	text = strings.TrimSpace(text)
	title, _, multi := strings.Cut(text, "\n")
	if title = strings.TrimSpace(title); title == "" {
		title = fallbackTitle
	}
	if title == "" && len(atts) > 0 {
		title = atts[0].Name
	}
	it := core.Item{ID: v.s.NewID(), Kind: core.KindCapture, State: core.StInbox, Title: title,
		Original: text, Source: src, Attachments: atts}
	if multi {
		it.Body = text
	}
	it.Source.By = v.user
	via := src.Via
	if via == "" {
		via = "web"
	}
	if _, err := v.Apply(v.user, core.Op{Via: via, Type: core.OpCreate, Payload: mustJSON(it)}); err != nil {
		return nil, err
	}
	return v.Item(v.user, it.ID)
}
