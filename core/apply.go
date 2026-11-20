package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// State is what Apply needs to know about one space. Items is the committed
// state and is never modified. Pending holds items changed by earlier ops of the
// same batch and overrides Items.
type State struct {
	Space   *Space
	Items   map[string]*Item
	Pending map[string]*Item
	// PersonalContexts are the extra context keys of the owner of a personal space.
	PersonalContexts []string
	// Comment is the existing comment a delete_comment op targets, loaded by the caller.
	Comment *Comment
}

// Item returns the current version of an item, or nil.
func (s *State) Item(id string) *Item {
	if it, ok := s.Pending[id]; ok {
		return it
	}
	return s.Items[id]
}

// Each calls f for the current version of every item.
func (s *State) Each(f func(*Item)) {
	for id, it := range s.Items {
		if p, ok := s.Pending[id]; ok {
			it = p
		}
		if it != nil { // nil in Pending: the item left the space in this batch
			f(it)
		}
	}
	for id, it := range s.Pending {
		if _, ok := s.Items[id]; !ok && it != nil {
			f(it)
		}
	}
}

// Result is everything one op changes. The caller writes all of it in one batch.
type Result struct {
	Removed       []string       // items that left the space (move)
	Items         []*Item        // new versions of changed items
	Comments      []*Comment     // new or tombstoned comments
	Ops           []Op           // the op as logged, followed by derived system ops
	Notifications []Notification // Seq is assigned by the caller
}

// Apply validates op against st and returns its effects, including the ops and
// notifications derived by the dependency rule (SPEC §6.2). It modifies neither
// st nor any item in it. op.ID, op.TS and op.Actor must be set by the caller.
func Apply(st *State, op Op, newID func() string) (*Result, error) {
	if op.ID == "" || op.TS.IsZero() {
		return nil, fmt.Errorf("%w: op needs id and ts", ErrInvalid)
	}
	if op.Space != st.Space.ID {
		return nil, fmt.Errorf("%w: op %s is for space %q, state is space %q", ErrInvalid, op.ID, op.Space, st.Space.ID)
	}
	if !CanAct(st.Space, op.Actor) {
		return nil, fmt.Errorf("%w: space %q", ErrNotFound, op.Space)
	}
	op.V = Version
	op.Seq = 0
	ws := &State{Space: st.Space, Items: st.Items, Pending: maps.Clone(st.Pending),
		PersonalContexts: st.PersonalContexts, Comment: st.Comment}
	if ws.Pending == nil {
		ws.Pending = map[string]*Item{}
	}
	res := &Result{}

	if op.Type == OpComment || op.Type == OpDeleteComment {
		if err := prepareComment(ws, &op, newID); err != nil {
			return nil, err
		}
		c, err := replayComment(ws.Comment, op)
		if err != nil {
			return nil, err
		}
		res.Comments = []*Comment{c}
		res.Ops = []Op{op}
		return res, nil
	}

	if op.Type == OpMove {
		return applyMove(ws, op, res)
	}
	if op.Type == OpImport {
		// A marker in the log (IMPORT.md §1). It touches no item.
		var m ImportMarker
		if err := strict(op.Payload, &m); err != nil || (m.Phase != "begin" && m.Phase != "end") || m.Source == "" || op.Item != "" || len(op.Changes) > 0 {
			return nil, fmt.Errorf("%w: import marker needs a source and phase begin or end (%v)", ErrInvalid, err)
		}
		res.Ops = []Op{op}
		return res, nil
	}

	before := ws.Item(op.Item)
	var err error
	switch op.Type {
	case OpCreate:
		err = prepareCreate(ws, &op)
	case OpPatch:
		err = preparePatch(before, &op)
	case OpTransition:
		err = prepareTransition(ws, before, &op)
	case OpAttach:
		err = prepareAttach(before, &op)
	default:
		err = fmt.Errorf("%w: unknown op type %q", ErrInvalid, op.Type)
	}
	if err != nil {
		return nil, err
	}
	if op.Type == OpPatch {
		delegatePatch(ws, before, &op)
	}
	waitRule(before, &op)
	after, err := replayItem(before, op)
	if err != nil {
		return nil, err
	}
	// A template date field marked as tickler sets defer_until (SPEC §7). The
	// change is added to the op, so the log still explains the whole state.
	if d := ticklerDate(ws.Space, after); op.Type == OpPatch && d != "" && d != after.DeferUntil &&
		slices.ContainsFunc(op.Changes, func(c FieldChange) bool { return c.Field == "fields" || c.Field == "template" }) &&
		!slices.ContainsFunc(op.Changes, func(c FieldChange) bool { return c.Field == "defer_until" }) {
		op.Changes = append(op.Changes, FieldChange{Field: "defer_until", Old: mustJSON(before.DeferUntil), New: mustJSON(d)})
		if before.DeferUntil == "" {
			op.Changes[len(op.Changes)-1].Old = nil
		}
		if after, err = replayItem(before, op); err != nil {
			return nil, err
		}
	}
	ws.Pending[after.ID] = after
	if err := validate(ws, after); err != nil {
		return nil, err
	}
	res.Ops = []Op{op}
	notifyDelegation(ws, res, before, after, op)
	touched := []string{after.ID}
	touched = append(touched, settle(ws, op, before, after, newID, res)...)
	if id := repeatRule(ws, op, before, after, newID, res); id != "" {
		touched = append(touched, id)
	}
	for _, id := range touched {
		res.Items = append(res.Items, ws.Pending[id])
	}
	return res, nil
}

