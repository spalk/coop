// Package room implements coop rooms: storage and the rules the server
// enforces (credentials, phases, floor, human-first delivery, charter,
// resolution, pause, budget, close). See docs/protocol.md.
package room

import (
	"fmt"
	"net/http"
	"time"
)

// Phases.
const (
	PhaseBriefing    = "briefing"
	PhaseNegotiation = "negotiation"
	PhaseApproval    = "approval"
	PhaseClosed      = "closed"
)

// Pause kinds (negotiation only).
const (
	PauseHand      = "hand"
	PauseBudget    = "budget"
	PauseAmendment = "amendment"
)

// Actors and credential roles.
const (
	ActorAgent  = "agent"
	ActorHuman  = "human"
	ActorServer = "server"
	roleInvite  = "invite"
)

// Message types.
const (
	TypeSay        = "say"
	TypeAsk        = "ask"
	TypePropose    = "propose"
	TypeAgree      = "agree"
	TypeReject     = "reject"
	TypeDefer      = "defer"
	TypeResolution = "resolution"
	TypeAmendment  = "amendment"
)

// Server event types.
const (
	EventCreated            = "created"
	EventJoined             = "joined"
	EventClaimed            = "claimed"
	EventPhase              = "phase"
	EventReady              = "ready"
	EventUnready            = "unready"
	EventTurn               = "turn"
	EventPaused             = "paused"
	EventResumed            = "resumed"
	EventVote               = "vote"
	EventCharter            = "charter"
	EventAmendmentRejected  = "amendment_rejected"
	EventResolutionDeclined = "resolution_declined"
	EventResolutionVoided   = "resolution_voided"
	EventApproved           = "approved"
	EventResolutionRejected = "resolution_rejected"
	EventMandate            = "mandate"
	EventClosed             = "closed"
	EventRenamed            = "renamed"
	EventLinkIssued         = "link_issued"
)

// Close reasons; a human close is "closed_by_" + side label.
const (
	ReasonApproved = "approved"
	ReasonExpired  = "expired"
)

// Limits and defaults; see docs/protocol.md section 15.
const (
	MaxBody        = 8192
	MaxSummary     = 120
	MaxTopic       = 200
	MaxDescription = 4000
	MaxName        = 60
	MaxReason      = 500
	MinSides       = 2
	MaxSides       = 6
	MaxOpenAnon    = 500
)

// Quota is what a creator may ask for.
type Quota struct {
	DefaultTTL, MaxTTL       time.Duration
	DefaultBudget, MaxBudget int
}

// Quotas for anonymous creators and holders of a host key.
var (
	AnonQuota = Quota{24 * time.Hour, 72 * time.Hour, 20, 40}
	KeyQuota  = Quota{7 * 24 * time.Hour, 90 * 24 * time.Hour, 40, 500}
)

// Principal is an authenticated participant: one side of one room, as its
// agent or its human.
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

// ParseLabel converts a side label to its index; -1 if it is not a side of
// a room with n sides.
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

// Event is one entry of a room log.
type Event struct {
	Seq     int64          `json:"seq"`
	TS      time.Time      `json:"ts"`
	Type    string         `json:"type"`
	Side    string         `json:"side,omitempty"`
	Actor   string         `json:"actor"`
	To      string         `json:"to,omitempty"`
	Re      int64          `json:"re,omitempty"`
	Summary string         `json:"summary,omitempty"`
	Body    string         `json:"body,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
	Private bool           `json:"private,omitempty"`
	Hash    string         `json:"hash,omitempty"`
	// Trust is set per reader; see docs/protocol.md section 12.
	Trust string `json:"trust,omitempty"`
}

// IsMessage reports whether the event is a participant message.
func (e Event) IsMessage() bool { return isMessageType(e.Type) }

func isMessageType(t string) bool {
	switch t {
	case TypeSay, TypeAsk, TypePropose, TypeAgree, TypeReject, TypeDefer, TypeResolution, TypeAmendment:
		return true
	}
	return false
}

// SideInfo describes one side of a room.
type SideInfo struct {
	Label         string     `json:"label"`
	Name          string     `json:"name"`
	Claimed       bool       `json:"claimed"`
	TurnsUsed     int        `json:"turns_used"`
	ReadyAt       *time.Time `json:"ready_at,omitempty"`
	AgentJoinedAt *time.Time `json:"agent_joined_at,omitempty"`
	HumanJoinedAt *time.Time `json:"human_joined_at,omitempty"`
}

// Pause describes why agents are paused.
type Pause struct {
	Kind      string `json:"kind"`
	Side      string `json:"side,omitempty"`
	Actor     string `json:"actor,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Amendment int64  `json:"amendment,omitempty"`
}

