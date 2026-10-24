// Command reroot reorganizes a workspace while keeping coding agents' memory intact.
package main

import (
	"os"

	"github.com/xjtuwxg/AgentReRoot/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
