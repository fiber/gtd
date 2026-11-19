package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/fiber/gtd/core"
	"github.com/fiber/gtd/store"
)

var gtdBin string

// TestMain builds the real binary. The tests run it as separate processes,
// with and without a daemon.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gtd")
	if err != nil {
		panic(err)
	}
	gtdBin = filepath.Join(dir, "gtd")
	if out, err := exec.Command("go", "build", "-o", gtdBin, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// env is one data directory plus ssh-agents holding each user's key.
type env struct {
	t      *testing.T
	data   string
	agents map[string]string // user → SSH_AUTH_SOCK
	pubs   map[string]string // user → public key file
}

// newEnv uses a short temp path: Unix socket paths are limited to ~100 bytes.
func newEnv(t *testing.T, users ...string) *env {
	dir, err := os.MkdirTemp("", "gtd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	e := &env{t: t, data: filepath.Join(dir, "d"), agents: map[string]string{}, pubs: map[string]string{}}
	for _, u := range users {
		_, priv, _ := ed25519.GenerateKey(rand.Reader)
		keyring := agent.NewKeyring()
		if err := keyring.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
			t.Fatal(err)
		}
		signer, _ := ssh.NewSignerFromKey(priv)
		e.pubs[u] = filepath.Join(dir, u+".pub")
		os.WriteFile(e.pubs[u], ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o644)
		e.agents[u] = filepath.Join(dir, u+".agent")
		ln, err := net.Listen("unix", e.agents[u])
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				go func() { agent.ServeAgent(keyring, conn); conn.Close() }()
			}
		}()
	}
	return e
}

func (e *env) cmd(user string, args ...string) *exec.Cmd {
	cmd := exec.Command(gtdBin, append([]string{"-data", e.data, "-user", user}, args...)...)
	sock := e.agents[user]
	if sock == "" {
		sock = e.agents["sven"] // users without a key of their own try sven's agent
	}
	cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK="+sock)
	return cmd
}

