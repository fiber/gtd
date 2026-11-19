package web

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fiber/gtd/core"
	"github.com/fiber/gtd/embed"
	"github.com/fiber/gtd/store"
	"github.com/fiber/gtd/wizard"
)

// The JSON shapes the client renders. The client is a pure renderer: titles
// of referenced items, allowed transitions, roll-ups, warnings and rendered
// Markdown are all computed here.

// Named is a reference to an item or user, with what a chip needs.
type Named struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state,omitempty"`
	Owner string `json:"owner,omitempty"`
	Kind  string `json:"kind,omitempty"`
}

// Brief is an item in a list, on the board or in the plan.
type Brief struct {
	ID           string       `json:"id"`
	Space        string       `json:"space"`
	Kind         string       `json:"kind"`
	State        string       `json:"state"`
	Rev          uint64       `json:"rev"`
	Title        string       `json:"title"`
	Project      string       `json:"project,omitempty"`
	ProjectTitle string       `json:"project_title,omitempty"`
	Parent       string       `json:"parent,omitempty"`
	Area         string       `json:"area,omitempty"`
	AreaTitle    string       `json:"area_title,omitempty"`
	Context      string       `json:"context,omitempty"`
	Owner        string       `json:"owner,omitempty"`
	DelegatedBy  string       `json:"delegated_by,omitempty"`
	WaitingOn    string       `json:"waiting_on,omitempty"`
	DeferUntil   string       `json:"defer_until,omitempty"`
	Target       string       `json:"target,omitempty"`
	Due          string       `json:"due,omitempty"`
	Energy       string       `json:"energy,omitempty"`
	Time         string       `json:"time,omitempty"`
	Repeat       string       `json:"repeat,omitempty"`
	Deferred     bool         `json:"deferred,omitempty"`
	Slipping     bool         `json:"slipping,omitempty"`
	Unblocked    bool         `json:"unblocked,omitempty"`  // the dependency rule made it next, in the last 3 days
	Resurfaced   bool         `json:"resurfaced,omitempty"` // the tickler brought it back, in the last 3 days
	WaitsFor     []Named      `json:"waits_for,omitempty"`
	Badges       []string     `json:"badges,omitempty"` // values of the template's enum fields
	Attachments  int          `json:"attachments,omitempty"`
	Rollup       *core.Rollup `json:"rollup,omitempty"`     // projects
	Excerpt      string       `json:"excerpt,omitempty"`    // notes: the start of the body as plain text
	SpaceName    string       `json:"space_name,omitempty"` // in lists that span spaces
	Notes        int          `json:"notes,omitempty"`      // projects in the ideas list: notes filed in them
	Created      time.Time    `json:"created"`
	Updated      time.Time    `json:"updated"`
}

func title(st *core.State, id string) string {
	if it := st.Item(id); it != nil {
		return it.Title
	}
	return ""
}

func named(items []*core.Item) []Named {
	out := []Named{}
	for _, it := range items {
		out = append(out, Named{ID: it.ID, Title: it.Title, State: it.State, Owner: it.Owner, Kind: it.Kind})
	}
	return out
}

func finished(state string) bool {
	return state == core.StDone || state == core.StDropped || state == core.StTrashed
}

