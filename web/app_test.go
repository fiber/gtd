package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/fiber/gtd/core"
	"github.com/fiber/gtd/store"
)

// site is a test server with three users. robin and dana share the space net;
// jo is a member of nothing but her personal space.
type site struct {
	t   *testing.T
	st  *store.Store
	srv *Server
	url string
}

const password = "correct horse"

func newSite(t *testing.T) *site {
	st, err := store.Create(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	for _, u := range []core.User{{ID: "robin", Name: "Robin", Lang: "en"}, {ID: "dana", Name: "Dana", Lang: "en"}, {ID: "jo", Name: "Jo", Lang: "de"}} {
		u.PasswordHash = string(hash)
		if err := st.AddUser(u); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateSpace("net", "NET", []string{"robin", "dana"}); err != nil {
		t.Fatal(err)
	}
	app := New(st)
	app.Admins = []string{"robin"}
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(func() { srv.Close(); st.Close() })
	return &site{t, st, app, srv.URL + Prefix}
}

// client is one browser: a cookie jar.
type client struct {
	s    *site
	http *http.Client
}

func (s *site) anon() *client {
	jar, _ := cookiejar.New(nil)
	return &client{s, &http.Client{Jar: jar}}
}

func (s *site) login(user string) *client {
	c := s.anon()
	if code, body := c.do("POST", "/api/v1/login", map[string]string{"user": user, "password": password}, nil); code != 200 {
		s.t.Fatalf("login %s: %d %s", user, code, body)
	}
	return c
}

// do sends JSON and decodes the answer into out (if not nil). It returns the
// status and the raw body.
func (c *client) do(method, path string, in, out any) (int, string) {
	c.s.t.Helper()
	var body io.Reader
	if in != nil {
		data, _ := json.Marshal(in)
		body = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, c.s.url+path, body)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.s.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if out != nil && res.StatusCode == 200 {
		if err := json.Unmarshal(data, out); err != nil {
			c.s.t.Fatalf("%s %s: %v in %s", method, path, err, data)
		}
	}
	return res.StatusCode, string(data)
}

func (c *client) ok(method, path string, in, out any) {
	c.s.t.Helper()
	if code, body := c.do(method, path, in, out); code != 200 {
		c.s.t.Fatalf("%s %s: %d %s", method, path, code, body)
	}
}

// create makes an item and returns its ID.
func (c *client) create(space string, fields map[string]any) string {
	c.s.t.Helper()
	var res struct{ Ops []core.Op }
	c.ok("POST", "/api/v1/ops", map[string]any{"space": space, "ops": []map[string]any{{"type": "create", "payload": fields}}}, &res)
	return res.Ops[0].Item
}

func TestShellAndStatic(t *testing.T) {
	s := newSite(t)
	c := s.anon()
	// Outside the prefix there is nothing but a redirect into it.
	bare := strings.TrimSuffix(s.url, Prefix)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if res, err := noFollow.Get(bare + "/"); err != nil || res.StatusCode != 302 || res.Header.Get("Location") != Prefix+"/" {
		t.Errorf("GET /: %v %v", res.StatusCode, res.Header.Get("Location"))
	}
	if res, err := noFollow.Get(bare + Prefix); err != nil || (res.StatusCode != 301 && res.StatusCode != 307) || res.Header.Get("Location") != Prefix+"/" {
		t.Errorf("GET %s: %v %v", Prefix, res.StatusCode, res.Header.Get("Location"))
	}
	for _, path := range []string{"/api/v1/me", "/ui/app.js", "/inbox", "/i/01ABC"} {
		if res, _ := noFollow.Get(bare + path); res.StatusCode != 404 {
			t.Errorf("GET %s outside the prefix: %d, want 404", path, res.StatusCode)
		}
	}
	for _, path := range []string{"/", "/inbox", "/i/01ABC", "/p/x", "/settings", "/anything/else"} {
		res, err := c.http.Get(s.url + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		csp := res.Header.Get("Content-Security-Policy")
		if res.StatusCode != 200 || !strings.Contains(string(body), "<gtd-app>") || !strings.Contains(string(body), `<base href="/gtd/">`) || !strings.Contains(csp, "script-src 'self' 'sha256-") || strings.Contains(csp, "unsafe-eval") ||
			!strings.Contains(csp, "base-uri 'self'") /* the shell's <base href> must be allowed */ {
			t.Errorf("%s: %d, csp %q", path, res.StatusCode, csp)
		}
	}
	for path, want := range map[string]string{"/ui/app.js": "javascript", "/ui/style.css": "css", "/ui/vendor/lit-core.min.js": "javascript", "/ui/i18n/de.json": "json"} {
		res, _ := c.http.Get(s.url + path)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Type"), want) {
			t.Errorf("%s: %d %s", path, res.StatusCode, res.Header.Get("Content-Type"))
		}
	}
	if code, body := c.do("GET", "/api/v1/nope", nil, nil); code != 404 || !strings.Contains(body, "error") {
		t.Errorf("unknown API route: %d %s", code, body)
	}
}

// TestNoBuildNoThirdParty: the UI is plain files, and nothing in it points at
// another origin (M1 acceptance: no request leaves the server's origin).
func TestNoBuildNoThirdParty(t *testing.T) {
	reURL := regexp.MustCompile(`https?://[^\s"'<>)]+`)
	var files []string
	filepath.WalkDir("ui", func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	for _, f := range files {
		switch filepath.Ext(f) {
		case ".js", ".css", ".html", ".json":
		case ".svg", ".png":
			continue // an image; an SVG's xmlns is a name, not a request
		case ".webmanifest": // the install manifest: JSON, checked like the scripts
		default:
			t.Errorf("%s: unexpected file type in the UI (no build artefacts, no TypeScript)", f)
		}
		data, _ := os.ReadFile(f)
		src := string(data)
		if strings.Contains(f, "vendor") {
			continue // the vendored Lit bundle: checked for imports below
		}
		for _, u := range reURL.FindAllString(src, -1) {
			t.Errorf("%s refers to %s", f, u)
		}
		for _, m := range regexp.MustCompile(`from '([^']+)'`).FindAllStringSubmatch(src, -1) {
			if m[1] != "lit" && !strings.HasPrefix(m[1], "./") {
				t.Errorf("%s imports %q: only lit and local modules are allowed", f, m[1])
			}
		}
	}
	lit, _ := os.ReadFile(filepath.Join("ui", "vendor", "lit-core.min.js"))
	if regexp.MustCompile(`\bimport\b[^;]*from|import\(`).Match(lit) {
		t.Error("the vendored Lit bundle imports other modules")
	}
	for _, name := range []string{"package.json", "node_modules", "tsconfig.json"} {
		if _, err := os.Stat(filepath.Join("..", name)); err == nil {
			t.Errorf("%s exists: the frontend has no build chain", name)
		}
	}
}

func TestLoginAndSessions(t *testing.T) {
	s := newSite(t)
	routes := []string{"/api/v1/me", "/api/v1/inbox", "/api/v1/engage", "/api/v1/projects", "/api/v1/board", "/api/v1/areas",
		"/api/v1/search?q=x", "/api/v1/items?space=robin", "/api/v1/items/x", "/api/v1/att/" + strings.Repeat("a", 64)}
	for _, r := range routes {
		if code, _ := s.anon().do("GET", r, nil, nil); code != 401 {
			t.Errorf("anonymous GET %s: %d, want 401", r, code)
		}
	}
	if code, _ := s.anon().do("POST", "/api/v1/ops", map[string]any{"space": "robin"}, nil); code != 401 {
		t.Errorf("anonymous POST /ops: %d", code)
	}

	c := s.anon()
	if code, body := c.do("POST", "/api/v1/login", map[string]string{"user": "robin", "password": "wrong"}, nil); code != 401 || !strings.Contains(body, "login.failed") {
		t.Fatalf("wrong password: %d %s", code, body)
	}
	if code, body := c.do("POST", "/api/v1/login", map[string]string{"user": "nobody", "password": password}, nil); code != 401 || !strings.Contains(body, "login.failed") {
		t.Fatalf("unknown user: %d %s", code, body)
	}
	res, err := c.http.Post(s.url+"/api/v1/login", "application/json", strings.NewReader(`{"user":"Robin","password":"`+password+`"}`))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("login: %v %v", err, res.Status)
	}
	res.Body.Close()
	ck := res.Cookies()[0]
	if ck.Path != "/gtd/" {
		t.Errorf("session cookie path %q: it must not be sent to other applications on the host", ck.Path)
	}
	if ck.Name != cookieName || !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode || ck.MaxAge < 89*24*3600 || len(ck.Value) != 64 {
		t.Errorf("session cookie: %+v", ck)
	}
	var me struct {
		User   struct{ ID, Name string }
		Lang   string
		Spaces []core.Space
		Today  string
	}
	c.ok("GET", "/api/v1/me", nil, &me)
	if me.User.ID != "robin" || me.Lang != "en" || len(me.Spaces) != 2 || me.Today == "" {
		t.Errorf("me = %+v", me)
	}

	// A web session is not enough for admin routes (D-21: SSH-key challenge).
	if code, _ := c.do("POST", "/api/v1/admin/users", map[string]string{"id": "mallory"}, nil); code != 403 {
		t.Errorf("admin route with a web session: %d, want 403", code)
	}
	// The cookie does not count for requests another site makes.
	req, _ := http.NewRequest("GET", s.url+"/api/v1/me", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	res, _ = c.http.Do(req)
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Errorf("cross-site request with the cookie: %d, want 401", res.StatusCode)
	}

	c.ok("PUT", "/api/v1/me", map[string]any{"lang": "de", "personal_contexts": []string{"boat", " Garage "}}, nil)
	if u := s.st.User("robin"); u.Lang != "de" || strings.Join(u.PersonalContexts, ",") != "boat,Garage" /* names keep their capitals */ {
		t.Errorf("user after PUT /me: %+v", u)
	}
	if code, _ := c.do("PUT", "/api/v1/me", map[string]any{"lang": "fr"}, nil); code != 400 {
		t.Errorf("unknown language: %d", code)
	}

	c.ok("POST", "/api/v1/logout", nil, nil)
	if code, _ := c.do("GET", "/api/v1/me", nil, nil); code != 401 {
		t.Errorf("after logout: %d", code)
	}

	// Five failures lock the user for a minute, even with the right password.
	d := s.anon()
	for i := 0; i < maxFailures; i++ {
		d.do("POST", "/api/v1/login", map[string]string{"user": "dana", "password": "wrong"}, nil)
	}
	if code, body := d.do("POST", "/api/v1/login", map[string]string{"user": "dana", "password": password}, nil); code != 429 || !strings.Contains(body, "login.locked") {
		t.Errorf("after %d failures: %d %s", maxFailures, code, body)
	}
}

// TestIsolationOverHTTP calls every route that reaches a space as a
// non-member and expects 404, the same answer as for something that does not
// exist (STORAGE §2.2).
func TestIsolationOverHTTP(t *testing.T) {
	s := newSite(t)
	robin, dana, jo := s.login("robin"), s.login("dana"), s.login("jo")
	project := robin.create("net", map[string]any{"kind": "project", "title": "Secret NET project"})
	note := robin.create("net", map[string]any{"kind": "note", "title": "Secret NET note", "project": project, "body": "- [ ] secret task"})
	robin.create("net", map[string]any{"kind": "action", "title": "Secret NET action", "project": project, "owner": "dana"})
	sha := upload(t, robin, note, "secret.txt", "secret content")
	private := robin.create("robin", map[string]any{"kind": "note", "title": "Robin's private note"})

	type route struct {
		method, path string
		body         any
	}
	routes := []route{
		{"GET", "/api/v1/items/" + note, nil},
		{"GET", "/api/v1/items/" + note + "/comments", nil},
		{"GET", "/api/v1/items?space=net", nil},
		{"GET", "/api/v1/plan/" + project, nil},
		{"GET", "/api/v1/board?space=net", nil},
		{"GET", "/api/v1/spaces/net/options", nil},
		{"GET", "/api/v1/spaces/net/templates", nil},
		{"PUT", "/api/v1/spaces/net/templates", map[string]string{"yaml": ""}},
		{"GET", "/api/v1/att/" + sha, nil},
		{"GET", "/api/v1/ops?space=net&n=5", nil},
		{"GET", "/api/v1/export?space=net", nil},
		{"POST", "/api/v1/ops", map[string]any{"space": "net", "ops": []map[string]any{{"type": "create", "payload": map[string]any{"kind": "note", "title": "intruder"}}}}},
		{"POST", "/api/v1/ops", map[string]any{"space": "net", "ops": []map[string]any{{"type": "comment", "item": note, "payload": map[string]any{"body": "hi"}}}}},
		{"POST", "/api/v1/wizard/note/start", map[string]string{"item": note}},
	}
	for _, r := range routes {
		if code, body := dana.do(r.method, r.path, r.body, nil); code != 200 {
			t.Errorf("member: %s %s: %d %s", r.method, r.path, code, body)
		}
		code, body := jo.do(r.method, r.path, r.body, nil)
		if code != 404 {
			t.Errorf("non-member: %s %s: %d, want 404 (%s)", r.method, r.path, code, body)
		}
		if strings.Contains(body, "Secret") || strings.Contains(body, "secret") {
			t.Errorf("non-member: %s %s leaks content: %s", r.method, r.path, body)
		}
		// Something that does not exist answers the same way.
		missing := strings.NewReplacer("net", "nope", note, "01NOPE", project, "01NOPE", sha, strings.Repeat("0", 64)).Replace(r.path)
		if r.body == nil {
			if code, _ := jo.do(r.method, missing, nil, nil); code != 404 {
				t.Errorf("missing: %s %s: %d, want 404", r.method, missing, code)
			}
		}
	}
	// Routes without a space must only ever show the user's own spaces.
	for _, path := range []string{"/api/v1/search?q=secret", "/api/v1/pick?q=secret", "/api/v1/engage", "/api/v1/engage?tab=waiting",
		"/api/v1/engage?tab=dates", "/api/v1/projects", "/api/v1/areas", "/api/v1/inbox", "/api/v1/me", "/api/v1/board"} {
		code, body := jo.do("GET", path, nil, nil)
		if code != 200 || strings.Contains(body, "Secret") || strings.Contains(body, `"net"`) {
			t.Errorf("jo GET %s: %d %s", path, code, body)
		}
	}
	if _, body := dana.do("GET", "/api/v1/search?q=secret", nil, nil); !strings.Contains(body, "Secret NET note") {
		t.Errorf("dana's search misses the shared note: %s", body)
	}
	// A co-member of net still cannot see robin's personal space.
	if code, _ := dana.do("GET", "/api/v1/items/"+private, nil, nil); code != 404 {
		t.Errorf("dana reads robin's private note: %d", code)
	}
	// A link to an item the reader cannot see renders as "unavailable item".
	shared := robin.create("net", map[string]any{"kind": "note", "title": "Shared", "body": "see [[" + private + "]]"})
	var iv ItemView
	dana.ok("GET", "/api/v1/items/"+shared, nil, &iv)
	if !strings.Contains(iv.BodyHTML, "unavailable item") || strings.Contains(iv.BodyHTML, "private") || strings.Contains(iv.BodyEdit, "private") {
		t.Errorf("dana sees through the link: %s / %s", iv.BodyHTML, iv.BodyEdit)
	}
	robin.ok("GET", "/api/v1/items/"+shared, nil, &iv)
	if !strings.Contains(iv.BodyHTML, "Robin&#39;s private note") || iv.BodyEdit != "see [[Robin's private note]]" {
		t.Errorf("robin's own link: %s / %s", iv.BodyHTML, iv.BodyEdit)
	}
}

func upload(t *testing.T, c *client, item, name, content string) string {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("file", name)
	part.Write([]byte(content))
	mw.Close()
	res, err := c.http.Post(c.s.url+"/api/v1/attachments", mw.FormDataContentType(), &buf)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("upload: %v %v", err, res.Status)
	}
	var att core.Attachment
	json.NewDecoder(res.Body).Decode(&att)
	res.Body.Close()
	var iv ItemView
	c.ok("GET", "/api/v1/items/"+item, nil, &iv)
	c.ok("POST", "/api/v1/ops", map[string]any{"space": iv.Space, "ops": []map[string]any{{"type": "attach", "item": item, "payload": att}}}, nil)
	return att.SHA256
}

// TestGTDLoopOverHTTP: capture → clarify (wizard endpoints) → engage → done,
// with the project prompt, body_html and the editor form of the body.
func TestGTDLoopOverHTTP(t *testing.T) {
	s := newSite(t)
	c := s.login("robin")
	var cap struct{ ID string }
	c.ok("POST", "/api/v1/capture", map[string]any{"text": "engine thing <script>alert(1)</script>\nsecond line"}, &cap)
	var inbox struct{ Captures []Brief }
	c.ok("GET", "/api/v1/inbox", nil, &inbox)
	if len(inbox.Captures) != 1 || inbox.Captures[0].ID != cap.ID {
		t.Fatalf("inbox = %+v", inbox)
	}

	type stepRes struct {
		Session string
		Item    string
		Step    struct {
			Key     string
			Type    string
			Options []struct{ Value string }
		}
		Done bool
		Ops  []core.Op
	}
	var w stepRes
	c.ok("POST", "/api/v1/wizard/clarify/start", map[string]string{}, &w)
	if w.Item != cap.ID || w.Step.Key != "what" {
		t.Fatalf("start = %+v", w)
	}
	session := w.Session
	answer := func(choice string, kv ...string) stepRes {
		t.Helper()
		values := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			values[kv[i]] = kv[i+1]
		}
		var r stepRes
		c.ok("POST", "/api/v1/wizard/clarify/"+session+"/step", map[string]any{"choice": choice, "values": values}, &r)
		return r
	}
	// A rejected answer names the field and an i18n key, and keeps the step.
	if code, body := c.do("POST", "/api/v1/wizard/clarify/"+session+"/step", map[string]any{"choice": "action", "values": map[string]string{"title": ""}}, nil); code != 400 || !strings.Contains(body, `"err.required"`) || !strings.Contains(body, `"field":"title"`) {
		t.Fatalf("empty title: %d %s", code, body)
	}
	// Another user cannot answer in this session.
	if code, _ := s.login("dana").do("POST", "/api/v1/wizard/clarify/"+session+"/step", map[string]any{"choice": "continue"}, nil); code != 404 {
		t.Errorf("foreign wizard session: %d", code)
	}
	// robin has a shared space; untouched, the capture stays private and
	// Clarify goes straight on.
	if r := answer("project", "title", "Engine survives resets", "body", "notes"); r.Step.Key != "project" {
		t.Fatalf("the first screen did not lead to the project step: %+v", r)
	}
	if r := answer("continue", "title", "Engine survives resets"); r.Step.Key != "action" {
		t.Fatalf("after project: %+v", r)
	}
	answer("continue", "action", "Ask Dana")
	answer("no")
	answer("me")
	answer("continue", "context", "calls")
	r := answer("finish", "title", "Implement it")
	if !r.Done || len(r.Ops) < 4 {
		t.Fatalf("finish: %+v", r)
	}
	if code, _ := c.do("POST", "/api/v1/wizard/clarify/"+session+"/step", map[string]any{"choice": "finish"}, nil); code != 404 {
		t.Errorf("a finished session still answers: %d", code)
	}
	c.ok("POST", "/api/v1/wizard/clarify/start", map[string]string{}, &w)
	if !w.Done {
		t.Errorf("inbox should be empty: %+v", w)
	}

	// The capture became the project. Its original text is untouched.
	var p ItemView
	c.ok("GET", "/api/v1/items/"+cap.ID, nil, &p)
	if p.Kind != core.KindProject || p.Original != "engine thing <script>alert(1)</script>\nsecond line" || p.Rollup == nil || p.Rollup.Total != 2 || !p.Rollup.Alive {
		t.Fatalf("project: %+v rollup %+v", p.Item, p.Rollup)
	}
	var eng struct{ Items []Brief }
	c.ok("GET", "/api/v1/engage?context=calls", nil, &eng)
	if len(eng.Items) != 1 || eng.Items[0].Title != "Ask Dana" || eng.Items[0].ProjectTitle != "Engine survives resets" {
		t.Fatalf("engage: %+v", eng.Items)
	}
	c.ok("GET", "/api/v1/engage?context=home", nil, &eng)
	if len(eng.Items) != 0 {
		t.Errorf("engage at home: %+v", eng.Items)
	}

	// Done unblocks the follow-up; no prompt, the project still has a next action.
	done := func(b Brief) (prompts []Prompt) {
		var res struct{ Prompts []Prompt }
		c.ok("POST", "/api/v1/ops", map[string]any{"space": "robin", "ops": []map[string]any{{"type": "transition", "item": b.ID, "base_rev": b.Rev,
			"changes": []map[string]any{{"field": "state", "new": "done"}}}}}, &res)
		return res.Prompts
	}
	c.ok("GET", "/api/v1/engage", nil, &eng)
	if prompts := done(eng.Items[0]); len(prompts) != 0 {
		t.Errorf("prompt although a follow-up was unblocked: %+v", prompts)
	}
	c.ok("GET", "/api/v1/engage", nil, &eng)
	if len(eng.Items) != 1 || eng.Items[0].Title != "Implement it" || !eng.Items[0].Unblocked {
		t.Fatalf("after done: %+v", eng.Items)
	}
	// Completing the last open action asks for the next one (SPEC §5.3).
	prompts := done(eng.Items[0])
	if len(prompts) != 1 || prompts[0].Type != "next_action" || prompts[0].Project != cap.ID {
		t.Errorf("prompts = %+v", prompts)
	}
	// Stale rev: conflict.
	if code, _ := c.do("POST", "/api/v1/ops", map[string]any{"space": "robin", "ops": []map[string]any{{"type": "transition", "item": cap.ID, "base_rev": 1,
		"changes": []map[string]any{{"field": "state", "old": "someday", "new": "done"}}}}}, nil); code != 409 {
		t.Errorf("stale transition: %d, want 409", code)
	}

	// Markdown is rendered on the server and is safe.
	note := c.create("robin", map[string]any{"kind": "note", "title": "N", "body": "# T\n<script>alert(1)</script>\n- [ ] task\n[[" + cap.ID + "]]"})
	var iv ItemView
	c.ok("GET", "/api/v1/items/"+note, nil, &iv)
	if strings.Contains(iv.BodyHTML, "<script") || !strings.Contains(iv.BodyHTML, `href="i/`+cap.ID+`"`) || iv.Tasks != 1 ||
		!strings.Contains(iv.BodyEdit, "[[Engine survives resets]]") || len(iv.Refs) != 1 || iv.Refs[0].Token != "[["+cap.ID+"]]" {
		t.Errorf("note view: html %q edit %q refs %+v tasks %d", iv.BodyHTML, iv.BodyEdit, iv.Refs, iv.Tasks)
	}
	var rendered struct{ HTML string }
	c.ok("POST", "/api/v1/render", map[string]string{"item": note, "text": "**b** <img src=x onerror=1>"}, &rendered)
	if !strings.Contains(rendered.HTML, "<strong>b</strong>") || strings.Contains(rendered.HTML, "<img") || strings.Contains(rendered.HTML, "onerror") {
		t.Errorf("render = %q", rendered.HTML)
	}

	// An attachment is served with headers that keep it inert.
	sha := upload(t, c, note, "page.html", "<script>alert(1)</script>")
	res, _ := c.http.Get(s.url + "/api/v1/att/" + sha)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(body) != "<script>alert(1)</script>" || !strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") ||
		res.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(res.Header.Get("Content-Disposition"), "attachment") {
		t.Errorf("attachment: %q headers %v", body, res.Header)
	}

	// Templates: YAML in, validation, and back.
	if code, body := c.do("PUT", "/api/v1/spaces/robin/templates", map[string]string{"yaml": "templates:\n  - {key: t, kind: area}\n"}, nil); code != 400 {
		t.Errorf("bad template: %d %s", code, body)
	}
	var tpl struct{ YAML string }
	c.ok("PUT", "/api/v1/spaces/robin/templates", map[string]string{"yaml": "templates:\n  - key: trade_setup\n    kind: note\n    fields:\n      - {key: expires, type: date, tickler: true}\n"}, &tpl)
	if !strings.Contains(tpl.YAML, "trade_setup") {
		t.Errorf("templates = %q", tpl.YAML)
	}
	setup := c.create("robin", map[string]any{"kind": "note", "title": "NVDA", "template": "trade_setup", "fields": map[string]string{"expires": "2027-01-15"}})
	c.ok("GET", "/api/v1/items/"+setup, nil, &iv)
	if iv.DeferUntil != "2027-01-15" || iv.TemplateDef == nil || !iv.Deferred {
		t.Errorf("tickler field: defer %q, def %v", iv.DeferUntil, iv.TemplateDef)
	}

	// Plan and board answer with the shapes the client draws.
	var pl Plan
	c.ok("GET", "/api/v1/plan/"+cap.ID, nil, &pl)
	if len(pl.Projects) != 1 || len(pl.Actions) != 2 || pl.Projects[0].Rollup.Done != 2 {
		t.Errorf("plan: %+v", pl)
	}
	var bd struct{ Columns []Column }
	c.ok("GET", "/api/v1/board", nil, &bd)
	if len(bd.Columns) != 7 || bd.Columns[6].Key != "done" || len(bd.Columns[6].Items) != 2 {
		t.Errorf("board: %+v", bd.Columns)
	}
	if code, _ := c.do("GET", "/api/v1/plan/"+note, nil, nil); code != 400 {
		t.Errorf("plan of a note: %d", code)
	}
}

