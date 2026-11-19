package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func init() { backoff = time.Millisecond }

func TestNew(t *testing.T) {
	if _, err := New(Options{Provider: "anthropic"}); err == nil {
		t.Error("a provider without a key was accepted")
	}
	if _, err := New(Options{Provider: "openai", APIKey: "k"}); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("unknown provider: %v", err)
	}
	for provider, want := range map[string]string{"": "anthropic claude-opus-5-5", "Anthropic": "anthropic claude-opus-5-5", "gemini": "gemini gemini-3.8-flash"} {
		p, err := New(Options{Provider: provider, APIKey: "k"})
		if err != nil || p.Name() != want || p.Local() {
			t.Errorf("provider %q: %v %v", provider, p, err)
		}
	}
	if p, _ := New(Options{Provider: "gemini", APIKey: "k", Model: "other"}); p.Name() != "gemini other" {
		t.Errorf("model override: %s", p.Name())
	}
}

func TestAnthropic(t *testing.T) {
	var calls atomic.Int32
	var seen map[string]any
	var header http.Header
	reply := `{"content":[{"type":"thinking","thinking":""},{"type":"text","text":"Hello "},{"type":"text","text":"there"}],"stop_reason":"end_turn"}`
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		header = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		seen = nil
		json.Unmarshal(body, &seen)
		if r.URL.Path != "/v1/messages" || r.Method != "POST" {
			t.Errorf("request to %s %s", r.Method, r.URL.Path)
		}
		if status == 529 && n < 2 { // overloaded once, then fine
			w.Header().Set("retry-after", "0")
			w.WriteHeader(529)
			io.WriteString(w, `{"error":{"message":"overloaded"}}`)
			return
		}
		if status != 200 && status != 529 {
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"message":"credit balance is too low"}}`)
			return
		}
		io.WriteString(w, reply)
	}))
	defer srv.Close()
	p, _ := New(Options{APIKey: "secret", BaseURL: srv.URL})
	got, err := p.Ask(context.Background(), "be brief", "hi")
	if err != nil || got != "Hello there" {
		t.Fatalf("ask: %q %v", got, err)
	}
	if header.Get("x-api-key") != "secret" || header.Get("anthropic-version") == "" || header.Get("anthropic-beta") != "server-side-fallback-2026-07-01" {
		t.Errorf("headers: %v", header)
	}
	if seen["model"] != "claude-opus-5-5" || seen["system"] != "be brief" || seen["fallbacks"] != "default" || seen["max_tokens"] != float64(16000) {
		t.Errorf("body: %v", seen)
	}
	// Nothing that current models reject: no thinking budget, no sampling settings.
	for _, k := range []string{"thinking", "temperature", "top_p", "top_k"} {
		if _, ok := seen[k]; ok {
			t.Errorf("the request sets %q", k)
		}
	}
	// A model without the refusal fallback gets a plain request.
	old, _ := New(Options{APIKey: "secret", BaseURL: srv.URL, Model: "claude-sonnet-4-6"})
	if _, err := old.Ask(context.Background(), "s", "m"); err != nil || seen["fallbacks"] != nil || header.Get("anthropic-beta") != "" {
		t.Errorf("older model: %v, fallbacks %v, beta %q", err, seen["fallbacks"], header.Get("anthropic-beta"))
	}
	// Overloaded once: retried.
	calls.Store(0)
	status = 529
	if got, err := p.Ask(context.Background(), "s", "m"); err != nil || got != "Hello there" || calls.Load() != 2 {
		t.Errorf("after one 529: %q %v, %d calls", got, err, calls.Load())
	}
	// A request error is not retried and names what the vendor said.
	calls.Store(0)
	status = 400
	if _, err := p.Ask(context.Background(), "s", "m"); err == nil || !strings.Contains(err.Error(), "credit balance") || errors.Is(err, ErrUnavailable) || calls.Load() != 1 {
		t.Errorf("a 400: %v, %d calls", err, calls.Load())
	}
	// A refusal and a cut-off answer are errors, not answers.
	status = 200
	reply = `{"content":[],"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber"}}`
	if _, err := p.Ask(context.Background(), "s", "m"); !errors.Is(err, ErrRefused) {
		t.Errorf("refusal: %v", err)
	}
	reply = `{"content":[{"type":"text","text":"half an ans"}],"stop_reason":"max_tokens"}`
	if _, err := p.Ask(context.Background(), "s", "m"); err == nil || !strings.Contains(err.Error(), "output limit") {
		t.Errorf("cut off: %v", err)
	}
	// The key is in no error message.
	srv.Close()
	if _, err := p.Ask(context.Background(), "s", "m"); !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "secret") {
		t.Errorf("server gone: %v", err)
	}
}

func TestGemini(t *testing.T) {
	var seen map[string]any
	var header http.Header
	var path string
	reply := `{"candidates":[{"content":{"parts":[{"text":"scratch","thought":true},{"text":"Hallo"}]},"finishReason":"STOP"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header, path = r.Header.Clone(), r.URL.String()
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &seen)
		io.WriteString(w, reply)
	}))
	defer srv.Close()
	p, _ := New(Options{Provider: "gemini", APIKey: "secret", BaseURL: srv.URL})
	got, err := p.Ask(context.Background(), "be brief", "hi")
	if err != nil || got != "Hallo" {
		t.Fatalf("ask: %q %v", got, err)
	}
	if path != "/v1beta/models/gemini-3.8-flash:generateContent" || header.Get("x-goog-api-key") != "secret" || strings.Contains(path, "secret") {
		t.Errorf("request: %s %v", path, header)
	}
	sys, _ := json.Marshal(seen["systemInstruction"])
	msg, _ := json.Marshal(seen["contents"])
	if !strings.Contains(string(sys), "be brief") || !strings.Contains(string(msg), `"role":"user"`) || !strings.Contains(string(msg), "hi") {
		t.Errorf("body: %s %s", sys, msg)
	}
	reply = `{"promptFeedback":{"blockReason":"SAFETY"}}`
	if _, err := p.Ask(context.Background(), "s", "m"); !errors.Is(err, ErrRefused) {
		t.Errorf("blocked prompt: %v", err)
	}
	reply = `{"candidates":[{"content":{"parts":[{"text":"half"}]},"finishReason":"MAX_TOKENS"}]}`
	if _, err := p.Ask(context.Background(), "s", "m"); err == nil || !strings.Contains(err.Error(), "output limit") {
		t.Errorf("cut off: %v", err)
	}
}

