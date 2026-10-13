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
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

//go:embed schema.sql
var schema string

// schemaVersion 2 is protocol v2. Older room data is dropped on upgrade
// (host keys are kept); see docs/protocol.md.
const schemaVersion = 2

// Service owns the database and applies the room rules. Writes are
// serialized by mu; SQLite runs in WAL mode so reads proceed concurrently.
type Service struct {
	db  *sql.DB
	mu  sync.Mutex
	hub *Hub
	// Now returns the current time; tests replace it.
	Now func() time.Time

	joined sync.Map // "room/side/actor" -> struct{}: joined event recorded
}

// Open opens (and creates or upgrades if needed) the database at path.
func Open(path string) (*Service, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Service{db: db, hub: NewHub(), Now: time.Now}, nil
}

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v < schemaVersion {
		for _, t := range []string{"approvals", "sessions", "marks", "charter", "events", "credentials", "sides", "rooms"} {
			if _, err := db.Exec(`DROP TABLE IF EXISTS ` + t); err != nil {
				return fmt.Errorf("upgrade: %w", err)
			}
		}
	}
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	_, err := db.Exec(`PRAGMA user_version = ` + strconv.Itoa(schemaVersion))
	return err
}

// Close closes the database.
func (s *Service) Close() error { return s.db.Close() }

// Hub returns the notification hub used to wake waiting readers.
func (s *Service) Hub() *Hub { return s.hub }

func (s *Service) now() time.Time { return s.Now().UTC().Truncate(time.Millisecond) }

// write runs fn in a transaction under the write lock and wakes readers of
// the room afterwards.
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

// Token prefixes by role.
const (
	PrefixHuman   = "cooph_"
	PrefixAgent   = "coopa_"
	PrefixInvite  = "coopi_"
	PrefixHostKey = "coophk_"
)

func newToken(prefix string) string { return prefix + randomString(20) }
func newRoomID() string             { return randomString(10) }
func hash(s string) string          { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

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

func nullIndex(i int) sql.NullInt64 {
	if i < 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(i), Valid: true}
}

// roomRow is the mutable state of a room loaded inside a transaction.
type roomRow struct {
	ID                string
	Topic             string
	Description       string
	Public            bool
	Anonymous         bool
	CreatedAt         int64
	ExpiresAt         int64
	ClosedAt          sql.NullInt64
	CloseReason       string
	CloseNote         string
	Phase             string
	Floor             int
	TurnBudget        int
	PauseKind         string
	PauseSide         sql.NullInt64
	PauseActor        string
	PauseReason       string
	PendingResolution int64
	ResolutionSide    int
	ResolutionCharter int
	PendingAmendment  int64
	CharterVersion    int
	LastSeq           int64
	LastHash          string
	Sides             []sideRow
}

type sideRow struct {
	Name          string
	Mandate       string
	TurnsUsed     int
	ClaimedAt     sql.NullInt64
	ReadyAt       sql.NullInt64
	AgentJoinedAt sql.NullInt64
	HumanJoinedAt sql.NullInt64
}

type querier interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}

