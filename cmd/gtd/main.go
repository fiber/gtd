// Command gtd is the gtd server and its CLI. `gtd serve` is the daemon. Every
// other subcommand talks to the daemon over its Unix socket, or opens the
// database directly when no daemon is running (STORAGE §1).
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/fiber/gtd/core"
	"github.com/fiber/gtd/store"
	"github.com/fiber/gtd/web"
)

const usage = `usage: gtd [-data dir] [-user id] [-key pubkey-file] <command>

  serve                                     run the daemon (Unix socket <data>/gtd.sock)
  add [-file path]... <text>                capture into your inbox (text from stdin if omitted)
  grep <words>                              search your spaces
  ops tail [-space id] [-n 20]              show the last ops of a space
  export [-space id]... [-from snapshot] <dir>
  import -restore <dir>                     rebuild an empty data dir from a full export
  calibrate [-space id]                     similarity score distributions and cluster statistics
  import obsidian [-space id] [-dry-run] <vault>
                                            import an Obsidian vault as unsorted notes
  snapshot create [-label l] | list | prune | verify [name]...
  restore <snapshot>                        replace the database with a snapshot (daemon stopped)
  reindex                                   drop all embeddings and build them again (needs the daemon)
  user add [-admin] [-name n] [-ssh-key k] <id> | key-add <id> <key> | key-rm <id> <key>
  user passwd <id>                          set the web password (prompted, or one line on stdin)
  space create [-name n] <id> [member]... | add-member <space> <user> | rm-member <space> <user>

  -data  data directory (default $GTD_DATA)
  -user  user to act as (default $GTD_USER, then $USER)
  -key   public key file of the ssh-agent key to authenticate with (default: try all)
`

type globals struct {
	data string
	user string
	key  string
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "gtd: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	var g globals
	fs := flag.NewFlagSet("gtd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	fs.StringVar(&g.data, "data", os.Getenv("GTD_DATA"), "")
	fs.StringVar(&g.user, "user", os.Getenv("GTD_USER"), "")
	fs.StringVar(&g.key, "key", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	args = fs.Args()
	if len(args) == 0 {
		fs.Usage()
		return errors.New("no command")
	}
	if g.data == "" {
		return errors.New("no data directory: set -data or GTD_DATA")
	}
	if g.user == "" {
		if u, err := user.Current(); err == nil {
			g.user = u.Username
		}
	}
	if g.key != "" {
		data, err := os.ReadFile(g.key)
		if err != nil {
			return err
		}
		g.key = string(data)
	}
	cmd, args := args[0], args[1:]
	sub := func() (string, []string, error) {
		if len(args) == 0 {
			return "", nil, fmt.Errorf("gtd %s: missing subcommand\n%s", cmd, usage)
		}
		return args[0], args[1:], nil
	}
	switch cmd {
	case "serve":
		return serve(g, stdout, stderr)
	case "add":
		return cmdAdd(g, args, stdin, stdout)
	case "grep":
		return cmdGrep(g, args, stdout, stderr)
	case "export":
		return cmdExport(g, args, stdout)
	case "import":
		return cmdImport(g, args, stdout)
	case "restore":
		return cmdRestore(g, args, stdout)
	case "calibrate":
		return cmdCalibrate(g, args, stdout)
	case "reindex":
		c, err := connect(g, false)
		if err != nil {
			return err
		}
		defer c.close()
		var st struct {
			Queued int `json:"queued"`
		}
		if err := c.do("POST", "/api/v1/admin/reindex", nil, &st); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "all vectors dropped, %d items queued for embedding\n", st.Queued)
		return nil
	case "ops":
		s, rest, err := sub()
		if err != nil {
			return err
		}
		if s != "tail" {
			return fmt.Errorf("gtd ops: unknown subcommand %q", s)
		}
		return cmdOpsTail(g, rest, stdout)
	case "snapshot":
		s, rest, err := sub()
		if err != nil {
			return err
		}
		return cmdSnapshot(g, s, rest, stdout)
	case "user":
		s, rest, err := sub()
		if err != nil {
			return err
		}
		return cmdUser(g, s, rest, stdin, stdout)
	case "space":
		s, rest, err := sub()
		if err != nil {
			return err
		}
		return cmdSpace(g, s, rest, stdout)
	}
	return fmt.Errorf("unknown command %q\n%s", cmd, usage)
}

// flags parses the flags of one command and returns the remaining arguments.
func flags(name string, args []string, setup func(*flag.FlagSet)) ([]string, error) {
	fs := flag.NewFlagSet("gtd "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if setup != nil {
		setup(fs)
	}
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("gtd %s: %v", name, err)
	}
	return fs.Args(), nil
}

// list is a repeatable string flag.
type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func want(name string, args []string, n int, what string) error {
	if len(args) != n {
		return fmt.Errorf("gtd %s: want %s", name, what)
	}
	return nil
}

func cmdAdd(g globals, args []string, stdin io.Reader, stdout io.Writer) error {
	var files list
	args, err := flags("add", args, func(fs *flag.FlagSet) { fs.Var(&files, "file", "") })
	if err != nil {
		return err
	}
	text := strings.Join(args, " ")
	if text == "" && len(files) == 0 {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		text = string(data)
	}
	c, err := connect(g, false)
	if err != nil {
		return err
	}
	defer c.close()
	var out struct {
		ID string `json:"id"`
	}
	if len(files) == 0 {
		in := map[string]any{"text": text, "source": core.Source{Via: "cli"}}
		if err := c.do("POST", "/api/v1/capture", in, &out); err != nil {
			return err
		}
	} else {
		// Stream the files: the request is written while the server reads it.
		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		go func() {
			err := func() error {
				if err := mw.WriteField("text", text); err != nil {
					return err
				}
				if err := mw.WriteField("via", "cli"); err != nil {
					return err
				}
				for _, path := range files {
					f, err := os.Open(path)
					if err != nil {
						return err
					}
					part, err := mw.CreateFormFile("file", filepath.Base(path))
					if err == nil {
						_, err = io.Copy(part, f)
					}
					f.Close()
					if err != nil {
						return err
					}
				}
				return mw.Close()
			}()
			pw.CloseWithError(err)
		}()
		resp, err := c.send("POST", "/api/v1/capture", mw.FormDataContentType(), pr)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return err
		}
	}
	fmt.Fprintln(stdout, out.ID)
	return nil
}

