// Command swarmdash is a single binary that runs in one of two modes:
//
//	swarmdash agent   - runs on every swarm node, exposes node-local Docker
//	                     operations (exec, logs, stats) to the admin process.
//	swarmdash admin   - runs on a manager node, serves the web UI and talks
//	                     to the swarm-wide Docker API plus each node's agent.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"swarmdash/internal/version"
)

func main() {
	root := &cobra.Command{
		Use:           "swarmdash",
		Short:         "Docker Swarm admin panel (agent + admin in one binary)",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newAgentCmd())
	root.AddCommand(newAdminCmd())
	root.AddCommand(newTLSCmd())
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("swarmdash %s (commit %s, built %s)\n", version.Version, version.Commit, version.Date)
			return nil
		},
	})

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