// run executes gtd as user and returns its stdout. It fails the test on error.
func (e *env) run(user string, args ...string) string {
	e.t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := e.cmd(user, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		e.t.Fatalf("gtd %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String()
}

// fail executes gtd, expects a non-zero exit and returns stderr.
func (e *env) fail(user string, args ...string) string {
	e.t.Helper()
	var stderr bytes.Buffer
	cmd := e.cmd(user, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		e.t.Fatalf("gtd %s: succeeded, want an error", strings.Join(args, " "))
	}
	return stderr.String()
}

// serve starts the daemon and returns a function that stops it with SIGTERM.
func (e *env) serve() (stop func()) {
	e.t.Helper()
	cmd := e.cmd("sven", "serve")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cmd.Process.Signal(syscall.SIGTERM)
		if err := cmd.Wait(); err != nil {
			e.t.Errorf("daemon exit: %v\n%s", err, out.String())
		}
		if _, err := os.Stat(socketPath(e.data)); err == nil {
			e.t.Error("daemon left its socket behind")
		}
	}
	e.t.Cleanup(stop)
	for i := 0; i < 200; i++ {
		if conn, err := net.Dial("unix", socketPath(e.data)); err == nil {
			conn.Close()
			return stop
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("daemon did not start:\n%s", out.String())
	return nil
}

func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		rel, _ := filepath.Rel(dir, path)
		out[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(t *testing.T, what, a, b string) {
	t.Helper()
	ta, tb := tree(t, a), tree(t, b)
	if len(ta) == 0 {
		t.Fatalf("%s: %s is empty", what, a)
	}
	for name, data := range ta {
		if other, ok := tb[name]; !ok {
			t.Errorf("%s: %s only in %s", what, name, a)
		} else if other != data {
			t.Errorf("%s: %s differs\n%q\n%q", what, name, data, other)
		}
	}
	for name := range tb {
		if _, ok := ta[name]; !ok {
			t.Errorf("%s: %s only in %s", what, name, b)
		}
	}
}

// setup bootstraps sven (admin), eric and grit, the shared space nis, and a
// few captures, all by direct database access.
func setup(t *testing.T) *env {
	e := newEnv(t, "sven", "eric", "grit")
	if msg := e.fail("sven", "grep", "x"); !strings.Contains(msg, "no database") {
		t.Fatalf("grep without a database: %s", msg)
	}
	if msg := e.fail("sven", "user", "add", "sven"); !strings.Contains(msg, "no database") {
		t.Fatalf("non-admin bootstrap: %s", msg)
	}
	e.run("sven", "user", "add", "-admin", "-name", "Sven", "-ssh-key", e.pubs["sven"], "sven")
	e.run("sven", "user", "add", "-ssh-key", e.pubs["eric"], "eric")
	e.run("sven", "user", "add", "grit")
	e.run("sven", "user", "key-add", "grit", e.pubs["grit"])
	e.run("sven", "space", "create", "-name", "NIS", "nis", "sven", "eric")
	e.run("sven", "add", "bgp collector reset thing ask eric")
	att := filepath.Join(filepath.Dir(e.data), "whiteboard.png")
	os.WriteFile(att, []byte("\x89PNG not really"), 0o644)
	e.run("sven", "add", "-file", att, "design session whiteboard")
	e.run("eric", "add", "eric's private capture about the collector")
	return e
}

// TestExportWithAndWithoutDaemon is the M0 acceptance criterion: the same
// export output either way. It also compares the other read commands.
func TestExportWithAndWithoutDaemon(t *testing.T) {
	e := setup(t)
	out := filepath.Dir(e.data)
	direct, viaDaemon := filepath.Join(out, "direct"), filepath.Join(out, "daemon")

	msgDirect := e.run("sven", "export", direct)
	grepDirect := e.run("sven", "grep", "collector")
	opsDirect := e.run("sven", "ops", "tail")
	partialDirect := filepath.Join(out, "pd")
	e.run("eric", "export", "-space", "eric", partialDirect)

	stop := e.serve()
	msgDaemon := e.run("sven", "export", viaDaemon)
	sameTree(t, "export direct vs daemon", direct, viaDaemon)
	if a, b := strings.Replace(msgDirect, direct, "", 1), strings.Replace(msgDaemon, viaDaemon, "", 1); a != b {
		t.Errorf("export message differs:\n%s%s", a, b)
	}
	if got := e.run("sven", "grep", "collector"); got != grepDirect || !strings.Contains(got, "bgp collector reset thing ask eric") {
		t.Errorf("grep differs:\ndirect: %s\ndaemon: %s", grepDirect, got)
	}
	if strings.Contains(grepDirect, "eric's private") {
		t.Errorf("sven's grep shows eric's capture:\n%s", grepDirect)
	}
	if got := e.run("sven", "ops", "tail"); got != opsDirect || strings.Count(got, "\n") != 2 {
		t.Errorf("ops tail differs:\ndirect: %s\ndaemon: %s", opsDirect, got)
	}
	partialDaemon := filepath.Join(out, "pm")
	e.run("eric", "export", "-space", "eric", partialDaemon)
	sameTree(t, "partial export direct vs daemon", partialDirect, partialDaemon)

	// Writes through the daemon, then both modes again on the new state.
	id := strings.TrimSpace(e.run("sven", "add", "captured through the socket"))
	if len(id) != 26 {
		t.Fatalf("add printed %q", id)
	}
	daemon2, direct2 := filepath.Join(out, "daemon2"), filepath.Join(out, "direct2")
	e.run("sven", "export", daemon2)
	if _, ok := tree(t, daemon2)["spaces/sven/items/"+id+".md"]; !ok {
		t.Errorf("export lacks the capture made through the daemon")
	}
	stop()
	e.run("sven", "export", direct2)
	sameTree(t, "export daemon vs direct after more writes", daemon2, direct2)

	// An export restores into an empty data directory and exports identically.
	e2 := &env{t: t, data: filepath.Join(out, "restored"), agents: e.agents, pubs: e.pubs}
	e2.run("sven", "import", "-restore", direct2)
	again := filepath.Join(out, "again")
	e2.run("sven", "export", again)
	sameTree(t, "export after import -restore", direct2, again)
	if msg := e2.fail("sven", "import", "-restore", direct2); !strings.Contains(msg, "already holds a database") {
		t.Errorf("second restore: %s", msg)
	}
}

// TestDaemonAuth: over the socket every command needs the SSH-key challenge,
// admin commands need the admin flag, and non-members get "not found".
func TestDaemonAuth(t *testing.T) {
	e := setup(t)
	e.serve()

	// Admin over the socket.
	e.run("sven", "space", "add-member", "nis", "grit")
	e.run("sven", "space", "rm-member", "nis", "grit")
	e.run("sven", "user", "add", "kid")

	for _, c := range []struct {
		name string
		user string
		args []string
		want string
	}{
		{"user add by a non-admin", "eric", []string{"user", "add", "mallory"}, "needs an admin"},
		{"space create by a non-admin", "eric", []string{"space", "create", "x", "eric"}, "needs an admin"},
		{"snapshot by a non-admin", "eric", []string{"snapshot", "create"}, "needs an admin"},
		{"full export by a non-admin", "eric", []string{"export", filepath.Join(filepath.Dir(e.data), "x")}, "needs an admin"},
		{"ops of a space as a non-member", "grit", []string{"ops", "tail", "-space", "nis"}, `not found: space "nis"`},
		{"ops of a missing space", "grit", []string{"ops", "tail", "-space", "nope"}, `not found: space "nope"`},
		{"export of a space as a non-member", "grit", []string{"export", "-space", "nis", filepath.Join(filepath.Dir(e.data), "y")}, `not found: space "nis"`},
		{"ops of someone's personal space", "eric", []string{"ops", "tail", "-space", "sven"}, `not found: space "sven"`},
		{"unknown user", "mallory", []string{"grep", "x"}, "authentication failed"},
		{"user without a registered key", "kid", []string{"grep", "x"}, "authentication failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if msg := e.fail(c.user, c.args...); !strings.Contains(msg, c.want) {
				t.Errorf("stderr = %q, want %q", msg, c.want)
			}
		})
	}
	t.Run("another user's key", func(t *testing.T) {
		cmd := e.cmd("sven", "grep", "collector")
		cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK="+e.agents["eric"])
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "authentication failed") {
			t.Errorf("sven with eric's key: %v %s", err, out)
		}
	})
	t.Run("no agent", func(t *testing.T) {
		cmd := e.cmd("sven", "grep", "collector")
		cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=")
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "SSH_AUTH_SOCK is not set") {
			t.Errorf("without an agent: %v %s", err, out)
		}
	})
	t.Run("named key", func(t *testing.T) {
		if out := e.run("sven", "-key", e.pubs["sven"], "grep", "whiteboard"); !strings.Contains(out, "design session whiteboard") {
			t.Errorf("grep = %q", out)
		}
		if msg := e.fail("sven", "-key", e.pubs["eric"], "grep", "x"); !strings.Contains(msg, "does not hold the key") {
			t.Errorf("stderr = %q", msg)
		}
	})
	t.Run("a removed key stops working", func(t *testing.T) {
		e.run("eric", "grep", "collector")
		e.run("sven", "user", "key-rm", "eric", e.pubs["eric"])
		if msg := e.fail("eric", "grep", "collector"); !strings.Contains(msg, "authentication failed") {
			t.Errorf("stderr = %q", msg)
		}
	})
	t.Run("member sees the shared space, others do not", func(t *testing.T) {
		if out := e.run("sven", "ops", "tail", "-space", "nis"); out != "" {
			t.Errorf("nis has ops: %q", out)
		}
	})
	if info, err := os.Stat(socketPath(e.data)); err != nil || info.Mode().Perm() != 0o660 {
		t.Errorf("socket mode: %v %v", info.Mode(), err)
	}
	// With the daemon running, nothing else can open the database.
	if _, err := store.Open(e.data); err == nil {
		t.Error("the database opened while the daemon holds it")
	}
}

