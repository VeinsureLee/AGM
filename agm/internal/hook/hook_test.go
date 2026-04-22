package hook

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agm-project/agm-mvp/internal/recorder"
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

// newHandler builds a Handler wired to a recorder. Pass nil for jsonl to
// skip the JSONL mirror.
func newHandler(s *store.Store, jsonl io.Writer) *Handler {
	return &Handler{
		Store:    s,
		Recorder: recorder.New(s, jsonl),
	}
}

// --- normalizePayload -------------------------------------------------------

func TestNormalizePayload_ValidJSONPreserved(t *testing.T) {
	raw := []byte(`{"tool_name":"Edit","extra":42}`)
	payload, hint := normalizePayload(raw)
	if payload != string(raw) {
		t.Fatalf("payload not preserved: %q", payload)
	}
	if hint != "" {
		t.Fatalf("want no hint, got %q", hint)
	}
}

func TestNormalizePayload_ExtractsSessionHint(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"session_id", `{"session_id":"sess_aaa"}`, "sess_aaa"},
		{"sessionId", `{"sessionId":"sess_bbb"}`, "sess_bbb"},
		{"sess_id", `{"sess_id":"sess_ccc"}`, "sess_ccc"},
		{"id", `{"id":"sess_ddd"}`, "sess_ddd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, hint := normalizePayload([]byte(tc.raw))
			if hint != tc.want {
				t.Fatalf("want hint %q, got %q", tc.want, hint)
			}
		})
	}
}

func TestNormalizePayload_SessionIdWinsOverId(t *testing.T) {
	raw := []byte(`{"session_id":"sess_win","id":"sess_lose"}`)
	_, hint := normalizePayload(raw)
	if hint != "sess_win" {
		t.Fatalf("want session_id to win, got %q", hint)
	}
}

func TestNormalizePayload_NonStringSessionIdIgnored(t *testing.T) {
	raw := []byte(`{"session_id":42}`)
	_, hint := normalizePayload(raw)
	if hint != "" {
		t.Fatalf("want empty hint for non-string session_id, got %q", hint)
	}
}

func TestNormalizePayload_Empty(t *testing.T) {
	payload, hint := normalizePayload(nil)
	if payload != "{}" {
		t.Fatalf("want {}, got %q", payload)
	}
	if hint != "" {
		t.Fatalf("want empty hint, got %q", hint)
	}
}

func TestNormalizePayload_NonJSONWrapped(t *testing.T) {
	raw := []byte("not json at all")
	payload, hint := normalizePayload(raw)
	if hint != "" {
		t.Fatalf("want empty hint, got %q", hint)
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		t.Fatalf("wrapped payload should be valid JSON, got %q: %v", payload, err)
	}
	if out["_raw"] != "not json at all" {
		t.Fatalf("want _raw to preserve original, got %+v", out)
	}
}

// --- resolveSessionID -------------------------------------------------------

func TestResolveSessionID_HintWins(t *testing.T) {
	t.Setenv("AGM_SESSION_ID", "from-env")
	s := newTestStore(t)
	_ = s.CreateSession(store.Session{ID: "sess_latest", Name: "n", CWD: "/tmp"})
	h := newHandler(s, nil)
	got := h.resolveSessionID("sess_hint")
	if got != "sess_hint" {
		t.Fatalf("want hint to win, got %q", got)
	}
}

func TestResolveSessionID_EnvBeatsLatest(t *testing.T) {
	t.Setenv("AGM_SESSION_ID", "from-env")
	s := newTestStore(t)
	_ = s.CreateSession(store.Session{ID: "sess_latest", Name: "n", CWD: "/tmp"})
	h := newHandler(s, nil)
	got := h.resolveSessionID("")
	if got != "from-env" {
		t.Fatalf("want env to win, got %q", got)
	}
}

func TestResolveSessionID_LatestFallback(t *testing.T) {
	t.Setenv("AGM_SESSION_ID", "")
	s := newTestStore(t)
	_ = s.CreateSession(store.Session{ID: "sess_latest", Name: "n", CWD: "/tmp"})
	h := newHandler(s, nil)
	got := h.resolveSessionID("")
	if got != "sess_latest" {
		t.Fatalf("want latest running, got %q", got)
	}
}

