package room

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// CreateRequest describes a new room.
type CreateRequest struct {
	Topic       string `json:"topic"`
	Description string `json:"description"`
	TurnBudget  int    `json:"turn_budget"`
	TTLHours    int    `json:"ttl_hours"`
	Sides       []struct {
		Name string `json:"name"`
	} `json:"sides"`
}

// Created is the result of creating a room: the status and the plaintext
// token of every side, in side order.
type Created struct {
	Status Status
	Tokens []string
}

// CreateRoom checks the host key and creates a room. Side A is the creator.
func (s *Service) CreateRoom(hostKey string, req CreateRequest) (Created, error) {
	var keyID int64
	err := s.db.QueryRow(`SELECT id FROM host_keys WHERE hash = ? AND revoked_at IS NULL`, hash(hostKey)).Scan(&keyID)
	if errors.Is(err, sql.ErrNoRows) {
		return Created{}, ErrHostKey
	}
	if err != nil {
		return Created{}, err
	}

	req.Topic = strings.TrimSpace(req.Topic)
	if req.Topic == "" {
		return Created{}, errorf(http.StatusBadRequest, "bad_request", "topic is required")
	}
	if utf8.RuneCountInString(req.Topic) > MaxTopic {
		return Created{}, errorf(http.StatusBadRequest, "bad_request", "topic is longer than %d characters", MaxTopic)
	}
	if utf8.RuneCountInString(req.Description) > MaxDescription {
		return Created{}, errorf(http.StatusBadRequest, "bad_request", "description is longer than %d characters", MaxDescription)
	}
	budget := req.TurnBudget
	if budget == 0 {
		budget = DefaultBudget
	}
	if budget < 1 || budget > MaxBudget {
		return Created{}, errorf(http.StatusBadRequest, "bad_request", "turn_budget must be between 1 and %d", MaxBudget)
	}
	ttl := DefaultTTL
	if req.TTLHours != 0 {
		ttl = time.Duration(req.TTLHours) * time.Hour
	}
	if ttl < MinTTL || ttl > MaxTTL {
		return Created{}, errorf(http.StatusBadRequest, "bad_request", "ttl_hours must be between 1 and %d", int(MaxTTL/time.Hour))
	}
	names := make([]string, MinSides)
	if len(req.Sides) > 0 {
		if len(req.Sides) < MinSides || len(req.Sides) > MaxSides {
			return Created{}, errorf(http.StatusBadRequest, "bad_request", "a room has between %d and %d sides", MinSides, MaxSides)
		}
		names = make([]string, len(req.Sides))
		for i, sd := range req.Sides {
			n, err := cleanName(sd.Name)
			if err != nil {
				return Created{}, err
			}
			names[i] = n
		}
	}

	id := newRoomID()
	tokens := make([]string, len(names))
	err = s.write(id, func(tx *sql.Tx) error {
		now := ms(s.now())
		if _, err := tx.Exec(`INSERT INTO rooms (id, topic, description, host_key_id, created_at, expires_at, state, floor, turn_budget)
			VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)`,
			id, req.Topic, req.Description, keyID, now, now+ttl.Milliseconds(), StateOpen, budget); err != nil {
			return err
		}
		for i, name := range names {
			tokens[i] = NewToken()
			if _, err := tx.Exec(`INSERT INTO sides (room_id, idx, name, token_hash, token_issued_at) VALUES (?, ?, ?, ?, ?)`,
				id, i, name, hash(tokens[i]), now); err != nil {
				return err
			}
		}
		r, err := loadRoom(tx, id)
		if err != nil {
			return err
		}
		labels := make([]string, len(names))
		for i := range names {
			labels[i] = Label(i)
		}
		if _, err := appendEvent(tx, r, now, EventCreated, -1, ActorServer, -1, "",
			map[string]any{"topic": req.Topic, "sides": labels}); err != nil {
			return err
		}
		if _, err := appendEvent(tx, r, now, EventTurn, -1, ActorServer, 0, "", nil); err != nil {
			return err
		}
		return saveRoom(tx, r)
	})
	if err != nil {
		return Created{}, err
	}
	st, err := s.Status(id, &Principal{RoomID: id, Side: 0, Actor: ActorAgent})
	if err != nil {
		return Created{}, err
	}
	return Created{Status: st, Tokens: tokens}, nil
}

func cleanName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if utf8.RuneCountInString(n) > MaxName {
		return "", errorf(http.StatusBadRequest, "bad_request", "a side name is longer than %d characters", MaxName)
	}
	if strings.ContainsAny(n, "\r\n") {
		return "", errorf(http.StatusBadRequest, "bad_request", "a side name must be one line")
	}
	return n, nil
}

