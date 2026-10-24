// Package codex migrates OpenAI Codex CLI state when projects move.
//
// Codex keeps path-keyed state in:
//
//	~/.codex/sessions/**/rollout-*.jsonl   session_meta.payload.cwd and transcript paths
//	~/.codex/archived_sessions/            same, for archived sessions
//	~/.codex/state_<n>.sqlite              threads.cwd, project_roots.path
//	~/.codex/config.toml                   [projects."<path>"] trust settings
//	AGENTS.md                              project instructions
package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xjtuwxg/AgentReRoot/internal/adapter"
	"github.com/xjtuwxg/AgentReRoot/internal/journal"
	"github.com/xjtuwxg/AgentReRoot/internal/pathx"
	"github.com/xjtuwxg/AgentReRoot/internal/rewrite"
)

// Adapter is the Codex adapter.
type Adapter struct {
	Home string // ~/.codex

	threads []thread
	loaded  bool
}

type thread struct {
	Cwd      string
	Updated  time.Time
	Archived bool
}

// New returns an adapter for the current user, honouring CODEX_HOME.
func New() *Adapter {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return &Adapter{Home: d}
	}
	home, _ := os.UserHomeDir()
	return &Adapter{Home: filepath.Join(home, ".codex")}
}

func (a *Adapter) Name() string { return "codex" }

func (a *Adapter) Available() bool {
	st, err := os.Stat(a.Home)
	return err == nil && st.IsDir()
}

var stateRe = regexp.MustCompile(`^state_(\d+)\.sqlite$`)

// stateDBs returns the state databases, newest schema first.
func (a *Adapter) stateDBs() []string {
	ents, _ := os.ReadDir(a.Home)
	best := -1
	var out []string
	for _, e := range ents {
		m := stateRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		p := filepath.Join(a.Home, e.Name())
		if n > best {
			best = n
			out = append([]string{p}, out...)
		} else {
			out = append(out, p)
		}
	}
	return out
}

func (a *Adapter) load() []thread {
	if a.loaded {
		return a.threads
	}
	a.loaded = true
	dbs := a.stateDBs()
	if len(dbs) == 0 {
		return nil
	}
	db, err := journal.OpenSQLite(dbs[0], true)
	if err != nil {
		return nil
	}
	defer db.Close()
	if !journal.HasTable(db, "threads") {
		return nil
	}
	rows, err := db.Query(`SELECT cwd, updated_at, archived FROM threads`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var t thread
		var ts int64
		if rows.Scan(&t.Cwd, &ts, &t.Archived) == nil {
			t.Updated = time.Unix(ts, 0)
			a.threads = append(a.threads, t)
		}
	}
	return a.threads
}

func (a *Adapter) KnownPaths() []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range a.load() {
		if !seen[t.Cwd] {
			seen[t.Cwd] = true
			out = append(out, t.Cwd)
		}
	}
	return out
}

func (a *Adapter) Footprint(path string) adapter.Footprint {
	fp := adapter.Footprint{Agent: a.Name()}
	for _, t := range a.load() {
		if !pathx.Within(t.Cwd, path) {
			continue
		}
		fp.Sessions++
		if t.Updated.After(fp.LastActive) {
			fp.LastActive = t.Updated
		}
	}
	return fp
}

// Migrate rewrites Codex state after oldPath moved to newPath.
func (a *Adapter) Migrate(tx *journal.Tx, oldPath, newPath string) error {
	if !a.Available() {
		return nil
	}
	desc := fmt.Sprintf("codex: rewrite paths %s -> %s", oldPath, newPath)
	for _, dir := range []string{"sessions", "archived_sessions"} {
		for _, f := range adapter.FindExt(tx, filepath.Join(a.Home, dir), ".jsonl") {
			// As for Claude, only sessions that ran inside the moved tree.
			if cwd := rolloutCwd(tx.Real(f)); cwd == "" || !pathx.Within(cwd, oldPath) {
				continue
			}
			if err := adapter.RewriteFile(tx, f, true, oldPath, newPath, desc+" (rollout)"); err != nil {
				return err
			}
		}
	}
	for _, db := range a.stateDBs() {
		if _, err := tx.SQLPrefix(db, "threads", "cwd", oldPath, newPath, desc+" (threads.cwd)"); err != nil {
			return fmt.Errorf("codex: %s: %w", db, err)
		}
		if _, err := tx.SQLPrefix(db, "project_roots", "path", oldPath, newPath, desc+" (project_roots.path)"); err != nil {
			return fmt.Errorf("codex: %s: %w", db, err)
		}
	}
	for i := range a.threads {
		if np, ok := pathx.Rebase(a.threads[i].Cwd, oldPath, newPath); ok {
			a.threads[i].Cwd = np
		}
	}
	if err := a.rewriteConfig(tx, oldPath, newPath); err != nil {
		return err
	}
	if err := adapter.RewriteFile(tx, filepath.Join(a.Home, "AGENTS.md"), false, oldPath, newPath, desc); err != nil {
		return err
	}
	for _, f := range adapter.FindExt(tx, filepath.Join(a.Home, "memories"), ".md") {
		if err := adapter.RewriteFile(tx, f, false, oldPath, newPath, desc+" (memory)"); err != nil {
			return err
		}
	}
	names := map[string]bool{"AGENTS.md": true, "AGENTS.override.md": true}
	for _, f := range adapter.FindNamed(tx, newPath, names, 200000) {
		if err := adapter.RewriteFile(tx, f, false, oldPath, newPath, desc); err != nil {
			return err
		}
	}
	return nil
}

