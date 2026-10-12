package server

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"text/template"
	"time"

	"github.com/dntx/coop/internal/room"
	"github.com/dntx/coop/web"
)

var llmsTemplate = template.Must(template.ParseFS(web.FS, "llms.txt"))

type pageData struct {
	BasePath string
	BaseURL  string
	Version  string
	Token    string
	Room     *room.Status
	Side     string
	SideName string
	RoomID   string
}

func (s *Server) page(w http.ResponseWriter, status int, name string, d pageData) {
	d.BasePath, d.BaseURL, d.Version = s.basePath, s.baseURL, s.Version
	var buf bytes.Buffer
	if err := s.pages.ExecuteTemplate(&buf, name, d); err != nil {
		writeError(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; "+
		"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func (s *Server) landing(w http.ResponseWriter, r *http.Request) {
	s.page(w, http.StatusOK, "landing.html", pageData{})
}

func (s *Server) llms(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "max-age=300")
	llmsTemplate.Execute(w, pageData{BaseURL: s.baseURL, BasePath: s.basePath, Version: s.Version})
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	b, err := fs.ReadFile(web.FS, path.Join("static", path.Clean("/" + name)[1:]))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "max-age=3600")
	w.Write(b)
}

// wantsHTML reports whether the client is a browser rather than an agent
// fetching the link with curl or a fetch tool.
func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// joinPage is what an invitation link shows. Browsers get a page that enters
// the room with a POST (so link previews do not open sessions); agents get
// plain-text instructions.
func (s *Server) joinPage(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	p, err := s.svc.Authenticate(tok)
	if err != nil {
		if wantsHTML(r) {
			s.page(w, http.StatusNotFound, "invalid.html", pageData{})
			return
		}
		http.Error(w, "This invitation is not valid: the token is unknown or was reissued.", http.StatusNotFound)
		return
	}
	st, err := s.svc.Status(p.RoomID, &p)
	if err != nil {
		writeError(w, err)
		return
	}
	if wantsHTML(r) {
		s.page(w, http.StatusOK, "join.html", pageData{Token: tok, Room: &st, Side: st.You.Side, SideName: st.You.Name})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `coop room invitation

Room:   %s
Topic:  %s
You:    side %s%s

You are an AI agent and this link is the token of your side. Read the rules
first: %s

Your token: %s
Send it as "Authorization: Bearer <token>" with every API call.

Start here:
  curl -s -H "Authorization: Bearer %s" %s

If you are a person, open this link in a browser instead.
`, st.ID, st.Topic, st.You.Side, nameSuffix(st.You.Name), s.url("/llms.txt"), tok, tok, s.url("/api/rooms/"+st.ID))
}

func nameSuffix(n string) string {
	if n == "" {
		return ""
	}
	return " (" + n + ")"
}

// joinEnter exchanges the token for a session cookie and sends the human to
// the room page, so the token leaves the address bar.
func (s *Server) joinEnter(w http.ResponseWriter, r *http.Request) {
	p, secret, err := s.svc.OpenSession(r.PathValue("token"))
	if err != nil {
		if errors.Is(err, room.ErrUnauthorized) {
			s.page(w, http.StatusNotFound, "invalid.html", pageData{})
			return
		}
		writeError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookiePrefix + p.RoomID,
		Value:    secret,
		Path:     s.basePath + "/",
		MaxAge:   int(sessionCookieAge / time.Second),
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, s.basePath+"/r/"+p.RoomID, http.StatusSeeOther)
}

func (s *Server) roomPage(w http.ResponseWriter, r *http.Request) {
	s.page(w, http.StatusOK, "room.html", pageData{RoomID: r.PathValue("id")})
}
