// Command agm is the AGM MVP CLI entry point.
//
// All subcommands operate relative to the current working directory: an AGM
// project is just a directory containing a .agm/ folder. The --agm-dir flag
// overrides discovery.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/agm-project/agm-mvp/internal/hook"
	"github.com/agm-project/agm-mvp/internal/id"
	"github.com/agm-project/agm-mvp/internal/recorder"
	"github.com/agm-project/agm-mvp/internal/store"
	"github.com/agm-project/agm-mvp/internal/watcher"
)

// Version is injected at build time via -ldflags "-X main.Version=...".
var Version = "0.0.1-dev"

var (
	flagAGMDir string
)

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		// cobra already prints the error; just exit non-zero.
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "agm",
		Short:   "AGM — Agent Management orchestration layer (MVP)",
		Version: Version,
		SilenceUsage: true,
	}
	cmd.PersistentFlags().StringVar(&flagAGMDir, "agm-dir", "",
		"override .agm directory (default: <cwd>/.agm)")

	cmd.AddCommand(
		newInitCmd(),
		newWatchCmd(),
		newSessionCmd(),
		newHookCmd(),
		newEventsCmd(),
		newStatusCmd(),
	)
	return cmd
}

// ---------- helpers ----------

// agmDir returns the resolved .agm directory path (absolute).
func agmDir() (string, error) {
	if flagAGMDir != "" {
		return filepath.Abs(flagAGMDir)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, ".agm"), nil
}

// openStore opens the SQLite database for an initialised AGM dir.
func openStore() (*store.Store, string, error) {
	dir, err := agmDir()
	if err != nil {
		return nil, "", err
	}
	dbPath := filepath.Join(dir, "state.db")
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return nil, dir, fmt.Errorf("AGM not initialised in %s — run `agm init` first", filepath.Dir(dir))
	}
	s, err := store.Open(dbPath)
	return s, dir, err
}

// openJSONL opens (creates) events.jsonl for append.
func openJSONL(agmdir string) (*os.File, error) {
	return os.OpenFile(
		filepath.Join(agmdir, "events.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0o644,
	)
}

// ---------- init ----------

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialise .agm/ in the current directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := agmDir()
			if err != nil {
				return err
			}
			logsDir := filepath.Join(dir, "logs")
			if err := os.MkdirAll(logsDir, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", logsDir, err)
			}

			// Config (write only if missing — don't stomp user edits).
			configPath := filepath.Join(dir, "config.json")
			if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
				cfg := defaultConfig()
				b, _ := json.MarshalIndent(cfg, "", "  ")
				if err := os.WriteFile(configPath, b, 0o644); err != nil {
					return fmt.Errorf("write config: %w", err)
				}
				fmt.Printf("✓ Wrote %s\n", configPath)
			} else {
				fmt.Printf("= Keeping existing %s\n", configPath)
			}

			// SQLite: Open() applies schema idempotently.
			dbPath := filepath.Join(dir, "state.db")
			s, err := store.Open(dbPath)
			if err != nil {
				return err
			}
			defer s.Close()
			v, _ := s.SchemaVersion()
			fmt.Printf("✓ Initialised %s (schema v%d)\n", dbPath, v)

			// Touch events.jsonl so `agm watch` can open it for append immediately.
			jsonlPath := filepath.Join(dir, "events.jsonl")
			f, err := os.OpenFile(jsonlPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return fmt.Errorf("touch events.jsonl: %w", err)
			}
			f.Close()

			fmt.Printf("✓ Created %s\n", dir)
			fmt.Println()
			fmt.Println("AGM ready. Next:")
			fmt.Println("  agm watch                  # start watching this directory")
			fmt.Println("  agm session start <name>   # register a new session")
			return nil
		},
	}
}

type configFile struct {
	Version        string   `json:"version"`
	SchemaVersion  int      `json:"schema_version"`
	IgnorePatterns []string `json:"ignore_patterns"`
}

