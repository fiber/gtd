// Package web holds gtd's HTTP/JSON API. In M0 it is served only on the
// daemon's Unix socket and, for a CLI without a daemon, in-process (Local).
// Both run the same handlers, so the CLI behaves the same either way.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
	"io"
	"mime"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fiber/gtd/ai"
	"github.com/fiber/gtd/auth"
	"github.com/fiber/gtd/core"
	"github.com/fiber/gtd/embed"
	"github.com/fiber/gtd/store"
)

// maxAttachment is the per-file limit (SPEC §3.2). It becomes configurable
// with the web upload in M1.
const maxAttachment = 25 << 20

type Server struct {
	st  *store.Store
	ssh *auth.SSH
	mux *http.ServeMux
	app *app

	// SecureCookies marks the session cookie Secure. The daemon sets it when
	// server.base_url is https.
	SecureCookies bool
	// AI is the generation backend (config.yaml, `ai:`), or nil. It is asked
	// only when a user presses a button, and only for spaces whose policy
	// allows this backend.
	AI ai.Provider
	// LinkKey is this server's SSH key for connecting to linked servers.
	LinkKey ssh.Signer
	// Kick, if set, asks the daemon to run SyncLinks soon.
	Kick chan struct{}
	// BoxKey is this server's private key for sealing what travels to linked
	// servers over HTTPS.
	BoxKey *[32]byte
	// wakes are the linked servers that listen for what is put into their outbox.
	wakes wakeHub
	// relink says that a link was added or removed, for WatchLinks.
	relink chan struct{}
	// Admins are the users who may administer the server (config.yaml,
	// `admins`): users, spaces, backups. Being listed is not enough: admin
	// routes also need the SSH-key challenge, not a web session (D-21).
	Admins []string

	// Embed answers similarity questions. It may be nil: then everything
	// works without similarity and says so.
	Embed *embed.Service
}

// actor is who a request acts as.
type actor struct {
	user   string // may be empty for an operator running admin commands
	admin  bool
	cookie bool   // authenticated by a web session, not by the SSH-key challenge
	link   string // authenticated by a linked server's token: the link's name
}

type actorKey struct{}

// errLog receives errors that can no longer be sent to the client.
var errLog io.Writer = os.Stderr

var (
	errDenied    = errors.New("authentication required")
	errForbidden = errors.New("this needs an admin")
	errScope     = errors.New("this token may not do that")
)

func New(st *store.Store) *Server {
	s := &Server{st: st, mux: http.NewServeMux(), relink: make(chan struct{}, 1)}
	s.ssh = auth.NewSSH(st.Server().ID, func(user string) []string {
		if u := st.User(user); u != nil {
			return u.SSHKeys
		}
		return nil
	})
	s.mux.HandleFunc("POST /api/v1/auth/ssh/challenge", s.challenge)
	s.mux.HandleFunc("POST /api/v1/auth/ssh/verify", s.verify)
	s.mux.HandleFunc("POST /api/v1/capture", s.user(s.capture))
	s.mux.HandleFunc("GET /api/v1/me/tokens", s.user(s.tokens))
	s.mux.HandleFunc("POST /api/v1/me/tokens", s.user(s.tokens))
	s.mux.HandleFunc("DELETE /api/v1/me/tokens/{id}", s.user(s.tokens))
	s.mux.HandleFunc("GET /api/v1/search", s.user(s.search))
	s.mux.HandleFunc("GET /api/v1/ops", s.user(s.ops))
	s.mux.HandleFunc("GET /api/v1/export", s.any(s.export))
	s.mux.HandleFunc("GET /api/v1/ai/status", s.user(s.aiStatus))
	s.mux.HandleFunc("POST /api/v1/admin/reindex", s.admin(s.reindex))
	s.mux.HandleFunc("POST /api/v1/admin/snapshots", s.admin(s.snapshot))
	s.mux.HandleFunc("POST /api/v1/admin/unlock", s.user(s.unlock))
	s.mux.HandleFunc("GET /api/v1/admin/users", s.admin(s.adminUsers))
	s.mux.HandleFunc("GET /api/v1/admin/spaces", s.admin(s.adminSpaces))
	s.mux.HandleFunc("POST /api/v1/admin/users", s.admin(s.addUser))
	s.mux.HandleFunc("POST /api/v1/admin/users/{id}/keys", s.admin(s.setKey))
	s.mux.HandleFunc("DELETE /api/v1/admin/users/{id}/keys", s.admin(s.setKey))
	s.mux.HandleFunc("POST /api/v1/admin/spaces", s.admin(s.createSpace))
	s.mux.HandleFunc("PUT /api/v1/admin/spaces/{id}/members/{user}", s.admin(s.setMember))
	s.mux.HandleFunc("DELETE /api/v1/admin/spaces/{id}/members/{user}", s.admin(s.setMember))
	s.initApp()
	return s
}

