# AGM — Agent Management

AGM is an orchestration layer for AI coding agents. The long-term goal is to let multiple agents work concurrently on the same repository without stepping on each other — through lightweight virtual branches, hunk-level attribution, and conflict detection built on top of Git primitives.

This repository is the **early-stage** version, currently at MVP v0.0.1: an event recorder that observes what a single Claude Code agent does in a repository and stores it to a local SQLite database and a JSONL log. Higher-level orchestration (virtual branches, merging, multi-agent scheduling) is deferred to later milestones.

## Repository layout

```
.
├── agm/              # Go implementation (v0.0.1 — the event recorder)
│   ├── cmd/agm/      # CLI entry point
│   ├── internal/     # hook / id / store / watcher packages
│   ├── examples/     # Claude Code hook config
│   └── README.md     # build & usage instructions
└── AGM_Design/       # Reference documentation (subset published)
    └── AGM/
        ├── mvp-design.md   # v0.0.1 specification
        └── 概念解释.md      # glossary / concept primer
```

See [`agm/README.md`](agm/README.md) for build instructions, CLI commands, and Claude Code hook integration.

## Status

| Version | Scope |
|---|---|
| **v0.0.1** (current) | Event recorder — file watcher + hook receiver + SQLite / JSONL storage |
| v0.1.0 (planned) | go-git: orphan branch metadata + commit trailer |
| v0.2.0 (planned) | Single-agent virtual branch |
| v0.3.0+ (planned) | Multi-agent, conflict handling, token budget, handover notes |

## License

MIT (LICENSE file to be added before public release).
