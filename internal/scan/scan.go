// Package scan collects facts about the projects in a workspace. It makes no
// decisions; its output is meant to be read by an agent or a human who then
// writes a plan.
package scan

import (
	"bufio"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xjtuwxg/AgentReRoot/internal/adapter"
	"github.com/xjtuwxg/AgentReRoot/internal/pathx"
	"github.com/xjtuwxg/AgentReRoot/internal/plan"
)

// Report is the result of a scan.
type Report struct {
	Root       string      `json:"root"`
	ScannedAt  time.Time   `json:"scanned_at"`
	Agents     []string    `json:"agents"`
	Groups     []Group     `json:"groups"`
	Projects   []*Project  `json:"projects"`
	Duplicates []Duplicate `json:"duplicates,omitempty"`
}

// Group is a directory that only organizes other projects (a category).
type Group struct {
	Path     string `json:"path"`
	Projects int    `json:"projects"`
}

// Project is a directory reroot treats as one unit of work.
type Project struct {
	Path         string              `json:"path"`
	Rel          string              `json:"rel"`
	Markers      []string            `json:"markers"`
	Languages    []string            `json:"languages,omitempty"`
	Files        int                 `json:"files"`
	SizeBytes    int64               `json:"size_bytes"`
	Truncated    bool                `json:"truncated,omitempty"`
	NewestFile   time.Time           `json:"newest_file,omitzero"`
	Git          *Git                `json:"git,omitempty"`
	Readme       string              `json:"readme,omitempty"`
	Agents       []adapter.Footprint `json:"agents,omitempty"`
	LastActivity time.Time           `json:"last_activity,omitzero"`
	Activity     string              `json:"activity"`
	Archived     bool                `json:"archived,omitempty"`
	Warnings     []string            `json:"warnings,omitempty"`
}

// Git holds repository facts.
type Git struct {
	LastCommit time.Time `json:"last_commit,omitzero"`
	Commits90d int       `json:"commits_90d"`
	Origin     string    `json:"origin,omitempty"`
	Remotes    []string  `json:"remotes,omitempty"`
	Dirty      bool      `json:"dirty"`
	Worktrees  int       `json:"worktrees,omitempty"`
	IsWorktree bool      `json:"is_worktree,omitempty"`
}

// Duplicate is a hint that several projects may cover the same ground.
type Duplicate struct {
	Reason   string   `json:"reason"`
	Projects []string `json:"projects"`
}

// Options control a scan.
type Options struct {
	MaxDepth int
	Adapters []adapter.Adapter
	Now      time.Time
}

var manifestMarkers = []string{
	".git", "go.mod", "package.json", "pyproject.toml", "setup.py", "requirements.txt",
	"Cargo.toml", "CMakeLists.txt", "Makefile", "meson.build", "build.gradle", "pom.xml",
	"Gemfile", "composer.json", "Package.swift", "pubspec.yaml", "mix.exs", "deno.json",
	"CLAUDE.md", "AGENTS.md", "README.md", "README", "README.rst", "README.txt",
}

// weakMarkers alone do not make a project when the directory also holds
// several real projects: a stray script or README in a category folder is
// common.
var weakMarkers = map[string]bool{
	"CLAUDE.md": true, "AGENTS.md": true, "README.md": true, "README": true,
	"README.rst": true, "README.txt": true, "Makefile": true, "source-files": true,
}

var langByExt = map[string]string{
	".go": "Go", ".py": "Python", ".ipynb": "Jupyter", ".rs": "Rust", ".js": "JavaScript",
	".mjs": "JavaScript", ".ts": "TypeScript", ".tsx": "TypeScript", ".jsx": "JavaScript",
	".c": "C", ".h": "C", ".cc": "C++", ".cpp": "C++", ".hpp": "C++", ".java": "Java",
	".kt": "Kotlin", ".swift": "Swift", ".m": "Objective-C", ".rb": "Ruby", ".sh": "Shell",
	".tex": "TeX", ".lua": "Lua", ".cs": "C#", ".php": "PHP", ".scala": "Scala",
	".r": "R", ".jl": "Julia", ".zig": "Zig", ".sol": "Solidity", ".vue": "Vue",
	".dart": "Dart", ".html": "HTML", ".css": "CSS", ".ml": "OCaml", ".hs": "Haskell",
	".ex": "Elixir", ".s": "Assembly", ".S": "Assembly", ".v": "Verilog",
}

const maxFilesPerProject = 20000