// Prefix is the path everything is served under over HTTP: the web UI, its
// files and the API (/gtd/api/v1/…). A reverse proxy forwards this one path,
// so gtd can share a host name with other things. The shell's <base href> and
// the session cookie's path carry the same value.
const Prefix = "/gtd"

// Handler serves the web UI and the API under Prefix, to browsers (session
// cookies) and API clients (bearer tokens). "/" redirects to the app.
func (s *Server) Handler() http.Handler {
	h := http.NewServeMux()
	h.Handle(Prefix+"/", http.StripPrefix(Prefix, s.mux))
	h.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, Prefix+"/", http.StatusFound)
	})
	return h
}

// API serves the same routes without the prefix. It is for the daemon's Unix
// socket, where only the CLI connects.
func (s *Server) API() http.Handler { return s.mux }

// ---- authentication and authorization ----

type handler func(w http.ResponseWriter, r *http.Request, a actor) error

// any resolves the actor: the operator of an in-process request, or the user
// of a session token.
func (s *Server) any(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, ok := r.Context().Value(actorKey{}).(actor)
		if !ok {
			token, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			user, valid := s.ssh.Session(token)
			link := ""
			if bearer && strings.HasPrefix(token, "gtd_") {
				// An API token (COLLAB §6). It may do what its scopes say and
				// nothing else. Every route is closed to tokens unless it is
				// named here.
				var tok store.Token
				var err error
				if user, tok, valid = s.st.TokenUser(token); valid {
					p := r.URL.Path
					ok := r.Method == "POST" && p == "/api/v1/capture" && slices.Contains(tok.Scopes, store.ScopeCapture) ||
						slices.Contains(tok.Scopes, store.ScopeOutbox) && tok.Link != "" &&
							(r.Method == "GET" && (p == "/api/v1/outbox" || strings.HasPrefix(p, "/api/v1/outbox/")) ||
								r.Method == "POST" && strings.HasPrefix(p, "/api/v1/outbox/") && strings.HasSuffix(p, "/ack") ||
								r.Method == "POST" && p == "/api/v1/outbox/ticket")
					if !ok {
						fail(w, errScope)
						return
					}
					// A link's token belongs to the first server that used it
					// through the seal, and from then on only there (linkhttp.go).
					if peer, _ := r.Context().Value(sealedBy{}).(string); tok.Link != "" {
						if tok.Peer == "" && peer != "" {
							if tok.Peer, err = s.st.PinToken(user, tok.ID, peer); err != nil {
								fail(w, err)
								return
							}
						}
						if tok.Peer != peer {
							fail(w, &keyError{http.StatusForbidden, "link.pinned", "token"})
							return
						}
					}
					link = tok.Link
				}
			}
			if !bearer {
				user, valid = s.sessionUser(r)
				// A browser says where a request comes from. Cookies only count for
				// requests from our own pages.
				if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
					valid = false
				}
			}
			u := s.st.User(user)
			if !valid || u == nil {
				fail(w, errDenied)
				return
			}
			a = actor{user: u.ID, admin: slices.Contains(s.Admins, u.ID), cookie: !bearer, link: link}
		}
		if err := h(w, r, a); err != nil {
			fail(w, err)
		}
	}
}

