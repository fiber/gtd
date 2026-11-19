package wizard

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fiber/gtd/core"
)

func createOp(it core.Item) core.Op {
	return core.Op{Via: "cli", Type: core.OpCreate, Payload: mustJSON(it)}
}

// reviewWorld is newWorld (area, p1, p2, pdone, capture c) plus what a review
// has to deal with, all created seven weeks ago.
func reviewWorld(t *testing.T) *world {
	w := newWorld(t)
	w.ts = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	err := w.apply([]core.Op{
		createOp(core.Item{ID: "home", Kind: core.KindArea, Title: "Home"}),
		createOp(core.Item{ID: "a1", Kind: core.KindAction, Title: "Read RFC 4271", Project: "p1"}),
		createOp(core.Item{ID: "psub", Kind: core.KindProject, Title: "Session state persisted", Parent: "p1", Target: "2027-03-01"}),
		createOp(core.Item{ID: "w1", Kind: core.KindAction, Title: "Get the API key", Project: "psub", State: core.StWaiting, WaitingOn: "Lee"}),
		createOp(core.Item{ID: "pstuck", Kind: core.KindProject, Title: "Garden gate fixed", Area: "home"}),
		createOp(core.Item{ID: "s1", Kind: core.KindAction, Title: "Try gap fill trades", State: core.StSomeday}),
		createOp(core.Item{ID: "sp", Kind: core.KindProject, Title: "Learn to sail", State: core.StSomeday}),
		createOp(core.Item{ID: "loose", Kind: core.KindNote, Title: "Loose thought"}),
		createOp(core.Item{ID: "old", Kind: core.KindAction, Title: "Renew passport", Target: "2026-11-01"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	w.ts = time.Time{}
	// p1 carries a target earlier than its child: a date warning.
	if err := w.apply([]core.Op{{Via: "cli", Type: core.OpPatch, Item: "p1", BaseRev: w.st.Items["p1"].Rev,
		Changes: []core.FieldChange{{Field: "target", New: quote("2027-01-15")}}}}); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) reviewEnv(inbox int) ReviewEnv {
	return ReviewEnv{User: "robin", Today: today, Spaces: []*core.State{w.st}, Inbox: inbox, FromOthers: 2,
		SomedayReviewed: map[string]string{"s1": "2026-11-01"}, FollowUp: "Follow up with %s: %s", Nudge: "Any news?"}
}

// TestReviewWalk is the M2 acceptance test: a scripted review over a fixture
// walks every step. After each answer the state goes through JSON, as it does
// when it is stored between devices.
func TestReviewWalk(t *testing.T) {
	w := reviewWorld(t)
	r := StartReview(today)
	inbox := 1
	var visited []string
	// answer gives one answer and applies its ops, like the server does.
	answer := func(in Input) error {
		t.Helper()
		env := w.reviewEnv(inbox)
		at, done := r.At(env)
		if done {
			t.Fatalf("the review is over, cannot answer %+v", in)
		}
		step := at.Step(env)
		if step.Question == "" || len(step.Options) == 0 || step.Data["phases"] != 8 {
			t.Fatalf("malformed step: %+v", step)
		}
		visited = append(visited, step.Key+":"+step.Subject)
		next, space, ops, _, err := at.Next(env, in, w.newID)
		if err != nil {
			return err
		}
		if len(ops) > 0 && space != "robin" {
			t.Fatalf("ops for space %q", space)
		}
		if err := w.apply(ops); err != nil {
			t.Fatalf("ops of %s rejected by core.Apply: %v", step.Key, err)
		}
		data, _ := json.Marshal(next) // stored between answers
		r = Review{}
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	must := func(in Input) {
		t.Helper()
		if err := answer(in); err != nil {
			t.Fatalf("%+v: %v", in, err)
		}
	}
	rejected := func(in Input, key string) {
		t.Helper()
		err := answer(in)
		var ie *InputError
		if !errors.As(err, &ie) || ie.Key != key {
			t.Fatalf("%+v: %v, want %s", in, err, key)
		}
	}

	// 1. Get clear: not past the gate with a capture in the inbox.
	rejected(in("continue"), "err.inbox_not_empty")
	inbox = 0
	must(in("continue"))
	// 2. Unsorted notes.
	must(in("continue"))
	// 3. Projects, in tree order: p1, its child psub, then the others.
	must(in("keep"))                  // p1 is alive: it has a next action
	must(in("someday"))               // psub
	must(in("done"))                  // p2: stuck, and finished
	rejected(in("keep"), "err.stuck") // pstuck: a kept project needs a next action
	rejected(in("keep", "next_action", ""), "err.stuck")
	must(in("keep", "next_action", "Buy new hinges"))
	// 4. Stale next actions.
	rejected(in("defer"), "err.required")
	must(in("defer", "defer_until", "2026-12-03")) // a1
	must(in("keep"))                               // old
	// 5. Waiting for.
	must(in("nudge")) // w1, waiting for Lee
	// 6. Dates.
	env := w.reviewEnv(0)
	at, _ := r.At(env)
	var notes []string
	for _, d := range at.Step(env).Data["items"].([]map[string]string) {
		notes = append(notes, d["id"]+":"+d["note"])
	}
	// p1 is the parent, so psub's later target is the warning; "old" has slipped.
	if !slices.Contains(notes, "old:warning.short.slipping") {
		t.Errorf("dates step: %v", notes)
	}
	must(in("continue"))
	// 7. Someday: never reviewed first (sp), then s1, reviewed on 1 Nov. psub
	// was parked a moment ago in this very review and is not asked about again.
	must(in("activate")) // sp
	must(in("drop"))     // s1
	// 8. Areas.
	env = w.reviewEnv(0)
	at, _ = r.At(env)
	areasData := fmt.Sprint(at.Step(env).Data["areas"])
	if !strings.Contains(areasData, "projects:1 title:Home") || !strings.Contains(areasData, "title:Collectors") {
		t.Errorf("areas step: %s", areasData)
	}
	must(in("continue"))
	if _, done := r.At(w.reviewEnv(0)); !done {
		t.Fatalf("the review is not over, at %q", r.Phase)
	}

	want := "clear: | clear: | unsorted: | projects:BGP collector handles resets | projects:Session state persisted | " +
		"projects:Example API integrated | projects:Garden gate fixed | projects:Garden gate fixed | projects:Garden gate fixed | " +
		"stale:Read RFC 4271 | stale:Read RFC 4271 | stale:Renew passport | waiting:Get the API key | dates: | " +
		"someday:Learn to sail | someday:Try gap fill trades | areas:"
	if got := strings.Join(visited, " | "); got != want {
		t.Errorf("steps visited:\n%s\nwant:\n%s", got, want)
	}
	state := func(id string) string { return w.st.Items[id].State }
	if state("psub") != core.StSomeday || state("p2") != core.StDone || state("sp") != core.StActive || state("s1") != core.StDropped ||
		w.st.Items["a1"].DeferUntil != "2026-12-03" {
		t.Errorf("after the review: psub %s, p2 %s, sp %s, s1 %s, a1 deferred %q", state("psub"), state("p2"), state("sp"), state("s1"), w.st.Items["a1"].DeferUntil)
	}
	var made []string
	for _, it := range w.st.Items {
		if strings.HasPrefix(it.ID, "n") {
			made = append(made, fmt.Sprintf("%s|%s|%s|%s", it.Title, it.Project, it.Context, it.State))
		}
	}
	slices.Sort(made)
	if !slices.Equal(made, []string{"Buy new hinges|pstuck||next", "Follow up with Lee: Get the API key|psub|calls|next"}) {
		t.Errorf("actions the review created: %q", made)
	}
	if !core.Alive(w.st, "pstuck") {
		t.Error("the kept project is still stuck")
	}
	wantStats := map[string]int{"projects.keep": 2, "projects.someday": 1, "projects.done": 1, "stale.defer": 1, "stale.keep": 1,
		"waiting.nudge": 1, "someday.activate": 1, "someday.drop": 1}
	if fmt.Sprint(r.Stats) != fmt.Sprint(wantStats) {
		t.Errorf("stats = %v\nwant  %v", r.Stats, wantStats)
	}
}

func TestReviewDetails(t *testing.T) {
	t.Run("nothing to review: straight through", func(t *testing.T) {
		w := &world{t: t, st: &core.State{Space: &core.Space{ID: "robin", Kind: core.SpacePersonal, Members: []string{"robin"}}, Items: map[string]*core.Item{}}}
		env := ReviewEnv{User: "robin", Today: today, Spaces: []*core.State{w.st}}
		r, done := StartReview(today).At(env)
		if done || r.Phase != PhaseClear {
			t.Fatalf("phase %q, done %v", r.Phase, done)
		}
		r, _, _, _, err := r.Next(env, in("continue"), w.newID)
		if err != nil {
			t.Fatal(err)
		}
		if _, done := r.At(env); !done {
			t.Fatalf("an empty world has more steps: %q", r.Phase)
		}
	})
	t.Run("someday shows at most ten a week", func(t *testing.T) {
		w := newWorld(t)
		var ops []core.Op
		for i := 0; i < 14; i++ {
			ops = append(ops, createOp(core.Item{ID: fmt.Sprintf("s%02d", i), Kind: core.KindAction, Title: fmt.Sprintf("maybe %d", i), State: core.StSomeday}))
		}
		if err := w.apply(ops); err != nil {
			t.Fatal(err)
		}
		env := ReviewEnv{User: "robin", Today: today, Spaces: []*core.State{w.st}, SomedayReviewed: map[string]string{"s00": "2026-11-12", "s01": "2026-11-12"}}
		r := Review{Phase: PhaseSomeday, Stats: map[string]int{}}
		var shown []string
		for {
			at, done := r.At(env)
			if done || at.Phase != PhaseSomeday {
				break
			}
			shown = append(shown, at.Step(env).Data["item"].(string))
			r, _, _, _, _ = at.Next(env, in("keep"), w.newID)
		}
		// The two reviewed last week come last, so they are not among the ten.
		if len(shown) != 10 || slices.Contains(shown, "s00") || slices.Contains(shown, "s01") || shown[0] != "s02" {
			t.Fatalf("someday cards: %v", shown)
		}
	})
	t.Run("bad answers", func(t *testing.T) {
		w := reviewWorld(t)
		env := w.reviewEnv(0)
		r := Review{Phase: PhaseStale, Stats: map[string]int{}}
		for _, bad := range []Input{in("activate"), in(""), in("defer", "defer_until", "soon")} {
			if _, _, _, _, err := r.Next(env, bad, w.newID); !errors.Is(err, core.ErrInvalid) {
				t.Errorf("%+v: %v", bad, err)
			}
		}
	})
	t.Run("another user's spaces are not reviewed", func(t *testing.T) {
		w := reviewWorld(t)
		env := w.reviewEnv(0)
		env.User = "dana" // not the owner of anything here
		r := Review{Phase: PhaseStale, Stats: map[string]int{}}
		if at, _ := r.At(env); at.Phase == PhaseStale || at.Phase == PhaseWaiting {
			t.Errorf("dana is asked about robin's actions: phase %q", at.Phase)
		}
	})
}
