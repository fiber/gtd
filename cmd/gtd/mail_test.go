package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fiber/gtd/core"
	"github.com/fiber/gtd/store"
	"github.com/fiber/gtd/web"
)

func TestParseMail(t *testing.T) {
	plain := "From: Dana <dana@example.org>\r\nTo: in@example.org\r\nSubject: =?UTF-8?B?QsO8Y2hlciBmw7xyIGRlbiBVcmxhdWI=?=\r\n" +
		"Date: Thu, 19 Nov 2026 09:30:00 +0100\r\nMessage-ID: <abc123@example.org>\r\n" +
		"Content-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"Gr=FC=DFe,\r\n\r\n\r\n\r\nzweite Zeile   \r\n"
	m, err := parseMail(strings.NewReader(plain))
	if err != nil {
		t.Fatal(err)
	}
	want := "Bücher für den Urlaub\n\nFrom: Dana <dana@example.org>\nDate: Thu, 19 Nov 2026 09:30:00 +0100\n\nGrüße,\n\nzweite Zeile"
	if m.Text != want || m.Ref != "abc123@example.org" || len(m.Files) != 0 {
		t.Errorf("plain mail:\n%q\nwant\n%q\nref %q, %d files", m.Text, want, m.Ref, len(m.Files))
	}

	multi := "From: lee@example.org\r\nSubject: Quote for the kitchen\r\nMessage-ID: <q1@example.org>\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=OUTER\r\n\r\n" +
		"--OUTER\r\nContent-Type: multipart/alternative; boundary=INNER\r\n\r\n" +
		"--INNER\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nPlease find the quote attached.\r\n" +
		"--INNER\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Please find the <b>quote</b> attached.</p>\r\n" +
		"--INNER--\r\n" +
		"--OUTER\r\nContent-Type: application/pdf; name=\"quote.pdf\"\r\nContent-Disposition: attachment; filename=\"quote.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		"JVBERi0xLjQK\r\nJSVFT0YK\r\n" +
		"--OUTER\r\nContent-Type: image/png\r\nContent-Disposition: inline; filename*=UTF-8''k%C3%BCche.png\r\nContent-Transfer-Encoding: base64\r\n\r\niVBORw0KGgo=\r\n" +
		"--OUTER--\r\n"
	m, err = parseMail(strings.NewReader(multi))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(m.Text, "Quote for the kitchen\n\nFrom: lee@example.org\n\nPlease find the quote attached.") || strings.Contains(m.Text, "<b>") {
		t.Errorf("multipart text: %q", m.Text)
	}
	if len(m.Files) != 2 || m.Files[0].Name != "quote.pdf" || m.Files[0].Type != "application/pdf" || string(m.Files[0].Data) != "%PDF-1.4\n%%EOF\n" ||
		m.Files[1].Name != "küche.png" || m.Files[1].Type != "image/png" || len(m.Files[1].Data) != 8 {
		t.Errorf("attachments: %+v", m.Files)
	}

	// HTML only: the tags are removed.
	html := "From: news@example.org\r\nSubject: Only HTML\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		"<html><head><style>p{color:red}</style></head><body><p>Fish &amp; chips</p><script>alert(1)</script></body></html>\r\n"
	m, err = parseMail(strings.NewReader(html))
	if err != nil || !strings.HasSuffix(m.Text, "Fish & chips") || strings.Contains(m.Text, "color") || strings.Contains(m.Text, "alert") {
		t.Errorf("html mail: %q %v", m.Text, err)
	}
	// No subject, no content type.
	m, err = parseMail(strings.NewReader("From: x@example.org\r\n\r\njust a line\r\n"))
	if err != nil || m.Text != "(no subject)\n\nFrom: x@example.org\n\njust a line" {
		t.Errorf("bare mail: %q %v", m.Text, err)
	}
	if _, err := parseMail(strings.NewReader("this is not a mail message")); err == nil {
		t.Error("garbage was accepted")
	}
}

// TestCaptureMail: a message piped into `gtd capture-mail` arrives in the
// inbox of the token's owner, with its attachment.
func TestCaptureMail(t *testing.T) {
	st, err := store.Create(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, u := range []string{"robin", "dana"} {
		if err := st.AddUser(core.User{ID: u}); err != nil {
			t.Fatal(err)
		}
	}
	token, _, err := st.AddToken("robin", "mail", "", []string{store.ScopeCapture})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(web.New(st).Handler())
	defer srv.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tokenFile, []byte(token+"\n"), 0o600)

	msg := "From: Lee <lee@example.org>\r\nSubject: Quote for the kitchen\r\nMessage-ID: <q1@example.org>\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nThe quote is attached.\r\n" +
		"--B\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"quote.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQK\r\n" +
		"--B--\r\n"
	var out strings.Builder
	if err := cmdCaptureMail([]string{"-server", srv.URL, "-token-file", tokenFile}, strings.NewReader(msg), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "(1 attachments)") {
		t.Errorf("output: %s", out.String())
	}
	v, _ := st.View("robin")
	caps, _ := v.Items("robin", func(it *core.Item) bool { return it.Kind == core.KindCapture })
	if len(caps) != 1 {
		t.Fatalf("%d captures in robin's inbox", len(caps))
	}
	c := caps[0]
	if c.Title != "Quote for the kitchen" || c.State != core.StInbox || c.Source.Via != "email" || c.Source.Ref != "q1@example.org" ||
		!strings.Contains(c.Original, "From: Lee <lee@example.org>") || !strings.Contains(c.Body, "The quote is attached.") ||
		len(c.Attachments) != 1 || c.Attachments[0].Name != "quote.pdf" || c.Attachments[0].Size != 9 {
		t.Errorf("capture: %+v", c)
	}
	dv, _ := st.View("dana")
	if others, _ := dv.Items("dana", nil); len(others) != 0 {
		t.Errorf("the mail landed in someone else's space: %d items", len(others))
	}

	// Without a token, with a wrong one, with a revoked one: nothing is captured.
	t.Setenv("GTD_TOKEN", "")
	if err := cmdCaptureMail([]string{"-server", srv.URL}, strings.NewReader(msg), &out); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Errorf("no token: %v", err)
	}
	t.Setenv("GTD_TOKEN", token+"x")
	if err := cmdCaptureMail([]string{"-server", srv.URL}, strings.NewReader(msg), &out); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("wrong token: %v", err)
	}
	t.Setenv("GTD_TOKEN", token)
	list, _ := st.Tokens("robin")
	if err := st.DeleteToken("robin", list[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := cmdCaptureMail([]string{"-server", srv.URL}, strings.NewReader(msg), &out); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("revoked token: %v", err)
	}
	if caps, _ := v.Items("robin", func(it *core.Item) bool { return it.Kind == core.KindCapture }); len(caps) != 1 {
		t.Errorf("%d captures after the refused attempts", len(caps))
	}
}
