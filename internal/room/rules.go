package room

import (
	"database/sql"
	"net/http"
	"strings"
	"unicode/utf8"
)

// mutate loads the room in a write transaction, closes it if its lifetime
// has ended (committing that and returning ErrClosed), and otherwise runs fn
// and saves the room.
func (s *Service) mutate(roomID string, fn func(tx *sql.Tx, r *roomRow, now int64) error) error {
	expired := false
	err := s.write(roomID, func(tx *sql.Tx) error {
		r, err := loadRoom(tx, roomID)
		if err != nil {
			return err
		}
		now := ms(s.now())
		wasOpen := r.Phase != PhaseClosed
		if err := expireIfDue(tx, r, now); err != nil {
			return err
		}
		if wasOpen && r.Phase == PhaseClosed {
			expired = true
			return nil
		}
		if err := fn(tx, r, now); err != nil {
			return err
		}
		return saveRoom(tx, r)
	})
	if err == nil && expired {
		return ErrClosed
	}
	return err
}

// Message is what a participant posts.
type Message struct {
	Type     string `json:"type"`
	Body     string `json:"body"`
	Summary  string `json:"summary"`
	Re       int64  `json:"re"`
	To       string `json:"to"`
	Floor    string `json:"floor"` // agents: "pass" (default) or "keep"
	Private  bool   `json:"private"`
	Ack      *int64 `json:"ack"`      // agents: highest seq read
	Replaces int    `json:"replaces"` // amendments: charter item replaced
}

func (m *Message) validate(actor string) error {
	agentTypes := map[string]bool{TypeSay: true, TypeAsk: true, TypePropose: true, TypeAgree: true,
		TypeReject: true, TypeDefer: true, TypeResolution: true}
	humanTypes := map[string]bool{TypeSay: true, TypeAmendment: true}
	switch {
	case m.Type == "":
		return badRequest("type is required")
	case actor == ActorAgent && !agentTypes[m.Type]:
		return badRequest("agents post say, ask, propose, agree, reject, defer or resolution; not %q", m.Type)
	case actor == ActorHuman && !humanTypes[m.Type]:
		return badRequest("humans post say or amendment; not %q", m.Type)
	}
	if strings.TrimSpace(m.Body) == "" {
		return badRequest("body is required")
	}
	if len(m.Body) > MaxBody {
		return errorf(http.StatusRequestEntityTooLarge, "too_large", "body is %d bytes; the limit is %d", len(m.Body), MaxBody)
	}
	if !utf8.ValidString(m.Body) || !utf8.ValidString(m.Summary) {
		return badRequest("text must be UTF-8")
	}
	m.Summary = strings.TrimSpace(m.Summary)
	if utf8.RuneCountInString(m.Summary) > MaxSummary || strings.ContainsAny(m.Summary, "\r\n") {
		return badRequest("summary must be one line of at most %d characters", MaxSummary)
	}
	if m.Floor != "" && m.Floor != "pass" && m.Floor != "keep" {
		return badRequest("floor must be pass or keep")
	}
	if actor == ActorHuman {
		if m.Floor != "" || m.Ack != nil {
			return badRequest("floor and ack are for agents")
		}
		if m.Private && m.Type != TypeSay {
			return badRequest("only say can be private")
		}
		if m.Type != TypeAmendment && m.Replaces != 0 {
			return badRequest("replaces is for amendments")
		}
		return nil
	}
	if m.Ack == nil {
		return badRequest("ack is required: the highest seq you have read (next_after)")
	}
	if m.Replaces != 0 {
		return badRequest("replaces is for amendments")
	}
	if m.Private {
		if m.Type != TypeSay && m.Type != TypeAsk {
			return badRequest("only say and ask can be private")
		}
		if m.Floor != "" || m.To != "" || m.Re != 0 {
			return badRequest("a private message has no floor, to or re")
		}
		return nil
	}
	if m.Summary == "" {
		return badRequest("summary is required: one line stating the point of your move")
	}
	if (m.Type == TypeAgree || m.Type == TypeReject || m.Type == TypeDefer) && m.Re == 0 {
		return badRequest("%s needs re: the seq of the message it answers", m.Type)
	}
	if m.Type == TypeResolution && m.Re != 0 {
		return badRequest("a resolution stands on its own; it has no re")
	}
	return nil
}

