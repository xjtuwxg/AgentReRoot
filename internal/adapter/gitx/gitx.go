// Package gitx repairs git worktree links, which store absolute paths, after
// repositories are moved.
package gitx

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/xjtuwxg/AgentReRoot/internal/journal"
)

// LinkedRepos returns repositories under root (as seen through tx) that take
// part in a worktree setup: main repos with linked worktrees, and worktrees.
func LinkedRepos(tx *journal.Tx, root string, maxDepth int) []string {
	real := tx.Real(root)
	var out []string
	_ = filepath.WalkDir(real, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(real, p)
		if depth(rel) > maxDepth {
			return filepath.SkipDir
		}
		if d.Name() == "node_modules" || d.Name() == ".git" {
			return filepath.SkipDir
		}
		gitPath := filepath.Join(p, ".git")
		st, err := os.Lstat(gitPath)
		if err != nil {
			return nil
		}
		linked := !st.IsDir() // a .git file means this is a linked worktree
		if st.IsDir() {
			if ents, err := os.ReadDir(filepath.Join(gitPath, "worktrees")); err == nil && len(ents) > 0 {
				linked = true
			}
		}
		if linked {
			out = append(out, filepath.Join(root, rel))
		}
		return nil
	})
	return out
}

func depth(rel string) int {
	if rel == "." {
		return 0
	}
	n := 1
	for _, c := range rel {
		if c == filepath.Separator {
			n++
		}
	}
	return n
}

// Repair runs `git worktree repair` in every linked repository under root.
// It is idempotent and therefore not journaled.
func Repair(tx *journal.Tx, root string) {
	for _, repo := range LinkedRepos(tx, root, 4) {
		if tx.DryRun {
			tx.Note("git", "git worktree repair in "+repo)
			continue
		}
		out, err := exec.Command("git", "-C", repo, "worktree", "repair").CombinedOutput()
		if err != nil {
			tx.Note("warn", "git worktree repair failed in "+repo+": "+string(out))
			continue
		}
		tx.Note("git", "git worktree repair in "+repo)
	}
}
