package cli

import (
	"net/http"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

var stateGroups = []string{"backlog", "unstarted", "started", "completed", "cancelled"}

func newStateCmd(a *App) *cobra.Command {
	state := &cobra.Command{
		Use:   "state",
		Short: "Manage project states (list/get/create/update/delete)",
	}

	var lf listFlags
	list := &cobra.Command{
		Use:   "list",
		Short: "List states in the project",
		Long: `List workflow states of the configured project. The built-in
Triage state is hidden by the API.

Examples:
  plane state list -p DEPLOY
  plane state list -p DEPLOY --fields id,name,group`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			return a.runList(cmd.Context(), cl, projPath(cfg, pid, "states"), lf.query(), &lf)
		},
	}
	addListFlags(list, &lf)

	var gf listFlags
	get := &cobra.Command{
		Use:   "get <state>",
		Short: "Get one state by name or UUID",
		Long: `Get a single state by case-insensitive name or UUID.

Examples:
  plane state get "In Progress" -p DEPLOY`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(cmd.Context(), cl, "state", projPath(cfg, pid, "states"), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runGet(cmd.Context(), cl, projPath(cfg, pid, "states", id), gf.query())
		},
	}
	addGetFlags(get, &gf)

	var cName, cColor, cGroup, cDescription, cData string
	var cDefault bool
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a state",
		Long: `Create a workflow state. --name and --color are required.
--group is one of backlog|unstarted|started|completed|cancelled (default
backlog; triage cannot be created via the API). Setting --default clears
the default flag on sibling states.

A duplicate name fails with HTTP 409 (exit 5) and the existing state's id
in error.details.

Examples:
  plane state create -p DEPLOY --name Review --color "#F59E0B" --group started
  plane state create -p DEPLOY --name Blocked --color "#FF0000" --group unstarted`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cName == "" || cColor == "" {
				return a.usageErr("--name and --color are required")
			}
			if cGroup != "" && !slices.Contains(stateGroups, cGroup) {
				return a.usageErr("--group must be one of %s", strings.Join(stateGroups, ", "))
			}
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			fields := map[string]any{"name": cName, "color": cColor}
			if cGroup != "" {
				fields["group"] = cGroup
			}
			if cDescription != "" {
				fields["description"] = cDescription
			}
			if cDefault {
				fields["default"] = true
			}
			body, err := payload(cData, fields)
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(cmd.Context(), cl, http.MethodPost, projPath(cfg, pid, "states"), body)
		},
	}
	create.Flags().StringVar(&cName, "name", "", "state name (required)")
	create.Flags().StringVar(&cColor, "color", "", "hex color, e.g. #60646C (required)")
	create.Flags().StringVar(&cGroup, "group", "", "backlog|unstarted|started|completed|cancelled")
	create.Flags().StringVar(&cDescription, "description", "", "description")
	create.Flags().BoolVar(&cDefault, "default", false, "make this the project's default state")
	create.Flags().StringVar(&cData, "data", "", "additional fields as a JSON object")

	var uData string
	update := &cobra.Command{
		Use:   "update <state>",
		Short: "Update a state (PATCH)",
		Long: `Update a state by name or UUID.

Examples:
  plane state update Review -p DEPLOY --color "#00FF00"
  plane state update Review -p DEPLOY --data '{"description":"code review"}'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(cmd.Context(), cl, "state", projPath(cfg, pid, "states"), args[0])
			if err != nil {
				return a.fail(err)
			}
			fields := map[string]any{}
			changedString(cmd, fields, "name", "name")
			changedString(cmd, fields, "color", "color")
			changedString(cmd, fields, "group", "group")
			changedString(cmd, fields, "description", "description")
			if cmd.Flags().Changed("default") {
				v, _ := cmd.Flags().GetBool("default")
				fields["default"] = v
			}
			body, err := payload(uData, fields)
			if err != nil {
				return a.fail(err)
			}
			if len(body) == 0 {
				return a.usageErr("nothing to update: pass field flags or --data")
			}
			return a.runMutate(cmd.Context(), cl, http.MethodPatch, projPath(cfg, pid, "states", id), body)
		},
	}
	update.Flags().String("name", "", "new name")
	update.Flags().String("color", "", "new color")
	update.Flags().String("group", "", "new group")
	update.Flags().String("description", "", "new description")
	update.Flags().Bool("default", false, "set/unset default")
	update.Flags().StringVar(&uData, "data", "", "fields to change as a JSON object")

	del := &cobra.Command{
		Use:   "delete <state>",
		Short: "Delete a state",
		Long: `Delete a state by name or UUID. The default state and states
that still contain work items cannot be deleted (HTTP 400, exit 5).

Examples:
  plane state delete Review -p DEPLOY`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(cmd.Context(), cl, "state", projPath(cfg, pid, "states"), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(cmd.Context(), cl, http.MethodDelete, projPath(cfg, pid, "states", id), nil)
		},
	}

	state.AddCommand(list, get, create, update, del)
	return state
}