// unreadHuman returns human events visible to the side with seq > ack.
func unreadHuman(tx *sql.Tx, roomID string, side int, ack int64) ([]Event, error) {
	return queryEvents(tx, `room_id = ? AND seq > ? AND actor = ? AND (visible_to IS NULL OR visible_to = ?)
		ORDER BY seq LIMIT 50`, roomID, ack, ActorHuman, side)
}

// agentsBlocked returns the error an agent gets for a public message, or nil.
func agentsBlocked(r *roomRow) *Error {
	switch r.Phase {
	case PhaseClosed:
		return ErrClosed
	case PhaseBriefing:
		return errorf(http.StatusConflict, "wrong_phase", "briefing: only humans write; wait for the negotiation phase")
	case PhaseApproval:
		return errorf(http.StatusConflict, "wrong_phase", "approval: the humans are deciding on the resolution")
	case PhaseDecided:
		return errorf(http.StatusConflict, "wrong_phase", "decided: the humans are choosing to close the room or to continue")
	}
	if r.PauseKind != "" || r.PendingAmendment != 0 {
		kind := r.PauseKind
		if kind == "" {
			kind = PauseAmendment
		}
		return errorf(http.StatusLocked, "paused", "the room is paused (%s); wait for a resumed or charter event", kind)
	}
	return nil
}

// Post appends a message and applies the rules. It returns the message and
// the last seq written with it (server events that followed).
func (s *Service) Post(p Principal, m Message) (Event, int64, error) {
	if err := m.validate(p.Actor); err != nil {
		return Event{}, 0, err
	}
	var ev Event
	var last int64
	err := s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.Phase == PhaseClosed {
			return ErrClosed
		}
		n := len(r.Sides)
		to := -1
		if m.To != "" {
			if to = ParseLabel(m.To, n); to < 0 {
				return badRequest("no side %q in this room", m.To)
			}
		}
		visible := -1
		if m.Private {
			visible = p.Side
		}
		msg := newEvent{Type: m.Type, Side: p.Side, Actor: p.Actor, To: to, Re: m.Re, Summary: m.Summary,
			Body: m.Body, VisibleTo: visible}

		if p.Actor == ActorHuman {
			if m.Type == TypeAmendment {
				return s.amend(tx, r, now, p, m, &ev, &last)
			}
			var err error
			ev, err = appendEvent(tx, r, now, msg)
			last = r.LastSeq
			return err
		}

		if unread, err := unreadHuman(tx, r.ID, p.Side, *m.Ack); err != nil {
			return err
		} else if len(unread) > 0 {
			e := errorf(http.StatusConflict, "unread_human_message",
				"read the human messages after seq %d first, then post again with a new ack", *m.Ack)
			e.Unread = unread
			return e
		}
		if m.Private {
			var err error
			ev, err = appendEvent(tx, r, now, msg)
			last = r.LastSeq
			return err
		}
		if err := agentsBlocked(r); err != nil {
			return err
		}
		if r.Floor != p.Side {
			return errorf(http.StatusConflict, "not_your_turn", "the floor is with side %s; wait for a turn event", Label(r.Floor))
		}
		if to == p.Side {
			return badRequest("to must be another side; use floor=keep to keep the floor")
		}
		if m.Re != 0 {
			target, err := queryEvents(tx, `room_id = ? AND seq = ? AND visible_to IS NULL`, r.ID, m.Re)
			if err != nil {
				return err
			}
			if len(target) == 0 || !target[0].IsMessage() || target[0].Actor != ActorAgent {
				return badRequest("re must be the seq of a public agent message")
			}
		}

		var err error
		if ev, err = appendEvent(tx, r, now, msg); err != nil {
			return err
		}
		toApproval, err := s.resolutionMove(tx, r, now, p, m, ev)
		if err != nil {
			return err
		}
		r.Sides[p.Side].TurnsUsed++
		if toApproval {
			last = r.LastSeq
			return nil
		}
		if m.Floor != "keep" {
			next := (p.Side + 1) % n
			if to >= 0 {
				next = to
			}
			r.Floor = next
			if _, err := appendEvent(tx, r, now, turnEvent(next)); err != nil {
				return err
			}
		}
		if r.Sides[p.Side].TurnsUsed >= r.TurnBudget {
			r.PauseKind, r.PauseSide, r.PauseActor, r.PauseReason = PauseBudget, sql.NullInt64{}, ActorServer, ""
			if _, err := appendEvent(tx, r, now, serverEvent(EventPaused,
				map[string]any{"kind": PauseBudget, "side": p.Label()})); err != nil {
				return err
			}
		}
		last = r.LastSeq
		return nil
	})
	return ev, last, err
}

