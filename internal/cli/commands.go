package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/spf13/cobra"

	"github.com/xjtuwxg/AgentReRoot/internal/adapter"
	"github.com/xjtuwxg/AgentReRoot/internal/adapter/gitx"
	"github.com/xjtuwxg/AgentReRoot/internal/adapter/registry"
	"github.com/xjtuwxg/AgentReRoot/internal/apply"
	"github.com/xjtuwxg/AgentReRoot/internal/journal"
	"github.com/xjtuwxg/AgentReRoot/internal/pathx"
	"github.com/xjtuwxg/AgentReRoot/internal/plan"
	"github.com/xjtuwxg/AgentReRoot/internal/scan"
	"github.com/xjtuwxg/AgentReRoot/internal/ui"
)

func rootArg(args []string) (string, error) {
	p := "."
	if len(args) > 0 {
		p = args[0]
	}
	return pathx.Abs(p, "")
}

func (a *app) scanCmd() *cobra.Command {
	var depth int
	cmd := &cobra.Command{
		Use:   "scan [root]",
		Short: "Collect facts about every project under root",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := rootArg(args)
			if err != nil {
				return err
			}
			r, err := scan.Run(root, scan.Options{MaxDepth: depth, Adapters: registry.Available()})
			if err != nil {
				return err
			}
			if a.json {
				return a.emit(r)
			}
			a.printScan(r)
			return nil
		},
	}
	cmd.Flags().IntVar(&depth, "depth", 5, "maximum directory depth to look for projects")
	return cmd
}

func (a *app) printScan(r *scan.Report) {
	a.printf("%s: %d projects in %d groups (agents: %s)\n\n", r.Root, len(r.Projects), len(r.Groups), strings.Join(r.Agents, ", "))
	counts := map[string]int{}
	for _, p := range r.Projects {
		counts[p.Activity]++
		last := "-"
		if !p.LastActivity.IsZero() {
			last = humanize.Time(p.LastActivity)
		}
		var agents []string
		for _, f := range p.Agents {
			agents = append(agents, fmt.Sprintf("%s:%d", f.Agent, f.Sessions))
		}
		a.printf("  %-8s %-48s %-14s %-22s %s\n", p.Activity, p.Rel, last, strings.Join(p.Languages, ","), strings.Join(agents, " "))
	}
	a.printf("\n  active %d, warm %d, dormant %d, stale %d, unknown %d\n",
		counts["active"], counts["warm"], counts["dormant"], counts["stale"], counts["unknown"])
	if len(r.Duplicates) > 0 {
		a.printf("\nPossible overlaps:\n")
		for _, d := range r.Duplicates {
			a.printf("  - %s: %s\n", d.Reason, strings.Join(d.Projects, ", "))
		}
	}
}

const examplePlan = `# reroot plan. Paths are relative to root. Operations run in order.
version: 1
root: ~/workspace
operations:
  - op: move                      # move or rename one directory
    from: ai/sec-scanner
    to: security/sec-scanner
    reason: "grew from an LLM demo into a security tool"

  - op: merge                     # sources become into/<name>; agent memory is merged
    sources: [ai/paper-bot, tools/paper-helper]
    into: research/paper-tools
    reason: "two attempts at the same problem"

  - op: archive                   # default destination: _archive/<path>
    path: misc/old-demo
    reason: "no commits for 2 years"
`

func (a *app) planCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "plan", Short: "Work with plan files"}
	cmd.AddCommand(&cobra.Command{
		Use:   "example",
		Short: "Print an annotated example plan",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a.printf("%s", examplePlan)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "check <plan.yaml>",
		Short: "Validate a plan against the filesystem without changing anything",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := plan.Load(args[0])
			if err != nil {
				return err
			}
			steps, errs := p.Resolve()
			if a.json {
				var msgs []string
				for _, e := range errs {
					msgs = append(msgs, e.Error())
				}
				if err := a.emit(map[string]any{"valid": len(errs) == 0, "operations": len(steps), "errors": msgs}); err != nil {
					return err
				}
			} else {
				for _, e := range errs {
					a.printf("error: %v\n", e)
				}
				if len(errs) == 0 {
					a.printf("plan is valid: %d operation(s)\n", len(steps))
				}
			}
			if len(errs) > 0 {
				return fmt.Errorf("plan has %d error(s)", len(errs))
			}
			return nil
		},
	})
	return cmd
}

func (a *app) applyCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "apply <plan.yaml>",
		Short: "Apply a plan (dry run unless --yes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := plan.Load(args[0])
			if err != nil {
				return err
			}
			planFile, _ := pathx.Abs(args[0], "")
			res, err := apply.Run(p, apply.Options{DryRun: !yes, PlanFile: planFile, Adapters: registry.Available()})
			if res != nil {
				if a.json {
					if jerr := a.emit(res); jerr != nil {
						return jerr
					}
				} else {
					a.printApply(res)
				}
			}
			if err != nil && res != nil && res.JournalID != "" {
				return fmt.Errorf("%w\npartial changes are journaled; roll back with: reroot undo %s", err, res.JournalID)
			}
			return err
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "really apply the plan")
	return cmd
}

