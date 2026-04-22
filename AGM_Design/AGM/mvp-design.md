# AGM MVP v0.0.1 设计文档

> 设计时间：2026-04-22
> 本文件定义 AGM **首个可运行版本**（v0.0.1）的范围、架构、接口与构建方法。
> 配合阅读：`beta-plan.md`（整体 12 周路线）、`概念解释.md`（术语）。

---

## 一、MVP 目标：最小可下载、可命令行运行的版本

v0.0.1 要解决的单一问题：**让一个用户下载 `agm` 后，能在自己电脑上启动它、看到 Claude Code 的行为被记录下来**。

其它一切（虚拟分支、多 agent、冲突处理、归属引擎、handover）**全部延后**。

### 验收标准（v0.0.1 算成功的标志）

1. 用户执行 `go install github.com/<user>/agm-mvp/cmd/agm@latest` 或从 release 下载二进制，命令行能直接跑 `agm --version`
2. 在任意目录运行 `agm init`，能看到 `.agm/` 目录被创建，内含 SQLite 库和事件日志
3. 运行 `agm watch`，修改当前目录任意文件，事件被写入 `.agm/events.jsonl` 和 SQLite
4. 从 stdin 喂 JSON 给 `agm hook SessionStart`，事件被正确记录
5. `agm status` 能打印当前 session、文件变更统计
6. 在 Windows 11、Linux、macOS 上都能构建并运行

### 明确不做的事

- ❌ 虚拟分支 / merge / 归属判定（β plan P1+ 的事）
- ❌ 冲突检测
- ❌ 多 agent 编排调度
- ❌ Orphan branch / commit trailer 写入（预留 hook 但不实现）
- ❌ TUI 界面 / 看板可视化
- ❌ Token 熔断逻辑（只记录，不干预）
- ❌ 跨项目知识传承

v0.0.1 就是一个**事件记录器**，后续所有功能都建立在它之上。

---

## 二、技术栈

| 层 | 选型 | 理由 |
|---|---|---|
| 语言 | **Go 1.25+** | 跨平台 single-binary，Windows 友好 |
| CLI 框架 | **cobra** (`github.com/spf13/cobra`) | 事实标准，自动 help / 补全 |
| SQLite | **`modernc.org/sqlite`** | **纯 Go**，无需 cgo / gcc，Windows 编译零痛 |
| 文件监听 | **fsnotify** (`github.com/fsnotify/fsnotify`) | 跨平台封装 |
| 结构化日志 | `log/slog`（标准库） | 无依赖 |
| ID 生成 | `crypto/rand`（标准库） | 无依赖 |
| Git 操作 | **v0.0.1 不引入**（go-git 放 v0.0.2） | 减少初期依赖，先跑通骨架 |

### 为什么选 `modernc.org/sqlite` 而不是 `mattn/go-sqlite3`

`mattn/go-sqlite3` 通过 cgo 包装 C 版 SQLite，在 Windows 上需要 gcc（MinGW 或 MSYS2），交叉编译也麻烦。`modernc.org/sqlite` 是 SQLite C 代码的 Go 自动翻译版本，**纯 Go**，`go build` 直接出 Windows 二进制，代价是稍慢（对 AGM 场景无感）。

---

## 三、目录结构

### 代码仓库

```
agm-mvp/
├── go.mod
├── go.sum
├── README.md
├── LICENSE
├── .gitignore
├── cmd/
│   └── agm/
│       └── main.go            # CLI 入口，注册 cobra 命令
├── internal/
│   ├── store/
│   │   ├── store.go           # SQLite 封装：open/close/迁移
│   │   ├── schema.go          # SQL schema 常量
│   │   ├── sessions.go        # session 增删改查
│   │   ├── events.go          # 事件写入/查询
│   │   └── filechanges.go     # 文件变更写入/查询
│   ├── watcher/
│   │   └── watcher.go         # fsnotify 包装，产 FileChange 事件
│   ├── hook/
│   │   └── hook.go            # 读 stdin JSON，归一化后落库
│   └── id/
│       └── id.go              # 生成 session_id（短 UUID）
└── examples/
    └── claude-hooks.json      # 示例 Claude Code hook 配置
```

### 运行时（`.agm/` 目录）

用户在任意 repo 里 `agm init`，生成：

