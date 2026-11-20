package web

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/nacl/box"
	"golang.org/x/net/websocket"

	"github.com/fiber/gtd/core"
)

// Links over HTTPS. A link's address is either an SSH address (links.go) or
// the other gtd's own URL, such as https://example.org/gtd/. Everything else
// is the same: one side connects, with the token made on the other side, and
// what goes the other way waits in the outbox there.
//
// TLS is not trusted to keep this private: a company proxy may open it. So
// every request and every answer of a link is sealed between the two gtd
// servers themselves (NaCl box: X25519, XSalsa20, Poly1305), and travels as
// one opaque binary POST to /api/v1/link. What is sealed is the ordinary API
// request, token included, so whoever sits in between sees neither the token
// nor a capture, and cannot change either without the seal breaking.
//
// The keys do not travel through the proxy to be trusted. The reachable
// side's public key is part of the code its user carries to the other server
// by hand ("gtd_…~<key>"). The connecting side's public key comes with each
// request; that it is the right server is shown by the token inside the
// seal, which only the holder of the code can have. The first key that
// arrives with a token is pinned to it: from then on the token is good only
// through the seal and only from that server, so a code that leaks later is
// of no use to anyone else.
//
// A link over HTTPS also keeps a WebSocket open to the other side, on which
// that side says "wake" when it has put something into the outbox. Then the
// capture is collected at once and not with the next minute's connection.
// The WebSocket carries nothing else, and nothing depends on it: where it
// cannot be opened (a proxy that does not pass it on), the minute remains.

const wakePing = 25 * time.Second // keeps the WebSocket alive through proxies

// cleanLinkURL checks a link's URL and returns its base, without a trailing
// slash: https, or http to this machine only (a token must not travel in the
// clear).
func cleanLinkURL(raw string) (string, error) {
	bad := &keyError{http.StatusBadRequest, "link.url", "ssh"}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", bad
	}
	host := u.Hostname()
	local := host == "localhost" || net.ParseIP(host).IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return "", bad
	}
	path := strings.TrimRight(u.Path, "/")
	if path == "" {
		path = Prefix
	}
	return u.Scheme + "://" + u.Host + path, nil
}

// ---- the seal ----

// sealedBy is the context key for the sealing key (base64) of the server a
// sealed request came from.
type sealedBy struct{}

// keyPrint is a short fingerprint of a sealing key, for people to compare.
func keyPrint(key []byte) string {
	sum := sha256.Sum256(key)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])[:27]
}

var errSealed = errors.New("the sealed message cannot be opened")

// sealTime is how far the clocks of two linked servers may differ. A sealed
// request older than this is not accepted again.
const sealTime = 10 * time.Minute

const maxSealed = 64 << 20

func seal(msg []byte, peer, priv *[32]byte) []byte {
	var nonce [24]byte
	rand.Read(nonce[:])
	return box.Seal(nonce[:], msg, &nonce, peer, priv)
}

func unseal(data []byte, peer, priv *[32]byte) ([]byte, error) {
	if len(data) < 24+box.Overhead {
		return nil, errSealed
	}
	msg, ok := box.Open(nil, data[24:], (*[24]byte)(data[:24]), peer, priv)
	if !ok {
		return nil, errSealed
	}
	return msg, nil
}

// BoxPublic is the public half of a server's sealing key.
func BoxPublic(priv *[32]byte) *[32]byte {
	k, err := ecdh.X25519().NewPrivateKey(priv[:])
	if err != nil {
		panic(err) // 32 bytes are always a key
	}
	return (*[32]byte)(k.PublicKey().Bytes())
}

// linkCode is what the reachable side hands out: the token and, if this
// server can seal, its public key.
func (s *Server) linkCode(token string) string {
	if s.BoxKey == nil {
		return token
	}
	return token + "~" + base64.RawURLEncoding.EncodeToString(BoxPublic(s.BoxKey)[:])
}

// splitCode takes a code apart. The key is empty for a bare token.
func splitCode(code string) (token, key string) {
	token, key, _ = strings.Cut(strings.TrimSpace(code), "~")
	return token, key
}

