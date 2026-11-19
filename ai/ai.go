// Package ai is gtd's generation client: one provider per server, chosen in
// config.yaml (`ai:`), asked only when a user presses a button. It follows
// the shape of nuro's client: plain HTTP, no SDK, the same config keys.
//
// Nothing here decides what may be sent where. The caller checks the space's
// AI policy before it asks (DECISIONS, Q-11).
package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Provider is a generation backend.
type Provider interface {
	// Ask sends one message under a system prompt and returns the text of
	// the answer.
	Ask(ctx context.Context, system, message string) (string, error)
	// Name is the provider and model, for messages and logs.
	Name() string
	// Local reports whether the model runs on the operator's own machines.
	// Text of a space whose policy is "local" goes only to such a provider.
	Local() bool
}

var (
	// ErrUnavailable: the model could not be reached, or said "not now"
	// (rate limit, overload) after the retries. Trying again later may work.
	ErrUnavailable = errors.New("ai: model unavailable")
	// ErrRefused: the model declined to answer this request.
	ErrRefused = errors.New("ai: the model declined")
)

// Options is the `ai:` section of config.yaml.
type Options struct {
	Provider  string // anthropic (default) | gemini
	APIKey    string
	Model     string // the provider's default when empty
	MaxTokens int    // output cap; 16000 when 0
	BaseURL   string // for tests; the provider's API when empty
}

// New builds the configured provider. An unknown name is an error: a typo
// must not quietly use another vendor's key.
func New(o Options) (Provider, error) {
	if strings.TrimSpace(o.APIKey) == "" {
		return nil, errors.New("ai: no api_key")
	}
	if o.MaxTokens <= 0 {
		o.MaxTokens = 16000
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	switch strings.ToLower(strings.TrimSpace(o.Provider)) {
	case "", "anthropic":
		if o.Model == "" {
			o.Model = "claude-opus-5-5"
		}
		if o.BaseURL == "" {
			o.BaseURL = "https://api.anthropic.com"
		}
		return &anthropic{o, client}, nil
	case "gemini":
		if o.Model == "" {
			o.Model = "gemini-3.8-flash"
		}
		if o.BaseURL == "" {
			o.BaseURL = "https://generativelanguage.googleapis.com"
		}
		return &gemini{o, client}, nil
	}
	return nil, fmt.Errorf("ai: unknown provider %q (want anthropic or gemini)", o.Provider)
}

// backoff is the first wait between attempts; tests shorten it.
var backoff = 2 * time.Second

// retry runs do up to four times, waiting between attempts while it says
// the failure is one to wait out.
func retry(ctx context.Context, do func() (retryable bool, wait time.Duration, err error)) error {
	for attempt := 0; ; attempt++ {
		again, wait, err := do()
		if err == nil || !again || attempt == 3 {
			return err
		}
		if wait <= 0 || wait > 30*time.Second {
			wait = time.Duration(attempt+1) * backoff
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}
