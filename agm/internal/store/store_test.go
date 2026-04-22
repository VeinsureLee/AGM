package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// newTestStore opens a fresh SQLite file in t.TempDir() and registers cleanup.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// --- Open / schema -----------------------------------------------------------

func TestOpen_SetsSchemaVersion(t *testing.T) {
	s := newTestStore(t)
	v, err := s.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != CurrentSchemaVersion {
		t.Fatalf("want schema version %d, got %d", CurrentSchemaVersion, v)
	}
}

func TestOpen_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s1.CreateSession(Session{ID: "sess_aaa", Name: "a", CWD: "/tmp"}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()
	got, err := s2.GetSession("sess_aaa")
	if err != nil {
		t.Fatalf("GetSession after reopen: %v", err)
	}
	if got.Name != "a" {
		t.Fatalf("want name a, got %q", got.Name)
	}
}

// --- Sessions ---------------------------------------------------------------

func TestCreateSession_BasicRoundtrip(t *testing.T) {
	s := newTestStore(t)
	sess := Session{
		ID:        "sess_abc",
		Name:      "fix-bug",
		AgentType: "claude-code",
		CWD:       "/tmp/repo",
		StartedAt: time.Unix(1700000000, 0),
	}
	if err := s.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, err := s.GetSession("sess_abc")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Name != "fix-bug" || got.AgentType != "claude-code" || got.CWD != "/tmp/repo" {
		t.Fatalf("unexpected session: %+v", got)
	}
	if got.State != StateRunning {
		t.Fatalf("want running, got %q", got.State)
	}
	if !got.StartedAt.Equal(time.UnixMilli(time.Unix(1700000000, 0).UnixMilli())) {
		t.Fatalf("started_at not preserved: %v", got.StartedAt)
	}
	if got.StoppedAt != nil {
		t.Fatalf("stopped_at should be nil, got %v", got.StoppedAt)
	}
}

func TestCreateSession_RequiresID(t *testing.T) {
	s := newTestStore(t)
	err := s.CreateSession(Session{Name: "x", CWD: "/tmp"})
	if err == nil {
		t.Fatal("want error for missing id")
	}
}