func defaultConfig() configFile {
	return configFile{
		Version:        Version,
		SchemaVersion:  store.CurrentSchemaVersion,
		IgnorePatterns: watcher.DefaultIgnorePatterns(),
	}
}

func loadConfig(agmdir string) configFile {
	b, err := os.ReadFile(filepath.Join(agmdir, "config.json"))
	if err != nil {
		return defaultConfig()
	}
	var c configFile
	if err := json.Unmarshal(b, &c); err != nil {
		return defaultConfig()
	}
	if len(c.IgnorePatterns) == 0 {
		c.IgnorePatterns = watcher.DefaultIgnorePatterns()
	}
	return c
}

// ---------- watch ----------

func newWatchCmd() *cobra.Command {
	var quiet bool
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Watch the current directory and record file changes",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, agmdir, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()

			cfg := loadConfig(agmdir)
			root, err := os.Getwd()
			if err != nil {
				return err
			}

			jsonl, err := openJSONL(agmdir)
			if err != nil {
				return err
			}
			defer jsonl.Close()
			rec := recorder.New(s, jsonl)

			onChange := func(fc watcher.FileChange) {
				// Resolve active session (may be empty).
				sessID := ""
				if sess, err := s.LatestRunningSession(); err == nil && sess != nil {
					sessID = sess.ID
				}
				_, _ = s.InsertFileChange(store.FileChange{
					SessionID: sessID,
					Path:      fc.RelPath,
					Operation: string(fc.Op),
					Timestamp: time.Now(),
				})
				// Also surface as an event so the event stream is a complete log.
				payload, _ := json.Marshal(map[string]string{
					"path": fc.RelPath,
					"op":   string(fc.Op),
				})
				_, _ = rec.RecordEvent(store.Event{
					SessionID: sessID,
					Type:      "FileChange",
					Timestamp: time.Now(),
					Payload:   string(payload),
				})
				if !quiet {
					ts := time.Now().Format("15:04:05")
					fmt.Printf("[%s] %-6s %s\n", ts, fc.Op, fc.RelPath)
				}
			}

			w, err := watcher.New(watcher.Config{
				Root:           root,
				IgnorePatterns: cfg.IgnorePatterns,
				OnChange:       onChange,
			})
			if err != nil {
				return err
			}
			defer w.Close()

			fmt.Printf("Watching %s (recursive). Press Ctrl+C to stop.\n", root)

			sigC := make(chan os.Signal, 1)
			signal.Notify(sigC, os.Interrupt, syscall.SIGTERM)
			<-sigC
			fmt.Println("\nStopping watcher...")
			return nil
		},
	}
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress per-event stdout output")
	return cmd
}

// ---------- session ----------

func newSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage agent sessions",
	}
	cmd.AddCommand(
		newSessionStartCmd(),
		newSessionStopCmd(),
		newSessionListCmd(),
		newSessionShowCmd(),
	)
	return cmd
}