// Authenticate resolves a side token to the agent of that side.
func (s *Service) Authenticate(token string) (Principal, error) {
	if !strings.HasPrefix(token, "coop_") {
		return Principal{}, ErrUnauthorized
	}
	var p Principal
	err := s.db.QueryRow(`SELECT room_id, idx FROM sides WHERE token_hash = ?`, hash(token)).Scan(&p.RoomID, &p.Side)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrUnauthorized
	}
	if err != nil {
		return Principal{}, err
	}
	p.Actor = ActorAgent
	return p, nil
}

// OpenSession exchanges a side token for a browser session of that side's
// human. It returns the principal and the session secret for the cookie.
func (s *Service) OpenSession(token string) (Principal, string, error) {
	p, err := s.Authenticate(token)
	if err != nil {
		return Principal{}, "", err
	}
	p.Actor = ActorHuman
	secret := newSession()
	if _, err := s.db.Exec(`INSERT INTO sessions (hash, room_id, side, created_at) VALUES (?, ?, ?, ?)`,
		hash(secret), p.RoomID, p.Side, ms(s.now())); err != nil {
		return Principal{}, "", err
	}
	return p, secret, s.MarkJoined(p)
}

// AuthenticateSession resolves a browser session secret for a room.
func (s *Service) AuthenticateSession(roomID, secret string) (Principal, error) {
	p := Principal{RoomID: roomID, Actor: ActorHuman}
	err := s.db.QueryRow(`SELECT side FROM sessions WHERE hash = ? AND room_id = ?`, hash(secret), roomID).Scan(&p.Side)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrUnauthorized
	}
	if err != nil {
		return Principal{}, err
	}
	return p, nil
}

// MarkJoined records the first appearance of a participant with a joined event.
func (s *Service) MarkJoined(p Principal) error {
	key := p.RoomID + "/" + p.Label() + "/" + p.Actor
	if _, ok := s.joined.Load(key); ok {
		return nil
	}
	col := "agent_joined_at"
	if p.Actor == ActorHuman {
		col = "human_joined_at"
	}
	err := s.write(p.RoomID, func(tx *sql.Tx) error {
		var joined sql.NullInt64
		if err := tx.QueryRow(`SELECT `+col+` FROM sides WHERE room_id = ? AND idx = ?`, p.RoomID, p.Side).Scan(&joined); err != nil {
			return err
		}
		if joined.Valid {
			return nil
		}
		now := ms(s.now())
		if _, err := tx.Exec(`UPDATE sides SET `+col+` = ? WHERE room_id = ? AND idx = ?`, now, p.RoomID, p.Side); err != nil {
			return err
		}
		r, err := loadRoom(tx, p.RoomID)
		if err != nil {
			return err
		}
		if _, err := appendEvent(tx, r, now, EventJoined, p.Side, p.Actor, -1, "", nil); err != nil {
			return err
		}
		return saveRoom(tx, r)
	})
	if err == nil {
		s.joined.Store(key, struct{}{})
	}
	return err
}

