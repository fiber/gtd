package web

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	pathpkg "path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"

	"github.com/fiber/gtd/core"
	"github.com/fiber/gtd/store"
	"github.com/fiber/gtd/wizard"
)

// The web application: password login with a session cookie, the JSON API of
// SPEC §9, the wizard endpoints, and the app shell with the embedded Lit
// frontend. Everything in web/ui is served as is: there is no build step.

//go:embed ui
var uiFS embed.FS

//go:embed TUTORIAL.md
var tutorial string

const (
	cookieName  = "gtd_session"
	sessionTTL  = 90 * 24 * time.Hour
	maxFailures = 5 // failed logins before a user is locked for a minute
)

// app is the state of the web application inside Server.
type app struct {
	catalogs map[string]map[string]string // lang → key → text
	shell    []byte
	csp      string

	mu       sync.Mutex
	failures map[string]*failure
	wizards  map[string]*wizardSession
	unlocked map[string]time.Time // web session → until when administration is open
}

type failure struct {
	count int
	until time.Time
}

type wizardSession struct {
	user    string
	kind    string
	space   string
	clarify wizard.Clarify
	note    wizard.Note
	sweep   wizard.Sweep
	later   bool // clarify: other captures wait behind this one
	started time.Time
	// What the embedding index said about the item when the wizard started.
	similar  []wizard.Similar
	projects []string
}

var reImportMap = regexp.MustCompile(`(?s)<script type="importmap">(.*?)</script>`)

func (s *Server) initApp() {
	a := &app{catalogs: map[string]map[string]string{}, failures: map[string]*failure{}, wizards: map[string]*wizardSession{}, unlocked: map[string]time.Time{}}
	for _, lang := range []string{"en", "de"} {
		data, err := uiFS.ReadFile("ui/i18n/" + lang + ".json")
		if err != nil {
			panic(err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(data, &m); err != nil {
			panic(fmt.Sprintf("ui/i18n/%s.json: %v", lang, err))
		}
		a.catalogs[lang] = m
	}
	var err error
	if a.shell, err = uiFS.ReadFile("ui/index.html"); err != nil {
		panic(err)
	}
	if !bytes.Contains(a.shell, []byte(`<base href="`+Prefix+`/">`)) {
		panic("ui/index.html: <base href> does not match web.Prefix " + Prefix)
	}
	// The import map is the one inline script. The CSP allows exactly it.
	script := "'self'"
	if m := reImportMap.FindSubmatch(a.shell); m != nil {
		sum := sha256.Sum256(m[1])
		script += " 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	}
	a.csp = "default-src 'self'; script-src " + script + "; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; " +
		"object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'"
	s.app = a

	m := s.mux
	m.HandleFunc("POST /api/v1/login", s.login)
	m.HandleFunc("POST /api/v1/logout", s.logout)
	m.HandleFunc("GET /api/v1/me", s.user(s.me))
	m.HandleFunc("PUT /api/v1/me", s.user(s.setMe))
	m.HandleFunc("GET /api/v1/items/{id}", s.user(s.item))
	m.HandleFunc("GET /api/v1/items", s.user(s.items))
	m.HandleFunc("POST /api/v1/ops", s.user(s.postOps))
	m.HandleFunc("GET /api/v1/items/{id}/comments", s.user(s.comments))
	m.HandleFunc("POST /api/v1/attachments", s.user(s.upload))
	m.HandleFunc("GET /api/v1/ai/calibration", s.user(s.calibration))
	m.HandleFunc("GET /api/v1/import", s.user(s.importState))
	m.HandleFunc("POST /api/v1/import", s.user(s.importNotes))
	m.HandleFunc("GET /api/v1/att/{sha}", s.user(s.attachment))
	m.HandleFunc("POST /api/v1/render", s.user(s.render))
	m.HandleFunc("GET /api/v1/pick", s.user(s.pick))
	m.HandleFunc("GET /api/v1/inbox", s.user(s.inbox))
	m.HandleFunc("POST /api/v1/notifications/{seq}/dismiss", s.user(s.dismiss))
	m.HandleFunc("GET /api/v1/engage", s.user(s.engage))
	m.HandleFunc("GET /api/v1/projects", s.user(s.projects))
	m.HandleFunc("GET /api/v1/plan/{id}", s.user(s.plan))
	m.HandleFunc("GET /api/v1/board", s.user(s.board))
	m.HandleFunc("GET /api/v1/areas", s.user(s.areas))
	m.HandleFunc("GET /api/v1/notes", s.user(s.notes))
	m.HandleFunc("GET /api/v1/ideas", s.user(s.ideas))
	m.HandleFunc("GET /api/v1/help", s.user(s.help))
	m.HandleFunc("GET /api/v1/spaces/{id}/options", s.user(s.options))
	m.HandleFunc("GET /api/v1/spaces/{id}/templates", s.user(s.getTemplates))
	m.HandleFunc("PUT /api/v1/spaces/{id}/templates", s.user(s.putTemplates))
	m.HandleFunc("PUT /api/v1/spaces/{id}/contexts", s.user(s.putContexts))
	m.HandleFunc("PUT /api/v1/spaces/{id}/ai", s.user(s.putAIPolicy))
	m.HandleFunc("PUT /api/v1/spaces/{id}/people", s.user(s.putPeople))
	m.HandleFunc("POST /api/v1/items/{id}/tasklines", s.user(s.taskLines))
	m.HandleFunc("POST /api/v1/items/{id}/move", s.user(s.moveItem))
	m.HandleFunc("DELETE /api/v1/wizard/review", s.user(s.reviewStop))
	m.HandleFunc("POST /api/v1/wizard/{kind}/start", s.user(s.wizardStart))
	m.HandleFunc("POST /api/v1/wizard/{kind}/{session}/step", s.user(s.wizardStep))
	m.HandleFunc("PUT /api/v1/admin/users/{id}/password", s.admin(s.setPassword))
	m.HandleFunc("PUT /api/v1/me/password", s.user(s.changePassword))
	m.HandleFunc("GET /api/v1/me/links", s.user(s.links))
	m.HandleFunc("POST /api/v1/me/links", s.user(s.links))
	m.HandleFunc("DELETE /api/v1/me/links/{name}", s.user(s.links))
	m.HandleFunc("GET /api/v1/outbox", s.user(s.outboxFor))
	m.HandleFunc("GET /api/v1/outbox/{id}/att/{sha}", s.user(s.outboxFor))
	m.HandleFunc("POST /api/v1/outbox/{id}/ack", s.user(s.outboxFor))

	assets := loadAssets()
	m.HandleFunc("GET /ui/", s.static(assets))
	// The service worker is served from the app's root, so that its scope is
	// the whole app and not just /ui/.
	m.HandleFunc("GET /sw.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(assets["/sw.js"].raw)
	})
	// Every other GET is a UI route: the same shell, routing is client-side.
	m.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			fail(w, fmt.Errorf("%w: no such API route %s", core.ErrNotFound, r.URL.Path))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", a.csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(a.shell)
	})
}