func TestCreateSession_DefaultsAgentType(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession(Session{ID: "sess_x", Name: "x", CWD: "/tmp"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, _ := s.GetSession("sess_x")
	if got.AgentType != "claude-code" {
		t.Fatalf("want default agent_type claude-code, got %q", got.AgentType)
	}
}

func TestCreateSession_DefaultsStartedAt(t *testing.T) {
	s := newTestStore(t)
	before := time.Now()
	if err := s.CreateSession(Session{ID: "sess_x", Name: "x", CWD: "/tmp"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, _ := s.GetSession("sess_x")
	if got.StartedAt.Before(before.Add(-time.Second)) {
		t.Fatalf("started_at not auto-filled: %v", got.StartedAt)
	}
}

func TestStopSession_MarksStopped(t *testing.T) {
	s := newTestStore(t)
	_ = s.CreateSession(Session{ID: "sess_x", Name: "x", CWD: "/tmp"})
	if err := s.StopSession("sess_x"); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	got, _ := s.GetSession("sess_x")
	if got.State != StateStopped {
		t.Fatalf("want stopped, got %q", got.State)
	}
	if got.StoppedAt == nil {
		t.Fatal("stopped_at should be set")
	}
}

func TestStopSession_IdempotentFailsSecondTime(t *testing.T) {
	s := newTestStore(t)
	_ = s.CreateSession(Session{ID: "sess_x", Name: "x", CWD: "/tmp"})
	if err := s.StopSession("sess_x"); err != nil {
		t.Fatalf("first StopSession: %v", err)
	}
	if err := s.StopSession("sess_x"); err == nil {
		t.Fatal("want error stopping already-stopped session")
	}
}

func TestStopSession_UnknownIDFails(t *testing.T) {
	s := newTestStore(t)
	if err := s.StopSession("sess_nope"); err == nil {
		t.Fatal("want error for unknown session")
	}
}

func TestGetSession_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetSession("sess_nope")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("want sql.ErrNoRows, got %v", err)
	}
}

func TestLatestRunningSession_PicksNewestRunning(t *testing.T) {
	s := newTestStore(t)
	_ = s.CreateSession(Session{ID: "sess_old", Name: "old", CWD: "/tmp",
		StartedAt: time.Now().Add(-1 * time.Hour)})
	_ = s.CreateSession(Session{ID: "sess_new", Name: "new", CWD: "/tmp",
		StartedAt: time.Now()})
	got, err := s.LatestRunningSession()
	if err != nil {
		t.Fatalf("LatestRunningSession: %v", err)
	}
	if got.ID != "sess_new" {
		t.Fatalf("want sess_new, got %s", got.ID)
	}
}

func TestLatestRunningSession_IgnoresStopped(t *testing.T) {
	s := newTestStore(t)
	_ = s.CreateSession(Session{ID: "sess_run", Name: "r", CWD: "/tmp",
		StartedAt: time.Now().Add(-1 * time.Hour)})
	_ = s.CreateSession(Session{ID: "sess_stop", Name: "s", CWD: "/tmp",
		StartedAt: time.Now()})
	_ = s.StopSession("sess_stop")
	got, err := s.LatestRunningSession()
	if err != nil {
		t.Fatalf("LatestRunningSession: %v", err)
	}
	if got.ID != "sess_run" {
		t.Fatalf("want sess_run (only running), got %s", got.ID)
	}
}

func TestLatestRunningSession_NoneReturnsErrNoRows(t *testing.T) {
	s := newTestStore(t)
	_, err := s.LatestRunningSession()
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("want sql.ErrNoRows, got %v", err)
	}
}

func TestListSessions_AllVsRunningOnly(t *testing.T) {
	s := newTestStore(t)
	_ = s.CreateSession(Session{ID: "sess_1", Name: "a", CWD: "/tmp"})
	_ = s.CreateSession(Session{ID: "sess_2", Name: "b", CWD: "/tmp"})
	_ = s.StopSession("sess_1")

	all, err := s.ListSessions(false)
	if err != nil || len(all) != 2 {
		t.Fatalf("all: err=%v len=%d", err, len(all))
	}
	running, err := s.ListSessions(true)
	if err != nil || len(running) != 1 || running[0].ID != "sess_2" {
		t.Fatalf("running: err=%v sessions=%+v", err, running)
	}
}

// --- Events ------------------------------------------------------------------

func TestInsertEvent_RequiresType(t *testing.T) {
	s := newTestStore(t)
	_, err := s.InsertEvent(Event{SessionID: "sess_x"})
	if err == nil {
		t.Fatal("want error for missing type")
	}
}

func TestInsertEvent_DefaultsTimestampAndPayload(t *testing.T) {
	s := newTestStore(t)
	id, err := s.InsertEvent(Event{Type: "Ping"})
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	if id == 0 {
		t.Fatal("want non-zero id")
	}
	evs, _ := s.ListEvents(EventFilter{})
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	if evs[0].Timestamp.IsZero() {
		t.Fatal("timestamp should default to now")
	}
	if evs[0].Payload != "{}" {
		t.Fatalf("want default payload {}, got %q", evs[0].Payload)
	}
}

func TestInsertEvent_OrphanSessionStoredAsEmpty(t *testing.T) {
	s := newTestStore(t)
	_, err := s.InsertEvent(Event{Type: "Orphan", Payload: `{"x":1}`})
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	evs, _ := s.ListEvents(EventFilter{})
	if evs[0].SessionID != "" {
		t.Fatalf("want empty session_id, got %q", evs[0].SessionID)
	}
}

func TestListEvents_MostRecentFirstAndFilter(t *testing.T) {
	s := newTestStore(t)
	_ = s.CreateSession(Session{ID: "sess_a", Name: "a", CWD: "/tmp"})
	_ = s.CreateSession(Session{ID: "sess_b", Name: "b", CWD: "/tmp"})
	_, _ = s.InsertEvent(Event{SessionID: "sess_a", Type: "E1"})
	_, _ = s.InsertEvent(Event{SessionID: "sess_b", Type: "E2"})
	_, _ = s.InsertEvent(Event{SessionID: "sess_a", Type: "E3"})

	all, _ := s.ListEvents(EventFilter{})
	if len(all) != 3 || all[0].Type != "E3" {
		t.Fatalf("want most-recent-first, got %+v", all)
	}
	onlyA, _ := s.ListEvents(EventFilter{SessionID: "sess_a"})
	if len(onlyA) != 2 {
		t.Fatalf("want 2 events for sess_a, got %d", len(onlyA))
	}
}

func TestListEvents_LimitEnforced(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 5; i++ {
		_, _ = s.InsertEvent(Event{Type: "E"})
	}
	got, _ := s.ListEvents(EventFilter{Limit: 2})
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
}

func TestCountRecentEvents(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.InsertEvent(Event{Type: "E", Timestamp: time.Now()})
	_, _ = s.InsertEvent(Event{Type: "E", Timestamp: time.Now().Add(-2 * time.Hour)})
	n, err := s.CountRecentEvents(1 * time.Hour)
	if err != nil {
		t.Fatalf("CountRecentEvents: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 recent event, got %d", n)
	}
}

func TestLastEventID_EmptyReturnsZero(t *testing.T) {
	s := newTestStore(t)
	id, err := s.LastEventID()
	if err != nil {
		t.Fatalf("LastEventID: %v", err)
	}
	if id != 0 {
		t.Fatalf("want 0 for empty table, got %d", id)
	}
}

func TestLastEventID_AfterInsert(t *testing.T) {
	s := newTestStore(t)
	id1, _ := s.InsertEvent(Event{Type: "E"})
	id2, _ := s.InsertEvent(Event{Type: "E"})
	got, _ := s.LastEventID()
	if got != id2 || id2 <= id1 {
		t.Fatalf("LastEventID=%d id1=%d id2=%d", got, id1, id2)
	}
}

func TestEventsSince_ReturnsAfterID(t *testing.T) {
	s := newTestStore(t)
	id1, _ := s.InsertEvent(Event{Type: "E1"})
	_, _ = s.InsertEvent(Event{Type: "E2"})
	_, _ = s.InsertEvent(Event{Type: "E3"})
	evs, err := s.EventsSince(id1, 10)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if len(evs) != 2 || evs[0].Type != "E2" || evs[1].Type != "E3" {
		t.Fatalf("unexpected: %+v", evs)
	}
}

// --- FileChanges ------------------------------------------------------------

func TestInsertFileChange_RequiresPath(t *testing.T) {
	s := newTestStore(t)
	_, err := s.InsertFileChange(FileChange{Operation: "WRITE"})
	if err == nil {
		t.Fatal("want error for missing path")
	}
}

func TestInsertFileChange_RequiresOperation(t *testing.T) {
	s := newTestStore(t)
	_, err := s.InsertFileChange(FileChange{Path: "a.txt"})
	if err == nil {
		t.Fatal("want error for missing operation")
	}
}

func TestInsertFileChange_DefaultsTimestamp(t *testing.T) {
	s := newTestStore(t)
	before := time.Now().Add(-time.Second)
	_, err := s.InsertFileChange(FileChange{Path: "a.txt", Operation: "WRITE"})
	if err != nil {
		t.Fatalf("InsertFileChange: %v", err)
	}
	fc, err := s.LastFileChange()
	if err != nil {
		t.Fatalf("LastFileChange: %v", err)
	}
	if fc.Timestamp.Before(before) {
		t.Fatalf("timestamp not auto-filled: %v", fc.Timestamp)
	}
}

func TestLastFileChange_EmptyReturnsErrNoRows(t *testing.T) {
	s := newTestStore(t)
	_, err := s.LastFileChange()
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("want sql.ErrNoRows, got %v", err)
	}
}

func TestLastFileChange_ReturnsNewest(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.InsertFileChange(FileChange{Path: "a.txt", Operation: "CREATE"})
	_, _ = s.InsertFileChange(FileChange{Path: "b.txt", Operation: "WRITE"})
	fc, err := s.LastFileChange()
	if err != nil {
		t.Fatalf("LastFileChange: %v", err)
	}
	if fc.Path != "b.txt" || fc.Operation != "WRITE" {
		t.Fatalf("want newest b.txt/WRITE, got %+v", fc)
	}
}