// resolutionMove applies a public agent move to the pending resolution. It
// reports whether the room moved to the approval phase.
func (s *Service) resolutionMove(tx *sql.Tx, r *roomRow, now int64, p Principal, m Message, ev Event) (bool, error) {
	if m.Type == TypeResolution {
		r.PendingResolution, r.ResolutionSide, r.ResolutionCharter = ev.Seq, p.Side, r.CharterVersion
		_, err := tx.Exec(`INSERT INTO marks (room_id, subject, kind, side, ts) VALUES (?, ?, 'agree', ?, ?)`,
			r.ID, ev.Seq, p.Side, now)
		return false, err
	}
	if r.PendingResolution == 0 {
		return false, nil
	}
	agreed, err := marks(tx, r.ID, r.PendingResolution, "agree")
	if err != nil || agreed[p.Side] {
		return false, err
	}
	if m.Type != TypeAgree || m.Re != r.PendingResolution {
		res := r.PendingResolution
		r.PendingResolution = 0
		de := serverEvent(EventResolutionDeclined, map[string]any{"resolution": res})
		de.Side = p.Side
		_, err := appendEvent(tx, r, now, de)
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO marks (room_id, subject, kind, side, ts) VALUES (?, ?, 'agree', ?, ?)`,
		r.ID, r.PendingResolution, p.Side, now); err != nil {
		return false, err
	}
	if len(agreed)+1 < len(r.Sides) {
		return false, nil
	}
	return true, setPhase(tx, r, now, PhaseApproval)
}

func setPhase(tx *sql.Tx, r *roomRow, now int64, phase string) error {
	from := r.Phase
	r.Phase = phase
	_, err := appendEvent(tx, r, now, serverEvent(EventPhase, map[string]any{"from": from, "to": phase}))
	return err
}

// amend records a human's amendment and starts the vote.
func (s *Service) amend(tx *sql.Tx, r *roomRow, now int64, p Principal, m Message, ev *Event, last *int64) error {
	if r.PendingAmendment != 0 {
		return errorf(http.StatusConflict, "amendment_pending", "amendment #%d is still being voted on", r.PendingAmendment)
	}
	var data map[string]any
	if m.Replaces != 0 {
		var count int
		if err := tx.QueryRow(`SELECT count(*) FROM charter WHERE room_id = ? AND n = ? AND replaced_by = 0`,
			r.ID, m.Replaces).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return badRequest("charter item %d does not exist or was replaced", m.Replaces)
		}
		data = map[string]any{"replaces": m.Replaces}
	}
	var err error
	*ev, err = appendEvent(tx, r, now, newEvent{Type: TypeAmendment, Side: p.Side, Actor: p.Actor, To: -1,
		Body: m.Body, Summary: m.Summary, Data: data, VisibleTo: -1})
	if err != nil {
		return err
	}
	r.PendingAmendment = ev.Seq
	if _, err := tx.Exec(`INSERT INTO marks (room_id, subject, kind, side, ts) VALUES (?, ?, 'accept', ?, ?)`,
		r.ID, ev.Seq, p.Side, now); err != nil {
		return err
	}
	*last = r.LastSeq
	return nil
}

// Vote records a human's vote on the pending amendment.
func (s *Service) Vote(p Principal, amendment int64, accept bool, comment string) error {
	if p.Actor != ActorHuman {
		return ErrHumansOnly
	}
	if err := checkReason(comment, false); err != nil {
		return err
	}
	return s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.Phase == PhaseClosed {
			return ErrClosed
		}
		if r.PendingAmendment == 0 || r.PendingAmendment != amendment {
			return errorf(http.StatusConflict, "stale_amendment", "amendment #%d is not being voted on", amendment)
		}
		var voted int
		if err := tx.QueryRow(`SELECT count(*) FROM marks WHERE room_id = ? AND subject = ? AND side = ?
			AND kind IN ('accept', 'reject')`, r.ID, amendment, p.Side).Scan(&voted); err != nil {
			return err
		}
		if voted > 0 {
			return errorf(http.StatusConflict, "already_voted", "your side has already voted on amendment #%d", amendment)
		}
		kind := "reject"
		if accept {
			kind = "accept"
		}
		if _, err := tx.Exec(`INSERT INTO marks (room_id, subject, kind, side, ts) VALUES (?, ?, ?, ?, ?)`,
			r.ID, amendment, kind, p.Side, now); err != nil {
			return err
		}
		data := map[string]any{"amendment": amendment, "accept": accept}
		comment = strings.TrimSpace(comment)
		if _, err := appendEvent(tx, r, now, newEvent{Type: EventVote, Side: p.Side, Actor: p.Actor, To: -1,
			VisibleTo: -1, Body: comment, Data: data}); err != nil {
			return err
		}
		if !accept {
			r.PendingAmendment = 0
			_, err := appendEvent(tx, r, now, serverEvent(EventAmendmentRejected, map[string]any{"amendment": amendment}))
			return err
		}
		accepted, err := marks(tx, r.ID, amendment, "accept")
		if err != nil || len(accepted) < len(r.Sides) {
			return err
		}
		return s.adopt(tx, r, now, amendment)
	})
}

// adopt puts an accepted amendment into the charter and voids a pending
// resolution, which was based on the previous charter.
func (s *Service) adopt(tx *sql.Tx, r *roomRow, now int64, amendment int64) error {
	evs, err := queryEvents(tx, `room_id = ? AND seq = ?`, r.ID, amendment)
	if err != nil {
		return err
	}
	a := evs[0]
	var n int
	if err := tx.QueryRow(`SELECT coalesce(max(n), 0) + 1 FROM charter WHERE room_id = ?`, r.ID).Scan(&n); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO charter (room_id, n, text, amendment, accepted_at) VALUES (?, ?, ?, ?, ?)`,
		r.ID, n, a.Body, amendment, now); err != nil {
		return err
	}
	data := map[string]any{"n": n, "amendment": amendment}
	if rep, ok := a.Data["replaces"].(float64); ok {
		if _, err := tx.Exec(`UPDATE charter SET replaced_by = ? WHERE room_id = ? AND n = ?`, n, r.ID, int(rep)); err != nil {
			return err
		}
		data["replaces"] = int(rep)
	}
	r.CharterVersion++
	r.PendingAmendment = 0
	data["version"] = r.CharterVersion
	ce := serverEvent(EventCharter, data)
	ce.Body = a.Body
	if _, err := appendEvent(tx, r, now, ce); err != nil {
		return err
	}
	if r.PendingResolution == 0 {
		return nil
	}
	res := r.PendingResolution
	r.PendingResolution = 0
	if _, err := appendEvent(tx, r, now, serverEvent(EventResolutionVoided,
		map[string]any{"resolution": res, "reason": "charter_changed"})); err != nil {
		return err
	}
	if r.Phase != PhaseApproval {
		return nil
	}
	r.Floor = r.ResolutionSide
	if err := setPhase(tx, r, now, PhaseNegotiation); err != nil {
		return err
	}
	_, err = appendEvent(tx, r, now, turnEvent(r.Floor))
	return err
}

