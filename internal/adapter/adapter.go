// Package adapter defines how reroot learns about, and rewrites, the
// path-keyed state that coding agents keep about projects.
package adapter

import (
	"time"

	"github.com/xjtuwxg/AgentReRoot/internal/journal"
)

// Footprint summarizes what an agent remembers about one project.
type Footprint struct {
	Agent       string    `json:"agent"`
	Sessions    int       `json:"sessions"`
	MemoryFiles int       `json:"memory_files,omitempty"`
	LastActive  time.Time `json:"last_active,omitzero"`
}

// Finding is a problem reported by Verify.
type Finding struct {
	Agent  string `json:"agent"`
	Kind   string `json:"kind"` // orphaned-state, stale-reference, ...
	Path   string `json:"path"`
	Detail string `json:"detail"`
}

// Adapter knows one agent's on-disk state.
type Adapter interface {
	// Name is a short identifier such as "claude" or "codex".
	Name() string
	// Available reports whether this agent's state exists on this machine.
	Available() bool
	// KnownPaths lists every project path the agent has state for.
	KnownPaths() []string
	// Footprint reports the agent's state for projectPath and its subdirectories.
	Footprint(projectPath string) Footprint
	// Migrate rewrites the agent's state after oldPath was moved to newPath.
	Migrate(tx *journal.Tx, oldPath, newPath string) error
	// MergeMemory makes the memory of every source visible from into.
	// Sources have already been moved (they are their new paths).
	MergeMemory(tx *journal.Tx, sources []string, into string) error
	// Verify reports orphaned or stale state that refers to paths under root.
	Verify(root string) []Finding
}
