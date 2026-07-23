package cli

import (
	"github.com/spf13/cobra"
)

func newMeCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "me",
		Short: "Show the user owning the API key",
		Long: `Show the user that the configured API key authenticates as.
Useful as a cheap auth/connectivity check.

Examples:
  plane me
  plane me --debug   # trace the request to stderr`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, _, err := a.Client(false)
			if err != nil {
				return a.fail(err)
			}
			return a.runGet(cmd.Context(), cl, "/api/v1/users/me/", nil)
		},
	}
}

func newWorkspaceCmd(a *App) *cobra.Command {
	ws := &cobra.Command{
		Use:   "workspace",
		Short: "Workspace-level reads (the public API has no workspace CRUD)",
	}

	members := &cobra.Command{
		Use:   "members",
		Short: "List workspace members",
		Long: `List all members of the configured workspace.

The response is a plain array (this endpoint is not paginated server-side);
each element includes the member's workspace role (20 admin, 15 member,
5 guest).

Examples:
  plane workspace members
  plane workspace members -w myworkspace`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, err := a.Client(true)
			if err != nil {
				return a.fail(err)
			}
			return a.runGet(cmd.Context(), cl, wsPath(cfg, "members"), nil)
		},
	}
	ws.AddCommand(members)
	return ws
}
