package web

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func newBoxKey() *[32]byte {
	var k [32]byte
	rand.Read(k[:])
	return &k
}

// TestLinkOverHTTPS: a link by URL. Everything between the two servers is
// sealed: what passes in between holds neither the token nor a capture, a
// change on the way is noticed, and the WebSocket brings what waits on the
// other side at once.
func TestLinkOverHTTPS(t *testing.T) {
	home, work := newSite(t), newSite(t)
	home.srv.BoxKey, work.srv.BoxKey = newBoxKey(), newBoxKey()
	home.srv.Kick = make(chan struct{}, 1)
	h, w := home.login("robin"), work.login("robin")

	// What a proxy between the two would see, and a switch to make it meddle.
	var mu sync.Mutex
	var seen [][]byte
	meddle := false
	target, _ := url.Parse(work.url)
	between := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "" { // the WebSocket: pass the bytes through
			seenURL := r.URL.String()
			mu.Lock()
			seen = append(seen, []byte(seenURL+" "+r.Header.Get("Authorization")))
			mu.Unlock()
			up, err := net.Dial("tcp", target.Host)
			if err != nil {
				t.Error(err)
				return
			}
			defer up.Close()
			r.Write(up)
			down, _, _ := rw.(http.Hijacker).Hijack()
			defer down.Close()
			go io.Copy(up, down)
			io.Copy(down, up)
			return
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, append([]byte(r.URL.String()+" "+r.Header.Get("Authorization")+" "), body...))
		bad := meddle
		mu.Unlock()
		if bad && len(body) > 0 {
			body[len(body)-1] ^= 1
		}
		req, _ := http.NewRequest(r.Method, "http://"+target.Host+r.URL.Path, bytes.NewReader(body))
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		answer, _ := io.ReadAll(resp.Body)
		mu.Lock()
		seen = append(seen, answer)
		mu.Unlock()
		rw.WriteHeader(resp.StatusCode)
		rw.Write(answer)
	}))
	t.Cleanup(between.Close)
	via := between.URL + Prefix

	type linksRes struct {
		Links []struct {
			Name, SSH, Token string
			Waiting          int
		}
	}
	var wl linksRes
	w.ok("POST", "/api/v1/me/links", map[string]string{"name": "home"}, &wl)
	code := wl.Links[0].Token
	token, key, _ := strings.Cut(code, "~")
	if !strings.HasPrefix(token, "gtd_") || len(key) < 40 {
		t.Fatalf("the code of a waiting link: %q", code)
	}
	add := func(in map[string]string) (int, string) { return h.do("POST", "/api/v1/me/links", in, nil) }
	for name, in := range map[string]map[string]string{
		"link.url":         {"name": "work", "ssh": "http://example.org/gtd", "token": code}, // a token in the clear
		"link.code":        {"name": "work", "ssh": via, "token": token},                     // no key: nothing to seal to
		"link.sealed":      {"name": "work", "ssh": via, "token": token + "~" + strings.Repeat("A", 43)},
		"link.token":       {"name": "work", "ssh": via, "token": token + "x~" + key},
		"link.unreachable": {"name": "work", "ssh": "http://127.0.0.1:1/gtd", "token": code},
		"link.no_gtd":      {"name": "work", "ssh": between.URL + "/elsewhere", "token": code},
	} {
		if c, body := add(in); c < 400 || !strings.Contains(body, `"`+name+`"`) {
			t.Errorf("%s: %d %s", name, c, body)
		}
	}
	var hl linksRes
	h.ok("POST", "/api/v1/me/links", map[string]string{"name": "work", "ssh": via + "/", "token": code}, &hl)
	if len(hl.Links) != 1 || hl.Links[0].SSH != via {
		t.Fatalf("link by URL: %+v", hl)
	}

	inbox := func(c *client) []Brief {
		t.Helper()
		var in struct{ Captures []Brief }
		c.ok("GET", "/api/v1/inbox", nil, &in)
		return in.Captures
	}
	// home → work.
	var cap struct{ ID string }
	h.ok("POST", "/api/v1/capture", map[string]any{"text": "the quarterly figures are confidential", "link": "work"}, &cap)
	if n, err := home.srv.SyncLinks(t.Context()); err != nil || n != 1 {
		t.Fatalf("sync: %d %v", n, err)
	}
	if got := inbox(w); len(got) != 1 || got[0].Title != "the quarterly figures are confidential" {
		t.Fatalf("work inbox: %+v", got)
	}

	// work → home, with the WebSocket: home is told, and comes for it.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go home.srv.WatchLinks(ctx)
	woken := func(what string) {
		t.Helper()
		select {
		case <-home.srv.Kick:
		case <-time.After(5 * time.Second):
			t.Fatalf("home was not woken: %s", what)
		}
	}
	woken("on connecting")
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) { // the WebSocket is up
		work.srv.wakes.mu.Lock()
		n := len(work.srv.wakes.subs["robin\x00home"])
		work.srv.wakes.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("work has no listener for home")
		}
	}
	w.ok("POST", "/api/v1/capture", map[string]any{"text": "bring the secret recipe", "link": "home"}, &cap)
	woken("after a capture for home")
	if n, err := home.srv.SyncLinks(t.Context()); err != nil || n != 1 {
		t.Fatalf("sync after the wake: %d %v", n, err)
	}
	if got := inbox(h); len(got) != 1 || got[0].Title != "bring the secret recipe" {
		t.Fatalf("home inbox: %+v", got)
	}

	// Nothing that passed in between gives anything away.
	mu.Lock()
	for _, b := range seen {
		for _, secret := range []string{token, "gtd_", "confidential", "recipe", "Bearer", "capture"} {
			if bytes.Contains(b, []byte(secret)) {
				t.Errorf("%q passed in the clear: %.80q", secret, b)
			}
		}
	}
	if len(seen) < 6 {
		t.Errorf("only %d messages passed the proxy", len(seen))
	}
	meddle = true
	mu.Unlock()

	// One changed bit: the capture stays in the outbox and nothing arrives.
	h.ok("POST", "/api/v1/capture", map[string]any{"text": "changed on the way?", "link": "work"}, &cap)
	if _, err := home.srv.SyncLinks(t.Context()); err == nil || !strings.Contains(err.Error(), "link.sealed") {
		t.Errorf("sync through a meddling proxy: %v", err)
	}
	h.ok("GET", "/api/v1/me/links", nil, &hl)
	if hl.Links[0].Waiting != 1 || len(inbox(w)) != 1 {
		t.Errorf("after meddling: %d waiting, work inbox %d", hl.Links[0].Waiting, len(inbox(w)))
	}
	mu.Lock()
	meddle = false
	mu.Unlock()
	if n, err := home.srv.SyncLinks(t.Context()); err != nil || n != 1 || len(inbox(w)) != 2 {
		t.Errorf("sync once the proxy behaves: %d %v, work inbox %d", n, err, len(inbox(w)))
	}

	// A paused link makes no connection at all; what is sent to it waits and
	// goes when the link is resumed.
	mu.Lock()
	before := len(seen)
	mu.Unlock()
	h.ok("PUT", "/api/v1/me/links/work", map[string]bool{"off": true}, nil)
	h.ok("POST", "/api/v1/capture", map[string]any{"text": "while paused", "link": "work"}, &cap)
	if n, err := home.srv.SyncLinks(t.Context()); err != nil || n != 0 {
		t.Errorf("sync of a paused link: %d %v", n, err)
	}
	mu.Lock()
	if len(seen) != before {
		t.Errorf("a paused link talked: %d messages", len(seen)-before)
	}
	mu.Unlock()
	h.ok("PUT", "/api/v1/me/links/work", map[string]bool{"off": false}, nil)
	if n, err := home.srv.SyncLinks(t.Context()); err != nil || n != 1 || len(inbox(w)) != 3 {
		t.Errorf("sync after resuming: %d %v, work inbox %d", n, err, len(inbox(w)))
	}
	if c, _ := w.do("PUT", "/api/v1/me/links/home", map[string]bool{"off": true}, nil); c != 400 {
		t.Errorf("pausing a link that only waits: %d", c)
	}

	// The code is pinned to home: work shows whose it is, another server with
	// the same code is turned away, and so is the bare token without the seal.
	var pinned struct {
		Links []struct{ Name, Fingerprint string }
		Box   string
	}
	var mine struct{ Box string }
	w.ok("GET", "/api/v1/me/links", nil, &pinned)
	h.ok("GET", "/api/v1/me/links", nil, &mine)
	if pinned.Links[0].Fingerprint == "" || pinned.Links[0].Fingerprint != mine.Box || pinned.Box == mine.Box {
		t.Errorf("work shows %q for the link, home's key is %q, work's own %q", pinned.Links[0].Fingerprint, mine.Box, pinned.Box)
	}
	thief := newSite(t)
	thief.srv.BoxKey = newBoxKey()
	if c, body := thief.login("robin").do("POST", "/api/v1/me/links", map[string]string{"name": "work", "ssh": work.url, "token": code}, nil); c < 400 || !strings.Contains(body, `"link.pinned"`) {
		t.Errorf("a second server with the same code: %d %s", c, body)
	}
	bare, _ := http.NewRequest("GET", work.url+"/api/v1/outbox", nil)
	bare.Header.Set("Authorization", "Bearer "+token)
	if res, err := http.DefaultClient.Do(bare); err != nil || res.StatusCode != 403 {
		t.Errorf("the token without the seal: %v %v", res, err)
	}
	if n, err := home.srv.SyncLinks(t.Context()); err != nil || n != 0 {
		t.Errorf("home after the attempts: %d %v", n, err)
	}

	// The sealed door lets in API requests with a token and nothing else,
	// and the WebSocket needs a ticket.
	c, err := home.srv.dial(t.Context(), &Link{Name: "work", URL: via, Token: token, PeerKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.get("/api/v1/inbox"); err == nil {
		t.Error("a link's token reads the inbox through the seal")
	}
	if res, err := http.Get(work.url + "/api/v1/outbox/wake?ticket=nope"); err != nil || res.StatusCode != 401 {
		t.Errorf("the WebSocket without a ticket: %v %v", res, err)
	}
	if res, err := http.Post(work.url+"/api/v1/link", "application/octet-stream", strings.NewReader(strings.Repeat("x", 200))); err != nil || res.StatusCode != 400 {
		t.Errorf("rubbish at the sealed door: %v %v", res, err)
	}
	// An old request is not accepted again.
	old, _ := http.NewRequest("GET", "http://gtd/api/v1/outbox", nil)
	old.Header.Set("Authorization", "Bearer "+token)
	old.Header.Set("X-Link-Time", "1000000")
	var plain bytes.Buffer
	old.Write(&plain)
	peer := BoxPublic(work.srv.BoxKey)
	res, err := http.Post(work.url+"/api/v1/link", "application/octet-stream",
		bytes.NewReader(append(BoxPublic(home.srv.BoxKey)[:], seal(plain.Bytes(), peer, home.srv.BoxKey)...)))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	answer, err := unseal(data, peer, home.srv.BoxKey)
	if err != nil {
		t.Fatal(err)
	}
	if in, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(answer)), old); err != nil || in.StatusCode != 400 {
		t.Errorf("an old sealed request: %v %v", in, err)
	}
}

