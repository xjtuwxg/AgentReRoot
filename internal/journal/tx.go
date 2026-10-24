// Package journal executes filesystem and database mutations through a
// write-ahead journal so that every apply can be undone.
package journal

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Action is a human/machine readable description of one step of an apply.
type Action struct {
	Kind   string `json:"kind"`
	Desc   string `json:"desc"`
	Path   string `json:"path,omitempty"`
	Target string `json:"target,omitempty"`
	Count  int    `json:"count,omitempty"`
}

// Entry is one line of journal.jsonl.
type Entry struct {
	Seq    int     `json:"seq"`
	Phase  string  `json:"phase"` // "begin" or "commit"
	Kind   string  `json:"kind,omitempty"`
	From   string  `json:"from,omitempty"`
	To     string  `json:"to,omitempty"`
	Path   string  `json:"path,omitempty"`
	Backup string  `json:"backup,omitempty"`
	Hash   string  `json:"hash,omitempty"`
	DB     string  `json:"db,omitempty"`
	Table  string  `json:"table,omitempty"`
	Column string  `json:"column,omitempty"`
	RowIDs []int64 `json:"rowids,omitempty"`
}

// Meta describes a journal as a whole.
type Meta struct {
	ID       string    `json:"id"`
	Created  time.Time `json:"created"`
	PlanFile string    `json:"plan_file,omitempty"`
	Root     string    `json:"root,omitempty"`
	Status   string    `json:"status"` // running, applied, failed, undone
	Summary  []string  `json:"summary,omitempty"`
}

// Tx is a journaled transaction. With DryRun set it only records Actions.
type Tx struct {
	DryRun  bool
	Actions []Action

	meta     Meta
	dir      string
	f        *os.File
	seq      int
	ov       *Overlay
	dbBackup map[string]bool
}

