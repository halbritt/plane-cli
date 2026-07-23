package cli

import (
	"runtime"

	"github.com/spf13/cobra"
)

// Version and Commit are stamped at build time via -ldflags
// (see Makefile); "dev" means a plain `go build`.
var (
	Version = "dev"
	Commit  = ""
)

// TargetAPI is the Plane release whose public API surface this CLI was
// built and tested against.
const TargetAPI = "v1.3.1"

func newVersionCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show plane-cli version and the Plane API it targets",
		Long: `Show the CLI version, source commit, and the Plane release whose
public API this binary was built against.

Examples:
  plane version
  plane version | jq -r .data.version`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.success(map[string]string{
				"version":    Version,
				"commit":     Commit,
				"target_api": TargetAPI,
				"go":         runtime.Version(),
			}, nil)
		},
	}
}