func cmdGrep(g globals, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("gtd grep: want search words")
	}
	c, err := connect(g, false)
	if err != nil {
		return err
	}
	defer c.close()
	var res web.SearchResult
	if err := c.do("GET", "/api/v1/search?q="+url.QueryEscape(strings.Join(args, " ")), nil, &res); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, h := range res.Hits {
		mark := ""
		if h.Similar { // found by meaning, not by the words
			mark = fmt.Sprintf("  ~%.2f", h.Score)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s/%s\t%s%s\n", h.ID, h.Space, h.Kind, h.State, h.Title, mark)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	// Say so when the answer is a text match only (never silently).
	switch res.Semantic {
	case "offline":
		fmt.Fprintln(stderr, "gtd: text match only: the embedding backend does not answer")
	case "building":
		fmt.Fprintln(stderr, "gtd: text match only for some spaces: their embeddings are being built")
	}
	return nil
}

func cmdOpsTail(g globals, args []string, stdout io.Writer) error {
	space, n := "", 20
	args, err := flags("ops tail", args, func(fs *flag.FlagSet) {
		fs.StringVar(&space, "space", "", "")
		fs.IntVar(&n, "n", 20, "")
	})
	if err != nil {
		return err
	}
	if err := want("ops tail", args, 0, "no arguments"); err != nil {
		return err
	}
	c, err := connect(g, false)
	if err != nil {
		return err
	}
	defer c.close()
	var ops []core.Op
	q := url.Values{"space": {space}, "n": {strconv.Itoa(n)}}
	if err := c.do("GET", "/api/v1/ops?"+q.Encode(), nil, &ops); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, op := range ops {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", op.Seq, op.TS.Format(time.RFC3339), op.Actor, op.Via, op.Type, op.Item, opSummary(op))
	}
	return tw.Flush()
}

// opSummary says in a few words what an op did.
func opSummary(op core.Op) string {
	short := func(raw json.RawMessage) string {
		s := string(raw)
		if s == "" {
			s = `""`
		}
		if len(s) > 40 {
			s = s[:37] + "..."
		}
		return s
	}
	switch op.Type {
	case core.OpCreate:
		var it core.Item
		json.Unmarshal(op.Payload, &it)
		return fmt.Sprintf("%s/%s %q", it.Kind, it.State, it.Title)
	case core.OpAttach:
		var a core.Attachment
		json.Unmarshal(op.Payload, &a)
		return fmt.Sprintf("%s (%d bytes)", a.Name, a.Size)
	case core.OpComment:
		var c core.Comment
		json.Unmarshal(op.Payload, &c)
		return short(json.RawMessage(strconv.Quote(c.Body)))
	case core.OpDeleteComment:
		return short(op.Payload)
	}
	var parts []string
	for _, ch := range op.Changes {
		parts = append(parts, fmt.Sprintf("%s: %s → %s", ch.Field, short(ch.Old), short(ch.New)))
	}
	return strings.Join(parts, ", ")
}

func cmdExport(g globals, args []string, stdout io.Writer) error {
	var spaces list
	from := ""
	args, err := flags("export", args, func(fs *flag.FlagSet) {
		fs.Var(&spaces, "space", "")
		fs.StringVar(&from, "from", "", "")
	})
	if err != nil {
		return err
	}
	if err := want("export", args, 1, "a target directory"); err != nil {
		return err
	}
	dir := args[0]
	if from != "" {
		// A snapshot is a directory of its own: no daemon and no lock involved.
		st, err := store.OpenSnapshot(g.data, from)
		if err != nil {
			return err
		}
		defer st.Close()
		pr, pw := io.Pipe()
		go func() { pw.CloseWithError(st.Export(pw, spaces)) }()
		if err := store.Untar(pr, dir); err != nil {
			return err
		}
	} else {
		c, err := connect(g, false)
		if err != nil {
			return err
		}
		defer c.close()
		resp, err := c.send("GET", "/api/v1/export?"+url.Values{"space": spaces}.Encode(), "", nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := store.Untar(resp.Body, dir); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return fmt.Errorf("export is incomplete: %w", err)
	}
	var man store.Manifest
	if err := json.Unmarshal(data, &man); err != nil {
		return err
	}
	fmt.Fprintln(stdout, manifestSummary("exported", &man, dir))
	return nil
}

func manifestSummary(verb string, man *store.Manifest, dir string) string {
	items, comments, ops := 0, 0, 0
	for _, sp := range man.Spaces {
		items += sp.Items
		comments += sp.Comments
		ops += sp.Ops
	}
	return fmt.Sprintf("%s %d spaces, %d users, %d items, %d comments, %d ops, %d blobs: %s",
		verb, len(man.Spaces), man.Users, items, comments, ops, man.Blobs, dir)
}

func cmdImport(g globals, args []string, stdout io.Writer) error {
	if len(args) > 0 && args[0] == "obsidian" {
		return importObsidian(g, args[1:], stdout)
	}
	restore := false
	args, err := flags("import", args, func(fs *flag.FlagSet) { fs.BoolVar(&restore, "restore", false, "") })
	if err != nil {
		return err
	}
	if !restore {
		return errors.New("gtd import: want `import obsidian …` or `import -restore <dir>`")
	}
	if err := want("import -restore", args, 1, "an export directory"); err != nil {
		return err
	}
	man, err := store.Restore(g.data, args[0])
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, manifestSummary("restored", man, g.data))
	return nil
}

func cmdRestore(g globals, args []string, stdout io.Writer) error {
	if err := want("restore", args, 1, "a snapshot name (see `gtd snapshot list`)"); err != nil {
		return err
	}
	moved, err := store.RestoreSnapshot(g.data, args[0], time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "restored snapshot %s\n", args[0])
	if moved != "" {
		fmt.Fprintf(stdout, "the previous database is kept at %s\n", moved)
	}
	return nil
}

func cmdSnapshot(g globals, sub string, args []string, stdout io.Writer) error {
	switch sub {
	case "create":
		label := ""
		args, err := flags("snapshot create", args, func(fs *flag.FlagSet) { fs.StringVar(&label, "label", "", "") })
		if err != nil {
			return err
		}
		if err := want("snapshot create", args, 0, "no arguments"); err != nil {
			return err
		}
		c, err := connect(g, false)
		if err != nil {
			return err
		}
		defer c.close()
		var out struct {
			Name string `json:"name"`
		}
		if err := c.do("POST", "/api/v1/admin/snapshots", map[string]string{"label": label}, &out); err != nil {
			return err
		}
		fmt.Fprintln(stdout, out.Name)
		return nil
	case "list":
		list, err := store.ListSnapshots(g.data)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		for _, m := range list {
			ops := uint64(0)
			for _, seq := range m.Seqs {
				ops += seq
			}
			fmt.Fprintf(tw, "%s\t%s\t%d spaces\t%d ops\t%d blobs\n", m.Name, m.Created.Format(time.RFC3339), len(m.Seqs), ops, len(m.Blobs))
		}
		return tw.Flush()
	case "prune":
		cfg, err := loadConfig(g.data)
		if err != nil {
			return err
		}
		if cfg.Snapshots.Keep == (store.Keep{}) {
			return fmt.Errorf("no retention configured: set snapshots.keep in %s", filepath.Join(g.data, "config.yaml"))
		}
		del, err := store.PruneSnapshots(g.data, cfg.Snapshots.Keep)
		if err != nil {
			return err
		}
		for _, name := range del {
			fmt.Fprintf(stdout, "deleted %s\n", name)
		}
		return nil
	case "verify":
		if len(args) == 0 {
			list, err := store.ListSnapshots(g.data)
			if err != nil {
				return err
			}
			for _, m := range list {
				args = append(args, m.Name)
			}
		}
		var failed []error
		for _, name := range args {
			if err := store.VerifySnapshot(g.data, name); err != nil {
				failed = append(failed, err)
				continue
			}
			fmt.Fprintf(stdout, "%s ok\n", name)
		}
		return errors.Join(failed...)
	}
	return fmt.Errorf("gtd snapshot: unknown subcommand %q", sub)
}

// keyArg accepts a public key as a file path or as the key line itself.
func keyArg(arg string) string {
	if data, err := os.ReadFile(arg); err == nil {
		return string(data)
	}
	return arg
}

func cmdUser(g globals, sub string, args []string, stdin io.Reader, stdout io.Writer) error {
	switch sub {
	case "passwd":
		if err := want("user passwd", args, 1, "a user id"); err != nil {
			return err
		}
		var password string
		if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			fmt.Fprintf(stdout, "New password for %s: ", args[0])
			pw, err := term.ReadPassword(int(f.Fd()))
			fmt.Fprintln(stdout)
			if err != nil {
				return err
			}
			password = string(pw)
		} else {
			line, err := bufio.NewReader(stdin).ReadString('\n')
			if err != nil && line == "" {
				return fmt.Errorf("gtd user passwd: no password on stdin")
			}
			password = strings.TrimRight(line, "\r\n")
		}
		c, err := connect(g, false)
		if err != nil {
			return err
		}
		defer c.close()
		if err := c.do("PUT", "/api/v1/admin/users/"+url.PathEscape(args[0])+"/password", map[string]string{"password": password}, nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "password set for %s\n", args[0])
		return nil
	case "add":
		var admin bool
		var name, key string
		args, err := flags("user add", args, func(fs *flag.FlagSet) {
			fs.BoolVar(&admin, "admin", false, "")
			fs.StringVar(&name, "name", "", "")
			fs.StringVar(&key, "ssh-key", "", "")
		})
		if err != nil {
			return err
		}
		if err := want("user add", args, 1, "a user id"); err != nil {
			return err
		}
		in := map[string]any{"id": args[0], "name": name, "admin": admin}
		if key != "" {
			in["ssh_keys"] = []string{keyArg(key)}
		}
		// Bootstrap: the first admin is created by direct access to a new database.
		c, err := connect(g, admin)
		if err != nil {
			return err
		}
		defer c.close()
		if err := c.do("POST", "/api/v1/admin/users", in, nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "user %s created with personal space %s\n", args[0], args[0])
		return nil
	case "key-add", "key-rm":
		if err := want("user "+sub, args, 2, "a user id and a public key (file or key line)"); err != nil {
			return err
		}
		c, err := connect(g, false)
		if err != nil {
			return err
		}
		defer c.close()
		method := map[string]string{"key-add": "POST", "key-rm": "DELETE"}[sub]
		var out struct {
			Keys int `json:"keys"`
		}
		if err := c.do(method, "/api/v1/admin/users/"+url.PathEscape(args[0])+"/keys", map[string]string{"key": keyArg(args[1])}, &out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "user %s has %d SSH keys\n", args[0], out.Keys)
		return nil
	}
	return fmt.Errorf("gtd user: unknown subcommand %q", sub)
}