// Ready marks the human's side ready to leave the briefing; when all sides
// are ready the negotiation starts with the floor at side A.
// Unready withdraws a side's start while the room is still in briefing.
func (s *Service) Unready(p Principal) error {
	if p.Actor != ActorHuman {
		return ErrHumansOnly
	}
	return s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.Phase != PhaseBriefing {
			return wrongPhase(r.Phase)
		}
		if !r.Sides[p.Side].ReadyAt.Valid {
			return nil
		}
		if _, err := tx.Exec(`UPDATE sides SET ready_at = NULL WHERE room_id = ? AND idx = ?`, r.ID, p.Side); err != nil {
			return err
		}
		r.Sides[p.Side].ReadyAt = sql.NullInt64{}
		_, err := appendEvent(tx, r, now, newEvent{Type: EventUnready, Side: p.Side, Actor: p.Actor, To: -1, VisibleTo: -1})
		return err
	})
}

func (s *Service) Ready(p Principal) error {
	if p.Actor != ActorHuman {
		return ErrHumansOnly
	}
	return s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.Phase != PhaseBriefing {
			return wrongPhase(r.Phase)
		}
		if r.Sides[p.Side].ReadyAt.Valid {
			return nil
		}
		if _, err := tx.Exec(`UPDATE sides SET ready_at = ? WHERE room_id = ? AND idx = ?`, now, r.ID, p.Side); err != nil {
			return err
		}
		r.Sides[p.Side].ReadyAt = sql.NullInt64{Int64: now, Valid: true}
		if _, err := appendEvent(tx, r, now, newEvent{Type: EventReady, Side: p.Side, Actor: p.Actor, To: -1,
			VisibleTo: -1}); err != nil {
			return err
		}
		for _, sd := range r.Sides {
			if !sd.ReadyAt.Valid {
				return nil
			}
		}
		r.Floor = 0
		if err := setPhase(tx, r, now, PhaseNegotiation); err != nil {
			return err
		}
		_, err := appendEvent(tx, r, now, turnEvent(0))
		return err
	})
}

