package web

import (
	"html"
	"regexp"
	"strings"
)

// A small, safe Markdown renderer (SPEC §3.1): headings, paragraphs, emphasis,
// code, block quotes, lists with task items, tables, links, and gtd's tokens
// (SPEC §3.3). It never passes HTML through: every piece of source text is
// escaped, and every tag in the output is written by this file. It is a
// subset of CommonMark, not a full implementation.

// tokens resolves gtd's text tokens for one viewer.
type tokens struct {
	// link returns the title and state of an item the viewer may see.
	link func(id string) (title, state string, ok bool)
	// att returns the name and MIME type of an attachment of the rendered item.
	att func(sha string) (name, mime string, ok bool)
	// user returns the display name of a participant.
	user        func(id string) (name string, ok bool)
	unavailable string // localized "unavailable item"
}

var (
	reHeading = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	reRule    = regexp.MustCompile(`^\s{0,3}(-{3,}|\*{3,}|_{3,})\s*$`)
	reItem    = regexp.MustCompile(`^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$`)
	reTask    = regexp.MustCompile(`^\[([ xX])\]\s+(.*)$`)
	reTblSep  = regexp.MustCompile(`^\s*\|?\s*:?-{1,}:?\s*(\|\s*:?-{1,}:?\s*)*\|?\s*$`)
	reInline  = regexp.MustCompile("`([^`\n]+)`" + // 1 code
		`|!\[\[att:([0-9a-f]{64})\]\]` + // 2 embedded image
		`|\[\[att:([0-9a-f]{64})\]\]` + // 3 file chip
		`|\[\[([0-9A-Za-z_-]{1,40})(?:\|([^\]\n]*))?\]\]` + // 4 item link, 5 label
		`|@\[user:([a-z0-9-]{1,64})\]` + // 6 mention
		`|!?\[([^\]\n]*)\]\(([^)\s]+)\)` + // 7 link text, 8 url
		`|\*\*([^*\n]+)\*\*` + // 9 strong
		`|~~([^~\n]+)~~` + // 10 strike
		`|\*([^*\s][^*\n]*)\*` + // 11 emphasis
		`|(https?://[^\s<>()"']+)`) // 12 bare URL
)

// renderMarkdown returns sanitized HTML for a Markdown body.
func renderMarkdown(src string, tk tokens) string {
	var b strings.Builder
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	renderBlocks(&b, lines, tk, 0)
	return b.String()
}

func renderBlocks(b *strings.Builder, lines []string, tk tokens, depth int) {
	for i := 0; i < len(lines); {
		line := lines[i]
		trim := strings.TrimSpace(line)
		switch {
		case trim == "":
			i++
		case strings.HasPrefix(trim, "```"):
			j := i + 1
			for j < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
				j++
			}
			b.WriteString("<pre><code>" + html.EscapeString(strings.Join(lines[i+1:j], "\n")) + "</code></pre>\n")
			i = j + 1
		case reHeading.MatchString(line):
			m := reHeading.FindStringSubmatch(line)
			h := string(rune('0' + len(m[1])))
			b.WriteString("<h" + h + ">" + inline(m[2], tk) + "</h" + h + ">\n")
			i++
		case reRule.MatchString(line):
			b.WriteString("<hr>\n")
			i++
		case strings.HasPrefix(trim, ">") && depth < 8:
			var quoted []string
			for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">") {
				quoted = append(quoted, strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(lines[i]), ">"), " "))
				i++
			}
			b.WriteString("<blockquote>\n")
			renderBlocks(b, quoted, tk, depth+1)
			b.WriteString("</blockquote>\n")
		case strings.Contains(line, "|") && i+1 < len(lines) && reTblSep.MatchString(lines[i+1]) && strings.Contains(lines[i+1], "-"):
			i = renderTable(b, lines, i, tk)
		case reItem.MatchString(line):
			i = renderList(b, lines, i, tk)
		default:
			j := i
			for j < len(lines) && strings.TrimSpace(lines[j]) != "" && (j == i || !startsBlock(lines[j])) {
				j++
			}
			b.WriteString("<p>" + inline(strings.Join(lines[i:j], "\n"), tk) + "</p>\n")
			i = j
		}
	}
}

func startsBlock(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, ">") || reHeading.MatchString(line) ||
		reRule.MatchString(line) || reItem.MatchString(line)
}