// sealedTransport sends each request sealed to the other gtd.
type sealedTransport struct {
	url  string // the other gtd, with its path
	peer *[32]byte
	priv *[32]byte
	next http.RoundTripper
}

func (t *sealedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("X-Link-Time", strconv.FormatInt(time.Now().Unix(), 10))
	var plain bytes.Buffer
	if err := req.Write(&plain); err != nil {
		return nil, err
	}
	body := append(BoxPublic(t.priv)[:], seal(plain.Bytes(), t.peer, t.priv)...)
	out, err := http.NewRequestWithContext(req.Context(), "POST", t.url+"/api/v1/link", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	out.Header.Set("Content-Type", "application/octet-stream")
	resp, err := t.next.RoundTrip(out)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSealed))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// Not from gtd's sealed door: a proxy's page, a wrong path. It is
		// reported by its status and trusted for nothing else.
		return &http.Response{StatusCode: resp.StatusCode, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(data)), Request: req}, nil
	}
	answer, err := unseal(data, t.peer, t.priv)
	if err != nil {
		return nil, err
	}
	return http.ReadResponse(bufio.NewReader(bytes.NewReader(answer)), req)
}

// dialHTTP returns a client for a link that is reached by its URL. Proxies
// are taken from the environment (HTTPS_PROXY, NO_PROXY).
func (s *Server) dialHTTP(link *Link) (*linkConn, error) {
	if s.BoxKey == nil {
		return nil, fmt.Errorf("%w: this server has no sealing key (it is made when the daemon starts)", core.ErrInvalid)
	}
	peer, err := base64.RawURLEncoding.DecodeString(link.PeerKey)
	if err != nil || len(peer) != 32 {
		return nil, &keyError{http.StatusBadRequest, "link.code", "token"}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	st := &sealedTransport{url: link.URL, peer: (*[32]byte)(peer), priv: s.BoxKey, next: tr}
	return &linkConn{link: link, base: "http://gtd", http: &http.Client{Transport: st, Timeout: 2 * time.Minute},
		close: tr.CloseIdleConnections}, nil
}

// recorder takes the answer of a sealed request, to be sealed in turn.
type recorder struct {
	code   int
	header http.Header
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) WriteHeader(code int)        { r.code = code }
func (r *recorder) Write(p []byte) (int, error) { return r.body.Write(p) }

// sealed is the one door for linked servers that come over HTTPS: it opens a
// sealed API request, lets the ordinary routes answer it, and seals the
// answer. Only requests that carry an API token are let in, so a link can do
// here exactly what its token may (capture, and its own outbox).
func (s *Server) sealed(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxSealed))
	if s.BoxKey == nil || err != nil || len(data) < 32 {
		http.Error(w, errSealed.Error(), http.StatusBadRequest)
		return
	}
	peer := (*[32]byte)(data[:32])
	plain, err := unseal(data[32:], peer, s.BoxKey)
	if err != nil {
		http.Error(w, errSealed.Error(), http.StatusBadRequest)
		return
	}
	rec := &recorder{code: http.StatusOK, header: http.Header{}}
	inner, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(plain)))
	var sent int64
	if err == nil {
		sent, _ = strconv.ParseInt(inner.Header.Get("X-Link-Time"), 10, 64)
	}
	switch age := time.Since(time.Unix(sent, 0)); {
	case err != nil:
		http.Error(rec, "sealed request: "+err.Error(), http.StatusBadRequest)
	case age > sealTime || age < -sealTime:
		http.Error(rec, fmt.Sprintf("sealed request is from another time (%s off): check the clocks of both servers", age.Round(time.Second)), http.StatusBadRequest)
	case !strings.HasPrefix(inner.Header.Get("Authorization"), "Bearer gtd_") || !strings.HasPrefix(inner.URL.Path, "/api/v1/") || inner.URL.Path == "/api/v1/link":
		http.Error(rec, "sealed request: an API request with a token is expected", http.StatusBadRequest)
	default:
		inner.Header.Del("Cookie")
		inner.RemoteAddr = r.RemoteAddr
		from := base64.RawURLEncoding.EncodeToString(peer[:])
		s.mux.ServeHTTP(rec, inner.WithContext(context.WithValue(r.Context(), sealedBy{}, from)))
	}
	var out bytes.Buffer
	(&http.Response{StatusCode: rec.code, ProtoMajor: 1, ProtoMinor: 1, Header: rec.header,
		Body: io.NopCloser(&rec.body), ContentLength: int64(rec.body.Len())}).Write(&out)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(seal(out.Bytes(), peer, s.BoxKey))
}