// asset is one embedded UI file, ready to serve: gzipped once at startup, with
// a content hash as ETag so that browsers revalidate cheaply.
type asset struct {
	ctype string
	raw   []byte
	gz    []byte
	etag  string
}

func loadAssets() map[string]asset {
	types := map[string]string{".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8",
		".json": "application/json", ".html": "text/html; charset=utf-8", ".svg": "image/svg+xml",
		".png": "image/png", ".webmanifest": "application/manifest+json"}
	assets := map[string]asset{}
	err := fs.WalkDir(uiFS, "ui", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := uiFS.ReadFile(path)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		zw.Write(raw)
		zw.Close()
		sum := sha256.Sum256(raw)
		assets[strings.TrimPrefix(path, "ui")] = asset{types[pathpkg.Ext(path)], raw, buf.Bytes(), `"` + hex.EncodeToString(sum[:8]) + `"`}
		return nil
	})
	if err != nil {
		panic(err)
	}
	return assets
}

func (s *Server) static(assets map[string]asset) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, ok := assets[strings.TrimPrefix(r.URL.Path, "/ui")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", a.ctype)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-cache") // revalidate: the files change with the binary
		h.Set("ETag", a.etag)
		h.Set("Vary", "Accept-Encoding")
		if r.Header.Get("If-None-Match") == a.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		body := a.raw
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			h.Set("Content-Encoding", "gzip")
			body = a.gz
		}
		h.Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body)
	}
}

// t returns a catalog text. The server uses the same catalogs as the client (D-09).
func (s *Server) t(lang, key string) string {
	if v, ok := s.app.catalogs[lang][key]; ok {
		return v
	}
	if v, ok := s.app.catalogs["en"][key]; ok {
		return v
	}
	return key
}

func (s *Server) today() string { return s.st.Now().Format(time.DateOnly) }

// lang returns the user's UI language: their setting, else Accept-Language.
func (s *Server) lang(r *http.Request, user string) string {
	if u := s.st.User(user); u != nil && s.app.catalogs[u.Lang] != nil {
		return u.Lang
	}
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		if l := strings.SplitN(tag, "-", 2)[0]; s.app.catalogs[l] != nil {
			return l
		}
	}
	return "en"
}

// ---- login and sessions ----

func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// sessionUser returns the user of the request's session cookie and slides
// the session's expiry (at most one write per day).
func (s *Server) sessionUser(r *http.Request) (string, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return "", false
	}
	sess := s.st.GetSession(c.Value)
	now := s.st.Now()
	if sess == nil || now.After(sess.Expires) {
		return "", false
	}
	if sess.Expires.Sub(now) < sessionTTL-24*time.Hour {
		sess.Expires = now.Add(sessionTTL)
		s.st.PutSession(c.Value, *sess)
	}
	return sess.User, true
}

func (s *Server) setCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: value, Path: Prefix + "/", MaxAge: maxAge,
		HttpOnly: true, Secure: s.SecureCookies, SameSite: http.SameSiteLaxMode})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := body(r, &in); err != nil {
		fail(w, err)
		return
	}
	in.User = strings.ToLower(strings.TrimSpace(in.User))
	now := s.st.Now()
	a := s.app
	a.mu.Lock()
	f := a.failures[in.User]
	locked := f != nil && f.count >= maxFailures && now.Before(f.until)
	a.mu.Unlock()
	if locked {
		w.Header().Set("Retry-After", "60")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]string{"error": "login.locked"})
		return
	}
	u := s.st.User(in.User)
	// Compare against a dummy hash for unknown users, so timing does not tell them apart.
	hash := "$2a$10$7EqJtq98hPqEX7fNZaFWoOe5.rjZ1sM5cG9JjY0bZ1mT1uQe1mQeS"
	if u != nil && !u.Service && u.PasswordHash != "" {
		hash = u.PasswordHash
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil || u == nil || u.PasswordHash == "" {
		a.mu.Lock()
		if f == nil || now.After(f.until) {
			f = &failure{}
			a.failures[in.User] = f
		}
		f.count++
		f.until = now.Add(time.Minute)
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "login.failed"})
		return
	}
	a.mu.Lock()
	delete(a.failures, in.User)
	a.mu.Unlock()
	id := newToken()
	if err := s.st.PutSession(id, store.Session{User: u.ID, Created: now, Expires: now.Add(sessionTTL), Device: r.UserAgent()}); err != nil {
		fail(w, err)
		return
	}
	s.setCookie(w, id, int(sessionTTL/time.Second))
	reply(w, map[string]string{"user": u.ID})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.st.DeleteSession(c.Value)
	}
	s.setCookie(w, "", -1)
	reply(w, map[string]bool{"ok": true})
}

func (s *Server) setPassword(w http.ResponseWriter, r *http.Request, a actor) error {
	var in struct {
		Password string `json:"password"`
	}
	if err := body(r, &in); err != nil {
		return err
	}
	if len(in.Password) < 8 || len(in.Password) > 72 {
		return fmt.Errorf("%w: a password has 8 to 72 characters", core.ErrInvalid)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := s.st.UpdateUser(r.PathValue("id"), func(u *core.User) error {
		if u.Service {
			return fmt.Errorf("%w: service user %q has no web login", core.ErrInvalid, u.ID)
		}
		u.PasswordHash = string(hash)
		return nil
	}); err != nil {
		return err
	}
	return reply(w, map[string]bool{"ok": true})
}

// changePassword lets a signed-in user set a new password. It asks for the
// current one, counts wrong attempts like the login does, and ends the user's
// other sessions.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, a actor) error {
	var in struct {
		Current  string `json:"current"`
		Password string `json:"password"`
	}
	if err := body(r, &in); err != nil {
		return err
	}
	u := s.st.User(a.user)
	now := s.st.Now()
	ap := s.app
	ap.mu.Lock()
	f := ap.failures[a.user]
	locked := f != nil && f.count >= maxFailures && now.Before(f.until)
	ap.mu.Unlock()
	if locked {
		return &keyError{http.StatusTooManyRequests, "login.locked", "current"}
	}
	if u == nil || u.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Current)) != nil {
		ap.mu.Lock()
		if f == nil || now.After(f.until) {
			f = &failure{}
			ap.failures[a.user] = f
		}
		f.count++
		f.until = now.Add(time.Minute)
		ap.mu.Unlock()
		return &keyError{http.StatusBadRequest, "password.wrong", "current"}
	}
	if len(in.Password) < 8 || len(in.Password) > 72 {
		return &keyError{http.StatusBadRequest, "password.length", "password"}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := s.st.UpdateUser(a.user, func(u *core.User) error { u.PasswordHash = string(hash); return nil }); err != nil {
		return err
	}
	keep := ""
	if c, err := r.Cookie(cookieName); err == nil {
		keep = c.Value
	}
	if err := s.st.DeleteSessions(a.user, keep); err != nil {
		return err
	}
	return reply(w, map[string]bool{"ok": true})
}

