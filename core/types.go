// Package core holds the domain types and rules of gtd. It is pure: no I/O, no
// clocks, no randomness. Callers pass time and ID generation in.
package core

import (
	"encoding/json"
	"errors"
	"time"
)

// Item kinds.
const (
	KindCapture = "capture"
	KindAction  = "action"
	KindProject = "project"
	KindNote    = "note"
	KindArea    = "area"
)

// Item states.
const (
	StInbox    = "inbox"
	StNext     = "next"
	StPlanned  = "planned"
	StWaiting  = "waiting"
	StSomeday  = "someday"
	StDone     = "done"
	StDropped  = "dropped"
	StTrashed  = "trashed"
	StActive   = "active"
	StUnsorted = "unsorted"
	StFiled    = "filed"
	StArchived = "archived"
	StRetired  = "retired"
)

// Op types.
const (
	OpCreate        = "create"
	OpPatch         = "patch"
	OpTransition    = "transition"
	OpComment       = "comment"
	OpAttach        = "attach"
	OpDeleteComment = "delete_comment"
	OpImport        = "import" // marks the start or the end of an import run; changes nothing
	OpMove          = "move"   // an item leaves this space, or arrives in it (move.go)
)

// Space kinds.
const (
	SpacePersonal = "personal"
	SpaceShared   = "shared"
)

// Notification types created in M0.
const (
	NoteUnblocked      = "unblocked"
	NoteBlockerDropped = "blocker_dropped"
)

// System is the actor of derived ops (dependency rule, tickler).
const System = "system"

// Version is the schema version written into every stored record.
const Version = 1