// ---- the reachable side: saying that something waits ----

// wakeHub holds the open WebSockets of linked servers, by user and link.
type wakeHub struct {
	mu      sync.Mutex
	subs    map[string]map[chan struct{}]bool
	tickets map[string]wakeTicket
}

// wakeTicket lets its holder open the WebSocket once, soon. It is asked for
// through the seal, so that the token never shows in the WebSocket's request.
// Whoever reads a ticket on the way can at most learn when something waits.
type wakeTicket struct {
	user, link string
	until      time.Time
}

func (h *wakeHub) ticket(user, link string) string {
	var b [16]byte
	rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tickets == nil {
		h.tickets = map[string]wakeTicket{}
	}
	for k, t := range h.tickets {
		if time.Now().After(t.until) {
			delete(h.tickets, k)
		}
	}
	h.tickets[id] = wakeTicket{user, link, time.Now().Add(time.Minute)}
	return id
}

func (h *wakeHub) redeem(id string) (wakeTicket, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.tickets[id]
	delete(h.tickets, id)
	return t, ok && time.Now().Before(t.until)
}

func (h *wakeHub) add(user, link string) chan struct{} {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs == nil {
		h.subs = map[string]map[chan struct{}]bool{}
	}
	key := user + "\x00" + link
	if h.subs[key] == nil {
		h.subs[key] = map[chan struct{}]bool{}
	}
	h.subs[key][ch] = true
	return ch
}

func (h *wakeHub) remove(user, link string, ch chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs[user+"\x00"+link], ch)
}

// wake tells the linked server, if it listens, that something waits for it.
func (h *wakeHub) wake(user, link string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[user+"\x00"+link] {
		select {
		case ch <- struct{}{}:
		default: // it knows already
		}
	}
}

// ticketFor gives a linked server (actor.link) a ticket for the WebSocket.
func (s *Server) ticketFor(w http.ResponseWriter, r *http.Request, a actor) error {
	if a.link == "" {
		return errScope // only a link's token gets here
	}
	return reply(w, map[string]string{"ticket": s.wakes.ticket(a.user, a.link)})
}

// wakeFor is the WebSocket a linked server holds open, entered with a ticket.
// It gets "wake" when a capture was put into its outbox, and "tick" in
// between. Nothing else is said on it.
func (s *Server) wakeFor(w http.ResponseWriter, r *http.Request) {
	a, ok := s.wakes.redeem(r.URL.Query().Get("ticket"))
	if !ok {
		fail(w, errDenied)
		return
	}
	websocket.Server{Handler: func(ws *websocket.Conn) {
		ch := s.wakes.add(a.user, a.link)
		defer s.wakes.remove(a.user, a.link, ch)
		gone := make(chan struct{})
		go func() { io.Copy(io.Discard, ws); close(gone) }()
		tick := time.NewTicker(wakePing)
		defer tick.Stop()
		for {
			msg := "tick"
			select {
			case <-gone:
				return
			case <-ch:
				msg = "wake"
			case <-tick.C:
			}
			ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if websocket.Message.Send(ws, msg) != nil {
				return
			}
		}
	}}.ServeHTTP(w, r)
}

// ---- the connecting side: listening ----