func TestSnapshotCommands(t *testing.T) {
	e := setup(t)
	out := filepath.Dir(e.data)
	stop := e.serve()
	name := strings.TrimSpace(e.run("sven", "snapshot", "create", "-label", "nightly"))
	if !strings.HasSuffix(name, "-nightly") {
		t.Fatalf("snapshot name %q", name)
	}
	atSnapshot := filepath.Join(out, "at")
	e.run("sven", "export", atSnapshot)
	e.run("sven", "add", "after the snapshot")

	if list := e.run("sven", "snapshot", "list"); !strings.Contains(list, name) || !strings.Contains(list, "1 blobs") {
		t.Errorf("list = %q", list)
	}
	if got := e.run("sven", "snapshot", "verify"); got != name+" ok\n" {
		t.Errorf("verify = %q", got)
	}
	fromSnapshot := filepath.Join(out, "from")
	e.run("sven", "export", "-from", name, fromSnapshot)
	sameTree(t, "export -from snapshot vs export at snapshot time", atSnapshot, fromSnapshot)

	// Corrupt the one blob: flip a bit.
	var blob string
	filepath.WalkDir(filepath.Join(e.data, "blobs"), func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && core.ValidSHA256(d.Name()) {
			blob = path
		}
		return nil
	})
	orig, _ := os.ReadFile(blob)
	bad := bytes.Clone(orig)
	bad[3] ^= 0x10
	os.Chmod(blob, 0o600)
	os.WriteFile(blob, bad, 0o600)
	msg := e.fail("sven", "snapshot", "verify", name)
	if !strings.Contains(msg, "blob "+filepath.Base(blob)+" is corrupt") {
		t.Errorf("verify of a corrupted blob: %q", msg)
	}
	t.Logf("gtd snapshot verify: %s", strings.TrimSpace(msg))
	os.WriteFile(blob, orig, 0o600)

	if msg := e.fail("sven", "restore", name); !strings.Contains(msg, "stop the daemon") {
		t.Errorf("restore with a running daemon: %q", msg)
	}
	stop()
	if got := e.run("sven", "restore", name); !strings.Contains(got, "previous database is kept at") {
		t.Errorf("restore = %q", got)
	}
	afterRestore := filepath.Join(out, "after")
	e.run("sven", "export", afterRestore)
	sameTree(t, "export after restore vs export at snapshot time", atSnapshot, afterRestore)

	if msg := e.fail("sven", "snapshot", "prune"); !strings.Contains(msg, "no retention configured") {
		t.Errorf("prune without config: %q", msg)
	}
	os.WriteFile(filepath.Join(e.data, "config.yaml"), []byte("server: {name: home}\nsnapshots: {schedule: hourly, keep: {hourly: 1}}\nembed: {provider: norad}\n"), 0o644)
	second := strings.TrimSpace(e.run("sven", "snapshot", "create"))
	if got := e.run("sven", "snapshot", "prune"); got != "deleted "+name+"\n" {
		t.Errorf("prune = %q", got)
	}
	if list := e.run("sven", "snapshot", "list"); strings.Contains(list, name) || !strings.Contains(list, second) {
		t.Errorf("list after prune = %q", list)
	}
}

