package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fiber/gtd/embed"
	"github.com/fiber/gtd/store"
	"github.com/fiber/gtd/web"
)

// serve runs the daemon: it holds the database lock and serves the API on the
// Unix socket <data_dir>/gtd.sock (mode 0660) for the CLI, and the API plus
// the web UI over HTTP on server.listen (behind nginx for TLS).
func serve(g globals, stdout, stderr io.Writer) error {
	cfg, err := loadConfig(g.data)
	if err != nil {
		return err
	}
	st, err := store.Open(g.data)
	if errors.Is(err, store.ErrNoDB) {
		return fmt.Errorf("%w: create one with `gtd user add --admin --ssh-key <pubkey> <id>`", err)
	}
	if err != nil {
		return err
	}
	defer st.Close()

	// We hold the database lock, so a socket file left here belongs to a dead daemon.
	sock := socketPath(g.data)
	if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(sock) > 100 {
		return fmt.Errorf("socket path %s is %d bytes long, Unix sockets allow about 100: use a shorter data directory", sock, len(sock))
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", sock, err)
	}
	defer os.Remove(sock)
	if err := os.Chmod(sock, 0o660); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if interval := scheduleInterval(cfg.Snapshots.Schedule); interval > 0 {
		go func() {
			tick := time.NewTicker(time.Minute)
			defer tick.Stop()
			for {
				if name, pruned, err := scheduledSnapshot(st, interval, cfg.Snapshots.Keep); err != nil {
					fmt.Fprintf(stderr, "gtd: scheduled snapshot: %v\n", err)
				} else if name != "" {
					fmt.Fprintf(stdout, "snapshot %s created, %d pruned\n", name, len(pruned))
				}
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
				}
			}
		}()
	}

	// The tickler (SPEC §5.6): at start and whenever the local date changes.
	go func() {
		day := ""
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			if today := st.Now().Format(time.DateOnly); today != day {
				day = today
				if n, err := st.Tickle(today); err != nil {
					fmt.Fprintf(stderr, "gtd: tickler: %v\n", err)
				} else if n > 0 {
					fmt.Fprintf(stdout, "gtd: tickler: %d items resurfaced\n", n)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()

	app := web.New(st)
	// Embeddings are optional. A model that does not load is reported and the
	// daemon runs without similarity.
	var emb embed.Embedder
	switch cfg.Embed.Provider {
	case "internal":
		if emb, err = embed.NewInternal(cfg.Embed.ModelDir); err != nil {
			fmt.Fprintf(stderr, "gtd: embeddings are off: %v\n", err)
			emb = nil
		}
	case "ollama":
		url := cfg.Embed.URL
		if url == "" {
			url = "http://127.0.0.1:11434"
		}
		emb = embed.NewOllama(url, cfg.Embed.Model)
	case "":
	default:
		// Only the daemon cares: other commands must keep working with a
		// config written for another version.
		fmt.Fprintf(stderr, "gtd: embeddings are off: embed.provider is %q, want internal or ollama\n", cfg.Embed.Provider)
	}
	if emb != nil {
		defer emb.Close()
		svc := embed.New(st, emb)
		if cfg.Similarity.DupThreshold > 0 {
			svc.DupThreshold = cfg.Similarity.DupThreshold
		}
		if cfg.Similarity.SuggestThreshold > 0 {
			svc.SuggestThreshold = cfg.Similarity.SuggestThreshold
		}
		if cfg.Similarity.SearchThreshold > 0 {
			svc.SearchThreshold = cfg.Similarity.SearchThreshold
		}
		svc.Start(ctx)
		app.Embed = svc
		fmt.Fprintf(stdout, "gtd: embeddings: %s\n", cfg.Embed.Provider)
	}
	app.SecureCookies = strings.HasPrefix(cfg.Server.BaseURL, "https://")
	// The CLI talks to the bare API on the socket; browsers get everything
	// under web.Prefix over HTTP.
	sockSrv := &http.Server{Handler: app.API(), ReadHeaderTimeout: 10 * time.Second}
	srv := &http.Server{Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sockSrv.Shutdown(shut)
		srv.Shutdown(shut)
	}()
	fmt.Fprintf(stdout, "gtd: server %q (%s) listening on %s\n", st.Server().Name, st.Server().ID, sock)
	errs := make(chan error, 2)
	go func() { errs <- sockSrv.Serve(ln) }()
	if cfg.Server.Listen != "off" {
		tcp, err := net.Listen("tcp", cfg.Server.Listen)
		if err != nil {
			return fmt.Errorf("listen on %s: %w", cfg.Server.Listen, err)
		}
		fmt.Fprintf(stdout, "gtd: web UI on http://%s%s/\n", tcp.Addr(), web.Prefix)
		go func() { errs <- srv.Serve(tcp) }()
	}
	if err := <-errs; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func scheduleInterval(schedule string) time.Duration {
	switch schedule {
	case "hourly":
		return time.Hour
	case "daily":
		return 24 * time.Hour
	}
	return 0
}

// scheduledSnapshot creates a snapshot if the newest one is at least interval
// old (or there is none), then applies the retention. It returns the name of
// the snapshot it created, or "".
func scheduledSnapshot(st *store.Store, interval time.Duration, keep store.Keep) (name string, pruned []string, err error) {
	list, err := store.ListSnapshots(st.Dir())
	if err != nil {
		return "", nil, err
	}
	if n := len(list); n > 0 && st.Now().Sub(list[n-1].Created) < interval {
		return "", nil, nil
	}
	man, err := st.Snapshot("")
	if err != nil {
		return "", nil, err
	}
	pruned, err = store.PruneSnapshots(st.Dir(), keep)
	return man.Name, pruned, err
}
