package cli

import (
	"net/http"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

var cycleViews = []string{"all", "current", "upcoming", "completed", "draft", "incomplete"}

func newCycleCmd(a *App) *cobra.Command {
	cycle := &cobra.Command{
		Use:   "cycle",
		Short: "Manage cycles and their work items",
	}

	var lf listFlags
	var view string
	list := &cobra.Command{
		Use:   "list",
		Short: "List cycles in the project",
		Long: `List cycles. --view filters server-side: all (default), current,
upcoming, completed, draft, incomplete. Note: --view current returns a
plain array from the API (no pagination).

Examples:
  plane cycle list -p DEPLOY
  plane cycle list -p DEPLOY --view current`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if view != "" && !slices.Contains(cycleViews, view) {
				return a.usageErr("--view must be one of %s", strings.Join(cycleViews, ", "))
			}
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			q := lf.query()
			if view != "" {
				q.Set("cycle_view", view)
			}
			if view == "current" {
				// The API returns a bare array for the current view.
				return a.runGet(cmd.Context(), cl, projPath(cfg, pid, "cycles"), q)
			}
			return a.runList(cmd.Context(), cl, projPath(cfg, pid, "cycles"), q, &lf)
		},
	}
	addListFlags(list, &lf)
	list.Flags().StringVar(&view, "view", "", "all|current|upcoming|completed|draft|incomplete")

	var gf listFlags
	get := &cobra.Command{
		Use:   "get <cycle>",
		Short: "Get one cycle by name or UUID",
		Long: `Get a single (non-archived) cycle by case-insensitive name or UUID.

Examples:
  plane cycle get "Sprint 12" -p DEPLOY`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(cmd.Context(), cl, "cycle", projPath(cfg, pid, "cycles"), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runGet(cmd.Context(), cl, projPath(cfg, pid, "cycles", id), gf.query())
		},
	}
	addGetFlags(get, &gf)

	var cName, cDescription, cStart, cEnd, cData string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a cycle",
		Long: `Create a cycle. --name is required. Dates must be passed both
or neither (HTTP 400 otherwise); a cycle without dates is a draft.
The key's user becomes the owner unless --data sets owned_by.

Examples:
  plane cycle create -p DEPLOY --name "Sprint 13" \
    --start-date 2026-08-01 --end-date 2026-08-14
  plane cycle create -p DEPLOY --name Backlog-Grooming`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cName == "" {
				return a.usageErr("--name is required")
			}
			if (cStart == "") != (cEnd == "") {
				return a.usageErr("--start-date and --end-date must be passed together (or neither)")
			}
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			// v1.3.1 quirk: CycleCreateSerializer.validate reads project_id
			// from the request body, not the URL.
			fields := map[string]any{"name": cName, "project_id": pid}
			if cDescription != "" {
				fields["description"] = cDescription
			}
			if cStart != "" {
				fields["start_date"] = cStart
				fields["end_date"] = cEnd
			}
			body, err := payload(cData, fields)
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(cmd.Context(), cl, http.MethodPost, projPath(cfg, pid, "cycles"), body)
		},
	}
	create.Flags().StringVar(&cName, "name", "", "cycle name (required)")
	create.Flags().StringVar(&cDescription, "description", "", "description")
	create.Flags().StringVar(&cStart, "start-date", "", "start date/time (YYYY-MM-DD)")
	create.Flags().StringVar(&cEnd, "end-date", "", "end date/time (YYYY-MM-DD)")
	create.Flags().StringVar(&cData, "data", "", "additional fields as a JSON object")

	var uData string
	update := &cobra.Command{
		Use:   "update <cycle>",
		Short: "Update a cycle (PATCH)",
		Long: `Update a cycle by name or UUID. Archived cycles cannot be
edited; completed cycles only accept sort_order changes.

Examples:
  plane cycle update "Sprint 13" -p DEPLOY --name "Sprint 13b"
  plane cycle update "Sprint 13" -p DEPLOY --data '{"end_date":"2026-08-21"}'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(cmd.Context(), cl, "cycle", projPath(cfg, pid, "cycles"), args[0])
			if err != nil {
				return a.fail(err)
			}
			fields := map[string]any{}
			changedString(cmd, fields, "name", "name")
			changedString(cmd, fields, "description", "description")
			changedString(cmd, fields, "start-date", "start_date")
			changedString(cmd, fields, "end-date", "end_date")
			body, err := payload(uData, fields)
			if err != nil {
				return a.fail(err)
			}
			if len(body) == 0 {
				return a.usageErr("nothing to update: pass field flags or --data")
			}
			return a.runMutate(cmd.Context(), cl, http.MethodPatch, projPath(cfg, pid, "cycles", id), body)
		},
	}
	update.Flags().String("name", "", "new name")
	update.Flags().String("description", "", "new description")
	update.Flags().String("start-date", "", "new start date")
	update.Flags().String("end-date", "", "new end date")
	update.Flags().StringVar(&uData, "data", "", "fields to change as a JSON object")

	del := &cobra.Command{
		Use:   "delete <cycle>",
		Short: "Delete a cycle",
		Long: `Delete a cycle by name or UUID. Only the cycle owner or a
project admin may delete (HTTP 403 otherwise). Work items in the cycle are
not deleted, only unlinked.

Examples:
  plane cycle delete "Sprint 13" -p DEPLOY`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(cmd.Context(), cl, "cycle", projPath(cfg, pid, "cycles"), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(cmd.Context(), cl, http.MethodDelete, projPath(cfg, pid, "cycles", id), nil)
		},
	}

	addIssues := &cobra.Command{
		Use:   "add-issues <cycle> <issue>...",
		Short: "Add work items to a cycle",
		Long: `Add one or more work items (UUID or PROJ-123) to a cycle. A work
item already in another cycle is MOVED to this one. Completed cycles refuse
additions (HTTP 400). The response is the full set of cycle-issue link
objects for the cycle.

Examples:
  plane cycle add-issues "Sprint 13" DEPLOY-42 DEPLOY-43 -p DEPLOY`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(ctx, cl, "cycle", projPath(cfg, pid, "cycles"), args[0])
			if err != nil {
				return a.fail(err)
			}
			ids, err := a.resolveIssueList(ctx, cl, cfg, args[1:])
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(ctx, cl, http.MethodPost,
				projPath(cfg, pid, "cycles", id, "cycle-issues"), map[string]any{"issues": ids})
		},
	}

	removeIssue := &cobra.Command{
		Use:   "remove-issue <cycle> <issue>",
		Short: "Remove a work item from a cycle",
		Long: `Remove a work item from a cycle (the work item itself is kept).

Examples:
  plane cycle remove-issue "Sprint 13" DEPLOY-42 -p DEPLOY`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(ctx, cl, "cycle", projPath(cfg, pid, "cycles"), args[0])
			if err != nil {
				return a.fail(err)
			}
			iid, _, err := a.resolveIssue(ctx, cl, cfg, args[1])
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(ctx, cl, http.MethodDelete,
				projPath(cfg, pid, "cycles", id, "cycle-issues", iid), nil)
		},
	}

	var liFlags listFlags
	listIssues := &cobra.Command{
		Use:   "list-issues <cycle>",
		Short: "List work items in a cycle",
		Long: `List the work items in a cycle (full work item objects with a
bridge_id field naming the cycle-issue link).

Examples:
  plane cycle list-issues "Sprint 13" -p DEPLOY --fields id,name,state`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(cmd.Context(), cl, "cycle", projPath(cfg, pid, "cycles"), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runList(cmd.Context(), cl, projPath(cfg, pid, "cycles", id, "cycle-issues"), liFlags.query(), &liFlags)
		},
	}
	addListFlags(listIssues, &liFlags)

	transfer := &cobra.Command{
		Use:   "transfer-issues <from-cycle> <to-cycle>",
		Short: "Move incomplete work items to another cycle",
		Long: `Transfer the incomplete work items (backlog/unstarted/started)
of an ENDED cycle to another cycle, recording a progress snapshot on the
source. The source must be past its end date; the target must not be
completed (HTTP 400 otherwise).

Examples:
  plane cycle transfer-issues "Sprint 12" "Sprint 13" -p DEPLOY`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			from, err := a.resolveNamed(ctx, cl, "cycle", projPath(cfg, pid, "cycles"), args[0])
			if err != nil {
				return a.fail(err)
			}
			to, err := a.resolveNamed(ctx, cl, "cycle", projPath(cfg, pid, "cycles"), args[1])
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(ctx, cl, http.MethodPost,
				projPath(cfg, pid, "cycles", from, "transfer-issues"), map[string]any{"new_cycle_id": to})
		},
	}

	archive := &cobra.Command{
		Use:   "archive <cycle>",
		Short: "Archive a completed cycle",
		Long: `Archive a cycle. Only cycles past their end date can be
archived (HTTP 400 otherwise).

Examples:
  plane cycle archive "Sprint 12" -p DEPLOY`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(cmd.Context(), cl, "cycle", projPath(cfg, pid, "cycles"), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(cmd.Context(), cl, http.MethodPost, projPath(cfg, pid, "cycles", id, "archive"), nil)
		},
	}

	var laFlags listFlags
	listArchived := &cobra.Command{
		Use:   "list-archived",
		Short: "List archived cycles",
		Long: `List archived cycles in the project.

Examples:
  plane cycle list-archived -p DEPLOY`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			return a.runList(cmd.Context(), cl, projPath(cfg, pid, "archived-cycles"), laFlags.query(), &laFlags)
		},
	}
	addListFlags(listArchived, &laFlags)

	unarchive := &cobra.Command{
		Use:   "unarchive <cycle-id>",
		Short: "Unarchive a cycle",
		Long: `Unarchive an archived cycle. Pass the cycle UUID (archived
cycles cannot be resolved by name from the active list; find the id with
"plane cycle list-archived").

Examples:
  plane cycle unarchive 4f6a...b3 -p DEPLOY`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			ref := args[0]
			if !isUUID(ref) {
				id, err := a.resolveNamed(cmd.Context(), cl, "cycle", projPath(cfg, pid, "archived-cycles"), ref)
				if err != nil {
					return a.fail(err)
				}
				ref = id
			}
			return a.runMutate(cmd.Context(), cl, http.MethodDelete,
				projPath(cfg, pid, "archived-cycles", ref, "unarchive"), nil)
		},
	}

	cycle.AddCommand(list, get, create, update, del, addIssues, removeIssue, listIssues, transfer, archive, listArchived, unarchive)
	return cycle
}
