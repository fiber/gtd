// Package store persists gtd's state in Pebble and keeps the in-memory index.
// Content is reachable only through a View, which checks space membership on
// every call. The unscoped paths are user and space administration, export,
// restore and snapshots.
package store

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/oklog/ulid/v2"

	"github.com/fiber/gtd/core"
)

var (
	// ErrLocked means another process (the daemon) has the database open.
	ErrLocked = errors.New("database is locked by another process")
	// ErrNoDB means the data directory holds no database yet.
	ErrNoDB = errors.New("no database")
)

// Server identifies one gtd server (m/server).
type Server struct {
	V       int       `json:"v"`
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
}

type Store struct {
	dir    string // data directory: db/, blobs/, snapshots/
	db     *pebble.DB
	server Server

	// Now and NewID are replaced by tests that need deterministic output.
	Now   func() time.Time
	NewID func() string

	// OnCommit, if set, is called after every committed batch with the items it
	// changed. The embedding service uses it to keep vectors current. It runs
	// outside the store's locks and must not block.
	OnCommit func(space string, items []*core.Item)

	pdfMu sync.RWMutex
	pdf   map[string]string // extracted PDF text by blob hash, for search

	admin sync.Mutex // serializes user and space administration

	mu     sync.RWMutex // guards users, spaces and everything inside a space except wmu
	users  map[string]*core.User
	spaces map[string]*space

	nmu  sync.Mutex // guards nseq
	nseq map[string]uint64
}

// space is the in-memory index of one space. Items are immutable once stored
// here: a change replaces the pointer.
type space struct {
	wmu      sync.Mutex // serializes Apply and commit for this space
	rec      *core.Space
	seq      uint64
	items    map[string]*core.Item
	text     map[string]string // lowercase search corpus per item
	comments map[string]int    // comment count per item
}

type quietLogger struct{}

func (quietLogger) Infof(string, ...any) {}
func (quietLogger) Errorf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "pebble: "+format+"\n", args...)
}
func (quietLogger) Fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "pebble: "+format+"\n", args...)
	os.Exit(2)
}

func dbDir(dir string) string { return filepath.Join(dir, "db") }

// Create initializes a new database in dir. It fails if one exists.
func Create(dir, serverName string) (*Store, error) {
	return create(dir, serverName, nil)
}

// create lets tests install a fixed clock and ID source before anything is written.
func create(dir, serverName string, setup func(*Store)) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	db, err := pebble.Open(dbDir(dir), &pebble.Options{ErrorIfExists: true, Logger: quietLogger{}})
	if err != nil {
		return nil, fmt.Errorf("create database %s: %w", dbDir(dir), err)
	}
	s := newStore(dir, db)
	if setup != nil {
		setup(s)
	}
	s.server = Server{V: core.Version, ID: s.NewID(), Name: serverName, Created: s.Now()}
	b := db.NewBatch()
	b.Set([]byte(keySchema), []byte(fmt.Sprintf(`{"v":%d}`, core.Version)), nil)
	b.Set([]byte(keyServer), mustJSON(s.server), nil)
	if err := b.Commit(pebble.Sync); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Open opens the database in dir and builds the index. It returns ErrNoDB if
// there is none and ErrLocked if another process holds it.
func Open(dir string) (*Store, error) {
	return open(dir, dbDir(dir), false)
}

// OpenSnapshot opens the checkpoint of a snapshot read-only. Blobs are read
// from the live blob directory, where they are immutable.
func OpenSnapshot(dir, name string) (*Store, error) {
	return open(dir, filepath.Join(snapshotDir(dir, name), "db"), true)
}

func open(dir, dbPath string, readOnly bool) (*Store, error) {
	db, err := pebble.Open(dbPath, &pebble.Options{ErrorIfNotExists: true, ReadOnly: readOnly, Logger: quietLogger{}})
	switch {
	case errors.Is(err, pebble.ErrDBDoesNotExist) || errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("%w in %s", ErrNoDB, dbPath)
	case errors.Is(err, syscall.EAGAIN) || err != nil && strings.Contains(err.Error(), "lock"):
		return nil, fmt.Errorf("%w: %s", ErrLocked, dbPath)
	case err != nil:
		return nil, fmt.Errorf("open database %s: %w", dbPath, err)
	}
	s := newStore(dir, db)
	if err := s.load(); err != nil {
		db.Close()
		return nil, fmt.Errorf("load %s: %w", dbPath, err)
	}
	return s, nil
}