func cmdSpace(g globals, sub string, args []string, stdout io.Writer) error {
	switch sub {
	case "create":
		name := ""
		args, err := flags("space create", args, func(fs *flag.FlagSet) { fs.StringVar(&name, "name", "", "") })
		if err != nil {
			return err
		}
		if len(args) == 0 {
			return errors.New("gtd space create: want a space id and optional member ids")
		}
		c, err := connect(g, false)
		if err != nil {
			return err
		}
		defer c.close()
		in := map[string]any{"id": args[0], "name": name, "members": args[1:]}
		if err := c.do("POST", "/api/v1/admin/spaces", in, nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "space %s created with %d members\n", args[0], len(args)-1)
		return nil
	case "add-member", "rm-member":
		if err := want("space "+sub, args, 2, "a space id and a user id"); err != nil {
			return err
		}
		c, err := connect(g, false)
		if err != nil {
			return err
		}
		defer c.close()
		method := map[string]string{"add-member": "PUT", "rm-member": "DELETE"}[sub]
		path := "/api/v1/admin/spaces/" + url.PathEscape(args[0]) + "/members/" + url.PathEscape(args[1])
		if err := c.do(method, path, nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "space %s: %s %s\n", args[0], map[string]string{"add-member": "added", "rm-member": "removed"}[sub], args[1])
		return nil
	}
	return fmt.Errorf("gtd space: unknown subcommand %q", sub)
}