// Approve records a human's approval of the resolution in the approval
// phase. When all sides have approved, the resolution is adopted as a
// decision and the room moves to decided: the humans close it or continue.
func (s *Service) Approve(p Principal, resolution int64) error {
	if p.Actor != ActorHuman {
		return ErrHumansOnly
	}
	return s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if err := checkApproval(r, resolution); err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT OR IGNORE INTO marks (room_id, subject, kind, side, ts) VALUES (?, ?, 'approve', ?, ?)`,
			r.ID, resolution, p.Side, now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		if _, err := appendEvent(tx, r, now, newEvent{Type: EventApproved, Side: p.Side, Actor: p.Actor, To: -1,
			VisibleTo: -1, Data: map[string]any{"resolution": resolution}}); err != nil {
			return err
		}
		approved, err := marks(tx, r.ID, resolution, "approve")
		if err != nil || len(approved) < len(r.Sides) {
			return err
		}
		if _, err := appendEvent(tx, r, now, serverEvent(EventDecided, map[string]any{"resolution": resolution})); err != nil {
			return err
		}
		return setPhase(tx, r, now, PhaseDecided)
	})
}

// Finish is a human's choice after a decision. close: the side wants to
// close; the room closes (reason approved) when every side has chosen so.
// Otherwise request is the next question for the agents: it is posted as
// the human's message, negotiation resumes and the floor goes to the
// requester's side. One request is enough to continue.
func (s *Service) Finish(p Principal, close bool, request string) error {
	if p.Actor != ActorHuman {
		return ErrHumansOnly
	}
	request = strings.TrimSpace(request)
	if !close {
		if request == "" {
			return badRequest("to continue, write the next question or request for the agents")
		}
		if len(request) > MaxBody {
			return errorf(http.StatusRequestEntityTooLarge, "too_large", "the request is longer than %d bytes", MaxBody)
		}
	}
	return s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.Phase != PhaseDecided {
			return wrongPhase(r.Phase)
		}
		resolution := r.PendingResolution
		if close {
			res, err := tx.Exec(`INSERT OR IGNORE INTO marks (room_id, subject, kind, side, ts) VALUES (?, ?, 'close', ?, ?)`,
				r.ID, resolution, p.Side, now)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return nil
			}
			if _, err := appendEvent(tx, r, now, newEvent{Type: EventFinishVote, Side: p.Side, Actor: p.Actor, To: -1,
				VisibleTo: -1, Data: map[string]any{"resolution": resolution, "choice": "close"}}); err != nil {
				return err
			}
			voted, err := marks(tx, r.ID, resolution, "close")
			if err != nil || len(voted) < len(r.Sides) {
				return err
			}
			return closeRoom(tx, r, now, ReasonApproved, "", serverEvent(EventClosed,
				map[string]any{"reason": ReasonApproved, "resolution": resolution}))
		}
		if _, err := tx.Exec(`DELETE FROM marks WHERE room_id = ? AND subject = ? AND kind = 'close'`, r.ID, resolution); err != nil {
			return err
		}
		if _, err := appendEvent(tx, r, now, newEvent{Type: TypeSay, Side: p.Side, Actor: p.Actor, To: -1, VisibleTo: -1,
			Body: request, Data: map[string]any{"continues": resolution}}); err != nil {
			return err
		}
		if _, err := appendEvent(tx, r, now, newEvent{Type: EventContinued, Side: p.Side, Actor: p.Actor, To: -1,
			VisibleTo: -1, Data: map[string]any{"resolution": resolution}}); err != nil {
			return err
		}
		r.PendingResolution = 0
		r.Floor = p.Side
		if err := setPhase(tx, r, now, PhaseNegotiation); err != nil {
			return err
		}
		_, err := appendEvent(tx, r, now, turnEvent(r.Floor))
		return err
	})
}

func checkApproval(r *roomRow, resolution int64) error {
	if r.Phase == PhaseClosed {
		return ErrClosed
	}
	if r.Phase != PhaseApproval {
		return wrongPhase(r.Phase)
	}
	if r.PendingAmendment != 0 {
		return errorf(http.StatusConflict, "amendment_pending", "vote on amendment #%d first", r.PendingAmendment)
	}
	if resolution != r.PendingResolution {
		return errorf(http.StatusConflict, "stale_resolution", "the resolution under approval is #%d", r.PendingResolution)
	}
	return nil
}

// Reject sends the resolution back to negotiation with a comment for the
// agents. The floor goes to the side that wrote the resolution.
func (s *Service) Reject(p Principal, resolution int64, comment string) error {
	if p.Actor != ActorHuman {
		return ErrHumansOnly
	}
	if err := checkReason(comment, true); err != nil {
		return err
	}
	return s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if err := checkApproval(r, resolution); err != nil {
			return err
		}
		if _, err := appendEvent(tx, r, now, newEvent{Type: EventResolutionRejected, Side: p.Side, Actor: p.Actor,
			To: -1, VisibleTo: -1, Body: strings.TrimSpace(comment), Data: map[string]any{"resolution": resolution}}); err != nil {
			return err
		}
		r.PendingResolution = 0
		r.Floor = r.ResolutionSide
		if err := setPhase(tx, r, now, PhaseNegotiation); err != nil {
			return err
		}
		_, err := appendEvent(tx, r, now, turnEvent(r.Floor))
		return err
	})
}

func checkReason(reason string, required bool) error {
	reason = strings.TrimSpace(reason)
	if required && reason == "" {
		return badRequest("a reason is required")
	}
	if utf8.RuneCountInString(reason) > MaxReason {
		return badRequest("the text is longer than %d characters", MaxReason)
	}
	return nil
}

// CloseRoom closes the room on behalf of a side's human, with a reason.
func (s *Service) CloseRoom(p Principal, reason string) error {
	if p.Actor != ActorHuman {
		return ErrHumansOnly
	}
	if err := checkReason(reason, true); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	return s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.Phase == PhaseClosed {
			return ErrClosed
		}
		code := "closed_by_" + p.Label()
		return closeRoom(tx, r, now, code, reason, newEvent{Type: EventClosed, Side: p.Side, Actor: p.Actor, To: -1,
			VisibleTo: -1, Body: reason, Data: map[string]any{"reason": code}})
	})
}

// Raise pauses the agents: an agent calling the humans, or a human.
func (s *Service) Raise(p Principal, reason string) error {
	if err := checkReason(reason, false); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	return s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.Phase == PhaseClosed {
			return ErrClosed
		}
		if r.Phase != PhaseNegotiation {
			return wrongPhase(r.Phase)
		}
		if r.PauseKind != "" {
			return errorf(http.StatusConflict, "already_paused", "the room is already paused")
		}
		r.PauseKind, r.PauseSide, r.PauseActor, r.PauseReason = PauseHand, nullIndex(p.Side), p.Actor, reason
		_, err := appendEvent(tx, r, now, newEvent{Type: EventPaused, Side: p.Side, Actor: p.Actor, To: -1,
			VisibleTo: -1, Body: reason, Data: map[string]any{"kind": PauseHand}})
		return err
	})
}

// Resume ends a hand or budget pause. floor is the side that gets the floor
// (empty: unchanged); addTurns is added to every side's budget.
func (s *Service) Resume(p Principal, floor string, addTurns int) error {
	if p.Actor != ActorHuman {
		return ErrHumansOnly
	}
	if addTurns < 0 || addTurns > KeyQuota.MaxBudget {
		return badRequest("add_turns must be between 0 and %d", KeyQuota.MaxBudget)
	}
	return s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.Phase == PhaseClosed {
			return ErrClosed
		}
		if r.PauseKind == "" {
			return errorf(http.StatusConflict, "not_paused", "the room is not paused by a hand or the budget")
		}
		if r.PauseKind == PauseHand && r.PauseSide.Valid && int(r.PauseSide.Int64) != p.Side {
			return errorf(http.StatusForbidden, "forbidden", "side %s raised the hand; only its human can resume",
				Label(int(r.PauseSide.Int64)))
		}
		next := r.Floor
		if floor != "" {
			if next = ParseLabel(floor, len(r.Sides)); next < 0 {
				return badRequest("no side %q in this room", floor)
			}
		}
		r.TurnBudget += addTurns
		for i, sd := range r.Sides {
			if sd.TurnsUsed >= r.TurnBudget {
				return errorf(http.StatusConflict, "budget_exhausted",
					"side %s has used its %d turns; resume with add_turns", Label(i), r.TurnBudget)
			}
		}
		r.PauseKind, r.PauseSide, r.PauseActor, r.PauseReason = "", sql.NullInt64{}, "", ""
		r.Floor = next
		if _, err := appendEvent(tx, r, now, newEvent{Type: EventResumed, Side: p.Side, Actor: p.Actor, To: -1,
			VisibleTo: -1, Data: map[string]any{"floor": Label(next), "turn_budget": r.TurnBudget}}); err != nil {
			return err
		}
		_, err := appendEvent(tx, r, now, turnEvent(next))
		return err
	})
}

// SetMandate replaces the private note of the human's side.
func (s *Service) SetMandate(p Principal, text string) error {
	if p.Actor != ActorHuman {
		return ErrHumansOnly
	}
	if len(text) > MaxBody || !utf8.ValidString(text) {
		return badRequest("the mandate must be UTF-8 text of at most %d bytes", MaxBody)
	}
	return s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.Phase == PhaseClosed {
			return ErrClosed
		}
		if _, err := tx.Exec(`UPDATE sides SET mandate = ? WHERE room_id = ? AND idx = ?`, text, r.ID, p.Side); err != nil {
			return err
		}
		r.Sides[p.Side].Mandate = text
		_, err := appendEvent(tx, r, now, newEvent{Type: EventMandate, Side: p.Side, Actor: p.Actor, To: -1,
			VisibleTo: p.Side, Body: text})
		return err
	})
}