func newStore(dir string, db *pebble.DB) *Store {
	return &Store{dir: dir, db: db,
		Now:   time.Now,
		NewID: func() string { return ulid.Make().String() },
		users: map[string]*core.User{}, spaces: map[string]*space{}, nseq: map[string]uint64{}, pdf: map[string]string{}}
}

func (s *Store) Close() error { return s.db.Close() }

// Dir returns the data directory.
func (s *Store) Dir() string { return s.dir }

// Server returns the server record.
func (s *Store) Server() Server { return s.server }

// load reads users, spaces, items and comment counts into memory.
func (s *Store) load() error {
	if err := checkSchema(s.db); err != nil {
		return err
	}
	if err := getJSON(s.db, []byte(keyServer), &s.server); err != nil {
		return err
	}
	err := scan(s.db, "u/", func(key string, v []byte) error {
		user, rest, sub := strings.Cut(key[len("u/"):], "/")
		switch {
		case !sub:
			u := &core.User{}
			if err := json.Unmarshal(v, u); err != nil {
				return fmt.Errorf("key %q: %v", key, err)
			}
			s.users[u.ID] = u
		case strings.HasPrefix(rest, "n/") && len(rest) == 10:
			s.nseq[user] = binary.BigEndian.Uint64([]byte(rest[2:]))
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Extracted PDF text is part of what search scans.
	if err := scan(s.db, "x/pdf/", func(key string, v []byte) error {
		s.pdf[key[len("x/pdf/"):]] = string(v)
		return nil
	}); err != nil {
		return err
	}
	ids, err := spaceIDs(s.db)
	if err != nil {
		return err
	}
	for _, id := range ids {
		sp := &space{rec: &core.Space{}, items: map[string]*core.Item{}, text: map[string]string{}, comments: map[string]int{}}
		if err := getJSON(s.db, spaceKey(id), sp.rec); err != nil {
			return err
		}
		if sp.seq, err = readSeq(s.db, id); err != nil {
			return err
		}
		err := scan(s.db, itemPrefix(id), func(key string, v []byte) error {
			it := &core.Item{}
			if err := json.Unmarshal(v, it); err != nil {
				return fmt.Errorf("key %q: %v", key, err)
			}
			sp.items[it.ID] = it
			sp.text[it.ID] = s.searchText(it)
			return nil
		})
		if err != nil {
			return err
		}
		// Comments: keys only. The value is not read.
		p := commentPrefix(id)
		it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte(p), UpperBound: prefixEnd(p)})
		if err != nil {
			return err
		}
		for it.First(); it.Valid(); it.Next() {
			item, _, _ := strings.Cut(string(it.Key()[len(p):]), "/")
			sp.comments[item]++
		}
		if err := it.Close(); err != nil {
			return err
		}
		s.spaces[id] = sp
	}
	return nil
}

func checkSchema(r pebble.Reader) error {
	var schema struct{ V int }
	if err := getJSON(r, []byte(keySchema), &schema); err != nil {
		return err
	}
	if schema.V != core.Version {
		return fmt.Errorf("database has schema v%d, this binary needs v%d", schema.V, core.Version)
	}
	return nil
}

func readSeq(r pebble.Reader, space string) (uint64, error) {
	v, closer, err := r.Get(seqKey(space))
	if errors.Is(err, pebble.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer closer.Close()
	if len(v) != 8 {
		return 0, fmt.Errorf("key %q: want 8 bytes, got %d", seqKey(space), len(v))
	}
	return binary.BigEndian.Uint64(v), nil
}

func getJSON(r pebble.Reader, key []byte, v any) error {
	b, closer, err := r.Get(key)
	if err != nil {
		return fmt.Errorf("key %q: %w", key, err)
	}
	defer closer.Close()
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("key %q: %v", key, err)
	}
	return nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// searchText is the lowercase text grep scans: title, body, field values and
// the extracted text of attached PDFs.
func (s *Store) searchText(it *core.Item) string {
	var b strings.Builder
	b.WriteString(it.Title)
	b.WriteByte('\n')
	b.WriteString(it.Body)
	for _, k := range slices.Sorted(maps.Keys(it.Fields)) {
		b.WriteByte('\n')
		b.WriteString(it.Fields[k])
	}
	s.pdfMu.RLock()
	for _, a := range it.Attachments {
		if t := s.pdf[a.SHA256]; t != "" {
			b.WriteByte('\n')
			b.WriteString(t)
		}
	}
	s.pdfMu.RUnlock()
	return strings.ToLower(b.String())
}

// ---- users and spaces (administration; authorization is the caller's job) ----

// User returns a copy of a user record, or nil.
func (s *Store) User(id string) *core.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := s.users[id]
	if u == nil {
		return nil
	}
	c := *u
	return &c
}

// UserCount returns the number of users.
func (s *Store) UserCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users)
}