// rolloutCwd returns the cwd recorded in a rollout's session_meta line.
func rolloutCwd(file string) string {
	fh, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer fh.Close()
	r := bufio.NewReaderSize(fh, 1<<16)
	for i := 0; i < 5; i++ {
		line, err := r.ReadBytes('\n')
		var v struct {
			Type    string `json:"type"`
			Payload struct {
				Cwd string `json:"cwd"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &v) == nil && v.Type == "session_meta" {
			return v.Payload.Cwd
		}
		if err != nil {
			break
		}
	}
	return ""
}

var projectHeader = regexp.MustCompile(`^\s*\[projects\."((?:[^"\\]|\\.)*)"\]\s*$`)

// rewriteConfig rewrites config.toml line by line. A [projects."<new>"]
// table that already exists is left alone instead of creating a duplicate.
func (a *Adapter) rewriteConfig(tx *journal.Tx, oldPath, newPath string) error {
	p := filepath.Join(a.Home, "config.toml")
	if !tx.Exists(p) {
		return nil
	}
	data, err := tx.ReadFile(p)
	if err != nil || !bytes.Contains(data, []byte(rewrite.EscapeJSON(oldPath))) {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	headers := map[string]bool{}
	for _, l := range lines {
		if m := projectHeader.FindStringSubmatch(l); m != nil {
			headers[m[1]] = true
		}
	}
	n := 0
	for i, l := range lines {
		nl, c := rewrite.JSON([]byte(l), oldPath, newPath)
		if c == 0 {
			continue
		}
		if m := projectHeader.FindStringSubmatch(string(nl)); m != nil && headers[m[1]] {
			tx.Note("warn", fmt.Sprintf("codex: %s already has a [projects] entry for %s; leaving the old entry in place", p, m[1]))
			continue
		}
		lines[i] = string(nl)
		n += c
	}
	if n == 0 {
		return nil
	}
	return tx.WriteFile(p, []byte(strings.Join(lines, "\n")), "codex: rewrite project paths in config.toml", n)
}

// MergeMemory is a no-op: Codex memory is global rather than per project.
func (a *Adapter) MergeMemory(tx *journal.Tx, sources []string, into string) error {
	return nil
}

// Verify reports Codex state that refers to paths under root that no longer exist.
func (a *Adapter) Verify(root string) []adapter.Finding {
	var out []adapter.Finding
	counts := map[string]int{}
	for _, t := range a.load() {
		if !t.Archived && pathx.Within(t.Cwd, root) {
			counts[t.Cwd]++
		}
	}
	for cwd, n := range counts {
		if _, err := os.Stat(cwd); err != nil {
			out = append(out, adapter.Finding{Agent: a.Name(), Kind: "orphaned-state", Path: cwd, Detail: fmt.Sprintf("%d thread(s)", n)})
		}
	}
	if data, err := os.ReadFile(filepath.Join(a.Home, "config.toml")); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			m := projectHeader.FindStringSubmatch(l)
			if m == nil || !pathx.Within(m[1], root) {
				continue
			}
			if _, err := os.Stat(m[1]); err != nil {
				out = append(out, adapter.Finding{Agent: a.Name(), Kind: "orphaned-config", Path: m[1], Detail: "entry in config.toml"})
			}
		}
	}
	return out
}