// keyError is an error the client shows from its catalog: an i18n key and the
// field it belongs to.
type keyError struct {
	status     int
	key, field string
}

func (e *keyError) Error() string { return e.key }

// ---- me ----

func (s *Server) me(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	u := s.st.User(a.user)
	contexts := u.PersonalContexts
	if contexts == nil {
		contexts = []string{}
	}
	return reply(w, map[string]any{
		"user": map[string]any{"id": u.ID, "name": u.Name, "admin": slices.Contains(s.Admins, u.ID), "personal_contexts": contexts,
			"aliases": append([]string{}, u.Aliases...)},
		"ai":      s.aiInfo(),
		"lang":    s.lang(r, a.user),
		"spaces":  v.Spaces(),
		"today":   s.today(),
		"review":  s.reviewInfo(a.user),
		"presets": wizard.DatePresets(s.today()),
		"server":  s.st.Server().Name,
		"links":   linkNames(s.userLinks(a.user)),
	})
}

func (s *Server) setMe(w http.ResponseWriter, r *http.Request, a actor) error {
	var in struct {
		Lang             *string   `json:"lang"`
		PersonalContexts *[]string `json:"personal_contexts"`
		Aliases          *[]string `json:"aliases"`
		ReviewWeekday    *int      `json:"review_weekday"` // 1 Monday … 7 Sunday, 0 none
	}
	if err := body(r, &in); err != nil {
		return err
	}
	err := s.st.UpdateUser(a.user, func(u *core.User) error {
		if in.Lang != nil {
			if s.app.catalogs[*in.Lang] == nil {
				return fmt.Errorf("%w: language %q", core.ErrInvalid, *in.Lang)
			}
			u.Lang = *in.Lang
		}
		if in.ReviewWeekday != nil {
			if *in.ReviewWeekday < 0 || *in.ReviewWeekday > 7 {
				return fmt.Errorf("%w: review_weekday %d (1 Monday to 7 Sunday, 0 none)", core.ErrInvalid, *in.ReviewWeekday)
			}
			u.ReviewWeekday = *in.ReviewWeekday
		}
		if in.Aliases != nil {
			u.Aliases = nil
			for _, n := range *in.Aliases {
				if n = strings.Join(strings.Fields(n), " "); n != "" && len([]rune(n)) <= 40 && !slices.Contains(u.Aliases, n) {
					u.Aliases = append(u.Aliases, n)
				}
			}
		}
		if in.PersonalContexts != nil {
			list, err := core.CleanContexts(*in.PersonalContexts)
			if err != nil {
				return err
			}
			u.PersonalContexts = nil
			if len(list) > 0 {
				u.PersonalContexts = list
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.me(w, r, a)
}

// ---- items ----

func (s *Server) item(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	it, err := v.Find(r.PathValue("id"))
	if err != nil {
		return err
	}
	iv, err := s.itemView(v, it, s.lang(r, a.user), s.today())
	if err != nil {
		return err
	}
	w.Header().Set("ETag", strconv.FormatUint(it.Rev, 10))
	return reply(w, iv)
}

func (s *Server) items(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	st, err := v.State(q.Get("space"))
	if err != nil {
		return err
	}
	match := func(want, have string) bool { return want == "" || want == have }
	var list []*core.Item
	st.Each(func(it *core.Item) {
		if match(q.Get("kind"), it.Kind) && match(q.Get("state"), it.State) && match(q.Get("project"), it.Project) &&
			match(q.Get("owner"), it.Owner) && match(q.Get("context"), it.Context) && match(q.Get("area"), it.Area) {
			list = append(list, it)
		}
	})
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return reply(w, briefs(st, list, s.today(), nil))
}

// Prompt asks the user for a follow-up decision after an op.
type Prompt struct {
	Type    string `json:"type"` // next_action: the project has no next action left
	Space   string `json:"space"`
	Project string `json:"project"`
	Title   string `json:"title"`
	Rev     uint64 `json:"rev"`
}

// postOps applies ops to one space in one batch (SPEC §9). A create op
// without an item ID gets one here: clients do not generate IDs.
func (s *Server) postOps(w http.ResponseWriter, r *http.Request, a actor) error {
	var in struct {
		Space string    `json:"space"`
		Ops   []core.Op `json:"ops"`
	}
	if err := body(r, &in); err != nil {
		return err
	}
	if len(in.Ops) == 0 || len(in.Ops) > 50 {
		return fmt.Errorf("%w: want 1 to 50 ops", core.ErrInvalid)
	}
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	for i := range in.Ops {
		op := &in.Ops[i]
		if op.Via == "" {
			op.Via = "web"
		}
		if op.Type == core.OpCreate {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(op.Payload, &m); err != nil {
				return fmt.Errorf("%w: create payload: %v", core.ErrInvalid, err)
			}
			if _, ok := m["id"]; !ok {
				m["id"], _ = json.Marshal(s.st.NewID())
				op.Payload, _ = json.Marshal(m)
			}
		}
	}
	logged, err := v.Apply(in.Space, in.Ops...)
	if err != nil {
		return err
	}
	if logged == nil {
		logged = []core.Op{}
	}
	return reply(w, map[string]any{"ops": logged, "prompts": s.prompts(v, in.Space, logged)})
}

// prompts: when an action was completed and its project has no next action
// left, ask for one (SPEC §5.3).
func (s *Server) prompts(v *store.View, space string, logged []core.Op) []Prompt {
	out := []Prompt{}
	st, err := v.State(space)
	if err != nil {
		return out
	}
	seen := map[string]bool{}
	for _, op := range logged {
		it := st.Item(op.Item)
		if op.Type != core.OpTransition || it == nil || it.Kind != core.KindAction || it.State != core.StDone || it.Project == "" || seen[it.Project] {
			continue
		}
		seen[it.Project] = true
		if p := st.Item(it.Project); p != nil && p.State == core.StActive && !core.Alive(st, p.ID) {
			out = append(out, Prompt{"next_action", space, p.ID, p.Title, p.Rev})
		}
	}
	return out
}

type commentView struct {
	*core.Comment
	AuthorName string `json:"author_name"`
	BodyHTML   string `json:"body_html"`
}

func (s *Server) comments(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	it, err := v.Find(r.PathValue("id"))
	if err != nil {
		return err
	}
	list, err := v.Comments(it.Space, it.ID)
	if err != nil {
		return err
	}
	tk := s.tokensFor(v, it, s.lang(r, a.user))
	out := []commentView{}
	for _, c := range list {
		cv := commentView{Comment: c, AuthorName: c.Author, BodyHTML: renderMarkdown(c.Body, tk)}
		if u := s.st.User(c.Author); u != nil {
			cv.AuthorName = u.Name
		}
		out = append(out, cv)
	}
	return reply(w, out)
}

// upload stores one file as a blob. The client then attaches it with an op.
func (s *Server) upload(w http.ResponseWriter, r *http.Request, a actor) error {
	mr, err := r.MultipartReader()
	if err != nil {
		return fmt.Errorf("%w: %v", core.ErrInvalid, err)
	}
	for {
		p, err := mr.NextPart()
		if err != nil {
			return fmt.Errorf("%w: no file part", core.ErrInvalid)
		}
		if p.FormName() != "file" {
			continue
		}
		sha, size, err := s.st.PutBlob(io.LimitReader(p, maxAttachment+1))
		if err != nil {
			return err
		}
		if size > maxAttachment {
			return fmt.Errorf("%w: attachment %q is larger than %d MB", core.ErrInvalid, p.FileName(), maxAttachment>>20)
		}
		return reply(w, core.Attachment{SHA256: sha, Name: p.FileName(), MIME: p.Header.Get("Content-Type"), Size: size})
	}
}

// attachment serves a blob the user can reach through one of their items.
func (s *Server) attachment(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	sha := r.PathValue("sha")
	att, ok := v.Blob(sha)
	if !ok || !core.ValidSHA256(sha) {
		return fmt.Errorf("%w: attachment %s", core.ErrNotFound, sha)
	}
	disposition := "attachment"
	if strings.HasPrefix(att.MIME, "image/") || att.MIME == "application/pdf" {
		disposition = "inline"
	}
	if att.MIME != "" {
		w.Header().Set("Content-Type", att.MIME)
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, att.Name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'") // an uploaded file never runs script
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeFile(w, r, s.st.BlobPath(sha))
	return nil
}

// render previews Markdown the user is editing (the client never renders it).
func (s *Server) render(w http.ResponseWriter, r *http.Request, a actor) error {
	var in struct {
		Item string `json:"item"`
		Text string `json:"text"`
	}
	if err := body(r, &in); err != nil {
		return err
	}
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	it, _ := v.Find(in.Item)
	return reply(w, map[string]string{"html": renderMarkdown(in.Text, s.tokensFor(v, it, s.lang(r, a.user)))})
}

// pick is the link picker's title search (SPEC §3.3).
func (s *Server) pick(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	out := []Named{}
	for _, sp := range v.Spaces() {
		items, _ := v.Items(sp.ID, func(it *core.Item) bool {
			return it.State != core.StTrashed && it.Kind != core.KindCapture && strings.Contains(strings.ToLower(it.Title), q)
		})
		for _, it := range items {
			out = append(out, Named{it.ID, it.Title, it.State, it.Owner, it.Kind})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { // titles that start with the query first
		pi, pj := strings.HasPrefix(strings.ToLower(out[i].Title), q), strings.HasPrefix(strings.ToLower(out[j].Title), q)
		return pi && !pj
	})
	if len(out) > 8 {
		out = out[:8]
	}
	return reply(w, out)
}

// ---- lists ----

type notice struct {
	core.Notification
	Title string `json:"title"`
}

func (s *Server) inbox(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	st, err := v.State(a.user)
	if err != nil {
		return err
	}
	caps, err := s.inboxQueue(v, a.user) // the order they will be processed in
	if err != nil {
		return err
	}
	all, err := v.Notifications()
	if err != nil {
		return err
	}
	notes := []notice{}
	for _, n := range all {
		if n.Dismissed {
			continue
		}
		nt := notice{Notification: n}
		if it, err := v.Find(n.Item); err == nil {
			nt.Title = it.Title
		}
		notes = append(notes, nt)
	}
	return reply(w, map[string]any{"captures": briefs(st, caps, s.today(), nil), "notifications": notes})
}

func (s *Server) dismiss(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	seq, err := strconv.ParseUint(r.PathValue("seq"), 10, 64)
	if err != nil {
		return fmt.Errorf("%w: notification number", core.ErrInvalid)
	}
	if err := v.Dismiss(seq); err != nil {
		return err
	}
	return reply(w, map[string]bool{"ok": true})
}

// engage answers Engage and its tabs across all the user's spaces (SPEC §5.3).
func (s *Server) engage(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	today := s.today()
	until := s.st.Now().AddDate(0, 0, 14).Format(time.DateOnly)
	f := core.EngageFilter{Contexts: q["context"], Time: q.Get("time"), Energy: q.Get("energy")}
	type entry struct {
		it *core.Item
		st *core.State
	}
	var all []entry
	contexts := []wizard.Option{}
	seen := map[string]bool{}
	active := core.Active{Contexts: []string{}, Time: []string{}, Energy: []string{}}
	for _, sp := range v.Spaces() {
		st, err := v.State(sp.ID)
		if err != nil {
			return err
		}
		core.EngageActive(core.Engage(st, a.user, today, core.EngageFilter{}), f, &active)
		var list []*core.Item
		switch q.Get("tab") {
		case "today":
			list = core.Today(st, a.user, today)
		case "waiting":
			list = core.WaitingFor(st, a.user)
		case "dates":
			list = core.Dated(st, until)
		default:
			list = core.Engage(st, a.user, today, f)
		}
		for _, it := range list {
			all = append(all, entry{it, st})
		}
		for _, o := range wizard.ContextOptions(core.Contexts(st, a.user)) {
			if !seen[o.Value] {
				seen[o.Value] = true
				contexts = append(contexts, o)
			}
		}
	}
	if tab := q.Get("tab"); tab == "" || tab == "today" {
		// One order across spaces.
		items := make([]*core.Item, len(all))
		by := map[string]*core.State{}
		for i, e := range all {
			items[i], by[e.it.ID] = e.it, e.st
		}
		core.SortEngage(items, today)
		for i, it := range items {
			all[i] = entry{it, by[it.ID]}
		}
	}
	out := []Brief{}
	for _, e := range all {
		out = append(out, brief(e.st, e.it, today, nil))
	}
	slices.Sort(active.Contexts)
	slices.Sort(active.Time)
	slices.Sort(active.Energy)
	return reply(w, map[string]any{"items": out, "contexts": contexts, "active": active})
}

func (s *Server) projects(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	today := s.today()
	out := []Brief{}
	for _, sp := range v.Spaces() {
		st, err := v.State(sp.ID)
		if err != nil {
			return err
		}
		var list []*core.Item
		st.Each(func(it *core.Item) {
			if it.Kind == core.KindProject && it.State != core.StTrashed {
				list = append(list, it)
			}
		})
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		out = append(out, briefs(st, list, today, core.Rollups(st, today))...)
	}
	return reply(w, out)
}

func (s *Server) plan(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	it, err := v.Find(r.PathValue("id"))
	if err != nil {
		return err
	}
	if it.Kind != core.KindProject {
		return fmt.Errorf("%w: item %s is a %s, not a project", core.ErrInvalid, it.ID, it.Kind)
	}
	st, err := v.State(it.Space)
	if err != nil {
		return err
	}
	p := plan(st, it, s.today())
	for _, h := range s.Embed.BelongHere(v, it, 5) {
		if n := st.Item(h.ID); n != nil {
			p.Suggested = append(p.Suggested, brief(st, n, s.today(), nil))
		}
	}
	return reply(w, p)
}

func (s *Server) board(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	space := q.Get("space")
	if space == "" {
		space = a.user
	}
	if space == allSpaces {
		// One board over every space of the user. Areas and projects belong
		// to one space each, so those two filters do not apply here.
		var cols []Column
		for _, sp := range v.Spaces() {
			st, err := v.State(sp.ID)
			if err != nil {
				return err
			}
			part := board(st, s.today(), s.st.Now(), "", "", q.Get("field"))
			for i := range part {
				for j := range part[i].Items {
					part[i].Items[j].SpaceName = sp.Name
				}
			}
			cols = mergeColumns(cols, part, s.today())
		}
		return reply(w, map[string]any{"space": space, "options": &SpaceOptions{Areas: []Named{}, Projects: []Named{}, Actions: []Named{}}, "columns": cols})
	}
	st, err := v.State(space)
	if err != nil {
		return err
	}
	return reply(w, map[string]any{"space": space, "options": spaceOptions(st, a.user),
		"columns": board(st, s.today(), s.st.Now(), q.Get("area"), q.Get("project"), q.Get("field"))})
}

// areas lists the areas of the user's spaces with their open project counts
// (SPEC §5.4 step 8 flags areas with none).
func (s *Server) areas(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	type area struct {
		Brief
		Projects int `json:"projects"`
		Actions  int `json:"actions"`
	}
	out := []area{}
	for _, sp := range v.Spaces() {
		st, err := v.State(sp.ID)
		if err != nil {
			return err
		}
		var list []*core.Item
		projects, actions := map[string]int{}, map[string]int{}
		st.Each(func(it *core.Item) {
			switch {
			case it.Kind == core.KindArea:
				list = append(list, it)
			case it.Kind == core.KindProject && it.State == core.StActive:
				projects[it.Area]++
			case it.Kind == core.KindAction && !finished(it.State):
				actions[it.Area]++
			}
		})
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		for _, it := range list {
			out = append(out, area{brief(st, it, s.today(), nil), projects[it.ID], actions[it.ID]})
		}
	}
	return reply(w, out)
}

func (s *Server) options(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	st, err := v.State(r.PathValue("id"))
	if err != nil {
		return err
	}
	return reply(w, spaceOptions(st, a.user))
}

// notes lists the notes of all the user's spaces, most recently changed
// first: the home of note-taking. Filters: state, project, area and q (every
// word must occur in title or body).
func (s *Server) notes(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	words := strings.Fields(strings.ToLower(q.Get("q")))
	today := s.today()
	out := []Brief{}
	counts := map[string]int{}
	projects := []Named{}
	for _, sp := range v.Spaces() {
		st, err := v.State(sp.ID)
		if err != nil {
			return err
		}
		st.Each(func(it *core.Item) {
			if it.Kind == core.KindProject && !finished(it.State) {
				projects = append(projects, Named{ID: it.ID, Title: it.Title, State: it.State})
			}
			if it.Kind != core.KindNote || it.State == core.StTrashed {
				return
			}
			if p := q.Get("project"); p != "" && it.Project != p {
				return
			}
			if ar := q.Get("area"); ar != "" && it.Area != ar {
				return
			}
			text := strings.ToLower(it.Title + "\n" + it.Body)
			for _, word := range words {
				if !strings.Contains(text, word) {
					return
				}
			}
			counts[it.State]++ // per state, within the other filters
			if state := q.Get("state"); state != "" && it.State != state {
				return
			}
			b := brief(st, it, today, nil)
			b.Excerpt, b.SpaceName = excerpt(it.Body, 180), sp.Name
			out = append(out, b)
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Updated.Equal(out[j].Updated) {
			return out[i].Updated.After(out[j].Updated)
		}
		return out[i].ID < out[j].ID
	})
	sort.Slice(projects, func(i, j int) bool {
		return strings.ToLower(projects[i].Title)+projects[i].ID < strings.ToLower(projects[j].Title)+projects[j].ID
	})
	res := map[string]any{"notes": out, "counts": counts, "projects": projects}
	if id := q.Get("similar"); id != "" {
		// The notes most like one note, nearest first, instead of the filtered
		// list. Only notes the user can see are compared (invariant 4).
		it, err := v.Find(id)
		if err != nil {
			return err
		}
		sim := s.Embed.SimilarTo(v, it, 12, func(x *core.Item) bool { return x.Kind == core.KindNote })
		similar := []Brief{}
		for _, h := range sim.Hits {
			if h.Score < s.Embed.SuggestThreshold {
				break // best first: the rest is not similar
			}
			st, err := v.State(h.Space)
			if err != nil {
				continue
			}
			if x := st.Item(h.ID); x != nil {
				b := brief(st, x, today, nil)
				b.Excerpt, b.SpaceName, b.Score = excerpt(x.Body, 180), st.Space.Name, h.Score
				similar = append(similar, b)
			}
		}
		res["notes"], res["similar"] = similar, map[string]string{"id": it.ID, "title": it.Title, "state": sim.State}
	}
	return reply(w, res)
}

// ideas lists what is parked: someday projects (themes that collect ideas)
// with the number of notes in each, and someday actions.
func (s *Server) ideas(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	today := s.today()
	themes, actions := []Brief{}, []Brief{}
	for _, sp := range v.Spaces() {
		st, err := v.State(sp.ID)
		if err != nil {
			return err
		}
		notes := map[string]int{}
		var list []*core.Item
		st.Each(func(it *core.Item) {
			if it.Kind == core.KindNote && it.Project != "" && it.State != core.StTrashed {
				notes[it.Project]++
			}
			if it.State == core.StSomeday && (it.Kind == core.KindProject || it.Kind == core.KindAction) {
				list = append(list, it)
			}
		})
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		for _, it := range list {
			b := brief(st, it, today, nil)
			b.SpaceName = sp.Name
			if it.Kind == core.KindProject {
				b.Notes = notes[it.ID]
				themes = append(themes, b)
			} else {
				actions = append(actions, b)
			}
		}
	}
	return reply(w, map[string]any{"themes": themes, "actions": actions})
}

// help returns the tutorial as HTML.
func (s *Server) help(w http.ResponseWriter, r *http.Request, a actor) error {
	return reply(w, map[string]string{"html": renderMarkdown(tutorial, tokens{})})
}

// putContexts sets a shared space's own contexts (besides the canonical
// ones): any member may. A personal space uses its user's own contexts.
func (s *Server) putContexts(w http.ResponseWriter, r *http.Request, a actor) error {
	var in struct {
		Contexts []string `json:"contexts"`
	}
	if err := body(r, &in); err != nil {
		return err
	}
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	st, err := v.State(id) // members only
	if err != nil {
		return err
	}
	if st.Space.Kind != core.SpaceShared {
		return fmt.Errorf("%w: space %q is personal: its contexts are the user's own (settings)", core.ErrInvalid, id)
	}
	contexts, err := core.CleanContexts(in.Contexts)
	if err != nil {
		return err
	}
	if err := s.st.UpdateSpace(id, func(sp *core.Space) error { sp.Contexts = contexts; return nil }); err != nil {
		return err
	}
	return reply(w, map[string]any{"contexts": contexts})
}

// putPeople sets the people a space keeps an agenda for who have no account
// here: a boss, a customer. Each gets an "Agenda · name" context. A name
// that actions still use as their context cannot be removed.
func (s *Server) putPeople(w http.ResponseWriter, r *http.Request, a actor) error {
	var in struct {
		People []string `json:"people"`
	}
	if err := body(r, &in); err != nil {
		return err
	}
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	st, err := v.State(id) // members only
	if err != nil {
		return err
	}
	people, err := core.CleanPeople(st.Space, in.People)
	if err != nil {
		return err
	}
	for _, old := range st.Space.People {
		if slices.Contains(people, old) {
			continue
		}
		used, err := v.Items(id, func(it *core.Item) bool { return it.Context == "agenda:"+old })
		if err != nil {
			return err
		}
		if len(used) > 0 {
			return fmt.Errorf("%w: %d actions in space %s still have the context of %q (for example %s)", core.ErrConflict, len(used), id, old, used[0].ID)
		}
	}
	if err := s.st.UpdateSpace(id, func(sp *core.Space) error { sp.People = people; return nil }); err != nil {
		return err
	}
	return reply(w, map[string]any{"people": people})
}

// ---- templates (SPEC §7): a YAML textarea with validation ----

func (s *Server) getTemplates(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	st, err := v.State(r.PathValue("id"))
	if err != nil {
		return err
	}
	text := ""
	if len(st.Space.Templates) > 0 {
		y, err := yaml.Marshal(map[string]any{"templates": st.Space.Templates})
		if err != nil {
			return err
		}
		text = string(y)
	}
	return reply(w, map[string]string{"yaml": text})
}

func (s *Server) putTemplates(w http.ResponseWriter, r *http.Request, a actor) error {
	var in struct {
		YAML string `json:"yaml"`
	}
	if err := body(r, &in); err != nil {
		return err
	}
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	if _, err := v.State(id); err != nil { // members only
		return err
	}
	var doc struct {
		Templates []core.Template `yaml:"templates"`
	}
	dec := yaml.NewDecoder(strings.NewReader(in.YAML))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: %v", core.ErrInvalid, err)
	}
	if err := core.ValidateTemplates(doc.Templates); err != nil {
		return err
	}
	if err := s.st.UpdateSpace(id, func(sp *core.Space) error { sp.Templates = doc.Templates; return nil }); err != nil {
		return err
	}
	return s.getTemplates(w, r, a)
}

// ---- wizards (SPEC §5) ----

// wizardStart begins a wizard: clarify on the oldest capture of the inbox (or
// the given one), note on a note with open task lines.
func (s *Server) wizardStart(w http.ResponseWriter, r *http.Request, a actor) error {
	var in struct {
		Item  string `json:"item"`
		Space string `json:"space"` // sweep: the space to sweep (default: the personal space)
		Skip  int    `json:"skip"`  // sweep: clusters to pass over
	}
	if err := body(r, &in); err != nil {
		return err
	}
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	if r.PathValue("kind") == "review" {
		return s.reviewStart(w, r, v)
	}
	ws := &wizardSession{user: a.user, kind: r.PathValue("kind"), started: s.st.Now()}
	var it *core.Item
	switch ws.kind {
	case "clarify":
		ws.space = a.user
		caps, err := s.inboxQueue(v, a.user)
		if err != nil {
			return err
		}
		ws.later = len(caps) > 1
		for _, c := range caps {
			if in.Item == c.ID || in.Item == "" && it == nil {
				it = c
			}
		}
		if it == nil {
			if in.Item != "" {
				return fmt.Errorf("%w: capture %s in your inbox", core.ErrNotFound, in.Item)
			}
			return reply(w, map[string]any{"done": true, "empty": true})
		}
		ws.clarify = wizard.StartClarify(it)
		// Top-3 similar items (a duplicate above the threshold) and the three
		// nearest projects, if the capture has been embedded by now.
		sim := s.Embed.SimilarTo(v, it, 3, func(x *core.Item) bool { return x.Kind != core.KindCapture })
		for _, h := range sim.Hits {
			if x, err := v.Item(h.Space, h.ID); err == nil && h.Score >= s.Embed.SuggestThreshold {
				ws.similar = append(ws.similar, wizard.Similar{ID: x.ID, Space: x.Space, Title: x.Title, Kind: x.Kind, State: x.State,
					Score: h.Score, Duplicate: h.Score >= s.Embed.DupThreshold})
			}
		}
		for _, h := range s.Embed.Projects(v, it, 3) {
			ws.projects = append(ws.projects, h.ID)
		}
	case "note":
		if it, err = v.Find(in.Item); err != nil {
			return err
		}
		ok := false
		if ws.note, ok = wizard.StartNote(it); !ok {
			return fmt.Errorf("%w: item %s has no open task lines", core.ErrInvalid, it.ID)
		}
		ws.space = it.Space
		for _, h := range s.Embed.Projects(v, it, 3) {
			ws.projects = append(ws.projects, h.ID)
		}
	case "sweep":
		// One cluster of unsorted notes per session (SPEC §5.7). The clusters
		// are computed again each time: filing notes changes them.
		if ws.space = in.Space; ws.space == "" {
			ws.space = a.user
		}
		if _, err := v.State(ws.space); err != nil {
			return err
		}
		clusters, state := s.Embed.Clusters(v, ws.space)
		if in.Skip < 0 || in.Skip >= len(clusters) {
			return reply(w, map[string]any{"done": true, "empty": true, "state": state, "total": len(clusters)})
		}
		c := clusters[in.Skip]
		var notes []*core.Item
		for _, id := range c.IDs {
			if n, err := v.Item(ws.space, id); err == nil {
				notes = append(notes, n)
			}
		}
		ws.sweep = wizard.StartSweep(notes, c.Cohesion, c.Loose, in.Skip+1, len(clusters))
		if len(notes) > 0 {
			it = notes[0] // the note nearest to the centre stands for the cluster
			for _, h := range s.Embed.Projects(v, it, 3) {
				ws.projects = append(ws.projects, h.ID)
			}
		}
	default:
		return fmt.Errorf("%w: wizard %q", core.ErrNotFound, ws.kind)
	}
	if it == nil {
		return reply(w, map[string]any{"done": true, "empty": true})
	}
	env, err := s.wizardEnv(v, ws)
	if err != nil {
		return err
	}
	id := newToken()
	ap := s.app
	ap.mu.Lock()
	for k, old := range ap.wizards { // a user's abandoned sessions go after a day
		if old.user == a.user && s.st.Now().Sub(old.started) > 24*time.Hour {
			delete(ap.wizards, k)
		}
	}
	ap.wizards[id] = ws
	ap.mu.Unlock()
	return reply(w, map[string]any{"session": id, "item": it.ID, "step": ws.step(env)})
}

func (s *Server) wizardEnv(v *store.View, ws *wizardSession) (wizard.Env, error) {
	st, err := v.State(ws.space)
	if err != nil {
		return wizard.Env{}, err
	}
	env := wizard.NewEnv(st, ws.user, s.today())
	env.Similar, env.SuggestProjects = ws.similar, ws.projects
	if st.Space.Kind == core.SpaceShared {
		for _, m := range st.Space.Members {
			if u := s.st.User(m); m != ws.user && u != nil && !u.Service {
				name := u.Name
				if name == "" {
					name = m
				}
				env.Members = append(env.Members, wizard.Ref{ID: m, Title: name})
			}
		}
	}
	if ws.kind == "clarify" {
		env.Later = ws.later
		for _, l := range s.userLinks(ws.user) {
			env.Links = append(env.Links, l.Name)
		}
		// Where a capture may go: it can stay, or move to a shared space of
		// the user. Each choice says who will see it there.
		for _, sp := range v.Spaces() {
			var names []string
			for _, m := range sp.Members {
				if u := s.st.User(m); u != nil && u.Name != "" {
					names = append(names, u.Name)
				} else {
					names = append(names, m)
				}
			}
			ref := wizard.Ref{ID: sp.ID, Title: sp.Name + " · " + strings.Join(names, ", ")}
			if sp.ID == ws.user {
				env.Spaces = append([]wizard.Ref{{ID: sp.ID}}, env.Spaces...) // the personal space first
			} else {
				env.Spaces = append(env.Spaces, ref)
			}
		}
		env.SpaceHint = s.spaceHint(ws.user, ws.clarify.Item)
	}
	return env, nil
}

func (ws *wizardSession) step(env wizard.Env) wizard.Step {
	switch ws.kind {
	case "note":
		return ws.note.Step(env)
	case "sweep":
		return ws.sweep.Step(env)
	}
	return ws.clarify.Step(env)
}

// wizardStep takes one answer. At the end the server applies the wizard's ops
// in one batch and returns them.
func (s *Server) wizardStep(w http.ResponseWriter, r *http.Request, a actor) error {
	var in wizard.Input
	if err := body(r, &in); err != nil {
		return err
	}
	if r.PathValue("kind") == "review" {
		v, err := s.st.View(a.user)
		if err != nil {
			return err
		}
		return s.reviewStep(w, r, v, in)
	}
	ap := s.app
	id := r.PathValue("session")
	ap.mu.Lock()
	ws := ap.wizards[id]
	ap.mu.Unlock()
	if ws == nil || ws.user != a.user || ws.kind != r.PathValue("kind") {
		return fmt.Errorf("%w: wizard session (it may have expired: start again)", core.ErrNotFound)
	}
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	env, err := s.wizardEnv(v, ws)
	if err != nil {
		return err
	}
	var (
		ops  []core.Op
		done bool
	)
	// One answer at a time per session.
	ap.mu.Lock()
	switch ws.kind {
	case "note":
		ws.note, ops, done, err = ws.note.Next(env, in, s.st.NewID)
	case "sweep":
		ws.sweep, ops, done, err = ws.sweep.Next(env, in, s.st.NewID)
	default:
		ws.clarify, ops, done, err = ws.clarify.Next(env, in, s.st.NewID)
		if err == nil && ws.clarify.Space != "" && ws.space != ws.clarify.Space {
			// From here on the questions are about the space it moves to: its
			// projects, areas, contexts and people.
			ws.space = ws.clarify.Space
		}
	}
	if done {
		delete(ap.wizards, id)
	}
	ap.mu.Unlock()
	var ie *wizard.InputError
	if errors.As(err, &ie) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		return json.NewEncoder(w).Encode(map[string]string{"error": ie.Key, "field": ie.Field})
	}
	if err != nil {
		return err
	}
	if !done {
		if env, err = s.wizardEnv(v, ws); err != nil { // the space may have changed
			return err
		}
		return reply(w, map[string]any{"step": ws.step(env)})
	}
	if ws.kind == "clarify" && ws.clarify.Outcome == "later" {
		// Allen's multitasking exception: not skipped, put back a few places.
		if err := s.putBack(v, a.user, ws.clarify.Item); err != nil {
			return err
		}
	}
	if ws.kind == "clarify" && ws.clarify.SendTo != "" {
		// It goes into the outbox for that server, and to the trash here. It
		// is delivered by the next connection between the two.
		if err := s.queueCapture(a.user, ws.clarify.SendTo, ws.clarify.Item); err != nil {
			return err
		}
		defer s.KickLinks()
	}
	logged := []core.Op{}
	switch {
	case ws.kind == "clarify" && ws.clarify.Space != "":
		// The capture moves, and becomes what was decided, in one batch.
		if logged, err = v.Move(a.user, ws.clarify.Space, ws.clarify.Item, ops...); err != nil {
			return err
		}
	case len(ops) > 0:
		if logged, err = v.Apply(ws.space, ops...); err != nil {
			return err
		}
	}
	return reply(w, map[string]any{"done": true, "ops": logged})
}

// ---- weekly review (SPEC §5.4) ----
//
// The review's progress is stored per user (u/<user>/st/review), not in the
// daemon's memory: it can be continued on another device or after a restart.

// reviewMeta is what the server remembers between reviews.
type reviewMeta struct {
	LastDone string            `json:"last_done,omitempty"` // date of the last finished review
	Someday  map[string]string `json:"someday,omitempty"`   // someday item → date it was last looked at
}

func (s *Server) reviewMeta(user string) reviewMeta {
	var m reviewMeta
	s.st.GetUserState(user, "review-meta", &m)
	if m.Someday == nil {
		m.Someday = map[string]string{}
	}
	return m
}

// reviewInfo is what the client needs for the banner: is a review due today
// (the user's weekday, and none finished today), or in progress?
func (s *Server) reviewInfo(user string) map[string]any {
	u := s.st.User(user)
	meta := s.reviewMeta(user)
	var r wizard.Review
	started := s.st.GetUserState(user, "review", &r)
	weekday := int(s.st.Now().Weekday())
	if weekday == 0 {
		weekday = 7
	}
	return map[string]any{"weekday": u.ReviewWeekday, "last_done": meta.LastDone, "started": started,
		"due": u.ReviewWeekday == weekday && meta.LastDone != s.today()}
}

func (s *Server) reviewEnv(r *http.Request, v *store.View) (wizard.ReviewEnv, error) {
	lang := s.lang(r, v.User())
	env := wizard.ReviewEnv{User: v.User(), Today: s.today(), SomedayReviewed: s.reviewMeta(v.User()).Someday,
		FollowUp: s.t(lang, "review.follow_up"), Nudge: s.t(lang, "review.nudge")}
	for _, sp := range v.Spaces() {
		st, err := v.State(sp.ID)
		if err != nil {
			return env, err
		}
		env.Spaces = append(env.Spaces, st)
		if sp.ID == v.User() {
			st.Each(func(it *core.Item) {
				if it.Kind == core.KindCapture && it.State == core.StInbox {
					env.Inbox++
				}
			})
		}
	}
	notes, err := v.Notifications()
	if err != nil {
		return env, err
	}
	for _, n := range notes {
		if !n.Dismissed {
			env.FromOthers++
		}
	}
	return env, nil
}

// reviewAnswer sends the current step, or closes the review when no step is
// left. With save the progress is stored: that is what makes a review "in
// progress", so looking at the first step does not.
func (s *Server) reviewAnswer(w http.ResponseWriter, v *store.View, rev wizard.Review, env wizard.ReviewEnv, ops []core.Op, save bool) error {
	if ops == nil {
		ops = []core.Op{}
	}
	at, done := rev.At(env)
	if done {
		meta := s.reviewMeta(v.User())
		meta.LastDone = s.today()
		if err := s.st.PutUserState(v.User(), "review-meta", meta); err != nil {
			return err
		}
		if err := s.st.DeleteUserState(v.User(), "review"); err != nil {
			return err
		}
		return reply(w, map[string]any{"done": true, "ops": ops, "summary": rev.Stats})
	}
	if save {
		if err := s.st.PutUserState(v.User(), "review", at); err != nil {
			return err
		}
	}
	return reply(w, map[string]any{"session": "review", "step": at.Step(env), "ops": ops})
}

// reviewStart continues the user's review, or begins one.
func (s *Server) reviewStart(w http.ResponseWriter, r *http.Request, v *store.View) error {
	env, err := s.reviewEnv(r, v)
	if err != nil {
		return err
	}
	var rev wizard.Review
	started := s.st.GetUserState(v.User(), "review", &rev)
	if !started {
		rev = wizard.StartReview(s.today())
	}
	return s.reviewAnswer(w, v, rev, env, nil, started)
}

// reviewStop abandons the review in progress. What was decided in it stays
// decided; only the place in it is forgotten.
func (s *Server) reviewStop(w http.ResponseWriter, r *http.Request, a actor) error {
	if err := s.st.DeleteUserState(a.user, "review"); err != nil {
		return err
	}
	return reply(w, map[string]bool{"ok": true})
}

// reviewStep takes one answer. Its ops are applied at once, and the progress
// is stored before the next step is sent.
func (s *Server) reviewStep(w http.ResponseWriter, r *http.Request, v *store.View, in wizard.Input) error {
	env, err := s.reviewEnv(r, v)
	if err != nil {
		return err
	}
	var rev wizard.Review
	if !s.st.GetUserState(v.User(), "review", &rev) {
		rev = wizard.StartReview(s.today()) // the first answer starts it
	}
	at, done := rev.At(env)
	if done {
		return s.reviewAnswer(w, v, rev, env, nil, true)
	}
	next, space, ops, _, err := at.Next(env, in, s.st.NewID)
	var ie *wizard.InputError
	if errors.As(err, &ie) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		return json.NewEncoder(w).Encode(map[string]string{"error": ie.Key, "field": ie.Field})
	}
	if err != nil {
		return err
	}
	var logged []core.Op
	if len(ops) > 0 {
		if logged, err = v.Apply(space, ops...); err != nil {
			return err
		}
	}
	if at.Phase == wizard.PhaseSomeday && len(next.Seen) > len(at.Seen) {
		// Remember when this someday item was looked at: next week others come first.
		meta := s.reviewMeta(v.User())
		meta.Someday[next.Seen[len(next.Seen)-1]] = s.today()
		if err := s.st.PutUserState(v.User(), "review-meta", meta); err != nil {
			return err
		}
	}
	if env, err = s.reviewEnv(r, v); err != nil { // the world after the ops
		return err
	}
	return s.reviewAnswer(w, v, next, env, logged, true)
}

// moveItem moves an item, and for a project everything in it, to another
// space of the user (COLLAB §1). Without confirm it only says what would
// move and who would see it: the client shows that before the user decides.
func (s *Server) moveItem(w http.ResponseWriter, r *http.Request, a actor) error {
	var in struct {
		Space   string `json:"space"`
		Confirm bool   `json:"confirm"`
	}
	if err := body(r, &in); err != nil {
		return err
	}
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	it, err := v.Find(r.PathValue("id"))
	if err != nil {
		return err
	}
	target, err := v.State(in.Space)
	if err != nil {
		return err
	}
	if in.Space == it.Space {
		return fmt.Errorf("%w: item %s is already in space %q", core.ErrInvalid, it.ID, in.Space)
	}
	from, err := v.State(it.Space)
	if err != nil {
		return err
	}
	set, err := core.MoveSet(from, it.ID)
	if err != nil {
		return err
	}
	if !in.Confirm {
		names := []string{}
		for _, m := range target.Space.Members {
			if u := s.st.User(m); u != nil && u.Name != "" {
				names = append(names, u.Name)
			} else {
				names = append(names, m)
			}
		}
		files := 0
		for _, x := range set {
			files += len(x.Attachments)
		}
		return reply(w, map[string]any{"count": len(set), "attachments": files, "space": target.Space.Name, "members": names})
	}
	if _, err := v.Move(it.Space, in.Space, it.ID); err != nil {
		return err
	}
	return reply(w, map[string]any{"moved": len(set), "space": in.Space})
}

// aiInfo tells the client whether there is a generation backend and of
// which kind, so that it shows AI buttons only where they can work.
func (s *Server) aiInfo() map[string]any {
	if s.AI == nil {
		return map[string]any{"on": false}
	}
	return map[string]any{"on": true, "name": s.AI.Name(), "local": s.AI.Local()}
}

func jsonOf(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
