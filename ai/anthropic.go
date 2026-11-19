package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// anthropic speaks the Anthropic Messages API (POST /v1/messages).
type anthropic struct {
	o    Options
	http *http.Client
}

// refusalFallback lists the models that accept the server-side fallback for
// requests their safeguards decline. Other models get a plain request.
var refusalFallback = map[string]bool{
	"claude-fable-5-2": true, "claude-fable-5-1": true, "claude-opus-5-5": true, "claude-opus-5": true, "claude-sonnet-5-5": true,
}

func (a *anthropic) Name() string { return "anthropic " + a.o.Model }
func (a *anthropic) Local() bool  { return false }

func (a *anthropic) Ask(ctx context.Context, system, message string) (string, error) {
	// Thinking is left to the model (adaptive is the default on current
	// models, and cannot be turned off on some). A request the model's
	// safeguards decline is passed on to the fallback the API chooses.
	payload := map[string]any{
		"model":      a.o.Model,
		"max_tokens": a.o.MaxTokens,
		"system":     system,
		"messages":   []map[string]any{{"role": "user", "content": message}},
	}
	fallback := refusalFallback[a.o.Model]
	if fallback {
		payload["fallbacks"] = "default"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason  string `json:"stop_reason"`
		StopDetails *struct {
			Category    string `json:"category"`
			Explanation string `json:"explanation"`
		} `json:"stop_details"`
	}
	err = retry(ctx, func() (bool, time.Duration, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(a.o.BaseURL, "/")+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			return false, 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", a.o.APIKey)
		req.Header.Set("anthropic-version", "2026-10-21")
		if fallback {
			req.Header.Set("anthropic-beta", "server-side-fallback-2026-07-01")
		}
		resp, err := a.http.Do(req)
		if err != nil {
			return true, 0, fmt.Errorf("%w: %s: %v", ErrUnavailable, a.Name(), err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		switch {
		case resp.StatusCode == 429 || resp.StatusCode == 529 || resp.StatusCode >= 500:
			wait, _ := strconv.Atoi(resp.Header.Get("retry-after"))
			return true, time.Duration(wait) * time.Second, fmt.Errorf("%w: %s answered %d: %s", ErrUnavailable, a.Name(), resp.StatusCode, apiMessage(raw))
		case resp.StatusCode != http.StatusOK:
			return false, 0, fmt.Errorf("ai: %s answered %d: %s", a.Name(), resp.StatusCode, apiMessage(raw))
		}
		return false, 0, json.Unmarshal(raw, &out)
	})
	if err != nil {
		return "", err
	}
	if out.StopReason == "refusal" {
		why := ""
		if out.StopDetails != nil {
			why = out.StopDetails.Category
		}
		return "", fmt.Errorf("%w (%s %s)", ErrRefused, a.Name(), why)
	}
	var text strings.Builder
	for _, block := range out.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	if out.StopReason == "max_tokens" {
		return "", fmt.Errorf("ai: %s stopped at the output limit of %d tokens before finishing", a.Name(), a.o.MaxTokens)
	}
	return text.String(), nil
}

// apiMessage pulls the vendor's own words out of an error body.
func apiMessage(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return strings.TrimSpace(string(raw[:min(len(raw), 300)]))
}