// ImportMarker is the payload of an import op.
type ImportMarker struct {
	Source string `json:"source"` // "obsidian"
	Phase  string `json:"phase"`  // begin | end
	Notes  int    `json:"notes,omitempty"`
}

// Replay applies the recorded effect of a logged op to items and comments,
// without validation and without rules. Replaying a space's op log in sequence
// order from empty maps reproduces its stored items and comments exactly.
func Replay(items map[string]*Item, comments map[string]*Comment, op Op) error {
	switch op.Type {
	case OpMove:
		return replayMove(items, comments, op)
	case OpImport:
	case OpComment:
		c, err := replayComment(nil, op)
		if err != nil {
			return err
		}
		comments[c.ID] = c
	case OpDeleteComment:
		var p struct{ Comment string }
		if err := json.Unmarshal(op.Payload, &p); err != nil {
			return fmt.Errorf("op %s: %v", op.ID, err)
		}
		c, err := replayComment(comments[p.Comment], op)
		if err != nil {
			return err
		}
		comments[c.ID] = c
	default:
		it, err := replayItem(items[op.Item], op)
		if err != nil {
			return err
		}
		items[it.ID] = it
	}
	return nil
}

// replayItem returns the item as it is after op. cur is nil for create.
func replayItem(cur *Item, op Op) (*Item, error) {
	var it *Item
	switch op.Type {
	case OpCreate:
		it = &Item{}
		if err := strict(op.Payload, it); err != nil {
			return nil, fmt.Errorf("%w: op %s create payload: %v", ErrInvalid, op.ID, err)
		}
	case OpPatch, OpTransition:
		if cur == nil {
			return nil, fmt.Errorf("%w: item %s", ErrNotFound, op.Item)
		}
		var err error
		if it, err = setFields(cur, op.Changes); err != nil {
			return nil, err
		}
	case OpAttach:
		if cur == nil {
			return nil, fmt.Errorf("%w: item %s", ErrNotFound, op.Item)
		}
		var a Attachment
		if err := strict(op.Payload, &a); err != nil {
			return nil, fmt.Errorf("%w: op %s attach payload: %v", ErrInvalid, op.ID, err)
		}
		n := *cur
		n.Attachments = append(slices.Clone(cur.Attachments), a)
		it = &n
	default:
		return nil, fmt.Errorf("%w: op %s has unknown type %q", ErrInvalid, op.ID, op.Type)
	}
	if cur != nil {
		it.Rev = cur.Rev
	}
	it.Rev++
	it.Updated = op.TS
	it.UpdatedBy = op.Actor
	return it, nil
}

