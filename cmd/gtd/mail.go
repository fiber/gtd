package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/textproto"
	"net/url"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/text/encoding/htmlindex"
)

// cmdToken manages the acting user's own API tokens.
func cmdToken(g globals, sub string, args []string, stdout io.Writer) error {
	c, err := connect(g, false)
	if err != nil {
		return err
	}
	defer c.close()
	type token struct {
		ID       string    `json:"id"`
		Label    string    `json:"label"`
		Scopes   []string  `json:"scopes"`
		Created  time.Time `json:"created"`
		LastUsed time.Time `json:"last_used"`
		Token    string    `json:"token"`
	}
	switch sub {
	case "add":
		label := ""
		args, err := flags("token add", args, func(fs *flag.FlagSet) { fs.StringVar(&label, "label", "", "") })
		if err != nil {
			return err
		}
		if err := want("token add", args, 0, "[-label text]"); err != nil {
			return err
		}
		var t token
		if err := c.do("POST", "/api/v1/me/tokens", map[string]any{"label": label, "scopes": []string{"capture"}}, &t); err != nil {
			return err
		}
		// The token itself is the only output on stdout, so that it can be
		// redirected into a file. It is not shown again.
		fmt.Fprintln(stdout, t.Token)
		return nil
	case "list":
		var list []token
		if err := c.do("GET", "/api/v1/me/tokens", nil, &list); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		for _, t := range list {
			used := "never used"
			if !t.LastUsed.IsZero() {
				used = "last used " + t.LastUsed.Format(time.DateOnly)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\tcreated %s\t%s\n", t.ID, strings.Join(t.Scopes, ","), t.Label, t.Created.Format(time.DateOnly), used)
		}
		return tw.Flush()
	case "rm":
		if err := want("token rm", args, 1, "a token id (see `gtd token list`)"); err != nil {
			return err
		}
		if err := c.do("DELETE", "/api/v1/me/tokens/"+url.PathEscape(args[0]), nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "token %s revoked\n", args[0])
		return nil
	}
	return fmt.Errorf("gtd token: unknown subcommand %q (want add, list or rm)", sub)
}

// cmdCaptureMail reads one mail message on stdin and captures it into the
// inbox of the token's owner: the subject as the first line, then who sent
// it and the text, with the attachments attached. A mail server pipes into
// it (an alias or a filter); gtd itself runs no mail service.
func cmdCaptureMail(args []string, stdin io.Reader, stdout io.Writer) error {
	server, tokenFile := "", ""
	args, err := flags("capture-mail", args, func(fs *flag.FlagSet) {
		fs.StringVar(&server, "server", "http://127.0.0.1:8640", "")
		fs.StringVar(&tokenFile, "token-file", "", "")
	})
	if err != nil {
		return err
	}
	if err := want("capture-mail", args, 0, "[-server url] [-token-file file], the message on stdin"); err != nil {
		return err
	}
	token := strings.TrimSpace(os.Getenv("GTD_TOKEN"))
	if tokenFile != "" {
		data, err := os.ReadFile(tokenFile)
		if err != nil {
			return fmt.Errorf("gtd capture-mail: %w", err)
		}
		token = strings.TrimSpace(string(data))
	}
	if token == "" {
		return errors.New("gtd capture-mail: no token: give -token-file or set GTD_TOKEN (create one with `gtd token add`)")
	}
	m, err := parseMail(stdin)
	if err != nil {
		return fmt.Errorf("gtd capture-mail: %w", err)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("text", m.Text)
	mw.WriteField("via", "email")
	mw.WriteField("ref", m.Ref)
	for _, f := range m.Files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": f.Name}))
		h.Set("Content-Type", f.Type)
		part, err := mw.CreatePart(h)
		if err != nil {
			return err
		}
		part.Write(f.Data)
	}
	mw.Close()
	req, err := http.NewRequest("POST", strings.TrimRight(server, "/")+"/gtd/api/v1/capture", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("gtd capture-mail: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gtd capture-mail: %s answered %d: %s", server, resp.StatusCode, bytes.TrimSpace(body))
	}
	var out struct{ ID string }
	json.Unmarshal(body, &out)
	fmt.Fprintf(stdout, "captured %s (%d attachments)\n", out.ID, len(m.Files))
	return nil
}

type mailFile struct {
	Name, Type string
	Data       []byte
}

type mailMessage struct {
	Text  string // subject, who and when, the text
	Ref   string // the Message-ID
	Files []mailFile
}

// maxMailFile is the server's attachment limit: a larger one is left out and named.
const maxMailFile = 25 << 20

var (
	reTag    = regexp.MustCompile(`(?s)<(script|style)[^>]*>.*?</(script|style)>|<[^>]+>`)
	reBlank  = regexp.MustCompile(`\n{3,}`)
	reSpaces = regexp.MustCompile(`[ \t]+\n`)
)

// parseMail turns an RFC 5322 message into the text and files of a capture.
func parseMail(r io.Reader) (*mailMessage, error) {
	msg, err := mail.ReadMessage(r)
	if err != nil {
		return nil, fmt.Errorf("not a mail message: %w", err)
	}
	dec := &mime.WordDecoder{CharsetReader: charsetReader}
	header := func(k string) string {
		v := msg.Header.Get(k)
		if d, err := dec.DecodeHeader(v); err == nil {
			v = d
		}
		return strings.TrimSpace(v)
	}
	out := &mailMessage{Ref: strings.Trim(header("Message-Id"), "<>")}
	var plain, html []string
	var left []string
	var walk func(h textproto.MIMEHeader, body io.Reader, depth int) error
	walk = func(h textproto.MIMEHeader, body io.Reader, depth int) error {
		ctype, params, err := mime.ParseMediaType(h.Get("Content-Type"))
		if err != nil {
			ctype, params = "text/plain", nil
		}
		if strings.HasPrefix(ctype, "multipart/") && depth < 8 {
			mr := multipart.NewReader(body, params["boundary"])
			for {
				p, err := mr.NextRawPart()
				if err == io.EOF {
					return nil
				}
				if err != nil {
					return fmt.Errorf("broken multipart message: %w", err)
				}
				if err := walk(p.Header, p, depth+1); err != nil {
					return err
				}
			}
		}
		switch strings.ToLower(h.Get("Content-Transfer-Encoding")) {
		case "base64":
			body = base64.NewDecoder(base64.StdEncoding, &skipNewlines{body})
		case "quoted-printable":
			body = quotedprintable.NewReader(body)
		}
		data, err := io.ReadAll(io.LimitReader(body, maxMailFile+1))
		if err != nil {
			return fmt.Errorf("undecodable part: %w", err)
		}
		disp, dparams, _ := mime.ParseMediaType(h.Get("Content-Disposition"))
		name := dparams["filename"]
		if name == "" {
			name = params["name"]
		}
		if d, err := dec.DecodeHeader(name); err == nil {
			name = d
		}
		if disp == "attachment" || name != "" || !strings.HasPrefix(ctype, "text/") {
			if name == "" {
				name = "attachment"
			}
			if len(data) > maxMailFile {
				left = append(left, name)
				return nil
			}
			out.Files = append(out.Files, mailFile{Name: name, Type: ctype, Data: data})
			return nil
		}
		text := string(data)
		if cr, err := charsetReader(params["charset"], bytes.NewReader(data)); err == nil {
			if b, err := io.ReadAll(cr); err == nil {
				text = string(b)
			}
		}
		if ctype == "text/html" {
			html = append(html, text)
		} else {
			plain = append(plain, text)
		}
		return nil
	}
	if err := walk(textproto.MIMEHeader(msg.Header), msg.Body, 0); err != nil {
		return nil, err
	}
	body := strings.Join(plain, "\n\n")
	if strings.TrimSpace(body) == "" && len(html) > 0 {
		// No text part: the HTML with its tags removed is better than nothing.
		body = reTag.ReplaceAllString(strings.Join(html, "\n\n"), "")
		body = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`).Replace(body)
	}
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = reBlank.ReplaceAllString(reSpaces.ReplaceAllString(body, "\n"), "\n\n")
	subject := header("Subject")
	if subject == "" {
		subject = "(no subject)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nFrom: %s\n", subject, header("From"))
	if d := header("Date"); d != "" {
		fmt.Fprintf(&b, "Date: %s\n", d)
	}
	for _, name := range left {
		fmt.Fprintf(&b, "Left out, larger than %d MB: %s\n", maxMailFile>>20, name)
	}
	fmt.Fprintf(&b, "\n%s", strings.TrimSpace(body))
	out.Text = strings.TrimSpace(b.String())
	return out, nil
}

// charsetReader decodes the character sets mail still uses into UTF-8.
func charsetReader(charset string, r io.Reader) (io.Reader, error) {
	switch strings.ToLower(charset) {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return r, nil
	}
	enc, err := htmlindex.Get(charset)
	if err != nil {
		return nil, fmt.Errorf("unknown charset %q", charset)
	}
	return enc.NewDecoder().Reader(r), nil
}

// skipNewlines removes line breaks, which base64 bodies have and the decoder
// does not accept everywhere.
type skipNewlines struct{ r io.Reader }

func (s *skipNewlines) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	out := p[:0]
	for _, c := range p[:n] {
		if c != '\r' && c != '\n' && c != ' ' && c != '\t' {
			out = append(out, c)
		}
	}
	if len(out) == 0 && err == nil && n > 0 {
		return s.Read(p)
	}
	return len(out), err
}