func (a *app) printApply(res *apply.Result) {
	for _, w := range res.Warnings {
		a.printf("warning: %s\n", w)
	}
	for _, act := range res.Actions {
		a.printAction(act)
	}
	switch {
	case res.DryRun:
		a.printf("\ndry run: %d action(s). Re-run with --yes to apply.\n", len(res.Actions))
	default:
		a.printf("\napplied %d action(s). Journal: %s (undo with: reroot undo %s)\n", len(res.Actions), res.JournalID, res.JournalID)
	}
}

func (a *app) printAction(act journal.Action) {
	switch {
	case act.Target != "":
		a.printf("  %-7s %s -> %s\n", act.Kind, act.Path, act.Target)
	case act.Path != "":
		suffix := ""
		if act.Count > 0 {
			suffix = fmt.Sprintf(" (%d)", act.Count)
		}
		a.printf("  %-7s %s%s  [%s]\n", act.Kind, act.Path, suffix, act.Desc)
	default:
		a.printf("  %-7s %s\n", act.Kind, act.Desc)
	}
}

func (a *app) undoCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "undo [journal-id]",
		Short: "Roll back an apply (default: the most recent one)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			meta, acts, err := journal.Undo(id, force)
			tx, _ := journal.Begin(true, journal.Meta{})
			for _, act := range acts {
				if act.Kind == "rename" && act.Target != "" {
					gitx.Repair(tx, act.Target)
				}
			}
			for _, n := range tx.Actions {
				acts = append(acts, journal.Action{Kind: "note", Desc: "run: " + n.Desc})
			}
			if a.json {
				if jerr := a.emit(map[string]any{"journal": meta, "actions": acts}); jerr != nil {
					return jerr
				}
			} else {
				for _, act := range acts {
					a.printAction(act)
				}
				if err == nil {
					a.printf("\nundone journal %s\n", meta.ID)
				}
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "restore files even if they changed since the apply")
	return cmd
}

func (a *app) historyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "history",
		Short: "List journals of past applies",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			all, err := journal.List()
			if err != nil {
				return err
			}
			if a.json {
				return a.emit(all)
			}
			if len(all) == 0 {
				a.printf("no journals yet\n")
			}
			for _, m := range all {
				a.printf("%s  %-8s %s  %s\n", m.ID, m.Status, m.Created.Local().Format(time.DateTime), m.Root)
				for _, s := range m.Summary {
					a.printf("    %s\n", s)
				}
			}
			return nil
		},
	}
}

func (a *app) verifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify [root]",
		Short: "Report agent state that points to missing paths under root",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := rootArg(args)
			if err != nil {
				return err
			}
			var all []adapter.Finding
			for _, ad := range registry.Available() {
				all = append(all, ad.Verify(root)...)
			}
			sort.Slice(all, func(i, j int) bool {
				if all[i].Kind != all[j].Kind {
					return all[i].Kind < all[j].Kind
				}
				return all[i].Path < all[j].Path
			})
			if a.json {
				if all == nil {
					all = []adapter.Finding{}
				}
				return a.emit(all)
			}
			if len(all) == 0 {
				a.printf("no problems found under %s\n", root)
				return nil
			}
			for _, f := range all {
				a.printf("  %-7s %-17s %s  (%s)\n", f.Agent, f.Kind, f.Path, f.Detail)
			}
			a.printf("\n%d finding(s)\n", len(all))
			return nil
		},
	}
}

func (a *app) uiCmd() *cobra.Command {
	var planFile, addr string
	var depth int
	var noOpen bool
	cmd := &cobra.Command{
		Use:   "ui [root]",
		Short: "Open the local web UI: workspace map, plan editor, dry run, apply, undo",
		Long: `Starts a web UI on 127.0.0.1 and opens it in the browser. The UI edits a
plan file (default ~/.reroot/plans/<root>.yaml, or --plan), so a plan an
agent wrote can be reviewed and applied here, and vice versa.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := rootArg(args)
			if err != nil {
				return err
			}
			if planFile == "" {
				if planFile, err = ui.DefaultPlanFile(root); err != nil {
					return err
				}
			} else if planFile, err = pathx.Abs(planFile, ""); err != nil {
				return err
			}
			return ui.Serve(ui.Options{Root: root, PlanFile: planFile, Addr: addr, Depth: depth, Open: !noOpen}, func(url string) {
				a.printf("reroot ui for %s\nplan file: %s\n\n  %s\n\nPress Ctrl+C to stop.\n", root, planFile, url)
			})
		},
	}
	cmd.Flags().StringVar(&planFile, "plan", "", "plan file to edit")
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:0", "listen address (loopback only)")
	cmd.Flags().IntVar(&depth, "depth", 5, "maximum directory depth to look for projects")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "do not open a browser")
	return cmd
}