```
<cwd>/.agm/
├── config.json                # AGM 配置
├── state.db                   # SQLite 库（WAL 模式）
├── state.db-wal               # WAL 文件（SQLite 自动创建）
├── state.db-shm               # 共享内存（SQLite 自动创建）
├── events.jsonl               # 人类可读事件日志（append-only）
└── logs/
    └── agm.log                # AGM 自己的运行日志
```

**设计原则**：
- `state.db` 是结构化存储，查询用
- `events.jsonl` 是人类可读副本，任何编辑器都能看
- 两者互为冗余，损坏一个能从另一个恢复

---

## 四、SQLite 数据模型

### 表结构

```sql
-- schema_version: 追踪 schema 版本，未来做迁移
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);

-- sessions: 每个 Claude Code / 其它 agent 会话一行
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,                -- sess_<12 hex>
    name TEXT NOT NULL,                 -- 人类可读名
    agent_type TEXT NOT NULL DEFAULT 'claude-code',
    started_at INTEGER NOT NULL,        -- unix ms
    stopped_at INTEGER,                 -- 结束时间，running 时为 NULL
    state TEXT NOT NULL DEFAULT 'running',  -- running | stopped | error
    cwd TEXT NOT NULL,                  -- session 启动时的工作目录
    metadata TEXT                       -- JSON blob，存 transcript path 等
);

CREATE INDEX IF NOT EXISTS idx_sessions_state ON sessions(state);
CREATE INDEX IF NOT EXISTS idx_sessions_started ON sessions(started_at);

-- events: 所有 agent 生命周期事件 + 文件变更事件的统一流水
CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT,                    -- 关联 session，FileChange 可能为 NULL
    event_type TEXT NOT NULL,           -- Claude hook: SessionStart|UserPromptSubmit|PostToolUse|Stop
                                        -- CLI admin:   SessionRegistered|SessionEnded
                                        -- Watcher:     FileChange
    timestamp INTEGER NOT NULL,         -- unix ms
    payload TEXT NOT NULL               -- JSON blob，原样保存
);

CREATE INDEX IF NOT EXISTS idx_events_session ON events(session_id);
CREATE INDEX IF NOT EXISTS idx_events_type ON events(event_type);
CREATE INDEX IF NOT EXISTS idx_events_ts ON events(timestamp);

-- file_changes: fsnotify 捕捉到的文件变更（冗余存储，便于查询）
CREATE TABLE IF NOT EXISTS file_changes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT,                    -- active session，可为 NULL
    path TEXT NOT NULL,                 -- 相对 cwd 的路径
    operation TEXT NOT NULL,            -- CREATE|WRITE|REMOVE|RENAME|CHMOD
    timestamp INTEGER NOT NULL          -- unix ms
);

CREATE INDEX IF NOT EXISTS idx_fc_session ON file_changes(session_id);
CREATE INDEX IF NOT EXISTS idx_fc_path ON file_changes(path);
```

### PRAGMA 配置

连接建立后立即执行：

```sql
PRAGMA journal_mode = WAL;      -- 并发读写；Windows 文件锁友好
PRAGMA synchronous = NORMAL;    -- WAL 下 NORMAL 足够安全
PRAGMA foreign_keys = ON;       -- 启用外键约束
PRAGMA busy_timeout = 5000;     -- 锁等待 5s
```

---

## 五、CLI 命令设计

所有命令默认在 cwd 下的 `.agm/` 工作。`--agm-dir <path>` 可覆盖。

### `agm init`

在 cwd 下创建 `.agm/` 目录，初始化 SQLite schema，写入默认 config。

```
$ agm init
✓ Created .agm/
✓ Initialized .agm/state.db (schema v1)
✓ Wrote .agm/config.json
AGM ready. Next:
  agm watch          # start watching this directory
  agm session start  # register a new session
```

**幂等**：再次运行不破坏已有数据，只打印 "already initialized"。

### `agm watch`

前台运行 fsnotify 监听，捕获的每个事件：
1. 写入 `.agm/events.jsonl`（追加一行 JSON）
2. 插入 `file_changes` 表
3. 打印到 stdout（可选 `--quiet`）

```
$ agm watch
Watching D:\project (recursive)
Press Ctrl+C to stop
[12:34:56] WRITE  src/main.go
[12:34:58] CREATE docs/new.md
```

**递归监听**：v0.0.1 监听 cwd 及所有子目录（`filepath.Walk` 批量 `Add`）。
**忽略规则**：`.git/`、`node_modules/`、`.agm/` 自身、以 `.` 开头的隐藏目录默认忽略。

