// Package watcher wraps fsnotify with recursive directory discovery, simple
// path-prefix / glob ignore rules, and event normalisation.
//
// The watcher is agnostic about storage — callers attach an OnChange handler
// that decides what to do with each FileChange (log it, insert it into SQLite,
// etc.). This keeps the Watcher focused on I/O and ignore logic.
package watcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// Operation is the normalised op verb stored in file_changes.operation.
type Operation string

const (
	OpCreate Operation = "CREATE"
	OpWrite  Operation = "WRITE"
	OpRemove Operation = "REMOVE"
	OpRename Operation = "RENAME"
	OpChmod  Operation = "CHMOD"
)

// FileChange is an event emitted to OnChange.
type FileChange struct {
	AbsPath string
	RelPath string // relative to root
	Op      Operation
}

// OnChangeFunc handles a single file change. It must not block — the watcher
// serialises events and a slow handler will drop subsequent ones from fsnotify's
// internal buffer.
type OnChangeFunc func(FileChange)

// Config is passed to New.
type Config struct {
	Root            string   // directory to watch (absolute)
	IgnorePatterns  []string // path-prefix or glob patterns to skip
	OnChange        OnChangeFunc
	OnError         func(error) // optional; default logs to stderr
}

// Watcher is a long-running recursive file watcher.
type Watcher struct {
	cfg   Config
	fs    *fsnotify.Watcher
	close sync.Once
	done  chan struct{}
}

// DefaultIgnorePatterns returns the baseline ignore list.
func DefaultIgnorePatterns() []string {
	return []string{
		".git/",
		".agm/",
		"node_modules/",
		"target/",
		"dist/",
		"build/",
		".idea/",
		".vscode/",
		"__pycache__/",
	}
}

// New starts a recursive watcher rooted at cfg.Root. Caller must Close() it.
func New(cfg Config) (*Watcher, error) {
	if cfg.Root == "" {
		return nil, fmt.Errorf("root required")
	}
	if !filepath.IsAbs(cfg.Root) {
		abs, err := filepath.Abs(cfg.Root)
		if err != nil {
			return nil, fmt.Errorf("abs(root): %w", err)
		}
		cfg.Root = abs
	}
	if cfg.OnChange == nil {
		return nil, fmt.Errorf("OnChange required")
	}
	if cfg.OnError == nil {
		cfg.OnError = func(err error) {
			fmt.Fprintln(os.Stderr, "agm watcher error:", err)
		}
	}

	fs, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify.NewWatcher: %w", err)
	}
	w := &Watcher{
		cfg:  cfg,
		fs:   fs,
		done: make(chan struct{}),
	}
	if err := w.addRecursive(cfg.Root); err != nil {
		_ = fs.Close()
		return nil, err
	}
	go w.run()
	return w, nil
}

// Close stops the watcher. Safe to call multiple times.
func (w *Watcher) Close() error {
	var err error
	w.close.Do(func() {
		close(w.done)
		err = w.fs.Close()
	})
	return err
}

// addRecursive walks root and subscribes to every directory not matching an
// ignore rule. Hidden directories (starting with ".") are skipped unless
// explicitly rooted.
func (w *Watcher) addRecursive(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Walking errors are surfaced but don't abort the whole walk.
			w.cfg.OnError(fmt.Errorf("walk %s: %w", path, err))
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if path == root {
			return w.fs.Add(path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if w.shouldIgnore(rel, true) {
			return filepath.SkipDir
		}
		// Skip hidden directories (".git", ".vscode" etc. even if not in
		// ignore list) — agent edits don't usually live there.
		base := filepath.Base(path)
		if strings.HasPrefix(base, ".") && base != "." && base != ".." {
			return filepath.SkipDir
		}
		if err := w.fs.Add(path); err != nil {
			w.cfg.OnError(fmt.Errorf("watch %s: %w", path, err))
		}
		return nil
	})
}

// shouldIgnore checks rel (forward-slash form) against the ignore patterns.
// isDir hints whether rel refers to a directory (affects trailing-slash rules).
func (w *Watcher) shouldIgnore(rel string, isDir bool) bool {
	// Normalise to forward slashes for pattern matching on Windows.
	slashed := filepath.ToSlash(rel)
	for _, pat := range w.cfg.IgnorePatterns {
		if pat == "" {
			continue
		}
		if strings.HasSuffix(pat, "/") {
			prefix := strings.TrimSuffix(pat, "/")
			if slashed == prefix || strings.HasPrefix(slashed, prefix+"/") {
				return true
			}
			if isDir && slashed == prefix {
				return true
			}
			continue
		}
		if ok, _ := filepath.Match(pat, filepath.Base(slashed)); ok {
			return true
		}
		if ok, _ := filepath.Match(pat, slashed); ok {
			return true
		}
	}
	return false
}

// run is the fsnotify event loop. It handles three tasks:
//  1. dispatching file events to OnChange
//  2. following directory creations (add them to the watch set)
//  3. propagating errors via OnError
func (w *Watcher) run() {
	for {
		select {
		case <-w.done:
			return
		case err, ok := <-w.fs.Errors:
			if !ok {
				return
			}
			w.cfg.OnError(err)
		case ev, ok := <-w.fs.Events:
			if !ok {
				return
			}
			w.handle(ev)
		}
	}
}

func (w *Watcher) handle(ev fsnotify.Event) {
	// Resolve rel path for ignore checks and the emitted FileChange.
	rel, err := filepath.Rel(w.cfg.Root, ev.Name)
	if err != nil {
		// Outside the root — ignore.
		return
	}
	// fsnotify can deliver events for the root dir itself — skip.
	if rel == "." {
		return
	}

	// Check if it's a directory (only on CREATE). If so, watch it too.
	if ev.Op.Has(fsnotify.Create) {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			if !w.shouldIgnore(rel, true) {
				_ = w.fs.Add(ev.Name)
			}
		}
	}

	if w.shouldIgnore(rel, false) {
		return
	}

	op := normalizeOp(ev.Op)
	if op == "" {
		return
	}
	w.cfg.OnChange(FileChange{
		AbsPath: ev.Name,
		RelPath: filepath.ToSlash(rel),
		Op:      op,
	})
}

func normalizeOp(op fsnotify.Op) Operation {
	switch {
	case op.Has(fsnotify.Create):
		return OpCreate
	case op.Has(fsnotify.Write):
		return OpWrite
	case op.Has(fsnotify.Remove):
		return OpRemove
	case op.Has(fsnotify.Rename):
		return OpRename
	case op.Has(fsnotify.Chmod):
		return OpChmod
	}
	return ""
}
