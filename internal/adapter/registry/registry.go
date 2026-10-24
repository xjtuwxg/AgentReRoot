// Package registry lists the agent adapters reroot knows about.
package registry

import (
	"github.com/xjtuwxg/AgentReRoot/internal/adapter"
	"github.com/xjtuwxg/AgentReRoot/internal/adapter/claude"
	"github.com/xjtuwxg/AgentReRoot/internal/adapter/codex"
)

// Available returns the adapters whose agent state exists on this machine.
func Available() []adapter.Adapter {
	var out []adapter.Adapter
	for _, a := range []adapter.Adapter{claude.New(), codex.New()} {
		if a.Available() {
			out = append(out, a)
		}
	}
	return out
}