// user requires an acting user.
func (s *Server) user(h handler) http.HandlerFunc {
	return s.any(func(w http.ResponseWriter, r *http.Request, a actor) error {
		if s.st.User(a.user) == nil {
			return fmt.Errorf("%w: unknown user %q (set --user or GTD_USER)", core.ErrInvalid, a.user)
		}
		return h(w, r, a)
	})
}

// admin requires an admin (config.yaml) who came by the SSH-key challenge or
// direct database access. A web session is not enough by itself (D-21): in
// the browser the admin unlocks administration with their password, for a
// quarter of an hour. Admin manages users, spaces and backups. It does not
// open any space's content through a View.
func (s *Server) admin(h handler) http.HandlerFunc {
	return s.any(func(w http.ResponseWriter, r *http.Request, a actor) error {
		if !a.admin {
			return errForbidden
		}
		if a.cookie && !s.unlocked(r) {
			return &keyError{http.StatusForbidden, "admin.locked", "password"}
		}
		return h(w, r, a)
	})
}

const adminUnlock = 15 * time.Minute

// unlocked says whether the request's web session has administration open.
func (s *Server) unlocked(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	s.app.mu.Lock()
	defer s.app.mu.Unlock()
	return s.st.Now().Before(s.app.unlocked[c.Value])
}

// unlock opens administration for the admin's web session, against their
// password. Wrong attempts count like failed logins.
func (s *Server) unlock(w http.ResponseWriter, r *http.Request, a actor) error {
	var in struct {
		Password string `json:"password"`
	}
	if err := body(r, &in); err != nil {
		return err
	}
	c, err := r.Cookie(cookieName)
	if !a.admin || !a.cookie || err != nil {
		return errForbidden
	}
	u := s.st.User(a.user)
	now := s.st.Now()
	ap := s.app
	ap.mu.Lock()
	f := ap.failures[a.user]
	locked := f != nil && f.count >= maxFailures && now.Before(f.until)
	ap.mu.Unlock()
	if locked {
		return &keyError{http.StatusTooManyRequests, "login.locked", "password"}
	}
	if u == nil || u.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Password)) != nil {
		ap.mu.Lock()
		if f == nil || now.After(f.until) {
			f = &failure{}
			ap.failures[a.user] = f
		}
		f.count++
		f.until = now.Add(time.Minute)
		ap.mu.Unlock()
		return &keyError{http.StatusBadRequest, "password.wrong", "password"}
	}
	ap.mu.Lock()
	for k, until := range ap.unlocked { // forget what has run out
		if now.After(until) {
			delete(ap.unlocked, k)
		}
	}
	ap.unlocked[c.Value] = now.Add(adminUnlock)
	ap.mu.Unlock()
	return reply(w, map[string]any{"minutes": int(adminUnlock / time.Minute)})
}

// adminUsers lists the users: who they are, not what they have.
func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request, a actor) error {
	type view struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Admin    bool   `json:"admin"`
		Service  bool   `json:"service,omitempty"`
		Password bool   `json:"password"` // has one
		Keys     int    `json:"keys"`
	}
	out := []view{}
	s.st.EachUser(func(u *core.User) {
		out = append(out, view{u.ID, u.Name, slices.Contains(s.Admins, u.ID), u.Service, u.PasswordHash != "", len(u.SSHKeys)})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return reply(w, out)
}

// adminSpaces lists the shared spaces and their members. Not their content.
func (s *Server) adminSpaces(w http.ResponseWriter, r *http.Request, a actor) error {
	type view struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Members []string `json:"members"`
	}
	out := []view{}
	s.st.EachSpace(func(sp *core.Space) {
		if sp.Kind == core.SpaceShared {
			out = append(out, view{sp.ID, sp.Name, sp.Members})
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return reply(w, out)
}

func fail(w http.ResponseWriter, err error) {
	var ke *keyError
	if errors.As(err, &ke) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(ke.status)
		json.NewEncoder(w).Encode(map[string]string{"error": ke.key, "field": ke.field})
		return
	}
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, core.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, core.ErrConflict):
		code = http.StatusConflict
	case errors.Is(err, core.ErrInvalid):
		code = http.StatusBadRequest
	case errors.Is(err, errDenied), errors.Is(err, auth.ErrDenied):
		code = http.StatusUnauthorized
	case errors.Is(err, errForbidden), errors.Is(err, errScope):
		code = http.StatusForbidden
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func reply(w http.ResponseWriter, v any) error {
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(v)
}

func body(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: request body: %v", core.ErrInvalid, err)
	}
	return nil
}

func (s *Server) challenge(w http.ResponseWriter, r *http.Request) {
	var in struct {
		User string `json:"user"`
	}
	if err := body(r, &in); err != nil {
		fail(w, err)
		return
	}
	reply(w, map[string]string{"nonce": s.ssh.Challenge(in.User), "server_id": s.st.Server().ID})
}

func (s *Server) verify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Nonce      string   `json:"nonce"`
		Signatures [][]byte `json:"signatures"`
	}
	if err := body(r, &in); err != nil {
		fail(w, err)
		return
	}
	token, err := s.ssh.Verify(in.Nonce, in.Signatures)
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, map[string]string{"token": token})
}

