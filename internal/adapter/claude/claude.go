// Package claude migrates Claude Code state when projects move.
//
// Claude Code keys its per-project state by the absolute launch directory:
//
//	~/.claude/projects/<encoded-path>/   session transcripts (*.jsonl), memory/
//	~/.claude.json                       "projects" map, githubRepoPaths
//	~/.claude/history.jsonl              prompt history with "project" fields
//
// The encoding replaces every non-alphanumeric character with "-", which is
// lossy, so the real path of a project directory is read from the "cwd"
// fields of its transcripts instead of being decoded from the name.
package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/xjtuwxg/AgentReRoot/internal/adapter"
	"github.com/xjtuwxg/AgentReRoot/internal/journal"
	"github.com/xjtuwxg/AgentReRoot/internal/pathx"
	"github.com/xjtuwxg/AgentReRoot/internal/rewrite"
)

// Adapter is the Claude Code adapter.
type Adapter struct {
	Home         string // ~/.claude
	GlobalConfig string // ~/.claude.json

	index  []*project
	loaded bool
}

type project struct {
	Dir      string // absolute path of projects/<encoded>
	Path     string // project path it belongs to ("" if unresolved)
	Sessions int
	Memory   int
	Last     time.Time
}

// New returns an adapter for the current user, honouring CLAUDE_CONFIG_DIR.
func New() *Adapter {
	a := &Adapter{}
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		a.Home = d
		a.GlobalConfig = filepath.Join(d, ".claude.json")
		return a
	}
	home, _ := os.UserHomeDir()
	a.Home = filepath.Join(home, ".claude")
	a.GlobalConfig = filepath.Join(home, ".claude.json")
	return a
}

