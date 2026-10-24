// Package plan parses and validates reorganization plans.
package plan

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/xjtuwxg/AgentReRoot/internal/journal"
	"github.com/xjtuwxg/AgentReRoot/internal/pathx"
)

// Plan is an ordered list of operations on a workspace.
type Plan struct {
	Version    int         `yaml:"version" json:"version"`
	Root       string      `yaml:"root" json:"root"`
	Operations []Operation `yaml:"operations" json:"operations"`
}

// Operation is one step of a plan. Which fields apply depends on Op.
type Operation struct {
	Op      string   `yaml:"op" json:"op"` // move, merge, archive
	From    string   `yaml:"from,omitempty" json:"from,omitempty"`
	To      string   `yaml:"to,omitempty" json:"to,omitempty"`
	Sources []string `yaml:"sources,omitempty" json:"sources,omitempty"`
	Into    string   `yaml:"into,omitempty" json:"into,omitempty"`
	Path    string   `yaml:"path,omitempty" json:"path,omitempty"`
	Reason  string   `yaml:"reason,omitempty" json:"reason,omitempty"`
}

// Move is a single resolved directory move.
type Move struct {
	From, To string
}

// Step is an operation resolved to absolute moves.
type Step struct {
	Index int
	Op    Operation
	Moves []Move
	Into  string // merge target, empty otherwise
}

// ArchiveDir is the default archive area, relative to the plan root.
const ArchiveDir = "_archive"

// Load reads a plan from a YAML or JSON file.
func Load(path string) (*Plan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Plan
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if p.Root == "" {
		return nil, fmt.Errorf("%s: plan has no root", path)
	}
	root, err := pathx.Abs(p.Root, filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	p.Root = root
	return &p, nil
}

// Resolve turns operations into absolute moves and validates them against
// the filesystem, simulating earlier operations for later ones.
func (p *Plan) Resolve() ([]Step, []error) {
	var errs []error
	if p.Version != 0 && p.Version != 1 {
		errs = append(errs, fmt.Errorf("unsupported plan version %d", p.Version))
	}
	if st, err := os.Stat(p.Root); err != nil || !st.IsDir() {
		return nil, append(errs, fmt.Errorf("root %s is not a directory", p.Root))
	}
	ov := journal.NewOverlay()
	var steps []Step
	for i, op := range p.Operations {
		s := Step{Index: i + 1, Op: op}
		fail := func(format string, a ...any) {
			errs = append(errs, fmt.Errorf("operation %d (%s): %s", i+1, op.Op, fmt.Sprintf(format, a...)))
		}
		abs := func(rel string) string {
			a, err := pathx.Abs(rel, p.Root)
			if err != nil {
				fail("%v", err)
			}
			return a
		}
		switch op.Op {
		case "move":
			if op.From == "" || op.To == "" {
				fail("move needs from and to")
				continue
			}
			s.Moves = []Move{{abs(op.From), abs(op.To)}}
		case "archive":
			if op.Path == "" {
				fail("archive needs path")
				continue
			}
			to := op.To
			if to == "" {
				to = filepath.Join(ArchiveDir, op.Path)
			}
			s.Moves = []Move{{abs(op.Path), abs(to)}}
		case "merge":
			if len(op.Sources) == 0 || op.Into == "" {
				fail("merge needs sources and into")
				continue
			}
			s.Into = abs(op.Into)
			seen := map[string]bool{}
			for _, src := range op.Sources {
				from := abs(src)
				to := filepath.Join(s.Into, filepath.Base(from))
				if seen[to] {
					fail("two sources share the name %s", filepath.Base(from))
				}
				seen[to] = true
				s.Moves = append(s.Moves, Move{from, to})
			}
			if ov.Exists(s.Into) && !ov.IsDir(s.Into) {
				fail("into %s exists and is not a directory", s.Into)
			}
		default:
			fail("unknown op %q (want move, merge or archive)", op.Op)
			continue
		}
		for _, m := range s.Moves {
			switch {
			case !pathx.Within(m.From, p.Root) || !pathx.Within(m.To, p.Root):
				fail("%s -> %s leaves the plan root %s", m.From, m.To, p.Root)
			case m.From == p.Root:
				fail("cannot move the plan root itself")
			case m.From == m.To:
				fail("%s is moved onto itself", m.From)
			case pathx.Within(m.To, m.From):
				fail("%s cannot be moved inside itself (%s)", m.From, m.To)
			case !ov.Exists(m.From):
				fail("%s does not exist (at this point of the plan)", m.From)
			case ov.Exists(m.To):
				fail("%s already exists", m.To)
			default:
				ov.Rename(m.From, m.To)
			}
		}
		steps = append(steps, s)
	}
	return steps, errs
}
