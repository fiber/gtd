package ai

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed prompts/tasklines.md
var taskLinesPrompt string

// TaskLine is a proposed task line for a note: who is to do what, and the
// passage of the note it was taken from.
type TaskLine struct {
	Who   string `json:"who"`   // "me", a name, or "" when the note does not say
	Text  string `json:"text"`  // the task
	Quote string `json:"quote"` // found in the note
}

// maxPart is how much of a note goes into one request, in characters. A
// longer note is asked about in parts, cut between paragraphs.
const maxPart = 24000

// SuggestTaskLines asks the provider for the commitments in a note. names
// are how the reader appears in notes ("Sven", "SE"): commitments of theirs
// come back with Who "me".
//
// The answer is checked before it is returned: an entry without a task, or
// whose quote is not in the note, is dropped. A model cannot add a task the
// note does not contain a source for.
func SuggestTaskLines(ctx context.Context, p Provider, title, body string, names []string) ([]TaskLine, error) {
	reader := "The reader is not named in the note."
	if len(names) > 0 {
		reader = "The reader appears in notes as: " + strings.Join(names, ", ") + "."
	}
	var out []TaskLine
	seen := map[string]bool{}
	for _, part := range parts(body) {
		msg := fmt.Sprintf("The reader\n%s\n\nTitle of the note: %s\n\nThe note:\n\n%s", reader, title, part)
		answer, err := p.Ask(ctx, taskLinesPrompt, msg)
		if err != nil {
			return nil, err
		}
		lines, err := parseTaskLines(answer)
		if err != nil {
			return nil, fmt.Errorf("ai: %s did not answer in the asked form: %v", p.Name(), err)
		}
		flat := squash(body)
		for _, l := range lines {
			l.Who, l.Text, l.Quote = strings.TrimSpace(l.Who), strings.TrimSpace(l.Text), strings.TrimSpace(l.Quote)
			key := strings.ToLower(l.Who + "\x00" + l.Text)
			if l.Text == "" || l.Quote == "" || seen[key] || !strings.Contains(flat, squash(l.Quote)) {
				continue
			}
			if strings.EqualFold(l.Who, "me") {
				l.Who = "me"
			}
			seen[key] = true
			out = append(out, l)
		}
	}
	return out, nil
}

// parseTaskLines reads the JSON array out of an answer. A code fence around
// it, which models add despite being told not to, is tolerated.
func parseTaskLines(answer string) ([]TaskLine, error) {
	s := strings.TrimSpace(answer)
	if i, j := strings.Index(s, "["), strings.LastIndex(s, "]"); i >= 0 && j > i {
		s = s[i : j+1]
	}
	var lines []TaskLine
	if err := json.Unmarshal([]byte(s), &lines); err != nil {
		return nil, err
	}
	return lines, nil
}

// squash makes two texts comparable despite line breaks and runs of spaces.
func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

// parts cuts a long text between paragraphs into pieces of at most maxPart.
func parts(text string) []string {
	if len(text) <= maxPart {
		return []string{text}
	}
	var out []string
	cur := ""
	for _, para := range strings.Split(text, "\n\n") {
		for len(para) > maxPart { // one enormous paragraph: cut it at a line or a space
			cut := strings.LastIndexAny(para[:maxPart], "\n ")
			if cut <= 0 {
				cut = maxPart
			}
			if cur != "" {
				out, cur = append(out, cur), ""
			}
			out, para = append(out, para[:cut]), para[cut:]
		}
		if cur != "" && len(cur)+2+len(para) > maxPart {
			out, cur = append(out, cur), ""
		}
		if cur != "" {
			cur += "\n\n"
		}
		cur += para
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// Format writes accepted proposals as Markdown task lines: "- [ ] Me: …",
// "- [ ] Dana: …", "- [ ] Open: …". me and open are the words for the reader
// and for "nobody yet", in the user's language.
func Format(lines []TaskLine, me, open string) string {
	var b strings.Builder
	for _, l := range lines {
		who := l.Who
		switch who {
		case "me":
			who = me
		case "":
			who = open
		}
		fmt.Fprintf(&b, "- [ ] %s: %s\n", who, strings.ReplaceAll(l.Text, "\n", " "))
	}
	return b.String()
}