// CharterItem is a condition in force.
type CharterItem struct {
	N          int       `json:"n"`
	Text       string    `json:"text"`
	Amendment  int64     `json:"amendment"`
	AcceptedAt time.Time `json:"accepted_at"`
}

// Charter is the current version of the conditions agreed by all humans.
type Charter struct {
	Version int           `json:"version"`
	Items   []CharterItem `json:"items"`
}

// ResolutionState is the pending resolution and who has agreed or approved.
type ResolutionState struct {
	Seq            int64    `json:"seq"`
	Side           string   `json:"side"`
	CharterVersion int      `json:"charter_version"`
	AgreedBy       []string `json:"agreed_by"`
	ApprovedBy     []string `json:"approved_by"`
}

// AmendmentState is the pending amendment and its votes.
type AmendmentState struct {
	Seq      int64             `json:"seq"`
	Side     string            `json:"side"`
	Text     string            `json:"text"`
	Replaces int               `json:"replaces,omitempty"`
	Votes    map[string]string `json:"votes"`
}

// Viewer identifies who a status was rendered for; Mandate is the private
// note of the viewer's side.
type Viewer struct {
	Side    string `json:"side"`
	Name    string `json:"name"`
	Actor   string `json:"actor"`
	Mandate string `json:"mandate"`
}

// Status is the current state of a room as seen by a participant.
type Status struct {
	ID          string           `json:"id"`
	Topic       string           `json:"topic"`
	Description string           `json:"description,omitempty"`
	Public      bool             `json:"public"`
	Phase       string           `json:"phase"`
	Pause       *Pause           `json:"pause,omitempty"`
	Floor       string           `json:"floor,omitempty"`
	TurnBudget  int              `json:"turn_budget"`
	Charter     Charter          `json:"charter"`
	Resolution  *ResolutionState `json:"resolution,omitempty"`
	Amendment   *AmendmentState  `json:"amendment,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	ExpiresAt   time.Time        `json:"expires_at"`
	ClosedAt    *time.Time       `json:"closed_at,omitempty"`
	CloseReason string           `json:"close_reason,omitempty"`
	CloseNote   string           `json:"close_note,omitempty"`
	Sides       []SideInfo       `json:"sides"`
	LastSeq     int64            `json:"last_seq"`
	You         *Viewer          `json:"you,omitempty"`
}

// Error is a rule violation or a bad request, mapped to an HTTP status.
// Unread carries the human events an agent has not acknowledged.
type Error struct {
	Status  int     `json:"-"`
	Code    string  `json:"error"`
	Message string  `json:"message"`
	Unread  []Event `json:"unread,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errorf(status int, code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

func badRequest(format string, args ...any) *Error {
	return errorf(http.StatusBadRequest, "bad_request", format, args...)
}

func wrongPhase(phase string) *Error {
	return errorf(http.StatusConflict, "wrong_phase", "not allowed in the %s phase", phase)
}

// Common errors.
var (
	ErrNotFound     = &Error{Status: http.StatusNotFound, Code: "not_found", Message: "no such room"}
	ErrUnauthorized = &Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "missing or invalid token"}
	ErrHostKey      = &Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "invalid host key"}
	ErrClosed       = &Error{Status: http.StatusConflict, Code: "room_closed", Message: "the room is closed"}
	ErrHumansOnly   = &Error{Status: http.StatusForbidden, Code: "humans_only", Message: "only a human can do this"}
	ErrAgentsOnly   = &Error{Status: http.StatusForbidden, Code: "agents_only", Message: "only an agent can do this"}
	ErrInvite       = &Error{Status: http.StatusNotFound, Code: "invalid_invitation", Message: "the invitation is unknown, used or replaced"}
)
