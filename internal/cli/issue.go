package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"plane-cli/internal/client"
	"plane-cli/internal/config"
	"plane-cli/internal/out"
)

var priorities = []string{"urgent", "high", "medium", "low", "none"}

func newIssueCmd(a *App) *cobra.Command {
	issue := &cobra.Command{
		Use:     "issue",
		Aliases: []string{"work-item"},
		Short:   "Manage work items (list/get/create/update/delete/search)",
	}
	issue.AddCommand(
		newIssueListCmd(a),
		newIssueGetCmd(a),
		newIssueCreateCmd(a),
		newIssueUpdateCmd(a),
		newIssueDeleteCmd(a),
		newIssueSearchCmd(a),
	)
	return issue
}

func newIssueListCmd(a *App) *cobra.Command {
	var lf listFlags
	var orderBy, fPriority, fState, fLabel, fAssignee string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List work items in a project",
		Long: `List work items in the configured project (-p / PLANE_PROJECT).
Pagination is drained automatically; --limit caps results.

The public API has no server-side field filters, so --priority/--state/
--label/--assignee filter client-side after fetching (state/label names are
resolved to IDs first). meta.filtered reports before/after counts.

Filters see complete records whatever --fields says: fields a filter reads
(priority, state, labels, assignees) are requested from the server when
--fields omits them, then dropped from the output, so --fields never changes
which issues match.

Examples:
  plane issue list -p DEPLOY
  plane issue list -p DEPLOY --priority high --state "In Progress"
  plane issue list -p DEPLOY --order-by -created_at --limit 20
  plane issue list -p DEPLOY --label bug --fields id,name,priority`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cl, cfg, err := a.Client(true)
			if err != nil {
				return a.fail(err)
			}
			pid, err := a.resolveProject(ctx, cl, cfg)
			if err != nil {
				return a.fail(err)
			}
			q := lf.query()
			if orderBy != "" {
				q.Set("order_by", orderBy)
			}
			// --fields is a server-side sparse fieldset. Ask for the fields the
			// active filters read, and drop them from the output afterwards.
			var filterOnly []string
			if lf.fields != "" {
				needed := filterFields(fPriority, fState, fLabel, fAssignee)
				filterOnly = missingFields(lf.fields, needed)
				if len(needed) > 0 {
					// Plane does not trim names in a sparse fieldset.
					q.Set("fields", strings.Join(append(splitFields(lf.fields), filterOnly...), ","))
				}
			}
			results, meta, err := cl.ListAll(ctx, projPath(cfg, pid, "work-items"), q, lf.limit, lf.cursor)
			if err != nil {
				return a.fail(err)
			}

			mOut := paginationMeta(meta)
			if fPriority != "" || fState != "" || fLabel != "" || fAssignee != "" {
				var stateID, labelID string
				if fState != "" {
					if stateID, err = a.resolveNamed(ctx, cl, "state", projPath(cfg, pid, "states"), fState); err != nil {
						return a.fail(err)
					}
				}
				if fLabel != "" {
					if labelID, err = a.resolveNamed(ctx, cl, "label", projPath(cfg, pid, "labels"), fLabel); err != nil {
						return a.fail(err)
					}
				}
				if fPriority != "" && !slices.Contains(priorities, fPriority) {
					return a.usageErr("--priority must be one of %s", strings.Join(priorities, ", "))
				}
				before := len(results)
				if results, err = filterIssues(results, fPriority, stateID, labelID, fAssignee); err != nil {
					return a.fail(err)
				}
				mOut["filtered"] = map[string]any{"before": before, "after": len(results), "client_side": true}
				if len(filterOnly) > 0 {
					if results, err = projectFields(results, splitFields(lf.fields)); err != nil {
						return a.fail(err)
					}
				}
			}
			return a.success(results, mOut)
		},
	}
	addListFlags(cmd, &lf)
	cmd.Flags().StringVar(&orderBy, "order-by", "", "server-side ordering (e.g. -created_at, priority, state__name)")
	cmd.Flags().StringVar(&fPriority, "priority", "", "client-side filter: urgent|high|medium|low|none")
	cmd.Flags().StringVar(&fState, "state", "", "client-side filter: state name or UUID")
	cmd.Flags().StringVar(&fLabel, "label", "", "client-side filter: label name or UUID")
	cmd.Flags().StringVar(&fAssignee, "assignee", "", "client-side filter: assignee UUID")
	return cmd
}