func prepareCreate(ws *State, op *Op) error {
	var it Item
	if err := strict(op.Payload, &it); err != nil {
		return fmt.Errorf("%w: create payload: %v", ErrInvalid, err)
	}
	if it.ID == "" || strings.Contains(it.ID, "/") {
		return fmt.Errorf("%w: create needs an item id", ErrInvalid)
	}
	if op.Item != "" && op.Item != it.ID {
		return fmt.Errorf("%w: op item %s differs from payload id %s", ErrInvalid, op.Item, it.ID)
	}
	if ws.Item(it.ID) != nil {
		return fmt.Errorf("%w: item %s already exists", ErrConflict, it.ID)
	}
	states, ok := initial[it.Kind]
	if !ok {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalid, it.Kind)
	}
	if it.State == "" {
		it.State = states[0]
	}
	if !slices.Contains(states, it.State) {
		return fmt.Errorf("%w: a %s cannot be created in state %q", ErrInvalid, it.Kind, it.State)
	}
	it.V = Version
	it.Space = op.Space
	it.Rev = 0
	it.Updated = time.Time{}
	it.UpdatedBy = ""
	if it.Created.IsZero() {
		it.Created = op.TS
	}
	if o := defaultOwner(ws.Space, it.Kind, it.Owner); o != "" {
		it.Owner = o
	}
	// An action created for another member is delegated by its creator.
	it.DelegatedBy = ""
	if delegates(ws, op.Actor, it.Kind, it.Owner) {
		it.DelegatedBy = op.Actor
	}
	if d := ticklerDate(ws.Space, &it); d != "" {
		it.DeferUntil = d
	}
	op.Item = it.ID
	op.BaseRev = 0
	op.Changes = nil
	op.Payload = mustJSON(it)
	return nil
}

// patchable lists the fields a patch op may change. id, space, kind, state,
// created, original, source and the rev/updated bookkeeping are not among them.
var patchable = map[string]bool{
	"title": true, "body": true, "area": true, "project": true, "parent": true,
	"context": true, "owner": true, "delegated_by": true, "waiting_on": true,
	"blocked_by": true, "defer_until": true, "target": true, "due": true,
	"energy": true, "time": true, "repeat": true, "template": true, "fields": true,
	"derived_from": true, "generated": true, "conflict": true, "imported": true,
}

func preparePatch(cur *Item, op *Op) error {
	if cur == nil {
		return fmt.Errorf("%w: item %s", ErrNotFound, op.Item)
	}
	if len(op.Changes) == 0 {
		return fmt.Errorf("%w: patch without changes", ErrInvalid)
	}
	for _, c := range op.Changes {
		if !patchable[c.Field] {
			return fmt.Errorf("%w: field %q cannot be patched", ErrInvalid, c.Field)
		}
	}
	op.Payload = nil
	return checkBase(cur, op)
}

func prepareTransition(ws *State, cur *Item, op *Op) error {
	if cur == nil {
		return fmt.Errorf("%w: item %s", ErrNotFound, op.Item)
	}
	kind, state := cur.Kind, ""
	for _, c := range op.Changes {
		var s string
		if err := json.Unmarshal(c.New, &s); err != nil || s == "" {
			return fmt.Errorf("%w: transition field %q needs a string value", ErrInvalid, c.Field)
		}
		switch c.Field {
		case "state":
			state = s
		case "kind":
			kind = s
		default:
			return fmt.Errorf("%w: a transition changes state and kind only, not %q", ErrInvalid, c.Field)
		}
	}
	if state == "" {
		return fmt.Errorf("%w: transition without state", ErrInvalid)
	}
	op.Payload = nil
	if err := checkBase(cur, op); err != nil {
		return err
	}
	if kind != cur.Kind {
		if cur.Kind != KindCapture || cur.State != StInbox || !CanClarify(kind, state) {
			return fmt.Errorf("%w: item %s: %s/%s cannot become %s/%s", ErrConflict, cur.ID, cur.Kind, cur.State, kind, state)
		}
		if o := defaultOwner(ws.Space, kind, cur.Owner); o != "" {
			op.Changes = append(op.Changes, FieldChange{Field: "owner", New: mustJSON(o)})
		}
		return nil
	}
	if !CanTransition(kind, cur.State, state) {
		return fmt.Errorf("%w: item %s: %s cannot go from %s to %s", ErrConflict, cur.ID, kind, cur.State, state)
	}
	return nil
}

