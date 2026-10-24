---
name: reroot
description: Reorganize a workspace of projects (move, merge, archive directories) with the reroot CLI while keeping Claude Code and Codex sessions and memory intact. Use when the user wants to tidy, restructure, re-categorize, deduplicate or archive project folders, or asks which projects are active or dormant.
---

# Reorganizing a workspace with reroot

`reroot` does the mechanical, reversible part; you supply the judgement.
Never move project directories with `mv` – that orphans agent memory.

## Loop

1. **Scan** – collect facts:

   ```bash
   reroot scan <root> --json > /tmp/reroot-scan.json
   ```

   Each project has `rel`, `markers`, `languages`, `readme`, `git`
   (`last_commit`, `commits_90d`, `origin`, `dirty`), `agents` (sessions and
   memory per agent), `activity` (`active` < 30d, `warm` < 90d, `dormant`
   < 1y, `stale`), and `warnings`. `duplicates` lists overlap *hints*.

2. **Understand** – for projects whose category is unclear, read their README,
   manifests and a few source files. Check whether duplicate hints are real
   (same remote plus same content is a copy; a `.bak` copy is an archive
   candidate; similar names may be unrelated).

3. **Propose** – write a plan (see `reroot plan example`) and show it to the
   user as a table: operation, from, to, reason. Group by intent
   (re-categorize, merge, archive). Ask before including anything that is
   `active`, has a `dirty` git tree, or has `warnings`.

4. **Check and dry-run**:

   ```bash
   reroot plan check plan.yaml
   reroot apply plan.yaml          # prints every fs and agent-state change
   ```

5. **Apply** only after the user approves the dry run:

   ```bash
   reroot apply plan.yaml --yes
   reroot verify <root>
   ```

   Report the journal id. `reroot undo <id>` rolls everything back.

6. **After a merge**, the merged memory index (`MEMORY.md` of the `into`
   project) has one "Merged from <name>" section per source. Offer to
   consolidate it: deduplicate entries, fix contradictions, keep provenance.

## Rules

- Operations run in order; a later operation sees the result of earlier ones.
- `merge` moves each source to `into/<basename>`; it does not merge code
  files. Merging code is a separate, explicit task.
- If `apply` warns that Claude Code or Codex is running, tell the user that
  other open sessions may overwrite migrated state and suggest closing them.
- Keep plans small (one theme per plan) so dry runs stay reviewable.
