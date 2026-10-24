package adapter

import (
	"bytes"
	"io/fs"
	"path/filepath"

	"github.com/xjtuwxg/AgentReRoot/internal/journal"
	"github.com/xjtuwxg/AgentReRoot/internal/rewrite"
)

// SkipDirs are directory names that never contain project or agent state
// worth looking at and are expensive to walk.
var SkipDirs = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true,
	"__pycache__": true, "target": true, "dist": true, "build": true,
	".tox": true, ".mypy_cache": true, ".pytest_cache": true, ".gradle": true,
	".next": true, ".cache": true, "vendor": true, "Pods": true,
}

// FindNamed walks root (as seen through tx) and returns the virtual paths of
// files whose base name is in names. It visits at most limit entries.
func FindNamed(tx *journal.Tx, root string, names map[string]bool, limit int) []string {
	real := tx.Real(root)
	var out []string
	seen := 0
	_ = filepath.WalkDir(real, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		seen++
		if seen > limit {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if p != real && SkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if names[d.Name()] {
			rel, _ := filepath.Rel(real, p)
			out = append(out, filepath.Join(root, rel))
		}
		return nil
	})
	return out
}

// FindExt walks dir (as seen through tx) and returns the virtual paths of
// files with the given extension.
func FindExt(tx *journal.Tx, dir, ext string) []string {
	real := tx.Real(dir)
	var out []string
	_ = filepath.WalkDir(real, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Ext(p) == ext {
			rel, _ := filepath.Rel(real, p)
			out = append(out, filepath.Join(dir, rel))
		}
		return nil
	})
	return out
}

// RewriteFile rewrites the path prefix old to new inside the file p. With
// asJSON the paths are matched in their JSON-escaped form.
func RewriteFile(tx *journal.Tx, p string, asJSON bool, old, new, desc string) error {
	if !tx.Exists(p) {
		return nil
	}
	data, err := tx.ReadFile(p)
	if err != nil {
		return nil // unreadable state is reported by verify, not fatal here
	}
	needle := old
	if asJSON {
		needle = rewrite.EscapeJSON(old)
	}
	if !bytes.Contains(data, []byte(needle)) {
		return nil
	}
	var out []byte
	var n int
	if asJSON {
		out, n = rewrite.JSON(data, old, new)
	} else {
		out, n = rewrite.Text(data, old, new)
	}
	if n == 0 {
		return nil
	}
	return tx.WriteFile(p, out, desc, n)
}
