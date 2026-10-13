// Package server is the HTTP front of coop: the JSON API, long-poll and SSE
// delivery, the pages and /llms.txt.
package server

import (
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dntx/coop/internal/room"
	"github.com/dntx/coop/web"
)

// Limits of the HTTP layer; see docs/protocol.md section 15.
const (
	maxRequestBody   = 64 << 10
	defaultWait      = 300 * time.Second
	maxWait          = 540 * time.Second
	maxStreams       = 4
	agentPerMinute   = 20
	humanPerMinute   = 30
	anonRoomsPerHour = 10
	sseHeartbeat     = 20 * time.Second
	cookiePrefix     = "coop_"
	cookieAge        = 90 * 24 * time.Hour
)

// Server serves one coop instance.
type Server struct {
	svc      *room.Service
	baseURL  string // public URL without trailing slash
	basePath string // path part of baseURL: "" or "/prefix"
	secure   bool
	pages    *template.Template
	mux      *http.ServeMux
	limiter  *limiter
	streams  *streams
	Version  string
}

// New returns a server for svc reachable at baseURL.
func New(svc *room.Service, baseURL string) (*Server, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("base URL must be absolute, e.g. https://coop.example.org")
	}
	pages, err := template.New("").Funcs(funcs).ParseFS(web.FS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	s := &Server{
		svc:      svc,
		baseURL:  u.String(),
		basePath: u.Path,
		secure:   u.Scheme == "https",
		pages:    pages,
		mux:      http.NewServeMux(),
		limiter:  newLimiter(),
		streams:  &streams{n: map[string]int{}},
		Version:  "dev",
	}
	s.routes()
	return s, nil
}

// ServeHTTP strips the base path and dispatches.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if s.basePath == "" {
		s.mux.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == s.basePath {
		http.Redirect(w, r, s.basePath+"/", http.StatusMovedPermanently)
		return
	}
	http.StripPrefix(s.basePath, s.mux).ServeHTTP(w, r)
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /{$}", s.home)
	m.HandleFunc("POST /new", s.createForm)
	m.HandleFunc("GET /llms.txt", s.llms)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })
	m.HandleFunc("GET /static/", s.static)
	m.HandleFunc("GET /a/{token}", s.agentLink)
	m.HandleFunc("GET /h/{token}", s.humanLink)
	m.HandleFunc("POST /h/{token}", s.humanEnter)
	m.HandleFunc("GET /i/{token}", s.invitation)
	m.HandleFunc("POST /i/{token}", s.claim)
	m.HandleFunc("GET /r/{id}", s.roomPage)
	m.HandleFunc("GET /p/{id}", s.publicPage)
	m.HandleFunc("GET /p/{id}/minutes.md", s.published(s.minutesMD))
	m.HandleFunc("GET /p/{id}/minutes.json", s.published(s.minutesJSON))

	m.HandleFunc("POST /api/rooms", s.createRoom)
	m.HandleFunc("GET /api/rooms/{id}", s.withRoom(s.getRoom))
	m.HandleFunc("GET /api/rooms/{id}/messages", s.withRoom(s.listEvents))
	m.HandleFunc("POST /api/rooms/{id}/messages", s.withRoom(s.postMessage))
	m.HandleFunc("GET /api/rooms/{id}/wait", s.withRoom(s.wait))
	m.HandleFunc("GET /api/rooms/{id}/events", s.withRoom(s.sse))
	m.HandleFunc("GET /api/rooms/{id}/protocol", s.withRoom(s.protocol))
	m.HandleFunc("POST /api/rooms/{id}/raise", s.withRoom(s.raise))
	m.HandleFunc("POST /api/rooms/{id}/resume", s.withRoom(s.resume))
	m.HandleFunc("POST /api/rooms/{id}/ready", s.withRoom(s.ready))
	m.HandleFunc("POST /api/rooms/{id}/unready", s.withRoom(s.unready))
	m.HandleFunc("POST /api/rooms/{id}/amendments/{seq}/vote", s.withRoom(s.vote))
	m.HandleFunc("POST /api/rooms/{id}/approve", s.withRoom(s.approve))
	m.HandleFunc("POST /api/rooms/{id}/reject", s.withRoom(s.reject))
	m.HandleFunc("POST /api/rooms/{id}/finish", s.withRoom(s.finish))
	m.HandleFunc("POST /api/rooms/{id}/close", s.withRoom(s.closeRoom))
	m.HandleFunc("PUT /api/rooms/{id}/mandate", s.withRoom(s.mandate))
	m.HandleFunc("POST /api/rooms/{id}/name", s.withRoom(s.rename))
	m.HandleFunc("POST /api/rooms/{id}/publish", s.withRoom(s.publish))
	m.HandleFunc("POST /api/rooms/{id}/unpublish", s.withRoom(s.unpublish))
	m.HandleFunc("POST /api/rooms/{id}/agent-link", s.withRoom(s.issueAgentLink))
	m.HandleFunc("POST /api/rooms/{id}/sides/{side}/invitation", s.withRoom(s.issueInvitation))
	m.HandleFunc("POST /api/rooms/{id}/sides/{side}/links", s.withRoom(s.issueSideLinks))
	m.HandleFunc("GET /api/rooms/{id}/minutes.md", s.withRoom(s.minutesMD))
	m.HandleFunc("GET /api/rooms/{id}/minutes.json", s.withRoom(s.minutesJSON))
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, &room.Error{Status: http.StatusNotFound, Code: "not_found", Message: "no such endpoint; see /llms.txt"})
	})
}