func prepareAttach(cur *Item, op *Op) error {
	if cur == nil {
		return fmt.Errorf("%w: item %s", ErrNotFound, op.Item)
	}
	var a Attachment
	if err := strict(op.Payload, &a); err != nil {
		return fmt.Errorf("%w: attach payload: %v", ErrInvalid, err)
	}
	if !ValidSHA256(a.SHA256) || a.Size < 0 {
		return fmt.Errorf("%w: attachment needs a sha256 and a size", ErrInvalid)
	}
	for _, x := range cur.Attachments {
		if x.SHA256 == a.SHA256 {
			return fmt.Errorf("%w: item %s already has attachment %s", ErrConflict, cur.ID, a.SHA256)
		}
	}
	op.Changes = nil
	op.Payload = mustJSON(a)
	return nil
}

// checkBase detects conflicting edits and normalizes op.Changes. An op applies
// if it is based on the current rev, or if every field it changes still has the
// value the client saw (field-level check, OFFLINE §3). Old is then set to the
// actual current value, which is what undo needs.
func checkBase(cur *Item, op *Op) error {
	m := fieldMap(cur)
	seen := map[string]bool{}
	for i, c := range op.Changes {
		if seen[c.Field] {
			return fmt.Errorf("%w: field %q changed twice in one op", ErrInvalid, c.Field)
		}
		seen[c.Field] = true
		old, err := canon(m[c.Field])
		if err != nil {
			return err
		}
		if op.BaseRev != cur.Rev {
			given, err := canon(c.Old)
			if err != nil {
				return err
			}
			if !bytes.Equal(given, old) {
				return fmt.Errorf("%w: item %s is at rev %d, op is based on rev %d, and field %q changed meanwhile",
					ErrConflict, cur.ID, cur.Rev, op.BaseRev, c.Field)
			}
		}
		nw, err := canon(c.New)
		if err != nil {
			return err
		}
		op.Changes[i].Old, op.Changes[i].New = old, nw
	}
	return nil
}

func prepareComment(ws *State, op *Op, newID func() string) error {
	if ws.Item(op.Item) == nil {
		return fmt.Errorf("%w: item %s", ErrNotFound, op.Item)
	}
	op.Changes = nil
	if op.Type == OpDeleteComment {
		var p struct {
			Comment string `json:"comment"`
		}
		if err := strict(op.Payload, &p); err != nil {
			return fmt.Errorf("%w: delete_comment payload: %v", ErrInvalid, err)
		}
		c := ws.Comment
		if c == nil || c.ID != p.Comment || c.Item != op.Item {
			return fmt.Errorf("%w: comment %s on item %s", ErrNotFound, p.Comment, op.Item)
		}
		if c.Deleted {
			return fmt.Errorf("%w: comment %s is already deleted", ErrConflict, c.ID)
		}
		if c.Author != op.Actor {
			return fmt.Errorf("%w: comment %s can only be deleted by its author %s", ErrInvalid, c.ID, c.Author)
		}
		op.Payload = mustJSON(p)
		return nil
	}
	var c Comment
	if err := strict(op.Payload, &c); err != nil {
		return fmt.Errorf("%w: comment payload: %v", ErrInvalid, err)
	}
	if c.ID == "" {
		c.ID = newID()
	}
	if strings.TrimSpace(c.Body) == "" && len(c.Attachments) == 0 {
		return fmt.Errorf("%w: empty comment", ErrInvalid)
	}
	for _, m := range c.Mentions {
		if !ws.Space.HasMember(m) {
			return fmt.Errorf("%w: mentioned user %q is not a member of space %q", ErrInvalid, m, ws.Space.ID)
		}
	}
	c.V, c.Item, c.Author, c.TS, c.Deleted = Version, op.Item, op.Actor, op.TS, false
	op.Payload = mustJSON(c)
	ws.Comment = nil
	return nil
}

