// Package recorder is the single write path for events: SQLite + events.jsonl.
//
// Before this existed, three call sites (watch / session / hook) each did
// their own InsertEvent, and only the hook path also wrote events.jsonl. The
// JSONL audit log silently lost half the events. Recorder collapses both
// writes into one call so the two stores cannot drift.
//
// Contract: SQLite is the source of truth. JSONL is best-effort audit; a
// failed line is logged-and-swallowed rather than propagated.
package recorder

import (
	"encoding/json"
	"io"
	"time"

	"github.com/agm-project/agm-mvp/internal/store"
)

// Recorder writes events to the store and (optionally) mirrors each one into
// an append-only JSONL stream.
type Recorder struct {
	Store *store.Store
	JSONL io.Writer // nil = skip JSONL mirror
}

// New constructs a Recorder. Pass nil for jsonl to disable the JSONL mirror.
func New(s *store.Store, jsonl io.Writer) *Recorder {
	return &Recorder{Store: s, JSONL: jsonl}
}

// RecordEvent inserts ev into the events table, then mirrors it into JSONL.
// Returns the new event id. JSONL write errors are intentionally swallowed —
// a stale audit log must never break the agent that triggered the event.
func (r *Recorder) RecordEvent(ev store.Event) (int64, error) {
	id, err := r.Store.InsertEvent(ev)
	if err != nil {
		return id, err
	}
	if r.JSONL != nil {
		if ev.Timestamp.IsZero() {
			ev.Timestamp = time.Now()
		}
		_ = writeJSONLLine(r.JSONL, id, ev)
	}
	return id, nil
}

// writeJSONLLine appends one newline-terminated JSON object to w.
func writeJSONLLine(w io.Writer, eventID int64, ev store.Event) error {
	line := map[string]any{
		"id":         eventID,
		"session_id": ev.SessionID,
		"type":       ev.Type,
		"timestamp":  ev.Timestamp.Format(time.RFC3339Nano),
	}
	var pl any
	if err := json.Unmarshal([]byte(ev.Payload), &pl); err == nil {
		line["payload"] = pl
	} else {
		line["payload"] = ev.Payload
	}
	b, err := json.Marshal(line)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}