// TestScheduledSnapshot drives the daemon's snapshot schedule with a fake clock.
func TestScheduledSnapshot(t *testing.T) {
	st, err := store.Create(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
	st.Now = func() time.Time { return now }
	keep := store.Keep{Hourly: 2}
	created := 0
	for minute := 0; minute <= 5*60; minute++ { // five hours, one tick per minute
		name, _, err := scheduledSnapshot(st, time.Hour, keep)
		if err != nil {
			t.Fatal(err)
		}
		if name != "" {
			created++
		}
		now = now.Add(time.Minute)
	}
	list, _ := store.ListSnapshots(st.Dir())
	if created != 6 || len(list) != 2 {
		t.Fatalf("created %d snapshots (want 6: at 0h..5h), %d left after retention (want 2)", created, len(list))
	}
	if got := scheduleInterval("daily"); got != 24*time.Hour || scheduleInterval("") != 0 {
		t.Fatalf("intervals: %v", got)
	}
}

func TestUsageErrors(t *testing.T) {
	e := newEnv(t, "sven")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"frobnicate"}, "unknown command"},
		{[]string{"user"}, "missing subcommand"},
		{[]string{"export"}, "want a target directory"},
		{[]string{"import", "x"}, "only `import -restore <dir>`"},
		{[]string{"restore", "nope"}, "snapshot nope"},
	} {
		if msg := e.fail("sven", c.args...); !strings.Contains(msg, c.want) {
			t.Errorf("gtd %v: %q, want %q", c.args, msg, c.want)
		}
	}
	cmd := exec.Command(gtdBin, "grep", "x")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "no data directory") {
		t.Errorf("without -data: %v %s", err, out)
	}
}
