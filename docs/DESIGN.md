# AgentReRoot Design

Status: draft, v0.1 scope.

## 1. Problem

A long-lived `~/workspace` drifts. Projects get created under a category that
later stops fitting, two or three directories end up solving the same problem,
and many projects quietly go dormant. Reorganizing by hand is tedious, and today
it is also *destructive*: coding agents such as Claude Code and Codex key their
sessions, memory, trust settings and indexes by absolute path. A plain `mv`
orphans all of that agent state.

AgentReRoot (`reroot`) reorganizes a workspace **and** carries the agents'
working memory along with every move, merge and archive.

## 2. Principles

1. **The agent is the brain, the CLI is the hands.** `reroot` itself contains no
   LLM. It produces facts (`scan`) and executes plans (`apply`)
   deterministically. Judgement — "this is really a security project", "these
   three directories should be merged" — is made by an agent (or a human) that
   reads the scan output and writes a plan. This keeps the tool predictable,
   testable and model-agnostic, and it is a natural fit for agent tool use.
2. **Plans are data.** A plan is a YAML/JSON file. It can be reviewed, edited,
   committed and re-run. Every future front end (TUI, GUI) is just another plan
   editor.
3. **Dry-run by default, journal always.** `apply` only prints what it would do
   unless `--yes` is given. Every mutation is written to a write-ahead journal
   first, and `reroot undo` replays the journal backwards.
4. **Adapters own agent knowledge.** Each agent (Claude Code, Codex, ...) is an
   adapter that knows where that agent stores path-keyed state and how to
   rewrite it. New agents are added without touching the core.
5. **Machine-readable everything.** All commands accept `--json`.

## 3. Workflow

```
reroot scan ~/workspace --json > scan.json     # facts
<agent reads scan.json, writes plan.yaml>       # judgement
reroot plan check plan.yaml                     # validation
reroot apply plan.yaml                          # dry run: fs + agent changes
reroot apply plan.yaml --yes                    # execute, journaled
reroot verify ~/workspace                       # find orphaned / stale state
reroot undo <journal-id>                        # roll back if needed
```

A Claude Code skill (`skills/reroot/SKILL.md`) teaches an agent this loop.

## 4. Scan

`scan` walks a root directory and classifies directories into:

- **project** — contains `.git`, a build/manifest marker (`go.mod`,
  `package.json`, `pyproject.toml`, `Cargo.toml`, `CMakeLists.txt`, `Makefile`,
  `*.tex`, ...), an agent instruction file (`CLAUDE.md`, `AGENTS.md`), or has
  agent state recorded for it. Scanning does not descend into a project.
- **group** — an intermediate directory (a "category") that contains projects.

For each project it reports: markers, dominant languages (by file extension),
size, git facts (last commit, commits in the last 90 days, remotes, dirty,
worktrees), README title/first paragraph, and agent footprints (session count,
memory files, last agent activity) from every adapter.

From those it derives:

- **last activity** = max(last commit, newest file mtime, last agent session)
- **activity label**: `active` (< 30 days), `warm` (< 90), `dormant` (< 365),
  `stale` (older)
- **duplicate hints**: same git remote, same normalized name, high name-token
  overlap. These are hints for the agent, never acted on automatically.
- **warnings**: things a move may break that `reroot` does not fix (Python
  virtualenvs with absolute shebangs, nested git repos, ...).

## 5. Plan format

```yaml
version: 1
root: /Users/me/workspace          # relative paths resolve against this
operations:
  - op: move
    from: ai/sec-scanner
    to: security/sec-scanner
    reason: "Project evolved from an LLM demo into a security tool"
  - op: merge
    sources: [ai/paper-bot, tools/paper-helper]
    into: research/paper-tools
    reason: "Two attempts at the same problem"
  - op: archive
    path: misc/old-demo
    to: _archive/misc/old-demo      # optional; default _archive/<path>
```

Operations run **in order**; each one sees the result of the previous one.

- `move` (also covers rename): `from` must exist, `to` must not.
- `merge`: each source is moved to `into/<basename>` (`into` is created if
  needed). Agent memory of every source is additionally copied into the memory
  of `into`, with provenance, so an agent started at `into` sees all of it.
  Merging *code* inside one tree is left to the agent afterwards; `reroot` only
  guarantees that nothing is lost.
