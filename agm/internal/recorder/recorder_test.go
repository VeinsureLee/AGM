package recorder

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agm-project/agm-mvp/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRecordEvent_WritesDBRow(t *testing.T) {
	s := newTestStore(t)
	rec := New(s, nil)

	id, err := rec.RecordEvent(store.Event{
		Type:    "PostToolUse",
		Payload: `{"tool_name":"Edit"}`,
	})
	if err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	if id == 0 {
		t.Fatal("want non-zero event id")
	}
	evs, err := s.ListEvents(store.EventFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != "PostToolUse" {
		t.Fatalf("unexpected DB state: %+v", evs)
	}
}

func TestRecordEvent_NilJSONLDoesNotPanic(t *testing.T) {
	s := newTestStore(t)
	rec := New(s, nil)
	if _, err := rec.RecordEvent(store.Event{Type: "Anything"}); err != nil {
		t.Fatalf("RecordEvent with nil JSONL: %v", err)
	}
}

func TestRecordEvent_WritesJSONLLine(t *testing.T) {
	s := newTestStore(t)
	var buf bytes.Buffer
	rec := New(s, &buf)

	ts := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	id, err := rec.RecordEvent(store.Event{
		SessionID: "sess_x",
		Type:      "PostToolUse",
		Timestamp: ts,
		Payload:   `{"tool_name":"Edit"}`,
	})
	if err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	line := buf.String()
	if !strings.HasSuffix(line, "\n") {
		t.Fatalf("JSONL line should end with newline, got %q", line)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(line, "\n")), &parsed); err != nil {
		t.Fatalf("JSONL line not valid JSON: %v", err)
	}
	if int64(parsed["id"].(float64)) != id {
		t.Fatalf("want id=%d, got %v", id, parsed["id"])
	}
	if parsed["session_id"] != "sess_x" {
		t.Fatalf("want session_id=sess_x, got %v", parsed["session_id"])
	}
	if parsed["type"] != "PostToolUse" {
		t.Fatalf("want type=PostToolUse, got %v", parsed["type"])
	}
	if _, ok := parsed["payload"].(map[string]any); !ok {
		t.Fatalf("payload should be embedded as JSON object, got %T", parsed["payload"])
	}
}

func TestRecordEvent_NonJSONPayloadKeptAsString(t *testing.T) {
	s := newTestStore(t)
	var buf bytes.Buffer
	rec := New(s, &buf)

	_, err := rec.RecordEvent(store.Event{
		Type:    "Raw",
		Payload: "not-json-at-all",
	})
	if err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(buf.String(), "\n")), &parsed); err != nil {
		t.Fatalf("JSONL line not valid JSON: %v", err)
	}
	if s, ok := parsed["payload"].(string); !ok || s != "not-json-at-all" {
		t.Fatalf("want payload=string %q, got %T %v", "not-json-at-all", parsed["payload"], parsed["payload"])
	}
}

func TestRecordEvent_DefaultsTimestampInJSONL(t *testing.T) {
	s := newTestStore(t)
	var buf bytes.Buffer
	rec := New(s, &buf)

	before := time.Now().Add(-time.Second)
	if _, err := rec.RecordEvent(store.Event{Type: "Anything"}); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	after := time.Now().Add(time.Second)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(buf.String(), "\n")), &parsed); err != nil {
		t.Fatal(err)
	}
	tsStr, _ := parsed["timestamp"].(string)
	got, err := time.Parse(time.RFC3339Nano, tsStr)
	if err != nil {
		t.Fatalf("invalid timestamp %q: %v", tsStr, err)
	}
	if got.Before(before) || got.After(after) {
		t.Fatalf("timestamp %v not in [%v, %v]", got, before, after)
	}
}

func TestRecordEvent_StoreErrorPropagates(t *testing.T) {
	s := newTestStore(t)
	rec := New(s, nil)
	// store.InsertEvent rejects empty Type.
	_, err := rec.RecordEvent(store.Event{Type: ""})
	if err == nil {
		t.Fatal("want error for empty Type, got nil")
	}
}

// jsonlFailWriter always returns an error from Write — used to verify that
// JSONL failures don't affect the DB write or the returned id.
type jsonlFailWriter struct{}

func (jsonlFailWriter) Write([]byte) (int, error) { return 0, errFailWrite }

var errFailWrite = jsonlSentinelError("forced JSONL failure")

type jsonlSentinelError string

func (e jsonlSentinelError) Error() string { return string(e) }

func TestRecordEvent_JSONLFailureSwallowed(t *testing.T) {
	s := newTestStore(t)
	rec := New(s, jsonlFailWriter{})

	id, err := rec.RecordEvent(store.Event{Type: "PostToolUse"})
	if err != nil {
		t.Fatalf("JSONL failure should not surface as error, got %v", err)
	}
	if id == 0 {
		t.Fatal("want non-zero id even when JSONL write fails")
	}
	evs, _ := s.ListEvents(store.EventFilter{})
	if len(evs) != 1 {
		t.Fatalf("DB write should have succeeded, got %d events", len(evs))
	}
}
