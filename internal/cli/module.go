package cli

import (
	"net/http"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

var moduleStatuses = []string{"backlog", "planned", "in-progress", "paused", "completed", "cancelled"}

func newModuleCmd(a *App) *cobra.Command {
	module := &cobra.Command{
		Use:   "module",
		Short: "Manage modules and their work items",
	}

	var lf listFlags
	list := &cobra.Command{
		Use:   "list",
		Short: "List modules in the project",
		Long: `List (non-archived) modules of the configured project.

Examples:
  plane module list -p DEPLOY
  plane module list -p DEPLOY --fields id,name,status`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			return a.runList(cmd.Context(), cl, projPath(cfg, pid, "modules"), lf.query(), &lf)
		},
	}
	addListFlags(list, &lf)

	var gf listFlags
	get := &cobra.Command{
		Use:   "get <module>",
		Short: "Get one module by name or UUID",
		Long: `Get a single (non-archived) module by case-insensitive name or UUID.

Examples:
  plane module get "Auth Overhaul" -p DEPLOY`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(cmd.Context(), cl, "module", projPath(cfg, pid, "modules"), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runGet(cmd.Context(), cl, projPath(cfg, pid, "modules", id), gf.query())
		},
	}
	addGetFlags(get, &gf)

	var cName, cDescription, cStatus, cStart, cTarget, cData string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a module",
		Long: `Create a module. --name is required (duplicate names fail with
HTTP 400). --status is one of backlog|planned|in-progress|paused|completed|
cancelled (default planned).

Examples:
  plane module create -p DEPLOY --name "Auth Overhaul" --status in-progress
  plane module create -p DEPLOY --name Hardening --target-date 2026-09-01`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cName == "" {
				return a.usageErr("--name is required")
			}
			if cStatus != "" && !slices.Contains(moduleStatuses, cStatus) {
				return a.usageErr("--status must be one of %s", strings.Join(moduleStatuses, ", "))
			}
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			fields := map[string]any{"name": cName}
			if cDescription != "" {
				fields["description"] = cDescription
			}
			if cStatus != "" {
				fields["status"] = cStatus
			}
			if cStart != "" {
				fields["start_date"] = cStart
			}
			if cTarget != "" {
				fields["target_date"] = cTarget
			}
			body, err := payload(cData, fields)
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(cmd.Context(), cl, http.MethodPost, projPath(cfg, pid, "modules"), body)
		},
	}
	create.Flags().StringVar(&cName, "name", "", "module name (required)")
	create.Flags().StringVar(&cDescription, "description", "", "description")
	create.Flags().StringVar(&cStatus, "status", "", "backlog|planned|in-progress|paused|completed|cancelled")
	create.Flags().StringVar(&cStart, "start-date", "", "YYYY-MM-DD")
	create.Flags().StringVar(&cTarget, "target-date", "", "YYYY-MM-DD")
	create.Flags().StringVar(&cData, "data", "", "additional fields as a JSON object (e.g. members, lead)")

	var uData string
	update := &cobra.Command{
		Use:   "update <module>",
		Short: "Update a module (PATCH)",
		Long: `Update a module by name or UUID. Archived modules cannot be
edited.

Examples:
  plane module update "Auth Overhaul" -p DEPLOY --status completed`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(cmd.Context(), cl, "module", projPath(cfg, pid, "modules"), args[0])
			if err != nil {
				return a.fail(err)
			}
			if cmd.Flags().Changed("status") {
				v, _ := cmd.Flags().GetString("status")
				if !slices.Contains(moduleStatuses, v) {
					return a.usageErr("--status must be one of %s", strings.Join(moduleStatuses, ", "))
				}
			}
			fields := map[string]any{}
			changedString(cmd, fields, "name", "name")
			changedString(cmd, fields, "description", "description")
			changedString(cmd, fields, "status", "status")
			changedString(cmd, fields, "start-date", "start_date")
			changedString(cmd, fields, "target-date", "target_date")
			body, err := payload(uData, fields)
			if err != nil {
				return a.fail(err)
			}
			if len(body) == 0 {
				return a.usageErr("nothing to update: pass field flags or --data")
			}
			return a.runMutate(cmd.Context(), cl, http.MethodPatch, projPath(cfg, pid, "modules", id), body)
		},
	}
	update.Flags().String("name", "", "new name")
	update.Flags().String("description", "", "new description")
	update.Flags().String("status", "", "new status")
	update.Flags().String("start-date", "", "new start date")
	update.Flags().String("target-date", "", "new target date")
	update.Flags().StringVar(&uData, "data", "", "fields to change as a JSON object")

	del := &cobra.Command{
		Use:   "delete <module>",
		Short: "Delete a module",
		Long: `Delete a module by name or UUID. Only the creator or a project
admin may delete (HTTP 403 otherwise). Work items are unlinked, not deleted.

Examples:
  plane module delete Hardening -p DEPLOY`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(cmd.Context(), cl, "module", projPath(cfg, pid, "modules"), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(cmd.Context(), cl, http.MethodDelete, projPath(cfg, pid, "modules", id), nil)
		},
	}

	addIssues := &cobra.Command{
		Use:   "add-issues <module> <issue>...",
		Short: "Add work items to a module",
		Long: `Add one or more work items (UUID or PROJ-123) to a module. The
response is the full set of module-issue link objects for the module.

Examples:
  plane module add-issues "Auth Overhaul" DEPLOY-42 DEPLOY-43 -p DEPLOY`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(ctx, cl, "module", projPath(cfg, pid, "modules"), args[0])
			if err != nil {
				return a.fail(err)
			}
			ids, err := a.resolveIssueList(ctx, cl, cfg, args[1:])
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(ctx, cl, http.MethodPost,
				projPath(cfg, pid, "modules", id, "module-issues"), map[string]any{"issues": ids})
		},
	}

	removeIssue := &cobra.Command{
		Use:   "remove-issue <module> <issue>",
		Short: "Remove a work item from a module",
		Long: `Remove a work item from a module (the work item itself is kept).

Examples:
  plane module remove-issue "Auth Overhaul" DEPLOY-42 -p DEPLOY`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(ctx, cl, "module", projPath(cfg, pid, "modules"), args[0])
			if err != nil {
				return a.fail(err)
			}
			iid, _, err := a.resolveIssue(ctx, cl, cfg, args[1])
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(ctx, cl, http.MethodDelete,
				projPath(cfg, pid, "modules", id, "module-issues", iid), nil)
		},
	}

	var liFlags listFlags
	listIssues := &cobra.Command{
		Use:   "list-issues <module>",
		Short: "List work items in a module",
		Long: `List the work items in a module (full work item objects).

Examples:
  plane module list-issues "Auth Overhaul" -p DEPLOY`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(cmd.Context(), cl, "module", projPath(cfg, pid, "modules"), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runList(cmd.Context(), cl, projPath(cfg, pid, "modules", id, "module-issues"), liFlags.query(), &liFlags)
		},
	}
	addListFlags(listIssues, &liFlags)

	archive := &cobra.Command{
		Use:   "archive <module>",
		Short: "Archive a completed or cancelled module",
		Long: `Archive a module. Only modules with status completed or
cancelled can be archived (HTTP 400 otherwise).

Examples:
  plane module archive Hardening -p DEPLOY`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(cmd.Context(), cl, "module", projPath(cfg, pid, "modules"), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(cmd.Context(), cl, http.MethodPost, projPath(cfg, pid, "modules", id, "archive"), nil)
		},
	}

	var laFlags listFlags
	listArchived := &cobra.Command{
		Use:   "list-archived",
		Short: "List archived modules",
		Long: `List archived modules in the project.

Examples:
  plane module list-archived -p DEPLOY`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			return a.runList(cmd.Context(), cl, projPath(cfg, pid, "archived-modules"), laFlags.query(), &laFlags)
		},
	}
	addListFlags(listArchived, &laFlags)

	unarchive := &cobra.Command{
		Use:   "unarchive <module-id>",
		Short: "Unarchive a module",
		Long: `Unarchive an archived module. Accepts a UUID or a name findable
via "plane module list-archived".

Examples:
  plane module unarchive Hardening -p DEPLOY`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			ref := args[0]
			if !isUUID(ref) {
				id, err := a.resolveNamed(cmd.Context(), cl, "module", projPath(cfg, pid, "archived-modules"), ref)
				if err != nil {
					return a.fail(err)
				}
				ref = id
			}
			return a.runMutate(cmd.Context(), cl, http.MethodDelete,
				projPath(cfg, pid, "archived-modules", ref, "unarchive"), nil)
		},
	}

	module.AddCommand(list, get, create, update, del, addIssues, removeIssue, listIssues, archive, listArchived, unarchive)
	return module
}
