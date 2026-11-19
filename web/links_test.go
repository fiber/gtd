package web

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/textproto"

	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/fiber/gtd/core"
)

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// sshd is a small SSH server that accepts one client key and forwards TCP
// connections, like an sshd with a restricted authorized_keys line.
func sshd(t *testing.T, host ssh.Signer, allowed ssh.PublicKey) string {
	t.Helper()
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if string(key.Marshal()) != string(allowed.Marshal()) {
			return nil, fmt.Errorf("unknown key")
		}
		return nil, nil
	}}
	cfg.AddHostKey(host)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				conn, chans, reqs, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					raw.Close()
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					if ch.ChannelType() != "direct-tcpip" {
						ch.Reject(ssh.UnknownChannelType, "only forwarding")
						continue
					}
					// payload: string host, uint32 port, string origin, uint32 origin port
					p := ch.ExtraData()
					n := binary.BigEndian.Uint32(p)
					target := net.JoinHostPort(string(p[4:4+n]), fmt.Sprint(binary.BigEndian.Uint32(p[4+n:])))
					up, err := net.Dial("tcp", target)
					if err != nil {
						ch.Reject(ssh.ConnectionFailed, err.Error())
						continue
					}
					c, creqs, err := ch.Accept()
					if err != nil {
						up.Close()
						continue
					}
					go ssh.DiscardRequests(creqs)
					go func() { io.Copy(up, c); up.Close() }()
					go func() { io.Copy(c, up); c.Close() }()
				}
			}()
		}
	}()
	return ln.Addr().String()
}