// filterFields lists the issue fields the active client-side filters read.
func filterFields(priority, state, label, assignee string) []string {
	var fields []string
	for _, f := range []struct{ active, field string }{{priority, "priority"}, {state, "state"}, {label, "labels"}, {assignee, "assignees"}} {
		if f.active != "" {
			fields = append(fields, f.field)
		}
	}
	return fields
}

// splitFields parses a comma-separated --fields value.
func splitFields(fields string) []string {
	var names []string
	for _, name := range strings.Split(fields, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// missingFields returns the needed fields that the requested --fields omits.
func missingFields(requested string, needed []string) []string {
	have := splitFields(requested)
	var missing []string
	for _, field := range needed {
		if !slices.Contains(have, field) {
			missing = append(missing, field)
		}
	}
	return missing
}

// refID is the ID of a relation the server returned either as a bare UUID or,
// when expanded, as an object with an id.
func refID(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"':
		var id string
		if json.Unmarshal(raw, &id) == nil {
			return id
		}
	case '{':
		var object struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &object) == nil {
			return object.ID
		}
	}
	return ""
}

func refIDs(raw json.RawMessage) ([]string, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		if string(bytes.TrimSpace(raw)) == "null" {
			return nil, nil
		}
		return nil, err
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, refID(item))
	}
	return ids, nil
}

// filterIssues keeps the issues that match every active filter. A missing field
// required by a filter is an error rather than a silent non-match.
func filterIssues(results []json.RawMessage, priority, stateID, labelID, assigneeID string) ([]json.RawMessage, error) {
	var kept []json.RawMessage
	for _, raw := range results {
		var record map[string]json.RawMessage
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, fmt.Errorf("cannot apply the filter: an issue record is not a JSON object")
		}
		field := func(flag, name string) (json.RawMessage, error) {
			value, ok := record[name]
			if !ok {
				return nil, fmt.Errorf("cannot apply --%s: the server returned an issue without its %q field", flag, name)
			}
			return value, nil
		}
		if priority != "" {
			value, err := field("priority", "priority")
			if err != nil {
				return nil, err
			}
			var have string
			if json.Unmarshal(value, &have) != nil || have != priority {
				continue
			}
		}
		if stateID != "" {
			value, err := field("state", "state")
			if err != nil {
				return nil, err
			}
			if refID(value) != stateID {
				continue
			}
		}
		if labelID != "" {
			value, err := field("label", "labels")
			if err != nil {
				return nil, err
			}
			ids, err := refIDs(value)
			if err != nil {
				return nil, fmt.Errorf("cannot apply --label: unexpected labels value: %w", err)
			}
			if !slices.Contains(ids, labelID) {
				continue
			}
		}
		if assigneeID != "" {
			value, err := field("assignee", "assignees")
			if err != nil {
				return nil, err
			}
			ids, err := refIDs(value)
			if err != nil {
				return nil, fmt.Errorf("cannot apply --assignee: unexpected assignees value: %w", err)
			}
			if !slices.Contains(ids, assigneeID) {
				continue
			}
		}
		kept = append(kept, raw)
	}
	return kept, nil
}

// projectFields drops every top-level key not in keep, preserving the server's
// key order and each value's exact bytes.
func projectFields(records []json.RawMessage, keep []string) ([]json.RawMessage, error) {
	if len(records) == 0 {
		return records, nil
	}
	projected := make([]json.RawMessage, 0, len(records))
	for _, raw := range records {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
			return nil, fmt.Errorf("cannot project fields: an issue record is not a JSON object")
		}
		var buf bytes.Buffer
		buf.WriteByte('{')
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return nil, fmt.Errorf("cannot project fields: %w", err)
			}
			key, _ := token.(string)
			var value json.RawMessage
			if err := decoder.Decode(&value); err != nil {
				return nil, fmt.Errorf("cannot project fields: %w", err)
			}
			if !slices.Contains(keep, key) {
				continue
			}
			if buf.Len() > 1 {
				buf.WriteByte(',')
			}
			name, _ := json.Marshal(key)
			buf.Write(name)
			buf.WriteByte(':')
			buf.Write(value)
		}
		buf.WriteByte('}')
		projected = append(projected, json.RawMessage(buf.Bytes()))
	}
	return projected, nil
}