func TestPhotoCaptureTitle(t *testing.T) {
	s := newSite(t)
	c := s.login("jo") // German
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("text", "")
	h := make(map[string][]string)
	h["Content-Disposition"] = []string{`form-data; name="file"; filename="IMG_1.jpg"`}
	h["Content-Type"] = []string{"image/jpeg"}
	part, _ := mw.CreatePart(h)
	part.Write([]byte("jpeg bytes"))
	mw.Close()
	res, err := c.http.Post(s.url+"/api/v1/capture", mw.FormDataContentType(), &buf)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("capture: %v %v", err, res.Status)
	}
	var out struct{ ID string }
	json.NewDecoder(res.Body).Decode(&out)
	res.Body.Close()
	var iv ItemView
	c.ok("GET", "/api/v1/items/"+out.ID, nil, &iv)
	if !regexp.MustCompile(`^Foto \d\d\.\d\d\.\d{4} \d\d:\d\d$`).MatchString(iv.Title) || iv.Original != "" || len(iv.Attachments) != 1 {
		t.Errorf("photo capture: title %q original %q", iv.Title, iv.Original)
	}
}

// TestChangePassword: a user changes their own password in the web UI; the
// other sessions end; admins are who the config says.
func TestChangePassword(t *testing.T) {
	s := newSite(t)
	phone, laptop, dana := s.login("robin"), s.login("robin"), s.login("dana")
	put := func(c *client, current, next string) (int, string) {
		return c.do("PUT", "/api/v1/me/password", map[string]string{"current": current, "password": next}, nil)
	}
	if code, body := put(laptop, "wrong", "a-new-password"); code != 400 || !strings.Contains(body, `"password.wrong"`) || !strings.Contains(body, `"current"`) {
		t.Fatalf("wrong current password: %d %s", code, body)
	}
	if code, body := put(laptop, password, "short"); code != 400 || !strings.Contains(body, `"password.length"`) {
		t.Fatalf("short password: %d %s", code, body)
	}
	if code, body := put(laptop, password, "a-new-password"); code != 200 {
		t.Fatalf("change: %d %s", code, body)
	}
	// This session goes on, the other one of the same user is over, dana's is untouched.
	if code, _ := laptop.do("GET", "/api/v1/me", nil, nil); code != 200 {
		t.Errorf("the session that changed the password: %d", code)
	}
	if code, _ := phone.do("GET", "/api/v1/me", nil, nil); code != 401 {
		t.Errorf("another session of the same user: %d, want 401", code)
	}
	if code, _ := dana.do("GET", "/api/v1/me", nil, nil); code != 200 {
		t.Errorf("another user's session: %d", code)
	}
	// The old password no longer signs in, the new one does.
	fresh := s.anon()
	if code, _ := fresh.do("POST", "/api/v1/login", map[string]string{"user": "robin", "password": password}, nil); code != 401 {
		t.Errorf("login with the old password: %d", code)
	}
	if code, _ := fresh.do("POST", "/api/v1/login", map[string]string{"user": "robin", "password": "a-new-password"}, nil); code != 200 {
		t.Errorf("login with the new password: %d", code)
	}
	// Repeated wrong attempts lock, like the login.
	for range maxFailures {
		put(dana, "wrong", "whatever-else")
	}
	if code, body := put(dana, password, "whatever-else"); code != 429 || !strings.Contains(body, "login.locked") {
		t.Errorf("after %d wrong attempts: %d %s", maxFailures, code, body)
	}
	// /me says who is an admin: the config's list, nothing stored.
	var me struct {
		User struct{ Admin bool }
	}
	laptop.ok("GET", "/api/v1/me", nil, &me)
	if !me.User.Admin {
		t.Error("robin is in Admins but /me says no")
	}
	s.srv.Admins = nil
	laptop.ok("GET", "/api/v1/me", nil, &me)
	if me.User.Admin {
		t.Error("robin is no longer in Admins but /me says yes")
	}
}

