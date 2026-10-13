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
	Briefing    bool   `json:"briefing"`
	Public      bool   `json:"public"`
	Sides       []struct {
		Name string `json:"name"`
	} `json:"sides"`
}

// Invitation is the plaintext invitation for an unclaimed side.
type Invitation struct {
	Side  string
	Token string
}

// Created is the result of creating a room: the creator's side A
// credentials and one invitation per other side.
type Created struct {
	Status      Status
	HumanToken  string
	AgentToken  string
	Invitations []Invitation
}

// CreateRoom creates a room. hostKey is optional and raises the quotas; ip
// is the creator's address, used for the anonymous quota.
func (s *Service) CreateRoom(hostKey, ip string, req CreateRequest) (Created, error) {
	var keyID sql.NullInt64
	quota := AnonQuota
	if hostKey != "" {
		var id int64
		err := s.db.QueryRow(`SELECT id FROM host_keys WHERE hash = ? AND revoked_at IS NULL`, hash(hostKey)).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return Created{}, ErrHostKey
		}
		if err != nil {
			return Created{}, err
		}
		keyID = sql.NullInt64{Int64: id, Valid: true}
		quota = KeyQuota
	}

	req.Topic = strings.TrimSpace(req.Topic)
	switch {
	case req.Topic == "":
		return Created{}, badRequest("topic is required")
	case utf8.RuneCountInString(req.Topic) > MaxTopic:
		return Created{}, badRequest("topic is longer than %d characters", MaxTopic)
	case utf8.RuneCountInString(req.Description) > MaxDescription:
		return Created{}, badRequest("description is longer than %d characters", MaxDescription)
	}
	budget := req.TurnBudget
	if budget == 0 {
		budget = quota.DefaultBudget
	}
	if budget < 1 || budget > quota.MaxBudget {
		return Created{}, badRequest("turn_budget must be between 1 and %d turns per side", quota.MaxBudget)
	}
	ttl := quota.DefaultTTL
	if req.TTLHours != 0 {
		ttl = time.Duration(req.TTLHours) * time.Hour
	}
	if ttl < time.Hour || ttl > quota.MaxTTL {
		return Created{}, badRequest("ttl_hours must be between 1 and %d", int(quota.MaxTTL/time.Hour))
	}
	names := make([]string, MinSides)
	if len(req.Sides) > 0 {
		if len(req.Sides) < MinSides || len(req.Sides) > MaxSides {
			return Created{}, badRequest("a room has between %d and %d sides", MinSides, MaxSides)
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
	c := Created{HumanToken: newToken(PrefixHuman), AgentToken: newToken(PrefixAgent)}
	err := s.write(id, func(tx *sql.Tx) error {
		now := ms(s.now())
		if !keyID.Valid {
			var open int
			if err := tx.QueryRow(`SELECT count(*) FROM rooms WHERE anonymous = 1 AND phase != ?`, PhaseClosed).Scan(&open); err != nil {
				return err
			}
			if open >= MaxOpenAnon {
				return errorf(http.StatusServiceUnavailable, "capacity", "too many open rooms on this server; try later")
			}
		}
		phase := PhaseNegotiation
		if req.Briefing {
			phase = PhaseBriefing
		}
		if _, err := tx.Exec(`INSERT INTO rooms (id, topic, description, public, anonymous, creator_ip, host_key_id,
			created_at, expires_at, phase, turn_budget) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, req.Topic, req.Description, req.Public, !keyID.Valid, ip, keyID, now, now+ttl.Milliseconds(),
			phase, budget); err != nil {
			return err
		}
		for i, name := range names {
			var claimed sql.NullInt64
			if i == 0 {
				claimed = sql.NullInt64{Int64: now, Valid: true}
			}
			if _, err := tx.Exec(`INSERT INTO sides (room_id, idx, name, claimed_at) VALUES (?, ?, ?, ?)`,
				id, i, name, claimed); err != nil {
				return err
			}
		}
		if err := addCredential(tx, c.HumanToken, id, 0, ActorHuman, now); err != nil {
			return err
		}
		if err := addCredential(tx, c.AgentToken, id, 0, ActorAgent, now); err != nil {
			return err
		}
		for i := 1; i < len(names); i++ {
			tok := newToken(PrefixInvite)
			if err := addCredential(tx, tok, id, i, roleInvite, now); err != nil {
				return err
			}
			c.Invitations = append(c.Invitations, Invitation{Side: Label(i), Token: tok})
		}
		r, err := loadRoom(tx, id)
		if err != nil {
			return err
		}
		sides := make([]string, len(names))
		for i := range names {
			sides[i] = Label(i)
		}
		if _, err := appendEvent(tx, r, now, serverEvent(EventCreated,
			map[string]any{"topic": req.Topic, "sides": sides, "phase": phase})); err != nil {
			return err
		}
		if phase == PhaseNegotiation {
			if _, err := appendEvent(tx, r, now, turnEvent(0)); err != nil {
				return err
			}
		}
		return saveRoom(tx, r)
	})
	if err != nil {
		return Created{}, err
	}
	c.Status, err = s.Status(id, &Principal{RoomID: id, Side: 0, Actor: ActorAgent})
	return c, err
}

func turnEvent(side int) newEvent {
	ev := serverEvent(EventTurn, nil)
	ev.To = side
	return ev
}

func addCredential(tx *sql.Tx, token, roomID string, side int, role string, now int64) error {
	_, err := tx.Exec(`INSERT INTO credentials (hash, room_id, side, role, created_at) VALUES (?, ?, ?, ?, ?)`,
		hash(token), roomID, side, role, now)
	return err
}

// replaceCredential revokes every credential of role for the side and adds token.
func replaceCredential(tx *sql.Tx, token, roomID string, side int, role string, now int64) error {
	if _, err := tx.Exec(`DELETE FROM credentials WHERE room_id = ? AND side = ? AND role = ?`, roomID, side, role); err != nil {
		return err
	}
	return addCredential(tx, token, roomID, side, role, now)
}

func cleanName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if utf8.RuneCountInString(n) > MaxName {
		return "", badRequest("a side name is longer than %d characters", MaxName)
	}
	if strings.ContainsAny(n, "\r\n") {
		return "", badRequest("a side name must be one line")
	}
	return n, nil
}

func lookup(q querier, token string) (roomID string, side int, role string, err error) {
	err = q.QueryRow(`SELECT room_id, side, role FROM credentials WHERE hash = ?`, hash(token)).Scan(&roomID, &side, &role)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrUnauthorized
	}
	return
}

// Authenticate resolves a human or agent token. The role is that of the token.
func (s *Service) Authenticate(token string) (Principal, error) {
	if !strings.HasPrefix(token, PrefixHuman) && !strings.HasPrefix(token, PrefixAgent) {
		return Principal{}, ErrUnauthorized
	}
	roomID, side, role, err := lookup(s.db, token)
	if err != nil {
		return Principal{}, err
	}
	if role != ActorHuman && role != ActorAgent {
		return Principal{}, ErrUnauthorized
	}
	return Principal{RoomID: roomID, Side: side, Actor: role}, nil
}

// Invitation resolves an unused invitation to its room and side.
func (s *Service) Invitation(token string) (roomID string, side int, err error) {
	if !strings.HasPrefix(token, PrefixInvite) {
		return "", 0, ErrInvite
	}
	roomID, side, role, err := lookup(s.db, token)
	if err != nil || role != roleInvite {
		return "", 0, ErrInvite
	}
	return roomID, side, nil
}

// ClaimInvitation uses an invitation: the side becomes claimed, the
// invitation stops working and the side's human token is returned.
func (s *Service) ClaimInvitation(token string) (Principal, string, error) {
	roomID, side, err := s.Invitation(token)
	if err != nil {
		return Principal{}, "", err
	}
	human := newToken(PrefixHuman)
	err = s.mutate(roomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.Phase == PhaseClosed {
			return ErrClosed
		}
		res, err := tx.Exec(`DELETE FROM credentials WHERE hash = ? AND role = ?`, hash(token), roleInvite)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrInvite
		}
		if err := replaceCredential(tx, human, roomID, side, ActorHuman, now); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE sides SET claimed_at = ?, human_joined_at = ? WHERE room_id = ? AND idx = ?`,
			now, now, roomID, side); err != nil {
			return err
		}
		r.Sides[side].ClaimedAt = sql.NullInt64{Int64: now, Valid: true}
		ev := serverEvent(EventClaimed, nil)
		ev.Side = side
		_, err = appendEvent(tx, r, now, ev)
		return err
	})
	if err != nil {
		return Principal{}, "", err
	}
	p := Principal{RoomID: roomID, Side: side, Actor: ActorHuman}
	s.joined.Store(joinKey(p), struct{}{})
	return p, human, nil
}

// IssueAgentLink issues a new agent token for the human's side; the previous
// agent token stops working.
func (s *Service) IssueAgentLink(p Principal) (string, error) {
	if p.Actor != ActorHuman {
		return "", ErrHumansOnly
	}
	tok := newToken(PrefixAgent)
	err := s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.Phase == PhaseClosed {
			return ErrClosed
		}
		if err := replaceCredential(tx, tok, r.ID, p.Side, ActorAgent, now); err != nil {
			return err
		}
		ev := serverEvent(EventLinkIssued, map[string]any{"role": ActorAgent})
		ev.Side = p.Side
		_, err := appendEvent(tx, r, now, ev)
		return err
	})
	return tok, err
}