// mutate loads the room in a write transaction, closes it if its lifetime has
// ended (committing that and returning ErrClosed), and otherwise runs fn and
// saves the room.
func (s *Service) mutate(roomID string, fn func(tx *sql.Tx, r *roomRow, now int64) error) error {
	expired := false
	err := s.write(roomID, func(tx *sql.Tx) error {
		r, err := loadRoom(tx, roomID)
		if err != nil {
			return err
		}
		now := ms(s.now())
		wasOpen := r.State != StateClosed
		if err := expireIfDue(tx, r, now); err != nil {
			return err
		}
		if wasOpen && r.State == StateClosed {
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
	Type  string `json:"type"`
	Body  string `json:"body"`
	To    string `json:"to"`
	Floor string `json:"floor"` // "pass" (default) or "keep"; agents only
}

// Post appends a message and applies the floor and budget rules. It returns
// the message and the last seq written with it (server events that followed).
func (s *Service) Post(p Principal, m Message) (Event, int64, error) {
	switch m.Type {
	case TypeSay, TypeAsk, TypePropose, TypeResolution:
	case TypeAgree:
		if p.Actor != ActorAgent {
			return Event{}, 0, errorf(http.StatusBadRequest, "bad_request", "agree is for agents; humans approve a resolution instead")
		}
	case "":
		return Event{}, 0, errorf(http.StatusBadRequest, "bad_request", "type is required: say, ask, propose, agree or resolution")
	default:
		return Event{}, 0, errorf(http.StatusBadRequest, "bad_request", "unknown message type %q", m.Type)
	}
	if strings.TrimSpace(m.Body) == "" {
		return Event{}, 0, errorf(http.StatusBadRequest, "bad_request", "body is required")
	}
	if len(m.Body) > MaxBody {
		return Event{}, 0, errorf(http.StatusRequestEntityTooLarge, "too_large", "body is %d bytes; the limit is %d", len(m.Body), MaxBody)
	}
	if !utf8.ValidString(m.Body) {
		return Event{}, 0, errorf(http.StatusBadRequest, "bad_request", "body must be UTF-8")
	}
	if m.Floor != "" && m.Floor != "pass" && m.Floor != "keep" {
		return Event{}, 0, errorf(http.StatusBadRequest, "bad_request", "floor must be pass or keep")
	}
	if m.Floor != "" && p.Actor != ActorAgent {
		return Event{}, 0, errorf(http.StatusBadRequest, "bad_request", "humans do not hold the floor")
	}

	var ev Event
	var last int64
	err := s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		n := len(r.Sides)
		to := -1
		if m.To != "" {
			to = ParseLabel(m.To, n)
			if to < 0 {
				return errorf(http.StatusBadRequest, "bad_request", "no side %q in this room", m.To)
			}
		}
		if r.State == StateClosed {
			return ErrClosed
		}
		if p.Actor == ActorAgent {
			if r.State == StatePaused {
				return ErrPaused
			}
			if r.Floor != p.Side {
				return errorf(http.StatusConflict, "not_your_turn", "the floor is with side %s; wait for a turn event", Label(r.Floor))
			}
			if to == p.Side {
				return errorf(http.StatusBadRequest, "bad_request", "to must be another side; use floor=keep to keep the floor")
			}
		}

		var err error
		ev, err = appendEvent(tx, r, now, m.Type, p.Side, p.Actor, to, m.Body, nil)
		if err != nil {
			return err
		}
		if m.Type == TypeResolution {
			r.PendingResolution = ev.Seq
		}
		last = r.LastSeq
		if p.Actor != ActorAgent {
			return nil
		}

		r.TurnsUsed++
		if m.Floor != "keep" {
			next := (p.Side + 1) % n
			if to >= 0 {
				next = to
			}
			r.Floor = next
			if _, err := appendEvent(tx, r, now, EventTurn, -1, ActorServer, next, "", nil); err != nil {
				return err
			}
		}
		if r.TurnsUsed >= r.TurnBudget {
			r.State = StatePaused
			r.PausedSide = sql.NullInt64{}
			r.PausedActor = ActorServer
			r.PauseReason = ReasonBudget
			if _, err := appendEvent(tx, r, now, EventPaused, -1, ActorServer, -1, "",
				map[string]any{"reason": ReasonBudget}); err != nil {
				return err
			}
		}
		last = r.LastSeq
		return nil
	})
	return ev, last, err
}

// Raise pauses the room: a human raising a hand, or an agent escalating to
// the humans.
func (s *Service) Raise(p Principal, reason string) error {
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > MaxReason {
		return errorf(http.StatusBadRequest, "bad_request", "reason is longer than %d characters", MaxReason)
	}
	return s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		switch r.State {
		case StateClosed:
			return ErrClosed
		case StatePaused:
			return errorf(http.StatusConflict, "already_paused", "the room is already paused")
		}
		r.State = StatePaused
		r.PausedSide = nullIndex(p.Side)
		r.PausedActor = p.Actor
		r.PauseReason = reason
		var data map[string]any
		if reason != "" {
			data = map[string]any{"reason": reason}
		}
		_, err := appendEvent(tx, r, now, EventPaused, p.Side, p.Actor, -1, "", data)
		return err
	})
}