// TestClarifyIntoSharedSpace: Clarify's "which space?" moves the capture to a
// shared space and makes it what was decided there, in one step (SPEC §5.2,
// COLLAB §1).
func TestClarifyIntoSharedSpace(t *testing.T) {
	s := newSite(t)
	robin, dana, jo := s.login("robin"), s.login("dana"), s.login("jo")
	project := dana.create("net", map[string]any{"kind": "project", "title": "Shared project"})
	robin.create("robin", map[string]any{"kind": "project", "title": "Private project"})
	var cap struct{ ID string }
	robin.ok("POST", "/api/v1/capture", map[string]any{"text": "ask about the shared thing\nwith a second line"}, &cap)

	type res struct {
		Session string
		Step    struct {
			Key     string
			Options []struct{ Value, Text, Label string }
			Fields  []struct {
				Key     string
				Options []struct{ Value, Text string }
			}
		}
		Done bool
		Ops  []core.Op
	}
	var w res
	robin.ok("POST", "/api/v1/wizard/clarify/start", map[string]string{}, &w)
	answer := func(c *client, choice string, kv ...string) (int, res) {
		values := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			values[kv[i]] = kv[i+1]
		}
		var r res
		code, _ := c.do("POST", "/api/v1/wizard/clarify/"+w.Session+"/step", map[string]any{"choice": choice, "values": values}, &r)
		return code, r
	}
	space := w.Step.Fields[len(w.Step.Fields)-1]
	if w.Step.Key != "what" || space.Key != "space" || len(space.Options) != 2 || space.Options[0].Value != "robin" || space.Options[1].Text != "NET · Dana, Robin" {
		t.Fatalf("first screen: %+v", w.Step)
	}
	// A space the user is not in is not a choice.
	if code, _ := answer(robin, "project", "title", "Ask about the shared thing", "space", "jo"); code != 400 {
		t.Errorf("choosing someone else's space: %d", code)
	}
	_, r := answer(robin, "project", "title", "Ask about the shared thing", "space", "net")
	// The project step now offers the shared space's projects, not the private ones.
	var offered []string
	for _, o := range r.Step.Fields[0].Options {
		offered = append(offered, o.Text)
	}
	if r.Step.Key != "project" || !slices.Contains(offered, "Shared project") || slices.Contains(offered, "Private project") {
		t.Fatalf("project step after choosing net: %s %v", r.Step.Key, offered)
	}
	answer(robin, "continue", "project", project)
	answer(robin, "continue", "action", "Ask Dana about the shared thing")
	answer(robin, "no")
	answer(robin, "me")
	answer(robin, "continue")
	_, r = answer(robin, "finish")
	if !r.Done || len(r.Ops) < 2 || r.Ops[0].Type != core.OpMove || r.Ops[0].Space != "net" {
		t.Fatalf("finish: %+v", r)
	}
	// It is in net now, as an action in the shared project, with its original text.
	var iv ItemView
	if code, _ := dana.do("GET", "/api/v1/items/"+cap.ID, nil, &iv); code != 200 || iv.Space != "net" || iv.Kind != core.KindAction || iv.State != core.StNext ||
		iv.Project != project || iv.Title != "Ask Dana about the shared thing" || iv.Owner != "robin" {
		t.Errorf("the item as dana sees it: %d space %s, %s/%s, project %s, %q, owner %s", code, iv.Space, iv.Kind, iv.State, iv.Project, iv.Title, iv.Owner)
	}
	if it, err := must(s.st.View("dana")).Item("net", cap.ID); err != nil || !strings.Contains(it.Original, "with a second line") || it.Space != "net" {
		t.Errorf("the stored item after the move: %+v %v", it, err)
	}
	if code, _ := jo.do("GET", "/api/v1/items/"+cap.ID, nil, nil); code != 404 {
		t.Errorf("a non-member reads the moved item: %d", code)
	}
	// It is gone from the personal space and its inbox; both logs tell the story.
	var inbox struct{ Captures []Brief }
	robin.ok("GET", "/api/v1/inbox", nil, &inbox)
	if len(inbox.Captures) != 0 {
		t.Errorf("inbox after the move: %+v", inbox.Captures)
	}
	items, _ := must(s.st.View("robin")).Items("robin", func(it *core.Item) bool { return it.ID == cap.ID })
	if len(items) != 0 {
		t.Error("the capture is still in the personal space")
	}
	var personal, shared []core.Op
	robin.ok("GET", "/api/v1/ops?n=3&space=robin", nil, &personal)
	robin.ok("GET", "/api/v1/ops?n=10&space=net", nil, &shared)
	if last := personal[len(personal)-1]; last.Type != core.OpMove || last.Item != cap.ID || !strings.Contains(string(last.Payload), `"to":"net"`) {
		t.Errorf("last op of the personal space: %+v", last)
	}
	moved := false
	for _, op := range shared {
		moved = moved || op.Type == core.OpMove && op.Item == cap.ID && strings.Contains(string(op.Payload), `"from":"robin"`)
	}
	if !moved {
		t.Error("the shared space's log has no arriving move")
	}
	// A move cannot be submitted as a plain op.
	if code, body := robin.do("POST", "/api/v1/ops", map[string]any{"space": "robin", "ops": []map[string]any{{"type": "move", "item": project, "payload": map[string]string{"to": "net"}}}}, nil); code != 400 {
		t.Errorf("move through /ops: %d %s", code, body)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// TestDelegationOverHTTP: a bug report comes in, is clarified into a shared
// space and delegated to the other member; they are told, hand it back with
// a reason, get it again and finish it (COLLAB §3).
func TestDelegationOverHTTP(t *testing.T) {
	s := newSite(t)
	robin, dana := s.login("robin"), s.login("dana")
	var cap struct{ ID string }
	robin.ok("POST", "/api/v1/capture", map[string]any{"text": "export fails for files over 2 GB"}, &cap)
	type res struct {
		Session string
		Step    struct {
			Key     string
			Options []struct{ Value, Text, Label string }
		}
		Done bool
	}
	var w res
	robin.ok("POST", "/api/v1/wizard/clarify/start", map[string]string{}, &w)
	answer := func(choice string, kv ...string) (int, res) {
		values := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			values[kv[i]] = kv[i+1]
		}
		var r res
		code, _ := robin.do("POST", "/api/v1/wizard/clarify/"+w.Session+"/step", map[string]any{"choice": choice, "values": values}, &r)
		return code, r
	}
	answer("action", "title", "Export fails for files over 2 GB", "space", "net")
	answer("continue", "action", "Fix the export of files over 2 GB")
	_, r := answer("no")
	var who []string
	for _, o := range r.Step.Options {
		who = append(who, o.Value+"="+o.Text+o.Label)
	}
	if r.Step.Key != "who" || !slices.Equal(who, []string{"me=clarify.opt.me", "member:dana=Dana", "person=clarify.opt.person"}) {
		t.Fatalf("who step: %s %v", r.Step.Key, who)
	}
	if code, _ := answer("member:jo"); code != 400 {
		t.Errorf("delegating to someone outside the space: %d", code)
	}
	answer("member:dana")
	if _, r := answer("continue", "target", "2026-11-30"); !r.Done {
		t.Fatalf("not done: %+v", r)
	}

	item := func(c *client) ItemView {
		t.Helper()
		var iv ItemView
		c.ok("GET", "/api/v1/items/"+cap.ID, nil, &iv)
		return iv
	}
	if iv := item(dana); iv.Owner != "dana" || iv.DelegatedBy != "robin" || iv.State != core.StNext || iv.Space != "net" {
		t.Fatalf("after delegation: owner %s, delegated_by %s, %s in %s", iv.Owner, iv.DelegatedBy, iv.State, iv.Space)
	}
	type inbox struct {
		Notifications []struct {
			Seq              uint64
			Type, Item, From string
		}
	}
	var in inbox
	dana.ok("GET", "/api/v1/inbox", nil, &in)
	if len(in.Notifications) != 1 || in.Notifications[0].Type != "delegated" || in.Notifications[0].Item != cap.ID || in.Notifications[0].From != "robin" {
		t.Fatalf("dana's From others: %+v", in.Notifications)
	}
	list := func(c *client, tab string) string {
		t.Helper()
		var e struct{ Items []Brief }
		c.ok("GET", "/api/v1/engage?tab="+tab, nil, &e)
		var ids []string
		for _, b := range e.Items {
			ids = append(ids, b.ID)
		}
		return strings.Join(ids, ",")
	}
	if list(dana, "") != cap.ID || list(robin, "") != "" || list(robin, "waiting") != cap.ID || list(dana, "waiting") != "" {
		t.Errorf("lists: dana next %q, robin next %q, robin waiting %q, dana waiting %q", list(dana, ""), list(robin, ""), list(robin, "waiting"), list(dana, "waiting"))
	}

	// Dana hands it back with a reason: a comment and the owner change in one batch.
	iv := item(dana)
	dana.ok("POST", "/api/v1/ops", map[string]any{"space": "net", "ops": []map[string]any{
		{"type": "comment", "item": cap.ID, "payload": map[string]string{"body": "I am out next week"}},
		{"type": "patch", "item": cap.ID, "base_rev": iv.Rev, "changes": []map[string]any{{"field": "owner", "old": "dana", "new": "robin"}}},
	}}, nil)
	if iv := item(robin); iv.Owner != "robin" || iv.DelegatedBy != "" {
		t.Errorf("after hand-back: owner %s, delegated_by %q", iv.Owner, iv.DelegatedBy)
	}
	robin.ok("GET", "/api/v1/inbox", nil, &in)
	if len(in.Notifications) != 1 || in.Notifications[0].Type != "handed_back" || in.Notifications[0].From != "dana" {
		t.Errorf("robin's From others: %+v", in.Notifications)
	}
	var comments []struct{ Body, Author string }
	robin.ok("GET", "/api/v1/items/"+cap.ID+"/comments", nil, &comments)
	if len(comments) != 1 || comments[0].Body != "I am out next week" || comments[0].Author != "dana" {
		t.Errorf("thread: %+v", comments)
	}
	if list(robin, "") != cap.ID || list(robin, "waiting") != "" {
		t.Errorf("after hand-back robin's lists: next %q, waiting %q", list(robin, ""), list(robin, "waiting"))
	}

	// Robin gives it to Dana again on the item page; she finishes it.
	iv = item(robin)
	robin.ok("POST", "/api/v1/ops", map[string]any{"space": "net", "ops": []map[string]any{
		{"type": "patch", "item": cap.ID, "base_rev": iv.Rev, "changes": []map[string]any{{"field": "owner", "old": "robin", "new": "dana"}}},
	}}, nil)
	if iv := item(dana); iv.Owner != "dana" || iv.DelegatedBy != "robin" || list(robin, "waiting") != cap.ID {
		t.Errorf("second delegation: owner %s, delegated_by %s", iv.Owner, iv.DelegatedBy)
	}
	iv = item(dana)
	dana.ok("POST", "/api/v1/ops", map[string]any{"space": "net", "ops": []map[string]any{
		{"type": "transition", "item": cap.ID, "base_rev": iv.Rev, "changes": []map[string]any{{"field": "state", "new": "done"}}},
	}}, nil)
	if list(robin, "waiting") != "" || list(dana, "") != "" {
		t.Errorf("after completion: robin waiting %q, dana next %q", list(robin, "waiting"), list(dana, ""))
	}
}