func replayComment(cur *Comment, op Op) (*Comment, error) {
	if op.Type == OpComment {
		var c Comment
		if err := strict(op.Payload, &c); err != nil {
			return nil, fmt.Errorf("%w: op %s comment payload: %v", ErrInvalid, op.ID, err)
		}
		return &c, nil
	}
	if cur == nil {
		return nil, fmt.Errorf("%w: op %s deletes a comment that does not exist", ErrNotFound, op.ID)
	}
	// The tombstone keeps who wrote the comment and when, nothing else.
	return &Comment{V: cur.V, ID: cur.ID, Item: cur.Item, Author: cur.Author, TS: cur.TS, Deleted: true}, nil
}

// defaultOwner returns the owner to set when an action in a personal space has
// none: there it is always the space's user (SPEC §1).
func defaultOwner(sp *Space, kind, owner string) string {
	if sp.Kind == SpacePersonal && kind == KindAction && owner == "" && len(sp.Members) > 0 {
		return sp.Members[0]
	}
	return ""
}

// settle enforces the dependency rule after the primary op: an open action with
// an open blocker is `planned`, and a `planned` action without one is `next`.
// Each correction is its own system op. It returns the IDs of the items it changed.
func settle(ws *State, op Op, before, after *Item, newID func() string, res *Result) []string {
	if after.Kind != KindAction {
		return nil
	}
	check := []string{after.ID}
	wasResolved := before != nil && before.Kind == KindAction && resolved(before.State)
	stateChanged := before != nil && before.State != after.State
	var deps []string
	if stateChanged && wasResolved != resolved(after.State) {
		deps = dependents(ws, after.ID)
		check = append(check, deps...)
	}
	var changed []string
	for _, id := range check {
		it := ws.Item(id)
		open := hasOpenBlocker(ws, it)
		to := ""
		switch {
		case open && (it.State == StNext || it.State == StWaiting || it.State == StSomeday):
			to = StPlanned
		case !open && it.State == StPlanned:
			to = StNext
		}
		if to == "" {
			continue
		}
		d := Op{V: Version, ID: newID(), Space: op.Space, TS: op.TS, Actor: System, Via: System,
			Type: OpTransition, Item: id, BaseRev: it.Rev,
			Changes: []FieldChange{{Field: "state", Old: mustJSON(it.State), New: mustJSON(to)}}}
		n, err := replayItem(it, d)
		if err != nil {
			panic(err) // a state change on an existing item cannot fail
		}
		ws.Pending[id] = n
		res.Ops = append(res.Ops, d)
		if id != after.ID {
			changed = append(changed, id)
			if to == StNext {
				notify(ws, res, NoteUnblocked, n, op)
			}
		}
	}
	if stateChanged && after.State == StDropped {
		for _, id := range deps {
			if it := ws.Item(id); !resolved(it.State) {
				notify(ws, res, NoteBlockerDropped, it, op)
			}
		}
	}
	return changed
}

// notify adds a notification about item for its owner, or for every member if
// it is unassigned.
func notify(ws *State, res *Result, typ string, it *Item, op Op) {
	to := []string{it.Owner}
	if it.Owner == "" {
		to = slices.Sorted(slices.Values(ws.Space.Members))
	}
	for _, u := range to {
		res.Notifications = append(res.Notifications, Notification{V: Version, User: u, Type: typ,
			Space: op.Space, Item: it.ID, From: op.Actor, TS: op.TS})
	}
}