// fake answers with a fixed text and remembers what it was asked.
type fake struct {
	answers []string
	asked   []string
	err     error
}

func (f *fake) Name() string { return "fake" }
func (f *fake) Local() bool  { return true }
func (f *fake) Ask(_ context.Context, system, message string) (string, error) {
	f.asked = append(f.asked, system+"\n---\n"+message)
	if f.err != nil {
		return "", f.err
	}
	a := f.answers[min(len(f.asked)-1, len(f.answers)-1)]
	return a, nil
}

func TestSuggestTaskLines(t *testing.T) {
	note := "Call with the plumber and Dana, 19 Nov.\n\nThe pipe has to be replaced, two days of work.\nSven sends the photos of the kitchen\nby Friday. Dana gets a second quote.\n\nSomeone should check the insurance.\n- [ ] order the tiles\n"
	f := &fake{answers: []string{"```json\n" + `[
	 {"who":"Me","text":"Send the photos of the kitchen by Friday","quote":"Sven sends the photos of the kitchen by Friday."},
	 {"who":"Dana","text":"Get a second quote","quote":"Dana gets a second quote."},
	 {"who":"","text":"Check the insurance","quote":"Someone should check the insurance."},
	 {"who":"Lee","text":"Invented task","quote":"Lee will paint the walls."},
	 {"who":"Dana","text":"","quote":"Dana gets a second quote."},
	 {"who":"Dana","text":"Get a second quote","quote":"Dana gets a second quote."}
	]` + "\n```"}}
	lines, err := SuggestTaskLines(context.Background(), f, "Call with the plumber", note, []string{"Sven", "SE"})
	if err != nil {
		t.Fatal(err)
	}
	// The invented one (its quote is not in the note), the empty one and the
	// repeated one are gone. A quote across a line break still counts.
	if len(lines) != 3 || lines[0] != (TaskLine{"me", "Send the photos of the kitchen by Friday", "Sven sends the photos of the kitchen by Friday."}) ||
		lines[1].Who != "Dana" || lines[2].Who != "" {
		t.Fatalf("lines: %+v", lines)
	}
	if !strings.Contains(f.asked[0], "The reader appears in notes as: Sven, SE.") || !strings.Contains(f.asked[0], note) || !strings.Contains(f.asked[0], "JSON array") {
		t.Errorf("what was asked:\n%s", f.asked[0])
	}
	if got, want := Format(lines, "Me", "Open"), "- [ ] Me: Send the photos of the kitchen by Friday\n- [ ] Dana: Get a second quote\n- [ ] Open: Check the insurance\n"; got != want {
		t.Errorf("formatted:\n%s", got)
	}
	// Nothing found, garbage, and a provider error.
	if lines, err := SuggestTaskLines(context.Background(), &fake{answers: []string{"[]"}}, "t", note, nil); err != nil || len(lines) != 0 {
		t.Errorf("nothing found: %v %v", lines, err)
	}
	if _, err := SuggestTaskLines(context.Background(), &fake{answers: []string{"I could not find any."}}, "t", note, nil); err == nil {
		t.Error("an answer that is not JSON was accepted")
	}
	if _, err := SuggestTaskLines(context.Background(), &fake{err: ErrUnavailable}, "t", note, nil); !errors.Is(err, ErrUnavailable) {
		t.Errorf("provider error: %v", err)
	}
	// A long note is asked about in parts, and each part's finds are kept.
	long := strings.Repeat("Filler paragraph with nothing to do in it.\n\n", 900) + "Dana gets a second quote.\n\n" + strings.Repeat("More filler.\n\n", 900) + "Sven sends the photos."
	lf := &fake{answers: []string{`[]`, `[{"who":"Dana","text":"Get a second quote","quote":"Dana gets a second quote."}]`, `[{"who":"me","text":"Send the photos","quote":"Sven sends the photos."}]`}}
	lines, err = SuggestTaskLines(context.Background(), lf, "long", long, []string{"Sven"})
	if err != nil || len(lf.asked) < 2 || len(lines) != 2 {
		t.Errorf("long note: %d requests, %+v, %v", len(lf.asked), lines, err)
	}
	for _, a := range lf.asked {
		if len(a) > maxPart+len(taskLinesPrompt)+400 {
			t.Errorf("a request of %d characters", len(a))
		}
	}
}
