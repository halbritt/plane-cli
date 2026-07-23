package cli

import (
	"net/http"

	"github.com/spf13/cobra"
)

func newLinkCmd(a *App) *cobra.Command {
	link := &cobra.Command{
		Use:   "link",
		Short: "Manage work item links (list/get/add/update/delete)",
	}

	var lf listFlags
	list := &cobra.Command{
		Use:   "list <issue>",
		Short: "List links on a work item",
		Long: `List external links attached to a work item (UUID or PROJ-123).

Examples:
  plane link list DEPLOY-42`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, iid, err := a.issueScope(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runList(cmd.Context(), cl, projPath(cfg, pid, "work-items", iid, "links"), lf.query(), &lf)
		},
	}
	addListFlags(list, &lf)

	var gf listFlags
	get := &cobra.Command{
		Use:   "get <issue> <link-id>",
		Short: "Get one link",
		Long: `Get a single link by UUID.

Examples:
  plane link get DEPLOY-42 9c2d...f0`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, iid, err := a.issueScope(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runGet(cmd.Context(), cl, projPath(cfg, pid, "work-items", iid, "links", args[1]), gf.query())
		},
	}
	addGetFlags(get, &gf)

	var addURL, addTitle string
	add := &cobra.Command{
		Use:   "add <issue>",
		Short: "Attach a URL to a work item",
		Long: `Attach an external http(s) URL. A URL already attached to the
same work item fails with HTTP 400 (exit 5).

Examples:
  plane link add DEPLOY-42 --url https://grafana.local/d/abc --title "Dashboard"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if addURL == "" {
				return a.usageErr("--url is required")
			}
			cl, cfg, pid, iid, err := a.issueScope(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			body := map[string]any{"url": addURL}
			if addTitle != "" {
				body["title"] = addTitle
			}
			return a.runMutate(cmd.Context(), cl, http.MethodPost, projPath(cfg, pid, "work-items", iid, "links"), body)
		},
	}
	add.Flags().StringVar(&addURL, "url", "", "http(s) URL to attach (required)")
	add.Flags().StringVar(&addTitle, "title", "", "display title")

	var updURL, updTitle string
	update := &cobra.Command{
		Use:   "update <issue> <link-id>",
		Short: "Update a link (PATCH)",
		Long: `Update a link's URL or title by UUID.

Examples:
  plane link update DEPLOY-42 9c2d...f0 --title "New title"`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, iid, err := a.issueScope(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			body := map[string]any{}
			if updURL != "" {
				body["url"] = updURL
			}
			if updTitle != "" {
				body["title"] = updTitle
			}
			if len(body) == 0 {
				return a.usageErr("nothing to update: pass --url or --title")
			}
			return a.runMutate(cmd.Context(), cl, http.MethodPatch, projPath(cfg, pid, "work-items", iid, "links", args[1]), body)
		},
	}
	update.Flags().StringVar(&updURL, "url", "", "new URL")
	update.Flags().StringVar(&updTitle, "title", "", "new title")

	del := &cobra.Command{
		Use:   "delete <issue> <link-id>",
		Short: "Delete a link",
		Long: `Delete a link by UUID.

Examples:
  plane link delete DEPLOY-42 9c2d...f0`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, iid, err := a.issueScope(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(cmd.Context(), cl, http.MethodDelete, projPath(cfg, pid, "work-items", iid, "links", args[1]), nil)
		},
	}

	link.AddCommand(list, get, add, update, del)
	return link
}