var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]`)

// Encode returns the name Claude Code uses for a project directory.
func Encode(p string) string { return nonAlnum.ReplaceAllString(p, "-") }

func (a *Adapter) Name() string { return "claude" }

func (a *Adapter) Available() bool {
	st, err := os.Stat(a.projectsDir())
	return err == nil && st.IsDir()
}

func (a *Adapter) projectsDir() string { return filepath.Join(a.Home, "projects") }

func (a *Adapter) load() []*project {
	if a.loaded {
		return a.index
	}
	a.loaded = true
	ents, err := os.ReadDir(a.projectsDir())
	if err != nil {
		return nil
	}
	for _, e := range ents {
		// ssh-* directories hold state of sessions on remote hosts.
		if !e.IsDir() || strings.HasPrefix(e.Name(), "ssh-") {
			continue
		}
		p := &project{Dir: filepath.Join(a.projectsDir(), e.Name())}
		files, _ := filepath.Glob(filepath.Join(p.Dir, "*.jsonl"))
		p.Sessions = len(files)
		for _, f := range files {
			if st, err := os.Stat(f); err == nil && st.ModTime().After(p.Last) {
				p.Last = st.ModTime()
			}
		}
		p.Path = resolvePath(e.Name(), files)
		if p.Path == "" {
			p.Path = a.configPaths()[e.Name()]
		}
		if p.Path == "" {
			p.Path = decodeOnDisk(e.Name())
		}
		if mem, err := os.ReadDir(filepath.Join(p.Dir, "memory")); err == nil {
			p.Memory = len(mem)
		}
		a.index = append(a.index, p)
	}
	return a.index
}

// resolvePath finds the launch directory recorded in the transcripts. The cwd
// whose encoding equals the directory name wins; otherwise the first cwd seen.
func resolvePath(name string, files []string) string {
	first := ""
	for _, f := range files {
		cwds := transcriptCwds(f, 200)
		for _, c := range cwds {
			if Encode(c) == name {
				return c
			}
			if first == "" {
				first = c
			}
		}
	}
	return first
}

// configPaths maps encoded names to the project paths listed in ~/.claude.json.
func (a *Adapter) configPaths() map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(a.GlobalConfig)
	if err != nil {
		return out
	}
	var top struct {
		Projects map[string]json.RawMessage `json:"projects"`
	}
	if json.Unmarshal(data, &top) == nil {
		for k := range top.Projects {
			out[Encode(k)] = k
		}
	}
	return out
}

// decodeOnDisk finds an existing path whose encoding is name by walking the
// filesystem from the root, trying every entry whose encoding fits. It is the
// fallback for state dirs that have memory but no transcripts left.
func decodeOnDisk(name string) string {
	sep := string(filepath.Separator)
	var search func(dir, rest string, depth int) string
	search = func(dir, rest string, depth int) string {
		if rest == "" {
			return dir
		}
		if depth > 32 || !strings.HasPrefix(rest, "-") {
			return ""
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return ""
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			enc := "-" + Encode(e.Name())
			if rest == enc || strings.HasPrefix(rest, enc+"-") {
				if p := search(filepath.Join(dir, e.Name()), rest[len(enc):], depth+1); p != "" {
					return p
				}
			}
		}
		return ""
	}
	return search(sep, name, 0)
}

func transcriptCwds(file string, maxLines int) []string {
	fh, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer fh.Close()
	r := bufio.NewReaderSize(fh, 1<<16)
	seen := map[string]bool{}
	var out []string
	for i := 0; i < maxLines; i++ {
		line, err := r.ReadBytes('\n')
		if bytes.Contains(line, []byte(`"cwd"`)) {
			var v struct {
				Cwd string `json:"cwd"`
			}
			if json.Unmarshal(line, &v) == nil && v.Cwd != "" && !seen[v.Cwd] {
				seen[v.Cwd] = true
				out = append(out, v.Cwd)
			}
		}
		if err != nil {
			break
		}
	}
	return out
}

func (a *Adapter) KnownPaths() []string {
	var out []string
	for _, p := range a.load() {
		if p.Path != "" {
			out = append(out, p.Path)
		}
	}
	return out
}

func (a *Adapter) Footprint(path string) adapter.Footprint {
	fp := adapter.Footprint{Agent: a.Name()}
	for _, p := range a.load() {
		if p.Path == "" || !pathx.Within(p.Path, path) {
			continue
		}
		fp.Sessions += p.Sessions
		fp.MemoryFiles += p.Memory
		if p.Last.After(fp.LastActive) {
			fp.LastActive = p.Last
		}
	}
	return fp
}

// Migrate rewrites Claude Code state after oldPath moved to newPath.
func (a *Adapter) Migrate(tx *journal.Tx, oldPath, newPath string) error {
	if !a.Available() {
		return nil
	}
	if err := a.moveProjectDirs(tx, oldPath, newPath); err != nil {
		return err
	}
	desc := fmt.Sprintf("claude: rewrite paths %s -> %s", oldPath, newPath)
	for _, p := range a.load() {
		// Only transcripts of the moved projects are rewritten so that they
		// resume at the new location. Other transcripts merely mention the
		// path as history, and may belong to sessions that are still running.
		if p.Path != "" && pathx.Within(p.Path, newPath) {
			for _, f := range adapter.FindExt(tx, p.Dir, ".jsonl") {
				if err := adapter.RewriteFile(tx, f, true, oldPath, newPath, desc+" (transcript)"); err != nil {
					return err
				}
			}
		}
		// Memory is knowledge, so a stale path is wrong wherever it appears.
		for _, f := range adapter.FindExt(tx, filepath.Join(p.Dir, "memory"), ".md") {
			if err := adapter.RewriteFile(tx, f, false, oldPath, newPath, desc+" (memory)"); err != nil {
				return err
			}
		}
	}
	if err := a.rewriteGlobalConfig(tx, oldPath, newPath); err != nil {
		return err
	}
	for _, f := range []string{"history.jsonl", "settings.json", "settings.local.json"} {
		if err := adapter.RewriteFile(tx, filepath.Join(a.Home, f), true, oldPath, newPath, desc); err != nil {
			return err
		}
	}
	if err := adapter.RewriteFile(tx, filepath.Join(a.Home, "CLAUDE.md"), false, oldPath, newPath, desc); err != nil {
		return err
	}
	// Instruction and settings files inside the moved tree.
	names := map[string]bool{"CLAUDE.md": true, "CLAUDE.local.md": true, "settings.json": true, "settings.local.json": true}
	for _, f := range adapter.FindNamed(tx, newPath, names, 200000) {
		isJSON := strings.HasSuffix(f, ".json")
		if isJSON && filepath.Base(filepath.Dir(f)) != ".claude" {
			continue
		}
		if err := adapter.RewriteFile(tx, f, isJSON, oldPath, newPath, desc); err != nil {
			return err
		}
	}
	return nil
}

// moveProjectDirs renames projects/<encoded> directories of every project
// within oldPath, merging into existing directories when needed.
func (a *Adapter) moveProjectDirs(tx *journal.Tx, oldPath, newPath string) error {
	var keep []*project
	byDir := map[string]*project{}
	for _, p := range a.load() {
		byDir[p.Dir] = p
	}
	for _, p := range a.load() {
		np, ok := pathx.Rebase(p.Path, oldPath, newPath)
		if p.Path == "" || !ok {
			keep = append(keep, p)
			continue
		}
		newDir := filepath.Join(a.projectsDir(), Encode(np))
		if newDir == p.Dir {
			p.Path = np
			keep = append(keep, p)
			continue
		}
		if !tx.Exists(newDir) {
			if err := tx.Rename(p.Dir, newDir, "claude: move project state for "+np); err != nil {
				return err
			}
			p.Dir, p.Path = newDir, np
			byDir[newDir] = p
			keep = append(keep, p)
			continue
		}
		if err := mergeDir(tx, p.Dir, newDir, filepath.Base(p.Path)); err != nil {
			return err
		}
		if dst := byDir[newDir]; dst != nil {
			dst.Sessions += p.Sessions
			dst.Memory += p.Memory
		}
	}
	a.index = keep
	return nil
}

// mergeDir moves the contents of src into the existing directory dst.
func mergeDir(tx *journal.Tx, src, dst, label string) error {
	ents, err := tx.ReadDir(src)
	if err != nil {
		return nil
	}
	for _, e := range ents {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		switch {
		case !tx.Exists(d):
			if err := tx.Rename(s, d, "claude: merge project state"); err != nil {
				return err
			}
		case e.Name() == "memory" && e.IsDir():
			if err := mergeMemoryDir(tx, s, d, label, true); err != nil {
				return err
			}
		case e.IsDir() && tx.IsDir(d):
			if err := mergeDir(tx, s, d, label); err != nil {
				return err
			}
		default:
			alt := filepath.Join(dst, label+"--"+e.Name())
			if err := tx.Rename(s, alt, "claude: merge project state (renamed on conflict)"); err != nil {
				return err
			}
		}
	}
	return nil
}

// mergeMemoryDir brings the memory files of src into dst. MEMORY.md indexes
// are concatenated under a provenance heading; other files that would collide
// are prefixed with label. With move the source files are moved, otherwise copied.
func mergeMemoryDir(tx *journal.Tx, src, dst, label string, move bool) error {
	ents, err := tx.ReadDir(src)
	if err != nil {
		return nil
	}
	if err := tx.MkdirAll(dst); err != nil {
		return err
	}
	renamed := map[string]string{}
	var index []byte
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		s := filepath.Join(src, e.Name())
		if e.Name() == "MEMORY.md" {
			index, _ = tx.ReadFile(s)
			continue
		}
		name := e.Name()
		if tx.Exists(filepath.Join(dst, name)) || !move {
			name = label + "--" + name
			renamed[e.Name()] = name
		}
		d := filepath.Join(dst, name)
		if move {
			if err := tx.Rename(s, d, "claude: merge memory file"); err != nil {
				return err
			}
			continue
		}
		data, err := tx.ReadFile(s)
		if err != nil {
			continue
		}
		if err := tx.WriteFile(d, data, "claude: copy memory file from "+label, 0); err != nil {
			return err
		}
	}
	if len(index) == 0 {
		return nil
	}
	keys := make([]string, 0, len(renamed))
	for k := range renamed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		index = bytes.ReplaceAll(index, []byte("]("+k+")"), []byte("]("+renamed[k]+")"))
	}
	dstIndex := filepath.Join(dst, "MEMORY.md")
	var merged []byte
	if tx.Exists(dstIndex) {
		merged, _ = tx.ReadFile(dstIndex)
		if len(merged) > 0 && merged[len(merged)-1] != '\n' {
			merged = append(merged, '\n')
		}
	}
	merged = append(merged, []byte("\n## Merged from "+label+"\n\n")...)
	merged = append(merged, index...)
	return tx.WriteFile(dstIndex, merged, "claude: merge MEMORY.md index from "+label, 0)
}

// MergeMemory copies the memory of every source project into the memory of
// into, so a session started at into sees everything that was learned.
func (a *Adapter) MergeMemory(tx *journal.Tx, sources []string, into string) error {
	if !a.Available() {
		return nil
	}
	dst := filepath.Join(a.projectsDir(), Encode(into), "memory")
	for _, src := range sources {
		mem := filepath.Join(a.projectsDir(), Encode(src), "memory")
		if !tx.IsDir(mem) {
			continue
		}
		if err := mergeMemoryDir(tx, mem, dst, filepath.Base(src), false); err != nil {
			return err
		}
	}
	return nil
}

// rewriteGlobalConfig handles ~/.claude.json structurally: keys of the
// "projects" map are renamed (an existing target entry wins), and every
// other value gets a JSON-string prefix rewrite.
func (a *Adapter) rewriteGlobalConfig(tx *journal.Tx, oldPath, newPath string) error {
	if !tx.Exists(a.GlobalConfig) {
		return nil
	}
	data, err := tx.ReadFile(a.GlobalConfig)
	if err != nil || !bytes.Contains(data, []byte(rewrite.EscapeJSON(oldPath))) {
		return nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return fmt.Errorf("parse %s: %w", a.GlobalConfig, err)
	}
	n := 0
	for k, v := range top {
		if k == "projects" {
			continue
		}
		if nv, c := rewrite.JSON(v, oldPath, newPath); c > 0 {
			top[k] = nv
			n += c
		}
	}
	if raw, ok := top["projects"]; ok {
		var pm map[string]json.RawMessage
		if err := json.Unmarshal(raw, &pm); err == nil {
			out := make(map[string]json.RawMessage, len(pm))
			for k, v := range pm {
				if nv, c := rewrite.JSON(v, oldPath, newPath); c > 0 {
					v = nv
					n += c
				}
				nk, moved := pathx.Rebase(k, oldPath, newPath)
				if moved {
					if _, clash := pm[nk]; clash {
						tx.Note("warn", fmt.Sprintf("claude: %s already has settings in %s; keeping them and dropping those of %s", nk, a.GlobalConfig, k))
						n++
						continue
					}
					n++
				}
				out[nk] = v
			}
			b, err := json.Marshal(out)
			if err != nil {
				return err
			}
			top["projects"] = b
		}
	}
	if n == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(top); err != nil {
		return err
	}
	return tx.WriteFile(a.GlobalConfig, buf.Bytes(), "claude: rewrite projects in global config", n)
}

var absPath = regexp.MustCompile(`/[^\s"'` + "`" + `()\[\]<>,;]+`)

// Verify reports Claude state that refers to paths under root that no longer exist.
func (a *Adapter) Verify(root string) []adapter.Finding {
	var out []adapter.Finding
	for _, p := range a.load() {
		if p.Path == "" {
			if (p.Sessions > 0 || p.Memory > 0) && strings.HasPrefix(filepath.Base(p.Dir), Encode(root)) {
				out = append(out, adapter.Finding{Agent: a.Name(), Kind: "unresolved-state", Path: p.Dir, Detail: "cannot tell which project this state belongs to"})
			}
			continue
		}
		if !pathx.Within(p.Path, root) {
			continue
		}
		if _, err := os.Stat(p.Path); err != nil {
			out = append(out, adapter.Finding{Agent: a.Name(), Kind: "orphaned-state", Path: p.Path,
				Detail: fmt.Sprintf("%d session(s), %d memory file(s) in %s", p.Sessions, p.Memory, p.Dir)})
		}
		files, _ := filepath.Glob(filepath.Join(p.Dir, "memory", "*.md"))
		for _, f := range files {
			out = append(out, staleRefs(a.Name(), f, root)...)
		}
	}
	if data, err := os.ReadFile(a.GlobalConfig); err == nil {
		var top struct {
			Projects map[string]json.RawMessage `json:"projects"`
		}
		if json.Unmarshal(data, &top) == nil {
			for k := range top.Projects {
				if pathx.Within(k, root) {
					if _, err := os.Stat(k); err != nil {
						out = append(out, adapter.Finding{Agent: a.Name(), Kind: "orphaned-config", Path: k, Detail: "entry in " + a.GlobalConfig})
					}
				}
			}
		}
	}
	return out
}

func staleRefs(agent, file, root string) []adapter.Finding {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var out []adapter.Finding
	seen := map[string]bool{}
	for _, m := range absPath.FindAll(data, -1) {
		p := strings.TrimRight(string(m), ".:")
		if !pathx.Within(p, root) || seen[p] {
			continue
		}
		seen[p] = true
		if _, err := os.Stat(p); err != nil {
			out = append(out, adapter.Finding{Agent: agent, Kind: "stale-reference", Path: p, Detail: "mentioned in " + file})
		}
	}
	return out
}
