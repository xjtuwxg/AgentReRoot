package journal

import (
	"os"

	"github.com/xjtuwxg/AgentReRoot/internal/pathx"
)

// Overlay simulates renames and directory creation without touching the
// filesystem. It lets a dry run (and plan validation) see the world as it
// would look after the earlier operations of a plan.
type Overlay struct {
	moves []move
	dirs  map[string]bool
	files map[string][]byte
}

type move struct{ from, to string }

// NewOverlay returns an empty overlay.
func NewOverlay() *Overlay {
	return &Overlay{dirs: map[string]bool{}, files: map[string][]byte{}}
}

// Rename records a virtual rename.
func (o *Overlay) Rename(from, to string) {
	o.moves = append(o.moves, move{from, to})
}

// Mkdir records a virtual directory.
func (o *Overlay) Mkdir(p string) { o.dirs[p] = true }

// WriteFile records virtual file content.
func (o *Overlay) WriteFile(p string, data []byte) { o.files[p] = data }

// Real maps a virtual path to the real path that currently holds its content.
// ok is false when the path was moved away and nothing replaced it.
func (o *Overlay) Real(p string) (real string, ok bool) {
	for i := len(o.moves) - 1; i >= 0; i-- {
		m := o.moves[i]
		if r, hit := pathx.Rebase(p, m.to, m.from); hit {
			p = r
			continue
		}
		if pathx.Within(p, m.from) {
			return "", false
		}
	}
	return p, true
}

// Exists reports whether p would exist.
func (o *Overlay) Exists(p string) bool {
	if o.dirs[p] {
		return true
	}
	if _, ok := o.files[p]; ok {
		return true
	}
	r, ok := o.Real(p)
	if !ok {
		return false
	}
	_, err := os.Lstat(r)
	return err == nil
}

// IsDir reports whether p would be a directory.
func (o *Overlay) IsDir(p string) bool {
	if o.dirs[p] {
		return true
	}
	r, ok := o.Real(p)
	if !ok {
		return false
	}
	st, err := os.Stat(r)
	return err == nil && st.IsDir()
}
