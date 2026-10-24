# AgentReRoot

**Reorganize your workspace with AI agents - without making them forget.**

Workspaces drift. A project created under `ai/` turns into a security tool,
three folders end up solving the same problem, half the projects go quiet.
Cleaning up used to be tedious; with coding agents it is also destructive:
Claude Code and Codex key sessions, memory and trust settings by absolute
path, so a plain `mv` orphans all of it.

`reroot` is a small CLI that

- **scans** a workspace and reports facts per project (languages, git activity,
  README, agent sessions and memory, activity label, likely duplicates),
- **applies** a reviewed plan of `move` / `merge` / `archive` operations,
- **migrates agent state** with every move: Claude Code project state,
  transcripts, memory, `~/.claude.json`; Codex rollouts, state database,
  `config.toml`; `CLAUDE.md` / `AGENTS.md`; git worktree links,
- **journals** every change, so `reroot undo` puts everything back.

It contains no LLM. The intended "brain" is your coding agent: it reads the
scan, proposes a plan, shows you the dry run, and applies it after you agree.

## Install

```bash
go install github.com/xjtuwxg/AgentReRoot/cmd/reroot@latest
```

Single static binary, macOS and Linux. Windows is planned.

## Quick start

```bash
reroot scan ~/workspace                 # human-readable overview
reroot scan ~/workspace --json          # for agents
reroot plan example > plan.yaml         # edit it
reroot plan check plan.yaml
reroot apply plan.yaml                  # dry run: shows every change
reroot apply plan.yaml --yes            # do it (journaled)
reroot verify ~/workspace               # find orphaned / stale agent state
reroot history
reroot undo                             # roll back the last apply
```

A plan:

```yaml
version: 1
root: ~/workspace
operations:
  - op: move
    from: ai/sec-scanner
    to: security/sec-scanner
    reason: grew from an LLM demo into a security tool
  - op: merge                          # sources become research/paper-tools/<name>
    sources: [ai/paper-bot, tools/paper-helper]
    into: research/paper-tools
  - op: archive                        # -> _archive/misc/old-demo
    path: misc/old-demo
```

## Using it from an agent

Copy [`skills/reroot`](skills/reroot/SKILL.md) into `~/.claude/skills/` (Claude
Code) or reference it from `AGENTS.md` (Codex), then ask:
*"Tidy up ~/workspace: re-categorize drifted projects, merge duplicates,
archive what's dead."*

## What gets migrated

| Agent | State | How |
|---|---|---|
| Claude Code | `~/.claude/projects/<encoded-path>/` | renamed (merged if target exists) |
| | transcripts of moved projects | `cwd` and paths rewritten |
| | `memory/*.md` (all projects), `~/.claude/CLAUDE.md` | path references rewritten |
| | `~/.claude.json` projects, `githubRepoPaths` | keys/values rewritten |
| | `history.jsonl`, `settings*.json` | path references rewritten |
| | `CLAUDE.md`, `.claude/settings*.json` in the tree | path references rewritten |
| Codex | rollouts of moved sessions | paths rewritten |
| | `state_*.sqlite` `threads.cwd`, `project_roots.path` | prefix update (row ids journaled) |
| | `config.toml` `[projects."…"]` | rewritten |
| | `AGENTS.md`, `memories/` | path references rewritten |
| git | linked worktrees | `git worktree repair` |

On `merge`, the memory of every source is also copied into the memory of the
merge target, with a "Merged from …" section in its `MEMORY.md` index.

See [docs/DESIGN.md](docs/DESIGN.md) for the design and the journal format.

## Safety

- `apply` is a dry run unless you pass `--yes`.
- Every mutation is recorded in `~/.reroot/journal/<id>/` before it happens,
  with backups of every rewritten file and database.
- `undo` refuses to clobber files you changed after the apply (override with
  `--force`).
- Running agents may rewrite their state files concurrently; `apply` warns
  when they are running. Close other sessions for best results.

## Status

Early (v0.1). Contributions of adapters for more agents (Cursor, Gemini CLI,
Aider, …) are very welcome – see `internal/adapter/adapter.go`.

## License

MIT
