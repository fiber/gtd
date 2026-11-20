package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/fiber/gtd/core"
	"github.com/fiber/gtd/store"
)

// Linked servers (Q-9). A user links their account on another gtd server, and
// Clarify can then send a capture to that server's inbox instead of keeping it.
// Only captures travel, only on the user's tap, and only into the user's own
// inbox on the other side. Nothing is shared, synced or passed on to a third
// server.
//
// The channel is SSH. Servers are not assumed to reach each other both ways:
// a link is dialed by one side only.
//
//   - The dialing side has the other host's SSH address. It connects with this
//     server's key (LinkKey), checks the host key it saw when the link was
//     made, and talks to the other gtd through that connection.
//   - The other side only has the link's name. What its user sends to the link
//     waits in an outbox until the dialing side next connects and collects it.
//
// Each connection delivers what waits here for the other side and collects
// what waits there for us. The token made on the reachable side is all the
// dialing side may use there: capture, and that link's outbox.

// Link is one linked server of a user.
type Link struct {
	Name    string `json:"name"`
	SSH     string `json:"ssh,omitempty"`      // user@host[:port]: this side connects over SSH
	URL     string `json:"url,omitempty"`      // https://host/gtd: this side connects over HTTPS (linkhttp.go)
	Target  string `json:"target,omitempty"`   // where the other gtd listens, as seen from that host
	HostKey string `json:"host_key,omitempty"` // the host key seen when the link was made
	Token   string `json:"token,omitempty"`    // the token to present there (dialing side)
	PeerKey string `json:"peer_key,omitempty"` // the other server's sealing key (URL links)
	Off     bool   `json:"off,omitempty"`      // paused: this side does not connect
}

// dials says whether this side connects. If not, the other side does, and
// what is sent to the link waits for it.
func (l Link) dials() bool { return l.SSH != "" || l.URL != "" }

// active says whether this side connects now: a paused link makes no
// connection and no check at all. What is sent to it waits.
func (l Link) active() bool { return l.dials() && !l.Off }

// outEntry is a capture of this server waiting to reach a linked server.
type outEntry struct {
	Link string `json:"link"`
	Item string `json:"item"`
}

const defaultTarget = "127.0.0.1:8640"

func (s *Server) userLinks(user string) []Link {
	var links []Link
	s.st.GetUserState(user, "links", &links)
	return links
}

// linkNames are the names a user picks from when sending a capture on.
func linkNames(links []Link) []string {
	names := []string{}
	for _, l := range links {
		names = append(names, l.Name)
	}
	return names
}

func (s *Server) outbox(user string) []outEntry {
	var out []outEntry
	s.st.GetUserState(user, "outbox", &out)
	return out
}

// LinkPublicKey is the line to put into authorized_keys on a host this
// server connects to.
func (s *Server) LinkPublicKey() string {
	if s.LinkKey == nil {
		return ""
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.LinkKey.PublicKey()))) + " gtd-link-" + s.st.Server().Name
}

type linkView struct {
	Name        string `json:"name"`
	SSH         string `json:"ssh,omitempty"`         // the address this side connects to: SSH or URL
	Fingerprint string `json:"fingerprint,omitempty"` // of the pinned host key
	Waiting     int    `json:"waiting"`               // captures not delivered yet
	Off         bool   `json:"off,omitempty"`         // paused
	Token       string `json:"token,omitempty"`       // shown once, when a waiting link is made
}

