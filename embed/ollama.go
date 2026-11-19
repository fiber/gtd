package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Ollama embeds through an Ollama server's /api/embed. Ollama does not add
// EmbeddingGemma's task prompts (measured: DECISIONS, 2026-11-19), so this
// adapter does.
type Ollama struct {
	url   string
	model string
	http  *http.Client

	mu sync.Mutex
	id string // set once the server has told us the model's digest
}

func NewOllama(url, model string) *Ollama {
	if model == "" {
		model = "embeddinggemma"
	}
	return &Ollama{url: strings.TrimRight(url, "/"), model: model, http: &http.Client{}}
}

func (o *Ollama) post(ctx context.Context, path string, in, out any) error {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, "POST", o.url+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := o.http.Do(req)
	if err != nil {
		return fmt.Errorf("ollama at %s: %w", o.url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 500))
		return fmt.Errorf("ollama at %s%s: status %d: %s", o.url, path, res.StatusCode, bytes.TrimSpace(msg))
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func (o *Ollama) Embed(ctx context.Context, texts []string, task Task) ([][]float32, error) {
	wrapped := make([]string, len(texts))
	for i, t := range texts {
		wrapped[i] = withPrompt(task, t)
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := o.post(ctx, "/api/embed", map[string]any{"model": o.model, "input": wrapped}, &out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama returned %d vectors for %d texts", len(out.Embeddings), len(texts))
	}
	return out.Embeddings, nil
}

// Identity includes the digest of the model the server has loaded under the
// name, so that a re-pulled or re-quantized model counts as another one. Until
// the server has answered once, the identity is empty and nothing is stored.
func (o *Ollama) Identity() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.id
}

// Connect asks the server for the model's digest. The service calls it before
// it embeds anything, and again after errors.
func (o *Ollama) Connect(ctx context.Context) error {
	var out struct {
		Details struct {
			QuantizationLevel string `json:"quantization_level"`
		} `json:"details"`
		ModifiedAt string `json:"modified_at"`
		Digest     string `json:"digest"`
	}
	if err := o.post(ctx, "/api/show", map[string]string{"model": o.model}, &out); err != nil {
		return err
	}
	digest := out.Digest
	if digest == "" { // older servers: no digest in /api/show
		digest = out.ModifiedAt
	}
	o.mu.Lock()
	o.id = "ollama:" + o.model + ":" + out.Details.QuantizationLevel + ":" + digest + ":" + promptSet
	o.mu.Unlock()
	return nil
}

func (o *Ollama) Close() error { return nil }