func brief(st *core.State, it *core.Item, today string, roll map[string]core.Rollup) Brief {
	b := Brief{ID: it.ID, Space: it.Space, Kind: it.Kind, State: it.State, Rev: it.Rev, Title: it.Title,
		Project: it.Project, ProjectTitle: title(st, it.Project), Parent: it.Parent, Area: it.Area, AreaTitle: title(st, it.Area),
		Context: it.Context, Owner: it.Owner, DelegatedBy: it.DelegatedBy, WaitingOn: it.WaitingOn, DeferUntil: it.DeferUntil, Target: it.Target, Due: it.Due,
		Energy: it.Energy, Time: it.Time, Repeat: it.Repeat, Attachments: len(it.Attachments), Created: it.Created, Updated: it.Updated,
		Deferred:   core.Deferred(it, today),
		Slipping:   it.Target != "" && it.Target < today && !finished(it.State),
		Unblocked:  core.Unblocked(it, recent(today)),
		Resurfaced: core.Resurfaced(it, recent(today)),
	}
	if it.Kind == core.KindAction {
		b.WaitsFor = named(core.OpenBlockers(st, it))
	}
	for _, t := range st.Space.Templates {
		if t.Key == it.Template && t.Kind == it.Kind {
			for _, f := range t.Fields {
				if f.Type == "enum" && it.Fields[f.Key] != "" {
					b.Badges = append(b.Badges, f.Key+":"+it.Fields[f.Key])
				}
			}
		}
	}
	if r, ok := roll[it.ID]; ok && it.Kind == core.KindProject {
		b.Rollup = &r
	}
	return b
}

// recent is the date three days before today: how long "unblocked" and
// "resurfaced" stay highlighted (SPEC §5.6).
func recent(today string) string {
	t, err := time.Parse(time.DateOnly, today)
	if err != nil {
		return today
	}
	return t.AddDate(0, 0, -3).Format(time.DateOnly)
}

func briefs(st *core.State, items []*core.Item, today string, roll map[string]core.Rollup) []Brief {
	out := []Brief{}
	for _, it := range items {
		out = append(out, brief(st, it, today, roll))
	}
	return out
}

var (
	reExcerptToken = regexp.MustCompile(`!?\[\[[^\]\n]*\]\]|@\[user:[a-z0-9-]+\]`)
	reExcerptMark  = regexp.MustCompile("(?m)^[ \\t]*(#{1,6}|>|[-*+]|\\d+[.)])[ \\t]+|[*_~`]+|\\[[ xX]\\][ \\t]")
	reSpaces       = regexp.MustCompile(`\s+`)
)

// excerpt is the start of a Markdown body as one line of plain text.
func excerpt(body string, max int) string {
	text := reExcerptToken.ReplaceAllString(body, "")
	text = reExcerptMark.ReplaceAllString(text, "")
	text = strings.TrimSpace(reSpaces.ReplaceAllString(text, " "))
	if r := []rune(text); len(r) > max {
		return strings.TrimSpace(string(r[:max])) + "…"
	}
	return text
}

// Ref maps what the editor shows for a token to the token itself.
type Ref struct {
	Label string `json:"label"`
	Token string `json:"token"`
}

var reToken = regexp.MustCompile(`!?\[\[att:([0-9a-f]{64})\]\]|\[\[([0-9A-Za-z_-]{1,40})(?:\|([^\]\n]*))?\]\]|@\[user:([a-z0-9-]{1,64})\]`)

// editForm returns the body as the editor shows it: tokens replaced by
// readable labels ([[Title]], ![[name.png]], @Name), plus the mapping back.
// Users never see or type IDs (D-07). The client replaces labels by tokens
// when it saves.
func editForm(body string, tk tokens) (string, []Ref) {
	refs := []Ref{}
	byLabel := map[string]string{}
	text := reToken.ReplaceAllStringFunc(body, func(tok string) string {
		m := reToken.FindStringSubmatch(tok)
		label := ""
		switch {
		case m[1] != "":
			name, _, ok := tk.att(m[1])
			if !ok {
				return tok
			}
			label = tok[:strings.Index(tok, "[[")+2] + name + "]]"
		case m[2] != "":
			t, _, ok := tk.link(m[2])
			if !ok {
				return tok
			}
			if m[3] != "" {
				t = m[3]
			}
			label = "[[" + t + "]]"
		default:
			name, ok := tk.user(m[4])
			if !ok {
				return tok
			}
			label = "@" + name
		}
		// Two different tokens must not share a label: number the later ones.
		base := label
		for n := 2; byLabel[label] != "" && byLabel[label] != tok; n++ {
			suffix := " (" + strconv.Itoa(n) + ")"
			if strings.HasSuffix(base, "]]") {
				label = strings.TrimSuffix(base, "]]") + suffix + "]]"
			} else {
				label = base + suffix
			}
		}
		if byLabel[label] == "" {
			byLabel[label] = tok
			refs = append(refs, Ref{label, tok})
		}
		return label
	})
	return text, refs
}