// IssueInvitation issues a new invitation for an unclaimed side; the previous
// one stops working.
func (s *Service) IssueInvitation(p Principal, label string) (string, error) {
	if p.Actor != ActorHuman {
		return "", ErrHumansOnly
	}
	tok := newToken(PrefixInvite)
	err := s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.Phase == PhaseClosed {
			return ErrClosed
		}
		side := ParseLabel(label, len(r.Sides))
		if side < 0 {
			return badRequest("no side %q in this room", label)
		}
		if r.Sides[side].ClaimedAt.Valid {
			return errorf(http.StatusConflict, "already_claimed", "side %s has already been claimed", Label(side))
		}
		if err := replaceCredential(tx, tok, r.ID, side, roleInvite, now); err != nil {
			return err
		}
		ev := serverEvent(EventLinkIssued, map[string]any{"role": roleInvite})
		ev.Side = side
		_, err := appendEvent(tx, r, now, ev)
		return err
	})
	return tok, err
}

// IssueSideLinks issues a human link and an agent link for another side that
// nobody has entered yet, so the creator can hand both to the partner at once.
// Earlier links and the invitation of that side stop working; once the
// side's human or agent has entered, its links belong to that side.
func (s *Service) IssueSideLinks(p Principal, label string) (human, agent string, err error) {
	if p.Actor != ActorHuman {
		return "", "", ErrHumansOnly
	}
	human, agent = newToken(PrefixHuman), newToken(PrefixAgent)
	err = s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.Phase == PhaseClosed {
			return ErrClosed
		}
		side := ParseLabel(label, len(r.Sides))
		if side < 0 {
			return badRequest("no side %q in this room", label)
		}
		if side == p.Side {
			return badRequest("use the agent link for your own side")
		}
		sd := r.Sides[side]
		if sd.HumanJoinedAt.Valid || sd.AgentJoinedAt.Valid {
			return errorf(http.StatusConflict, "already_joined", "side %s has already entered the room", Label(side))
		}
		if _, err := tx.Exec(`DELETE FROM credentials WHERE room_id = ? AND side = ? AND role = ?`, r.ID, side, roleInvite); err != nil {
			return err
		}
		if err := replaceCredential(tx, human, r.ID, side, ActorHuman, now); err != nil {
			return err
		}
		if err := replaceCredential(tx, agent, r.ID, side, ActorAgent, now); err != nil {
			return err
		}
		if !sd.ClaimedAt.Valid {
			if _, err := tx.Exec(`UPDATE sides SET claimed_at = ? WHERE room_id = ? AND idx = ?`, now, r.ID, side); err != nil {
				return err
			}
			r.Sides[side].ClaimedAt = sql.NullInt64{Int64: now, Valid: true}
		}
		ev := serverEvent(EventLinkIssued, map[string]any{"role": "pair"})
		ev.Side = side
		_, err := appendEvent(tx, r, now, ev)
		return err
	})
	return human, agent, err
}

