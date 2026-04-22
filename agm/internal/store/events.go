package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Event mirrors the `events` table.
type Event struct {
	ID        int64
	SessionID string // empty if not associated with a session
	Type      string
	Timestamp time.Time
	Payload   string // raw JSON string
}

// InsertEvent appends an event and returns its row id.
func (s *Store) InsertEvent(e Event) (int64, error) {
	if e.Type == "" {
		return 0, fmt.Errorf("event type required")
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	if e.Payload == "" {
		e.Payload = "{}"
	}
	var sessArg any
	if e.SessionID == "" {
		sessArg = nil
	} else {
		sessArg = e.SessionID
	}
	res, err := s.DB.Exec(
		`INSERT INTO events(session_id, event_type, timestamp, payload)
		 VALUES(?, ?, ?, ?)`,
		sessArg, e.Type, e.Timestamp.UnixMilli(), e.Payload,
	)
	if err != nil {
		return 0, fmt.Errorf("insert event: %w", err)
	}
	return res.LastInsertId()
}

// EventFilter constrains ListEvents.
type EventFilter struct {
	SessionID string // empty = no filter
	Limit     int    // 0 = no explicit limit (still capped to 1000)
}

// ListEvents returns events most-recent-first.
func (s *Store) ListEvents(f EventFilter) ([]Event, error) {
	q := `SELECT id, COALESCE(session_id, ''), event_type, timestamp, payload FROM events`
	args := []any{}
	if f.SessionID != "" {
		q += ` WHERE session_id = ?`
		args = append(args, f.SessionID)
	}
	q += ` ORDER BY id DESC`
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	q += ` LIMIT ?`
	args = append(args, limit)

	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var (
			e    Event
			ts   int64
			sess string
		)
		if err := rows.Scan(&e.ID, &sess, &e.Type, &ts, &e.Payload); err != nil {
			return nil, err
		}
		e.SessionID = sess
		e.Timestamp = time.UnixMilli(ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountRecentEvents returns the number of events in the last `since` duration.
func (s *Store) CountRecentEvents(since time.Duration) (int, error) {
	cutoff := time.Now().Add(-since).UnixMilli()
	var n int
	err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM events WHERE timestamp >= ?`, cutoff,
	).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// LastEventID returns the highest event id, or 0 if table is empty.
// Used by `agm events --tail` to poll for new rows.
func (s *Store) LastEventID() (int64, error) {
	var id sql.NullInt64
	err := s.DB.QueryRow(`SELECT MAX(id) FROM events`).Scan(&id)
	if err != nil {
		return 0, err
	}
	if !id.Valid {
		return 0, nil
	}
	return id.Int64, nil
}

// EventsSince returns events with id > after, oldest-first.
func (s *Store) EventsSince(after int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := s.DB.Query(
		`SELECT id, COALESCE(session_id, ''), event_type, timestamp, payload
		 FROM events WHERE id > ? ORDER BY id ASC LIMIT ?`,
		after, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("events since: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var (
			e    Event
			ts   int64
			sess string
		)
		if err := rows.Scan(&e.ID, &sess, &e.Type, &ts, &e.Payload); err != nil {
			return nil, err
		}
		e.SessionID = sess
		e.Timestamp = time.UnixMilli(ts)
		out = append(out, e)
	}
	return out, rows.Err()
}