// TestLinkedServers: two servers, of which only "home" can reach "work".
// Captures travel both ways over SSH: home delivers what its user sent, and
// collects what the user sent on work (Q-9).
func TestLinkedServers(t *testing.T) {
	home, work := newSite(t), newSite(t)
	homeKey, hostKey := newSigner(t), newSigner(t)
	home.srv.LinkKey = homeKey
	workSSH := sshd(t, hostKey, homeKey.PublicKey())
	workHTTP := strings.TrimPrefix(strings.TrimSuffix(work.url, Prefix), "http://")
	h, w := home.login("robin"), work.login("robin")

	type linksRes struct {
		Links []struct {
			Name, SSH, Fingerprint, Token string
			Waiting                       int
		}
		Key string
	}
	// On work, the reachable side: a link that waits. It hands out a token.
	var wl linksRes
	w.ok("POST", "/api/v1/me/links", map[string]string{"name": "home"}, &wl)
	token := wl.Links[0].Token
	if len(wl.Links) != 1 || !strings.HasPrefix(token, "gtd_") || wl.Links[0].SSH != "" {
		t.Fatalf("waiting link: %+v", wl)
	}
	var again linksRes
	w.ok("GET", "/api/v1/me/links", nil, &again)
	if again.Links[0].Token != "" {
		t.Error("the token is shown again")
	}
	// On home, the dialing side. Bad input first.
	add := func(in map[string]string) (int, string) { return h.do("POST", "/api/v1/me/links", in, nil) }
	for name, in := range map[string]map[string]string{
		"link.address":     {"name": "work", "ssh": "no-at-sign", "target": workHTTP, "token": token},
		"link.unreachable": {"name": "work", "ssh": "gtd@127.0.0.1:1", "target": workHTTP, "token": token},
		"link.token":       {"name": "work", "ssh": "gtd@" + workSSH, "target": workHTTP, "token": token + "x"},
		"link.no_gtd":      {"name": "work", "ssh": "gtd@" + workSSH, "target": "127.0.0.1:1", "token": token},
		"link.name":        {"name": "a/b", "ssh": "gtd@" + workSSH, "target": workHTTP, "token": token},
	} {
		if code, body := add(in); code < 400 || !strings.Contains(body, `"`+name+`"`) {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	var hl linksRes
	h.ok("POST", "/api/v1/me/links", map[string]string{"name": "work", "ssh": "gtd@" + workSSH, "target": workHTTP, "token": token}, &hl)
	if len(hl.Links) != 1 || hl.Links[0].Fingerprint != ssh.FingerprintSHA256(hostKey.PublicKey()) || !strings.HasPrefix(hl.Key, "ssh-ed25519 ") {
		t.Fatalf("dialing link: %+v", hl)
	}
	if code, body := add(map[string]string{"name": "Work", "ssh": "gtd@" + workSSH, "target": workHTTP, "token": token}); code != 400 || !strings.Contains(body, "link.exists") {
		t.Errorf("a second link of the same name: %d %s", code, body)
	}

	// send clarifies a capture with "Send to <link>".
	send := func(c *client, id, link string) {
		t.Helper()
		var st struct {
			Session string
			Step    struct {
				Options []struct{ Value, Text string }
			}
		}
		c.ok("POST", "/api/v1/wizard/clarify/start", map[string]string{"item": id}, &st)
		offered := false
		for _, o := range st.Step.Options {
			offered = offered || o.Value == "send:"+link && o.Text == link
		}
		if !offered {
			t.Fatalf("Clarify does not offer to send to %q: %+v", link, st.Step.Options)
		}
		var res struct{ Done bool }
		c.ok("POST", "/api/v1/wizard/clarify/"+st.Session+"/step", map[string]any{"choice": "send:" + link}, &res)
		if !res.Done {
			t.Fatal("sending did not finish the wizard")
		}
	}
	inbox := func(c *client) []Brief {
		t.Helper()
		var in struct{ Captures []Brief }
		c.ok("GET", "/api/v1/inbox", nil, &in)
		return in.Captures
	}
	sync := func() int {
		t.Helper()
		n, err := home.srv.SyncLinks(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	// home → work: with a photo. It leaves the inbox at once and arrives with
	// the next connection.
	png := "\x89PNG\r\n\x1a\nnot really a picture"
	var cap struct{ ID string }
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	mw.WriteField("text", "ask the team about the export\nsecond line")
	fh := textproto.MIMEHeader{}
	fh.Set("Content-Disposition", `form-data; name="file"; filename="board.png"`)
	fh.Set("Content-Type", "image/png")
	part, _ := mw.CreatePart(fh)
	part.Write([]byte(png))
	mw.Close()
	res, err := h.http.Post(home.url+"/api/v1/capture", mw.FormDataContentType(), &form)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("capture: %v %v", err, res)
	}
	json.NewDecoder(res.Body).Decode(&cap)
	res.Body.Close()
	send(h, cap.ID, "work")
	if len(inbox(h)) != 0 || len(inbox(w)) != 0 {
		t.Fatalf("after sending: home inbox %d, work inbox %d", len(inbox(h)), len(inbox(w)))
	}
	h.ok("GET", "/api/v1/me/links", nil, &hl)
	if hl.Links[0].Waiting != 1 {
		t.Errorf("waiting on home: %+v", hl.Links)
	}
	if n := sync(); n != 1 {
		t.Fatalf("first sync moved %d", n)
	}
	got := inbox(w)
	if len(got) != 1 || got[0].Title != "ask the team about the export" || got[0].Attachments != 1 {
		t.Fatalf("work inbox: %+v", got)
	}
	wv, _ := work.st.View("robin")
	if it, err := wv.Item("robin", got[0].ID); err != nil || it.Source.Via != "server" || it.Source.Ref != "test:"+cap.ID ||
		!strings.Contains(it.Original, "second line") || it.Attachments[0].Name != "board.png" || it.Attachments[0].Size != int64(len(png)) {
		t.Errorf("the capture on work: %+v %v", it, err)
	}
	hv, _ := home.st.View("robin")
	if it, _ := hv.Item("robin", cap.ID); it.State != core.StTrashed {
		t.Errorf("the capture on home is %s, want trashed", it.State)
	}
	if c, _ := hv.Comments("robin", cap.ID); len(c) != 1 || c[0].Body != "→ work" {
		t.Errorf("thread on home: %+v", c)
	}

	// work → home: work cannot connect. It waits there until home comes by.
	w.ok("POST", "/api/v1/capture", map[string]any{"text": "buy milk on the way home"}, &cap)
	send(w, cap.ID, "home")
	wl = linksRes{}
	w.ok("GET", "/api/v1/me/links", nil, &wl)
	if wl.Links[0].Waiting != 1 || len(inbox(w)) != 1 || len(inbox(h)) != 0 {
		t.Fatalf("before home connects: waiting %d, work inbox %d, home inbox %d", wl.Links[0].Waiting, len(inbox(w)), len(inbox(h)))
	}
	if n := sync(); n != 1 {
		t.Fatalf("second sync moved %d", n)
	}
	if got := inbox(h); len(got) != 1 || got[0].Title != "buy milk on the way home" {
		t.Fatalf("home inbox: %+v", got)
	}
	w.ok("GET", "/api/v1/me/links", nil, &wl)
	if wl.Links[0].Waiting != 0 {
		t.Errorf("still waiting on work after delivery: %d", wl.Links[0].Waiting)
	}
	// Nothing left: a further connection moves nothing and duplicates nothing.
	if n := sync(); n != 0 || len(inbox(h)) != 1 || len(inbox(w)) != 1 {
		t.Errorf("idle sync moved %d; inboxes %d and %d", n, len(inbox(h)), len(inbox(w)))
	}
	// A lost confirmation: the same capture is offered again and not created twice.
	if err := work.srv.queueCapture("robin", "home", cap.ID); err != nil {
		t.Fatal(err)
	}
	sync()
	if len(inbox(h)) != 1 {
		t.Errorf("a capture collected twice: home inbox has %d", len(inbox(h)))
	}

	// What the link's token may do on work, and what it may not.
	call := func(tok, method, path string) int {
		req, _ := http.NewRequest(method, work.url+path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := call(token, "GET", "/api/v1/outbox"); code != 200 {
		t.Errorf("the link token reading its outbox: %d", code)
	}
	for _, path := range []string{"/api/v1/inbox", "/api/v1/me", "/api/v1/items/" + cap.ID, "/api/v1/search?q=milk", "/api/v1/me/links", "/api/v1/ops?n=3"} {
		if code := call(token, "GET", path); code != 403 {
			t.Errorf("the link token on %s: %d, want 403", path, code)
		}
	}
	var plain struct{ Token string }
	w.ok("POST", "/api/v1/me/tokens", map[string]any{"scopes": []string{"capture"}}, &plain)
	if code := call(plain.Token, "GET", "/api/v1/outbox"); code != 403 {
		t.Errorf("a plain capture token reading an outbox: %d", code)
	}
	// Another user on work has nothing to do with it.
	dana := work.login("dana")
	var dl linksRes
	dana.ok("GET", "/api/v1/me/links", nil, &dl)
	if len(dl.Links) != 0 || len(inbox(dana)) != 0 {
		t.Errorf("dana sees links or captures: %+v", dl)
	}

	// The host shows another key than when the link was made: no connection.
	other := sshd(t, newSigner(t), homeKey.PublicKey())
	links := home.srv.userLinks("robin")
	links[0].SSH = "gtd@" + other
	home.st.PutUserState("robin", "links", links)
	if _, err := home.srv.SyncLinks(t.Context()); err == nil || !strings.Contains(err.Error(), "link.hostkey") {
		t.Errorf("a changed host key: %v", err)
	}
	// This server's key is not accepted there: no connection.
	links[0].SSH = "gtd@" + workSSH
	home.st.PutUserState("robin", "links", links)
	home.srv.LinkKey = newSigner(t)
	if _, err := home.srv.SyncLinks(t.Context()); err == nil || !strings.Contains(err.Error(), "link.ssh_refused") {
		t.Errorf("an unknown client key: %v", err)
	}

	// Removing the link on work revokes its token.
	w.ok("DELETE", "/api/v1/me/links/home", nil, nil)
	if code := call(token, "GET", "/api/v1/outbox"); code != 401 {
		t.Errorf("the token after its link was removed: %d", code)
	}
}