func joinKey(p Principal) string { return p.RoomID + "/" + p.Label() + "/" + p.Actor }

// MarkJoined records the first appearance of a participant.
func (s *Service) MarkJoined(p Principal) error {
	key := joinKey(p)
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
		ev := serverEvent(EventJoined, nil)
		ev.Side, ev.Actor = p.Side, p.Actor
		if _, err := appendEvent(tx, r, now, ev); err != nil {
			return err
		}
		return saveRoom(tx, r)
	})
	if err == nil {
		s.joined.Store(key, struct{}{})
	}
	return err
}

// Rename sets the display name of a side: the human's own side (label ""
// or its own label), or another side nobody of which has entered yet (the
// creator naming the partner before handing over the links).
func (s *Service) Rename(p Principal, label, name string) error {
	if p.Actor != ActorHuman {
		return ErrHumansOnly
	}
	name, err := cleanName(name)
	if err != nil {
		return err
	}
	if name == "" {
		return badRequest("name is required")
	}
	return s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.Phase == PhaseClosed {
			return ErrClosed
		}
		side := p.Side
		if label != "" {
			side = ParseLabel(label, len(r.Sides))
			if side < 0 {
				return badRequest("no side %q in this room", label)
			}
		}
		if side != p.Side {
			if sd := r.Sides[side]; sd.HumanJoinedAt.Valid || sd.AgentJoinedAt.Valid {
				return errorf(http.StatusConflict, "already_joined", "side %s has entered the room and names itself", Label(side))
			}
		}
		if _, err := tx.Exec(`UPDATE sides SET name = ? WHERE room_id = ? AND idx = ?`, name, r.ID, side); err != nil {
			return err
		}
		ev := newEvent{Type: EventRenamed, Side: side, Actor: p.Actor, To: -1, VisibleTo: -1,
			Data: map[string]any{"name": name, "by": Label(p.Side)}}
		_, err := appendEvent(tx, r, now, ev)
		return err
	})
}