// ItemView is one item with everything its page shows.
type ItemView struct {
	*core.Item
	BodyHTML     string          `json:"body_html"`
	BodyEdit     string          `json:"body_edit"`
	Refs         []Ref           `json:"refs"`
	Transitions  []string        `json:"transitions"`
	SpaceName    string          `json:"space_name"`
	ProjectTitle string          `json:"project_title,omitempty"`
	AreaTitle    string          `json:"area_title,omitempty"`
	ParentTitle  string          `json:"parent_title,omitempty"`
	Blockers     []Named         `json:"blockers"`
	Dependents   []Named         `json:"dependents"`
	Backlinks    []Named         `json:"backlinks"`
	Derived      []Named         `json:"derived"` // items created from this one
	Sources      []Named         `json:"sources"` // derived_from
	Comments     int             `json:"comments"`
	Tasks        int             `json:"tasks"` // open task lines, for "Process note"
	Deferred     bool            `json:"deferred"`
	Rollup       *core.Rollup    `json:"rollup,omitempty"`
	Warnings     []core.Warning  `json:"warnings"`
	TemplateDef  *core.Template  `json:"template_def,omitempty"`
	Templates    []core.Template `json:"templates"` // the space's templates for this kind
	// Related are the items most similar in meaning, from all the viewer's
	// spaces. RelatedState says whether they could be computed.
	Related      []Related     `json:"related"`
	RelatedState string        `json:"related_state"`
	Options      *SpaceOptions `json:"options"`
}

// Related is an item similar to the one shown.
type Related struct {
	Named
	Space     string  `json:"space"`
	SpaceName string  `json:"space_name"`
	Score     float32 `json:"score"`
}

func (s *Server) related(v *store.View, hits []embed.Hit) []Related {
	out := []Related{}
	names := map[string]string{}
	for _, sp := range v.Spaces() {
		names[sp.ID] = sp.Name
	}
	for _, h := range hits {
		if x, err := v.Item(h.Space, h.ID); err == nil {
			out = append(out, Related{Named{x.ID, x.Title, x.State, x.Owner, x.Kind}, x.Space, names[x.Space], h.Score})
		}
	}
	return out
}

// SpaceOptions are the choices the forms of a space offer.
type SpaceOptions struct {
	Areas    []Named         `json:"areas"`
	Projects []Named         `json:"projects"`
	Actions  []Named         `json:"actions"` // open actions, for "blocked by"
	Contexts []wizard.Option `json:"contexts"`
	People   []string        `json:"people"`
}

func spaceOptions(st *core.State, user string) *SpaceOptions {
	o := &SpaceOptions{Areas: []Named{}, Projects: []Named{}, Actions: []Named{}, People: st.Space.People,
		Contexts: wizard.ContextOptions(core.Contexts(st, user))}
	st.Each(func(it *core.Item) {
		n := Named{ID: it.ID, Title: it.Title, State: it.State}
		switch {
		case it.Kind == core.KindArea && it.State == core.StActive:
			o.Areas = append(o.Areas, n)
		case it.Kind == core.KindProject && !finished(it.State):
			o.Projects = append(o.Projects, n)
		case it.Kind == core.KindAction && !finished(it.State):
			o.Actions = append(o.Actions, n)
		}
	})
	for _, l := range [][]Named{o.Areas, o.Projects, o.Actions} {
		sort.Slice(l, func(i, j int) bool { return strings.ToLower(l[i].Title)+l[i].ID < strings.ToLower(l[j].Title)+l[j].ID })
	}
	return o
}

