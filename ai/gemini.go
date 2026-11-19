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

// gemini speaks Google's generateContent endpoint. The key travels in the
// x-goog-api-key header, never in the URL.
type gemini struct {
	o    Options
	http *http.Client
}

func (g *gemini) Name() string { return "gemini " + g.o.Model }
func (g *gemini) Local() bool  { return false }

type geminiPart struct {
	Text    string `json:"text,omitempty"`
	Thought bool   `json:"thought,omitempty"` // the model's scratchpad, never the answer
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

func (g *gemini) Ask(ctx context.Context, system, message string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"systemInstruction": geminiContent{Parts: []geminiPart{{Text: system}}},
		"contents":          []geminiContent{{Role: "user", Parts: []geminiPart{{Text: message}}}},
		"generationConfig":  map[string]any{"maxOutputTokens": g.o.MaxTokens},
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Candidates []struct {
			Content      geminiContent `json:"content"`
			FinishReason string        `json:"finishReason"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
	}
	url := strings.TrimRight(g.o.BaseURL, "/") + "/v1beta/models/" + g.o.Model + ":generateContent"
	err = retry(ctx, func() (bool, time.Duration, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if err != nil {
			return false, 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-goog-api-key", g.o.APIKey)
		resp, err := g.http.Do(req)
		if err != nil {
			return true, 0, fmt.Errorf("%w: %s: %v", ErrUnavailable, g.Name(), err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		switch {
		case resp.StatusCode == 429 || resp.StatusCode == 503:
			wait, _ := strconv.Atoi(resp.Header.Get("retry-after"))
			return true, time.Duration(wait) * time.Second, fmt.Errorf("%w: %s answered %d: %s", ErrUnavailable, g.Name(), resp.StatusCode, apiMessage(raw))
		case resp.StatusCode != http.StatusOK:
			return false, 0, fmt.Errorf("ai: %s answered %d: %s", g.Name(), resp.StatusCode, apiMessage(raw))
		}
		return false, 0, json.Unmarshal(raw, &out)
	})
	if err != nil {
		return "", err
	}
	if out.PromptFeedback.BlockReason != "" || len(out.Candidates) == 0 {
		return "", fmt.Errorf("%w (%s %s)", ErrRefused, g.Name(), out.PromptFeedback.BlockReason)
	}
	c := out.Candidates[0]
	var text strings.Builder
	for _, p := range c.Content.Parts {
		if !p.Thought {
			text.WriteString(p.Text)
		}
	}
	switch c.FinishReason {
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT", "BLOCKLIST":
		return "", fmt.Errorf("%w (%s %s)", ErrRefused, g.Name(), c.FinishReason)
	case "MAX_TOKENS":
		return "", fmt.Errorf("ai: %s stopped at the output limit of %d tokens before finishing", g.Name(), g.o.MaxTokens)
	}
	return text.String(), nil
}
