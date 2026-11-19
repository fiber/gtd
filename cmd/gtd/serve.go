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
	"syscall"
	"time"

	"github.com/fiber/gtd/store"
	"github.com/fiber/gtd/web"
)

// serve runs the daemon: it holds the database lock and serves the API on the
// Unix socket <data_dir>/gtd.sock (mode 0660). HTTP over TCP arrives with M1.
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

	srv := &http.Server{Handler: web.New(st).Handler()}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	fmt.Fprintf(stdout, "gtd: server %q (%s) listening on %s\n", st.Server().Name, st.Server().ID, sock)
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
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