func newSessionStartCmd() *cobra.Command {
	var agentType string
	cmd := &cobra.Command{
		Use:   "start <name>",
		Short: "Register a new session; prints the session id to stdout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, agmdir, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()

			jsonl, err := openJSONL(agmdir)
			if err != nil {
				fmt.Fprintln(os.Stderr, "warn: could not open events.jsonl:", err)
				jsonl = nil
			}
			if jsonl != nil {
				defer jsonl.Close()
			}
			rec := recorder.New(s, jsonl)

			cwd, _ := os.Getwd()
			sess := store.Session{
				ID:        id.NewSessionID(),
				Name:      args[0],
				AgentType: agentType,
				StartedAt: time.Now(),
				State:     store.StateRunning,
				CWD:       cwd,
			}
			if err := s.CreateSession(sess); err != nil {
				return err
			}
			// Log a CLI-side SessionRegistered event. This is distinct from
			// the Claude Code "SessionStart" hook, which fires separately when
			// the agent actually starts — mixing the two would produce a
			// duplicate SessionStart per session.
			payload, _ := json.Marshal(map[string]string{
				"name":       sess.Name,
				"agent_type": sess.AgentType,
				"cwd":        sess.CWD,
			})
			_, _ = rec.RecordEvent(store.Event{
				SessionID: sess.ID,
				Type:      "SessionRegistered",
				Timestamp: sess.StartedAt,
				Payload:   string(payload),
			})
			fmt.Println(sess.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&agentType, "agent-type", "claude-code", "agent type label")
	return cmd
}

func newSessionStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <session-id>",
		Short: "Mark a session as stopped",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, agmdir, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()

			jsonl, err := openJSONL(agmdir)
			if err != nil {
				fmt.Fprintln(os.Stderr, "warn: could not open events.jsonl:", err)
				jsonl = nil
			}
			if jsonl != nil {
				defer jsonl.Close()
			}
			rec := recorder.New(s, jsonl)

			if err := s.StopSession(args[0]); err != nil {
				return err
			}
			// CLI-side SessionEnded event — distinct from the Claude "Stop"
			// hook, same reasoning as SessionRegistered vs SessionStart.
			_, _ = rec.RecordEvent(store.Event{
				SessionID: args[0],
				Type:      "SessionEnded",
				Timestamp: time.Now(),
				Payload:   `{"source":"cli"}`,
			})
			fmt.Printf("✓ Session %s stopped\n", args[0])
			return nil
		},
	}
}

func newSessionListCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sessions (running only unless --all)",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			sessions, err := s.ListSessions(!all)
			if err != nil {
				return err
			}
			if len(sessions) == 0 {
				fmt.Println("(no sessions)")
				return nil
			}
			fmt.Printf("%-20s  %-20s  %-8s  %s\n", "ID", "NAME", "STATE", "STARTED")
			for _, sess := range sessions {
				fmt.Printf("%-20s  %-20s  %-8s  %s\n",
					sess.ID, truncate(sess.Name, 20), string(sess.State),
					sess.StartedAt.Format("2006-01-02 15:04:05"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include stopped sessions")
	return cmd
}

func newSessionShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <session-id>",
		Short: "Show session detail and recent events",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			sess, err := s.GetSession(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("ID:         %s\n", sess.ID)
			fmt.Printf("Name:       %s\n", sess.Name)
			fmt.Printf("Agent:      %s\n", sess.AgentType)
			fmt.Printf("State:      %s\n", sess.State)
			fmt.Printf("CWD:        %s\n", sess.CWD)
			fmt.Printf("Started at: %s\n", sess.StartedAt.Format(time.RFC3339))
			if sess.StoppedAt != nil {
				fmt.Printf("Stopped at: %s\n", sess.StoppedAt.Format(time.RFC3339))
			}
			fmt.Println()
			fmt.Println("Recent events:")
			events, _ := s.ListEvents(store.EventFilter{SessionID: sess.ID, Limit: 20})
			if len(events) == 0 {
				fmt.Println("  (none)")
				return nil
			}
			for _, e := range events {
				fmt.Printf("  #%-5d  %s  %-18s  %s\n",
					e.ID,
					e.Timestamp.Format("15:04:05"),
					e.Type,
					truncate(e.Payload, 60),
				)
			}
			return nil
		},
	}
}

// ---------- hook ----------

func newHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "hook <hook-name>",
		Short: "Process a Claude Code hook event (reads JSON from stdin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, agmdir, err := openStore()
			if err != nil {
				// Hooks should almost never error out the agent — but if the
				// DB isn't there, the user hasn't run `agm init`. Fail loudly.
				return err
			}
			defer s.Close()

			jsonl, err := openJSONL(agmdir)
			if err != nil {
				fmt.Fprintln(os.Stderr, "warn: could not open events.jsonl:", err)
				jsonl = nil
			}
			if jsonl != nil {
				defer jsonl.Close()
			}

			h := &hook.Handler{
				Store:    s,
				Recorder: recorder.New(s, jsonl),
			}
			rowID, sessID, err := h.Process(args[0], os.Stdin)
			if err != nil {
				// Print to stderr but exit 0 so Claude doesn't see a broken hook.
				fmt.Fprintln(os.Stderr, "agm hook error:", err)
				return nil
			}
			fmt.Fprintf(os.Stderr, "agm: event #%d (%s) session=%s\n", rowID, args[0], orNone(sessID))
			return nil
		},
	}
}

