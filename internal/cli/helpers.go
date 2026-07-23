package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"plane-cli/internal/client"
	"plane-cli/internal/config"
	"plane-cli/internal/out"
)

// wsPath builds /api/v1/workspaces/<slug>/<parts...> with path escaping.
func wsPath(cfg *config.Config, parts ...string) string {
	b := strings.Builder{}
	b.WriteString("/api/v1/workspaces/")
	b.WriteString(url.PathEscape(cfg.Workspace))
	for _, p := range parts {
		b.WriteString("/")
		b.WriteString(url.PathEscape(p))
	}
	b.WriteString("/")
	return b.String()
}

// projPath builds /api/v1/workspaces/<slug>/projects/<pid>/<parts...>.
func projPath(cfg *config.Config, projectID string, parts ...string) string {
	return wsPath(cfg, append([]string{"projects", projectID}, parts...)...)
}

// listFlags are the standard pagination/list flags.
type listFlags struct {
	limit  int
	cursor string
	fields string
	expand string
}

func addListFlags(cmd *cobra.Command, lf *listFlags) {
	cmd.Flags().IntVar(&lf.limit, "limit", 0, "cap the number of results (0 = drain all pages)")
	cmd.Flags().StringVar(&lf.cursor, "cursor", "", "resume from a previous next_cursor (format per_page:page:is_prev)")
	addGetFlags(cmd, lf)
}

func addGetFlags(cmd *cobra.Command, lf *listFlags) {
	cmd.Flags().StringVar(&lf.fields, "fields", "", "comma-separated fields to return (server-side sparse fieldset)")
	cmd.Flags().StringVar(&lf.expand, "expand", "", "comma-separated relations to expand (e.g. state,assignees)")
}

func (lf *listFlags) query() url.Values {
	q := url.Values{}
	if lf.fields != "" {
		q.Set("fields", lf.fields)
	}
	if lf.expand != "" {
		q.Set("expand", lf.expand)
	}
	return q
}

// runList drains a paginated endpoint and emits the standard envelope.
func (a *App) runList(ctx context.Context, cl *client.Client, path string, q url.Values, lf *listFlags) error {
	results, meta, err := cl.ListAll(ctx, path, q, lf.limit, lf.cursor)
	if err != nil {
		return a.fail(err)
	}
	return a.success(results, paginationMeta(meta))
}

func paginationMeta(m client.ListMeta) out.Meta {
	pg := map[string]any{
		"total_count": m.TotalCount,
		"fetched":     m.Fetched,
		"pages":       m.Pages,
		"drained":     m.Drained,
	}
	if m.NextCursor != "" {
		pg["next_cursor"] = m.NextCursor
	}
	return out.Meta{"pagination": pg}
}

// runGet fetches a single resource and passes the body through.
func (a *App) runGet(ctx context.Context, cl *client.Client, path string, q url.Values) error {
	resp, err := cl.Do(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return a.fail(err)
	}
	return a.success(resp.JSON(), nil)
}

// runMutate performs POST/PATCH/DELETE and passes the response through.
func (a *App) runMutate(ctx context.Context, cl *client.Client, method, path string, body any) error {
	resp, err := cl.Do(ctx, method, path, nil, body)
	if err != nil {
		return a.fail(err)
	}
	return a.success(resp.JSON(), out.Meta{"http_status": resp.Status})
}

// payload builds a request body from a raw --data JSON object merged with
// explicit flag-derived fields (flags win on conflict).
func payload(dataJSON string, fields map[string]any) (map[string]any, error) {
	m := map[string]any{}
	if dataJSON != "" {
		if err := json.Unmarshal([]byte(dataJSON), &m); err != nil {
			return nil, &usageError{msg: fmt.Sprintf("--data is not a JSON object: %v", err)}
		}
	}
	for k, v := range fields {
		m[k] = v
	}
	return m, nil
}

// changedString adds flag values to fields only when the flag was set,
// so PATCH bodies contain exactly what the caller asked to change.
func changedString(cmd *cobra.Command, fields map[string]any, flag, key string) {
	if cmd.Flags().Changed(flag) {
		v, _ := cmd.Flags().GetString(flag)
		fields[key] = v
	}
}

// bulkItem is one line's outcome for stdin bulk operations.
type bulkItem struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *out.ErrObj     `json:"error,omitempty"`
}

// runBulk reads newline-delimited JSON objects from r and applies fn to each.
// It emits a combined envelope: ok=true when every item succeeded, else a
// failure envelope with per-item results in error.details.results.
func (a *App) runBulk(r io.Reader, fn func(line map[string]any) (json.RawMessage, error)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	var items []bulkItem
	failed := 0
	lineNo := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		lineNo++
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			e := classify(&usageError{msg: fmt.Sprintf("stdin line %d: invalid JSON: %v", lineNo, err)})
			items = append(items, bulkItem{OK: false, Error: &e})
			failed++
			continue
		}
		data, err := fn(obj)
		if err != nil {
			e := classify(err)
			items = append(items, bulkItem{OK: false, Error: &e})
			failed++
			continue
		}
		items = append(items, bulkItem{OK: true, Data: data})
	}
	if err := sc.Err(); err != nil {
		return a.fail(fmt.Errorf("reading stdin: %w", err))
	}
	meta := out.Meta{"total": len(items), "succeeded": len(items) - failed, "failed": failed}
	if failed == 0 {
		return a.success(map[string]any{"results": items}, meta)
	}
	code := out.CodeValidation
	if failed == len(items) && len(items) > 0 {
		// Uniform failure: surface the first item's code (e.g. all auth).
		code = items[0].Error.Code
	}
	return &exitError{code: out.Failure(a.Stdout, out.ErrObj{
		Code:    code,
		Message: fmt.Sprintf("%d of %d items failed", failed, len(items)),
		Details: map[string]any{"results": items, "meta": meta},
	})}
}

// projectScope resolves client + configured project for project-scoped
// commands.
func (a *App) projectScope(cmd *cobra.Command) (cl *client.Client, cfg *config.Config, pid string, err error) {
	cl, cfg, err = a.Client(true)
	if err != nil {
		return
	}
	pid, err = a.resolveProject(cmd.Context(), cl, cfg)
	return
}

// issueScope resolves the common project + issue-ref scoping used by
// sub-resource commands (comments, links, attachments). A PROJ-123 ref
// carries its own project, so --project is only needed for UUID refs.
func (a *App) issueScope(ctx context.Context, issueRef string) (cl *client.Client, cfg *config.Config, projectID, issueID string, err error) {
	cl, cfg, err = a.Client(true)
	if err != nil {
		return
	}
	issueID, projectID, err = a.resolveIssue(ctx, cl, cfg, issueRef)
	if err != nil {
		return
	}
	if projectID == "" {
		projectID, err = a.resolveProject(ctx, cl, cfg)
	}
	return
}