// WatchLinks keeps a WebSocket open to every server this one reaches by URL,
// and asks for a sync when one of them says that something waits. It follows
// links being added and removed, and returns when ctx ends.
func (s *Server) WatchLinks(ctx context.Context) {
	running := map[string]context.CancelFunc{}
	for {
		want := map[string]Link{}
		s.st.EachUser(func(u *core.User) {
			for _, l := range s.userLinks(u.ID) {
				if l.URL != "" && l.active() {
					want[u.ID+"\x00"+l.Name+"\x00"+l.URL+"\x00"+l.Token] = l
				}
			}
		})
		for key, stop := range running {
			if _, ok := want[key]; !ok {
				stop()
				delete(running, key)
			}
		}
		for key, l := range want {
			if running[key] == nil {
				c, stop := context.WithCancel(ctx)
				running[key] = stop
				go s.watchLink(c, l)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-s.relink: // a link was added or removed just now
		case <-time.After(30 * time.Second):
		}
	}
}

func (s *Server) watchLink(ctx context.Context, link Link) {
	wait := 5 * time.Second
	for ctx.Err() == nil {
		if ws, err := s.dialWake(ctx, link); err == nil {
			wait = 5 * time.Second
			stop := context.AfterFunc(ctx, func() { ws.Close() })
			s.KickLinks() // what was put there while nobody listened
			for {
				var msg string
				ws.SetReadDeadline(time.Now().Add(3 * wakePing))
				if websocket.Message.Receive(ws, &msg) != nil {
					break
				}
				if msg == "wake" {
					s.KickLinks()
				}
			}
			stop()
			ws.Close()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, 5*time.Minute)
	}
}

// dialWake opens the WebSocket of a link, through the proxy the environment
// names for its URL, if any. The ticket for it is fetched through the seal.
func (s *Server) dialWake(ctx context.Context, link Link) (*websocket.Conn, error) {
	c, err := s.dial(ctx, &link)
	if err != nil {
		return nil, err
	}
	data, err := c.do("POST", "/api/v1/outbox/ticket", "", nil)
	c.close()
	if err != nil {
		return nil, err
	}
	var t struct{ Ticket string }
	if err := json.Unmarshal(data, &t); err != nil || t.Ticket == "" {
		return nil, fmt.Errorf("link %q: no ticket for the WebSocket", link.Name)
	}
	u, err := url.Parse(link.URL)
	if err != nil {
		return nil, err
	}
	proxy, err := http.ProxyFromEnvironment(&http.Request{URL: u})
	if err != nil {
		return nil, err
	}
	conn, err := dialThrough(ctx, proxy, u)
	if err != nil {
		return nil, err
	}
	ws := "ws" + strings.TrimPrefix(link.URL, "http") + "/api/v1/outbox/wake?ticket=" + t.Ticket // http → ws, https → wss
	cfg, err := websocket.NewConfig(ws, u.Scheme+"://"+u.Host)
	if err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	sock, err := websocket.NewClient(cfg, conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	return sock, nil
}

// dialThrough connects to the host of u, directly or through an HTTP proxy
// (CONNECT), and starts TLS for https.
func dialThrough(ctx context.Context, proxy, u *url.URL) (net.Conn, error) {
	target := u.Host
	if u.Port() == "" {
		target = net.JoinHostPort(u.Hostname(), map[string]string{"https": "443", "http": "80"}[u.Scheme])
	}
	d := net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	var err error
	if proxy == nil {
		conn, err = d.DialContext(ctx, "tcp", target)
	} else {
		if proxy.Scheme != "http" {
			return nil, fmt.Errorf("proxy %s: only http proxies are supported for the WebSocket", proxy.Redacted())
		}
		addr := proxy.Host
		if proxy.Port() == "" {
			addr = net.JoinHostPort(proxy.Hostname(), "80")
		}
		if conn, err = d.DialContext(ctx, "tcp", addr); err == nil {
			err = connect(conn, proxy, target)
		}
	}
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		return nil, err
	}
	if u.Scheme == "https" {
		tc := tls.Client(conn, &tls.Config{ServerName: u.Hostname()})
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tc
	}
	return conn, nil
}

// connect asks an HTTP proxy for a tunnel to target.
func connect(conn net.Conn, proxy *url.URL, target string) error {
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	defer conn.SetDeadline(time.Time{})
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if proxy.User != nil {
		pass, _ := proxy.User.Password()
		req += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(proxy.User.Username()+":"+pass)) + "\r\n"
	}
	if _, err := io.WriteString(conn, req+"\r\n"); err != nil {
		return err
	}
	// Nothing follows the proxy's answer until this side speaks, so the
	// reader's buffer holds nothing of the tunnel.
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("proxy %s: CONNECT %s: %s", proxy.Host, target, resp.Status)
	}
	return nil
}
