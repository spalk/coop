package room

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

//go:embed schema.sql
var schema string

// Service owns the database and applies the room rules. All writes are
// serialized by mu; SQLite runs in WAL mode so reads proceed concurrently.
type Service struct {
	db  *sql.DB
	mu  sync.Mutex
	hub *Hub
	// Now returns the current time; tests replace it.
	Now func() time.Time

	joined sync.Map // "room/side/actor" -> struct{}: joined event already recorded
}

// Open opens (and creates if needed) the database at path.
func Open(path string) (*Service, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Service{db: db, hub: NewHub(), Now: time.Now}, nil
}

// Close closes the database.
func (s *Service) Close() error { return s.db.Close() }

// Hub returns the notification hub used to wake waiting readers.
func (s *Service) Hub() *Hub { return s.hub }

func (s *Service) now() time.Time { return s.Now().UTC().Truncate(time.Millisecond) }

// write runs fn in a transaction under the write lock and notifies waiting
// readers of the room afterwards.
func (s *Service) write(roomID string, fn func(tx *sql.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if roomID != "" {
		s.hub.Notify(roomID)
	}
	return nil
}

// Secrets and identifiers.

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func randomString(nbytes int) string {
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToLower(b32.EncodeToString(b))
}

// NewToken returns a new side token.
func NewToken() string { return "coop_" + randomString(20) }

func newHostKey() string   { return "coophk_" + randomString(20) }
func newSession() string   { return randomString(20) }
func newRoomID() string    { return randomString(10) }
func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// Time helpers: the database stores Unix milliseconds.

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

func nullTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMS(v.Int64)
	return &t
}

// roomRow is the mutable state of a room loaded inside a transaction.
type roomRow struct {
	ID                string
	Topic             string
	Description       string
	CreatedAt         int64
	ExpiresAt         int64
	ClosedAt          sql.NullInt64
	CloseReason       string
	State             string
	Floor             int
	TurnBudget        int
	TurnsUsed         int
	PausedSide        sql.NullInt64
	PausedActor       string
	PauseReason       string
	PendingResolution int64
	LastSeq           int64
	Sides             []sideRow
}

type sideRow struct {
	Name          string
	AgentJoinedAt sql.NullInt64
	HumanJoinedAt sql.NullInt64
}

type querier interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}

