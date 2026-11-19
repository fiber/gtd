package web

import (
	"strings"
	"testing"
)

// TestNotesAndIdeas: the notes list with its filters and counts, the ideas
// list, both limited to the user's spaces, and the in-app help.
func TestNotesAndIdeas(t *testing.T) {
	s := newSite(t)
	robin, dana, jo := s.login("robin"), s.login("dana"), s.login("jo")
	theme := robin.create("robin", map[string]any{"kind": "project", "title": "Sourdough baking", "state": "someday"})
	active := robin.create("robin", map[string]any{"kind": "project", "title": "Engine survives resets"})
	robin.create("robin", map[string]any{"kind": "note", "title": "Loose thought", "body": "# Heading\n\nSome **bold** idea about [[" + theme + "]] and `code`.\n- [ ] a task"})
	robin.create("robin", map[string]any{"kind": "note", "title": "Backtest results", "state": "filed", "project": theme, "body": strings.Repeat("long text ", 60)})
	robin.create("robin", map[string]any{"kind": "note", "title": "Second idea", "state": "filed", "project": theme})
	robin.create("robin", map[string]any{"kind": "note", "title": "Reset design", "state": "filed", "project": active})
	shared := robin.create("net", map[string]any{"kind": "note", "title": "Shared NET note"})
	robin.create("robin", map[string]any{"kind": "action", "title": "Try rye flour", "state": "someday"})
	robin.create("robin", map[string]any{"kind": "action", "title": "A real next action"})
	jo.create("jo", map[string]any{"kind": "note", "title": "Jo's secret note"})
	jo.create("jo", map[string]any{"kind": "project", "title": "Jo's secret theme", "state": "someday"})

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
	robin.ok("GET", "/api/v1/notes", nil, &n)
	// Newest first, across the personal and the shared space, nothing of Jo's.
	if got := titles(n.Notes); got != "Shared NET note | Reset design | Second idea | Backtest results | Loose thought" {
		t.Errorf("robin's notes: %s", got)
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
		"?state=unsorted":                   "Shared NET note | Loose thought",
		"?project=" + theme:                 "Second idea | Backtest results",
		"?q=IDEA":                           "Second idea | Loose thought",
		"?q=bold+idea":                      "Loose thought",
		"?project=" + theme + "&q=backtest": "Backtest results",
		"?state=archived":                   "",
		"?q=secret":                         "",
	} {
		robin.ok("GET", "/api/v1/notes"+query, nil, &n)
		if got := titles(n.Notes); got != want {
			t.Errorf("notes%s: %q, want %q", query, got, want)
		}
	}
	// The counts follow the other filters, not the state tab.
	robin.ok("GET", "/api/v1/notes?state=unsorted&project="+theme, nil, &n)
	if len(n.Notes) != 0 || n.Counts["filed"] != 2 {
		t.Errorf("counts under a project filter: %v", n.Counts)
	}
	// Dana shares only net.
	dana.ok("GET", "/api/v1/notes", nil, &n)
	if got := titles(n.Notes); got != "Shared NET note" || len(n.Projects) != 0 {
		t.Errorf("dana's notes: %s, projects %v", got, n.Projects)
	}
	_ = shared

	var ideas struct{ Themes, Actions []Brief }
	robin.ok("GET", "/api/v1/ideas", nil, &ideas)
	if titles(ideas.Themes) != "Sourdough baking" || ideas.Themes[0].Notes != 2 || titles(ideas.Actions) != "Try rye flour" {
		t.Errorf("ideas: themes %s (%d notes), actions %s", titles(ideas.Themes), ideas.Themes[0].Notes, titles(ideas.Actions))
	}
	dana.ok("GET", "/api/v1/ideas", nil, &ideas)
	if len(ideas.Themes)+len(ideas.Actions) != 0 {
		t.Errorf("dana sees ideas: %+v", ideas)
	}

	var help struct{ HTML string }
	robin.ok("GET", "/api/v1/help", nil, &help)
	if !strings.Contains(help.HTML, "<h1>Using gtd</h1>") || !strings.Contains(help.HTML, "<table>") {
		t.Errorf("help does not render the tutorial: %.200s", help.HTML)
	}
	if code, _ := s.anon().do("GET", "/api/v1/help", nil, nil); code != 401 {
		t.Errorf("help without login: %d", code)
	}
}
