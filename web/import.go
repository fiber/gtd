package web

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/fiber/gtd/core"
	"github.com/fiber/gtd/store"
)

// importState returns what was imported into a space before: the path map
// (keyed by the SHA-1 of the path) and the last report.
func (s *Server) importState(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	space := r.URL.Query().Get("space")
	entries, err := v.ImportEntries(space)
	if err != nil {
		return err
	}
	report, err := v.ImportReport(space)
	if err != nil {
		return err
	}
	return reply(w, map[string]any{"entries": entries, "report": report})
}

// importNotes is the write side of `gtd import` (IMPORT.md): a marker op, or
// a batch of notes. The importer itself runs in the CLI, where the files are.
func (s *Server) importNotes(w http.ResponseWriter, r *http.Request, a actor) error {
	var in struct {
		Space  string             `json:"space"`
		Marker *core.ImportMarker `json:"marker"`
		Report json.RawMessage    `json:"report"`
		Notes  []store.ImportNote `json:"notes"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 16<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return fmt.Errorf("%w: request body: %v", core.ErrInvalid, err)
	}
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	if in.Marker != nil {
		if len(in.Notes) > 0 {
			return fmt.Errorf("%w: a marker or notes, not both", core.ErrInvalid)
		}
		return v.ImportMark(in.Space, *in.Marker, in.Report)
	}
	if len(in.Notes) == 0 || len(in.Notes) > 500 {
		return fmt.Errorf("%w: want 1 to 500 notes", core.ErrInvalid)
	}
	res, err := v.Import(in.Space, in.Notes)
	if err != nil {
		return err
	}
	return reply(w, res)
}

// calibration reports how similarity scores are distributed in one of the
// user's spaces (numbers only), for choosing thresholds.
func (s *Server) calibration(w http.ResponseWriter, r *http.Request, a actor) error {
	v, err := s.st.View(a.user)
	if err != nil {
		return err
	}
	space := r.URL.Query().Get("space")
	if space == "" {
		space = a.user
	}
	c, err := s.Embed.Calibrate(r.Context(), v, space)
	if err != nil {
		return err
	}
	return reply(w, c)
}
