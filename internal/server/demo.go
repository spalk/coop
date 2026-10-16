package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"sort"

	"github.com/spalk/coop/web"
)

// demo is a recorded conversation shown on the demo pages: real tasks,
// retold without internal names. See web/demo/*.json.
type demo struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
	Lead  string `json:"lead"`
	Sides []struct {
		Label string `json:"label"`
		Name  string `json:"name"`
		Agent string `json:"agent"`
	} `json:"sides"`
	Steps    []demoStep `json:"steps"`
	Decision string     `json:"decision"`
}

// demoStep is a message, or with Event set a line about the room.
type demoStep struct {
	Side    string `json:"side"`
	Actor   string `json:"actor"`
	Type    string `json:"type"`
	Re      int    `json:"re"`
	Summary string `json:"summary"`
	Body    string `json:"body"`
	Event   string `json:"event"`
}

// SideName returns the name of side label.
func (d demo) SideName(label string) string {
	for _, s := range d.Sides {
		if s.Label == label {
			return s.Name
		}
	}
	return "Side " + label
}

// assetVersion is a short hash of the static files, appended to their URLs
// so that browsers fetch new versions after a deploy.
func assetVersion() string {
	h := sha256.New()
	fs.WalkDir(web.FS, "static", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := fs.ReadFile(web.FS, p)
			h.Write([]byte(p))
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:10]
}

func loadDemos() ([]demo, error) {
	names, err := fs.Glob(web.FS, "demo/*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []demo
	for _, n := range names {
		b, err := fs.ReadFile(web.FS, n)
		if err != nil {
			return nil, err
		}
		var d demo
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *Server) demoList(w http.ResponseWriter, r *http.Request) {
	s.page(w, http.StatusOK, "demos.html", pageData{Demos: s.demos})
}

func (s *Server) demoPage(w http.ResponseWriter, r *http.Request) {
	slug := path.Base(r.PathValue("slug"))
	for i := range s.demos {
		if s.demos[i].Slug == slug {
			s.page(w, http.StatusOK, "demo.html", pageData{Demos: s.demos, Demo: &s.demos[i]})
			return
		}
	}
	s.page(w, http.StatusNotFound, "invalid.html", pageData{})
}