// Run scans root.
func Run(root string, opts Options) (*Report, error) {
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 5
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	r := &Report{Root: root, ScannedAt: opts.Now.UTC()}
	for _, a := range opts.Adapters {
		r.Agents = append(r.Agents, a.Name())
	}
	known := map[string]bool{}
	for _, a := range opts.Adapters {
		for _, p := range a.KnownPaths() {
			known[p] = true
		}
	}
	w := walker{root: root, maxDepth: opts.MaxDepth, known: known, report: r}
	if _, err := w.walk(root, 0); err != nil {
		return nil, err
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, p := range r.Projects {
		wg.Add(1)
		sem <- struct{}{}
		go func(p *Project) {
			defer wg.Done()
			defer func() { <-sem }()
			inspect(p)
		}(p)
	}
	wg.Wait()

	for _, p := range r.Projects {
		for _, a := range opts.Adapters {
			if fp := a.Footprint(p.Path); fp.Sessions > 0 || fp.MemoryFiles > 0 {
				p.Agents = append(p.Agents, fp)
			}
		}
		p.LastActivity, p.Activity = activity(p, opts.Now)
	}
	sort.Slice(r.Projects, func(i, j int) bool { return r.Projects[i].Rel < r.Projects[j].Rel })
	sort.Slice(r.Groups, func(i, j int) bool { return r.Groups[i].Path < r.Groups[j].Path })
	r.Duplicates = duplicates(r.Projects)
	return r, nil
}

type walker struct {
	root     string
	maxDepth int
	known    map[string]bool
	report   *Report
}

// walk returns the number of projects found at or below dir.
func (w *walker) walk(dir string, depth int) (int, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if dir == w.root {
			return 0, err
		}
		return 0, nil
	}
	names := map[string]bool{}
	var subdirs []string
	hasFiles := false
	codeFiles := false
	for _, e := range ents {
		names[e.Name()] = true
		if e.IsDir() {
			if !strings.HasPrefix(e.Name(), ".") && !adapter.SkipDirs[e.Name()] {
				subdirs = append(subdirs, filepath.Join(dir, e.Name()))
			}
			continue
		}
		if !strings.HasPrefix(e.Name(), ".") {
			hasFiles = true
			if _, ok := langByExt[filepath.Ext(e.Name())]; ok {
				codeFiles = true
			}
		}
	}
	if dir != w.root {
		var markers []string
		for _, m := range manifestMarkers {
			if names[m] {
				markers = append(markers, m)
			}
		}
		if codeFiles {
			markers = append(markers, "source-files")
		}
		if names["pyvenv.cfg"] {
			return 0, nil // a virtualenv, not a project
		}
		if len(markers) > 0 && !(allWeak(markers) && strongChildren(subdirs) >= 2) {
			w.addProject(dir, markers)
			return 1, nil
		}
		if depth >= w.maxDepth {
			w.addProject(dir, []string{"depth-limit"})
			return 1, nil
		}
	}
	found := 0
	for _, sd := range subdirs {
		n, err := w.walk(sd, depth+1)
		if err != nil {
			return found, err
		}
		found += n
	}
	if dir == w.root {
		return found, nil
	}
	if found == 0 && (hasFiles || w.known[dir]) {
		marker := "loose-files"
		if w.known[dir] {
			marker = "agent-state"
		}
		w.addProject(dir, []string{marker})
		return 1, nil
	}
	if found > 0 {
		w.report.Groups = append(w.report.Groups, Group{Path: pathx.Rel(w.root, dir), Projects: found})
	}
	return found, nil
}

func allWeak(markers []string) bool {
	for _, m := range markers {
		if !weakMarkers[m] {
			return false
		}
	}
	return true
}

// strongChildren counts subdirectories that carry a non-weak project marker.
func strongChildren(subdirs []string) int {
	n := 0
	for _, d := range subdirs {
		for _, m := range manifestMarkers {
			if weakMarkers[m] {
				continue
			}
			if _, err := os.Lstat(filepath.Join(d, m)); err == nil {
				n++
				break
			}
		}
	}
	return n
}

func (w *walker) addProject(dir string, markers []string) {
	rel := pathx.Rel(w.root, dir)
	w.report.Projects = append(w.report.Projects, &Project{
		Path:     dir,
		Rel:      rel,
		Markers:  markers,
		Archived: rel == plan.ArchiveDir || strings.HasPrefix(rel, plan.ArchiveDir+string(filepath.Separator)),
	})
}