func hasOpenBlocker(ws *State, it *Item) bool {
	for _, b := range it.BlockedBy {
		if x := ws.Item(b); x != nil && !resolved(x.State) {
			return true
		}
	}
	return false
}

// dependents returns the IDs of the actions blocked by id, sorted.
func dependents(ws *State, id string) []string {
	var out []string
	ws.Each(func(it *Item) {
		if slices.Contains(it.BlockedBy, id) {
			out = append(out, it.ID)
		}
	})
	sort.Strings(out)
	return out
}

var canonicalContexts = []string{"errands", "calls", "computer", "home", "office", "anywhere"}

// CleanContexts checks a user's or a space's own contexts and returns them
// tidied. They are names, shown as written ("Calls US"): letters, digits,
// spaces and - _ . + & /, at most 30 characters. A name may not repeat a
// canonical context or another name, whatever the capitals, and may not use
// the "agenda:" form, which belongs to people.
func CleanContexts(in []string) ([]string, error) {
	out := []string{}
	for _, c := range in {
		c = strings.Join(strings.Fields(c), " ")
		if c == "" {
			continue
		}
		if len([]rune(c)) > 30 {
			return nil, fmt.Errorf("%w: context %q is longer than 30 characters", ErrInvalid, c)
		}
		for _, r := range c {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(" -_.+&/", r) {
				return nil, fmt.Errorf("%w: context %q: %q is not allowed (letters, digits, spaces and - _ . + & /)", ErrInvalid, c, r)
			}
		}
		same := func(x string) bool { return strings.EqualFold(x, c) }
		if slices.ContainsFunc(canonicalContexts, same) {
			return nil, fmt.Errorf("%w: context %q exists already: it is one of the built-in ones", ErrInvalid, c)
		}
		if !slices.ContainsFunc(out, same) {
			out = append(out, c)
		}
	}
	return out, nil
}

func validContext(ws *State, key string) bool {
	if slices.Contains(canonicalContexts, key) {
		return true
	}
	if who, ok := strings.CutPrefix(key, "agenda:"); ok {
		return ws.Space.HasMember(who) || who != "" && slices.Contains(ws.Space.People, who)
	}
	if ws.Space.Kind == SpacePersonal {
		return slices.Contains(ws.PersonalContexts, key)
	}
	return slices.Contains(ws.Space.Contexts, key)
}