### `agm session start <name>`

注册一个新 session，打印 session_id。不阻塞（只是数据库 insert）。

```
$ agm session start "fix-login-bug"
sess_a3b2c4d5e6f7
```

`session_id` 输出到 stdout，方便 shell 脚本捕获（`SID=$(agm session start foo)`）。

### `agm session stop <session_id>`

标记 session 为 stopped，记录 stopped_at。

```
$ agm session stop sess_a3b2c4d5e6f7
✓ Session sess_a3b2c4d5e6f7 stopped
```

### `agm session list [--all]`

默认只列 `state=running`。`--all` 列所有。

```
$ agm session list
ID                  NAME            STATE    STARTED
sess_a3b2c4d5e6f7   fix-login-bug   running  12:34:56
```

### `agm session show <session_id>`

打印 session 详情 + 最近 20 条事件。

### `agm hook <hook-name>`

从 stdin 读 JSON，写入 events 表。hook-name 会作为 `event_type`。

**标准使用**（Claude Code 会自动这么调用）：
```
$ echo '{"session_id":"foo","cwd":"/tmp"}' | agm hook SessionStart
✓ Event recorded (id=42)
```

**查找 session_id 的策略**（v0.0.1 简化版）：
1. 如果 payload JSON 里有 `session_id`/`sess_id`，直接用
2. 否则尝试用**最近一个 running 的 session**（赌单 agent 场景）
3. 都找不到就记录为孤立事件（session_id=NULL）

未来版本：通过环境变量 `AGM_SESSION_ID` 或 payload 显式传入。

### `agm events [--session <id>] [--tail] [--limit N]`

查询事件。`--tail` 持续输出新事件（类似 `tail -f`）。

```
$ agm events --limit 5
ID   TS                 SESSION            TYPE             PAYLOAD (preview)
42   12:34:56.123       sess_a3b2c4d5e6f7  SessionStart     {"cwd":"/tmp"}
41   12:34:50.001       (none)             FileChange       {"path":"src/main.go",...}
```

### `agm status`

总览：

```
$ agm status
AGM 0.0.1
Data dir: D:\project\.agm (size: 124 KB)
Sessions: 1 running, 3 stopped
Recent events (last 1h): 47
Last file change: 12:34:58 (src/main.go)
```

### `agm --version` / `agm -v`

打印版本号（编译时 `-ldflags "-X main.Version=..."` 注入）。

---

## 六、Hook 事件数据流

### 完整链路（Claude Code 场景）

```
Claude Code 触发 hook
    → shell 执行 `agm hook SessionStart`
    → agm 从 stdin 读 JSON payload
    → agm 查/补 session_id
    → 落库（events 表）+ 追加 events.jsonl
    → 退出码 0（hook 成功，Claude 继续）
```

### payload 归一化

Claude Code 各 hook 传的 JSON 结构不同（有 `cwd`、`tool_name`、`transcript_path` 等）。v0.0.1 **不解析字段**，整段 JSON 原样存 `payload` 列。后续版本做结构化抽取。

### 错误处理

- stdin 读空：记录 `event_type=<hook>, payload={}`，不报错
- JSON 解析失败：记录原始字符串（用 `{"_raw": "..."}` 包装），退出码仍为 0（**不阻断 Claude**）
- DB 写入失败：stderr 打印错误，退出码 1（Claude 会看到错误但通常不会停）

**原则：hook 必须在 200ms 内返回**。AGM 是观察者，不是看门狗。

---

## 七、配置文件

`.agm/config.json`：

```json
{
  "version": "0.0.1",
  "schema_version": 1,
  "watcher": {
    "ignore_patterns": [
      ".git/",
      ".agm/",
      "node_modules/",
      "target/",
      "dist/",
      "*.log"
    ],
    "poll_interval_ms": 0
  },
  "hooks": {
    "auto_detect_session": true
  }
}
```

v0.0.1 只读取结构，不提供 CLI 修改命令（手动编辑即可）。

---

## 八、Claude Code 集成配置

在 `~/.claude/settings.json`（或项目 `.claude/settings.json`）加：

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

（`agm` 必须在 `PATH` 里。）

同样的内容放在 `agm-mvp/examples/claude-hooks.json` 供用户参考。

---

## 九、构建与分发

### 本地构建

```bash
git clone https://github.com/<user>/agm-mvp.git
cd agm-mvp
go build -o agm ./cmd/agm     # Linux/macOS
go build -o agm.exe ./cmd/agm # Windows
```