// TestSpaceContexts: a shared space has its own contexts, set by its members
// and usable by all of them, and by nobody else.
func TestSpaceContexts(t *testing.T) {
	s := newSite(t)
	robin, dana, jo := s.login("robin"), s.login("dana"), s.login("jo")
	put := func(c *client, space string, list ...string) (int, string) {
		return c.do("PUT", "/api/v1/spaces/"+space+"/contexts", map[string]any{"contexts": list}, nil)
	}
	if code, body := put(robin, "net", " Calls   US ", "calls us", "", "lab"); code != 200 || !strings.Contains(body, `["Calls US","lab"]`) {
		t.Fatalf("set: %d %s", code, body)
	}
	if code, _ := put(robin, "net", "not:ok"); code != 400 {
		t.Errorf("a bad key: %d", code)
	}
	if code, _ := put(robin, "net", "CALLS"); code != 400 {
		t.Errorf("a name that repeats a built-in context: %d", code)
	}
	if code, _ := put(robin, "net", "Calls US", "lab"); code != 200 {
		t.Errorf("setting the same list again: %d", code)
	}
	if code, _ := put(robin, "robin", "x"); code != 400 {
		t.Errorf("contexts on a personal space: %d", code)
	}
	if code, _ := put(jo, "net", "x"); code != 404 {
		t.Errorf("a non-member: %d", code)
	}
	// Dana, the other member, can use it on an action in the space; in her
	// personal space it does not exist.
	id := dana.create("net", map[string]any{"kind": "action", "title": "Call the US office", "context": "Calls US", "owner": "dana"})
	if code, _ := dana.do("POST", "/api/v1/ops", map[string]any{"space": "dana", "ops": []map[string]any{{"type": "create", "payload": map[string]any{"kind": "action", "title": "x", "context": "Calls US"}}}}, nil); code != 400 {
		t.Errorf("the space's context used in a personal space: %d", code)
	}
	// Engage offers it as a place, and filters by it.
	var e struct {
		Items    []Brief
		Contexts []struct{ Value string }
	}
	dana.ok("GET", "/api/v1/engage?context=Calls+US", nil, &e)
	offered := false
	for _, c := range e.Contexts {
		offered = offered || c.Value == "Calls US"
	}
	if !offered || len(e.Items) != 1 || e.Items[0].ID != id {
		t.Errorf("engage by the space's context: offered %v, items %+v", offered, e.Items)
	}
	dana.ok("GET", "/api/v1/engage?context=calls", nil, &e)
	if len(e.Items) != 0 {
		t.Errorf("the ordinary calls list shows it: %+v", e.Items)
	}
	// /me tells the client, for the Settings page.
	var me struct {
		Spaces []struct {
			ID       string
			Contexts []string
		}
	}
	robin.ok("GET", "/api/v1/me", nil, &me)
	for _, sp := range me.Spaces {
		if sp.ID == "net" && !slices.Equal(sp.Contexts, []string{"Calls US", "lab"}) {
			t.Errorf("/me: %+v", sp)
		}
	}
}

