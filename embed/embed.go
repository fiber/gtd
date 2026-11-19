// Package embed turns items into vectors and answers similarity questions
// (AI_CONTRACT §2). Everything here is optional at runtime: with no embedder,
// or a failing one, the rest of gtd works and says that similarity is off.
package embed

import (
	"context"
	"crypto/sha256"
	"math"
	"regexp"
	"strings"

	"github.com/fiber/gtd/core"
)

// Task says what a text is embedded for. EmbeddingGemma was trained with a
// different prompt for each, and retrieval quality depends on using them.
type Task string

const (
	Document Task = "document" // a stored item
	Query    Task = "query"    // a search
)

// promptSet names the prompts in use. It is part of a vector's identity:
// vectors made with other prompts are other vectors.
const promptSet = "gemma-retrieval-1"

// Embedder is the one interface of this package. It has three planned
// adapters (D-24): internal, ollama, and a third (deferred).
type Embedder interface {
	// Embed returns one unit-length vector per text, in order.
	Embed(ctx context.Context, texts []string, task Task) ([][]float32, error)
	// Identity names the adapter, the model and the prompt set. Vectors of
	// different identities are never compared.
	Identity() string
	Close() error
}

// withPrompt adds EmbeddingGemma's task prompt, for adapters whose backend
// does not add it.
func withPrompt(task Task, text string) string {
	if task == Query {
		return "task: search result | query: " + text
	}
	return "title: none | text: " + text
}

const (
	chunkSize    = 1500 // characters per passage
	chunkOverlap = 200
	maxChunks    = 16 // per item: 24,000 characters, the rest is not embedded
)

var (
	reAttToken  = regexp.MustCompile(`!?\[\[att:([0-9a-f]{64})\]\]`)
	reLinkToken = regexp.MustCompile(`\[\[([0-9A-Za-z_-]{1,40})(?:\|([^\]\n]*))?\]\]`)
	reUserToken = regexp.MustCompile(`@\[user:([a-z0-9-]{1,64})\]`)
	reMarkup    = regexp.MustCompile("(?m)^[ \\t]{0,3}(#{1,6}|>|[-*+]|\\d+[.)])[ \\t]+|[*_~`]{1,3}|\\[[ xX]\\][ \\t]")
)

// Resolver replaces tokens by what a reader would see.
type Resolver struct {
	Title    func(id string) string // title of a linked item in the same space, or ""
	UserName func(id string) string
	PDFText  func(sha string) string
}

// Text is the input text of an item (AI_CONTRACT §2.2): title and body with
// Markdown markup removed and tokens replaced by titles and names, then the
// template field values, import hints and the text of attached PDFs.
func Text(it *core.Item, r Resolver) string {
	body := reAttToken.ReplaceAllStringFunc(it.Body, func(tok string) string {
		sha := reAttToken.FindStringSubmatch(tok)[1]
		for _, a := range it.Attachments {
			if a.SHA256 == sha {
				return a.Name
			}
		}
		return ""
	})
	body = reLinkToken.ReplaceAllStringFunc(body, func(tok string) string {
		m := reLinkToken.FindStringSubmatch(tok)
		if m[2] != "" {
			return m[2]
		}
		return r.Title(m[1])
	})
	body = reUserToken.ReplaceAllStringFunc(body, func(tok string) string {
		return r.UserName(reUserToken.FindStringSubmatch(tok)[1])
	})
	body = reMarkup.ReplaceAllString(body, "")
	var b strings.Builder
	b.WriteString(it.Title)
	if strings.TrimSpace(body) != "" {
		b.WriteString("\n\n")
		b.WriteString(strings.TrimSpace(body))
	}
	for _, k := range sortedKeys(it.Fields) {
		if it.Fields[k] != "" {
			b.WriteString("\n" + k + ": " + it.Fields[k])
		}
	}
	for _, k := range []string{"folder", "tags"} {
		if v, ok := it.Imported[k]; ok {
			b.WriteString("\n" + k + ": " + strings.Trim(strings.NewReplacer("[", "", "]", "").Replace(toString(v)), " "))
		}
	}
	for _, a := range it.Attachments {
		if t := r.PDFText(a.SHA256); t != "" {
			b.WriteString("\n\n" + t)
		}
	}
	return b.String()
}

// Chunks splits a text into passages of at most chunkSize characters. It
// cuts between paragraphs (blank lines), and keeps a heading with the
// paragraph that follows it. Only a paragraph longer than chunkSize is
// cut inside, into overlapping pieces. At most maxChunks are returned.
func Chunks(text string) []string {
	if len([]rune(text)) <= chunkSize {
		return []string{text}
	}
	var out []string
	cur, size := "", 0
	flush := func() {
		if cur != "" {
			out = append(out, cur)
		}
		cur, size = "", 0
	}
	for _, para := range strings.Split(text, "\n\n") {
		if strings.TrimSpace(para) == "" {
			continue
		}
		runes := []rune(para)
		if size > 0 && size+2+len(runes) > chunkSize {
			// A heading goes with its section: a short single line at the
			// end of the passage moves to the next one.
			head := ""
			if i := strings.LastIndex(cur, "\n\n"); i >= 0 && isHeading(cur[i+2:]) && len([]rune(cur[i+2:]))+2+len(runes) <= chunkSize {
				cur, head = cur[:i], cur[i+2:]
			}
			flush()
			cur, size = head, len([]rune(head))
		}
		if len(runes) <= chunkSize {
			if cur != "" {
				cur += "\n\n"
				size += 2
			}
			cur += para
			size += len(runes)
			continue
		}
		flush()
		for start := 0; start < len(runes); start += chunkSize - chunkOverlap {
			end := min(start+chunkSize, len(runes))
			out = append(out, string(runes[start:end]))
			if end == len(runes) {
				break
			}
		}
	}
	flush()
	if len(out) > maxChunks {
		out = out[:maxChunks]
	}
	return out
}

// isHeading guesses: Text has removed the Markdown markers, so a heading is a
// short single line.
func isHeading(para string) bool {
	return !strings.Contains(para, "\n") && len([]rune(para)) <= 80
}

// Hash identifies the text a vector was made from.
func Hash(text string) [32]byte { return sha256.Sum256([]byte(text)) }

// Mean returns the unit-length mean of vectors: an item's vector from its
// chunks, a project's vector from its members.
func Mean(vecs [][]float32) []float32 {
	if len(vecs) == 0 {
		return nil
	}
	out := make([]float32, len(vecs[0]))
	for _, v := range vecs {
		for i, x := range v {
			out[i] += x
		}
	}
	var norm float64
	for _, x := range out {
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return out
	}
	scale := float32(1 / math.Sqrt(norm))
	for i := range out {
		out[i] *= scale
	}
	return out
}
