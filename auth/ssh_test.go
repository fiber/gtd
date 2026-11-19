package auth

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// newKey adds a fresh key to the agent and returns its authorized_keys line.
func newKey(t *testing.T, ag agent.Agent, ec bool) string {
	t.Helper()
	var priv any
	var err error
	if ec {
		priv, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	} else {
		_, priv, err = ed25519.GenerateKey(rand.Reader)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return string(ssh.MarshalAuthorizedKey(signer.PublicKey())) + " comment"
}

func TestChallenge(t *testing.T) {
	svenAgent, ericAgent, emptyAgent := agent.NewKeyring(), agent.NewKeyring(), agent.NewKeyring()
	svenKey := newKey(t, svenAgent, false)
	svenEC := newKey(t, svenAgent, true)
	ericKey := newKey(t, ericAgent, false)
	registered := map[string][]string{"robin": {svenKey, svenEC}, "dana": {ericKey}, "nokeys": nil}
	const server = "01SERVER"

	now := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
	a := NewSSH(server, func(u string) []string { return registered[u] })
	a.Now = func() time.Time { return now }

	sign := func(ag agent.Agent, serverID, nonce, only string) [][]byte {
		sigs, err := Sign(ag, serverID, nonce, only)
		if err != nil {
			t.Fatal(err)
		}
		return sigs
	}

	for _, c := range []struct {
		name string
		user string
		sigs func(nonce string) [][]byte
		wait time.Duration // time passing between challenge and verify
		ok   bool
	}{
		{"all agent keys", "robin", func(n string) [][]byte { return sign(svenAgent, server, n, "") }, 0, true},
		{"one named key (ed25519)", "robin", func(n string) [][]byte { return sign(svenAgent, server, n, svenKey) }, 0, true},
		{"one named key (ecdsa)", "robin", func(n string) [][]byte { return sign(svenAgent, server, n, svenEC) }, 0, true},
		{"just inside the TTL", "robin", func(n string) [][]byte { return sign(svenAgent, server, n, "") }, 59 * time.Second, true},
		{"someone else's key", "robin", func(n string) [][]byte { return sign(ericAgent, server, n, "") }, 0, false},
		{"unknown user", "mallory", func(n string) [][]byte { return sign(svenAgent, server, n, "") }, 0, false},
		{"user without keys", "nokeys", func(n string) [][]byte { return sign(svenAgent, server, n, "") }, 0, false},
		{"signed for another server", "robin", func(n string) [][]byte { return sign(svenAgent, "01OTHER", n, "") }, 0, false},
		{"signed another nonce", "robin", func(n string) [][]byte { return sign(svenAgent, server, n+"x", "") }, 0, false},
		{"expired nonce", "robin", func(n string) [][]byte { return sign(svenAgent, server, n, "") }, 61 * time.Second, false},
		{"no signatures", "robin", func(n string) [][]byte { return nil }, 0, false},
		{"garbage signature", "robin", func(n string) [][]byte { return [][]byte{[]byte("garbage")} }, 0, false},
		{"too many signatures", "robin", func(n string) [][]byte {
			s := sign(svenAgent, server, n, svenKey)
			for len(s) <= maxSigs {
				s = append(s, s[0])
			}
			return s
		}, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			now = time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
			nonce := a.Challenge(c.user)
			sigs := c.sigs(nonce)
			now = now.Add(c.wait)
			token, err := a.Verify(nonce, sigs)
			if c.ok != (err == nil) {
				t.Fatalf("err = %v, want ok = %v", err, c.ok)
			}
			if !c.ok {
				if !errors.Is(err, ErrDenied) || token != "" {
					t.Fatalf("err = %v, token = %q", err, token)
				}
				return
			}
			if user, ok := a.Session(token); !ok || user != c.user {
				t.Fatalf("session = %q, %v", user, ok)
			}
			// The nonce is single-use.
			if _, err := a.Verify(nonce, sigs); !errors.Is(err, ErrDenied) {
				t.Fatalf("replayed nonce: %v", err)
			}
			// The session lasts 12 hours.
			now = now.Add(sessionTTL - time.Second)
			if _, ok := a.Session(token); !ok {
				t.Fatal("session ended early")
			}
			now = now.Add(2 * time.Second)
			if _, ok := a.Session(token); ok {
				t.Fatal("session outlived its TTL")
			}
		})
	}

	t.Run("a failed attempt burns the nonce", func(t *testing.T) {
		nonce := a.Challenge("robin")
		if _, err := a.Verify(nonce, sign(ericAgent, server, nonce, "")); err == nil {
			t.Fatal("accepted the wrong key")
		}
		if _, err := a.Verify(nonce, sign(svenAgent, server, nonce, "")); !errors.Is(err, ErrDenied) {
			t.Fatalf("nonce reusable after a failure: %v", err)
		}
	})
	t.Run("unknown session", func(t *testing.T) {
		if _, ok := a.Session("nope"); ok {
			t.Fatal("accepted")
		}
	})
	t.Run("client errors", func(t *testing.T) {
		if _, err := Sign(emptyAgent, server, "n", ""); err == nil || !strings.Contains(err.Error(), "holds no keys") {
			t.Fatalf("empty agent: %v", err)
		}
		if _, err := Sign(svenAgent, server, "n", ericKey); err == nil || !strings.Contains(err.Error(), "does not hold") {
			t.Fatalf("key not in agent: %v", err)
		}
	})
}

func TestNormalizeKey(t *testing.T) {
	line := newKey(t, agent.NewKeyring(), false)
	bare := strings.TrimSuffix(strings.TrimSpace(line), " comment")
	bare = strings.TrimSpace(strings.TrimSuffix(bare, "\n"))
	for _, in := range []string{line, `from="10.0.0.1" ` + line, "  " + line + "\n"} {
		got, err := NormalizeKey(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got != strings.Fields(bare)[0]+" "+strings.Fields(bare)[1] {
			t.Fatalf("NormalizeKey(%q) = %q", in, got)
		}
	}
	for _, in := range []string{"", "ssh-ed25519", "not a key", "-----BEGIN OPENSSH PRIVATE KEY-----"} {
		if _, err := NormalizeKey(in); err == nil {
			t.Fatalf("NormalizeKey(%q) accepted", in)
		}
	}
}
