package store

import (
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"math"
	"strings"

	"github.com/cockroachdb/pebble/v2"

	"github.com/fiber/gtd/core"
)

// Derived data (keys under x/): embeddings and extracted PDF text. It is
// rebuilt from the items at any time, never exported, and written without
// fsync. These accessors are unscoped, like export and snapshots: they serve
// the embedding service, which answers users only through a View.

const keyVecID = "x/vecid"

// EachItem calls f for every item of every space. The items must not be modified.
func (s *Store) EachItem(f func(it *core.Item)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sp := range s.spaces {
		for _, it := range sp.items {
			f(it)
		}
	}
}

// ItemOf returns an item without a membership check. For derived data only.
func (s *Store) ItemOf(space, id string) *core.Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sp := s.spaces[space]; sp != nil {
		return sp.items[id]
	}
	return nil
}

// VectorIdentity returns the identity (adapter, model, prompt set) of the
// stored vectors, or "".
func (s *Store) VectorIdentity() string {
	v, closer, err := s.db.Get([]byte(keyVecID))
	if err != nil {
		return ""
	}
	defer closer.Close()
	return string(v)
}

// ResetVectors deletes every stored vector and records the identity of the
// ones about to be written.
func (s *Store) ResetVectors(identity string) error {
	b := s.db.NewBatch()
	defer b.Close()
	if err := b.DeleteRange([]byte("x/vec/"), prefixEnd("x/vec/"), nil); err != nil {
		return err
	}
	if err := b.Set([]byte(keyVecID), []byte(identity), nil); err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}

// PutVector stores an item's embedding with the hash of the text it was made
// from. passages are the vectors of the item's passages, if its text was long
// enough to be split. Value: hash, passage count, item vector, passage vectors.
func (s *Store) PutVector(space, id string, hash [32]byte, vec []float32, passages [][]float32) error {
	buf := make([]byte, 36, 36+4*len(vec)*(1+len(passages)))
	copy(buf, hash[:])
	binary.LittleEndian.PutUint32(buf[32:], uint32(len(passages)))
	for _, v := range append([][]float32{vec}, passages...) {
		for _, f := range v {
			buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(f))
		}
	}
	return s.db.Set(vecKey(space, id), buf, pebble.NoSync)
}

// DeleteVector removes an item's embedding.
func (s *Store) DeleteVector(space, id string) error {
	return s.db.Delete(vecKey(space, id), pebble.NoSync)
}

// EachVector calls f for every stored embedding, in key order.
func (s *Store) EachVector(f func(space, id string, hash [32]byte, vec []float32, passages [][]float32)) error {
	return scan(s.db, "x/vec/", func(key string, v []byte) error {
		space, id, ok := strings.Cut(key[len("x/vec/"):], "/")
		if !ok || len(v) < 36 {
			return fmt.Errorf("key %q: not an embedding (%d bytes)", key, len(v))
		}
		var hash [32]byte
		copy(hash[:], v)
		rows := 1 + int(binary.LittleEndian.Uint32(v[32:]))
		if (len(v)-36)%(4*rows) != 0 {
			return fmt.Errorf("key %q: not an embedding (%d bytes for %d vectors)", key, len(v), rows)
		}
		dim := (len(v) - 36) / (4 * rows)
		vecs := make([][]float32, rows)
		for r := range vecs {
			vecs[r] = make([]float32, dim)
			for i := range vecs[r] {
				vecs[r][i] = math.Float32frombits(binary.LittleEndian.Uint32(v[36+4*(r*dim+i):]))
			}
		}
		f(space, id, hash, vecs[0], vecs[1:])
		return nil
	})
}

// PDFText returns the cached text of a PDF blob. ok is false if it was never extracted.
func (s *Store) PDFText(sha string) (text string, ok bool) {
	s.pdfMu.RLock()
	defer s.pdfMu.RUnlock()
	text, ok = s.pdf[sha]
	return
}

// PutPDFText caches the extracted text of a PDF blob (possibly empty) and
// makes it searchable on every item that carries the blob.
func (s *Store) PutPDFText(sha, text string) error {
	if err := s.db.Set(pdfKey(sha), []byte(text), pebble.NoSync); err != nil && !errors.Is(err, pebble.ErrReadOnly) {
		return err
	}
	s.pdfMu.Lock()
	s.pdf[sha] = text
	s.pdfMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sp := range s.spaces {
		for id, it := range sp.items {
			for _, a := range it.Attachments {
				if a.SHA256 == sha {
					sp.text[id] = s.searchText(it)
				}
			}
		}
	}
	return nil
}

// Tickle resurfaces deferred items in every space (SPEC §5.6): it applies
// core.Tickle as the system actor. It returns the number of items resurfaced.
func (s *Store) Tickle(today string) (int, error) {
	s.mu.RLock()
	spaces := make(map[string]*space, len(s.spaces))
	for id, sp := range s.spaces {
		spaces[id] = sp
	}
	s.mu.RUnlock()
	n := 0
	for id, sp := range spaces {
		s.mu.RLock()
		st := &core.State{Space: sp.rec, Items: maps.Clone(sp.items)}
		s.mu.RUnlock()
		ops := core.Tickle(st, today)
		if len(ops) == 0 {
			continue
		}
		if _, err := s.apply(sp, id, core.System, ops); err != nil {
			return n, fmt.Errorf("tickler in space %q: %w", id, err)
		}
		n += len(ops)
	}
	return n, nil
}

// Per-user state (u/<user>/st/<key>): review progress and the like. It is not
// content: it is neither an op nor exported.

func userStateKey(user, key string) []byte { return []byte("u/" + user + "/st/" + key) }

// PutUserState stores a JSON value under a key of the user.
func (s *Store) PutUserState(user, key string, v any) error {
	return s.db.Set(userStateKey(user, key), mustJSON(v), pebble.Sync)
}

// GetUserState reads a value stored by PutUserState. ok is false if there is none.
func (s *Store) GetUserState(user, key string, v any) (ok bool) {
	return getJSON(s.db, userStateKey(user, key), v) == nil
}

// DeleteUserState removes a value.
func (s *Store) DeleteUserState(user, key string) error {
	return s.db.Delete(userStateKey(user, key), pebble.Sync)
}
