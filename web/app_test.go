package web

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

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
		case ".svg":
			continue // an image; its xmlns is a name, not a request
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
	if u := s.st.User("robin"); u.Lang != "de" || strings.Join(u.PersonalContexts, ",") != "boat,garage" {
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
