// Package room implements coop rooms: storage and the rules the server
// enforces (floor, pause, turn budget, approval). See docs/protocol.md.
package room

import (
	"fmt"
	"net/http"
	"time"
)

// Room states.
const (
	StateOpen   = "open"
	StatePaused = "paused"
	StateClosed = "closed"
)

// Actors.
const (
	ActorAgent  = "agent"
	ActorHuman  = "human"
	ActorServer = "server"
)

// Message types a participant may post.
const (
	TypeSay        = "say"
	TypeAsk        = "ask"
	TypePropose    = "propose"
	TypeAgree      = "agree"
	TypeResolution = "resolution"
)

// Server event types.
const (
	EventCreated       = "created"
	EventJoined        = "joined"
	EventTurn          = "turn"
	EventPaused        = "paused"
	EventResumed       = "resumed"
	EventApproved      = "approved"
	EventClosed        = "closed"
	EventTokenReissued = "token_reissued"
	EventRenamed       = "renamed"
)

// Reasons for pausing and closing.
const (
	ReasonBudget   = "budget"
	ReasonApproved = "approved"
	ReasonExpired  = "expired"
)

// Limits and defaults; see docs/protocol.md section 9.
const (
	MaxBody          = 8192
	MaxTopic         = 200
	MaxDescription   = 4000
	MaxName          = 60
	MaxReason        = 500
	MinSides         = 2
	MaxSides         = 6
	DefaultBudget    = 40
	MaxBudget        = 500
	DefaultTTL       = 7 * 24 * time.Hour
	MinTTL           = time.Hour
	MaxTTL           = 90 * 24 * time.Hour
	DefaultRetention = 30 * 24 * time.Hour
)

// Principal is an authenticated participant: one side of one room, acting as
// its agent or its human.
type Principal struct {
	RoomID string
	Side   int
	Actor  string
}

// Label returns the side label of the principal ("A", "B", ...).
func (p Principal) Label() string { return Label(p.Side) }

// Label converts a side index to its label.
func Label(i int) string {
	if i < 0 {
		return ""
	}
	return string(rune('A' + i))
}

// ParseLabel converts a side label to its index; it returns -1 if the label
// is not a valid side of a room with n sides.
func ParseLabel(s string, n int) int {
	if len(s) != 1 {
		return -1
	}
	c := s[0]
	if c >= 'a' && c <= 'z' {
		c -= 'a' - 'A'
	}
	i := int(c) - 'A'
	if i < 0 || i >= n {
		return -1
	}
	return i
}

// Event is one entry of a room log: a message or a server event.
type Event struct {
	Seq   int64          `json:"seq"`
	TS    time.Time      `json:"ts"`
	Type  string         `json:"type"`
	Side  string         `json:"side,omitempty"`
	Actor string         `json:"actor"`
	To    string         `json:"to,omitempty"`
	Body  string         `json:"body,omitempty"`
	Data  map[string]any `json:"data,omitempty"`
	// Trust is set per viewer for agents; see docs/protocol.md section 8.
	Trust string `json:"trust,omitempty"`
}

// IsMessage reports whether the event is a participant message.
func (e Event) IsMessage() bool {
	switch e.Type {
	case TypeSay, TypeAsk, TypePropose, TypeAgree, TypeResolution:
		return true
	}
	return false
}

// SideInfo describes one side of a room.
type SideInfo struct {
	Label         string     `json:"label"`
	Name          string     `json:"name"`
	AgentJoinedAt *time.Time `json:"agent_joined_at,omitempty"`
	HumanJoinedAt *time.Time `json:"human_joined_at,omitempty"`
	ApprovedAt    *time.Time `json:"approved_at,omitempty"`
}

// Pause describes who paused a room and why.
type Pause struct {
	Side   string `json:"side,omitempty"`
	Actor  string `json:"actor"`
	Reason string `json:"reason,omitempty"`
}

// Viewer identifies who a status was rendered for.
type Viewer struct {
	Side  string `json:"side"`
	Name  string `json:"name"`
	Actor string `json:"actor"`
}

// Status is the current state of a room as seen by a participant.
type Status struct {
	ID                string     `json:"id"`
	Topic             string     `json:"topic"`
	Description       string     `json:"description,omitempty"`
	State             string     `json:"state"`
	Floor             string     `json:"floor,omitempty"`
	TurnBudget        int        `json:"turn_budget"`
	TurnsUsed         int        `json:"turns_used"`
	CreatedAt         time.Time  `json:"created_at"`
	ExpiresAt         time.Time  `json:"expires_at"`
	ClosedAt          *time.Time `json:"closed_at,omitempty"`
	CloseReason       string     `json:"close_reason,omitempty"`
	Pause             *Pause     `json:"pause,omitempty"`
	PendingResolution int64      `json:"pending_resolution,omitempty"`
	Sides             []SideInfo `json:"sides"`
	LastSeq           int64      `json:"last_seq"`
	You               *Viewer    `json:"you,omitempty"`
}

// Error is a rule violation or a bad request, mapped to an HTTP status.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"error"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errorf(status int, code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// Common errors.
var (
	ErrNotFound     = &Error{http.StatusNotFound, "not_found", "no such room"}
	ErrUnauthorized = &Error{http.StatusUnauthorized, "unauthorized", "missing or invalid token"}
	ErrHostKey      = &Error{http.StatusUnauthorized, "unauthorized", "missing or invalid host key"}
	ErrClosed       = &Error{http.StatusConflict, "room_closed", "the room is closed"}
	ErrPaused       = &Error{http.StatusLocked, "paused", "the room is paused; wait for a resumed event"}
	ErrHumansOnly   = &Error{http.StatusForbidden, "humans_only", "only a human can do this"}
)