// tokensFor resolves tokens for a viewer looking at one item.
func (s *Server) tokensFor(v *store.View, it *core.Item, lang string) tokens {
	return tokens{
		link: func(id string) (string, string, bool) {
			x, err := v.Find(id)
			if err != nil || x.State == core.StTrashed {
				return "", "", false
			}
			return x.Title, x.State, true
		},
		att: func(sha string) (string, string, bool) {
			if it != nil {
				for _, a := range it.Attachments {
					if a.SHA256 == sha {
						return a.Name, a.MIME, true
					}
				}
			}
			return "", "", false
		},
		user: func(id string) (string, bool) {
			if u := s.st.User(id); u != nil {
				return u.Name, true
			}
			return "", false
		},
		unavailable: s.t(lang, "item.unavailable"),
	}
}

func (s *Server) itemView(v *store.View, it *core.Item, lang, today string) (*ItemView, error) {
	st, err := v.State(it.Space)
	if err != nil {
		return nil, err
	}
	tk := s.tokensFor(v, it, lang)
	iv := &ItemView{Item: it, BodyHTML: renderMarkdown(it.Body, tk), Transitions: core.Transitions(it),
		SpaceName: st.Space.Name, ProjectTitle: title(st, it.Project), AreaTitle: title(st, it.Area), ParentTitle: title(st, it.Parent),
		Blockers: []Named{}, Dependents: []Named{}, Backlinks: []Named{}, Derived: []Named{}, Sources: []Named{},
		Tasks: len(wizard.TaskLines(it.Body)), Deferred: core.Deferred(it, today), Warnings: []core.Warning{},
		Templates: []core.Template{}, Options: spaceOptions(st, v.User())}
	iv.BodyEdit, iv.Refs = editForm(it.Body, tk)
	rel := s.Embed.SimilarTo(v, it, 5, func(x *core.Item) bool { return x.Kind != core.KindCapture })
	iv.Related, iv.RelatedState = s.related(v, rel.Hits), rel.State
	if iv.Transitions == nil {
		iv.Transitions = []string{}
	}
	for _, b := range it.BlockedBy {
		if x := st.Item(b); x != nil {
			iv.Blockers = append(iv.Blockers, Named{x.ID, x.Title, x.State, x.Owner, x.Kind})
		}
	}
	for _, d := range it.DerivedFrom {
		if x, err := v.Find(d); err == nil {
			iv.Sources = append(iv.Sources, Named{x.ID, x.Title, x.State, x.Owner, x.Kind})
		}
	}
	link := "[[" + it.ID
	var deps, back, derived []*core.Item
	st.Each(func(x *core.Item) {
		if x.ID == it.ID || x.State == core.StTrashed {
			return
		}
		for _, b := range x.BlockedBy {
			if b == it.ID {
				deps = append(deps, x)
			}
		}
		for _, d := range x.DerivedFrom {
			if d == it.ID {
				derived = append(derived, x)
			}
		}
		if strings.Contains(x.Body, link+"]]") || strings.Contains(x.Body, link+"|") {
			back = append(back, x)
		}
	})
	for _, l := range []*[]*core.Item{&deps, &back, &derived} {
		sort.Slice(*l, func(i, j int) bool { return (*l)[i].ID < (*l)[j].ID })
	}
	iv.Dependents, iv.Backlinks, iv.Derived = named(deps), named(back), named(derived)
	if iv.Comments, err = v.CommentCount(it.Space, it.ID); err != nil {
		return nil, err
	}
	if it.Kind == core.KindProject {
		r := core.Rollups(st, today)[it.ID]
		iv.Rollup = &r
	}
	for _, w := range core.Warnings(st, today) {
		if w.Item == it.ID {
			iv.Warnings = append(iv.Warnings, w)
		}
	}
	for i, t := range st.Space.Templates {
		if t.Kind == it.Kind {
			iv.Templates = append(iv.Templates, t)
			if t.Key == it.Template {
				iv.TemplateDef = &st.Space.Templates[i]
			}
		}
	}
	return iv, nil
}

// board builds the columns of SPEC §5.8 from the actions and captures of one
// space, optionally narrowed to an area, a project subtree or a field value.
type Column struct {
	Key   string  `json:"key"`
	Items []Brief `json:"items"`
}

