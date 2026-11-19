package web

import (
	"strings"
	"testing"
)

// TestNotesAndIdeas: the notes list with its filters and counts, the ideas
// list, both limited to the user's spaces, and the in-app help.
func TestNotesAndIdeas(t *testing.T) {
	s := newSite(t)
	sven, eric, grit := s.login("sven"), s.login("eric"), s.login("grit")
	theme := sven.create("sven", map[string]any{"kind": "project", "title": "Gap fill trading", "state": "someday"})
	active := sven.create("sven", map[string]any{"kind": "project", "title": "Collector survives resets"})
	sven.create("sven", map[string]any{"kind": "note", "title": "Loose thought", "body": "# Heading\n\nSome **bold** idea about [[" + theme + "]] and `code`.\n- [ ] a task"})
	sven.create("sven", map[string]any{"kind": "note", "title": "Backtest results", "state": "filed", "project": theme, "body": strings.Repeat("long text ", 60)})
	sven.create("sven", map[string]any{"kind": "note", "title": "Second idea", "state": "filed", "project": theme})
	sven.create("sven", map[string]any{"kind": "note", "title": "Reset design", "state": "filed", "project": active})
	shared := sven.create("nis", map[string]any{"kind": "note", "title": "Shared NIS note"})
	sven.create("sven", map[string]any{"kind": "action", "title": "Try options on earnings days", "state": "someday"})
	sven.create("sven", map[string]any{"kind": "action", "title": "A real next action"})
	grit.create("grit", map[string]any{"kind": "note", "title": "Grit's secret note"})
	grit.create("grit", map[string]any{"kind": "project", "title": "Grit's secret theme", "state": "someday"})

	type notesRes struct {
		Notes    []Brief
		Counts   map[string]int
		Projects []Named
	}
	titles := func(list []Brief) string {
		var out []string
		for _, b := range list {
			out = append(out, b.Title)
		}
		return strings.Join(out, " | ")
	}
	var n notesRes
	sven.ok("GET", "/api/v1/notes", nil, &n)
	// Newest first, across the personal and the shared space, nothing of Grit's.
	if got := titles(n.Notes); got != "Shared NIS note | Reset design | Second idea | Backtest results | Loose thought" {
		t.Errorf("sven's notes: %s", got)
	}
	if n.Counts["unsorted"] != 2 || n.Counts["filed"] != 3 || len(n.Projects) != 2 {
		t.Errorf("counts %v, %d projects", n.Counts, len(n.Projects))
	}
	loose := n.Notes[4]
	if loose.Excerpt != "Heading Some bold idea about and code. a task" || loose.SpaceName == "" {
		t.Errorf("excerpt = %q, space %q", loose.Excerpt, loose.SpaceName)
	}
	if e := n.Notes[3].Excerpt; len([]rune(e)) > 181 || !strings.HasSuffix(e, "…") {
		t.Errorf("long excerpt: %q", e)
	}
	for query, want := range map[string]string{
		"?state=unsorted":                   "Shared NIS note | Loose thought",
		"?project=" + theme:                 "Second idea | Backtest results",
		"?q=IDEA":                           "Second idea | Loose thought",
		"?q=bold+idea":                      "Loose thought",
		"?project=" + theme + "&q=backtest": "Backtest results",
		"?state=archived":                   "",
		"?q=secret":                         "",
	} {
		sven.ok("GET", "/api/v1/notes"+query, nil, &n)
		if got := titles(n.Notes); got != want {
			t.Errorf("notes%s: %q, want %q", query, got, want)
		}
	}
	// The counts follow the other filters, not the state tab.
	sven.ok("GET", "/api/v1/notes?state=unsorted&project="+theme, nil, &n)
	if len(n.Notes) != 0 || n.Counts["filed"] != 2 {
		t.Errorf("counts under a project filter: %v", n.Counts)
	}
	// Eric shares only nis.
	eric.ok("GET", "/api/v1/notes", nil, &n)
	if got := titles(n.Notes); got != "Shared NIS note" || len(n.Projects) != 0 {
		t.Errorf("eric's notes: %s, projects %v", got, n.Projects)
	}
	_ = shared

	var ideas struct{ Themes, Actions []Brief }
	sven.ok("GET", "/api/v1/ideas", nil, &ideas)
	if titles(ideas.Themes) != "Gap fill trading" || ideas.Themes[0].Notes != 2 || titles(ideas.Actions) != "Try options on earnings days" {
		t.Errorf("ideas: themes %s (%d notes), actions %s", titles(ideas.Themes), ideas.Themes[0].Notes, titles(ideas.Actions))
	}
	eric.ok("GET", "/api/v1/ideas", nil, &ideas)
	if len(ideas.Themes)+len(ideas.Actions) != 0 {
		t.Errorf("eric sees ideas: %+v", ideas)
	}

	var help struct{ HTML string }
	sven.ok("GET", "/api/v1/help", nil, &help)
	if !strings.Contains(help.HTML, "<h1>Using gtd</h1>") || !strings.Contains(help.HTML, "<table>") {
		t.Errorf("help does not render the tutorial: %.200s", help.HTML)
	}
	if code, _ := s.anon().do("GET", "/api/v1/help", nil, nil); code != 401 {
		t.Errorf("help without login: %d", code)
	}
}
