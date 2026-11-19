// Package auth implements gtd's authentication. In M0 that is the SSH-key
// challenge used by the CLI and by admin commands (STORAGE §7).
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const (
	nonceTTL   = time.Minute
	sessionTTL = 12 * time.Hour
	maxSigs    = 16 // signatures accepted per attempt, and keys tried from an agent
)

// ErrDenied is the only failure Verify reports. It does not say whether the
// user exists, the nonce expired or the key is unknown.
var ErrDenied = errors.New("authentication failed")

// Message is what the client signs: bound to this server and to one nonce.
func Message(serverID, nonce string) []byte {
	return []byte("gtd-auth-v1\n" + serverID + "\n" + nonce)
}

// NormalizeKey parses an authorized_keys line and returns "<type> <base64>",
// without options or comment.
func NormalizeKey(line string) (string, error) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return "", fmt.Errorf("not an SSH public key (want an authorized_keys line): %v", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))), nil
}

// SSH is the server side of the challenge: it issues nonces, verifies
// signatures against a user's registered keys and holds the resulting
// sessions in memory. Sessions end when the daemon stops.
type SSH struct {
	serverID string
	keys     func(user string) []string // registered keys of a user; nil if unknown
	Now      func() time.Time

	mu       sync.Mutex
	nonces   map[string]grant
	sessions map[string]grant
}

type grant struct {
	user    string
	expires time.Time
}

func NewSSH(serverID string, keys func(user string) []string) *SSH {
	return &SSH{serverID: serverID, keys: keys, Now: time.Now, nonces: map[string]grant{}, sessions: map[string]grant{}}
}

// Challenge returns a single-use nonce for user. It answers the same way for
// unknown users.
func (a *SSH) Challenge(user string) string {
	nonce := random()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.purge()
	a.nonces[nonce] = grant{user, a.Now().Add(nonceTTL)}
	return nonce
}

// Verify checks the signatures (each an ssh.Marshal'ed ssh.Signature over
// Message) against the keys of the user the nonce was issued for. If one
// matches, it returns a session token valid for 12 hours.
func (a *SSH) Verify(nonce string, sigs [][]byte) (token string, err error) {
	a.mu.Lock()
	g, ok := a.nonces[nonce]
	delete(a.nonces, nonce) // single use, whatever the outcome
	a.mu.Unlock()
	if !ok || a.Now().After(g.expires) || len(sigs) > maxSigs {
		return "", ErrDenied
	}
	var keys []ssh.PublicKey
	for _, line := range a.keys(g.user) {
		if pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line)); err == nil {
			keys = append(keys, pub)
		}
	}
	msg := Message(a.serverID, nonce)
	for _, raw := range sigs {
		var sig ssh.Signature
		if ssh.Unmarshal(raw, &sig) != nil {
			continue
		}
		for _, pub := range keys {
			if pub.Verify(msg, &sig) == nil {
				token = random()
				a.mu.Lock()
				a.sessions[token] = grant{g.user, a.Now().Add(sessionTTL)}
				a.mu.Unlock()
				return token, nil
			}
		}
	}
	return "", ErrDenied
}

// Session returns the user of a valid session token.
func (a *SSH) Session(token string) (user string, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	g, ok := a.sessions[token]
	if !ok || a.Now().After(g.expires) {
		delete(a.sessions, token)
		return "", false
	}
	return g.user, true
}

// purge drops expired nonces and sessions. Callers hold a.mu.
func (a *SSH) purge() {
	now := a.Now()
	for k, g := range a.nonces {
		if now.After(g.expires) {
			delete(a.nonces, k)
		}
	}
	for k, g := range a.sessions {
		if now.After(g.expires) {
			delete(a.sessions, k)
		}
	}
}

func random() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Sign is the client side: it signs the challenge with keys held by an
// ssh-agent. With onlyKey (an authorized_keys line) it uses that key alone,
// otherwise every key in the agent, up to 16.
func Sign(ag agent.Agent, serverID, nonce, onlyKey string) ([][]byte, error) {
	keys, err := ag.List()
	if err != nil {
		return nil, fmt.Errorf("ssh-agent: %v", err)
	}
	if onlyKey != "" {
		want, err := NormalizeKey(onlyKey)
		if err != nil {
			return nil, err
		}
		var match []*agent.Key
		for _, k := range keys {
			if strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) == want {
				match = append(match, k)
			}
		}
		if len(match) == 0 {
			return nil, fmt.Errorf("ssh-agent does not hold the key %s", want)
		}
		keys = match
	}
	if len(keys) == 0 {
		return nil, errors.New("ssh-agent holds no keys (ssh-add one, or check SSH_AUTH_SOCK)")
	}
	if len(keys) > maxSigs {
		keys = keys[:maxSigs]
	}
	msg := Message(serverID, nonce)
	var sigs [][]byte
	for _, k := range keys {
		sig, err := ag.Sign(k, msg)
		if err != nil {
			continue // e.g. a hardware key that was not touched
		}
		sigs = append(sigs, ssh.Marshal(sig))
	}
	if len(sigs) == 0 {
		return nil, errors.New("ssh-agent could not sign with any key")
	}
	return sigs, nil
}