// validate checks every invariant of one item against the space.
func validate(ws *State, it *Item) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: item %s: %s", ErrInvalid, it.ID, fmt.Sprintf(format, a...))
	}
	if !validState(it.Kind, it.State) {
		return bad("a %s has no state %q", it.Kind, it.State)
	}
	if strings.TrimSpace(it.Title) == "" {
		return bad("empty title")
	}
	ref := func(field, id, kind string, allowed ...string) error {
		if id == "" {
			return nil
		}
		if !slices.Contains(allowed, it.Kind) {
			return bad("a %s has no %s", it.Kind, field)
		}
		x := ws.Item(id)
		if x == nil || x.Kind != kind {
			return bad("%s %s is not a %s in space %q", field, id, kind, it.Space)
		}
		return nil
	}
	if err := ref("area", it.Area, KindArea, KindAction, KindProject, KindNote); err != nil {
		return err
	}
	if err := ref("project", it.Project, KindProject, KindAction, KindNote); err != nil {
		return err
	}
	if err := ref("parent", it.Parent, KindProject, KindProject); err != nil {
		return err
	}
	if it.Parent != "" && reaches(ws, []string{it.Parent}, it.ID, func(x *Item) []string { return []string{x.Parent} }) {
		return bad("parent %s would create a cycle", it.Parent)
	}
	for i, b := range it.BlockedBy {
		if err := ref("blocked_by", b, KindAction, KindAction); err != nil {
			return err
		}
		if slices.Contains(it.BlockedBy[:i], b) {
			return bad("blocked_by lists %s twice", b)
		}
	}
	if reaches(ws, it.BlockedBy, it.ID, func(x *Item) []string { return x.BlockedBy }) {
		return bad("blocked_by would create a dependency cycle")
	}
	if it.Context != "" && (it.Kind != KindAction || !validContext(ws, it.Context)) {
		return bad("context %q is not allowed here", it.Context)
	}
	for field, u := range map[string]string{"owner": it.Owner, "delegated_by": it.DelegatedBy} {
		if u != "" && !ws.Space.HasMember(u) {
			return bad("%s %q is not a member of space %q", field, u, it.Space)
		}
	}
	if ws.Space.Kind == SpacePersonal && it.Kind == KindAction && it.Owner != ws.Space.Members[0] {
		return bad("actions in a personal space are owned by %q", ws.Space.Members[0])
	}
	for field, d := range map[string]string{"defer_until": it.DeferUntil, "target": it.Target, "due": it.Due} {
		if d == "" {
			continue
		}
		if _, err := time.Parse(time.DateOnly, d); err != nil {
			return bad("%s %q is not a YYYY-MM-DD date", field, d)
		}
	}
	if !slices.Contains([]string{"", "low", "med", "high"}, it.Energy) {
		return bad("energy %q", it.Energy)
	}
	if !slices.Contains([]string{"", "5m", "15m", "30m", "1h", "2h+"}, it.Time) {
		return bad("time %q", it.Time)
	}
	if it.Repeat != "" {
		if canonical, err := ParseRepeat(it.Repeat); err != nil || canonical != it.Repeat || it.Kind != KindAction {
			return bad("repeat %q (only actions repeat; want e.g. 2d, 1w or mon,wed,fri)", it.Repeat)
		}
	}
	for _, a := range it.Attachments {
		if !ValidSHA256(a.SHA256) {
			return bad("attachment sha256 %q", a.SHA256)
		}
	}
	return validateTemplate(ws, it)
}

// reaches reports whether target can be reached from the start IDs by following next.
func reaches(ws *State, start []string, target string, next func(*Item) []string) bool {
	seen := map[string]bool{}
	stack := slices.Clone(start)
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id == "" || seen[id] {
			continue
		}
		if id == target {
			return true
		}
		seen[id] = true
		if x := ws.Item(id); x != nil {
			stack = append(stack, next(x)...)
		}
	}
	return false
}

// ValidSHA256 reports whether s is a lowercase hex sha256.
func ValidSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func fieldMap(it *Item) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(mustJSON(it), &m); err != nil {
		panic(err)
	}
	return m
}

// setFields returns a copy of it with the changed fields set. A nil New clears
// the field.
func setFields(it *Item, changes []FieldChange) (*Item, error) {
	m := fieldMap(it)
	for _, c := range changes {
		if len(c.New) == 0 {
			delete(m, c.Field)
		} else {
			m[c.Field] = c.New
		}
	}
	var out Item
	if err := strict(mustJSON(m), &out); err != nil {
		return nil, fmt.Errorf("%w: item %s: %v", ErrInvalid, it.ID, err)
	}
	return &out, nil
}

// canon returns the canonical JSON of a field value, or nil for a zero value
// (absent, null, "", false, 0, [] or {}).
func canon(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("%w: field value: %v", ErrInvalid, err)
	}
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		if x == "" {
			return nil, nil
		}
	case bool:
		if !x {
			return nil, nil
		}
	case float64:
		if x == 0 {
			return nil, nil
		}
	case []any:
		if len(x) == 0 {
			return nil, nil
		}
	case map[string]any:
		if len(x) == 0 {
			return nil, nil
		}
	}
	return mustJSON(v), nil
}

