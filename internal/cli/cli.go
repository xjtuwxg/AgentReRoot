// Package cli implements the reroot command line.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// Version is set at build time with -ldflags "-X .../internal/cli.Version=...".
var Version = "0.1.0-dev"

type app struct {
	json bool
	out  io.Writer
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	a := &app{out: os.Stdout}
	root := &cobra.Command{
		Use:   "reroot",
		Short: "Reorganize a workspace and keep coding agents' memory intact",
		Long: `reroot reorganizes the projects in a workspace (move, merge, archive)
and migrates the path-keyed state of coding agents (Claude Code, Codex) with
every change, so sessions and memory keep working at the new locations.

Typical loop:
  reroot scan ~/workspace --json > scan.json   # facts for an agent or a human
  $EDITOR plan.yaml                            # decide (see: reroot plan example)
  reroot apply plan.yaml                       # dry run
  reroot apply plan.yaml --yes                 # execute, journaled
  reroot verify ~/workspace                    # look for orphaned agent state
  reroot undo                                  # roll back the last apply`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
	}
	root.PersistentFlags().BoolVar(&a.json, "json", false, "machine-readable JSON output")
	root.AddCommand(a.scanCmd(), a.planCmd(), a.applyCmd(), a.undoCmd(), a.historyCmd(), a.verifyCmd())
	if err := root.Execute(); err != nil {
		if a.json {
			a.emit(map[string]string{"error": err.Error()})
		} else {
			fmt.Fprintln(os.Stderr, "reroot:", err)
		}
		return 1
	}
	return 0
}

func (a *app) emit(v any) error {
	enc := json.NewEncoder(a.out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (a *app) printf(format string, args ...any) {
	fmt.Fprintf(a.out, format, args...)
}
