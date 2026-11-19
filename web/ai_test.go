package web

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/fiber/gtd/core"
)

// model is a stand-in generation backend: it records what it was sent.
type model struct {
	mu     sync.Mutex
	local  bool
	asked  []string
	answer string
}

func (m *model) Name() string { return "test-model" }
func (m *model) Local() bool  { return m.local }
func (m *model) Ask(_ context.Context, system, message string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked = append(m.asked, message)
	return m.answer, nil
}
func (m *model) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.asked)
}

// TestTaskLinesAndAIPolicy: the AI button proposes task lines and changes
// nothing; the user's acceptance adds them. No text of a space reaches a
// model its policy does not allow (Q-11), and none reaches it unasked.
func TestTaskLinesAndAIPolicy(t *testing.T) {
	s := newSite(t)
	robin, dana, jo := s.login("robin"), s.login("dana"), s.login("jo")
	minutes := "Call with the plumber.\n\nRobin sends the photos by Friday. Dana gets a second quote.\nSomeone should check the insurance."
	note := robin.create("robin", map[string]any{"kind": "note", "title": "Call notes", "body": minutes})
	shared := robin.create("net", map[string]any{"kind": "note", "title": "Team minutes", "body": minutes})
	ask := func(c *client, id string) (int, string) {
		return c.do("POST", "/api/v1/items/"+id+"/tasklines", map[string]any{}, nil)
	}

	// No backend: nothing to ask, and the client is told so.
	var me struct {
		AI     map[string]any
		Spaces []struct{ ID, AI string }
	}
	robin.ok("GET", "/api/v1/me", nil, &me)
	if me.AI["on"] != false {
		t.Errorf("/me without a backend: %v", me.AI)
	}
	if code, body := ask(robin, note); code != 400 || !strings.Contains(body, "ai.off") {
		t.Errorf("without a backend: %d %s", code, body)
	}

	// A cloud model. Spaces say nothing, which means "local": nothing is sent.
	m := &model{answer: `[{"who":"me","text":"Send the photos by Friday","quote":"Robin sends the photos by Friday."},
		{"who":"Dana","text":"Get a second quote","quote":"Dana gets a second quote."},
		{"who":"","text":"Check the insurance","quote":"Someone should check the insurance."},
		{"who":"Lee","text":"Paint the walls","quote":"Lee paints the walls."}]`}
	s.srv.AI = m
	robin.ok("GET", "/api/v1/me", nil, &me)
	if me.AI["on"] != true || me.AI["local"] != false || me.AI["name"] != "test-model" {
		t.Errorf("/me with a backend: %v", me.AI)
	}
	for _, id := range []string{note, shared} {
		if code, body := ask(robin, id); code != 403 || !strings.Contains(body, "ai.policy") {
			t.Errorf("a space that did not allow a cloud model: %d %s", code, body)
		}
	}
	if m.calls() != 0 {
		t.Fatalf("text was sent to the model %d times although no space allows it", m.calls())
	}

	// The policy: members set it; nobody else; only known values.
	set := func(c *client, space, policy string) int {
		code, _ := c.do("PUT", "/api/v1/spaces/"+space+"/ai", map[string]string{"policy": policy}, nil)
		return code
	}
	if set(robin, "robin", "any") != 200 || set(robin, "robin", "sometimes") != 400 || set(jo, "net", "any") != 404 || set(dana, "robin", "any") != 404 {
		t.Error("setting the AI policy: wrong status for one of the cases")
	}
	robin.ok("PUT", "/api/v1/me", map[string]any{"aliases": []string{"Rob", " R. ", "Rob"}}, nil)

	// Now the personal space allows it. The proposal changes nothing.
	var res struct {
		Lines []struct{ Who, Text, Quote string }
	}
	robin.ok("POST", "/api/v1/items/"+note+"/tasklines", map[string]any{}, &res)
	if len(res.Lines) != 3 || res.Lines[0].Who != "me" || res.Lines[1].Who != "Dana" || res.Lines[2].Who != "" {
		t.Fatalf("proposals: %+v", res.Lines) // the one without a source in the note is gone
	}
	if m.calls() != 1 || !strings.Contains(m.asked[0], minutes) || !strings.Contains(m.asked[0], "Rob, R., Robin") {
		t.Errorf("what the model was sent: %q", m.asked)
	}
	var iv ItemView
	robin.ok("GET", "/api/v1/items/"+note, nil, &iv)
	if iv.Body != minutes || iv.Rev != 1 {
		t.Errorf("a proposal changed the note: rev %d", iv.Rev)
	}
	// The shared space still does not allow it, whatever the personal one says.
	if code, _ := ask(robin, shared); code != 403 || m.calls() != 1 {
		t.Errorf("the shared space after the personal one was opened: %d, %d calls", code, m.calls())
	}
	// Someone who cannot see the note cannot have it read either.
	if code, _ := ask(jo, note); code != 404 || m.calls() != 1 {
		t.Errorf("another user asking about robin's note: %d, %d calls", code, m.calls())
	}

	// Accepting two of the three: they are appended as task lines.
	var added struct{ Added int }
	robin.ok("POST", "/api/v1/items/"+note+"/tasklines", map[string]any{"accept": []map[string]string{
		{"who": "me", "text": "Send the photos by Friday"}, {"who": "Dana", "text": "Get a second quote"}}}, &added)
	robin.ok("GET", "/api/v1/items/"+note, nil, &iv)
	want := minutes + "\n\n## Action items\n\n- [ ] Me: Send the photos by Friday\n- [ ] Dana: Get a second quote\n"
	if added.Added != 2 || iv.Body != want || iv.Rev != 2 || m.calls() != 1 {
		t.Errorf("after accepting: added %d, rev %d, %d model calls, body:\n%s", added.Added, iv.Rev, m.calls(), iv.Body)
	}
	// Process note takes it from there.
	var w struct{ Step struct{ Key string } }
	if code, body := robin.do("POST", "/api/v1/wizard/note/start", map[string]string{"item": note}, &w); code != 200 {
		t.Errorf("Process note on the result: %d %s", code, body)
	}
	for _, bad := range []map[string]any{{"accept": []map[string]string{}}, {"accept": []map[string]string{{"who": "x\n- [ ] y", "text": "z"}}}, {"accept": []map[string]string{{"who": "me", "text": " "}}}} {
		if code, _ := robin.do("POST", "/api/v1/items/"+note+"/tasklines", bad, nil); code != 400 {
			t.Errorf("accepting %v: %d", bad, code)
		}
	}

	// "off" refuses even a model on our own machines; "local" allows that one.
	local := &model{local: true, answer: "[]"}
	s.srv.AI = local
	if code, _ := ask(robin, shared); code != 200 || local.calls() != 1 {
		t.Errorf("a local model in a space that says nothing: %d", code)
	}
	if set(dana, "net", "off") != 200 {
		t.Fatal("dana could not set the policy of her space")
	}
	if code, body := ask(robin, shared); code != 403 || !strings.Contains(body, "ai.policy") || local.calls() != 1 {
		t.Errorf("a space set to off: %d %s", code, body)
	}
	// The policy is part of the space and survives in an export.
	st, _ := must(s.st.View("dana")).State("net")
	if st.Space.AI != core.AIOff || st.Space.AIPolicy() != core.AIOff {
		t.Errorf("stored policy: %q", st.Space.AI)
	}
}
