package journal

import (
	"fmt"
	"os"
	"path/filepath"
)

// Undo reverts the journal with the given id ("" means the newest applied or
// failed one). Files modified since the apply are skipped unless force is set.
func Undo(id string, force bool) (Meta, []Action, error) {
	home, err := Home()
	if err != nil {
		return Meta{}, nil, err
	}
	if id == "" {
		all, err := List()
		if err != nil {
			return Meta{}, nil, err
		}
		for _, m := range all {
			if m.Status == "applied" || m.Status == "failed" {
				id = m.ID
				break
			}
		}
		if id == "" {
			return Meta{}, nil, fmt.Errorf("no journal to undo")
		}
	}
	dir := filepath.Join(home, "journal", id)
	meta, err := readMeta(dir)
	if err != nil {
		return meta, nil, fmt.Errorf("journal %s: %w", id, err)
	}
	if meta.Status == "undone" {
		return meta, nil, fmt.Errorf("journal %s was already undone", id)
	}
	entries, err := readEntries(dir)
	if err != nil {
		return meta, nil, err
	}
	var acts []Action
	var failed []string
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		a, err := revert(dir, e, force)
		if err != nil {
			failed = append(failed, err.Error())
			acts = append(acts, Action{Kind: "skip", Desc: err.Error(), Path: e.Path})
			continue
		}
		if a != nil {
			acts = append(acts, *a)
		}
	}
	if len(failed) == 0 {
		meta.Status = "undone"
	}
	if err := writeJSON(filepath.Join(dir, "meta.json"), meta); err != nil {
		return meta, acts, err
	}
	if len(failed) > 0 {
		return meta, acts, fmt.Errorf("%d step(s) could not be undone; rerun with --force after checking them", len(failed))
	}
	return meta, acts, nil
}

func revert(dir string, e Entry, force bool) (*Action, error) {
	switch e.Kind {
	case "rename":
		_, errTo := os.Lstat(e.To)
		_, errFrom := os.Lstat(e.From)
		if errTo != nil {
			if e.Phase != "commit" {
				return nil, nil // never happened
			}
			return nil, fmt.Errorf("cannot move back %s: it no longer exists", e.To)
		}
		if errFrom == nil {
			return nil, fmt.Errorf("cannot move %s back: %s exists again", e.To, e.From)
		}
		if err := os.Rename(e.To, e.From); err != nil {
			return nil, err
		}
		return &Action{Kind: "rename", Desc: "move back", Path: e.To, Target: e.From}, nil
	case "mkdir":
		if err := os.Remove(e.Path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("leave directory %s: %v", e.Path, err)
		}
		return &Action{Kind: "rmdir", Desc: "remove created directory", Path: e.Path}, nil
	case "write", "create":
		cur, err := os.ReadFile(e.Path)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		changed := err != nil || hash(cur) != e.Hash
		if changed && e.Phase != "commit" {
			return nil, nil // the write never happened
		}
		if changed && !force {
			return nil, fmt.Errorf("%s changed since apply", e.Path)
		}
		if e.Kind == "create" {
			if err := os.Remove(e.Path); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
			return &Action{Kind: "delete", Desc: "remove created file", Path: e.Path}, nil
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Backup))
		if err != nil {
			return nil, err
		}
		mode := os.FileMode(0o644)
		if st, err := os.Stat(e.Path); err == nil {
			mode = st.Mode().Perm()
		}
		if err := writeAtomic(e.Path, b, mode); err != nil {
			return nil, err
		}
		return &Action{Kind: "restore", Desc: "restore original content", Path: e.Path}, nil
	case "sql-prefix":
		db, err := OpenSQLite(e.DB, false)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		ids, vals, err := selectRows(db, e.Table, e.Column, e.RowIDs)
		if err != nil {
			return nil, err
		}
		if err := updateRows(db, e.Table, e.Column, ids, vals, e.To, e.From); err != nil {
			return nil, err
		}
		return &Action{Kind: "sql", Desc: fmt.Sprintf("restore %s.%s", e.Table, e.Column), Path: e.DB, Count: len(ids)}, nil
	}
	return nil, nil
}