func inspect(p *Project) {
	langs := map[string]int{}
	_ = filepath.WalkDir(p.Path, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == p.Path {
				return nil
			}
			name := d.Name()
			if name == ".venv" || name == "venv" {
				p.Warnings = append(p.Warnings, "contains a Python virtualenv ("+pathx.Rel(p.Path, path)+") whose scripts use absolute paths; recreate it after moving")
			}
			if adapter.SkipDirs[name] || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
				p.Warnings = append(p.Warnings, "contains a nested git repository: "+pathx.Rel(p.Path, path))
			}
			return nil
		}
		p.Files++
		if p.Files > maxFilesPerProject {
			p.Truncated = true
			return filepath.SkipAll
		}
		if info, err := d.Info(); err == nil {
			p.SizeBytes += info.Size()
			if info.ModTime().After(p.NewestFile) {
				p.NewestFile = info.ModTime()
			}
		}
		if l, ok := langByExt[filepath.Ext(path)]; ok {
			langs[l]++
		}
		return nil
	})
	p.Languages = topLanguages(langs, 3)
	p.Git = gitFacts(p.Path)
	p.Readme = readme(p.Path)
}

func topLanguages(counts map[string]int, n int) []string {
	type kv struct {
		k string
		v int
	}
	var all []kv
	for k, v := range counts {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	var out []string
	for i := 0; i < len(all) && i < n; i++ {
		out = append(out, all[i].k)
	}
	return out
}

func git(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

func gitFacts(dir string) *Git {
	st, err := os.Lstat(filepath.Join(dir, ".git"))
	if err != nil {
		return nil
	}
	g := &Git{IsWorktree: !st.IsDir()}
	if out, err := git(dir, "log", "-1", "--format=%ct"); err == nil && out != "" {
		if ts, err := strconv.ParseInt(out, 10, 64); err == nil {
			g.LastCommit = time.Unix(ts, 0)
		}
	}
	if out, err := git(dir, "rev-list", "--count", "--since=90.days", "HEAD"); err == nil {
		g.Commits90d, _ = strconv.Atoi(out)
	}
	if out, err := git(dir, "remote", "-v"); err == nil {
		seen := map[string]bool{}
		for _, l := range strings.Split(out, "\n") {
			f := strings.Fields(l)
			if len(f) >= 2 && f[0] == "origin" {
				g.Origin = f[1]
			}
			if len(f) >= 2 && !seen[f[1]] {
				seen[f[1]] = true
				g.Remotes = append(g.Remotes, f[1])
			}
		}
	}
	if out, err := git(dir, "status", "--porcelain", "--untracked-files=no"); err == nil {
		g.Dirty = out != ""
	}
	if out, err := git(dir, "worktree", "list", "--porcelain"); err == nil {
		g.Worktrees = strings.Count(out, "worktree ") - 1
		if g.Worktrees < 0 {
			g.Worktrees = 0
		}
	}
	return g
}

// readme returns the title and first paragraph of the project's README.
func readme(dir string) string {
	for _, name := range []string{"README.md", "README", "README.rst", "README.txt", "readme.md"} {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		defer f.Close()
		var parts []string
		size := 0
		sc := bufio.NewScanner(f)
		inPara := false
		for sc.Scan() && size < 400 {
			l := strings.TrimSpace(sc.Text())
			if l == "" || strings.HasPrefix(l, "![") || strings.HasPrefix(l, "[![") || strings.HasPrefix(l, "<") {
				if inPara {
					break
				}
				continue
			}
			if strings.HasPrefix(l, "#") {
				if inPara {
					break
				}
				if len(parts) == 0 {
					parts = append(parts, strings.TrimSpace(strings.TrimLeft(l, "#"))+":")
				}
				continue
			}
			inPara = true
			parts = append(parts, l)
			size += len(l)
		}
		s := strings.Join(parts, " ")
		if len(s) > 400 {
			s = s[:400] + "..."
		}
		return s
	}
	return ""
}

func activity(p *Project, now time.Time) (time.Time, string) {
	last := p.NewestFile
	if p.Git != nil && p.Git.LastCommit.After(last) {
		last = p.Git.LastCommit
	}
	for _, a := range p.Agents {
		if a.LastActive.After(last) {
			last = a.LastActive
		}
	}
	if last.IsZero() {
		return last, "unknown"
	}
	age := now.Sub(last)
	switch {
	case age < 30*24*time.Hour:
		return last, "active"
	case age < 90*24*time.Hour:
		return last, "warm"
	case age < 365*24*time.Hour:
		return last, "dormant"
	}
	return last, "stale"
}
