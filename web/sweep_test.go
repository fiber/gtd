package web

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fiber/gtd/core"
	"github.com/fiber/gtd/embed"
)

// TestSweepOverHTTP: the unsorted sweep, one cluster per wizard session
// (SPEC §5.7), within the asking user's spaces only.
func TestSweepOverHTTP(t *testing.T) {
	s := newSite(t)
	robin, jo := s.login("robin"), s.login("jo")
	type start struct {
		Session string
		Done    bool
		Empty   bool
		State   string
		Step    struct {
			Key, Question, Subject string
			Fields                 []struct{ Key, Type, Value string }
			Data                   struct{ Index, Total int }
		}
	}
	begin := func(c *client, in map[string]any) start {
		t.Helper()
		var x start
		c.ok("POST", "/api/v1/wizard/sweep/start", in, &x)
		return x
	}
	// Without embeddings there is nothing to sweep, and the answer says why.
	st := begin(robin, map[string]any{})
	if !st.Done || st.State != embed.StateOff {
		t.Fatalf("sweep without embeddings: %+v", st)
	}

	project := robin.create("robin", map[string]any{"kind": "project", "title": "Kitchen renovated"})
	for i := range 12 {
		robin.create("robin", map[string]any{"kind": "note", "title": fmt.Sprintf("kitchen tiles plumber %d", i), "body": "kitchen renovation tiles plumber cabinets"})
		robin.create("robin", map[string]any{"kind": "note", "title": fmt.Sprintf("garden beds %d", i), "body": "garden raised beds zucchini compost"})
	}
	svc := embed.New(s.st, wordEmbedder{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svc.Start(ctx)
	s.srv.Embed = svc
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if x := svc.Status(); x.State == embed.StateOK && x.Queued == 0 && x.Vectors == 25 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("index did not build: %+v", svc.Status())
		}
	}

	st = begin(robin, map[string]any{})
	if st.Done || st.Step.Key != "cluster" || st.Step.Data.Total != 2 || st.Step.Data.Index != 1 || st.Step.Fields[0].Type != "checks" {
		t.Fatalf("first cluster: %+v", st)
	}
	first := st.Step.Subject
	members := strings.Split(st.Step.Fields[0].Value, ",")
	if len(members) != 12 {
		t.Fatalf("%d members", len(members))
	}
	// Skip: the next session, told to pass one over, shows the other cluster.
	var res struct {
		Done bool
		Ops  []core.Op
	}
	robin.ok("POST", "/api/v1/wizard/sweep/"+st.Session+"/step", map[string]any{"choice": "skip"}, &res)
	if !res.Done || len(res.Ops) != 0 {
		t.Fatalf("skip: %+v", res)
	}
	second := begin(robin, map[string]any{"skip": 1})
	if second.Done || second.Step.Subject == first || second.Step.Data.Index != 2 {
		t.Fatalf("after a skip: %+v", second)
	}
	kitchen := second
	if strings.Contains(first, "kitchen") {
		kitchen = begin(robin, map[string]any{})
	}
	// File all but one into the project.
	ids := strings.Split(kitchen.Step.Fields[0].Value, ",")
	robin.ok("POST", "/api/v1/wizard/sweep/"+kitchen.Session+"/step",
		map[string]any{"choice": "file", "values": map[string]string{"members": strings.Join(ids[1:], ","), "project": project}}, &res)
	if !res.Done || len(res.Ops) != 22 {
		t.Fatalf("file: done %v, %d ops", res.Done, len(res.Ops))
	}
	var notes struct {
		Counts map[string]int
	}
	robin.ok("GET", "/api/v1/notes", nil, &notes)
	if notes.Counts["filed"] != 11 || notes.Counts["unsorted"] != 13 {
		t.Errorf("counts after filing: %+v", notes.Counts)
	}
	// Beyond the last cluster: done.
	st = begin(robin, map[string]any{"skip": 9})
	if !st.Done || !st.Empty {
		t.Errorf("past the end: %+v", st)
	}
	// Isolation: jo has nothing to sweep, and cannot sweep robin's space or
	// answer in robin's session.
	st = begin(jo, map[string]any{})
	if !st.Done {
		t.Errorf("jo's sweep: %+v", st)
	}
	if code, _ := jo.do("POST", "/api/v1/wizard/sweep/start", map[string]any{"space": "robin"}, nil); code != 404 {
		t.Errorf("jo sweeping robin's space: %d", code)
	}
	st = begin(robin, map[string]any{})
	if code, _ := jo.do("POST", "/api/v1/wizard/sweep/"+st.Session+"/step", map[string]any{"choice": "archive", "values": map[string]string{"members": members[0]}}, nil); code != 404 {
		t.Errorf("jo answering in robin's session: %d", code)
	}
}