// links lists, adds and removes the acting user's links.
//
// POST {name}: the other server will connect to this one. The answer carries
// the token to enter there, once.
// POST {name, ssh, target?, token}: this server connects. It does so right
// away, to see the host key and to check that the other gtd answers. An ssh
// that begins with http is the other gtd's URL: then this server connects
// over HTTPS (target is not used).
func (s *Server) links(w http.ResponseWriter, r *http.Request, a actor) error {
	links := s.userLinks(a.user)
	made := ""
	switch r.Method {
	case "POST":
		var in Link
		if err := body(r, &in); err != nil {
			return err
		}
		in.Name = strings.Join(strings.Fields(in.Name), " ")
		if in.Name == "" || len([]rune(in.Name)) > 30 || strings.ContainsAny(in.Name, ":/") {
			return &keyError{http.StatusBadRequest, "link.name", "name"}
		}
		if slices.ContainsFunc(links, func(l Link) bool { return strings.EqualFold(l.Name, in.Name) }) {
			return &keyError{http.StatusBadRequest, "link.exists", "name"}
		}
		link := Link{Name: in.Name}
		if in.SSH = strings.TrimSpace(in.SSH); in.SSH == "" {
			token, _, err := s.st.AddToken(a.user, "link "+in.Name, in.Name, []string{store.ScopeCapture, store.ScopeOutbox})
			if err != nil {
				return err
			}
			made = s.linkCode(token)
		} else if strings.HasPrefix(in.SSH, "http://") || strings.HasPrefix(in.SSH, "https://") {
			var err error
			if link.URL, err = cleanLinkURL(in.SSH); err != nil {
				return err
			}
			if link.Token, link.PeerKey = splitCode(in.Token); !strings.HasPrefix(link.Token, "gtd_") {
				return &keyError{http.StatusBadRequest, "link.token", "token"}
			}
			c, err := s.dial(r.Context(), &link)
			if err != nil {
				return err
			}
			_, err = c.get("/api/v1/outbox")
			c.close()
			if err != nil {
				return err
			}
		} else {
			if s.LinkKey == nil {
				return fmt.Errorf("%w: this server has no link key (it is made when the daemon starts)", core.ErrInvalid)
			}
			link.SSH, link.Target = in.SSH, strings.TrimSpace(in.Target)
			link.Token, _ = splitCode(in.Token)
			if link.Target == "" {
				link.Target = defaultTarget
			}
			if !strings.HasPrefix(link.Token, "gtd_") {
				return &keyError{http.StatusBadRequest, "link.token", "token"}
			}
			// Trust on first use: the host key seen now is the one this link
			// accepts from now on. Its fingerprint is shown to the user.
			c, err := s.dial(r.Context(), &link)
			if err != nil {
				return err
			}
			_, err = c.get("/api/v1/outbox")
			c.close()
			if err != nil {
				return err
			}
		}
		links = append(links, link)
	case "PUT": // {off}: pause a link this side connects to, or resume it
		var in struct {
			Off bool `json:"off"`
		}
		if err := body(r, &in); err != nil {
			return err
		}
		at := slices.IndexFunc(links, func(l Link) bool { return l.Name == r.PathValue("name") })
		if at < 0 {
			return fmt.Errorf("%w: linked server %q", core.ErrNotFound, r.PathValue("name"))
		}
		if !links[at].dials() {
			return fmt.Errorf("%w: link %q waits for the other server; there is nothing to pause here", core.ErrInvalid, links[at].Name)
		}
		links[at].Off = in.Off
		if !in.Off {
			defer s.KickLinks() // what waited goes now
		}
	case "DELETE":
		name := r.PathValue("name")
		n := len(links)
		if links = slices.DeleteFunc(links, func(l Link) bool { return l.Name == name }); len(links) == n {
			return fmt.Errorf("%w: linked server %q", core.ErrNotFound, name)
		}
		// Its tokens here are revoked. What still waits for it stays in the trash.
		if toks, err := s.st.Tokens(a.user); err == nil {
			for _, t := range toks {
				if t.Link == name {
					s.st.DeleteToken(a.user, t.ID)
				}
			}
		}
		out := slices.DeleteFunc(s.outbox(a.user), func(e outEntry) bool { return e.Link == name })
		if err := s.st.PutUserState(a.user, "outbox", out); err != nil {
			return err
		}
	}
	if r.Method != "GET" {
		if err := s.st.PutUserState(a.user, "links", links); err != nil {
			return err
		}
		select {
		case s.relink <- struct{}{}:
		default:
		}
	}
	out := []linkView{}
	waiting := s.outbox(a.user)
	toks, _ := s.st.Tokens(a.user)
	for _, l := range links {
		v := linkView{Name: l.Name, SSH: l.SSH + l.URL, Off: l.Off}
		// A link that waits: the server its token is pinned to, once it came.
		for _, t := range toks {
			if key, err := base64.RawURLEncoding.DecodeString(t.Peer); !l.dials() && t.Link == l.Name && err == nil && len(key) == 32 {
				v.Fingerprint = keyPrint(key)
			}
		}
		if key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(l.HostKey)); err == nil {
			v.Fingerprint = ssh.FingerprintSHA256(key)
		}
		for _, e := range waiting {
			if e.Link == l.Name {
				v.Waiting++
			}
		}
		if made != "" && l.Name == links[len(links)-1].Name {
			v.Token = made // shown once, to be entered on the other server
		}
		out = append(out, v)
	}
	box := "" // this server's sealing key, to compare with what the other side shows
	if s.BoxKey != nil {
		box = keyPrint(BoxPublic(s.BoxKey)[:])
	}
	return reply(w, map[string]any{"links": out, "key": s.LinkPublicKey(), "box": box})
}

