package apply_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/xjtuwxg/AgentReRoot/internal/adapter/claude"
	"github.com/xjtuwxg/AgentReRoot/internal/adapter/registry"
	"github.com/xjtuwxg/AgentReRoot/internal/apply"
	"github.com/xjtuwxg/AgentReRoot/internal/journal"
	"github.com/xjtuwxg/AgentReRoot/internal/plan"
)

type fixture struct {
	t     *testing.T
	home  string
	ws    string
	codex string
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// newFixture builds a fake HOME with Claude Code and Codex state for a small
// workspace:
//
//	ws/ai/foo        project with Claude sessions + memory and Codex threads
//	ws/ai/bar        merge source with Claude memory
//	ws/tools/bar2    merge source with Claude memory
//	ws/misc/old      project to archive
func newFixture(t *testing.T) *fixture {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, home: filepath.Join(tmp, "home"), ws: filepath.Join(tmp, "ws")}
	f.codex = filepath.Join(f.home, ".codex")
	t.Setenv("HOME", f.home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("REROOT_HOME", filepath.Join(f.home, ".reroot"))

	foo := filepath.Join(f.ws, "ai", "foo")
	write(t, filepath.Join(foo, "README.md"), "# Foo\n")
	write(t, filepath.Join(foo, "CLAUDE.md"), "Build output goes to "+foo+"/out.\n")
	write(t, filepath.Join(foo, "AGENTS.md"), "Run tests from "+foo+".\n")
	write(t, filepath.Join(f.ws, "ai", "bar", "README.md"), "# Bar\n")
	write(t, filepath.Join(f.ws, "tools", "bar2", "README.md"), "# Bar2\n")
	write(t, filepath.Join(f.ws, "misc", "old", "main.py"), "print('old')\n")
	write(t, filepath.Join(f.ws, "ai", "foo-other", "README.md"), "# Not foo\n")

	cl := filepath.Join(f.home, ".claude", "projects")
	fooDir := filepath.Join(cl, claude.Encode(foo))
	write(t, filepath.Join(fooDir, "s1.jsonl"),
		`{"type":"user","cwd":"`+foo+`","message":"edit `+foo+`/main.go"}`+"\n"+
			`{"type":"user","cwd":"`+foo+`/sub"}`+"\n")
	write(t, filepath.Join(fooDir, "memory", "MEMORY.md"), "- [Layout](layout.md) — repo layout\n")
	write(t, filepath.Join(fooDir, "memory", "layout.md"), "Sources live in "+foo+"/src.\n")
	for _, name := range []string{"bar", "bar2"} {
		parent := "ai"
		if name == "bar2" {
			parent = "tools"
		}
		p := filepath.Join(f.ws, parent, name)
		d := filepath.Join(cl, claude.Encode(p))
		write(t, filepath.Join(d, "s.jsonl"), `{"cwd":"`+p+`"}`+"\n")
		write(t, filepath.Join(d, "memory", "MEMORY.md"), "- [Notes](notes.md) — "+name+" notes\n")
		write(t, filepath.Join(d, "memory", "notes.md"), name+" notes\n")
	}
	cfg := map[string]any{
		"numStartups": 3,
		"projects": map[string]any{
			foo:                         map[string]any{"hasTrustDialogAccepted": true},
			filepath.Join(f.ws, "misc"): map[string]any{"allowedTools": []string{}},
		},
		"githubRepoPaths": map[string]any{"me/foo": []string{foo}},
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	write(t, filepath.Join(f.home, ".claude.json"), string(b))
	write(t, filepath.Join(f.home, ".claude", "history.jsonl"), `{"display":"hi","project":"`+foo+`"}`+"\n")

	write(t, filepath.Join(f.codex, "sessions", "2026", "01", "01", "rollout-1.jsonl"),
		`{"type":"session_meta","payload":{"cwd":"`+foo+`"}}`+"\n")
	enc, _ := zstd.NewWriter(nil)
	write(t, filepath.Join(f.codex, "sessions", "2025", "12", "01", "rollout-0.jsonl.zst"),
		string(enc.EncodeAll([]byte(`{"type":"session_meta","payload":{"cwd":"`+foo+`"}}`+"\n"), nil)))
	write(t, filepath.Join(f.codex, "config.toml"),
		"model = \"x\"\n\n[projects.\""+foo+"\"]\ntrust_level = \"trusted\"\n")
	db, err := journal.OpenSQLite(filepath.Join(f.codex, "state_5.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE threads (id TEXT PRIMARY KEY, cwd TEXT NOT NULL, updated_at INTEGER NOT NULL, archived INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE project_roots (project_id TEXT, position INTEGER, path TEXT)`,
		`INSERT INTO threads VALUES ('t1', '` + foo + `', 1700000000, 0), ('t2', '` + foo + `-other', 1700000000, 0)`,
		`INSERT INTO project_roots VALUES ('p1', 0, '` + foo + `')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *fixture) plan() *plan.Plan {
	return &plan.Plan{Version: 1, Root: f.ws, Operations: []plan.Operation{
		{Op: "move", From: "ai/foo", To: "security/foo"},
		{Op: "merge", Sources: []string{"ai/bar", "tools/bar2"}, Into: "research/bars"},
		{Op: "archive", Path: "misc/old"},
	}}
}

// snapshot hashes every file under home and ws, except reroot's own state.
// SQLite files are summarized by their rows instead of their bytes.
func (f *fixture) snapshot() map[string]string {
	out := map[string]string{}
	for _, root := range []string{f.home, f.ws} {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == ".reroot" {
					return filepath.SkipDir
				}
				out[p] = "dir"
				return nil
			}
			if strings.Contains(p, ".sqlite") {
				return nil
			}
			b, _ := os.ReadFile(p)
			h := sha256.Sum256(b)
			out[p] = hex.EncodeToString(h[:])
			return nil
		})
	}
	out["sqlite"] = f.codexRows()
	return out
}

func (f *fixture) codexRows() string {
	db, err := journal.OpenSQLite(filepath.Join(f.codex, "state_5.sqlite"), true)
	if err != nil {
		f.t.Fatal(err)
	}
	defer db.Close()
	var s string
	for _, q := range []string{`SELECT id || '=' || cwd FROM threads ORDER BY id`, `SELECT path FROM project_roots`} {
		rows, err := db.Query(q)
		if err != nil {
			f.t.Fatal(err)
		}
		for rows.Next() {
			var v string
			_ = rows.Scan(&v)
			s += v + ";"
		}
		rows.Close()
	}
	return s
}

func diff(a, b map[string]string) []string {
	var d []string
	for k, v := range a {
		if b[k] != v {
			d = append(d, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			d = append(d, "+"+k)
		}
	}
	return d
}

func TestDryRunChangesNothing(t *testing.T) {
	f := newFixture(t)
	before := f.snapshot()
	res, err := apply.Run(f.plan(), apply.Options{DryRun: true, Adapters: registry.Available()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Actions) == 0 {
		t.Fatal("dry run reported no actions")
	}
	if d := diff(before, f.snapshot()); len(d) > 0 {
		t.Fatalf("dry run changed files: %v", d)
	}
}

func TestApplyMigratesAgentStateAndUndoRestores(t *testing.T) {
	f := newFixture(t)
	before := f.snapshot()
	res, err := apply.Run(f.plan(), apply.Options{Adapters: registry.Available()})
	if err != nil {
		t.Fatal(err)
	}

	oldFoo := filepath.Join(f.ws, "ai", "foo")
	newFoo := filepath.Join(f.ws, "security", "foo")
	cl := filepath.Join(f.home, ".claude", "projects")

	// Filesystem.
	for _, p := range []string{newFoo, filepath.Join(f.ws, "research", "bars", "bar"),
		filepath.Join(f.ws, "research", "bars", "bar2"), filepath.Join(f.ws, "_archive", "misc", "old")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s: %v", p, err)
		}
	}
	// Claude Code.
	newDir := filepath.Join(cl, claude.Encode(newFoo))
	if s := read(t, filepath.Join(newDir, "s1.jsonl")); strings.Contains(s, oldFoo) || !strings.Contains(s, newFoo+"/sub") {
		t.Errorf("transcript not rewritten: %s", s)
	}
	if s := read(t, filepath.Join(newDir, "memory", "layout.md")); !strings.Contains(s, newFoo+"/src") {
		t.Errorf("memory not rewritten: %s", s)
	}
	if s := read(t, filepath.Join(newFoo, "CLAUDE.md")); !strings.Contains(s, newFoo+"/out") {
		t.Errorf("CLAUDE.md not rewritten: %s", s)
	}
	var cfg struct {
		Projects        map[string]json.RawMessage `json:"projects"`
		GithubRepoPaths map[string][]string        `json:"githubRepoPaths"`
		NumStartups     int                        `json:"numStartups"`
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(f.home, ".claude.json"))), &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Projects[newFoo]; !ok || cfg.NumStartups != 3 || cfg.GithubRepoPaths["me/foo"][0] != newFoo {
		t.Errorf("global config not migrated: %+v", cfg)
	}
	if s := read(t, filepath.Join(f.home, ".claude", "history.jsonl")); !strings.Contains(s, newFoo) {
		t.Errorf("history not rewritten: %s", s)
	}
	merged := read(t, filepath.Join(cl, claude.Encode(filepath.Join(f.ws, "research", "bars")), "memory", "MEMORY.md"))
	for _, want := range []string{"Merged from bar\n", "(bar--notes.md)", "Merged from bar2", "(bar2--notes.md)"} {
		if !strings.Contains(merged, want) {
			t.Errorf("merged MEMORY.md lacks %q:\n%s", want, merged)
		}
	}
	// Codex.
	if s := read(t, filepath.Join(f.codex, "sessions", "2026", "01", "01", "rollout-1.jsonl")); !strings.Contains(s, newFoo) {
		t.Errorf("rollout not rewritten: %s", s)
	}
	dec, _ := zstd.NewReader(nil)
	zplain, err := dec.DecodeAll([]byte(read(t, filepath.Join(f.codex, "sessions", "2025", "12", "01", "rollout-0.jsonl.zst"))), nil)
	if err != nil || !strings.Contains(string(zplain), newFoo) {
		t.Errorf("compressed rollout not rewritten: %s, %v", zplain, err)
	}
	if s := read(t, filepath.Join(f.codex, "config.toml")); !strings.Contains(s, `[projects."`+newFoo+`"]`) {
		t.Errorf("config.toml not rewritten: %s", s)
	}
	if s := read(t, filepath.Join(newFoo, "AGENTS.md")); !strings.Contains(s, newFoo) {
		t.Errorf("AGENTS.md not rewritten: %s", s)
	}
	want := "t1=" + newFoo + ";t2=" + oldFoo + "-other;" + newFoo + ";"
	if got := f.codexRows(); got != want {
		t.Errorf("sqlite rows = %s, want %s", got, want)
	}
	// Verify finds nothing orphaned for the moved projects.
	for _, a := range registry.Available() {
		for _, fd := range a.Verify(f.ws) {
			if fd.Kind == "orphaned-state" {
				t.Errorf("unexpected finding after apply: %+v", fd)
			}
		}
	}

	// Undo restores the exact original state.
	if _, _, err := journal.Undo(res.JournalID, false); err != nil {
		t.Fatal(err)
	}
	if d := diff(before, f.snapshot()); len(d) > 0 {
		t.Fatalf("undo left differences: %v", d)
	}
}

func TestPlanValidation(t *testing.T) {
	f := newFixture(t)
	p := &plan.Plan{Version: 1, Root: f.ws, Operations: []plan.Operation{
		{Op: "move", From: "ai/foo", To: "ai/foo/inside"},
		{Op: "move", From: "missing", To: "x"},
		{Op: "move", From: "ai/bar", To: "tools/bar2/..", Reason: "escapes into existing dir"},
		{Op: "move", From: "ai/foo", To: "../outside"},
		{Op: "move", From: "ai/foo", To: "new/foo"},
		{Op: "move", From: "ai/foo", To: "again"},
		{Op: "frobnicate"},
	}}
	_, errs := p.Resolve()
	if len(errs) != 6 {
		t.Fatalf("got %d errors, want 6: %v", len(errs), errs)
	}
}
