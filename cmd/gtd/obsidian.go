package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fiber/gtd/core"
	"github.com/fiber/gtd/importer"
	"github.com/fiber/gtd/store"
)

// importObsidian reads a vault here, where the files are, and writes the
// notes through the server as the acting user (IMPORT.md).
func importObsidian(g globals, args []string, stdout io.Writer) error {
	space, dry := "", false
	args, err := flags("import obsidian", args, func(fs *flag.FlagSet) {
		fs.StringVar(&space, "space", "", "")
		fs.BoolVar(&dry, "dry-run", false, "")
	})
	if err != nil {
		return err
	}
	if err := want("import obsidian", args, 1, "[-space id] [-dry-run] <vault directory>"); err != nil {
		return err
	}
	root := args[0]
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return fmt.Errorf("gtd import obsidian: %s is not a directory", root)
	}
	start := time.Now()
	vault, err := importer.Read(os.DirFS(root))
	if err != nil {
		return fmt.Errorf("gtd import obsidian: reading %s: %w", root, err)
	}

	c, err := connect(g, false)
	if err != nil {
		return err
	}
	defer c.close()
	if space == "" {
		space = g.user // the personal space
	}
	var state struct {
		Entries map[string]store.ImportEntry `json:"entries"`
	}
	if err := c.do("GET", "/api/v1/import?space="+space, nil, &state); err != nil {
		return err
	}
	entry := func(path string) (store.ImportEntry, bool) {
		sum := sha1.Sum([]byte(path))
		e, ok := state.Entries[hex.EncodeToString(sum[:])]
		return e, ok
	}
	vault.AssignIDs(space, func(path string) string { e, _ := entry(path); return e.ID })

	rep := &vault.Report
	if !dry {
		if err := c.do("POST", "/api/v1/import", map[string]any{"space": space,
			"marker": core.ImportMarker{Source: "obsidian", Phase: "begin", Notes: len(vault.Notes)}}, nil); err != nil {
			return err
		}
	}
	vault.Resolve(func(file string) (string, error) {
		if dry {
			return vault.Hash256(file)
		}
		return c.upload(filepath.Join(root, filepath.FromSlash(file)))
	})

	var batch []store.ImportNote
	size := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		var res store.ImportResult
		if err := c.do("POST", "/api/v1/import", map[string]any{"space": space, "notes": batch}, &res); err != nil {
			return err
		}
		rep.Imported += res.Created
		rep.Updated += res.Updated
		rep.Unchanged += res.Unchanged
		batch, size = nil, 0
		return nil
	}
	for _, n := range vault.Notes {
		if dry {
			switch e, ok := entry(n.Path); {
			case !ok:
				rep.Imported++
			case e.Hash == n.Hash:
				rep.Unchanged++
			default:
				rep.Updated++
			}
			continue
		}
		batch = append(batch, store.ImportNote{Path: n.Path, Hash: n.Hash, Item: n.Item()})
		if size += 2*len(n.Original) + 1024; len(batch) == 500 || size > 4<<20 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if !dry {
		data, _ := json.Marshal(rep)
		if err := c.do("POST", "/api/v1/import", map[string]any{"space": space, "report": json.RawMessage(data),
			"marker": core.ImportMarker{Source: "obsidian", Phase: "end", Notes: rep.Imported + rep.Updated}}, nil); err != nil {
			return err
		}
	}
	printReport(stdout, rep, space, dry, time.Since(start))
	return nil
}

// upload stores a file as a blob and returns its hash.
func (c *client) upload(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filepath.Base(file))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", err
	}
	mw.Close()
	resp, err := c.send("POST", "/api/v1/attachments", mw.FormDataContentType(), &buf)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var att core.Attachment
	if err := json.NewDecoder(resp.Body).Decode(&att); err != nil {
		return "", err
	}
	return att.SHA256, nil
}

func printReport(w io.Writer, r *importer.Report, space string, dry bool, took time.Duration) {
	mode := "imported into"
	if dry {
		mode = "dry run for"
	}
	fmt.Fprintf(w, "%s space %q in %s\n", mode, space, took.Round(time.Millisecond))
	fmt.Fprintf(w, "notes        %d found: %d new, %d updated, %d unchanged, %d empty (skipped)\n", r.Found, r.Imported, r.Updated, r.Unchanged, r.Empty)
	fmt.Fprintf(w, "duplicates   %d groups of notes with identical content\n", len(r.DuplicateGroups))
	fmt.Fprintf(w, "attachments  %d referenced: %d stored, %d missing, %d bytes saved by deduplication\n", r.AttachmentsReferenced, r.AttachmentsIngested, r.AttachmentsMissing, r.BytesDeduplicated)
	fmt.Fprintf(w, "links        %d resolved (%d anchors dropped), %d ambiguous, %d missing\n", r.LinksResolved, r.AnchorsDropped, r.LinksAmbiguous, r.LinksMissing)
	fmt.Fprintf(w, "open todos   %d lines\n", r.Todos)
	section := func(title string, list []string) {
		if len(list) == 0 {
			return
		}
		fmt.Fprintf(w, "\n%s (%d):\n  %s\n", title, len(list), strings.Join(list, "\n  "))
	}
	section("frontmatter that is not YAML, kept in the body", r.BadMetadata)
	section("links that were kept as they are (first 50)", r.Unresolved)
	section("files no note refers to, not imported", r.Unreferenced)
	section("largest notes", r.Largest)
	for _, g := range r.DuplicateGroups {
		section("identical", g)
	}
}
