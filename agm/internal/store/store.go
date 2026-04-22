// Package store is the SQLite persistence layer for AGM.
//
// It uses modernc.org/sqlite (pure Go) so AGM builds without cgo on Windows.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps *sql.DB with AGM-specific helpers.
type Store struct {
	DB *sql.DB
}

// Open opens (and creates if missing) the SQLite database at path, applies
// the schema and PRAGMAs, and returns a ready-to-use Store.
func Open(path string) (*Store, error) {
	// _pragma query args are honoured by modernc.org/sqlite as initial PRAGMA
	// statements applied to every connection in the pool.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)",
		path,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// modernc.org/sqlite tolerates concurrent readers with WAL but limits
	// writers to one — match that.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	s := &Store{DB: db}
	if err := s.applySchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying DB handle.
func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

// applySchema runs the idempotent schema DDL and records the version row if
// missing. Safe to call on every open.
func (s *Store) applySchema() error {
	if _, err := s.DB.Exec(schemaV1); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	// Seed schema_version if the table is empty.
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&n); err != nil {
		return fmt.Errorf("check schema_version: %w", err)
	}
	if n == 0 {
		_, err := s.DB.Exec(
			"INSERT INTO schema_version(version, applied_at) VALUES(?, ?)",
			CurrentSchemaVersion, time.Now().UnixMilli(),
		)
		if err != nil {
			return fmt.Errorf("seed schema_version: %w", err)
		}
	}
	return nil
}

// SchemaVersion returns the highest applied schema version, or 0 if unset.
func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.DB.QueryRow(
		"SELECT COALESCE(MAX(version), 0) FROM schema_version",
	).Scan(&v)
	if err != nil {
		return 0, err
	}
	return v, nil
}