func loadRoom(q querier, id string) (*roomRow, error) {
	r := &roomRow{}
	err := q.QueryRow(`SELECT id, topic, description, public, anonymous, created_at, expires_at, closed_at,
		close_reason, close_note, phase, floor, turn_budget, pause_kind, pause_side, pause_actor, pause_reason,
		pending_resolution, resolution_side, resolution_charter, pending_amendment, charter_version, last_seq, last_hash
		FROM rooms WHERE id = ?`, id).Scan(
		&r.ID, &r.Topic, &r.Description, &r.Public, &r.Anonymous, &r.CreatedAt, &r.ExpiresAt, &r.ClosedAt,
		&r.CloseReason, &r.CloseNote, &r.Phase, &r.Floor, &r.TurnBudget, &r.PauseKind, &r.PauseSide, &r.PauseActor,
		&r.PauseReason, &r.PendingResolution, &r.ResolutionSide, &r.ResolutionCharter, &r.PendingAmendment,
		&r.CharterVersion, &r.LastSeq, &r.LastHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(`SELECT name, mandate, turns_used, claimed_at, ready_at, agent_joined_at, human_joined_at
		FROM sides WHERE room_id = ? ORDER BY idx`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sr sideRow
		if err := rows.Scan(&sr.Name, &sr.Mandate, &sr.TurnsUsed, &sr.ClaimedAt, &sr.ReadyAt,
			&sr.AgentJoinedAt, &sr.HumanJoinedAt); err != nil {
			return nil, err
		}
		r.Sides = append(r.Sides, sr)
	}
	return r, rows.Err()
}

func saveRoom(tx *sql.Tx, r *roomRow) error {
	_, err := tx.Exec(`UPDATE rooms SET closed_at = ?, close_reason = ?, close_note = ?, phase = ?, floor = ?,
		turn_budget = ?, pause_kind = ?, pause_side = ?, pause_actor = ?, pause_reason = ?, pending_resolution = ?,
		resolution_side = ?, resolution_charter = ?, pending_amendment = ?, charter_version = ?, last_seq = ?,
		last_hash = ? WHERE id = ?`,
		r.ClosedAt, r.CloseReason, r.CloseNote, r.Phase, r.Floor, r.TurnBudget, r.PauseKind, r.PauseSide,
		r.PauseActor, r.PauseReason, r.PendingResolution, r.ResolutionSide, r.ResolutionCharter,
		r.PendingAmendment, r.CharterVersion, r.LastSeq, r.LastHash, r.ID)
	if err != nil {
		return err
	}
	for i, sd := range r.Sides {
		if _, err := tx.Exec(`UPDATE sides SET turns_used = ? WHERE room_id = ? AND idx = ?`, sd.TurnsUsed, r.ID, i); err != nil {
			return err
		}
	}
	return nil
}

// newEvent is an event about to be appended. Side, To and VisibleTo are
// side indexes, -1 for none.
type newEvent struct {
	Type      string
	Side      int
	Actor     string
	To        int
	Re        int64
	Summary   string
	Body      string
	Data      map[string]any
	VisibleTo int
}

func serverEvent(typ string, data map[string]any) newEvent {
	return newEvent{Type: typ, Side: -1, Actor: ActorServer, To: -1, VisibleTo: -1, Data: data}
}

// chainHash is the hash of a public event; see docs/protocol.md section 13.
func chainHash(prev string, e Event, data string) string {
	h := sha256.New()
	for _, part := range []string{prev, strconv.FormatInt(e.Seq, 10), e.TS.Format(time.RFC3339Nano), e.Type,
		e.Side, e.Actor, e.To, strconv.FormatInt(e.Re, 10), e.Body, data} {
		fmt.Fprintf(h, "%d:%s|", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// appendEvent assigns the next seq, stores the event and returns it.
func appendEvent(tx *sql.Tx, r *roomRow, ts int64, ne newEvent) (Event, error) {
	r.LastSeq++
	var dataJSON string
	if len(ne.Data) > 0 {
		b, err := json.Marshal(ne.Data)
		if err != nil {
			return Event{}, err
		}
		dataJSON = string(b)
	}
	e := Event{Seq: r.LastSeq, TS: fromMS(ts), Type: ne.Type, Side: Label(ne.Side), Actor: ne.Actor,
		To: Label(ne.To), Re: ne.Re, Summary: ne.Summary, Body: ne.Body, Data: ne.Data, Private: ne.VisibleTo >= 0}
	if !e.Private {
		e.Hash = chainHash(r.LastHash, e, dataJSON)
		r.LastHash = e.Hash
	}
	_, err := tx.Exec(`INSERT INTO events (room_id, seq, ts, type, side, actor, to_side, re, body, summary, data, visible_to, hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, e.Seq, ts, e.Type, nullIndex(ne.Side), e.Actor, nullIndex(ne.To), e.Re, e.Body, e.Summary, dataJSON,
		nullIndex(ne.VisibleTo), e.Hash)
	return e, err
}

// Events returns up to limit events with seq greater than after that the
// side may see (side < 0: public events only).
func (s *Service) Events(roomID string, side int, after int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	return queryEvents(s.db, `room_id = ? AND seq > ? AND (visible_to IS NULL OR visible_to = ?) ORDER BY seq LIMIT ?`,
		roomID, after, side, limit)
}

func queryEvents(q querier, where string, args ...any) ([]Event, error) {
	rows, err := q.Query(`SELECT seq, ts, type, side, actor, to_side, re, body, summary, data, visible_to, hash
		FROM events WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	evs := []Event{}
	for rows.Next() {
		var (
			e                 Event
			ts                int64
			side, to, visible sql.NullInt64
			data              string
		)
		if err := rows.Scan(&e.Seq, &ts, &e.Type, &side, &e.Actor, &to, &e.Re, &e.Body, &e.Summary, &data,
			&visible, &e.Hash); err != nil {
			return nil, err
		}
		e.TS = fromMS(ts)
		if side.Valid {
			e.Side = Label(int(side.Int64))
		}
		if to.Valid {
			e.To = Label(int(to.Int64))
		}
		e.Private = visible.Valid
		if data != "" {
			if err := json.Unmarshal([]byte(data), &e.Data); err != nil {
				return nil, err
			}
		}
		evs = append(evs, e)
	}
	return evs, rows.Err()
}

// expireIfDue closes a room whose lifetime has ended.
func expireIfDue(tx *sql.Tx, r *roomRow, now int64) error {
	if r.Phase == PhaseClosed || now < r.ExpiresAt {
		return nil
	}
	return closeRoom(tx, r, now, ReasonExpired, "", newEvent{})
}

// closeRoom moves the room to closed. ev, if it has a type, is the event
// recorded for it; otherwise a server closed event is used.
func closeRoom(tx *sql.Tx, r *roomRow, now int64, reason, note string, ev newEvent) error {
	r.Phase = PhaseClosed
	r.ClosedAt = sql.NullInt64{Int64: now, Valid: true}
	r.CloseReason = reason
	r.CloseNote = note
	r.PauseKind, r.PauseSide, r.PauseActor, r.PauseReason = "", sql.NullInt64{}, "", ""
	if ev.Type == "" {
		ev = serverEvent(EventClosed, map[string]any{"reason": reason})
	}
	if _, err := appendEvent(tx, r, now, ev); err != nil {
		return err
	}
	return saveRoom(tx, r)
}

// marks returns side indexes that have a mark of kind on subject.
func marks(q querier, roomID string, subject int64, kind string) (map[int]bool, error) {
	rows, err := q.Query(`SELECT side FROM marks WHERE room_id = ? AND subject = ? AND kind = ?`, roomID, subject, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var side int
		if err := rows.Scan(&side); err != nil {
			return nil, err
		}
		out[side] = true
	}
	return out, rows.Err()
}

func labels(set map[int]bool, n int) []string {
	out := []string{}
	for i := 0; i < n; i++ {
		if set[i] {
			out = append(out, Label(i))
		}
	}
	return out
}

func loadCharter(q querier, roomID string, version int) (Charter, error) {
	c := Charter{Version: version, Items: []CharterItem{}}
	rows, err := q.Query(`SELECT n, text, amendment, accepted_at FROM charter
		WHERE room_id = ? AND replaced_by = 0 ORDER BY n`, roomID)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var it CharterItem
		var at int64
		if err := rows.Scan(&it.N, &it.Text, &it.Amendment, &at); err != nil {
			return c, err
		}
		it.AcceptedAt = fromMS(at)
		c.Items = append(c.Items, it)
	}
	return c, rows.Err()
}

// Status returns the room status; viewer may be nil (public view). A room
// whose lifetime has ended is closed on the way.
func (s *Service) Status(roomID string, viewer *Principal) (Status, error) {
	r, err := loadRoom(s.db, roomID)
	if err != nil {
		return Status{}, err
	}
	if r.Phase != PhaseClosed && ms(s.now()) >= r.ExpiresAt {
		if err := s.write(roomID, func(tx *sql.Tx) error {
			if r, err = loadRoom(tx, roomID); err != nil {
				return err
			}
			return expireIfDue(tx, r, ms(s.now()))
		}); err != nil {
			return Status{}, err
		}
	}
	return statusOf(s.db, r, viewer)
}

func statusOf(q querier, r *roomRow, viewer *Principal) (Status, error) {
	n := len(r.Sides)
	st := Status{
		ID: r.ID, Topic: r.Topic, Description: r.Description, Public: r.Public, Phase: r.Phase,
		TurnBudget: r.TurnBudget, CreatedAt: fromMS(r.CreatedAt), ExpiresAt: fromMS(r.ExpiresAt),
		ClosedAt: nullTime(r.ClosedAt), CloseReason: r.CloseReason, CloseNote: r.CloseNote, LastSeq: r.LastSeq,
	}
	var err error
	if st.Charter, err = loadCharter(q, r.ID, r.CharterVersion); err != nil {
		return st, err
	}
	switch {
	case r.PauseKind != "":
		p := &Pause{Kind: r.PauseKind, Actor: r.PauseActor, Reason: r.PauseReason}
		if r.PauseSide.Valid {
			p.Side = Label(int(r.PauseSide.Int64))
		}
		st.Pause = p
	case r.PendingAmendment != 0 && r.Phase == PhaseNegotiation:
		st.Pause = &Pause{Kind: PauseAmendment, Amendment: r.PendingAmendment}
	}
	if r.Phase == PhaseNegotiation && st.Pause == nil {
		st.Floor = Label(r.Floor)
	}
	if r.PendingResolution != 0 {
		agreed, err := marks(q, r.ID, r.PendingResolution, "agree")
		if err != nil {
			return st, err
		}
		approved, err := marks(q, r.ID, r.PendingResolution, "approve")
		if err != nil {
			return st, err
		}
		st.Resolution = &ResolutionState{Seq: r.PendingResolution, Side: Label(r.ResolutionSide),
			CharterVersion: r.ResolutionCharter, AgreedBy: labels(agreed, n), ApprovedBy: labels(approved, n)}
	}
	if r.PendingAmendment != 0 {
		evs, err := queryEvents(q, `room_id = ? AND seq = ?`, r.ID, r.PendingAmendment)
		if err != nil {
			return st, err
		}
		if len(evs) == 1 {
			a := &AmendmentState{Seq: evs[0].Seq, Side: evs[0].Side, Text: evs[0].Body, Votes: map[string]string{}}
			if v, ok := evs[0].Data["replaces"].(float64); ok {
				a.Replaces = int(v)
			}
			for _, kind := range []string{"accept", "reject"} {
				set, err := marks(q, r.ID, r.PendingAmendment, kind)
				if err != nil {
					return st, err
				}
				for _, l := range labels(set, n) {
					a.Votes[l] = kind
				}
			}
			st.Amendment = a
		}
	}
	for i, sr := range r.Sides {
		st.Sides = append(st.Sides, SideInfo{Label: Label(i), Name: sr.Name, Claimed: sr.ClaimedAt.Valid,
			TurnsUsed: sr.TurnsUsed, ReadyAt: nullTime(sr.ReadyAt),
			AgentJoinedAt: nullTime(sr.AgentJoinedAt), HumanJoinedAt: nullTime(sr.HumanJoinedAt)})
	}
	if viewer != nil && viewer.Side < n {
		sd := r.Sides[viewer.Side]
		st.You = &Viewer{Side: viewer.Label(), Name: sd.Name, Actor: viewer.Actor, Mandate: sd.Mandate}
	}
	return st, nil
}
