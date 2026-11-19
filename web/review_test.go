package web

import (
	"strings"
	"testing"

	"github.com/fiber/gtd/core"
)

// TestReviewOverHTTP: the weekly review keeps its progress on the server, so
// a second device continues where the first stopped, and its decisions are
// applied one by one (M2: resumable on any device).
func TestReviewOverHTTP(t *testing.T) {
	s := newSite(t)
	phone, laptop, dana := s.login("robin"), s.login("robin"), s.login("dana")
	stuck := phone.create("robin", map[string]any{"kind": "project", "title": "Garden gate fixed"})
	alive := phone.create("net", map[string]any{"kind": "project", "title": "Engine survives resets"})
	phone.create("net", map[string]any{"kind": "action", "title": "Ask Dana", "project": alive, "owner": "robin"})
	maybe := phone.create("robin", map[string]any{"kind": "action", "title": "Learn to sail", "state": "someday"})
	var cap struct{ ID string }
	phone.ok("POST", "/api/v1/capture", map[string]any{"text": "a thought in the inbox"}, &cap)

	type res struct {
		Session string
		Step    struct {
			Key, Subject string
			Options      []struct{ Value string }
			Data         map[string]any
		}
		Done    bool
		Ops     []core.Op
		Summary map[string]int
	}
	var r res
	phone.ok("POST", "/api/v1/wizard/review/start", map[string]string{}, &r)
	if r.Step.Key != "clear" || r.Step.Data["inbox"] != float64(1) {
		t.Fatalf("start: %+v", r.Step)
	}
	step := func(c *client, choice string, kv ...string) (res, int, string) {
		t.Helper()
		values := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			values[kv[i]] = kv[i+1]
		}
		var out res
		code, body := c.do("POST", "/api/v1/wizard/review/review/step", map[string]any{"choice": choice, "values": values}, &out)
		return out, code, body
	}
	// The gate: not with a capture in the inbox.
	if _, code, body := step(phone, "continue"); code != 400 || !strings.Contains(body, "err.inbox_not_empty") {
		t.Fatalf("gate: %d %s", code, body)
	}
	// Trash the capture; then the review goes on to the projects.
	phone.ok("POST", "/api/v1/ops", map[string]any{"space": "robin", "ops": []map[string]any{{"type": "transition", "item": cap.ID, "base_rev": 1,
		"changes": []map[string]any{{"field": "state", "new": "trashed"}}}}}, nil)
	r, code, body := step(phone, "continue")
	if code != 200 || r.Step.Key != "projects" {
		t.Fatalf("after the gate: %d %s", code, body)
	}
	first := r.Step.Subject

	// The laptop opens the review: same step, nothing lost.
	var onLaptop res
	laptop.ok("POST", "/api/v1/wizard/review/start", map[string]string{}, &onLaptop)
	if onLaptop.Step.Key != "projects" || onLaptop.Step.Subject != first {
		t.Fatalf("laptop continues at %s %q, phone was at projects %q", onLaptop.Step.Key, onLaptop.Step.Subject, first)
	}
	var me struct{ Review map[string]any }
	laptop.ok("GET", "/api/v1/me", nil, &me)
	if me.Review["started"] != true {
		t.Errorf("me.review = %v", me.Review)
	}
	// Answer on the laptop: projects of both of robin's spaces come up, the
	// stuck one cannot be kept without a next action.
	seen := map[string]bool{}
	for r = onLaptop; r.Step.Key == "projects"; {
		seen[r.Step.Subject] = true
		if r.Step.Subject == "Garden gate fixed" {
			if _, code, body := step(laptop, "keep"); code != 400 || !strings.Contains(body, "err.stuck") {
				t.Fatalf("keeping a stuck project: %d %s", code, body)
			}
			r, code, body = step(laptop, "keep", "next_action", "Buy hinges")
			if code != 200 || len(r.Ops) != 1 || r.Ops[0].Type != core.OpCreate {
				t.Fatalf("keep with a next action: %d %s", code, body)
			}
			continue
		}
		if r, code, body = step(laptop, "keep"); code != 200 {
			t.Fatalf("keep: %d %s", code, body)
		}
	}
	if !seen["Garden gate fixed"] || !seen["Engine survives resets"] {
		t.Errorf("projects reviewed: %v", seen)
	}
	var pl Plan
	laptop.ok("GET", "/api/v1/plan/"+stuck, nil, &pl)
	if len(pl.Actions) != 1 || pl.Actions[0].Title != "Buy hinges" || !pl.Projects[0].Rollup.Alive {
		t.Errorf("the stuck project after the review: %+v", pl.Actions)
	}
	// Back on the phone for the rest: someday, then done.
	for !r.Done {
		choice := "continue"
		if r.Step.Key == "someday" {
			if r.Step.Data["item"] != maybe {
				t.Errorf("someday asks about %v", r.Step.Data["item"])
			}
			choice = "activate"
		}
		if r, code, body = step(phone, choice); code != 200 {
			t.Fatalf("%s: %d %s", choice, code, body)
		}
	}
	if r.Summary["projects.keep"] != 2 || r.Summary["someday.activate"] != 1 {
		t.Errorf("summary = %v", r.Summary)
	}
	var iv ItemView
	phone.ok("GET", "/api/v1/items/"+maybe, nil, &iv)
	if iv.State != core.StNext {
		t.Errorf("activated someday action is %s", iv.State)
	}
	phone.ok("GET", "/api/v1/me", nil, &me)
	if me.Review["started"] != false || me.Review["last_done"] == "" {
		t.Errorf("after the review: %v", me.Review)
	}
	if _, code, _ := step(phone, "continue"); code != 404 {
		t.Errorf("answering a finished review: %d", code)
	}

	// Dana's review is his own: robin's personal project never comes up.
	var er res
	dana.ok("POST", "/api/v1/wizard/review/start", map[string]string{}, &er)
	for !er.Done {
		if strings.Contains(er.Step.Subject, "Garden") || strings.Contains(er.Step.Subject, "sail") {
			t.Fatalf("dana's review shows %q", er.Step.Subject)
		}
		choice := er.Step.Options[0].Value
		var code int
		if er, code, body = step(dana, choice); code != 200 {
			t.Fatalf("dana %s: %d %s", choice, code, body)
		}
	}

	// The banner: due on the user's weekday until a review is finished that day.
	weekday := int(s.st.Now().Weekday())
	if weekday == 0 {
		weekday = 7
	}
	jo := s.login("jo")
	jo.ok("PUT", "/api/v1/me", map[string]any{"review_weekday": weekday}, nil)
	jo.ok("GET", "/api/v1/me", nil, &me)
	if me.Review["due"] != true {
		t.Errorf("review day: %v", me.Review)
	}
	jo.ok("PUT", "/api/v1/me", map[string]any{"review_weekday": weekday%7 + 1}, nil)
	jo.ok("GET", "/api/v1/me", nil, &me)
	if me.Review["due"] != false {
		t.Errorf("not review day: %v", me.Review)
	}
	if code, _ := jo.do("PUT", "/api/v1/me", map[string]any{"review_weekday": 9}, nil); code != 400 {
		t.Errorf("weekday 9: %d", code)
	}
}
