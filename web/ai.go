package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fiber/gtd/ai"
	"github.com/fiber/gtd/core"
)

// AI on demand (DECISIONS: "ad-hoc AI buttons"). Nothing here runs by
// itself: every call is a user pressing a button on one item, and what
// comes back is a proposal that changes nothing until the user accepts it
// (invariant 2).
//
// aiFor is the one gate between a space's text and a model (Q-11): every
// function that sends text to the backend gets its provider from here.

// aiFor returns the generation backend for text of a space, or why there is
// none: no backend configured, or the space's policy does not allow it.
func (s *Server) aiFor(sp *core.Space) (ai.Provider, error) {
	if s.AI == nil {
		return nil, &keyError{http.StatusBadRequest, "ai.off", ""}
	}
	if !sp.AIAllows(s.AI.Local()) {
		return nil, &keyError{http.StatusForbidden, "ai.policy", ""}
	}
	return s.AI, nil
}

// taskLines proposes task lines for an item's text, or, with accept, appends
// the ones the user ticked to its body.
//
//	POST /items/{id}/tasklines {}                 → {lines: [{who, text, quote}]}
//	POST /items/{id}/tasklines {accept: [...]}    → {added: n}
func (s *Server) taskLines(w http.ResponseWriter, r *http.Request, a actor) error {
	var in struct {
		Accept []ai.TaskLine `json:"accept"`
	}
	if err := body(r, &in); err != nil {
		return err
	}
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	it, err := v.Find(r.PathValue("id"))
	if err != nil {
		return err
	}
	lang := s.lang(r, a.user)
	if in.Accept != nil {
		// The user's decision: these lines go into the note. They come from
		// the user's own request, so no model is involved here.
		if len(in.Accept) == 0 || len(in.Accept) > 100 {
			return fmt.Errorf("%w: want 1 to 100 lines to add", core.ErrInvalid)
		}
		for _, l := range in.Accept {
			if strings.TrimSpace(l.Text) == "" || len(l.Text) > 500 || len(l.Who) > 80 || strings.ContainsAny(l.Who, "\n[]") {
				return fmt.Errorf("%w: task line %q", core.ErrInvalid, l.Text)
			}
		}
		lines := ai.Format(in.Accept, s.t(lang, "tasklines.me"), s.t(lang, "tasklines.open"))
		next := strings.TrimRight(it.Body, "\n")
		if next != "" {
			next += "\n\n"
		}
		next += "## " + s.t(lang, "tasklines.heading") + "\n\n" + lines
		op := core.Op{Via: "web", Type: core.OpPatch, Item: it.ID, BaseRev: it.Rev,
			Changes: []core.FieldChange{{Field: "body", Old: jsonOf(it.Body), New: jsonOf(next)}}}
		if _, err := v.Apply(it.Space, op); err != nil {
			return err
		}
		return reply(w, map[string]int{"added": len(in.Accept)})
	}
	st, err := v.State(it.Space)
	if err != nil {
		return err
	}
	p, err := s.aiFor(st.Space)
	if err != nil {
		return err
	}
	if strings.TrimSpace(it.Body) == "" {
		return reply(w, map[string]any{"lines": []ai.TaskLine{}})
	}
	u := s.st.User(a.user)
	names := append([]string{}, u.Aliases...)
	if u.Name != "" {
		names = append(names, u.Name)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	lines, err := ai.SuggestTaskLines(ctx, p, it.Title, it.Body, names)
	switch {
	case errors.Is(err, ai.ErrUnavailable):
		return &keyError{http.StatusBadGateway, "ai.unavailable", ""}
	case errors.Is(err, ai.ErrRefused):
		return &keyError{http.StatusBadGateway, "ai.refused", ""}
	case err != nil:
		return err
	}
	if lines == nil {
		lines = []ai.TaskLine{}
	}
	return reply(w, map[string]any{"lines": lines})
}

// putAIPolicy sets which generation backends may read a space: any member
// may tighten or loosen it.
func (s *Server) putAIPolicy(w http.ResponseWriter, r *http.Request, a actor) error {
	var in struct {
		Policy string `json:"policy"`
	}
	if err := body(r, &in); err != nil {
		return err
	}
	if err := core.ValidAIPolicy(in.Policy); err != nil {
		return err
	}
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	if _, err := v.State(id); err != nil { // members only
		return err
	}
	if err := s.st.UpdateSpace(id, func(sp *core.Space) error { sp.AI = in.Policy; return nil }); err != nil {
		return err
	}
	return reply(w, map[string]string{"policy": in.Policy})
}
