# AGM — Agent Management (MVP)

AGM v0.0.1 is a command-line **event recorder** for AI coding agents. It watches a repository, listens to Claude Code hooks, and stores everything to a local SQLite database and a human-readable JSONL log.

This is the minimum viable slice of the β plan (see `../AGM/beta-plan.md`). It does **not** yet do virtual branches, merging, conflict detection, or multi-agent orchestration — that's v0.1+.

## Why this exists

Before AGM can orchestrate agents, it has to be able to **observe** them. v0.0.1 is that observer: it gives you a local, queryable history of every file touched and every hook fired, so you can trust the data before building anything on top.

## Install

### From source (recommended for MVP)

Requires Go 1.25+.

```bash
git clone <repo>
cd agm-mvp
go build -o agm ./cmd/agm    # Linux / macOS
go build -o agm.exe ./cmd/agm # Windows
```

Or directly:

```bash
go install github.com/agm-project/agm-mvp/cmd/agm@latest
```

The binary is pure Go — no cgo, no C compiler, no SQLite system library needed.

### Verify

```bash
./agm --version
# agm version 0.0.1-dev
```

Add the binary to your `PATH` so Claude Code hooks can invoke it.

## Quick start

```bash
cd /path/to/your/repo
agm init

# terminal 1: watch the directory
agm watch

# terminal 2: register a session and record some events
SID=$(agm session start "fix-login-bug")
echo '{"tool_name":"Edit"}' | agm hook PostToolUse
agm events --session $SID
agm session stop $SID
```

## Commands

| Command | What it does |
|---|---|
| `agm init` | Create `.agm/` in the current directory |
| `agm watch` | Foreground file-system watcher |
| `agm session start <name>` | Register a session, print the id |
| `agm session stop <id>` | Mark the session stopped |
| `agm session list [--all]` | List sessions |
| `agm session show <id>` | Detail + last 20 events |
| `agm hook <name>` | Process a hook payload from stdin |
| `agm events [--session <id>] [--tail] [-n 50]` | Print / follow events |
| `agm status` | One-screen summary |

All commands take `--agm-dir <path>` to override the default `./.agm`.

## Claude Code integration

Add to `~/.claude/settings.json` (or your project's `.claude/settings.json`):

```json
{
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "agm hook SessionStart"}]}
    ],
    "UserPromptSubmit": [
      {"hooks": [{"type": "command", "command": "agm hook UserPromptSubmit"}]}
    ],
    "PostToolUse": [
      {"hooks": [{"type": "command", "command": "agm hook PostToolUse"}]}
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": "agm hook Stop"}]}
    ]
  }
}
```

See `examples/claude-hooks.json` for the same content ready to copy.

AGM auto-attaches hook events to the most recent running session. For deterministic attachment, pass an explicit session id via `AGM_SESSION_ID`:

```bash
export AGM_SESSION_ID=$(agm session start my-task)
claude  # every hook fires with AGM_SESSION_ID in env
```

## What's in `.agm/`

```
.agm/
├── config.json       # ignore patterns, version
├── state.db          # SQLite (WAL mode)
├── events.jsonl      # human-readable append-only event log
└── logs/
```

Everything is local. No network, no background daemon. Safe to commit `events.jsonl` to git if you want an audit trail, or add `.agm/` to `.gitignore` if you don't.

## Platform notes

- **Windows**: pure Go build, no MSYS2/MinGW needed. `fsnotify` uses `ReadDirectoryChangesW`; under heavy load some events may be dropped — we consider this acceptable for v0.0.1.
- **Linux**: uses `inotify`. The default kernel `fs.inotify.max_user_watches` (8192) can run out on large trees — increase via `sudo sysctl fs.inotify.max_user_watches=524288`.
- **macOS**: uses `FSEvents`.

## Roadmap

```
v0.0.1  ← you are here  event recorder
v0.0.2  transcript parsing, explicit AGM_SESSION_ID propagation
v0.0.3  .gitignore integration, lazy watch for large trees
v0.1.0  go-git: orphan branch metadata + commit trailer
v0.2.0  single-agent virtual branch (β plan P2)
v0.3.0  multi-agent without conflicts (β plan P3)
v0.4.0  conflict handling (β plan P4)
v0.5.0  token budget, handover note (β plan P5)
```

See `../AGM/beta-plan.md` for the full β plan, `../AGM/mvp-design.md` for the v0.0.1 spec, and `../AGM/概念解释.md` for the concept primer (Chinese).

## License

MIT (add LICENSE file before release).
