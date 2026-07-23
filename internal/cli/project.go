package cli

import (
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

func newProjectCmd(a *App) *cobra.Command {
	proj := &cobra.Command{
		Use:   "project",
		Short: "Manage projects (list/get/create/update/delete/archive/summary)",
	}

	var lf listFlags
	var orderBy string
	list := &cobra.Command{
		Use:   "list",
		Short: "List projects in the workspace",
		Long: `List all projects in the workspace. Pagination is drained
automatically; use --limit to cap results and meta.pagination.next_cursor
with --cursor to resume.

Examples:
  plane project list
  plane project list --limit 10
  plane project list --order-by created_at --fields id,name,identifier`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, err := a.Client(true)
			if err != nil {
				return a.fail(err)
			}
			q := lf.query()
			if orderBy != "" {
				q.Set("order_by", orderBy)
			}
			return a.runList(cmd.Context(), cl, wsPath(cfg, "projects"), q, &lf)
		},
	}
	addListFlags(list, &lf)
	list.Flags().StringVar(&orderBy, "order-by", "", "server-side ordering (default sort_order; prefix - for descending)")

	var gf listFlags
	get := &cobra.Command{
		Use:   "get <project>",
		Short: "Get one project by UUID, identifier, or name",
		Long: `Get a single project. Accepts a UUID, a project identifier
(e.g. PROJ), or a project name (case-insensitive).

Examples:
  plane project get PROJ
  plane project get "My Project" --expand default_state
  plane project get 3f2c0f7d-...-a1b2`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, err := a.Client(true)
			if err != nil {
				return a.fail(err)
			}
			cfg.Project = args[0]
			id, err := a.resolveProject(cmd.Context(), cl, cfg)
			if err != nil {
				return a.fail(err)
			}
			return a.runGet(cmd.Context(), cl, wsPath(cfg, "projects", id), gf.query())
		},
	}
	addGetFlags(get, &gf)

	var createData, name, identifier, description string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a project",
		Long: `Create a project. --name and --identifier are required
(identifier: max 12 chars, uppercased server-side, no special characters).
Extra serializer fields can be passed as a JSON object via --data
(explicit flags win on conflict).

The server auto-creates the default states (Backlog/Todo/In Progress/
Done/Cancelled) and adds the key's user as project admin.

Duplicate name or identifier fails with HTTP 409 (exit 5).

Examples:
  plane project create --name "Deploy Tracker" --identifier DEPLOY
  plane project create --name X --identifier X --data '{"cycle_view":false}'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" || identifier == "" {
				return a.usageErr("--name and --identifier are required")
			}
			cl, cfg, err := a.Client(true)
			if err != nil {
				return a.fail(err)
			}
			fields := map[string]any{"name": name, "identifier": identifier}
			if description != "" {
				fields["description"] = description
			}
			body, err := payload(createData, fields)
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(cmd.Context(), cl, http.MethodPost, wsPath(cfg, "projects"), body)
		},
	}
	create.Flags().StringVar(&name, "name", "", "project name (required)")
	create.Flags().StringVar(&identifier, "identifier", "", "project identifier, e.g. DEPLOY (required)")
	create.Flags().StringVar(&description, "description", "", "project description")
	create.Flags().StringVar(&createData, "data", "", "additional fields as a JSON object")

	var updData string
	update := &cobra.Command{
		Use:   "update <project>",
		Short: "Update a project (PATCH)",
		Long: `Update fields on a project. Pass changes via flags and/or a
JSON object with --data. Archived projects cannot be updated (HTTP 400).

Examples:
  plane project update DEPLOY --data '{"description":"new text"}'
  plane project update "Deploy Tracker" --name "Deploy Tracker 2"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, err := a.Client(true)
			if err != nil {
				return a.fail(err)
			}
			cfg.Project = args[0]
			id, err := a.resolveProject(cmd.Context(), cl, cfg)
			if err != nil {
				return a.fail(err)
			}
			fields := map[string]any{}
			changedString(cmd, fields, "name", "name")
			changedString(cmd, fields, "description", "description")
			body, err := payload(updData, fields)
			if err != nil {
				return a.fail(err)
			}
			if len(body) == 0 {
				return a.usageErr("nothing to update: pass --name/--description or --data")
			}
			return a.runMutate(cmd.Context(), cl, http.MethodPatch, wsPath(cfg, "projects", id), body)
		},
	}
	update.Flags().String("name", "", "new project name")
	update.Flags().String("description", "", "new description")
	update.Flags().StringVar(&updData, "data", "", "fields to change as a JSON object")

	del := &cobra.Command{
		Use:   "delete <project>",
		Short: "Delete a project (requires project admin)",
		Long: `Delete a project and everything in it. Only project admins (or
workspace admins who are members) may delete; others get HTTP 403 (exit 3).
This is irreversible — the API has no confirmation step.

Examples:
  plane project delete SCRATCH`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, err := a.Client(true)
			if err != nil {
				return a.fail(err)
			}
			cfg.Project = args[0]
			id, err := a.resolveProject(cmd.Context(), cl, cfg)
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(cmd.Context(), cl, http.MethodDelete, wsPath(cfg, "projects", id), nil)
		},
	}

	archive := &cobra.Command{
		Use:   "archive <project>",
		Short: "Archive a project",
		Long: `Archive a project (it stops appearing in default lists and
becomes read-only until unarchived).

Examples:
  plane project archive OLDPROJ`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.projectArchiveOp(cmd, args[0], http.MethodPost)
		},
	}
	unarchive := &cobra.Command{
		Use:   "unarchive <project>",
		Short: "Unarchive a project",
		Long: `Reverse a project archive.

Examples:
  plane project unarchive OLDPROJ`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.projectArchiveOp(cmd, args[0], http.MethodDelete)
		},
	}

	var sumFields string
	summary := &cobra.Command{
		Use:   "summary <project>",
		Short: "Entity counts for a project",
		Long: `Return {id, name, identifier, counts:{...}} for a project.
Counts cover members, states, labels, cycles, modules, issues, intakes,
pages; restrict with --count-fields.

Examples:
  plane project summary DEPLOY
  plane project summary DEPLOY --count-fields issues,cycles`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, err := a.Client(true)
			if err != nil {
				return a.fail(err)
			}
			cfg.Project = args[0]
			id, err := a.resolveProject(cmd.Context(), cl, cfg)
			if err != nil {
				return a.fail(err)
			}
			q := url.Values{}
			if sumFields != "" {
				q.Set("fields", sumFields)
			}
			return a.runGet(cmd.Context(), cl, projPath(cfg, id, "summary"), q)
		},
	}
	summary.Flags().StringVar(&sumFields, "count-fields", "", "comma-separated subset: members,states,labels,cycles,modules,issues,intakes,pages")

	proj.AddCommand(list, get, create, update, del, archive, unarchive, summary)
	return proj
}

func (a *App) projectArchiveOp(cmd *cobra.Command, ref, method string) error {
	cl, cfg, err := a.Client(true)
	if err != nil {
		return a.fail(err)
	}
	cfg.Project = ref
	id, err := a.resolveProject(cmd.Context(), cl, cfg)
	if err != nil {
		return a.fail(err)
	}
	return a.runMutate(cmd.Context(), cl, method, projPath(cfg, id, "archive"), nil)
}
