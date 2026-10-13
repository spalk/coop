package room

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// HostKey is an issued host key, without its secret.
type HostKey struct {
	ID        int64
	Name      string
	CreatedAt time.Time
	RevokedAt *time.Time
	Rooms     int
}

// AddHostKey issues a host key and returns its plaintext; only the hash is stored.
func (s *Service) AddHostKey(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("a name is required")
	}
	key := newToken(PrefixHostKey)
	err := s.write("", func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO host_keys (name, hash, created_at) VALUES (?, ?, ?)`, name, hash(key), ms(s.now()))
		return err
	})
	return key, err
}

// ListHostKeys returns all host keys.
func (s *Service) ListHostKeys() ([]HostKey, error) {
	rows, err := s.db.Query(`SELECT k.id, k.name, k.created_at, k.revoked_at,
		(SELECT count(*) FROM rooms r WHERE r.host_key_id = k.id) FROM host_keys k ORDER BY k.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []HostKey
	for rows.Next() {
		var (
			k       HostKey
			created int64
			revoked sql.NullInt64
		)
		if err := rows.Scan(&k.ID, &k.Name, &created, &revoked, &k.Rooms); err != nil {
			return nil, err
		}
		k.CreatedAt = fromMS(created)
		k.RevokedAt = nullTime(revoked)
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// RevokeHostKey revokes a host key. Rooms created with it keep working.
func (s *Service) RevokeHostKey(id int64) error {
	return s.write("", func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE host_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, ms(s.now()), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("no active host key with id %d", id)
		}
		return nil
	})
}

// RoomSummary is a line of the admin room list.
type RoomSummary struct {
	ID        string
	Topic     string
	Phase     string
	HostKey   string
	CreatedAt time.Time
	ExpiresAt time.Time
	ClosedAt  *time.Time
	Events    int64
}

// ListRooms returns all rooms, newest first.
func (s *Service) ListRooms() ([]RoomSummary, error) {
	rows, err := s.db.Query(`SELECT r.id, r.topic, r.phase, coalesce(k.name, ''), r.created_at, r.expires_at,
		r.closed_at, r.last_seq FROM rooms r LEFT JOIN host_keys k ON k.id = r.host_key_id ORDER BY r.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoomSummary
	for rows.Next() {
		var (
			r                RoomSummary
			created, expires int64
			closed           sql.NullInt64
		)
		if err := rows.Scan(&r.ID, &r.Topic, &r.Phase, &r.HostKey, &created, &expires, &closed, &r.Events); err != nil {
			return nil, err
		}
		r.CreatedAt, r.ExpiresAt, r.ClosedAt = fromMS(created), fromMS(expires), nullTime(closed)
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRoom removes a room and everything in it.
func (s *Service) DeleteRoom(id string) error {
	return s.write(id, func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM rooms WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ExpireDue closes every room whose lifetime has ended. It returns the number
// of rooms closed.
func (s *Service) ExpireDue() (int, error) {
	rows, err := s.db.Query(`SELECT id FROM rooms WHERE phase != ? AND expires_at <= ?`, PhaseClosed, ms(s.now()))
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if _, err := s.Status(id, nil); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// Purge deletes rooms closed more than retention ago and returns their number.
func (s *Service) Purge(retention time.Duration) (int, error) {
	var n int64
	err := s.write("", func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM rooms WHERE closed_at IS NOT NULL AND closed_at < ?`, ms(s.now().Add(-retention)))
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return int(n), err
}

// PublicRoom is a line of the home page list. Topic and Sides are set only
// for rooms created as public.
type PublicRoom struct {
	CreatedAt   time.Time
	Phase       string
	CloseReason string
	TurnsUsed   int
	TurnBudget  int
	Sides       int
	Topic       string
	Names       []string
}

// PublicRooms lists the most recent rooms without identifying them, and the
// number of rooms that are not closed.
func (s *Service) PublicRooms(limit int) ([]PublicRoom, int, error) {
	var open int
	if err := s.db.QueryRow(`SELECT count(*) FROM rooms WHERE phase != ?`, PhaseClosed).Scan(&open); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(`SELECT r.created_at, r.phase, r.close_reason, r.turn_budget, r.public, r.topic,
		(SELECT coalesce(sum(turns_used), 0) FROM sides WHERE room_id = r.id),
		(SELECT group_concat(name, char(10)) FROM (SELECT name FROM sides WHERE room_id = r.id ORDER BY idx)),
		(SELECT count(*) FROM sides WHERE room_id = r.id)
		FROM rooms r ORDER BY r.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []PublicRoom
	for rows.Next() {
		var (
			pr      PublicRoom
			created int64
			public  bool
			topic   string
			names   sql.NullString
		)
		if err := rows.Scan(&created, &pr.Phase, &pr.CloseReason, &pr.TurnBudget, &public, &topic,
			&pr.TurnsUsed, &names, &pr.Sides); err != nil {
			return nil, 0, err
		}
		pr.CreatedAt = fromMS(created)
		pr.TurnBudget *= pr.Sides
		if public {
			pr.Topic = topic
			if names.Valid {
				pr.Names = strings.Split(names.String, "\n")
			}
		}
		out = append(out, pr)
	}
	return out, open, rows.Err()
}

// AnonymousRoomsSince counts rooms created without a host key from ip since t.
func (s *Service) AnonymousRoomsSince(ip string, t time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM rooms WHERE anonymous = 1 AND creator_ip = ? AND created_at >= ?`,
		ip, ms(t)).Scan(&n)
	return n, err
}