func newIssueGetCmd(a *App) *cobra.Command {
	var gf listFlags
	var extID, extSource string
	cmd := &cobra.Command{
		Use:   "get [<issue>]",
		Short: "Get one work item by UUID or PROJ-123 identifier",
		Long: `Get a single work item. Accepts a UUID (project context needed)
or a PROJ-123 identifier (self-contained). Alternatively look up by
--external-id + --external-source within the configured project.

Examples:
  plane issue get DEPLOY-42
  plane issue get 6a1a...d2 -p DEPLOY --expand state,assignees
  plane issue get --external-source jira --external-id ABC-1 -p DEPLOY`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cl, cfg, err := a.Client(true)
			if err != nil {
				return a.fail(err)
			}
			if extID != "" || extSource != "" {
				if extID == "" || extSource == "" {
					return a.usageErr("--external-id and --external-source must be used together")
				}
				if len(args) != 0 {
					return a.usageErr("pass either an issue reference or --external-id/--external-source, not both")
				}
				pid, err := a.resolveProject(ctx, cl, cfg)
				if err != nil {
					return a.fail(err)
				}
				q := gf.query()
				q.Set("external_id", extID)
				q.Set("external_source", extSource)
				return a.runGet(ctx, cl, projPath(cfg, pid, "work-items"), q)
			}
			if len(args) == 0 {
				return a.usageErr("an issue reference (UUID or PROJ-123) is required")
			}
			ref := args[0]
			if !isUUID(ref) {
				// By-identifier endpoint returns the issue directly.
				path := wsPath(cfg, "work-items", strings.ToUpper(ref))
				return a.runGet(ctx, cl, path, gf.query())
			}
			pid, err := a.resolveProject(ctx, cl, cfg)
			if err != nil {
				return a.fail(err)
			}
			return a.runGet(ctx, cl, projPath(cfg, pid, "work-items", ref), gf.query())
		},
	}
	addGetFlags(cmd, &gf)
	cmd.Flags().StringVar(&extID, "external-id", "", "look up by external id (with --external-source)")
	cmd.Flags().StringVar(&extSource, "external-source", "", "external source system name")
	return cmd
}

// issueFieldFlags collects the shared create/update field flags.
type issueFieldFlags struct {
	name, descriptionHTML, priority, state, parent string
	labels, assignees                              []string
	startDate, targetDate, data                    string
}

func addIssueFieldFlags(cmd *cobra.Command, f *issueFieldFlags) {
	cmd.Flags().StringVar(&f.name, "name", "", "work item title")
	cmd.Flags().StringVar(&f.descriptionHTML, "description-html", "", "description as HTML (e.g. \"<p>text</p>\")")
	cmd.Flags().StringVar(&f.priority, "priority", "", "urgent|high|medium|low|none")
	cmd.Flags().StringVar(&f.state, "state", "", "state name or UUID")
	cmd.Flags().StringVar(&f.parent, "parent", "", "parent work item (UUID or PROJ-123)")
	cmd.Flags().StringArrayVar(&f.labels, "label", nil, "label name or UUID (repeatable)")
	cmd.Flags().StringArrayVar(&f.assignees, "assignee", nil, "assignee user UUID (repeatable)")
	cmd.Flags().StringVar(&f.startDate, "start-date", "", "YYYY-MM-DD")
	cmd.Flags().StringVar(&f.targetDate, "target-date", "", "YYYY-MM-DD")
	cmd.Flags().StringVar(&f.data, "data", "", "additional serializer fields as a JSON object")
}

