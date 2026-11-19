package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/fiber/gtd/core"
)

// check verifies the consistency of the whole database and returns the
// problems found:
//   - the ops of every space are numbered 1..seq without gaps, and the seq key
//     says the same;
//   - every op has its opid/ entry, and every opid/ entry points at its op;
//   - replaying the op log from nothing gives exactly the stored items and
//     comments, byte for byte (so no item write exists without its op, and no
//     op without its item write);
//   - the in-memory index equals what is stored.
func check(s *Store) []string {
	var problems []string
	bad := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	ids, err := spaceIDs(s.db)
	if err != nil {
		return []string{err.Error()}
	}
	totalOps := 0
	for _, id := range ids {
		items, comments := map[string]*core.Item{}, map[string]*core.Comment{}
		var seq uint64
		err := scan(s.db, opPrefix(id), func(key string, v []byte) error {
			var op core.Op
			if err := json.Unmarshal(v, &op); err != nil {
				return err
			}
			seq++
			totalOps++
			if op.Seq != seq || key != string(opKey(id, seq)) {
				bad("space %q: op %s has seq %d at position %d", id, op.ID, op.Seq, seq)
			}
			var ref opRef
			if err := getJSON(s.db, opidKey(op.ID), &ref); err != nil || ref != (opRef{id, op.Seq}) {
				bad("space %q: op %s seq %d has opid entry %+v (%v)", id, op.ID, op.Seq, ref, err)
			}
			if err := core.Replay(items, comments, op); err != nil {
				bad("space %q: replay of op %s: %v", id, op.ID, err)
			}
			return nil
		})
		if err != nil {
			bad("space %q: %v", id, err)
		}
		if stored, err := readSeq(s.db, id); err != nil || stored != seq {
			bad("space %q: seq key says %d (%v), the log ends at %d", id, stored, err, seq)
		}

		storedItems := map[string]string{}
		scan(s.db, itemPrefix(id), func(key string, v []byte) error {
			storedItems[key[len(itemPrefix(id)):]] = string(v)
			return nil
		})
		for iid, it := range items {
			if storedItems[iid] != string(mustJSON(it)) {
				bad("space %q: item %s: stored %s, op log gives %s", id, iid, storedItems[iid], mustJSON(it))
			}
		}
		if len(storedItems) != len(items) {
			bad("space %q: %d items stored, op log gives %d", id, len(storedItems), len(items))
		}
		storedComments := 0
		scan(s.db, commentPrefix(id), func(key string, v []byte) error {
			storedComments++
			var c core.Comment
			json.Unmarshal(v, &c)
			if r := comments[c.ID]; r == nil || string(mustJSON(r)) != string(v) || key != string(commentKey(id, c.Item, c.ID)) {
				bad("space %q: comment at %q: stored %s, op log gives %s", id, key, v, mustJSON(r))
			}
			return nil
		})
		if storedComments != len(comments) {
			bad("space %q: %d comments stored, op log gives %d", id, storedComments, len(comments))
		}

		sp := s.spaces[id]
		if sp == nil {
			bad("space %q is not in the index", id)
			continue
		}
		if sp.seq != seq || len(sp.items) != len(items) {
			bad("space %q: index has seq %d and %d items, stored are seq %d and %d items", id, sp.seq, len(sp.items), seq, len(items))
		}
		for iid, it := range sp.items {
			if storedItems[iid] != string(mustJSON(it)) {
				bad("space %q: item %s differs between index and store", id, iid)
			}
		}
	}
	refs := 0
	scan(s.db, "opid/", func(string, []byte) error { refs++; return nil })
	if refs != totalOps {
		bad("%d opid entries for %d ops", refs, totalOps)
	}
	return problems
}

// TestCrashWriter is the child process of TestCrash. It writes until it is killed.
func TestCrashWriter(t *testing.T) {
	dir := os.Getenv("GTD_CRASH_DIR")
	if dir == "" {
		t.Skip("child process of TestCrash")
	}
	s := must(Open(dir))
	fmt.Println("ready")
	for _, w := range []struct{ user, space string }{{"sven", "nis"}, {"eric", "eric"}} {
		go func() {
			v := must(s.View(w.user))
			for {
				// Multi-key batches: two creates, then a completion that derives an
				// unblock op and a notification, then a comment.
				a, b := s.NewID(), s.NewID()
				must(v.Apply(w.space,
					createOp(core.Item{ID: a, Kind: core.KindAction, Title: "blocker", Owner: w.user}),
					createOp(core.Item{ID: b, Kind: core.KindAction, Title: "dependent", Owner: w.user, BlockedBy: []string{a}, State: core.StPlanned})))
				must(v.Apply(w.space, moveOp(must(v.Item(w.space, a)), core.StDone)))
				must(v.Apply(w.space, core.Op{Via: "cli", Type: core.OpComment, Item: b, Payload: raw(core.Comment{Body: strings.Repeat("x", 300)})}))
			}
		}()
	}
	select {}
}

// TestCrash kills a writing process with SIGKILL several times and checks the
// database after each kill.
func TestCrash(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns and kills processes")
	}
	dir := t.TempDir()
	s := must(Create(dir, "crash"))
	check0(s.AddUser(core.User{ID: "sven"}))
	check0(s.AddUser(core.User{ID: "eric"}))
	check0(s.CreateSpace("nis", "", []string{"sven", "eric"}))
	check0(s.Close())

	const rounds = 8
	prev := 0
	for round := 1; round <= rounds; round++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCrashWriter$")
		cmd.Env = append(os.Environ(), "GTD_CRASH_DIR="+dir)
		out := must(cmd.StdoutPipe())
		check0(cmd.Start())
		if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "ready\n" {
			cmd.Process.Kill()
			t.Fatalf("writer did not start: %q %v", line, err)
		}
		time.Sleep(time.Duration(40+rand.IntN(260)) * time.Millisecond)
		check0(cmd.Process.Kill()) // SIGKILL
		cmd.Wait()

		s, err := Open(dir)
		if err != nil {
			t.Fatalf("round %d: open after kill -9: %v", round, err)
		}
		for _, p := range check(s) {
			t.Errorf("round %d: %s", round, p)
		}
		// Notifications are written in the batch of the op that causes them:
		// here every system op is an unblock with exactly one notification.
		ops, system, notes := 0, 0, 0
		for _, id := range []string{"nis", "eric"} {
			scan(s.db, opPrefix(id), func(_ string, v []byte) error {
				ops++
				if strings.Contains(string(v), `"actor":"system"`) {
					system++
				}
				return nil
			})
		}
		for _, u := range []string{"sven", "eric"} {
			var last uint64
			for _, n := range must(must(s.View(u)).Notifications()) {
				notes++
				if n.Seq != last+1 {
					t.Errorf("round %d: user %s: notification seq %d follows %d", round, u, n.Seq, last)
				}
				last = n.Seq
			}
		}
		if system != notes {
			t.Errorf("round %d: %d system ops but %d notifications", round, system, notes)
		}
		if ops <= prev {
			t.Errorf("round %d: no ops were written before the kill (%d ops)", round, ops)
		}
		t.Logf("round %d: killed with SIGKILL, reopened: %d ops (+%d), %d notifications, consistent", round, ops, ops-prev, notes)
		prev = ops
		check0(s.Close())
		if t.Failed() {
			return
		}
	}
}