// AddUser creates a user and their personal space in one batch. The personal
// space has the user's ID, so users and spaces share one ID namespace.
func (s *Store) AddUser(u core.User) error {
	s.admin.Lock()
	defer s.admin.Unlock()
	if !core.ValidID(u.ID) || core.Reserved(u.ID) {
		return fmt.Errorf("%w: user id %q (want [a-z0-9][a-z0-9-]*, not reserved)", core.ErrInvalid, u.ID)
	}
	s.mu.RLock()
	_, userExists := s.users[u.ID]
	_, spaceExists := s.spaces[u.ID]
	s.mu.RUnlock()
	if userExists || spaceExists {
		return fmt.Errorf("%w: id %q is already used by a user or space", core.ErrConflict, u.ID)
	}
	if u.Name == "" {
		u.Name = u.ID
	}
	u.V, u.PersonalSpace = core.Version, u.ID
	rec := &core.Space{V: core.Version, ID: u.ID, Name: u.Name, Kind: core.SpacePersonal, Members: []string{u.ID}, Created: s.Now()}
	b := s.db.NewBatch()
	defer b.Close()
	b.Set(userKey(u.ID), mustJSON(u), nil)
	b.Set(spaceKey(rec.ID), mustJSON(rec), nil)
	if err := b.Commit(pebble.Sync); err != nil {
		return err
	}
	s.mu.Lock()
	s.users[u.ID] = &u
	s.spaces[rec.ID] = &space{rec: rec, items: map[string]*core.Item{}, text: map[string]string{}, comments: map[string]int{}}
	s.mu.Unlock()
	return nil
}

// UpdateUser applies f to a copy of the user record and stores the result.
func (s *Store) UpdateUser(id string, f func(*core.User) error) error {
	s.admin.Lock()
	defer s.admin.Unlock()
	u := s.User(id)
	if u == nil {
		return fmt.Errorf("%w: user %q", core.ErrNotFound, id)
	}
	if err := f(u); err != nil {
		return err
	}
	u.ID, u.PersonalSpace = id, id
	if err := s.db.Set(userKey(id), mustJSON(u), pebble.Sync); err != nil {
		return err
	}
	s.mu.Lock()
	s.users[id] = u
	s.mu.Unlock()
	return nil
}

// CreateSpace creates a shared space.
func (s *Store) CreateSpace(id, name string, members []string) error {
	s.admin.Lock()
	defer s.admin.Unlock()
	if !core.ValidID(id) || core.Reserved(id) {
		return fmt.Errorf("%w: space id %q (want [a-z0-9][a-z0-9-]*, not reserved)", core.ErrInvalid, id)
	}
	s.mu.RLock()
	_, userExists := s.users[id]
	_, spaceExists := s.spaces[id]
	missing := ""
	for _, m := range members {
		if s.users[m] == nil {
			missing = m
		}
	}
	s.mu.RUnlock()
	if userExists || spaceExists {
		return fmt.Errorf("%w: id %q is already used by a user or space", core.ErrConflict, id)
	}
	if missing != "" {
		return fmt.Errorf("%w: user %q", core.ErrNotFound, missing)
	}
	if name == "" {
		name = id
	}
	members = slices.Compact(slices.Sorted(slices.Values(members)))
	rec := &core.Space{V: core.Version, ID: id, Name: name, Kind: core.SpaceShared, Members: members, Created: s.Now()}
	if err := s.db.Set(spaceKey(id), mustJSON(rec), pebble.Sync); err != nil {
		return err
	}
	s.mu.Lock()
	s.spaces[id] = &space{rec: rec, items: map[string]*core.Item{}, text: map[string]string{}, comments: map[string]int{}}
	s.mu.Unlock()
	return nil
}

