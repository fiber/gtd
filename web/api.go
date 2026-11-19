// Package web holds gtd's HTTP/JSON API. In M0 it is served only on the
// daemon's Unix socket and, for a CLI without a daemon, in-process (Local).
// Both run the same handlers, so the CLI behaves the same either way.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/fiber/gtd/auth"
	"github.com/fiber/gtd/core"
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
}

// actor is who a request acts as.
type actor struct {
	user   string // may be empty for an operator running admin commands
	admin  bool
	cookie bool // authenticated by a web session, not by the SSH-key challenge
}

type actorKey struct{}

// errLog receives errors that can no longer be sent to the client.
var errLog io.Writer = os.Stderr

var (
	errDenied    = errors.New("authentication required")
	errForbidden = errors.New("this needs an admin")
)

func New(st *store.Store) *Server {
	s := &Server{st: st, mux: http.NewServeMux()}
	s.ssh = auth.NewSSH(st.Server().ID, func(user string) []string {
		if u := st.User(user); u != nil {
			return u.SSHKeys
		}
		return nil
	})
	s.mux.HandleFunc("POST /api/v1/auth/ssh/challenge", s.challenge)
	s.mux.HandleFunc("POST /api/v1/auth/ssh/verify", s.verify)
	s.mux.HandleFunc("POST /api/v1/capture", s.user(s.capture))
	s.mux.HandleFunc("GET /api/v1/search", s.user(s.search))
	s.mux.HandleFunc("GET /api/v1/ops", s.user(s.ops))
	s.mux.HandleFunc("GET /api/v1/export", s.any(s.export))
	s.mux.HandleFunc("POST /api/v1/admin/snapshots", s.admin(s.snapshot))
	s.mux.HandleFunc("POST /api/v1/admin/users", s.admin(s.addUser))
	s.mux.HandleFunc("POST /api/v1/admin/users/{id}/keys", s.admin(s.setKey))
	s.mux.HandleFunc("DELETE /api/v1/admin/users/{id}/keys", s.admin(s.setKey))
	s.mux.HandleFunc("POST /api/v1/admin/spaces", s.admin(s.createSpace))
	s.mux.HandleFunc("PUT /api/v1/admin/spaces/{id}/members/{user}", s.admin(s.setMember))
	s.mux.HandleFunc("DELETE /api/v1/admin/spaces/{id}/members/{user}", s.admin(s.setMember))
	s.initApp()
	return s
}

// Handler serves the API and the web UI to authenticated clients: CLI session
// tokens from the SSH-key challenge, and web session cookies.
func (s *Server) Handler() http.Handler { return s.mux }

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
			a = actor{user: u.ID, admin: u.Admin, cookie: !bearer}
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

// admin requires the admin flag and the SSH-key challenge (or direct database
// access): a web session is not enough (D-21). Admin manages users, spaces
// and backups. It does not open any space's content through a View.
func (s *Server) admin(h handler) http.HandlerFunc {
	return s.any(func(w http.ResponseWriter, r *http.Request, a actor) error {
		if !a.admin || a.cookie {
			return errForbidden
		}
		return h(w, r, a)
	})
}

func fail(w http.ResponseWriter, err error) {
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
	case errors.Is(err, errForbidden):
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
// number of "file" parts.
func (s *Server) capture(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	var (
		text string
		src  core.Source
		atts []core.Attachment
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
			}
		}
	} else {
		var in struct {
			Text   string      `json:"text"`
			Source core.Source `json:"source"`
		}
		if err := body(r, &in); err != nil {
			return err
		}
		text, src = in.Text, in.Source
	}
	if strings.TrimSpace(text) == "" && len(atts) == 0 {
		return fmt.Errorf("%w: nothing to capture", core.ErrInvalid)
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
	it, err := v.Capture(text, fallback, src, atts)
	if err != nil {
		return err
	}
	return reply(w, map[string]string{"id": it.ID})
}

// Hit is one search result.
type Hit struct {
	ID    string `json:"id"`
	Space string `json:"space"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	Title string `json:"title"`
}

func (s *Server) search(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	hits := []Hit{}
	for _, it := range v.Grep(r.URL.Query().Get("q")) {
		hits = append(hits, Hit{it.ID, it.Space, it.Kind, it.State, it.Title})
	}
	return reply(w, hits)
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
		Admin   bool     `json:"admin"`
		SSHKeys []string `json:"ssh_keys"`
	}
	if err := body(r, &in); err != nil {
		return err
	}
	u := core.User{ID: in.ID, Name: in.Name, Admin: in.Admin}
	for _, k := range in.SSHKeys {
		key, err := auth.NormalizeKey(k)
		if err != nil {
			return fmt.Errorf("%w: %v", core.ErrInvalid, err)
		}
		u.SSHKeys = append(u.SSHKeys, key)
	}
	if s.st.UserCount() == 0 && !u.Admin {
		return fmt.Errorf("%w: the first user must be an admin (--admin)", core.ErrInvalid)
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