// Resume reopens a paused room. floor is the side label that gets the floor
// (empty: the side that held it before the pause); addTurns extends the budget.
func (s *Service) Resume(p Principal, floor string, addTurns int) error {
	if p.Actor != ActorHuman {
		return ErrHumansOnly
	}
	if addTurns < 0 || addTurns > MaxBudget {
		return errorf(http.StatusBadRequest, "bad_request", "add_turns must be between 0 and %d", MaxBudget)
	}
	return s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		switch r.State {
		case StateClosed:
			return ErrClosed
		case StateOpen:
			return errorf(http.StatusConflict, "not_paused", "the room is not paused")
		}
		if r.PausedSide.Valid && int(r.PausedSide.Int64) != p.Side {
			return errorf(http.StatusForbidden, "forbidden", "side %s raised the hand; only its human can resume",
				Label(int(r.PausedSide.Int64)))
		}
		next := r.Floor
		if floor != "" {
			next = ParseLabel(floor, len(r.Sides))
			if next < 0 {
				return errorf(http.StatusBadRequest, "bad_request", "no side %q in this room", floor)
			}
		}
		if r.TurnBudget+addTurns > r.TurnsUsed+MaxBudget {
			return errorf(http.StatusBadRequest, "bad_request", "add_turns would leave more than %d turns", MaxBudget)
		}
		r.TurnBudget += addTurns
		if r.TurnsUsed >= r.TurnBudget {
			return errorf(http.StatusConflict, "budget_exhausted", "the turn budget is used up (%d of %d); resume with add_turns",
				r.TurnsUsed, r.TurnBudget)
		}
		r.State = StateOpen
		r.PausedSide = sql.NullInt64{}
		r.PausedActor = ""
		r.PauseReason = ""
		r.Floor = next
		if _, err := appendEvent(tx, r, now, EventResumed, p.Side, p.Actor, -1, "",
			map[string]any{"floor": Label(next), "turn_budget": r.TurnBudget}); err != nil {
			return err
		}
		_, err := appendEvent(tx, r, now, EventTurn, -1, ActorServer, next, "", nil)
		return err
	})
}

// Approve records the approval of the pending resolution by a side's human.
// The room closes when every side has approved.
func (s *Service) Approve(p Principal, resolution int64) error {
	if p.Actor != ActorHuman {
		return ErrHumansOnly
	}
	return s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.State == StateClosed {
			return ErrClosed
		}
		if r.PendingResolution == 0 {
			return errorf(http.StatusConflict, "no_resolution", "there is no resolution to approve")
		}
		if resolution != r.PendingResolution {
			return errorf(http.StatusConflict, "stale_resolution", "the pending resolution is #%d", r.PendingResolution)
		}
		res, err := tx.Exec(`INSERT OR IGNORE INTO approvals (room_id, resolution, side, ts) VALUES (?, ?, ?, ?)`,
			r.ID, resolution, p.Side, now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		if _, err := appendEvent(tx, r, now, EventApproved, p.Side, p.Actor, -1, "",
			map[string]any{"resolution": resolution}); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(`SELECT count(*) FROM approvals WHERE room_id = ? AND resolution = ?`, r.ID, resolution).Scan(&count); err != nil {
			return err
		}
		if count < len(r.Sides) {
			return nil
		}
		r.State = StateClosed
		r.ClosedAt = sql.NullInt64{Int64: now, Valid: true}
		r.CloseReason = ReasonApproved
		r.PausedSide = sql.NullInt64{}
		r.PausedActor = ""
		r.PauseReason = ""
		_, err = appendEvent(tx, r, now, EventClosed, -1, ActorServer, -1, "",
			map[string]any{"reason": ReasonApproved, "resolution": resolution})
		return err
	})
}

// ReissueToken replaces the token of a side and ends its browser sessions.
// Only side A, the creator, may do this.
func (s *Service) ReissueToken(p Principal, label string) (string, error) {
	if p.Side != 0 {
		return "", errorf(http.StatusForbidden, "forbidden", "only side A, the creator of the room, can reissue tokens")
	}
	var token string
	err := s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		side := ParseLabel(label, len(r.Sides))
		if side < 0 {
			return errorf(http.StatusBadRequest, "bad_request", "no side %q in this room", label)
		}
		token = NewToken()
		if _, err := tx.Exec(`UPDATE sides SET token_hash = ?, token_issued_at = ? WHERE room_id = ? AND idx = ?`,
			hash(token), now, r.ID, side); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM sessions WHERE room_id = ? AND side = ?`, r.ID, side); err != nil {
			return err
		}
		_, err := appendEvent(tx, r, now, EventTokenReissued, side, ActorServer, -1, "", nil)
		return err
	})
	return token, err
}

// Rename sets the display name of the principal's side.
func (s *Service) Rename(p Principal, name string) error {
	name, err := cleanName(name)
	if err != nil {
		return err
	}
	if name == "" {
		return errorf(http.StatusBadRequest, "bad_request", "name is required")
	}
	return s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.State == StateClosed {
			return ErrClosed
		}
		if _, err := tx.Exec(`UPDATE sides SET name = ? WHERE room_id = ? AND idx = ?`, name, r.ID, p.Side); err != nil {
			return err
		}
		_, err := appendEvent(tx, r, now, EventRenamed, p.Side, p.Actor, -1, "", map[string]any{"name": name})
		return err
	})
}