// SetMember adds or removes a member of a shared space.
func (s *Store) SetMember(spaceID, user string, member bool) error {
	s.mu.RLock()
	userExists := s.users[user] != nil
	s.mu.RUnlock()
	return s.UpdateSpace(spaceID, func(rec *core.Space) error {
		if !userExists {
			return fmt.Errorf("%w: user %q", core.ErrNotFound, user)
		}
		if rec.Kind != core.SpaceShared {
			return fmt.Errorf("%w: space %q is personal, its only member is its user", core.ErrInvalid, spaceID)
		}
		rec.Members = slices.DeleteFunc(slices.Clone(rec.Members), func(m string) bool { return m == user })
		if member {
			rec.Members = append(rec.Members, user)
			slices.Sort(rec.Members)
		}
		return nil
	})
}

// UpdateSpace applies f to a copy of the space record (members, people,
// templates) and stores the result. Space records are not ops.
func (s *Store) UpdateSpace(spaceID string, f func(*core.Space) error) error {
	s.admin.Lock()
	defer s.admin.Unlock()
	s.mu.RLock()
	sp := s.spaces[spaceID]
	s.mu.RUnlock()
	if sp == nil {
		return fmt.Errorf("%w: space %q", core.ErrNotFound, spaceID)
	}
	// The space's write lock keeps the record stable while an Apply runs.
	sp.wmu.Lock()
	defer sp.wmu.Unlock()
	rec := *sp.rec
	if err := f(&rec); err != nil {
		return err
	}
	rec.V, rec.ID, rec.Kind, rec.Created = sp.rec.V, sp.rec.ID, sp.rec.Kind, sp.rec.Created
	if err := s.db.Set(spaceKey(spaceID), mustJSON(rec), pebble.Sync); err != nil {
		return err
	}
	s.mu.Lock()
	sp.rec = &rec
	s.mu.Unlock()
	return nil
}

// Session is a web login (STORAGE §7). Sessions are not exported.
type Session struct {
	V       int       `json:"v"`
	User    string    `json:"user"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	Device  string    `json:"device,omitempty"`
}

// PutSession stores or replaces a session.
func (s *Store) PutSession(id string, sess Session) error {
	sess.V = core.Version
	return s.db.Set(sessionKey(id), mustJSON(sess), pebble.Sync)
}

// GetSession returns a session, or nil if there is none.
func (s *Store) GetSession(id string) *Session {
	var sess Session
	if id == "" || getJSON(s.db, sessionKey(id), &sess) != nil {
		return nil
	}
	return &sess
}

// DeleteSession removes a session.
func (s *Store) DeleteSession(id string) error {
	return s.db.Delete(sessionKey(id), pebble.Sync)
}

// DeleteSessions ends every web session of a user except the one named keep.
func (s *Store) DeleteSessions(user, keep string) error {
	b := s.db.NewBatch()
	defer b.Close()
	err := scan(s.db, "sess/", func(key string, v []byte) error {
		var sess Session
		if json.Unmarshal(v, &sess) == nil && sess.User == user && key != string(sessionKey(keep)) {
			return b.Delete([]byte(key), nil)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}

// EachUser calls f for every user.
func (s *Store) EachUser(f func(u *core.User)) {
	s.mu.RLock()
	users := make([]*core.User, 0, len(s.users))
	for _, u := range s.users {
		users = append(users, u)
	}
	s.mu.RUnlock()
	for _, u := range users {
		f(u)
	}
}