func cells(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimSuffix(strings.TrimPrefix(row, "|"), "|")
	parts := strings.Split(row, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func renderTable(b *strings.Builder, lines []string, i int, tk tokens) int {
	head := cells(lines[i])
	b.WriteString("<table>\n<thead><tr>")
	for _, c := range head {
		b.WriteString("<th>" + inline(c, tk) + "</th>")
	}
	b.WriteString("</tr></thead>\n<tbody>\n")
	i += 2
	for ; i < len(lines) && strings.Contains(lines[i], "|") && strings.TrimSpace(lines[i]) != ""; i++ {
		b.WriteString("<tr>")
		row := cells(lines[i])
		for c := range head {
			cell := ""
			if c < len(row) {
				cell = row[c]
			}
			b.WriteString("<td>" + inline(cell, tk) + "</td>")
		}
		b.WriteString("</tr>\n")
	}
	b.WriteString("</tbody>\n</table>\n")
	return i
}

func indentOf(s string) int {
	n := 0
	for _, r := range s {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

// renderList renders the list starting at lines[i] and returns the index of
// the first line after it. Items indented deeper than the list's first item
// form nested lists.
func renderList(b *strings.Builder, lines []string, i int, tk tokens) int {
	m := reItem.FindStringSubmatch(lines[i])
	base := indentOf(m[1])
	tag := "ul"
	if m[2][0] >= '0' && m[2][0] <= '9' {
		tag = "ol"
	}
	b.WriteString("<" + tag + ">\n")
	for i < len(lines) {
		m := reItem.FindStringSubmatch(lines[i])
		if m == nil || indentOf(m[1]) != base {
			break
		}
		text := m[3]
		i++
		// Continuation lines: indented text that is not a list item.
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" && indentOf(lines[i]) > base && !reItem.MatchString(lines[i]) {
			text += "\n" + strings.TrimSpace(lines[i])
			i++
		}
		b.WriteString("<li")
		if t := reTask.FindStringSubmatch(text); t != nil {
			checked := ""
			if t[1] != " " {
				checked = " checked"
			}
			b.WriteString(` class="task"><input type="checkbox" disabled` + checked + "> " + inline(t[2], tk))
		} else {
			b.WriteString(">" + inline(text, tk))
		}
		if i < len(lines) {
			if n := reItem.FindStringSubmatch(lines[i]); n != nil && indentOf(n[1]) > base {
				b.WriteString("\n")
				i = renderList(b, lines, i, tk)
			}
		}
		b.WriteString("</li>\n")
	}
	b.WriteString("</" + tag + ">\n")
	return i
}

// safeURL returns the URL if it is one a link may point to.
func safeURL(u string) (string, bool) {
	l := strings.ToLower(u)
	for _, p := range []string{"http://", "https://", "mailto:", "/", "#"} {
		if strings.HasPrefix(l, p) && !strings.HasPrefix(l, "//") {
			return u, true
		}
	}
	return "", false
}

// inline renders the inline elements of a text run. Everything that is not a
// recognized element is escaped.
func inline(s string, tk tokens) string {
	var b strings.Builder
	esc := html.EscapeString
	for {
		m := reInline.FindStringSubmatchIndex(s)
		if m == nil {
			b.WriteString(esc(s))
			return b.String()
		}
		b.WriteString(esc(s[:m[0]]))
		g := func(n int) string {
			if m[2*n] < 0 {
				return ""
			}
			return s[m[2*n]:m[2*n+1]]
		}
		has := func(n int) bool { return m[2*n] >= 0 }
		whole := s[m[0]:m[1]]
		switch {
		case has(1):
			b.WriteString("<code>" + esc(g(1)) + "</code>")
		case has(2), has(3):
			sha := g(2) + g(3)
			name, mime, ok := "", "", false
			if tk.att != nil {
				name, mime, ok = tk.att(sha)
			}
			switch {
			case !ok:
				b.WriteString(`<span class="chip unavailable">` + esc(tk.unavailable) + `</span>`)
			case has(2) && strings.HasPrefix(mime, "image/") && mime != "image/heic" && mime != "image/heif":
				b.WriteString(`<img class="att" src="/api/v1/att/` + sha + `" alt="` + esc(name) + `" loading="lazy">`)
			default:
				b.WriteString(`<a class="chip file" href="/api/v1/att/` + sha + `" download="` + esc(name) + `">` + esc(name) + `</a>`)
			}
		case has(4):
			title, state, ok := "", "", false
			if tk.link != nil {
				title, state, ok = tk.link(g(4))
			}
			if !ok {
				b.WriteString(`<span class="chip unavailable">` + esc(tk.unavailable) + `</span>`)
				break
			}
			if g(5) != "" {
				title = g(5)
			}
			b.WriteString(`<a class="chip link" data-state="` + esc(state) + `" href="/i/` + esc(g(4)) + `">` + esc(title) + `</a>`)
		case has(6):
			if tk.user != nil {
				if name, ok := tk.user(g(6)); ok {
					b.WriteString(`<span class="chip mention">` + esc(name) + `</span>`)
					break
				}
			}
			b.WriteString(esc(whole))
		case has(8):
			u, ok := safeURL(g(8))
			text := g(7)
			if text == "" {
				text = g(8)
			}
			if !ok {
				b.WriteString(esc(text))
				break
			}
			b.WriteString(`<a href="` + esc(u) + `" rel="noopener noreferrer" target="_blank">` + inline(text, tk) + `</a>`)
		case has(9):
			b.WriteString("<strong>" + inline(g(9), tk) + "</strong>")
		case has(10):
			b.WriteString("<del>" + inline(g(10), tk) + "</del>")
		case has(11):
			b.WriteString("<em>" + inline(g(11), tk) + "</em>")
		case has(12):
			u := strings.TrimRight(g(12), ".,;:!?")
			b.WriteString(`<a href="` + esc(u) + `" rel="noopener noreferrer" target="_blank">` + esc(u) + `</a>` + esc(g(12)[len(u):]))
		}
		s = s[m[1]:]
	}
}