// ---- the reachable side: its outbox, for the server that connects ----

type outItem struct {
	ID          string            `json:"id"`
	Text        string            `json:"text"`
	Attachments []core.Attachment `json:"attachments,omitempty"`
}

// outboxFor answers a linked server (actor.link): what waits for it, one
// attachment of such a capture, or the confirmation that one arrived.
func (s *Server) outboxFor(w http.ResponseWriter, r *http.Request, a actor) error {
	if a.link == "" {
		return errScope // only a link's token gets here
	}
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	waiting := s.outbox(a.user)
	mine := func(id string) (*core.Item, error) {
		if !slices.Contains(waiting, outEntry{a.link, id}) {
			return nil, fmt.Errorf("%w: capture %s in the outbox for %q", core.ErrNotFound, id, a.link)
		}
		return v.Item(a.user, id)
	}
	id := r.PathValue("id")
	switch {
	case r.Method == "POST": // ack
		if _, err := mine(id); err != nil {
			return err
		}
		waiting = slices.DeleteFunc(waiting, func(e outEntry) bool { return e == outEntry{a.link, id} })
		if err := s.st.PutUserState(a.user, "outbox", waiting); err != nil {
			return err
		}
		return reply(w, map[string]bool{"ok": true})
	case id != "": // an attachment
		it, err := mine(id)
		if err != nil {
			return err
		}
		sha := r.PathValue("sha")
		if !slices.ContainsFunc(it.Attachments, func(x core.Attachment) bool { return x.SHA256 == sha }) {
			return fmt.Errorf("%w: attachment %s of capture %s", core.ErrNotFound, sha, id)
		}
		http.ServeFile(w, r, s.st.BlobPath(sha))
		return nil
	}
	out := []outItem{}
	for _, e := range waiting {
		if e.Link != a.link {
			continue
		}
		if it, err := v.Item(a.user, e.Item); err == nil {
			out = append(out, outItem{ID: it.ID, Text: captureText(it), Attachments: it.Attachments})
		}
	}
	return reply(w, out)
}

func captureText(it *core.Item) string {
	if strings.TrimSpace(it.Original) != "" {
		return it.Original
	}
	return it.Title
}

// queueCapture puts a capture into the outbox for a link. The caller trashes it.
func (s *Server) queueCapture(user, link, item string) error {
	if !slices.ContainsFunc(s.userLinks(user), func(l Link) bool { return l.Name == link }) {
		return fmt.Errorf("%w: linked server %q", core.ErrNotFound, link)
	}
	out := s.outbox(user)
	if e := (outEntry{link, item}); !slices.Contains(out, e) {
		out = append(out, e)
	}
	if err := s.st.PutUserState(user, "outbox", out); err != nil {
		return err
	}
	s.wakes.wake(user, link) // if the other side listens, it comes for it now
	return nil
}

