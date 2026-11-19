package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func catalog(t *testing.T, lang string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("ui", "i18n", lang+".json"))
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%s.json: %v", lang, err)
	}
	return m
}

// TestCatalogs is the M1 acceptance test "all UI strings exist in both
// catalogs": the two catalogs have the same keys, every key the client, the
// wizards and the server use exists, placeholders match, and no key is unused.
func TestCatalogs(t *testing.T) {
	en, de := catalog(t, "en"), catalog(t, "de")
	for k, v := range en {
		d, ok := de[k]
		if !ok {
			t.Errorf("de.json lacks %q", k)
			continue
		}
		if v == "" || d == "" {
			t.Errorf("%q is empty in a catalog", k)
		}
		ph := regexp.MustCompile(`\{[a-z]+\}`)
		a, b := ph.FindAllString(v, -1), ph.FindAllString(d, -1)
		sort.Strings(a)
		sort.Strings(b)
		if strings.Join(a, ",") != strings.Join(b, ",") {
			t.Errorf("%q: placeholders differ: en %v, de %v", k, a, b)
		}
	}
	for k := range de {
		if _, ok := en[k]; !ok {
			t.Errorf("en.json lacks %q", k)
		}
	}

	used := map[string]bool{}
	// Keys written out in the client: every quoted dotted lowercase name.
	js, _ := filepath.Glob(filepath.Join("ui", "*.js"))
	if len(js) < 8 {
		t.Fatalf("found only %d client modules", len(js))
	}
	reJS := regexp.MustCompile(`'([a-z_]+(?:\.[a-z_0-9+]+)+)'`)
	// Keys built from a prefix: t('state.' + x) and friends.
	reDyn := regexp.MustCompile(`'([a-z_.]+\.)' \+`)
	families := map[string][]string{
		"state.":         {"inbox", "next", "planned", "waiting", "someday", "done", "dropped", "trashed", "active", "unsorted", "filed", "archived", "retired"},
		"transition.":    {"inbox", "next", "waiting", "someday", "done", "dropped", "trashed", "active", "unsorted", "filed", "archived", "retired"},
		"kind.":          {"capture", "action", "project", "note", "area"},
		"board.":         {"inbox", "next", "planned", "waiting", "deferred", "someday", "done"},
		"context.":       {"errands", "calls", "computer", "home", "office", "anywhere", "agenda"},
		"energy.":        {"low", "med", "high"},
		"time.":          {"5m", "15m", "30m", "1h", "2h+"},
		"notification.":  {"unblocked", "blocker_dropped", "delegated", "handed_back"},
		"warning.":       {"child_after_parent", "target_before_blocker", "slipping"},
		"warning.short.": {"child_after_parent", "target_before_blocker", "slipping"},
		"field.":         {"project", "parent", "area", "context", "owner", "waiting_on", "defer_until", "target", "energy", "time", "template"},
		"theme.":         {"light", "dark", "system"},
		"repeat.":        {"1d", "2d", "3d", "1w", "2w", "4w"},
		"notes.tab.":     {"notes", "ideas"},
		"note.":          {"saving", "saved", "unsaved"},
		"ai.state.":      {"off", "ok", "offline", "building"},
		"ai.policy.":     {"off", "local", "any"},
		"ai.note.":       {"offline", "building"},
		"wizard.title.":  {"clarify", "note", "review", "sweep"},
		"review.":        {"clear", "unsorted", "projects", "stale", "waiting", "dates", "someday", "areas"},
		"review.opt.":    {"continue", "keep", "someday", "done", "drop", "defer", "nudge", "resolved", "activate"},
		"clarify.":       {"what", "someday", "reference", "project", "action", "two_minutes", "do_now", "who", "person", "when", "after"},
	}
	for _, file := range js {
		src, _ := os.ReadFile(file)
		for _, m := range reJS.FindAllStringSubmatch(string(src), -1) {
			if strings.HasPrefix(m[1], "gtd.") { // a localStorage key
				continue
			}
			used[m[1]] = true
		}
		for _, m := range reDyn.FindAllStringSubmatch(string(src), -1) {
			members, ok := families[m[1]]
			if !ok {
				t.Errorf("%s builds keys with prefix %q: list its members in this test", file, m[1])
			}
			for _, x := range members {
				used[m[1]+x] = true
			}
		}
	}
	// Keys in Go: the wizards' questions, labels and errors, and the server's own texts.
	reGo := regexp.MustCompile(`"((?:clarify|note|field|option|answer|hint|date|err|login|capture|format|item|review|warning|sweep|password|link|admin|ai|tasklines)\.[a-z_0-9.+]+)"`)
	var gofiles []string
	for _, pattern := range []string{"*.go", filepath.Join("..", "wizard", "*.go")} {
		m, _ := filepath.Glob(pattern)
		gofiles = append(gofiles, m...)
	}
	for _, file := range gofiles {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, _ := os.ReadFile(file)
		for _, m := range reGo.FindAllStringSubmatch(string(src), -1) {
			if !strings.HasSuffix(m[1], ".") { // a prefix, completed in code
				used[m[1]] = true
			}
		}
	}
	for _, x := range families["clarify."] { // "clarify." + c.At in wizard/clarify.go
		used["clarify."+x] = true
	}
	for _, p := range []string{"review.", "review.opt."} { // "review." + phase, opt(…) in wizard/review.go
		for _, x := range families[p] {
			used[p+x] = true
		}
	}
	for _, p := range []string{"energy.", "time.", "context."} { // labels(prefix, …) in the wizards
		for _, x := range families[p] {
			used[p+x] = true
		}
	}

	for k := range used {
		if _, ok := en[k]; !ok {
			t.Errorf("key %q is used but missing from the catalogs", k)
		}
	}
	for k := range en {
		if !used[k] {
			t.Errorf("key %q is in the catalogs but not used", k)
		}
	}
	if len(used) < 200 {
		t.Errorf("only %d keys found in use: the scan is broken", len(used))
	}
}