func board(st *core.State, today string, now time.Time, area, project, field string) []Column {
	inSubtree := map[string]bool{}
	if project != "" {
		inSubtree[project] = true
		for changed := true; changed; {
			changed = false
			st.Each(func(it *core.Item) {
				if it.Kind == core.KindProject && inSubtree[it.Parent] && !inSubtree[it.ID] {
					inSubtree[it.ID], changed = true, true
				}
			})
		}
	}
	fk, fv, _ := strings.Cut(field, ":")
	cols := map[string][]*core.Item{}
	st.Each(func(it *core.Item) {
		if it.Kind != core.KindAction && it.Kind != core.KindCapture {
			return
		}
		if area != "" && it.Area != area || project != "" && !inSubtree[it.Project] || field != "" && it.Fields[fk] != fv {
			return
		}
		key := it.State
		switch {
		case it.State == core.StDropped || it.State == core.StTrashed:
			return
		case it.State == core.StDone:
			if now.Sub(it.Updated) > 14*24*time.Hour {
				return
			}
		case core.Deferred(it, today):
			key = "deferred"
		}
		cols[key] = append(cols[key], it)
	})
	var out []Column
	for _, key := range []string{core.StInbox, core.StNext, core.StPlanned, core.StWaiting, "deferred", core.StSomeday, core.StDone} {
		items := cols[key]
		if key == core.StNext {
			core.SortEngage(items)
		} else {
			sort.Slice(items, func(i, j int) bool {
				if key == core.StDone {
					return items[i].Updated.After(items[j].Updated)
				}
				return items[i].ID < items[j].ID
			})
		}
		out = append(out, Column{key, briefs(st, items, today, nil)})
	}
	return out
}

// Plan is the plan view of one project: its subtree with roll-ups, the
// actions and notes in it, and the date warnings that concern it (SPEC §6.4).
type Plan struct {
	Root     string         `json:"root"`
	Space    string         `json:"space"`
	Projects []Brief        `json:"projects"` // root first, then descendants
	Actions  []Brief        `json:"actions"`
	Notes    []Brief        `json:"notes"`
	Warnings []core.Warning `json:"warnings"`
	// Suggested are loose notes that might belong to the root project.
	Suggested []Brief `json:"suggested"`
}

func plan(st *core.State, root *core.Item, today string) *Plan {
	roll := core.Rollups(st, today)
	in := map[string]bool{root.ID: true}
	order := []*core.Item{root}
	for i := 0; i < len(order); i++ {
		var kids []*core.Item
		st.Each(func(it *core.Item) {
			if it.Kind == core.KindProject && it.Parent == order[i].ID && !in[it.ID] && it.State != core.StTrashed {
				kids = append(kids, it)
			}
		})
		sort.Slice(kids, func(a, b int) bool { return kids[a].ID < kids[b].ID })
		for _, k := range kids {
			in[k.ID] = true
		}
		order = append(order, kids...)
	}
	p := &Plan{Root: root.ID, Space: root.Space, Projects: briefs(st, order, today, roll), Warnings: []core.Warning{}, Suggested: []Brief{}}
	var actions, notes []*core.Item
	st.Each(func(it *core.Item) {
		if !in[it.Project] || it.State == core.StTrashed {
			return
		}
		switch it.Kind {
		case core.KindAction:
			actions = append(actions, it)
		case core.KindNote:
			notes = append(notes, it)
		}
	})
	sort.Slice(actions, func(i, j int) bool { return actions[i].ID < actions[j].ID })
	sort.Slice(notes, func(i, j int) bool { return notes[i].ID < notes[j].ID })
	p.Actions, p.Notes = briefs(st, actions, today, nil), briefs(st, notes, today, nil)
	mine := map[string]bool{}
	for _, a := range actions {
		mine[a.ID] = true
	}
	for _, w := range core.Warnings(st, today) {
		if in[w.Item] || mine[w.Item] {
			p.Warnings = append(p.Warnings, w)
		}
	}
	return p
}