- `archive`: a move into an archive area (default `<root>/_archive/`).

`plan check` validates: paths are inside `root`, sources exist, targets do not
exist, no target lies inside its own source, no two operations collide.

## 6. Apply and the journal

Journals live in `$REROOT_HOME` (default `~/.reroot`) under
`journal/<id>/`, containing `journal.jsonl` and a `backup/` folder.

Before each mutation an entry is appended and fsynced:

| kind           | effect                                  | undo                         |
|----------------|-----------------------------------------|------------------------------|
| `rename`       | rename a file or directory              | rename back                  |
| `mkdir`        | create a directory                      | remove it if empty           |
| `write`        | replace a file's content                | restore the backed-up copy   |
| `create`       | create a new file                       | delete it                    |
| `sql-prefix`   | rewrite a path prefix in a SQLite column | inverse prefix rewrite       |

Cross-device moves are refused in v0.1 (no partial copies).

## 7. Agent adapters

An adapter implements:

```go
type Adapter interface {
    Name() string
    Available() bool
    Footprint(projectPath string) Footprint          // used by scan
    Migrate(tx *Tx, oldPath, newPath string) error  // used by apply
    MergeMemory(tx *Tx, sources []string, into string) error
    Verify(root string) []Finding                    // used by verify
}
```

All path rewrites are **prefix rewrites with a path boundary**: `/a/b` matches
`/a/b` and `/a/b/c` but never `/a/bc`.

### 7.1 Claude Code

Location: `$CLAUDE_CONFIG_DIR` or `~/.claude`; global config `~/.claude.json`.

| State | Rewrite |
|---|---|
| `projects/<encoded-cwd>/` (sessions `*.jsonl`, `memory/`) | rename dir; the encoding replaces every non-alphanumeric character with `-`. If the target dir exists, contents are merged file by file. |
| session `*.jsonl` of the moved projects | JSON-string prefix rewrite (`cwd` and all paths), so `--resume` works at the new location. Transcripts of *other* projects are left alone: they only mention the path as history and may belong to a running session. |
| `~/.claude.json` `projects` keys, `githubRepoPaths` values | JSON-string prefix rewrite |
| `history.jsonl` `"project"` fields | JSON-string prefix rewrite |
| `memory/*.md` of all projects, `~/.claude/CLAUDE.md`, `CLAUDE.md`/`CLAUDE.local.md` in the moved tree | text prefix rewrite of absolute paths |

The mapping from a project directory back to its path is read from the `cwd`
fields of its sessions, then from the keys of `~/.claude.json`, and finally by
matching the encoded name against the filesystem (the encoding is lossy, so it
is never decoded blindly).
Directories prefixed `ssh-` belong to remote hosts and are ignored.

### 7.2 Codex

Location: `$CODEX_HOME` or `~/.codex`.

| State | Rewrite |
|---|---|
| `sessions/**/rollout-*.jsonl`, `archived_sessions/` whose `session_meta.payload.cwd` is in the moved tree | JSON-string prefix rewrite |
| `state_*.sqlite` `threads.cwd`, `project_roots.path` | SQL prefix rewrite |
| `config.toml` `[projects."<path>"]` | string prefix rewrite |
| `AGENTS.md` inside the project | text prefix rewrite |

### 7.3 Git

Not an agent, but path-keyed too: after a move, `git worktree repair` is run in
repositories that have linked worktrees.

## 8. Safety

- Agents rewrite their own state files while running. `apply` warns when
  `claude` or `codex` processes are running; for best results close other
  sessions first and run `reroot verify` afterwards.
- Remote (`ssh-`) and unknown state is never touched.
- `undo` refuses to run if a target it would restore has been modified since
  (content hash mismatch), unless `--force`.

## 9. Platform support

macOS and Linux in v0.1. Path handling is isolated in `internal/pathx` so that
Windows (drive letters, `\` separators, case-insensitive matching) can be added
later. The binary is a single static Go executable (pure-Go SQLite driver, no
cgo).

## 10. Roadmap

- v0.1: scan, plan check, apply, undo, verify; Claude Code, Codex and git adapters.
- v0.2: MCP server (`reroot mcp`) exposing scan/check/apply as tools; Cursor and
  Gemini CLI adapters; cross-device moves.
- v0.3: interactive TUI plan editor; Windows.
- later: GUI.
