package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh/agent"
	"gopkg.in/yaml.v3"

	"github.com/fiber/gtd/auth"
	"github.com/fiber/gtd/store"
	"github.com/fiber/gtd/web"
)

// config is <data_dir>/config.yaml (STORAGE §10). So far the
// server and snapshot sections are read. Other sections are ignored until their milestone.
type config struct {
	Server struct {
		Name    string `yaml:"name"`
		BaseURL string `yaml:"base_url"` // https://… makes the session cookie Secure
		Listen  string `yaml:"listen"`   // HTTP address, default 127.0.0.1:8640; "off" disables it
	} `yaml:"server"`
	// Embed selects the embedding adapter (D-24): "internal" runs
	// EmbeddingGemma in-process from model_dir, "ollama" asks the server at url.
	// Empty means no embeddings: everything works, similarity is off.
	Embed struct {
		Provider string `yaml:"provider"`
		ModelDir string `yaml:"model_dir"`
		URL      string `yaml:"url"`
		Model    string `yaml:"model"`
	} `yaml:"embed"`
	Similarity struct {
		DupThreshold     float32 `yaml:"dup_threshold"`
		SuggestThreshold float32 `yaml:"suggest_threshold"`
		SearchThreshold  float32 `yaml:"search_threshold"`
	} `yaml:"similarity"`
	Snapshots struct {
		Schedule string     `yaml:"schedule"` // "", hourly or daily
		Keep     store.Keep `yaml:"keep"`
	} `yaml:"snapshots"`
}

func loadConfig(dir string) (config, error) {
	var c config
	c.Server.Name, c.Server.Listen = "gtd", "127.0.0.1:8640"
	path := filepath.Join(dir, "config.yaml")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("%s: %v", path, err)
	}
	switch c.Snapshots.Schedule {
	case "", "hourly", "daily":
	default:
		return c, fmt.Errorf("%s: snapshots.schedule is %q, want hourly or daily", path, c.Snapshots.Schedule)
	}
	return c, nil
}

func socketPath(dir string) string { return filepath.Join(dir, "gtd.sock") }

// client sends API requests to the daemon over its Unix socket or, if no
// daemon runs, to the same handlers in this process on a directly opened
// database.
type client struct {
	http  *http.Client
	token string
	close func()
}

// connect tries the daemon first and falls back to direct access. With create
// set, direct access may initialize a new database.
func connect(g globals, create bool) (*client, error) {
	sock := socketPath(g.data)
	if conn, err := net.Dial("unix", sock); err == nil {
		conn.Close()
		c := &client{close: func() {}, http: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			}}}}
		if err := c.login(g); err != nil {
			return nil, err
		}
		return c, nil
	}
	st, err := store.Open(g.data)
	if errors.Is(err, store.ErrNoDB) && create {
		var cfg config
		if cfg, err = loadConfig(g.data); err == nil {
			st, err = store.Create(g.data, cfg.Server.Name)
		}
	}
	switch {
	case errors.Is(err, store.ErrLocked):
		return nil, fmt.Errorf("%w, but nothing accepts connections on %s", err, sock)
	case errors.Is(err, store.ErrNoDB):
		return nil, fmt.Errorf("%w: create one with `gtd user add --admin --ssh-key <pubkey> <id>`", err)
	case err != nil:
		return nil, err
	}
	return &client{close: func() { st.Close() }, http: &http.Client{Transport: web.New(st).Local(g.user)}}, nil
}

// login runs the SSH-key challenge against the daemon with keys from ssh-agent.
func (c *client) login(g globals) error {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return errors.New("the daemon asks for SSH-key authentication, but SSH_AUTH_SOCK is not set (no ssh-agent)")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return fmt.Errorf("ssh-agent at %s: %v", sock, err)
	}
	defer conn.Close()
	var ch struct {
		Nonce    string `json:"nonce"`
		ServerID string `json:"server_id"`
	}
	if err := c.do("POST", "/api/v1/auth/ssh/challenge", map[string]string{"user": g.user}, &ch); err != nil {
		return err
	}
	sigs, err := auth.Sign(agent.NewClient(conn), ch.ServerID, ch.Nonce, g.key)
	if err != nil {
		return err
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := c.do("POST", "/api/v1/auth/ssh/verify", map[string]any{"nonce": ch.Nonce, "signatures": sigs}, &out); err != nil {
		return fmt.Errorf("user %q: %w (is one of your ssh-agent keys registered with `gtd user key-add`?)", g.user, err)
	}
	c.token = out.Token
	return nil
}

// send performs a request and returns the response of a 2xx status. Any other
// status becomes the error the server reported.
func (c *client) send(method, path, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, "http://gtd"+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		var e struct {
			Error string `json:"error"`
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if json.Unmarshal(data, &e) != nil || e.Error == "" {
			e.Error = fmt.Sprintf("%s %s: status %d: %s", method, path, resp.StatusCode, bytes.TrimSpace(data))
		}
		return nil, errors.New(e.Error)
	}
	return resp, nil
}

// do sends in as JSON and decodes the JSON answer into out. Both may be nil.
func (c *client) do(method, path string, in, out any) error {
	var body io.Reader
	contentType := ""
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, contentType = bytes.NewReader(data), "application/json"
	}
	resp, err := c.send(method, path, contentType, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