// ---- user routes ----

// capture creates a capture in the acting user's inbox: JSON {text, source},
// or multipart with a "text" field, optional "via" and "ref" fields and any
// number of "file" parts. Two optional fields say where it belongs, so that
// nobody has to open another server's app to get something out of their head:
// "link" sends it on to the inbox of that linked server (it waits in the
// outbox until the two next talk), "space" is remembered as the space
// Clarify proposes for it.
func (s *Server) capture(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	var (
		text string
		src  core.Source
		atts []core.Attachment
		opID string // from a sender that may retry: the same op is not captured twice
		link string // the linked server it is sent on to
		toSp string // the shared space Clarify will propose
	)
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType == "multipart/form-data" {
		mr, err := r.MultipartReader()
		if err != nil {
			return fmt.Errorf("%w: %v", core.ErrInvalid, err)
		}
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("%w: %v", core.ErrInvalid, err)
			}
			if p.FormName() == "file" {
				sha, size, err := s.st.PutBlob(io.LimitReader(p, maxAttachment+1))
				if err != nil {
					return err
				}
				if size > maxAttachment {
					return fmt.Errorf("%w: attachment %q is larger than %d MB", core.ErrInvalid, p.FileName(), maxAttachment>>20)
				}
				atts = append(atts, core.Attachment{SHA256: sha, Name: p.FileName(), MIME: p.Header.Get("Content-Type"), Size: size})
				continue
			}
			val, err := io.ReadAll(io.LimitReader(p, 1<<20))
			if err != nil {
				return err
			}
			switch p.FormName() {
			case "text":
				text = string(val)
			case "via":
				src.Via = string(val)
			case "ref":
				src.Ref = string(val)
			case "op":
				opID = string(val)
			case "link":
				link = string(val)
			case "space":
				toSp = string(val)
			}
		}
	} else {
		var in struct {
			Text   string      `json:"text"`
			Source core.Source `json:"source"`
			Link   string      `json:"link"`
			Space  string      `json:"space"`
		}
		if err := body(r, &in); err != nil {
			return err
		}
		text, src, link, toSp = in.Text, in.Source, in.Link, in.Space
	}
	if strings.TrimSpace(text) == "" && len(atts) == 0 {
		return fmt.Errorf("%w: nothing to capture", core.ErrInvalid)
	}
	// Checked before anything is created: a capture must not end up in the
	// inbox here because its destination was misspelt.
	switch {
	case link != "" && toSp != "":
		return fmt.Errorf("%w: a capture goes to a linked server or to a space, not both", core.ErrInvalid)
	case link != "" && a.link != "":
		return fmt.Errorf("%w: a capture from linked server %q is not passed on to %q", core.ErrInvalid, a.link, link)
	case link != "" && !slices.ContainsFunc(s.userLinks(a.user), func(l Link) bool { return l.Name == link }):
		return fmt.Errorf("%w: linked server %q", core.ErrNotFound, link)
	case toSp != "" && !slices.ContainsFunc(v.Spaces(), func(sp *core.Space) bool { return sp.ID == toSp }):
		return fmt.Errorf("%w: space %q", core.ErrNotFound, toSp)
	}
	fallback := ""
	if strings.TrimSpace(text) == "" && strings.HasPrefix(atts[0].MIME, "image/") {
		// SPEC §3.2: a photo-only capture is titled "Photo <localized timestamp>".
		lang := s.lang(r, a.user)
		fallback = s.t(lang, "capture.photo") + " " + s.st.Now().Format(s.t(lang, "format.datetime"))
	}
	if src.Via == "" {
		src.Via = "web"
	}
	if opID != "" && (len(opID) > 80 || strings.ContainsAny(opID, "/ \x00")) {
		return fmt.Errorf("%w: op id %q", core.ErrInvalid, opID)
	}
	it, err := v.CaptureOnce(opID, text, fallback, src, atts)
	if err != nil {
		return err
	}
	if it == nil {
		return reply(w, map[string]any{"id": "", "duplicate": true})
	}
	switch {
	case link != "":
		// Into the outbox for that server and to the trash here, with a line
		// that says where it went: what Clarify's "send" does.
		if err := s.queueCapture(a.user, link, it.ID); err != nil {
			return err
		}
		defer s.KickLinks()
		if _, err := v.Apply(a.user,
			core.Op{Via: src.Via, Type: core.OpComment, Item: it.ID, Payload: sendNote(link)},
			core.Op{Via: src.Via, Type: core.OpTransition, Item: it.ID, BaseRev: it.Rev,
				Changes: []core.FieldChange{{Field: "state", New: json.RawMessage(`"` + core.StTrashed + `"`)}}}); err != nil {
			return err
		}
		return reply(w, map[string]string{"id": it.ID, "sent": link})
	case toSp != "" && toSp != a.user:
		if err := s.hintSpace(v, a.user, it.ID, toSp); err != nil {
			return err
		}
	}
	return reply(w, map[string]string{"id": it.ID})
}

