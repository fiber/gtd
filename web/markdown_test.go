package web

import (
	"strings"
	"testing"
)

const sha = "a02a057e737c31e8442a34db35f889a5013733b5e1adf7769bbc878ff36e0c3a"

var testTokens = tokens{
	link: func(id string) (string, string, bool) {
		if id == "01ITEM" {
			return `Call <Lee> & "Co"`, "next", true
		}
		return "", "", false
	},
	att: func(s string) (string, string, bool) {
		switch s {
		case sha:
			return `board "1".png`, "image/png", true
		case strings.Repeat("b", 64):
			return "report.pdf", "application/pdf", true
		case strings.Repeat("c", 64):
			return "photo.heic", "image/heic", true
		}
		return "", "", false
	},
	user:        func(id string) (string, bool) { return "Dana <E>", id == "dana" },
	unavailable: "unavailable item",
}

func TestRenderMarkdown(t *testing.T) {
	b64 := strings.Repeat("b", 64)
	for _, c := range []struct {
		name string
		in   string
		want string
	}{
		{"paragraph with a line break", "hello\nworld", "<p>hello<br>\nworld</p>\n"},
		{"two paragraphs", "a\n\nb", "<p>a</p>\n<p>b</p>\n"},
		{"headings", "# One\n### Three", "<h1>One</h1>\n<h3>Three</h3>\n"},
		{"emphasis, nested", "**bold *it*** ~~gone~~ `co<de>`", "<p><strong>bold <em>it</em></strong> <del>gone</del> <code>co&lt;de&gt;</code></p>\n"},
		{"code block", "```go\nif a < b {\n}\n```", "<pre><code class=\"language-go\">if a &lt; b {\n}\n</code></pre>\n"},
		{"quote", "> quoted", "<blockquote>\n<p>quoted</p>\n</blockquote>\n"},
		{"task list, nested", "- [ ] open\n- [x] done\n  - nested",
			"<ul>\n<li><input disabled=\"\" type=\"checkbox\"> open</li>\n<li><input checked=\"\" disabled=\"\" type=\"checkbox\"> done\n<ul>\n<li>nested</li>\n</ul>\n</li>\n</ul>\n"},
		{"table", "| A | B |\n|---|---|\n| 1 | **2** |",
			"<table>\n<thead>\n<tr>\n<th>A</th>\n<th>B</th>\n</tr>\n</thead>\n<tbody>\n<tr>\n<td>1</td>\n<td><strong>2</strong></td>\n</tr>\n</tbody>\n</table>\n"},
		{"external link", "[site](https://example.org/a?b=1&c=2)", `<p><a href="https://example.org/a?b=1&amp;c=2" target="_blank" rel="noopener noreferrer">site</a></p>` + "\n"},
		{"internal link", "[in](/i/1)", `<p><a href="/i/1">in</a></p>` + "\n"},
		{"bare url", "see https://example.org/x.", `<p>see <a href="https://example.org/x" target="_blank" rel="noopener noreferrer">https://example.org/x</a>.</p>` + "\n"},
		{"external image becomes a link", "![alt](https://example.org/i.png)", `<p><a href="https://example.org/i.png" target="_blank" rel="noopener noreferrer">alt</a></p>` + "\n"},
		{"raw html is dropped", "<b>x</b> y", "<p><!-- raw HTML omitted -->x<!-- raw HTML omitted --> y</p>\n"},

		{"item link", "see [[01ITEM]]", `<p>see <a class="chip link" data-state="next" href="i/01ITEM">Call &lt;Lee&gt; &amp; &#34;Co&#34;</a></p>` + "\n"},
		{"item link with label", "[[01ITEM|the call]]", `<p><a class="chip link" data-state="next" href="i/01ITEM">the call</a></p>` + "\n"},
		{"unavailable item", "[[01NOPE]]", `<p><span class="chip unavailable">unavailable item</span></p>` + "\n"},
		{"plain brackets stay text", "[[Some Title]]", "<p>[[Some Title]]</p>\n"},
		{"image", "![[att:" + sha + "]]", `<p><img class="att" src="api/v1/att/` + sha + `" alt="board &#34;1&#34;.png" loading="lazy"></p>` + "\n"},
		{"file chip", "[[att:" + b64 + "]]", `<p><a class="chip file" href="api/v1/att/` + b64 + `" download="report.pdf">report.pdf</a></p>` + "\n"},
		{"heic has no preview", "![[att:" + strings.Repeat("c", 64) + "]]", `<p><a class="chip file" href="api/v1/att/` + strings.Repeat("c", 64) + `" download="photo.heic">photo.heic</a></p>` + "\n"},
		{"attachment of another item", "![[att:" + strings.Repeat("d", 64) + "]]", `<p><span class="chip unavailable">unavailable item</span></p>` + "\n"},
		{"mention", "ask @[user:dana] and @[user:nobody] and @foo", `<p>ask <span class="chip mention">Dana &lt;E&gt;</span> and @[user:nobody] and @foo</p>` + "\n"},
		{"tokens in a heading and a task", "# [[01ITEM|x]]\n- [ ] [[01ITEM|y]]",
			`<h1><a class="chip link" data-state="next" href="i/01ITEM">x</a></h1>` + "\n<ul>\n" +
				`<li><input disabled="" type="checkbox"> <a class="chip link" data-state="next" href="i/01ITEM">y</a></li>` + "\n</ul>\n"},
		{"a token in code is code", "`[[01ITEM]]`", "<p><code>[[01ITEM]]</code></p>\n"},
		{"empty", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := renderMarkdown(c.in, testTokens); got != c.want {
				t.Errorf("\n got: %q\nwant: %q", got, c.want)
			}
		})
	}
}

