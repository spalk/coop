package room

import (
	"database/sql"
	"net/http"
	"strings"
)

// Delete records the human's request to delete a closed room before the
// retention period ends. When the human of every side whose human has entered
// the room has asked, the room is deleted at once with everything in it:
// sides, credentials, events (private ones too) and the minutes. A side whose
// human never entered is not asked. A published room must be unpublished
// first. deleted reports whether this request deleted the room.
func (s *Service) Delete(p Principal) (deleted bool, err error) {
	if p.Actor != ActorHuman {
		return false, ErrHumansOnly
	}
	err = s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.Phase != PhaseClosed {
			return wrongPhase(r.Phase)
		}
		if r.PublishedAt.Valid {
			return errorf(http.StatusConflict, "published", "the room is published: unpublish it first")
		}
		if r.Sides[p.Side].DeleteAt.Valid {
			return nil
		}
		r.Sides[p.Side].DeleteAt = sql.NullInt64{Int64: now, Valid: true}
		for _, sd := range r.Sides {
			if sd.HumanJoinedAt.Valid && !sd.DeleteAt.Valid {
				if _, err := tx.Exec(`UPDATE sides SET delete_at = ? WHERE room_id = ? AND idx = ?`, now, r.ID, p.Side); err != nil {
					return err
				}
				_, err := appendEvent(tx, r, now, newEvent{Type: EventDeleteVote, Side: p.Side, Actor: p.Actor, To: -1,
					VisibleTo: -1})
				return err
			}
		}
		if _, err := tx.Exec(`DELETE FROM rooms WHERE id = ?`, r.ID); err != nil {
			return err
		}
		deleted = true
		return nil
	})
	if err != nil || !deleted {
		return false, err
	}
	prefix := p.RoomID + "/"
	s.joined.Range(func(k, _ any) bool {
		if strings.HasPrefix(k.(string), prefix) {
			s.joined.Delete(k)
		}
		return true
	})
	return true, nil
}

// Keep withdraws the human's request to delete the room.
func (s *Service) Keep(p Principal) error {
	if p.Actor != ActorHuman {
		return ErrHumansOnly
	}
	return s.mutate(p.RoomID, func(tx *sql.Tx, r *roomRow, now int64) error {
		if r.Phase != PhaseClosed {
			return wrongPhase(r.Phase)
		}
		if !r.Sides[p.Side].DeleteAt.Valid {
			return nil
		}
		if _, err := tx.Exec(`UPDATE sides SET delete_at = NULL WHERE room_id = ? AND idx = ?`, r.ID, p.Side); err != nil {
			return err
		}
		r.Sides[p.Side].DeleteAt = sql.NullInt64{}
		_, err := appendEvent(tx, r, now, newEvent{Type: EventDeleteWithdrawn, Side: p.Side, Actor: p.Actor, To: -1,
			VisibleTo: -1})
		return err
	})
}