func strict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// Contexts returns the context keys allowed in the space, in display order:
// the canonical keys, the owner's personal keys (personal space only), and one
// agenda key per other member and per person (D-06).
func Contexts(st *State, user string) []string {
	out := slices.Clone(canonicalContexts)
	if st.Space.Kind == SpacePersonal {
		out = append(out, st.PersonalContexts...)
	} else {
		out = append(out, st.Space.Contexts...)
	}
	for _, m := range st.Space.Members {
		if m != user {
			out = append(out, "agenda:"+m)
		}
	}
	for _, p := range st.Space.People {
		out = append(out, "agenda:"+p)
	}
	return out
}

// Transitions returns the states a user may move the item to.
func Transitions(it *Item) []string {
	return slices.Clone(transitions[it.Kind][it.State])
}

// template returns the space's template with the key and kind, or nil.
func template(sp *Space, key, kind string) *Template {
	for i := range sp.Templates {
		if sp.Templates[i].Key == key && sp.Templates[i].Kind == kind {
			return &sp.Templates[i]
		}
	}
	return nil
}

// validateTemplate checks the item's template and the values of its fields.
// Keys the template does not define are preserved and not checked (SPEC §2).
func validateTemplate(ws *State, it *Item) error {
	if it.Template == "" {
		return nil
	}
	tpl := template(ws.Space, it.Template, it.Kind)
	if tpl == nil {
		return fmt.Errorf("%w: item %s: space %q has no %s template %q", ErrInvalid, it.ID, it.Space, it.Kind, it.Template)
	}
	for _, f := range tpl.Fields {
		v := it.Fields[f.Key]
		if v == "" {
			continue
		}
		ok := true
		switch f.Type {
		case "number":
			_, err := strconv.ParseFloat(v, 64)
			ok = err == nil
		case "date":
			_, err := time.Parse(time.DateOnly, v)
			ok = err == nil
		case "enum":
			ok = slices.Contains(f.Options, v)
		}
		if !ok {
			return fmt.Errorf("%w: item %s: field %q: %q is not a valid %s", ErrInvalid, it.ID, f.Key, v, f.Type)
		}
	}
	return nil
}

// ticklerDate returns the value of the item's first template date field marked
// `tickler: true`, or "".
func ticklerDate(sp *Space, it *Item) string {
	if tpl := template(sp, it.Template, it.Kind); tpl != nil {
		for _, f := range tpl.Fields {
			if f.Type == "date" && f.Tickler && it.Fields[f.Key] != "" {
				return it.Fields[f.Key]
			}
		}
	}
	return ""
}

// ValidateTemplates checks the templates of a space record before it is stored.
func ValidateTemplates(tpls []Template) error {
	seen := map[string]bool{}
	for _, t := range tpls {
		if t.Key == "" || seen[t.Kind+"/"+t.Key] {
			return fmt.Errorf("%w: template key %q is empty or used twice for kind %q", ErrInvalid, t.Key, t.Kind)
		}
		seen[t.Kind+"/"+t.Key] = true
		if !slices.Contains([]string{KindProject, KindNote, KindAction}, t.Kind) {
			return fmt.Errorf("%w: template %q: kind %q (want project, note or action)", ErrInvalid, t.Key, t.Kind)
		}
		keys := map[string]bool{}
		for _, f := range t.Fields {
			if f.Key == "" || keys[f.Key] {
				return fmt.Errorf("%w: template %q: field key %q is empty or used twice", ErrInvalid, t.Key, f.Key)
			}
			keys[f.Key] = true
			if !slices.Contains([]string{"text", "number", "date", "enum"}, f.Type) {
				return fmt.Errorf("%w: template %q: field %q has type %q (want text, number, date or enum)", ErrInvalid, t.Key, f.Key, f.Type)
			}
			if f.Type == "enum" && len(f.Options) == 0 {
				return fmt.Errorf("%w: template %q: enum field %q has no options", ErrInvalid, t.Key, f.Key)
			}
			if f.Tickler && f.Type != "date" {
				return fmt.Errorf("%w: template %q: field %q: tickler needs type date", ErrInvalid, t.Key, f.Key)
			}
		}
	}
	return nil
}
