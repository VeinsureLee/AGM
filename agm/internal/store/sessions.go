package store

import (
	"database/sql"
	"fmt"
	"time"
)

// SessionState is the lifecycle state of a session row.
type SessionState string

const (
	StateRunning SessionState = "running"
	StateStopped SessionState = "stopped"
	StateError   SessionState = "error"
)

// Session mirrors the `sessions` table.
type Session struct {
	ID        string
	Name      string
	AgentType string
	StartedAt time.Time
	StoppedAt *time.Time
	State     SessionState
	CWD       string
	Metadata  string // raw JSON string, "" if unset
}

// CreateSession inserts a new session row. started_at is filled with now() if zero.
func (s *Store) CreateSession(sess Session) error {
	if sess.ID == "" {
		return fmt.Errorf("session id required")
	}
	if sess.StartedAt.IsZero() {
		sess.StartedAt = time.Now()
	}
	if sess.State == "" {
		sess.State = StateRunning
	}
	if sess.AgentType == "" {
		sess.AgentType = "claude-code"
	}
	_, err := s.DB.Exec(
		`INSERT INTO sessions(id, name, agent_type, started_at, state, cwd, metadata)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.Name, sess.AgentType,
		sess.StartedAt.UnixMilli(), string(sess.State), sess.CWD, sess.Metadata,
	)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// StopSession marks a session as stopped with state=stopped and stopped_at=now.
func (s *Store) StopSession(id string) error {
	now := time.Now().UnixMilli()
	res, err := s.DB.Exec(
		`UPDATE sessions SET state = ?, stopped_at = ?
		 WHERE id = ? AND state = ?`,
		StateStopped, now, id, StateRunning,
	)
	if err != nil {
		return fmt.Errorf("stop session: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("session %s not running (or not found)", id)
	}
	return nil
}

// GetSession returns the session with the given id or sql.ErrNoRows.
func (s *Store) GetSession(id string) (*Session, error) {
	row := s.DB.QueryRow(
		`SELECT id, name, agent_type, started_at, stopped_at, state, cwd, COALESCE(metadata, '')
		 FROM sessions WHERE id = ?`, id,
	)
	return scanSession(row)
}

// LatestRunningSession returns the most recently started session with state=running,
// or nil, sql.ErrNoRows if none.
func (s *Store) LatestRunningSession() (*Session, error) {
	row := s.DB.QueryRow(
		`SELECT id, name, agent_type, started_at, stopped_at, state, cwd, COALESCE(metadata, '')
		 FROM sessions WHERE state = ? ORDER BY started_at DESC LIMIT 1`,
		StateRunning,
	)
	return scanSession(row)
}

// ListSessions returns sessions, optionally filtered to running only.
func (s *Store) ListSessions(runningOnly bool) ([]Session, error) {
	q := `SELECT id, name, agent_type, started_at, stopped_at, state, cwd, COALESCE(metadata, '')
	      FROM sessions`
	args := []any{}
	if runningOnly {
		q += ` WHERE state = ?`
		args = append(args, StateRunning)
	}
	q += ` ORDER BY started_at DESC`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		sess, err := scanSessionRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sess)
	}
	return out, rows.Err()
}

// scanSession works for *sql.Row.
func scanSession(row *sql.Row) (*Session, error) {
	var (
		sess      Session
		startedMS int64
		stoppedMS sql.NullInt64
		state     string
	)
	err := row.Scan(
		&sess.ID, &sess.Name, &sess.AgentType,
		&startedMS, &stoppedMS, &state, &sess.CWD, &sess.Metadata,
	)
	if err != nil {
		return nil, err
	}
	sess.StartedAt = time.UnixMilli(startedMS)
	if stoppedMS.Valid {
		t := time.UnixMilli(stoppedMS.Int64)
		sess.StoppedAt = &t
	}
	sess.State = SessionState(state)
	return &sess, nil
}

// scanSessionRows works for *sql.Rows.
func scanSessionRows(rows *sql.Rows) (*Session, error) {
	var (
		sess      Session
		startedMS int64
		stoppedMS sql.NullInt64
		state     string
	)
	err := rows.Scan(
		&sess.ID, &sess.Name, &sess.AgentType,
		&startedMS, &stoppedMS, &state, &sess.CWD, &sess.Metadata,
	)
	if err != nil {
		return nil, err
	}
	sess.StartedAt = time.UnixMilli(startedMS)
	if stoppedMS.Valid {
		t := time.UnixMilli(stoppedMS.Int64)
		sess.StoppedAt = &t
	}
	sess.State = SessionState(state)
	return &sess, nil
}