### 一键安装（推荐）

```bash
go install github.com/<user>/agm-mvp/cmd/agm@latest
# 二进制出现在 $GOPATH/bin 或 $HOME/go/bin
```

### 交叉编译（发布用）

```bash
# Windows
GOOS=windows GOARCH=amd64 go build -o dist/agm-windows-amd64.exe ./cmd/agm

# Linux
GOOS=linux GOARCH=amd64 go build -o dist/agm-linux-amd64 ./cmd/agm

# macOS (Intel)
GOOS=darwin GOARCH=amd64 go build -o dist/agm-darwin-amd64 ./cmd/agm

# macOS (Apple Silicon)
GOOS=darwin GOARCH=arm64 go build -o dist/agm-darwin-arm64 ./cmd/agm
```

因为用的是 `modernc.org/sqlite`（纯 Go），以上命令全部无需 cgo、无需 C 编译器。

### 版本注入

```bash
VERSION=$(git describe --tags --always 2>/dev/null || echo "dev")
go build -ldflags "-X main.Version=$VERSION" -o agm ./cmd/agm
```

---

## 十、测试策略

### 单元测试（v0.0.1 覆盖）

- `internal/store/*_test.go`：SQLite CRUD + schema 迁移
- `internal/id/id_test.go`：ID 生成唯一性 & 格式
- `internal/hook/hook_test.go`：payload 归一化、session_id 查找

目标覆盖率：**核心包 > 70%**。

### 端到端手测脚本

`scripts/smoke-test.sh`：
```bash
# 1. init 一个临时目录
tmpdir=$(mktemp -d)
cd $tmpdir
agm init

# 2. 启 watcher（后台）
agm watch &
WATCH_PID=$!

# 3. 创建文件 → 应被记录
echo "hello" > test.txt
sleep 1

# 4. 模拟 hook
SID=$(agm session start smoke-test)
echo "{\"session_id\":\"$SID\"}" | agm hook SessionStart
echo "{\"tool_name\":\"Edit\"}" | agm hook PostToolUse
agm session stop $SID

# 5. 验证
agm events --session $SID | grep -q SessionStart || exit 1
agm events --session $SID | grep -q PostToolUse || exit 1

kill $WATCH_PID
echo "✓ smoke test passed"
```

### Windows 特定测试

- fsnotify 对 `ReadDirectoryChangesW` 的事件合并/丢失要在 CI 跑 ≥ 1000 个文件的压力测试
- 长路径：准备一个 `MAX_PATH` 以上的嵌套目录，验证能正常监听

---

## 十一、已知局限（v0.0.1 就不修）

| 局限 | 原因 | 计划版本 |
|---|---|---|
| watcher 与 session 的关联靠"最近 running session"启发式 | MVP 简化 | v0.0.2（hook 显式传 session） |
| 递归监听大仓库可能慢 | 未做增量加载 | v0.0.3（lazy watch + gitignore 集成） |
| 无 gitignore 支持（自己维护 ignore_patterns） | 依赖最少 | v0.0.3 |
| 无 Claude Code transcript 解析 | payload 原样存 | v0.0.2 |
| 无虚拟分支 | MVP 范围外 | v0.1.0（β plan P1） |
| 无 TUI | MVP 范围外 | v0.2.0 |

---

## 十二、v0.0.1 之后的演进

```
v0.0.1  MVP：事件记录器 ← 当前
v0.0.2  Transcript 解析 + session_id 显式化
v0.0.3  gitignore + lazy watch + 性能优化
v0.1.0  go-git 集成：orphan branch 元数据写入 + commit trailer（β plan P1）
v0.2.0  单 agent 虚拟分支（β plan P2）
v0.3.0  多 agent 无冲突（β plan P3）
v0.4.0  冲突处理（β plan P4）
v0.5.0  差异化层：token 熔断 + handover note（β plan P5）
v1.0.0  稳定 API + 文档 + 跨平台安装器
```

---

## 十三、后续文档需求

- `README.md`（仓库根）：快速上手、安装、典型用法
- `docs/hooks.md`：Claude Code hook 详解
- `docs/events-schema.md`：events.jsonl 格式规范
- `docs/troubleshooting.md`：常见问题（Windows 长路径、fsnotify 丢事件、SQLite 锁）

以上在 v0.0.1 仓库建立时一起提供。