func loadRoom(q querier, id string) (*roomRow, error) {
	r := &roomRow{}
	err := q.QueryRow(`SELECT id, topic, description, created_at, expires_at, closed_at, close_reason,
		state, floor, turn_budget, turns_used, paused_side, paused_actor, pause_reason,
		pending_resolution, last_seq FROM rooms WHERE id = ?`, id).Scan(
		&r.ID, &r.Topic, &r.Description, &r.CreatedAt, &r.ExpiresAt, &r.ClosedAt, &r.CloseReason,
		&r.State, &r.Floor, &r.TurnBudget, &r.TurnsUsed, &r.PausedSide, &r.PausedActor, &r.PauseReason,
		&r.PendingResolution, &r.LastSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(`SELECT name, agent_joined_at, human_joined_at FROM sides WHERE room_id = ? ORDER BY idx`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sr sideRow
		if err := rows.Scan(&sr.Name, &sr.AgentJoinedAt, &sr.HumanJoinedAt); err != nil {
			return nil, err
		}
		r.Sides = append(r.Sides, sr)
	}
	return r, rows.Err()
}

func saveRoom(tx *sql.Tx, r *roomRow) error {
	_, err := tx.Exec(`UPDATE rooms SET closed_at = ?, close_reason = ?, state = ?, floor = ?, turn_budget = ?,
		turns_used = ?, paused_side = ?, paused_actor = ?, pause_reason = ?, pending_resolution = ?, last_seq = ?
		WHERE id = ?`,
		r.ClosedAt, r.CloseReason, r.State, r.Floor, r.TurnBudget, r.TurnsUsed, r.PausedSide, r.PausedActor,
		r.PauseReason, r.PendingResolution, r.LastSeq, r.ID)
	return err
}

// appendEvent assigns the next seq, stores the event and returns it.
func appendEvent(tx *sql.Tx, r *roomRow, ts int64, typ string, side int, actor string, to int, body string, data map[string]any) (Event, error) {
	r.LastSeq++
	var dataJSON string
	if len(data) > 0 {
		b, err := json.Marshal(data)
		if err != nil {
			return Event{}, err
		}
		dataJSON = string(b)
	}
	_, err := tx.Exec(`INSERT INTO events (room_id, seq, ts, type, side, actor, to_side, body, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.LastSeq, ts, typ, nullIndex(side), actor, nullIndex(to), body, dataJSON)
	if err != nil {
		return Event{}, err
	}
	return Event{Seq: r.LastSeq, TS: fromMS(ts), Type: typ, Side: Label(side), Actor: actor,
		To: Label(to), Body: body, Data: data}, nil
}

func nullIndex(i int) sql.NullInt64 {
	if i < 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(i), Valid: true}
}

// expireIfDue closes an open or paused room whose lifetime has ended.
func expireIfDue(tx *sql.Tx, r *roomRow, now int64) error {
	if r.State == StateClosed || now < r.ExpiresAt {
		return nil
	}
	r.State = StateClosed
	r.ClosedAt = sql.NullInt64{Int64: now, Valid: true}
	r.CloseReason = ReasonExpired
	if _, err := appendEvent(tx, r, now, EventClosed, -1, ActorServer, -1, "", map[string]any{"reason": ReasonExpired}); err != nil {
		return err
	}
	return saveRoom(tx, r)
}

// Events returns up to limit events with seq greater than after.
func (s *Service) Events(roomID string, after int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.Query(`SELECT seq, ts, type, side, actor, to_side, body, data FROM events
		WHERE room_id = ? AND seq > ? ORDER BY seq LIMIT ?`, roomID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	evs := []Event{}
	for rows.Next() {
		var (
			e        Event
			ts       int64
			side, to sql.NullInt64
			data     string
		)
		if err := rows.Scan(&e.Seq, &ts, &e.Type, &side, &e.Actor, &to, &e.Body, &data); err != nil {
			return nil, err
		}
		e.TS = fromMS(ts)
		if side.Valid {
			e.Side = Label(int(side.Int64))
		}
		if to.Valid {
			e.To = Label(int(to.Int64))
		}
		if data != "" {
			if err := json.Unmarshal([]byte(data), &e.Data); err != nil {
				return nil, err
			}
		}
		evs = append(evs, e)
	}
	return evs, rows.Err()
}

// Status returns the room status; viewer may be nil. A room whose lifetime
// has ended is closed on the way.
func (s *Service) Status(roomID string, viewer *Principal) (Status, error) {
	r, err := loadRoom(s.db, roomID)
	if err != nil {
		return Status{}, err
	}
	if r.State != StateClosed && ms(s.now()) >= r.ExpiresAt {
		if err := s.write(roomID, func(tx *sql.Tx) error {
			r, err = loadRoom(tx, roomID)
			if err != nil {
				return err
			}
			return expireIfDue(tx, r, ms(s.now()))
		}); err != nil {
			return Status{}, err
		}
	}
	approvals := map[int]int64{}
	if r.PendingResolution > 0 {
		rows, err := s.db.Query(`SELECT side, ts FROM approvals WHERE room_id = ? AND resolution = ?`, roomID, r.PendingResolution)
		if err != nil {
			return Status{}, err
		}
		for rows.Next() {
			var side int
			var ts int64
			if err := rows.Scan(&side, &ts); err != nil {
				rows.Close()
				return Status{}, err
			}
			approvals[side] = ts
		}
		rows.Close()
	}
	st := Status{
		ID: r.ID, Topic: r.Topic, Description: r.Description, State: r.State,
		TurnBudget: r.TurnBudget, TurnsUsed: r.TurnsUsed,
		CreatedAt: fromMS(r.CreatedAt), ExpiresAt: fromMS(r.ExpiresAt),
		ClosedAt: nullTime(r.ClosedAt), CloseReason: r.CloseReason,
		PendingResolution: r.PendingResolution, LastSeq: r.LastSeq,
	}
	if r.State == StateOpen {
		st.Floor = Label(r.Floor)
	}
	if r.State == StatePaused {
		p := &Pause{Actor: r.PausedActor, Reason: r.PauseReason}
		if r.PausedSide.Valid {
			p.Side = Label(int(r.PausedSide.Int64))
		}
		st.Pause = p
	}
	for i, sr := range r.Sides {
		si := SideInfo{Label: Label(i), Name: sr.Name,
			AgentJoinedAt: nullTime(sr.AgentJoinedAt), HumanJoinedAt: nullTime(sr.HumanJoinedAt)}
		if ts, ok := approvals[i]; ok {
			t := fromMS(ts)
			si.ApprovedAt = &t
		}
		st.Sides = append(st.Sides, si)
	}
	if viewer != nil {
		v := &Viewer{Side: viewer.Label(), Actor: viewer.Actor}
		if viewer.Side < len(r.Sides) {
			v.Name = r.Sides[viewer.Side].Name
		}
		st.You = v
	}
	return st, nil
}
