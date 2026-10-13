package room

import (
	"database/sql"
	"net/http"
)

// Publish records the human's vote to make a closed room public. When every
// side has voted the room is published: its public log, protocol and minutes
// become readable by anyone, and it is listed on the home page with topic
// and names. Private events and mandates are never published.
func (s *Service) Publish(p Principal) error {
	if p.Actor != ActorHuman {
		return ErrHumansOnly
	}
	return s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.Phase != PhaseClosed {
			return wrongPhase(r.Phase)
		}
		if r.PublishedAt.Valid || r.Sides[p.Side].PublishAt.Valid {
			return nil
		}
		if _, err := tx.Exec(`UPDATE sides SET publish_at = ? WHERE room_id = ? AND idx = ?`, now, r.ID, p.Side); err != nil {
			return err
		}
		r.Sides[p.Side].PublishAt = sql.NullInt64{Int64: now, Valid: true}
		if _, err := appendEvent(tx, r, now, newEvent{Type: EventPublishVote, Side: p.Side, Actor: p.Actor, To: -1,
			VisibleTo: -1}); err != nil {
			return err
		}
		for _, sd := range r.Sides {
			if !sd.PublishAt.Valid {
				return nil
			}
		}
		if _, err := tx.Exec(`UPDATE rooms SET published_at = ? WHERE id = ?`, now, r.ID); err != nil {
			return err
		}
		r.PublishedAt = sql.NullInt64{Int64: now, Valid: true}
		_, err := appendEvent(tx, r, now, serverEvent(EventPublished, nil))
		return err
	})
}

// Unpublish withdraws the human's vote, or takes a published room down: the
// consent of every side is needed to stay public, so any side may withdraw it.
// All votes are cleared.
func (s *Service) Unpublish(p Principal) error {
	if p.Actor != ActorHuman {
		return ErrHumansOnly
	}
	return s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.Phase != PhaseClosed {
			return wrongPhase(r.Phase)
		}
		if !r.PublishedAt.Valid && !r.Sides[p.Side].PublishAt.Valid {
			return nil
		}
		was := r.PublishedAt.Valid
		if _, err := tx.Exec(`UPDATE rooms SET published_at = NULL WHERE id = ?`, r.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE sides SET publish_at = NULL WHERE room_id = ?`, r.ID); err != nil {
			return err
		}
		r.PublishedAt = sql.NullInt64{}
		for i := range r.Sides {
			r.Sides[i].PublishAt = sql.NullInt64{}
		}
		ev := newEvent{Type: EventUnpublished, Side: p.Side, Actor: p.Actor, To: -1, VisibleTo: -1,
			Data: map[string]any{"was_published": was}}
		_, err := appendEvent(tx, r, now, ev)
		return err
	})
}

// Published reports whether anyone may read the room.
func (s *Service) Published(roomID string) error {
	var at sql.NullInt64
	err := s.db.QueryRow(`SELECT published_at FROM rooms WHERE id = ?`, roomID).Scan(&at)
	if err == sql.ErrNoRows || (err == nil && !at.Valid) {
		return errorf(http.StatusNotFound, "not_found", "no such published room")
	}
	return err
}
