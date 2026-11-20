package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/fiber/gtd/core"
)

// API tokens (STORAGE §7, COLLAB §6): `gtd_<id>_<secret>`. Only the sha256 of
// the secret is stored. A token acts as its user, within its scopes.

// Scopes. ScopeCapture allows POST /capture into the owner's inbox.
// ScopeOutbox allows a linked server to collect what the owner sent to it
// (the token's Link) and to confirm it. Neither can read anything else.
const (
	ScopeCapture = "capture"
	ScopeOutbox  = "outbox"
)

// Token is a stored API token. The secret is not in it.
type Token struct {
	ID       string    `json:"id"`
	Label    string    `json:"label,omitempty"`
	Link     string    `json:"link,omitempty"` // the linked server this token is for
	Peer     string    `json:"peer,omitempty"` // the sealing key of the server that uses it, once seen
	Hash     string    `json:"hash"`
	Scopes   []string  `json:"scopes"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"last_used,omitzero"`
}

func tokenPrefix(user string) string { return "u/" + user + "/tok/" }

// AddToken creates a token for a user and returns it in the clear, once.
func (s *Store) AddToken(user, label, link string, scopes []string) (plain string, tok Token, err error) {
	if s.User(user) == nil {
		return "", tok, fmt.Errorf("%w: user %q", core.ErrNotFound, user)
	}
	if len(scopes) == 0 {
		return "", tok, fmt.Errorf("%w: a token needs a scope", core.ErrInvalid)
	}
	for _, sc := range scopes {
		if sc != ScopeCapture && sc != ScopeOutbox {
			return "", tok, fmt.Errorf("%w: scope %q (want %q or %q)", core.ErrInvalid, sc, ScopeCapture, ScopeOutbox)
		}
	}
	raw := make([]byte, 6+24)
	if _, err := rand.Read(raw); err != nil {
		return "", tok, err
	}
	id, secret := hex.EncodeToString(raw[:6]), hex.EncodeToString(raw[6:])
	sum := sha256.Sum256([]byte(secret))
	tok = Token{ID: id, Label: strings.TrimSpace(label), Link: link, Hash: hex.EncodeToString(sum[:]), Scopes: scopes, Created: s.Now()}
	if err := s.db.Set([]byte(tokenPrefix(user)+id), mustJSON(tok), pebble.Sync); err != nil {
		return "", tok, err
	}
	return "gtd_" + id + "_" + secret, tok, nil
}

// Tokens lists a user's tokens, oldest first.
func (s *Store) Tokens(user string) ([]Token, error) {
	var out []Token
	err := scan(s.db, tokenPrefix(user), func(key string, v []byte) error {
		var t Token
		if err := json.Unmarshal(v, &t); err != nil {
			return fmt.Errorf("key %q: %v", key, err)
		}
		out = append(out, t)
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out, err
}

// PinToken binds a token to the sealing key of the server that uses it, if it
// is not bound yet, and returns the key it is bound to. The first server to
// use a link's token over HTTPS is the only one that can from then on.
func (s *Store) PinToken(user, id, peer string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var t Token
	key := []byte(tokenPrefix(user) + id)
	if err := getJSON(s.db, key, &t); err != nil {
		return "", fmt.Errorf("%w: token %s of user %q", core.ErrNotFound, id, user)
	}
	if t.Peer != "" {
		return t.Peer, nil
	}
	t.Peer = peer
	return peer, s.db.Set(key, mustJSON(t), pebble.Sync)
}

// DeleteToken revokes a token.
func (s *Store) DeleteToken(user, id string) error {
	key := []byte(tokenPrefix(user) + id)
	if _, closer, err := s.db.Get(key); err != nil {
		return fmt.Errorf("%w: token %s of user %q", core.ErrNotFound, id, user)
	} else {
		closer.Close()
	}
	return s.db.Delete(key, pebble.Sync)
}

// TokenUser returns the user and the token for a token given in the clear.
func (s *Store) TokenUser(plain string) (user string, tok Token, ok bool) {
	rest, isToken := strings.CutPrefix(plain, "gtd_")
	id, secret, cut := strings.Cut(rest, "_")
	if !isToken || !cut || id == "" || strings.ContainsAny(id, "/\x00") {
		return "", tok, false
	}
	sum := sha256.Sum256([]byte(secret))
	hash := hex.EncodeToString(sum[:])
	s.mu.RLock()
	users := make([]string, 0, len(s.users))
	for u := range s.users {
		users = append(users, u)
	}
	s.mu.RUnlock()
	slices.Sort(users)
	for _, u := range users {
		var t Token
		key := []byte(tokenPrefix(u) + id)
		if getJSON(s.db, key, &t) != nil {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(t.Hash), []byte(hash)) != 1 {
			return "", tok, false
		}
		if now := s.Now(); now.Sub(t.LastUsed) > time.Hour {
			t.LastUsed = now
			s.db.Set(key, mustJSON(t), pebble.NoSync)
		}
		return u, t, true
	}
	return "", tok, false
}