func sendNote(link string) json.RawMessage {
	b, _ := json.Marshal(core.Comment{Body: "→ " + link})
	return b
}

// Hit is one search result.
type Hit struct {
	ID    string `json:"id"`
	Space string `json:"space"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	Title string `json:"title"`
	Rev   uint64 `json:"rev"` // for acting on a hit from the list
	// Similar marks a hit found by meaning, not by the words typed.
	Similar bool    `json:"similar,omitempty"`
	Score   float32 `json:"score,omitempty"`
}

// SearchResult is the answer of /search: exact hits first, then hits by
// meaning that the exact search did not find (SPEC §5.9). Semantic says
// whether the second part could be computed: when it is not "ok", the result
// is a text match only, and the client says so.
type SearchResult struct {
	Hits     []Hit  `json:"hits"`
	Semantic string `json:"semantic"` // ok | off | offline | building
}

func (s *Server) search(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	q := r.URL.Query().Get("q")
	// archived=0 leaves out what was put away: archived notes.
	skip := func(it *core.Item) bool { return r.URL.Query().Get("archived") == "0" && it.State == core.StArchived }
	res := SearchResult{Hits: []Hit{}}
	seen := map[string]bool{}
	for _, it := range v.Grep(q) {
		if skip(it) {
			continue
		}
		seen[it.ID] = true
		res.Hits = append(res.Hits, Hit{ID: it.ID, Space: it.Space, Kind: it.Kind, State: it.State, Title: it.Title, Rev: it.Rev})
	}
	sem := s.Embed.Search(r.Context(), v, q, 40)
	res.Semantic = sem.State
	shown := 0
	for _, h := range sem.Hits {
		if it, err := v.Item(h.Space, h.ID); err == nil && !seen[h.ID] && !skip(it) && shown < 20 {
			shown++
			res.Hits = append(res.Hits, Hit{ID: it.ID, Space: it.Space, Kind: it.Kind, State: it.State, Title: it.Title, Rev: it.Rev, Similar: true, Score: h.Score})
		}
	}
	return reply(w, res)
}

func (s *Server) aiStatus(w http.ResponseWriter, r *http.Request, a actor) error {
	return reply(w, s.Embed.Status())
}

// reindex drops all vectors and embeds everything again (gtd reindex).
func (s *Server) reindex(w http.ResponseWriter, r *http.Request, a actor) error {
	if s.Embed == nil {
		return fmt.Errorf("%w: reindex needs the daemon with an embedding provider configured (embed.provider in config.yaml)", core.ErrInvalid)
	}
	if err := s.Embed.Reindex(); err != nil {
		return err
	}
	return reply(w, s.Embed.Status())
}

func (s *Server) ops(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(r.URL.Query().Get("n"))
	if err != nil || n < 1 {
		return fmt.Errorf("%w: n must be a positive number", core.ErrInvalid)
	}
	space := r.URL.Query().Get("space")
	if space == "" {
		space = a.user // the personal space
	}
	ops, err := v.Ops(space, n)
	if err != nil {
		return err
	}
	if ops == nil {
		ops = []core.Op{}
	}
	return reply(w, ops)
}

// export streams an export as tar. A full export needs admin: like a snapshot
// it is a backup of the whole server. An export of named spaces needs
// membership of each (or admin).
func (s *Server) export(w http.ResponseWriter, r *http.Request, a actor) error {
	spaces := r.URL.Query()["space"]
	if !a.admin {
		if len(spaces) == 0 {
			return errForbidden
		}
		v, err := s.st.View(a.user)
		if err != nil {
			return err
		}
		for _, id := range spaces {
			if _, err := v.Items(id, func(*core.Item) bool { return false }); err != nil {
				return err
			}
		}
	}
	w.Header().Set("Content-Type", "application/x-tar")
	if err := s.st.Export(w, spaces); err != nil {
		// The status line is sent. Break the stream so the client sees a failure.
		fmt.Fprintf(errLog, "gtd: export: %v\n", err)
		panic(http.ErrAbortHandler)
	}
	return nil
}

// ---- admin routes ----

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request, a actor) error {
	var in struct {
		Label string `json:"label"`
	}
	if err := body(r, &in); err != nil {
		return err
	}
	man, err := s.st.Snapshot(in.Label)
	if err != nil {
		return err
	}
	return reply(w, map[string]string{"name": man.Name})
}

func (s *Server) addUser(w http.ResponseWriter, r *http.Request, a actor) error {
	var in struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		SSHKeys []string `json:"ssh_keys"`
	}
	if err := body(r, &in); err != nil {
		return err
	}
	u := core.User{ID: in.ID, Name: in.Name}
	for _, k := range in.SSHKeys {
		key, err := auth.NormalizeKey(k)
		if err != nil {
			return fmt.Errorf("%w: %v", core.ErrInvalid, err)
		}
		u.SSHKeys = append(u.SSHKeys, key)
	}
	if err := s.st.AddUser(u); err != nil {
		return err
	}
	return reply(w, map[string]string{"id": u.ID, "personal_space": u.ID})
}

// setKey adds (POST) or removes (DELETE) one SSH key of a user.
func (s *Server) setKey(w http.ResponseWriter, r *http.Request, a actor) error {
	var in struct {
		Key string `json:"key"`
	}
	if err := body(r, &in); err != nil {
		return err
	}
	key, err := auth.NormalizeKey(in.Key)
	if err != nil {
		return fmt.Errorf("%w: %v", core.ErrInvalid, err)
	}
	id := r.PathValue("id")
	err = s.st.UpdateUser(id, func(u *core.User) error {
		keys := []string{}
		had := false
		for _, k := range u.SSHKeys {
			if k == key {
				had = true
			} else {
				keys = append(keys, k)
			}
		}
		switch {
		case r.Method == http.MethodPost && had:
			return fmt.Errorf("%w: user %q already has this key", core.ErrConflict, id)
		case r.Method == http.MethodPost:
			keys = append(keys, key)
		case !had:
			return fmt.Errorf("%w: user %q has no such key", core.ErrNotFound, id)
		}
		u.SSHKeys = keys
		return nil
	})
	if err != nil {
		return err
	}
	return reply(w, map[string]int{"keys": len(s.st.User(id).SSHKeys)})
}

func (s *Server) createSpace(w http.ResponseWriter, r *http.Request, a actor) error {
	var in struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Members []string `json:"members"`
	}
	if err := body(r, &in); err != nil {
		return err
	}
	if err := s.st.CreateSpace(in.ID, in.Name, in.Members); err != nil {
		return err
	}
	return reply(w, map[string]string{"id": in.ID})
}

func (s *Server) setMember(w http.ResponseWriter, r *http.Request, a actor) error {
	if err := s.st.SetMember(r.PathValue("id"), r.PathValue("user"), r.Method == http.MethodPut); err != nil {
		return err
	}
	return reply(w, map[string]string{"id": r.PathValue("id")})
}

// ---- in-process transport ----

// Local returns a transport that serves requests in this process as the
// operator: the CLI's path when no daemon runs and it has opened the database
// itself. Whoever can open the database files can read them, so there is no
// challenge; user says whom user-level commands act as. It cannot be reached
// over the socket.
func (s *Server) Local(user string) http.RoundTripper { return local{s, user} }

type local struct {
	s    *Server
	user string
}

func (l local) RoundTrip(req *http.Request) (*http.Response, error) {
	pr, pw := io.Pipe()
	rw := &pipeWriter{header: http.Header{}, pw: pw, started: make(chan struct{})}
	ctx := context.WithValue(req.Context(), actorKey{}, actor{user: l.user, admin: true})
	go func() {
		defer func() {
			if p := recover(); p != nil {
				rw.start(http.StatusInternalServerError)
				pw.CloseWithError(fmt.Errorf("request aborted: %v", p))
				return
			}
			rw.start(http.StatusOK)
			pw.Close()
		}()
		l.s.mux.ServeHTTP(rw, req.WithContext(ctx))
	}()
	<-rw.started
	return &http.Response{StatusCode: rw.status, Status: http.StatusText(rw.status), Header: rw.header,
		Body: pr, Request: req, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1}, nil
}

// pipeWriter is the http.ResponseWriter of an in-process request. The body is
// streamed through a pipe, so a large export is never held in memory.
type pipeWriter struct {
	header  http.Header
	pw      *io.PipeWriter
	status  int
	started chan struct{}
}

func (w *pipeWriter) Header() http.Header { return w.header }

func (w *pipeWriter) WriteHeader(status int) { w.start(status) }

func (w *pipeWriter) start(status int) {
	if w.status == 0 {
		w.status = status
		close(w.started)
	}
}

func (w *pipeWriter) Write(p []byte) (int, error) {
	w.start(http.StatusOK)
	return w.pw.Write(p)
}

// tokens lists, creates and revokes the acting user's own API tokens. A new
// token is shown once, in the answer to its creation.
func (s *Server) tokens(w http.ResponseWriter, r *http.Request, a actor) error {
	type view struct {
		ID       string    `json:"id"`
		Label    string    `json:"label"`
		Link     string    `json:"link,omitempty"` // made for a linked server
		Scopes   []string  `json:"scopes"`
		Created  time.Time `json:"created"`
		LastUsed time.Time `json:"last_used,omitzero"`
		Token    string    `json:"token,omitempty"`
	}
	switch r.Method {
	case "POST":
		var in struct {
			Label  string   `json:"label"`
			Scopes []string `json:"scopes"`
		}
		if err := body(r, &in); err != nil {
			return err
		}
		plain, t, err := s.st.AddToken(a.user, in.Label, "", in.Scopes)
		if err != nil {
			return err
		}
		return reply(w, view{t.ID, t.Label, t.Link, t.Scopes, t.Created, t.LastUsed, plain})
	case "DELETE":
		if err := s.st.DeleteToken(a.user, r.PathValue("id")); err != nil {
			return err
		}
		return reply(w, map[string]bool{"ok": true})
	}
	list, err := s.st.Tokens(a.user)
	if err != nil {
		return err
	}
	out := []view{}
	for _, t := range list {
		out = append(out, view{t.ID, t.Label, t.Link, t.Scopes, t.Created, t.LastUsed, ""})
	}
	return reply(w, out)
}

// KickLinks asks for a delivery to linked servers soon, without waiting.
func (s *Server) KickLinks() {
	if s.Kick != nil {
		select {
		case s.Kick <- struct{}{}:
		default:
		}
	}
}