// Errors returned by Apply. They are wrapped with details; test with errors.Is.
var (
	ErrInvalid  = errors.New("invalid")
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

type Source struct {
	Via string `json:"via,omitempty" yaml:"via,omitempty"`
	Ref string `json:"ref,omitempty" yaml:"ref,omitempty"`
	By  string `json:"by,omitempty" yaml:"by,omitempty"`
}

type Attachment struct {
	SHA256 string `json:"sha256" yaml:"sha256"`
	Name   string `json:"name,omitempty" yaml:"name,omitempty"`
	MIME   string `json:"mime,omitempty" yaml:"mime,omitempty"`
	Size   int64  `json:"size" yaml:"size"`
}

// Item is the stored record of SPEC §2. Its JSON encoding is canonical: zero
// values are omitted, so marshal(unmarshal(x)) == x for every stored item.
type Item struct {
	V           int               `json:"v"`
	ID          string            `json:"id"`
	Space       string            `json:"space"`
	Kind        string            `json:"kind"`
	Title       string            `json:"title,omitempty"`
	State       string            `json:"state"`
	Rev         uint64            `json:"rev,omitempty"`
	Created     time.Time         `json:"created"`
	Updated     time.Time         `json:"updated,omitzero"`
	UpdatedBy   string            `json:"updated_by,omitempty"`
	Original    string            `json:"original,omitempty"`
	Source      Source            `json:"source,omitzero"`
	AIGenerated bool              `json:"ai_generated,omitempty"`
	Area        string            `json:"area,omitempty"`
	Project     string            `json:"project,omitempty"`
	Parent      string            `json:"parent,omitempty"`
	Context     string            `json:"context,omitempty"`
	Owner       string            `json:"owner,omitempty"`
	DelegatedBy string            `json:"delegated_by,omitempty"`
	WaitingOn   string            `json:"waiting_on,omitempty"`
	BlockedBy   []string          `json:"blocked_by,omitempty"`
	DeferUntil  string            `json:"defer_until,omitempty"`
	Target      string            `json:"target,omitempty"`
	Due         string            `json:"due,omitempty"`
	Energy      string            `json:"energy,omitempty"`
	Time        string            `json:"time,omitempty"`
	Repeat      string            `json:"repeat,omitempty"` // actions: 2d, 1w or mon,wed,fri (repeat.go)
	Template    string            `json:"template,omitempty"`
	Fields      map[string]string `json:"fields,omitempty"`
	Attachments []Attachment      `json:"attachments,omitempty"`
	DerivedFrom []string          `json:"derived_from,omitempty"`
	Generated   bool              `json:"generated,omitempty"`
	Conflict    json.RawMessage   `json:"conflict,omitempty"`
	Imported    map[string]any    `json:"imported,omitempty"`
	Body        string            `json:"body,omitempty"`
}

type FieldChange struct {
	Field string          `json:"field"`
	Old   json.RawMessage `json:"old,omitempty"`
	New   json.RawMessage `json:"new,omitempty"`
}

// Op is the only way state changes (STORAGE §3).
type Op struct {
	V       int             `json:"v"`
	ID      string          `json:"id"`
	Space   string          `json:"space"`
	Seq     uint64          `json:"seq"`
	TS      time.Time       `json:"ts"`
	Actor   string          `json:"actor"`
	Via     string          `json:"via"`
	Type    string          `json:"type"`
	Item    string          `json:"item,omitempty"`
	BaseRev uint64          `json:"base_rev,omitempty"`
	Changes []FieldChange   `json:"changes,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type TemplateField struct {
	Key     string            `json:"key" yaml:"key"`
	Type    string            `json:"type" yaml:"type"`
	Options []string          `json:"options,omitempty" yaml:"options,omitempty"`
	Label   map[string]string `json:"label,omitempty" yaml:"label,omitempty"`
	Tickler bool              `json:"tickler,omitempty" yaml:"tickler,omitempty"`
}

type Template struct {
	Key    string            `json:"key" yaml:"key"`
	Kind   string            `json:"kind" yaml:"kind"`
	Label  map[string]string `json:"label,omitempty" yaml:"label,omitempty"`
	Fields []TemplateField   `json:"fields,omitempty" yaml:"fields,omitempty"`
	Body   string            `json:"body,omitempty" yaml:"body,omitempty"`
}

type Space struct {
	V        int      `json:"v" yaml:"-"`
	ID       string   `json:"id" yaml:"id"`
	Name     string   `json:"name" yaml:"name"`
	Kind     string   `json:"kind" yaml:"kind"`
	Members  []string `json:"members" yaml:"members"`
	People   []string `json:"people,omitempty" yaml:"people,omitempty"`
	Contexts []string `json:"contexts,omitempty" yaml:"contexts,omitempty"` // a shared space\'s own contexts, besides the canonical ones
	// AI says which generation backends may read this space's text: "off"
	// (none), "local" (only one that runs on the operator's machines; the
	// default, also when empty) or "any" (also a cloud model).
	AI        string     `json:"ai,omitempty" yaml:"ai,omitempty"`
	Templates []Template `json:"templates,omitempty" yaml:"templates,omitempty"`
	Created   time.Time  `json:"created" yaml:"-"`
}

type User struct {
	V                int      `json:"v"`
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Lang             string   `json:"lang,omitempty"`
	Service          bool     `json:"service,omitempty"`
	PasswordHash     string   `json:"password_hash,omitempty"`
	SSHKeys          []string `json:"ssh_keys,omitempty"`
	PersonalContexts []string `json:"personal_contexts,omitempty"`
	Aliases          []string `json:"aliases,omitempty"` // how the user appears in notes ("Sven", "SE")
	ReviewWeekday    int      `json:"review_weekday,omitempty"`
	PersonalSpace    string   `json:"personal_space"`
}

type Comment struct {
	V           int               `json:"v"`
	ID          string            `json:"id"`
	Item        string            `json:"item"`
	Author      string            `json:"author"`
	TS          time.Time         `json:"ts"`
	Body        string            `json:"body,omitempty"`
	Mentions    []string          `json:"mentions,omitempty"`
	Attachments []Attachment      `json:"attachments,omitempty"`
	AI          bool              `json:"ai,omitempty"`
	Proposals   []json.RawMessage `json:"proposals,omitempty"`
	Deleted     bool              `json:"deleted,omitempty"`
}

// Notification is one entry of a user's "From others" inbox.
type Notification struct {
	V         int       `json:"v"`
	User      string    `json:"user"`
	Seq       uint64    `json:"seq"`
	Type      string    `json:"type"`
	Space     string    `json:"space"`
	Item      string    `json:"item,omitempty"`
	Comment   string    `json:"comment,omitempty"`
	From      string    `json:"from,omitempty"`
	TS        time.Time `json:"ts"`
	Dismissed bool      `json:"dismissed,omitempty"`
}
