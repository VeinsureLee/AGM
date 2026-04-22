// Package hook handles Claude Code hook invocations.
//
// Flow: `agm hook <name>` reads stdin JSON, normalises it, finds a session id
// (explicit → env var → latest running), writes one row to events + one line to
// events.jsonl. The hook must not error-out the calling agent, so unreadable
// JSON is preserved as {"_raw": "..."} rather than rejected.
package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/agm-project/agm-mvp/internal/store"
)

// Handler carries the dependencies needed to persist a hook event.
type Handler struct {
	Store       *store.Store
	JSONLWriter io.Writer // append-only events.jsonl (nil = skip)
}

// Process consumes stdin, determines session id, writes the event, returns the
// row id and the (possibly synthesized) session id used.
func (h *Handler) Process(hookName string, stdin io.Reader) (int64, string, error) {
	if hookName == "" {
		return 0, "", fmt.Errorf("hook name required")
	}

	raw, err := io.ReadAll(stdin)
	if err != nil {
		return 0, "", fmt.Errorf("read stdin: %w", err)
	}

	payload, sessionHint := normalizePayload(raw)

	sessionID := h.resolveSessionID(sessionHint)

	now := time.Now()
	ev := store.Event{
		SessionID: sessionID,
		Type:      hookName,
		Timestamp: now,
		Payload:   payload,
	}
	id, err := h.Store.InsertEvent(ev)
	if err != nil {
		return 0, sessionID, fmt.Errorf("insert event: %w", err)
	}

	if h.JSONLWriter != nil {
		_ = writeJSONLLine(h.JSONLWriter, id, ev)
	}

	// SessionStart with no existing session row: auto-create one so later
	// hooks can attach. Named after the hint or "claude-code-<id>".
	if hookName == "SessionStart" && sessionHint != "" {
		h.tryAutoCreateSession(sessionHint, payload)
	}

	return id, sessionID, nil
}

// normalizePayload returns (payload string, session hint). When the stdin is
// valid JSON we keep it verbatim and try to pull "session_id"/"sessionId"/"id"
// out of the top level. When it's not JSON we wrap it in {"_raw": ...}.
func normalizePayload(raw []byte) (payload string, sessionHint string) {
	if len(raw) == 0 {
		return "{}", ""
	}
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err == nil {
		for _, k := range []string{"session_id", "sessionId", "sess_id", "id"} {
			if v, ok := top[k]; ok {
				if s, ok := v.(string); ok && s != "" {
					sessionHint = s
					break
				}
			}
		}
		return string(raw), sessionHint
	}
	// Not JSON — wrap so downstream consumers still see a JSON blob.
	wrap := map[string]string{"_raw": string(raw)}
	out, _ := json.Marshal(wrap)
	return string(out), ""
}

// resolveSessionID picks a session id in priority order:
//  1. hint from payload (trusted verbatim — may be a Claude UUID, not our sess_…)
//  2. AGM_SESSION_ID env var
//  3. latest running session in the DB
//  4. empty string (orphan event)
func (h *Handler) resolveSessionID(hint string) string {
	if hint != "" {
		return hint
	}
	if v := os.Getenv("AGM_SESSION_ID"); v != "" {
		return v
	}
	if sess, err := h.Store.LatestRunningSession(); err == nil && sess != nil {
		return sess.ID
	}
	return ""
}

// tryAutoCreateSession best-effort inserts a session row for a fresh id we've
// never seen. Failures are swallowed — hooks must not break the agent.
func (h *Handler) tryAutoCreateSession(id, payload string) {
	if _, err := h.Store.GetSession(id); err == nil {
		return // already exists
	}
	cwd, _ := os.Getwd()
	_ = h.Store.CreateSession(store.Session{
		ID:        id,
		Name:      "auto-" + id,
		AgentType: "claude-code",
		StartedAt: time.Now(),
		State:     store.StateRunning,
		CWD:       cwd,
		Metadata:  payload,
	})
}

// writeJSONLLine appends one line to the JSONL writer.
func writeJSONLLine(w io.Writer, eventID int64, ev store.Event) error {
	line := map[string]any{
		"id":         eventID,
		"session_id": ev.SessionID,
		"type":       ev.Type,
		"timestamp":  ev.Timestamp.Format(time.RFC3339Nano),
	}
	// Embed payload as parsed JSON when it parses, else as a string.
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
