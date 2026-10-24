package journal

import (
	"database/sql"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go driver, keeps the binary cgo-free

	"github.com/xjtuwxg/AgentReRoot/internal/pathx"
)

// OpenSQLite opens a SQLite database with a busy timeout suitable for files
// that another program (an agent) may be writing concurrently.
func OpenSQLite(path string, readOnly bool) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)"
	if readOnly {
		dsn += "&mode=ro"
	}
	return sql.Open("sqlite", dsn)
}

// HasTable reports whether table exists in db.
func HasTable(db *sql.DB, table string) bool {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n)
	return err == nil && n > 0
}

// SQLPrefix rewrites the path prefix old to new in table.column. The affected
// row ids are journaled so that undo touches exactly the same rows.
func (tx *Tx) SQLPrefix(dbPath, table, column, old, new, desc string) (int, error) {
	db, err := OpenSQLite(dbPath, tx.DryRun)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	if !HasTable(db, table) {
		return 0, nil
	}
	ids, vals, err := matchRows(db, table, column, old)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	tx.record(Action{Kind: "sql", Desc: desc, Path: dbPath, Count: len(ids)})
	if tx.DryRun {
		return len(ids), nil
	}
	if !tx.dbBackup[dbPath] {
		backup := filepath.Join(tx.dir, "backup", fmt.Sprintf("%d-%s", tx.seq+1, filepath.Base(dbPath)))
		if _, err := db.Exec(`VACUUM INTO ?`, backup); err != nil {
			return 0, fmt.Errorf("backup %s: %w", dbPath, err)
		}
		tx.dbBackup[dbPath] = true
	}
	seq, err := tx.begin(Entry{Kind: "sql-prefix", DB: dbPath, Table: table, Column: column, From: old, To: new, RowIDs: ids})
	if err != nil {
		return 0, err
	}
	if err := updateRows(db, table, column, ids, vals, old, new); err != nil {
		return 0, err
	}
	return len(ids), tx.commit(seq)
}

func matchRows(db *sql.DB, table, column, prefix string) ([]int64, []string, error) {
	q := fmt.Sprintf(`SELECT rowid, %[1]s FROM %[2]s WHERE %[1]s = ? OR instr(%[1]s, ?) = 1`, quoteIdent(column), quoteIdent(table))
	rows, err := db.Query(q, prefix, prefix+string(filepath.Separator))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ids []int64
	var vals []string
	for rows.Next() {
		var id int64
		var v string
		if err := rows.Scan(&id, &v); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		vals = append(vals, v)
	}
	return ids, vals, rows.Err()
}

func selectRows(db *sql.DB, table, column string, want []int64) ([]int64, []string, error) {
	q := fmt.Sprintf(`SELECT %s FROM %s WHERE rowid = ?`, quoteIdent(column), quoteIdent(table))
	var ids []int64
	var vals []string
	for _, id := range want {
		var v string
		err := db.QueryRow(q, id).Scan(&v)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		vals = append(vals, v)
	}
	return ids, vals, nil
}

func updateRows(db *sql.DB, table, column string, ids []int64, vals []string, old, new string) error {
	t, err := db.Begin()
	if err != nil {
		return err
	}
	q := fmt.Sprintf(`UPDATE %s SET %s = ? WHERE rowid = ?`, quoteIdent(table), quoteIdent(column))
	for i, id := range ids {
		nv, ok := pathx.Rebase(vals[i], old, new)
		if !ok {
			continue
		}
		if _, err := t.Exec(q, nv, id); err != nil {
			t.Rollback()
			return err
		}
	}
	return t.Commit()
}

func quoteIdent(s string) string { return `"` + s + `"` }