// Home returns the reroot state directory ($REROOT_HOME or ~/.reroot).
func Home() (string, error) {
	if h := os.Getenv("REROOT_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".reroot"), nil
}

// Begin starts a transaction. A dry run creates no files.
func Begin(dryRun bool, meta Meta) (*Tx, error) {
	tx := &Tx{DryRun: dryRun, ov: NewOverlay(), dbBackup: map[string]bool{}}
	if dryRun {
		return tx, nil
	}
	home, err := Home()
	if err != nil {
		return nil, err
	}
	meta.ID = newID()
	meta.Created = time.Now().UTC()
	meta.Status = "running"
	tx.meta = meta
	tx.dir = filepath.Join(home, "journal", meta.ID)
	if err := os.MkdirAll(filepath.Join(tx.dir, "backup"), 0o700); err != nil {
		return nil, err
	}
	if err := tx.writeMeta(); err != nil {
		return nil, err
	}
	tx.f, err = os.OpenFile(filepath.Join(tx.dir, "journal.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return tx, nil
}

// ID returns the journal id (empty for dry runs).
func (tx *Tx) ID() string { return tx.meta.ID }

// Finish records the final status of the transaction.
func (tx *Tx) Finish(err error, summary []string) error {
	if tx.DryRun {
		return nil
	}
	tx.meta.Status = "applied"
	if err != nil {
		tx.meta.Status = "failed"
	}
	tx.meta.Summary = summary
	if cerr := tx.f.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if werr := tx.writeMeta(); werr != nil {
		return werr
	}
	return nil
}

func (tx *Tx) writeMeta() error {
	return writeJSON(filepath.Join(tx.dir, "meta.json"), tx.meta)
}

func newID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

func (tx *Tx) begin(e Entry) (int, error) {
	tx.seq++
	e.Seq = tx.seq
	e.Phase = "begin"
	return e.Seq, tx.append(e)
}

func (tx *Tx) commit(seq int) error {
	return tx.append(Entry{Seq: seq, Phase: "commit"})
}

func (tx *Tx) append(e Entry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := tx.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return tx.f.Sync()
}

func (tx *Tx) record(a Action) { tx.Actions = append(tx.Actions, a) }

// Real maps a path to where its content currently lives (identity unless dry run).
func (tx *Tx) Real(p string) string {
	if !tx.DryRun {
		return p
	}
	r, ok := tx.ov.Real(p)
	if !ok {
		return filepath.Join(string(filepath.Separator), ".reroot-missing", p)
	}
	return r
}

// Exists reports whether p exists, taking a dry run's virtual state into account.
func (tx *Tx) Exists(p string) bool {
	if tx.DryRun {
		return tx.ov.Exists(p)
	}
	_, err := os.Lstat(p)
	return err == nil
}

// IsDir reports whether p is a directory, taking virtual state into account.
func (tx *Tx) IsDir(p string) bool {
	if tx.DryRun {
		return tx.ov.IsDir(p)
	}
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// ReadFile reads p, taking virtual state into account.
func (tx *Tx) ReadFile(p string) ([]byte, error) {
	if tx.DryRun {
		if b, ok := tx.ov.files[p]; ok {
			return b, nil
		}
	}
	return os.ReadFile(tx.Real(p))
}

// ReadDir lists p, taking virtual state into account.
func (tx *Tx) ReadDir(p string) ([]os.DirEntry, error) {
	return os.ReadDir(tx.Real(p))
}

// Note records an informational action that changes nothing.
func (tx *Tx) Note(kind, desc string) {
	tx.record(Action{Kind: kind, Desc: desc})
}

// Rename moves from to to. The parent of to must exist.
func (tx *Tx) Rename(from, to, desc string) error {
	tx.record(Action{Kind: "rename", Desc: desc, Path: from, Target: to})
	if tx.DryRun {
		tx.ov.Rename(from, to)
		return nil
	}
	seq, err := tx.begin(Entry{Kind: "rename", From: from, To: to})
	if err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", from, to, err)
	}
	return tx.commit(seq)
}

// MkdirAll creates p and any missing parents, journaling each created directory.
func (tx *Tx) MkdirAll(p string) error {
	var missing []string
	for d := p; !tx.Exists(d); d = filepath.Dir(d) {
		missing = append(missing, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		d := missing[i]
		tx.record(Action{Kind: "mkdir", Desc: "create directory", Path: d})
		if tx.DryRun {
			tx.ov.Mkdir(d)
			continue
		}
		seq, err := tx.begin(Entry{Kind: "mkdir", Path: d})
		if err != nil {
			return err
		}
		if err := os.Mkdir(d, 0o755); err != nil {
			return err
		}
		if err := tx.commit(seq); err != nil {
			return err
		}
	}
	return nil
}

// WriteFile replaces (or creates) p with data, keeping a backup of the original.
func (tx *Tx) WriteFile(p string, data []byte, desc string, count int) error {
	exists := tx.Exists(p)
	kind := "write"
	if !exists {
		kind = "create"
	}
	tx.record(Action{Kind: kind, Desc: desc, Path: p, Count: count})
	if tx.DryRun {
		tx.ov.WriteFile(p, data)
		return nil
	}
	e := Entry{Kind: kind, Path: p, Hash: hash(data)}
	mode := os.FileMode(0o644)
	if exists {
		st, err := os.Stat(p)
		if err != nil {
			return err
		}
		mode = st.Mode().Perm()
		e.Backup = fmt.Sprintf("backup/%d-%s", tx.seq+1, filepath.Base(p))
		if err := copyFile(p, filepath.Join(tx.dir, e.Backup)); err != nil {
			return err
		}
	}
	seq, err := tx.begin(e)
	if err != nil {
		return err
	}
	if err := writeAtomic(p, data, mode); err != nil {
		return err
	}
	return tx.commit(seq)
}

func hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}

// writeAtomic writes through a temp file in the same directory and renames it
// into place, so readers never observe a half-written file.
func writeAtomic(p string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), ".reroot-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func writeJSON(p string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(p, append(b, '\n'), 0o600)
}

// List returns all journals, newest first.
func List() ([]Meta, error) {
	home, err := Home()
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(filepath.Join(home, "journal"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		m, err := readMeta(filepath.Join(home, "journal", e.Name()))
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func readMeta(dir string) (Meta, error) {
	var m Meta
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	return m, err
}

func readEntries(dir string) ([]Entry, error) {
	b, err := os.ReadFile(filepath.Join(dir, "journal.jsonl"))
	if err != nil {
		return nil, err
	}
	begun := map[int]Entry{}
	var order []int
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("corrupt journal line: %w", err)
		}
		switch e.Phase {
		case "begin":
			begun[e.Seq] = e
			order = append(order, e.Seq)
		case "commit":
			c := begun[e.Seq]
			c.Phase = "commit"
			begun[e.Seq] = c
		}
	}
	out := make([]Entry, 0, len(order))
	for _, s := range order {
		out = append(out, begun[s])
	}
	return out, nil
}