// ---------- events ----------

func newEventsCmd() *cobra.Command {
	var (
		sessionID string
		limit     int
		tail      bool
	)
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Print recent events (optionally tail)",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()

			if tail {
				return runTail(s, sessionID)
			}

			events, err := s.ListEvents(store.EventFilter{
				SessionID: sessionID, Limit: limit,
			})
			if err != nil {
				return err
			}
			if len(events) == 0 {
				fmt.Println("(no events)")
				return nil
			}
			fmt.Printf("%-5s  %-19s  %-20s  %-18s  %s\n",
				"ID", "TIMESTAMP", "SESSION", "TYPE", "PAYLOAD")
			// Reverse so oldest is on top (ListEvents returns newest-first).
			for i := len(events) - 1; i >= 0; i-- {
				e := events[i]
				fmt.Printf("%-5d  %s  %-20s  %-18s  %s\n",
					e.ID,
					e.Timestamp.Format("2006-01-02 15:04:05"),
					orNone(e.SessionID),
					e.Type,
					truncate(e.Payload, 60),
				)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&sessionID, "session", "", "filter by session id")
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "max events to print")
	cmd.Flags().BoolVarP(&tail, "tail", "f", false, "follow — poll for new events")
	return cmd
}

// runTail polls for new events every 500ms. Dumb implementation — fine for MVP.
func runTail(s *store.Store, sessionID string) error {
	last, err := s.LastEventID()
	if err != nil {
		return err
	}
	fmt.Printf("Tailing events (session=%s). Ctrl+C to stop.\n", orNone(sessionID))

	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, os.Interrupt, syscall.SIGTERM)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-sigC:
			return nil
		case <-tick.C:
			evs, err := s.EventsSince(last, 200)
			if err != nil {
				fmt.Fprintln(os.Stderr, "tail error:", err)
				continue
			}
			for _, e := range evs {
				if sessionID != "" && e.SessionID != sessionID {
					last = e.ID
					continue
				}
				fmt.Printf("#%-5d  %s  %-20s  %-18s  %s\n",
					e.ID,
					e.Timestamp.Format("15:04:05"),
					orNone(e.SessionID),
					e.Type,
					truncate(e.Payload, 60),
				)
				last = e.ID
			}
		}
	}
}

// ---------- status ----------

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Summary of AGM state",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, agmdir, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()

			fmt.Printf("AGM %s\n", Version)
			fmt.Printf("Data dir: %s (%s)\n", agmdir, dirSize(agmdir))

			running, _ := s.ListSessions(true)
			all, _ := s.ListSessions(false)
			fmt.Printf("Sessions: %d running, %d total\n", len(running), len(all))

			recent, _ := s.CountRecentEvents(time.Hour)
			fmt.Printf("Events (last 1h): %d\n", recent)

			if fc, err := s.LastFileChange(); err == nil && fc != nil {
				fmt.Printf("Last file change: %s (%s %s)\n",
					fc.Timestamp.Format("15:04:05"), fc.Operation, fc.Path)
			}
			return nil
		},
	}
}

// ---------- small helpers ----------

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// dirSize returns a human-readable size for a directory (bytes, no recursion
// through symlinks).
func dirSize(dir string) string {
	var total int64
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	switch {
	case total >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(total)/(1<<20))
	case total >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(total)/(1<<10))
	default:
		return fmt.Sprintf("%d B", total)
	}
}

// unused import guard to keep io referenced when we later add piping helpers.
var _ = io.Discard