// TestMarkdownIsSafe: no input produces a tag, attribute or URL scheme that
// the renderer did not write itself.
func TestMarkdownIsSafe(t *testing.T) {
	attacks := []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`# <svg onload=alert(1)>`,
		`- <iframe src="javascript:alert(1)"></iframe>`,
		`| <b onmouseover=alert(1)>x</b> | y |` + "\n|---|---|\n| <script>1</script> | 2 |",
		`> <script>alert(1)</script>`,
		"```\n</code></pre><script>alert(1)</script>\n```",
		"`</code><script>alert(1)</script>`",
		`[click](javascript:alert(1))`,
		`[click](JaVaScRiPt:alert(1))`,
		`[click](data:text/html,<script>alert(1)</script>)`,
		`[click](vbscript:x)`,
		`[click](//evil.example/x)`,
		`[x](https://example.org/"onmouseover="alert(1))`,
		`[<script>alert(1)</script>](https://example.org)`,
		`![x](javascript:alert(1))`,
		`https://example.org/"><script>alert(1)</script>`,
		`[[01ITEM|<script>alert(1)</script>]]`,
		`[[01ITEM|" onmouseover="alert(1)]]`,
		`**<script>alert(1)</script>**`,
		`*<img src=x onerror=alert(1)>*`,
		`@[user:dana]<script>`,
		`&lt;script&gt; &#60;script&#62; <!-- --> <![CDATA[x]]>`,
	}
	allowed := map[string]bool{"p": true, "h1": true, "ul": true, "ol": true, "li": true, "table": true, "thead": true, "tbody": true,
		"tr": true, "th": true, "td": true, "blockquote": true, "pre": true, "code": true, "a": true, "strong": true, "em": true,
		"del": true, "span": true, "img": true, "input": true, "hr": true, "br": true, "!--": true}
	for _, in := range attacks {
		out := renderMarkdown(in, testTokens)
		low := strings.ToLower(out)
		for _, bad := range []string{"<script", "<iframe", "<svg", "<b ", "<img src=x", "onmouseover=\"", `href="javascript:`, `href="data:`, `href="vbscript:`} {
			if strings.Contains(low, bad) {
				t.Errorf("input %q\noutput contains %q: %s", in, bad, out)
			}
		}
		// Every tag in the output is one the renderer writes.
		for _, part := range strings.Split(out, "<")[1:] {
			name := strings.TrimLeft(part, "/")
			if i := strings.IndexAny(name, " >\n"); i >= 0 {
				name = name[:i]
			}
			if !allowed[name] {
				t.Errorf("input %q\noutput has tag <%s>: %s", in, name, out)
			}
		}
		// Attribute values cannot be broken out of: a quote from the source is always escaped.
		if strings.Contains(in, `"onmouseover="`) && strings.Contains(out, `"onmouseover="`) {
			t.Errorf("input %q\nattribute injection: %s", in, out)
		}
	}
}
