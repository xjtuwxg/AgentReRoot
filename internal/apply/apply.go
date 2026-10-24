// Package apply executes a validated plan through a journal and lets every
// agent adapter migrate its state along with each move.
package apply

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/xjtuwxg/AgentReRoot/internal/adapter"
	"github.com/xjtuwxg/AgentReRoot/internal/adapter/gitx"
	"github.com/xjtuwxg/AgentReRoot/internal/journal"
	"github.com/xjtuwxg/AgentReRoot/internal/pathx"
	"github.com/xjtuwxg/AgentReRoot/internal/plan"
)

// Result is what an apply (or dry run) did.
type Result struct {
	DryRun    bool             `json:"dry_run"`
	JournalID string           `json:"journal_id,omitempty"`
	Actions   []journal.Action `json:"actions"`
	Warnings  []string         `json:"warnings,omitempty"`
}

// Options control an apply.
type Options struct {
	DryRun   bool
	PlanFile string
	Adapters []adapter.Adapter
}

// Run executes the plan. On error the journal is kept so the partial work
// can be undone.
func Run(p *plan.Plan, opts Options) (*Result, error) {
	steps, errs := p.Resolve()
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	res := &Result{DryRun: opts.DryRun, Warnings: runningAgents()}
	if err := checkDevices(steps); err != nil {
		return nil, err
	}
	tx, err := journal.Begin(opts.DryRun, journal.Meta{PlanFile: opts.PlanFile, Root: p.Root})
	if err != nil {
		return nil, err
	}
	res.JournalID = tx.ID()
	var summary []string
	runErr := func() error {
		for _, s := range steps {
			tx.Op = s.Index
			for _, m := range s.Moves {
				if err := move(tx, m, opts.Adapters); err != nil {
					return fmt.Errorf("operation %d: %w", s.Index, err)
				}
				summary = append(summary, fmt.Sprintf("%s: %s -> %s", s.Op.Op, pathx.Rel(p.Root, m.From), pathx.Rel(p.Root, m.To)))
			}
			if s.Into != "" {
				var srcs []string
				for _, m := range s.Moves {
					srcs = append(srcs, m.To)
				}
				for _, a := range opts.Adapters {
					if err := a.MergeMemory(tx, srcs, s.Into); err != nil {
						return fmt.Errorf("operation %d: %s: %w", s.Index, a.Name(), err)
					}
				}
			}
		}
		return nil
	}()
	res.Actions = tx.Actions
	if err := tx.Finish(runErr, summary); err != nil && runErr == nil {
		runErr = err
	}
	return res, runErr
}

func move(tx *journal.Tx, m plan.Move, adapters []adapter.Adapter) error {
	if err := tx.MkdirAll(filepath.Dir(m.To)); err != nil {
		return err
	}
	if err := tx.Rename(m.From, m.To, "move directory"); err != nil {
		return err
	}
	for _, a := range adapters {
		if err := a.Migrate(tx, m.From, m.To); err != nil {
			return fmt.Errorf("%s: %w", a.Name(), err)
		}
	}
	gitx.Repair(tx, m.To)
	return nil
}

func checkDevices(steps []plan.Step) error {
	for _, s := range steps {
		for _, m := range s.Moves {
			from, err := os.Stat(m.From)
			if err != nil || !from.IsDir() && !from.Mode().IsRegular() {
				continue // created by an earlier step; checked when it ran
			}
			same, err := pathx.SameDevice(m.From, pathx.NearestExisting(filepath.Dir(m.To)))
			if err == nil && !same {
				return fmt.Errorf("%s -> %s crosses filesystems; not supported yet", m.From, m.To)
			}
		}
	}
	return nil
}

// runningAgents warns about agent processes that may rewrite their state
// files while reroot is changing them.
func runningAgents() []string {
	out, err := exec.Command("ps", "-A", "-o", "comm=").Output()
	if err != nil {
		return nil
	}
	found := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		switch filepath.Base(strings.TrimSpace(l)) {
		case "claude":
			found["Claude Code"] = true
		case "codex":
			found["Codex"] = true
		}
	}
	var w []string
	for name := range found {
		w = append(w, fmt.Sprintf("%s is running; it may overwrite migrated state. Close other sessions and run `reroot verify` afterwards.", name))
	}
	return w
}