func TestResolveSessionID_EmptyWhenNothingAvailable(t *testing.T) {
	t.Setenv("AGM_SESSION_ID", "")
	s := newTestStore(t)
	h := newHandler(s, nil)
	got := h.resolveSessionID("")
	if got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

// --- Process end-to-end -----------------------------------------------------

func TestProcess_RequiresHookName(t *testing.T) {
	s := newTestStore(t)
	h := newHandler(s, nil)
	_, _, err := h.Process("", strings.NewReader("{}"))
	if err == nil {
		t.Fatal("want error for empty hook name")
	}
}

func TestProcess_WritesDBRow(t *testing.T) {
	t.Setenv("AGM_SESSION_ID", "")
	s := newTestStore(t)
	_ = s.CreateSession(store.Session{ID: "sess_x", Name: "n", CWD: "/tmp"})
	h := newHandler(s, nil)

	id, sid, err := h.Process("PostToolUse", strings.NewReader(`{"tool_name":"Edit"}`))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if id == 0 {
		t.Fatal("want non-zero event id")
	}
	if sid != "sess_x" {
		t.Fatalf("want session sess_x (latest running), got %q", sid)
	}

	evs, _ := s.ListEvents(store.EventFilter{})
	if len(evs) != 1 || evs[0].Type != "PostToolUse" {
		t.Fatalf("unexpected DB state: %+v", evs)
	}
	if !strings.Contains(evs[0].Payload, `"tool_name":"Edit"`) {
		t.Fatalf("payload not preserved: %q", evs[0].Payload)
	}
}

func TestProcess_WritesJSONLLine(t *testing.T) {
	t.Setenv("AGM_SESSION_ID", "")
	s := newTestStore(t)
	_ = s.CreateSession(store.Session{ID: "sess_x", Name: "n", CWD: "/tmp"})
	var buf bytes.Buffer
	h := newHandler(s, &buf)

	_, _, err := h.Process("PostToolUse", strings.NewReader(`{"tool_name":"Edit"}`))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	line := buf.String()
	if !strings.HasSuffix(line, "\n") {
		t.Fatalf("JSONL line should end with newline, got %q", line)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(line, "\n")), &parsed); err != nil {
		t.Fatalf("JSONL line not valid JSON: %v", err)
	}
	if parsed["type"] != "PostToolUse" {
		t.Fatalf("want type=PostToolUse, got %+v", parsed)
	}
	if parsed["session_id"] != "sess_x" {
		t.Fatalf("want session_id=sess_x, got %+v", parsed)
	}
	// payload must be embedded as parsed JSON, not a string
	if _, ok := parsed["payload"].(map[string]any); !ok {
		t.Fatalf("payload should be embedded as JSON object, got %T: %+v",
			parsed["payload"], parsed["payload"])
	}
}

func TestProcess_JSONLEmbedsNonJSONPayloadAsString(t *testing.T) {
	// When stdin isn't JSON, normalizePayload wraps it; the wrapped form
	// IS valid JSON, so the JSONL "payload" field should still be an object.
	t.Setenv("AGM_SESSION_ID", "")
	s := newTestStore(t)
	var buf bytes.Buffer
	h := newHandler(s, &buf)

	_, _, err := h.Process("Raw", strings.NewReader("not json"))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(buf.String(), "\n")), &parsed); err != nil {
		t.Fatalf("JSONL line not valid JSON: %v", err)
	}
	payload, ok := parsed["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload should be object (wrapped), got %T", parsed["payload"])
	}
	if payload["_raw"] != "not json" {
		t.Fatalf("want _raw to carry original, got %+v", payload)
	}
}

func TestProcess_AutoCreatesSessionForFreshSessionStartHint(t *testing.T) {
	t.Setenv("AGM_SESSION_ID", "")
	s := newTestStore(t)
	h := newHandler(s, nil)

	_, sid, err := h.Process("SessionStart",
		strings.NewReader(`{"session_id":"sess_fresh"}`))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if sid != "sess_fresh" {
		t.Fatalf("want sess_fresh, got %q", sid)
	}
	got, err := s.GetSession("sess_fresh")
	if err != nil {
		t.Fatalf("session should have been auto-created: %v", err)
	}
	if got.State != store.StateRunning {
		t.Fatalf("want running, got %q", got.State)
	}
}

func TestProcess_NoAutoCreateForNonSessionStart(t *testing.T) {
	t.Setenv("AGM_SESSION_ID", "")
	s := newTestStore(t)
	h := newHandler(s, nil)

	_, _, err := h.Process("PostToolUse",
		strings.NewReader(`{"session_id":"sess_fresh"}`))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if _, err := s.GetSession("sess_fresh"); err == nil {
		t.Fatal("PostToolUse should not auto-create session")
	}
}

func TestProcess_DoesNotOverwriteExistingSessionOnStart(t *testing.T) {
	t.Setenv("AGM_SESSION_ID", "")
	s := newTestStore(t)
	_ = s.CreateSession(store.Session{ID: "sess_existing", Name: "original", CWD: "/tmp"})
	h := newHandler(s, nil)

	_, _, err := h.Process("SessionStart",
		strings.NewReader(`{"session_id":"sess_existing"}`))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	got, _ := s.GetSession("sess_existing")
	if got.Name != "original" {
		t.Fatalf("existing session was overwritten: %+v", got)
	}
}

func TestProcess_EmptyStdinStoresEmptyJSON(t *testing.T) {
	t.Setenv("AGM_SESSION_ID", "")
	s := newTestStore(t)
	h := newHandler(s, nil)
	_, _, err := h.Process("Stop", strings.NewReader(""))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	evs, _ := s.ListEvents(store.EventFilter{})
	if len(evs) != 1 || evs[0].Payload != "{}" {
		t.Fatalf("unexpected: %+v", evs)
	}
}
