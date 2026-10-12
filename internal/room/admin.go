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
	key := newHostKey()
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
	State     string
	HostKey   string
	CreatedAt time.Time
	ExpiresAt time.Time
	ClosedAt  *time.Time
	Events    int64
}

// ListRooms returns all rooms, newest first.
func (s *Service) ListRooms() ([]RoomSummary, error) {
	rows, err := s.db.Query(`SELECT r.id, r.topic, r.state, coalesce(k.name, ''), r.created_at, r.expires_at,
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
		if err := rows.Scan(&r.ID, &r.Topic, &r.State, &r.HostKey, &created, &expires, &closed, &r.Events); err != nil {
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
	rows, err := s.db.Query(`SELECT id FROM rooms WHERE state != ? AND expires_at <= ?`, StateClosed, ms(s.now()))
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