// ---- the dialing side ----

// linkConn is an open connection to a linked server.
type linkConn struct {
	link  *Link
	base  string // where the other gtd's API is, as seen through http
	http  *http.Client
	close func()
}

// dial connects to the link's host over SSH and returns a client that talks
// to the gtd there. If the link has no host key yet, the one presented is
// recorded in it (the caller stores the link).
func (s *Server) dial(ctx context.Context, link *Link) (*linkConn, error) {
	if link.URL != "" {
		return s.dialHTTP(link)
	}
	if s.LinkKey == nil {
		return nil, fmt.Errorf("%w: this server has no link key (it is made when the daemon starts)", core.ErrInvalid)
	}
	user, host, ok := strings.Cut(link.SSH, "@")
	if !ok || user == "" || host == "" {
		return nil, &keyError{http.StatusBadRequest, "link.address", "ssh"}
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "22")
	}
	cfg := &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(s.LinkKey)}, Timeout: 15 * time.Second}
	if link.HostKey == "" {
		cfg.HostKeyCallback = func(_ string, _ net.Addr, key ssh.PublicKey) error {
			link.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
			return nil
		}
	} else {
		pinned, _, _, _, err := ssh.ParseAuthorizedKey([]byte(link.HostKey))
		if err != nil {
			return nil, fmt.Errorf("link %q: stored host key: %v", link.Name, err)
		}
		cfg.HostKeyCallback = ssh.FixedHostKey(pinned)
	}
	d := net.Dialer{Timeout: cfg.Timeout}
	raw, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, &keyError{http.StatusBadGateway, "link.unreachable", "ssh"}
	}
	sc, chans, reqs, err := ssh.NewClientConn(raw, host, cfg)
	if err != nil {
		raw.Close()
		if strings.Contains(err.Error(), "host key") {
			return nil, &keyError{http.StatusBadGateway, "link.hostkey", "ssh"}
		}
		return nil, &keyError{http.StatusBadGateway, "link.ssh_refused", "ssh"}
	}
	client := ssh.NewClient(sc, chans, reqs)
	target := link.Target
	tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return client.Dial("tcp", target) }}
	return &linkConn{link: link, base: "http://gtd" + Prefix, http: &http.Client{Transport: tr, Timeout: 2 * time.Minute},
		close: func() { tr.CloseIdleConnections(); client.Close() }}, nil
}

func (c *linkConn) do(method, path, contentType string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+c.link.Token)
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, errSealed) {
			// Not the server the code came from, or changed on the way.
			return nil, &keyError{http.StatusBadGateway, "link.sealed", "ssh"}
		}
		if c.link.URL != "" {
			return nil, &keyError{http.StatusBadGateway, "link.unreachable", "ssh"}
		}
		return nil, &keyError{http.StatusBadGateway, "link.no_gtd", "target"}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 30<<20))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusForbidden && strings.Contains(string(data), "link.pinned"):
		return nil, &keyError{http.StatusBadGateway, "link.pinned", "token"} // the code is in use by another server
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, &keyError{http.StatusBadGateway, "link.token", "token"}
	case c.link.URL != "" && resp.StatusCode == http.StatusBadRequest && strings.Contains(string(data), errSealed.Error()):
		return nil, &keyError{http.StatusBadGateway, "link.sealed", "ssh"} // the other gtd could not open what we sent
	case c.link.URL != "" && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed):
		return nil, &keyError{http.StatusBadGateway, "link.no_gtd", "ssh"} // a web server, but no gtd at this path
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("linked server %q: %s %s: status %d: %s", c.link.Name, method, path, resp.StatusCode, bytes.TrimSpace(data))
	}
	return data, nil
}

func (c *linkConn) get(path string) ([]byte, error) { return c.do("GET", path, "", nil) }

