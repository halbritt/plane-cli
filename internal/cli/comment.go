package cli

import (
	"fmt"
	"html"
	"net/http"

	"github.com/spf13/cobra"
)

func newCommentCmd(a *App) *cobra.Command {
	comment := &cobra.Command{
		Use:   "comment",
		Short: "Manage work item comments (list/get/add/update/delete)",
	}

	var lf listFlags
	list := &cobra.Command{
		Use:   "list <issue>",
		Short: "List comments on a work item",
		Long: `List comments on a work item (UUID or PROJ-123).

Examples:
  plane comment list DEPLOY-42
  plane comment list DEPLOY-42 --fields id,comment_html,created_by`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, iid, err := a.issueScope(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runList(cmd.Context(), cl, projPath(cfg, pid, "work-items", iid, "comments"), lf.query(), &lf)
		},
	}
	addListFlags(list, &lf)

	var gf listFlags
	get := &cobra.Command{
		Use:   "get <issue> <comment-id>",
		Short: "Get one comment",
		Long: `Get a single comment by UUID.

Examples:
  plane comment get DEPLOY-42 7b0c...e1`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, iid, err := a.issueScope(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runGet(cmd.Context(), cl, projPath(cfg, pid, "work-items", iid, "comments", args[1]), gf.query())
		},
	}
	addGetFlags(get, &gf)

	var addText, addHTML, addAccess, addData string
	add := &cobra.Command{
		Use:   "add <issue>",
		Short: "Add a comment to a work item",
		Long: `Add a comment. Use --text for plain text (HTML-escaped and
wrapped in <p>…</p>) or --html for raw HTML; --access EXTERNAL makes the
comment visible on public boards (default INTERNAL).

Examples:
  plane comment add DEPLOY-42 --text "Deployed to staging"
  plane comment add DEPLOY-42 --html "<p>See <b>logs</b></p>"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (addText == "") == (addHTML == "") {
				return a.usageErr("exactly one of --text or --html is required")
			}
			cl, cfg, pid, iid, err := a.issueScope(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			fields := map[string]any{"comment_html": commentHTML(addText, addHTML)}
			if addAccess != "" {
				if addAccess != "INTERNAL" && addAccess != "EXTERNAL" {
					return a.usageErr("--access must be INTERNAL or EXTERNAL")
				}
				fields["access"] = addAccess
			}
			body, err := payload(addData, fields)
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(cmd.Context(), cl, http.MethodPost, projPath(cfg, pid, "work-items", iid, "comments"), body)
		},
	}
	add.Flags().StringVar(&addText, "text", "", "comment as plain text")
	add.Flags().StringVar(&addHTML, "html", "", "comment as raw HTML")
	add.Flags().StringVar(&addAccess, "access", "", "INTERNAL (default) or EXTERNAL")
	add.Flags().StringVar(&addData, "data", "", "additional fields as a JSON object")

	var updText, updHTML, updData string
	update := &cobra.Command{
		Use:   "update <issue> <comment-id>",
		Short: "Update a comment (PATCH)",
		Long: `Update a comment's HTML by UUID.

Examples:
  plane comment update DEPLOY-42 7b0c...e1 --text "Corrected note"`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if updText != "" && updHTML != "" {
				return a.usageErr("pass only one of --text or --html")
			}
			cl, cfg, pid, iid, err := a.issueScope(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			fields := map[string]any{}
			if updText != "" || updHTML != "" {
				fields["comment_html"] = commentHTML(updText, updHTML)
			}
			body, err := payload(updData, fields)
			if err != nil {
				return a.fail(err)
			}
			if len(body) == 0 {
				return a.usageErr("nothing to update: pass --text, --html, or --data")
			}
			return a.runMutate(cmd.Context(), cl, http.MethodPatch, projPath(cfg, pid, "work-items", iid, "comments", args[1]), body)
		},
	}
	update.Flags().StringVar(&updText, "text", "", "new comment as plain text")
	update.Flags().StringVar(&updHTML, "html", "", "new comment as raw HTML")
	update.Flags().StringVar(&updData, "data", "", "fields to change as a JSON object")

	del := &cobra.Command{
		Use:   "delete <issue> <comment-id>",
		Short: "Delete a comment",
		Long: `Delete a comment by UUID.

Examples:
  plane comment delete DEPLOY-42 7b0c...e1`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, iid, err := a.issueScope(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(cmd.Context(), cl, http.MethodDelete, projPath(cfg, pid, "work-items", iid, "comments", args[1]), nil)
		},
	}

	comment.AddCommand(list, get, add, update, del)
	return comment
}

func commentHTML(text, rawHTML string) string {
	if rawHTML != "" {
		return rawHTML
	}
	return fmt.Sprintf("<p>%s</p>", html.EscapeString(text))
}
