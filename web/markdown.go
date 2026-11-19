package web

import (
	"bytes"
	"html"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	goldhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Markdown is rendered on the server with goldmark: CommonMark plus GFM
// tables, task lists, strikethrough and autolinks (SPEC §3.1). Raw HTML in
// the source is dropped and dangerous URLs are emptied (goldmark's safe
// mode). gtd's text tokens (SPEC §3.3) are an inline extension. An image
// that points to another site becomes a link: the page never loads anything
// from elsewhere.

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

var tokensKey = parser.NewContextKey()

var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(
		parser.WithInlineParsers(util.Prioritized(tokenParser{}, 100)), // before links (200)
		parser.WithASTTransformers(util.Prioritized(linkTransformer{}, 500)),
	),
	goldmark.WithRendererOptions(
		goldhtml.WithHardWraps(), // a line break in a note is a line break
		renderer.WithNodeRenderers(util.Prioritized(tokenRenderer{}, 100)),
	),
)

// renderMarkdown returns sanitized HTML for a Markdown body.
func renderMarkdown(src string, tk tokens) string {
	ctx := parser.NewContext()
	ctx.Set(tokensKey, tk)
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(src), &buf, parser.WithContext(ctx)); err != nil {
		return "<pre>" + html.EscapeString(src) + "</pre>\n"
	}
	return buf.String()
}

// tokenNode is a resolved gtd token. Its HTML is built when it is parsed,
// from escaped values only.
type tokenNode struct {
	ast.BaseInline
	html string
}

var kindToken = ast.NewNodeKind("GtdToken")

func (n *tokenNode) Kind() ast.NodeKind { return kindToken }
func (n *tokenNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"html": n.html}, nil)
}

var reTokenAt = regexp.MustCompile(`^(?:(!)?\[\[att:([0-9a-f]{64})\]\]|\[\[([0-9A-Za-z_-]{1,40})(?:\|([^\]\n]*))?\]\]|@\[user:([a-z0-9-]{1,64})\])`)

type tokenParser struct{}

func (tokenParser) Trigger() []byte { return []byte{'[', '!', '@'} }

func (tokenParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, _ := block.PeekLine()
	m := reTokenAt.FindSubmatch(line)
	if m == nil {
		return nil
	}
	tk, _ := pc.Get(tokensKey).(tokens)
	esc := html.EscapeString
	unavailable := `<span class="chip unavailable">` + esc(tk.unavailable) + `</span>`
	var out string
	switch {
	case len(m[2]) > 0: // attachment
		sha := string(m[2])
		name, mime, ok := "", "", false
		if tk.att != nil {
			name, mime, ok = tk.att(sha)
		}
		switch {
		case !ok:
			out = unavailable
		case len(m[1]) > 0 && strings.HasPrefix(mime, "image/") && mime != "image/heic" && mime != "image/heif":
			out = `<img class="att" src="/api/v1/att/` + sha + `" alt="` + esc(name) + `" loading="lazy">`
		default:
			out = `<a class="chip file" href="/api/v1/att/` + sha + `" download="` + esc(name) + `">` + esc(name) + `</a>`
		}
	case len(m[3]) > 0: // item link
		id := string(m[3])
		title, state, ok := "", "", false
		if tk.link != nil {
			title, state, ok = tk.link(id)
		}
		if !ok {
			out = unavailable
			break
		}
		if len(m[4]) > 0 {
			title = string(m[4])
		}
		out = `<a class="chip link" data-state="` + esc(state) + `" href="/i/` + esc(id) + `">` + esc(title) + `</a>`
	default: // mention
		if tk.user == nil {
			return nil
		}
		name, ok := tk.user(string(m[5]))
		if !ok {
			return nil // an unknown @[user:…] stays plain text
		}
		out = `<span class="chip mention">` + esc(name) + `</span>`
	}
	block.Advance(len(m[0]))
	return &tokenNode{html: out}
}

type tokenRenderer struct{}

func (tokenRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindToken, func(w util.BufWriter, _ []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			w.WriteString(n.(*tokenNode).html)
		}
		return ast.WalkContinue, nil
	})
}

// linkTransformer opens external links in a new tab without a referrer, and
// turns images into links to them.
type linkTransformer struct{}

func (linkTransformer) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	var images []*ast.Image
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch x := n.(type) {
		case *ast.Image:
			images = append(images, x)
		case *ast.Link:
			external(x, x.Destination)
		case *ast.AutoLink:
			external(x, []byte("http"))
		}
		return ast.WalkContinue, nil
	})
	for _, img := range images {
		link := ast.NewLink()
		link.Destination, link.Title = img.Destination, img.Title
		for c := img.FirstChild(); c != nil; {
			next := c.NextSibling()
			link.AppendChild(link, c)
			c = next
		}
		external(link, link.Destination)
		img.Parent().ReplaceChild(img.Parent(), img, link)
	}
}

func external(n ast.Node, dest []byte) {
	if bytes.HasPrefix(dest, []byte("/")) && !bytes.HasPrefix(dest, []byte("//")) || bytes.HasPrefix(dest, []byte("#")) {
		return // a link inside gtd
	}
	n.SetAttributeString("target", []byte("_blank"))
	n.SetAttributeString("rel", []byte("noopener noreferrer"))
}