// TestCaptureToken: a token of scope capture can capture into its owner's
// inbox and can do nothing else (M8 acceptance: it cannot read anything).
func TestCaptureToken(t *testing.T) {
	s := newSite(t)
	robin := s.login("robin")
	var tok struct {
		ID, Token string
		Scopes    []string
	}
	robin.ok("POST", "/api/v1/me/tokens", map[string]any{"label": "phone", "scopes": []string{"capture"}}, &tok)
	if !strings.HasPrefix(tok.Token, "gtd_"+tok.ID+"_") || len(tok.Token) < 40 {
		t.Fatalf("token: %+v", tok)
	}
	if code, _ := robin.do("POST", "/api/v1/me/tokens", map[string]any{"scopes": []string{"read"}}, nil); code != 400 {
		t.Errorf("a scope that does not exist yet: %d", code)
	}
	// The list never shows the secret.
	var list []map[string]any
	robin.ok("GET", "/api/v1/me/tokens", nil, &list)
	if len(list) != 1 || list[0]["token"] != nil || list[0]["hash"] != nil || list[0]["label"] != "phone" {
		t.Errorf("token list: %+v", list)
	}
	call := func(token, method, path, body string) int {
		req, _ := http.NewRequest(method, s.url+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := call(tok.Token, "POST", "/api/v1/capture", `{"text":"from the phone"}`); code != 200 {
		t.Fatalf("capture with the token: %d", code)
	}
	var inbox struct{ Captures []Brief }
	robin.ok("GET", "/api/v1/inbox", nil, &inbox)
	if len(inbox.Captures) != 1 || inbox.Captures[0].Title != "from the phone" {
		t.Errorf("inbox: %+v", inbox.Captures)
	}
	// Everything else is refused: every GET route and every other write.
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/me", ""}, {"GET", "/api/v1/inbox", ""}, {"GET", "/api/v1/engage", ""}, {"GET", "/api/v1/search?q=phone", ""},
		{"GET", "/api/v1/items/" + inbox.Captures[0].ID, ""}, {"GET", "/api/v1/notes", ""}, {"GET", "/api/v1/ops?n=5", ""},
		{"GET", "/api/v1/export?space=robin", ""}, {"GET", "/api/v1/me/tokens", ""}, {"GET", "/api/v1/import?space=robin", ""},
		{"POST", "/api/v1/ops", `{"space":"robin","ops":[{"type":"create","payload":{"kind":"note","title":"x"}}]}`},
		{"POST", "/api/v1/me/tokens", `{"scopes":["capture"]}`}, {"PUT", "/api/v1/me/password", `{"current":"x","password":"yyyyyyyy"}`},
		{"POST", "/api/v1/wizard/clarify/start", `{}`}, {"POST", "/api/v1/attachments", ""}, {"POST", "/api/v1/admin/users", `{"id":"m"}`},
	} {
		if code := call(tok.Token, c.method, c.path, c.body); code != 403 {
			t.Errorf("%s %s with a capture token: %d, want 403", c.method, c.path, code)
		}
	}
	// Wrong, malformed and revoked tokens are unknown.
	for _, bad := range []string{tok.Token + "0", "gtd_" + tok.ID + "_", "gtd_nope_nope", "gtd_"} {
		if code := call(bad, "POST", "/api/v1/capture", `{"text":"x"}`); code != 401 {
			t.Errorf("token %q: %d, want 401", bad, code)
		}
	}
	// Another user cannot revoke it; its owner can.
	if code, _ := s.login("dana").do("DELETE", "/api/v1/me/tokens/"+tok.ID, nil, nil); code != 404 {
		t.Errorf("revoking someone else's token: %d", code)
	}
	robin.ok("DELETE", "/api/v1/me/tokens/"+tok.ID, nil, nil)
	if code := call(tok.Token, "POST", "/api/v1/capture", `{"text":"x"}`); code != 401 {
		t.Errorf("a revoked token: %d", code)
	}
}

// TestInstallable: what a phone needs to offer "install": a manifest that
// points into the app's prefix, icons, and a service worker whose scope is
// the app.
func TestInstallable(t *testing.T) {
	s := newSite(t)
	get := func(path string) (*http.Response, []byte) {
		t.Helper()
		res, err := http.Get(s.url + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res, body
	}
	// None of it needs a login: the browser fetches the manifest without cookies.
	res, body := get("/ui/manifest.webmanifest")
	var m struct {
		StartURL string `json:"start_url"`
		Scope    string
		ID       string
		Display  string
		Icons    []struct{ Src, Sizes, Type, Purpose string }
	}
	if err := json.Unmarshal(body, &m); err != nil || res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/manifest+json" {
		t.Fatalf("manifest: %d %s %v", res.StatusCode, res.Header.Get("Content-Type"), err)
	}
	if m.StartURL != Prefix+"/" || m.Scope != Prefix+"/" || m.ID != Prefix+"/" || m.Display != "standalone" {
		t.Errorf("manifest does not point at %s/: %+v", Prefix, m)
	}
	sizes := map[string]bool{}
	for _, ic := range m.Icons {
		res, png := get("/ui/" + ic.Src)
		if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/png" || !bytes.HasPrefix(png, []byte("\x89PNG")) {
			t.Errorf("icon %s: %d %s", ic.Src, res.StatusCode, res.Header.Get("Content-Type"))
		}
		sizes[ic.Sizes+" "+ic.Purpose] = true
	}
	for _, want := range []string{"192x192 any", "512x512 any", "192x192 maskable", "512x512 maskable"} {
		if !sizes[want] {
			t.Errorf("the manifest has no icon %s", want)
		}
	}
	res, body = get("/sw.js")
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/javascript") || !bytes.Contains(body, []byte("addEventListener('fetch'")) {
		t.Errorf("service worker: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	_, shell := get("/")
	if !bytes.Contains(shell, []byte(`<link rel="manifest" href="ui/manifest.webmanifest">`)) {
		t.Error("the shell does not link the manifest")
	}
}

// TestAdminInTheBrowser: an admin's web session administers only after the
// admin gave their password again, and only for a while. Everyone else never.
func TestAdminInTheBrowser(t *testing.T) {
	s := newSite(t)
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	s.st.Now = func() time.Time { return now }
	robin, dana := s.login("robin"), s.login("dana") // robin is the admin

	if code, body := robin.do("GET", "/api/v1/admin/users", nil, nil); code != 403 || !strings.Contains(body, "admin.locked") {
		t.Fatalf("admin route before unlocking: %d %s", code, body)
	}
	if code, body := robin.do("POST", "/api/v1/admin/unlock", map[string]string{"password": "wrong"}, nil); code != 400 || !strings.Contains(body, "password.wrong") {
		t.Errorf("unlock with a wrong password: %d %s", code, body)
	}
	if code, _ := dana.do("POST", "/api/v1/admin/unlock", map[string]string{"password": password}, nil); code != 403 {
		t.Errorf("a non-admin unlocking: %d", code)
	}
	robin.ok("POST", "/api/v1/admin/unlock", map[string]string{"password": password}, nil)

	// Users: list, add, first password. The new user can sign in.
	var users []struct {
		ID, Name        string
		Admin, Password bool
	}
	robin.ok("GET", "/api/v1/admin/users", nil, &users)
	if len(users) != 3 || users[2].ID != "robin" || !users[2].Admin || users[0].Admin || !users[0].Password {
		t.Errorf("users: %+v", users)
	}
	robin.ok("POST", "/api/v1/admin/users", map[string]any{"id": "lee", "name": "Lee"}, nil)
	robin.ok("PUT", "/api/v1/admin/users/lee/password", map[string]string{"password": "lee's first one"}, nil)
	lee := s.anon()
	if code, _ := lee.do("POST", "/api/v1/login", map[string]string{"user": "lee", "password": "lee's first one"}, nil); code != 200 {
		t.Errorf("the new user's login: %d", code)
	}
	// Spaces: create, list, add and remove a member. Lee sees it only as a member.
	robin.ok("POST", "/api/v1/admin/spaces", map[string]any{"id": "team", "name": "Team", "members": []string{"robin", "dana"}}, nil)
	var spaces []struct {
		ID, Name string
		Members  []string
	}
	robin.ok("GET", "/api/v1/admin/spaces", nil, &spaces)
	if len(spaces) != 2 || spaces[1].ID != "team" || !slices.Equal(spaces[1].Members, []string{"dana", "robin"}) {
		t.Errorf("spaces: %+v", spaces)
	}
	mySpaces := func(c *client) string {
		var me struct{ Spaces []struct{ ID string } }
		c.ok("GET", "/api/v1/me", nil, &me)
		var ids []string
		for _, sp := range me.Spaces {
			ids = append(ids, sp.ID)
		}
		return strings.Join(ids, ",")
	}
	if strings.Contains(mySpaces(lee), "team") {
		t.Error("lee is in the space before being added")
	}
	robin.ok("PUT", "/api/v1/admin/spaces/team/members/lee", nil, nil)
	if !strings.Contains(mySpaces(lee), "team") {
		t.Error("lee is not in the space after being added")
	}
	robin.ok("DELETE", "/api/v1/admin/spaces/team/members/lee", nil, nil)
	if strings.Contains(mySpaces(lee), "team") {
		t.Error("lee is still in the space after being removed")
	}

	// The unlock belongs to that session, and it runs out.
	if code, _ := s.login("robin").do("GET", "/api/v1/admin/users", nil, nil); code != 403 {
		t.Errorf("another session of the admin, not unlocked: %d", code)
	}
	if code, _ := dana.do("GET", "/api/v1/admin/users", nil, nil); code != 403 {
		t.Errorf("a non-admin on an admin route: %d", code)
	}
	now = now.Add(adminUnlock + time.Second)
	if code, body := robin.do("GET", "/api/v1/admin/users", nil, nil); code != 403 || !strings.Contains(body, "admin.locked") {
		t.Errorf("after the unlock ran out: %d %s", code, body)
	}
}

// TestMoveItemOverHTTP: an idea (a someday project with its notes) moves from
// the personal space to a shared one: first the preview, then the move.
func TestMoveItemOverHTTP(t *testing.T) {
	s := newSite(t)
	robin, dana, jo := s.login("robin"), s.login("dana"), s.login("jo")
	theme := robin.create("robin", map[string]any{"kind": "project", "title": "Greenhouse", "state": "someday"})
	note := robin.create("robin", map[string]any{"kind": "note", "title": "Greenhouse sizes", "project": theme, "state": "filed"})
	loose := robin.create("robin", map[string]any{"kind": "note", "title": "A loose note"})
	area := robin.create("robin", map[string]any{"kind": "area", "title": "Home"})

	move := func(c *client, id, space string, confirm bool) (int, map[string]any) {
		var out map[string]any
		code, _ := c.do("POST", "/api/v1/items/"+id+"/move", map[string]any{"space": space, "confirm": confirm}, &out)
		return code, out
	}
	// The preview says what moves and who will see it, and changes nothing.
	code, pre := move(robin, theme, "net", false)
	if code != 200 || pre["count"] != float64(2) || pre["space"] != "NET" || fmt.Sprint(pre["members"]) != "[Dana Robin]" {
		t.Fatalf("preview: %d %v", code, pre)
	}
	if code, _ := dana.do("GET", "/api/v1/items/"+theme, nil, nil); code != 404 {
		t.Fatalf("the preview moved something: dana gets %d", code)
	}
	if code, out := move(robin, theme, "net", true); code != 200 || out["moved"] != float64(2) {
		t.Fatalf("move: %d %v", code, out)
	}
	var iv ItemView
	dana.ok("GET", "/api/v1/items/"+note, nil, &iv)
	if iv.Space != "net" || iv.Project != theme || iv.State != core.StFiled {
		t.Errorf("the note as dana sees it: %s project %s %s", iv.Space, iv.Project, iv.State)
	}
	dana.ok("GET", "/api/v1/items/"+theme, nil, &iv)
	if iv.Space != "net" || iv.State != core.StSomeday {
		t.Errorf("the theme as dana sees it: %s %s", iv.Space, iv.State)
	}
	// A single note, and back.
	if code, out := move(robin, loose, "net", true); code != 200 || out["moved"] != float64(1) {
		t.Errorf("single note: %d %v", code, out)
	}
	if code, _ := move(dana, loose, "dana", true); code != 200 {
		t.Errorf("dana takes the note into her own space: %d", code)
	}
	if code, _ := robin.do("GET", "/api/v1/items/"+loose, nil, nil); code != 404 {
		t.Errorf("robin still sees a note that is now dana's own: %d", code)
	}
	// What is refused.
	for name, c := range map[string]struct {
		who       *client
		id, space string
		want      int
	}{
		"an area":                         {robin, area, "net", 400},
		"into the space it is in":         {robin, theme, "net", 400},
		"into a space of others":          {robin, theme, "jo", 404},
		"an item of a space of others":    {jo, theme, "jo", 404},
		"an item that does not exist":     {robin, "01NOPE", "net", 404},
		"a non-member previews the space": {jo, theme, "net", 404},
	} {
		for _, confirm := range []bool{false, true} {
			if code, _ := move(c.who, c.id, c.space, confirm); code != c.want {
				t.Errorf("%s (confirm %v): %d, want %d", name, confirm, code, c.want)
			}
		}
	}
}

// TestBoardAllSpaces: the board over every space of the user, and of nobody else.
func TestBoardAllSpaces(t *testing.T) {
	s := newSite(t)
	robin, dana, jo := s.login("robin"), s.login("dana"), s.login("jo")
	robin.create("robin", map[string]any{"kind": "action", "title": "Private, due late", "due": "2026-12-20"})
	robin.create("net", map[string]any{"kind": "action", "title": "Shared, due soon", "due": "2026-12-01", "owner": "robin"})
	robin.create("robin", map[string]any{"kind": "action", "title": "Private, no date"})
	dana.create("dana", map[string]any{"kind": "action", "title": "Dana's own"})
	jo.create("jo", map[string]any{"kind": "action", "title": "Jo's own"})

	type board struct {
		Space   string
		Columns []struct {
			Key   string
			Items []Brief
		}
		Options struct{ Areas, Projects []Named }
	}
	next := func(c *client, space string) []string {
		t.Helper()
		var b board
		c.ok("GET", "/api/v1/board?space="+url.QueryEscape(space), nil, &b)
		var out []string
		for _, col := range b.Columns {
			if col.Key == core.StNext {
				for _, it := range col.Items {
					out = append(out, it.Title+" @"+it.SpaceName)
				}
			}
		}
		if len(b.Columns) != 7 {
			t.Errorf("%d columns for space %q", len(b.Columns), space)
		}
		return out
	}
	// One order across the spaces: by due date, then the undated by age.
	if got, want := next(robin, "*"), []string{"Shared, due soon @NET", "Private, due late @Robin", "Private, no date @Robin"}; !slices.Equal(got, want) {
		t.Errorf("robin's board over all spaces:\n got %q\nwant %q", got, want)
	}
	if got := next(dana, "*"); !slices.Equal(got, []string{"Shared, due soon @NET", "Dana's own @Dana"}) {
		t.Errorf("dana's board over all spaces: %q", got)
	}
	if got := next(jo, "*"); !slices.Equal(got, []string{"Jo's own @Jo"}) {
		t.Errorf("jo's board over all spaces: %q", got)
	}
	// A single space is as before, without space names on the cards.
	if got := next(robin, "net"); !slices.Equal(got, []string{"Shared, due soon @"}) {
		t.Errorf("robin's board of net: %q", got)
	}
}