// buildIssueBody resolves names in the field flags and merges them over --data.
func (a *App) buildIssueBody(ctx context.Context, cmd *cobra.Command, cl *client.Client, cfg *config.Config, pid string, f *issueFieldFlags) (map[string]any, error) {
	fields := map[string]any{}
	if f.name != "" {
		fields["name"] = f.name
	}
	changedString(cmd, fields, "description-html", "description_html")
	if f.priority != "" {
		if !slices.Contains(priorities, f.priority) {
			return nil, &usageError{msg: "--priority must be one of " + strings.Join(priorities, ", ")}
		}
		fields["priority"] = f.priority
	}
	if f.state != "" {
		id, err := a.resolveNamed(ctx, cl, "state", projPath(cfg, pid, "states"), f.state)
		if err != nil {
			return nil, err
		}
		fields["state"] = id
	}
	if f.parent != "" {
		id, _, err := a.resolveIssue(ctx, cl, cfg, f.parent)
		if err != nil {
			return nil, err
		}
		fields["parent"] = id
	}
	if len(f.labels) > 0 {
		ids := make([]string, 0, len(f.labels))
		for _, l := range f.labels {
			id, err := a.resolveNamed(ctx, cl, "label", projPath(cfg, pid, "labels"), l)
			if err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		fields["labels"] = ids
	}
	if len(f.assignees) > 0 {
		fields["assignees"] = f.assignees
	}
	if f.startDate != "" {
		fields["start_date"] = f.startDate
	}
	if f.targetDate != "" {
		fields["target_date"] = f.targetDate
	}
	return payload(f.data, fields)
}

func newIssueCreateCmd(a *App) *cobra.Command {
	var f issueFieldFlags
	cmd := &cobra.Command{
		Use:   "create [-]",
		Short: "Create work item(s)",
		Long: `Create a work item in the configured project. --name is required
(the only required field). State/label names are resolved to IDs.

Bulk mode: pass "-" to read newline-delimited JSON objects from stdin, each
a full request body (bodies are sent as-is; names are not resolved). The
combined result reports per-line outcomes; any failure exits nonzero.

Duplicate --data external_id+external_source pairs fail with HTTP 409
(exit 5) and the existing issue's id in error.details.

Examples:
  plane issue create -p DEPLOY --name "Ship v2" --priority high --state Todo
  plane issue create -p DEPLOY --name Bug --label bug --label backend
  printf '%s\n' '{"name":"a"}' '{"name":"b"}' | plane issue create -p DEPLOY -`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cl, cfg, err := a.Client(true)
			if err != nil {
				return a.fail(err)
			}
			pid, err := a.resolveProject(ctx, cl, cfg)
			if err != nil {
				return a.fail(err)
			}
			path := projPath(cfg, pid, "work-items")

			if len(args) == 1 {
				if args[0] != "-" {
					return a.usageErr("the only positional argument accepted is \"-\" (bulk stdin mode)")
				}
				return a.runBulk(a.Stdin, func(line map[string]any) (json.RawMessage, error) {
					resp, err := cl.Do(ctx, http.MethodPost, path, nil, line)
					if err != nil {
						return nil, err
					}
					return resp.JSON(), nil
				})
			}

			if f.name == "" && f.data == "" {
				return a.usageErr("--name is required (or provide --data)")
			}
			body, err := a.buildIssueBody(ctx, cmd, cl, cfg, pid, &f)
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(ctx, cl, http.MethodPost, path, body)
		},
	}
	addIssueFieldFlags(cmd, &f)
	return cmd
}

func newIssueUpdateCmd(a *App) *cobra.Command {
	var f issueFieldFlags
	cmd := &cobra.Command{
		Use:   "update <issue>|-",
		Short: "Update work item(s) (PATCH)",
		Long: `Update a work item by UUID or PROJ-123 identifier. Only fields
you pass are changed; --label/--assignee REPLACE the full set.

Bulk mode: "plane issue update -" reads newline-delimited JSON objects from
stdin; each needs an "issue" key (UUID or PROJ-123) plus the fields to
change (sent as-is, no name resolution).

Examples:
  plane issue update DEPLOY-42 --state Done
  plane issue update DEPLOY-42 --priority low --target-date 2026-08-01
  printf '%s\n' '{"issue":"DEPLOY-42","priority":"low"}' | plane issue update -`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cl, cfg, err := a.Client(true)
			if err != nil {
				return a.fail(err)
			}

			if args[0] == "-" {
				return a.runBulk(a.Stdin, func(line map[string]any) (json.RawMessage, error) {
					ref, _ := line["issue"].(string)
					if ref == "" {
						return nil, &usageError{msg: `each line needs an "issue" key (UUID or PROJ-123)`}
					}
					delete(line, "issue")
					id, pidFromRef, err := a.resolveIssue(ctx, cl, cfg, ref)
					if err != nil {
						return nil, err
					}
					pid := pidFromRef
					if pid == "" {
						if pid, err = a.resolveProject(ctx, cl, cfg); err != nil {
							return nil, err
						}
					}
					resp, err := cl.Do(ctx, http.MethodPatch, projPath(cfg, pid, "work-items", id), nil, line)
					if err != nil {
						return nil, err
					}
					return resp.JSON(), nil
				})
			}

			id, pid, err := a.resolveIssue(ctx, cl, cfg, args[0])
			if err != nil {
				return a.fail(err)
			}
			if pid == "" {
				if pid, err = a.resolveProject(ctx, cl, cfg); err != nil {
					return a.fail(err)
				}
			}
			body, err := a.buildIssueBody(ctx, cmd, cl, cfg, pid, &f)
			if err != nil {
				return a.fail(err)
			}
			if len(body) == 0 {
				return a.usageErr("nothing to update: pass field flags or --data")
			}
			return a.runMutate(ctx, cl, http.MethodPatch, projPath(cfg, pid, "work-items", id), body)
		},
	}
	addIssueFieldFlags(cmd, &f)
	return cmd
}

func newIssueDeleteCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <issue>...",
		Short: "Delete work item(s)",
		Long: `Delete one or more work items (UUID or PROJ-123). Only the
creator or a project admin may delete (else HTTP 403, exit 3). With multiple
references, per-item outcomes are reported and any failure exits nonzero.

Examples:
  plane issue delete DEPLOY-42
  plane issue delete DEPLOY-42 DEPLOY-43 DEPLOY-44`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cl, cfg, err := a.Client(true)
			if err != nil {
				return a.fail(err)
			}
			del := func(ref string) (json.RawMessage, error) {
				id, pid, err := a.resolveIssue(ctx, cl, cfg, ref)
				if err != nil {
					return nil, err
				}
				if pid == "" {
					if pid, err = a.resolveProject(ctx, cl, cfg); err != nil {
						return nil, err
					}
				}
				resp, err := cl.Do(ctx, http.MethodDelete, projPath(cfg, pid, "work-items", id), nil, nil)
				if err != nil {
					return nil, err
				}
				return resp.JSON(), nil
			}
			if len(args) == 1 {
				data, err := del(args[0])
				if err != nil {
					return a.fail(err)
				}
				return a.success(data, out.Meta{"deleted": args[0]})
			}
			var items []bulkItem
			failed := 0
			for _, ref := range args {
				data, err := del(ref)
				if err != nil {
					e := classify(err)
					items = append(items, bulkItem{OK: false, Error: &e})
					failed++
					continue
				}
				items = append(items, bulkItem{OK: true, Data: data})
			}
			meta := out.Meta{"total": len(items), "succeeded": len(items) - failed, "failed": failed}
			if failed == 0 {
				return a.success(map[string]any{"results": items}, meta)
			}
			return &exitError{code: out.Failure(a.Stdout, out.ErrObj{
				Code:    out.CodeValidation,
				Message: fmt.Sprintf("%d of %d deletions failed", failed, len(items)),
				Details: map[string]any{"results": items, "meta": meta},
			})}
		},
	}
	return cmd
}

func newIssueSearchCmd(a *App) *cobra.Command {
	var limit int
	var workspaceWide bool
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search work items by name/sequence/project identifier",
		Long: `Search work items. Scoped to the configured project by default;
--workspace-wide searches every project you belong to. Matches on name
substring, sequence number, and project identifier.

Returns {"issues":[{name,id,sequence_id,project__identifier,project_id,
workspace__slug}]} — a lightweight shape, not full work items.

Examples:
  plane issue search "login bug" -p DEPLOY
  plane issue search 42 --workspace-wide --limit 25`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cl, cfg, err := a.Client(true)
			if err != nil {
				return a.fail(err)
			}
			q := url.Values{}
			q.Set("search", args[0])
			q.Set("limit", fmt.Sprint(limit))
			if workspaceWide {
				q.Set("workspace_search", "true")
			} else {
				q.Set("workspace_search", "false")
				pid, err := a.resolveProject(ctx, cl, cfg)
				if err != nil {
					return a.fail(err)
				}
				q.Set("project_id", pid)
			}
			return a.runGet(ctx, cl, wsPath(cfg, "work-items", "search"), q)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 25, "maximum results")
	cmd.Flags().BoolVar(&workspaceWide, "workspace-wide", false, "search across all projects instead of the configured one")
	return cmd
}