// TestDialThroughProxy: the WebSocket's connection asks an HTTP proxy for a
// tunnel, with the proxy's credentials.
func TestDialThroughProxy(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { echo.Close() })
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	var asked, auth string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked, auth = r.Method+" "+r.Host, r.Header.Get("Proxy-Authorization")
		if r.Method != "CONNECT" || auth == "" {
			http.Error(w, "no", http.StatusProxyAuthRequired)
			return
		}
		up, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer up.Close()
		w.WriteHeader(200)
		down, _, _ := w.(http.Hijacker).Hijack()
		defer down.Close()
		go io.Copy(up, down)
		io.Copy(down, up)
	}))
	t.Cleanup(proxy.Close)
	pu, _ := url.Parse(proxy.URL)
	pu.User = url.UserPassword("me", "secret")
	target := &url.URL{Scheme: "http", Host: echo.Addr().String()}
	conn, err := dialThrough(t.Context(), pu, target)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, "ping")
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
		t.Errorf("through the tunnel: %q %v", buf, err)
	}
	if asked != "CONNECT "+echo.Addr().String() || !strings.HasPrefix(auth, "Basic ") {
		t.Errorf("the proxy was asked %q with %q", asked, auth)
	}
	pu.User = nil
	if _, err := dialThrough(t.Context(), pu, target); err == nil {
		t.Error("a proxy that refuses was not noticed")
	}
}