// SyncLinks connects to every server this one dials, delivers what waits here
// and collects what waits there. The daemon calls it regularly, and after a
// capture was sent. It returns the number of captures moved either way.
func (s *Server) SyncLinks(ctx context.Context) (moved int, err error) {
	var users []string
	s.st.EachUser(func(u *core.User) { users = append(users, u.ID) })
	slices.Sort(users)
	for _, user := range users {
		for _, link := range s.userLinks(user) {
			if !link.active() || link.SSH != "" && s.LinkKey == nil {
				continue
			}
			n, e := s.syncLink(ctx, user, link)
			moved += n
			if e != nil && err == nil {
				err = fmt.Errorf("link %q of user %q: %w", link.Name, user, e)
			}
		}
	}
	return moved, err
}

func (s *Server) syncLink(ctx context.Context, user string, link Link) (int, error) {
	v, err := s.st.View(user)
	if err != nil {
		return 0, err
	}
	c, err := s.dial(ctx, &link)
	if err != nil {
		return 0, err
	}
	defer c.close()
	moved := 0
	here := s.st.Server().Name

	// What waits here goes there. The op ID makes a retry harmless.
	for _, e := range s.outbox(user) {
		if e.Link != link.Name {
			continue
		}
		it, err := v.Item(user, e.Item)
		if err != nil {
			continue
		}
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("text", captureText(it))
		mw.WriteField("via", "server")
		mw.WriteField("ref", here+":"+it.ID)
		mw.WriteField("op", linkOp(here, user, it.ID))
		for _, a := range it.Attachments {
			data, err := os.ReadFile(s.st.BlobPath(a.SHA256))
			if err != nil {
				return moved, fmt.Errorf("attachment %s of capture %s: %w", a.Name, it.ID, err)
			}
			writeFilePart(mw, a, data)
		}
		mw.Close()
		if _, err := c.do("POST", "/api/v1/capture", mw.FormDataContentType(), &buf); err != nil {
			return moved, err
		}
		left := slices.DeleteFunc(s.outbox(user), func(x outEntry) bool { return x == e })
		if err := s.st.PutUserState(user, "outbox", left); err != nil {
			return moved, err
		}
		moved++
	}

	// What waits there comes here.
	data, err := c.get("/api/v1/outbox")
	if err != nil {
		return moved, err
	}
	var waiting []outItem
	if err := json.Unmarshal(data, &waiting); err != nil {
		return moved, fmt.Errorf("outbox of %q: %v", link.Name, err)
	}
	for _, o := range waiting {
		var atts []core.Attachment
		for _, a := range o.Attachments {
			blob, err := c.get("/api/v1/outbox/" + url.PathEscape(o.ID) + "/att/" + a.SHA256)
			if err != nil {
				return moved, err
			}
			sha, size, err := s.st.PutBlob(bytes.NewReader(blob))
			if err != nil {
				return moved, err
			}
			if sha != a.SHA256 {
				return moved, fmt.Errorf("attachment %s of capture %s from %q arrived damaged", a.Name, o.ID, link.Name)
			}
			atts = append(atts, core.Attachment{SHA256: sha, Name: a.Name, MIME: a.MIME, Size: size})
		}
		src := core.Source{Via: "server", Ref: link.Name + ":" + o.ID}
		if _, err := v.CaptureOnce(linkOp(link.Name, user, o.ID), o.Text, "", src, atts); err != nil {
			return moved, err
		}
		if _, err := c.do("POST", "/api/v1/outbox/"+url.PathEscape(o.ID)+"/ack", "", nil); err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}

// linkOp is the op ID of a capture that travels: the same on every attempt.
func linkOp(from, user, item string) string {
	sum := sha256.Sum256([]byte(from + "\x00" + user + "\x00" + item))
	return "link-" + hex.EncodeToString(sum[:12])
}

func writeFilePart(mw *multipart.Writer, a core.Attachment, data []byte) {
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": a.Name}))
	if a.MIME != "" {
		h.Set("Content-Type", a.MIME)
	}
	if part, err := mw.CreatePart(h); err == nil {
		part.Write(data)
	}
}
