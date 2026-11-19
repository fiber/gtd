package embed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/fiber/ai/models/gemma"
)

// Internal runs EmbeddingGemma in this process through github.com/fiber/ai,
// on the CPU, from a directory of model files. No server, no port.
type Internal struct {
	mu    sync.Mutex // one forward pass at a time
	model *gemma.Model
	id    string
}

// NewInternal loads the model from dir (config.json, tokenizer.json,
// model.safetensors, the pooling and dense configs).
func NewInternal(dir string) (*Internal, error) {
	id, err := modelIdentity(dir)
	if err != nil {
		return nil, fmt.Errorf("embedding model in %s: %w", dir, err)
	}
	m, err := gemma.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("embedding model in %s: %w", dir, err)
	}
	return &Internal{model: m, id: "internal:" + id + ":" + promptSet}, nil
}

// modelIdentity hashes the configuration and the first megabyte and the size
// of the weights: enough to tell two models apart without reading 1.2 GB.
func modelIdentity(dir string) (string, error) {
	h := sha256.New()
	cfg, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return "", err
	}
	h.Write(cfg)
	f, err := os.Open(filepath.Join(dir, "model.safetensors"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	fmt.Fprintf(h, "|%d|", fi.Size())
	if _, err := io.CopyN(h, f, 1<<20); err != nil && err != io.EOF {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

func (e *Internal) Embed(ctx context.Context, texts []string, task Task) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return e.model.Embed(texts, gemma.Prompt(string(task)))
}

func (e *Internal) Identity() string { return e.id }

func (e *Internal) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.model.Close()
	return nil
}
