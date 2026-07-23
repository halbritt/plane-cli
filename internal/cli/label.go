package cli

import (
	"net/http"

	"github.com/spf13/cobra"
)

func newLabelCmd(a *App) *cobra.Command {
	label := &cobra.Command{
		Use:   "label",
		Short: "Manage project labels (list/get/create/update/delete)",
	}

	var lf listFlags
	list := &cobra.Command{
		Use:   "list",
		Short: "List labels in the project",
		Long: `List labels of the configured project.

Examples:
  plane label list -p DEPLOY
  plane label list -p DEPLOY --fields id,name,color`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			return a.runList(cmd.Context(), cl, projPath(cfg, pid, "labels"), lf.query(), &lf)
		},
	}
	addListFlags(list, &lf)

	var gf listFlags
	get := &cobra.Command{
		Use:   "get <label>",
		Short: "Get one label by name or UUID",
		Long: `Get a single label by case-insensitive name or UUID.

Examples:
  plane label get bug -p DEPLOY`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(cmd.Context(), cl, "label", projPath(cfg, pid, "labels"), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runGet(cmd.Context(), cl, projPath(cfg, pid, "labels", id), gf.query())
		},
	}
	addGetFlags(get, &gf)

	var cName, cColor, cDescription, cData string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a label",
		Long: `Create a label. --name is required; --color is optional
(hex string, no server default).

A duplicate name fails with HTTP 409 (exit 5) and the existing label's id
in error.details.

Examples:
  plane label create -p DEPLOY --name bug --color "#DC2626"
  plane label create -p DEPLOY --name backend`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cName == "" {
				return a.usageErr("--name is required")
			}
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			fields := map[string]any{"name": cName}
			if cColor != "" {
				fields["color"] = cColor
			}
			if cDescription != "" {
				fields["description"] = cDescription
			}
			body, err := payload(cData, fields)
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(cmd.Context(), cl, http.MethodPost, projPath(cfg, pid, "labels"), body)
		},
	}
	create.Flags().StringVar(&cName, "name", "", "label name (required)")
	create.Flags().StringVar(&cColor, "color", "", "hex color, e.g. #DC2626")
	create.Flags().StringVar(&cDescription, "description", "", "description")
	create.Flags().StringVar(&cData, "data", "", "additional fields as a JSON object")

	var uData string
	update := &cobra.Command{
		Use:   "update <label>",
		Short: "Update a label (PATCH)",
		Long: `Update a label by name or UUID.

Examples:
  plane label update bug -p DEPLOY --color "#B91C1C"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(cmd.Context(), cl, "label", projPath(cfg, pid, "labels"), args[0])
			if err != nil {
				return a.fail(err)
			}
			fields := map[string]any{}
			changedString(cmd, fields, "name", "name")
			changedString(cmd, fields, "color", "color")
			changedString(cmd, fields, "description", "description")
			body, err := payload(uData, fields)
			if err != nil {
				return a.fail(err)
			}
			if len(body) == 0 {
				return a.usageErr("nothing to update: pass field flags or --data")
			}
			return a.runMutate(cmd.Context(), cl, http.MethodPatch, projPath(cfg, pid, "labels", id), body)
		},
	}
	update.Flags().String("name", "", "new name")
	update.Flags().String("color", "", "new color")
	update.Flags().String("description", "", "new description")
	update.Flags().StringVar(&uData, "data", "", "fields to change as a JSON object")

	del := &cobra.Command{
		Use:   "delete <label>",
		Short: "Delete a label",
		Long: `Delete a label by name or UUID (unconditional; the label is
removed from all work items).

Examples:
  plane label delete obsolete-tag -p DEPLOY`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, err := a.projectScope(cmd)
			if err != nil {
				return a.fail(err)
			}
			id, err := a.resolveNamed(cmd.Context(), cl, "label", projPath(cfg, pid, "labels"), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(cmd.Context(), cl, http.MethodDelete, projPath(cfg, pid, "labels", id), nil)
		},
	}

	label.AddCommand(list, get, create, update, del)
	return label
}