// url returns an absolute public URL for a path inside the instance.
func (s *Server) url(path string) string { return s.baseURL + path }

// clientIP is the address of the client. Behind a reverse proxy on the same
// host the last X-Forwarded-For entry (added by the proxy) is used.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	return host
}

// Authentication.

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// principal authenticates the request for a room: a token in the
// Authorization header (agent or human, by its role), or a human token kept
// in the room's cookie.
func (s *Server) principal(r *http.Request, roomID string) (room.Principal, bool, error) {
	tok, viaCookie := bearer(r), false
	if tok == "" {
		c, err := r.Cookie(cookiePrefix + roomID)
		if err != nil {
			return room.Principal{}, false, room.ErrUnauthorized
		}
		tok, viaCookie = c.Value, true
	}
	p, err := s.svc.Authenticate(tok)
	if err != nil {
		return p, false, err
	}
	if p.RoomID != roomID || viaCookie && p.Actor != room.ActorHuman {
		return room.Principal{}, false, room.ErrUnauthorized
	}
	return p, viaCookie, s.svc.MarkJoined(p)
}

type roomHandler func(w http.ResponseWriter, r *http.Request, p room.Principal)

func (s *Server) withRoom(h roomHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, viaCookie, err := s.principal(r, r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		if r.Method != http.MethodGet && viaCookie && !isJSON(r) {
			// Browsers cannot send JSON cross-site without a preflight: this,
			// with SameSite cookies, is the CSRF defence.
			writeError(w, &room.Error{Status: http.StatusUnsupportedMediaType, Code: "bad_request",
				Message: "Content-Type must be application/json"})
			return
		}
		h(w, r, p)
	}
}

// published serves a read-only handler to anyone, for published rooms only.
func (s *Server) published(h roomHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := s.svc.Published(id); err != nil {
			writeError(w, err)
			return
		}
		h(w, r, room.Principal{RoomID: id, Side: -1})
	}
}

func isJSON(r *http.Request) bool {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return mt == "application/json"
}

// readJSON decodes an optional JSON body into v.
func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return &room.Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: "request body is too large"}
		}
		return &room.Error{Status: http.StatusBadRequest, Code: "bad_request", Message: "invalid JSON: " + err.Error()}
	}
	return nil
}

// Responses.

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

type errorBody struct {
	*room.Error
	Room *room.Status `json:"room,omitempty"`
}

func writeError(w http.ResponseWriter, err error) { writeErrorWithRoom(w, err, nil) }

func writeErrorWithRoom(w http.ResponseWriter, err error, st *room.Status) {
	var e *room.Error
	if !errors.As(err, &e) {
		log.Printf("internal error: %v", err)
		e = &room.Error{Status: http.StatusInternalServerError, Code: "internal", Message: "internal error"}
	}
	writeJSON(w, e.Status, errorBody{Error: e, Room: st})
}

// Rate limiting, per key.

type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLimiter() *limiter { return &limiter{hits: map[string][]time.Time{}} }

// allow records a hit and reports whether it fits in perMinute; when it does
// not, it returns how long to wait.
func (l *limiter) allow(key string, perMinute int, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := now.Add(-time.Minute)
	h := l.hits[key]
	i := 0
	for i < len(h) && h[i].Before(cut) {
		i++
	}
	h = h[i:]
	if len(h) >= perMinute {
		l.hits[key] = h
		return false, h[0].Sub(cut)
	}
	l.hits[key] = append(h, now)
	if len(l.hits) > 10000 {
		for k, v := range l.hits {
			if len(v) == 0 || v[len(v)-1].Before(cut) {
				delete(l.hits, k)
			}
		}
	}
	return true, 0
}

// streams counts open wait/SSE connections per side and role.
type streams struct {
	mu sync.Mutex
	n  map[string]int
}

func (s *streams) acquire(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n[key] >= maxStreams {
		return false
	}
	s.n[key]++
	return true
}

func (s *streams) release(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n[key]--; s.n[key] <= 0 {
		delete(s.n, key)
	}
}
