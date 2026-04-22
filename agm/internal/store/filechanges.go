package store

import (
	"database/sql"
	"fmt"
	"time"
)

// FileChange mirrors the `file_changes` table.
type FileChange struct {
	ID        int64
	SessionID string // empty if not associated
	Path      string
	Operation string // CREATE|WRITE|REMOVE|RENAME|CHMOD
	Timestamp time.Time
}

// InsertFileChange appends a file-change row.
func (s *Store) InsertFileChange(fc FileChange) (int64, error) {
	if fc.Path == "" {
		return 0, fmt.Errorf("path required")
	}
	if fc.Operation == "" {
		return 0, fmt.Errorf("operation required")
	}
	if fc.Timestamp.IsZero() {
		fc.Timestamp = time.Now()
	}
	var sessArg any
	if fc.SessionID == "" {
		sessArg = nil
	} else {
		sessArg = fc.SessionID
	}
	res, err := s.DB.Exec(
		`INSERT INTO file_changes(session_id, path, operation, timestamp)
		 VALUES(?, ?, ?, ?)`,
		sessArg, fc.Path, fc.Operation, fc.Timestamp.UnixMilli(),
	)
	if err != nil {
		return 0, fmt.Errorf("insert file_change: %w", err)
	}
	return res.LastInsertId()
}

// LastFileChange returns the most recent file_change row, or sql.ErrNoRows.
func (s *Store) LastFileChange() (*FileChange, error) {
	row := s.DB.QueryRow(
		`SELECT id, COALESCE(session_id, ''), path, operation, timestamp
		 FROM file_changes ORDER BY id DESC LIMIT 1`,
	)
	var (
		fc   FileChange
		ts   int64
		sess string
	)
	err := row.Scan(&fc.ID, &sess, &fc.Path, &fc.Operation, &ts)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, err
		}
		return nil, fmt.Errorf("last file_change: %w", err)
	}
	fc.SessionID = sess
	fc.Timestamp = time.UnixMilli(ts)
	return &fc, nil
}
