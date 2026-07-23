package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"plane-cli/internal/client"
	"plane-cli/internal/config"
	"plane-cli/internal/out"
)

// resolveError carries not-found/ambiguous resolution failures with the
// candidate list for the envelope's error.details.
type resolveError struct {
	code    string
	msg     string
	details any
}

func (e *resolveError) Error() string { return e.msg }

type candidate struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Identifier string `json:"identifier,omitempty"`
}

// resolveProject turns cfg.Project (or an explicit ref) into a project UUID,
// matching identifier or name case-insensitively.
func (a *App) resolveProject(ctx context.Context, cl *client.Client, cfg *config.Config) (string, error) {
	ref := cfg.Project
	if ref == "" {
		return "", &config.ConfigError{Msg: "project required (set --project/-p, PLANE_PROJECT, or project in config.toml)"}
	}
	if isUUID(ref) {
		return ref, nil
	}
	if a.flagNoResolve {
		return "", &usageError{msg: fmt.Sprintf("--no-resolve is set but project %q is not a UUID", ref)}
	}

	path := fmt.Sprintf("/api/v1/workspaces/%s/projects/", url.PathEscape(cfg.Workspace))
	results, _, err := cl.ListAll(ctx, path, nil, 0, "")
	if err != nil {
		return "", err
	}
	var matches []candidate
	for _, raw := range results {
		var p struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Identifier string `json:"identifier"`
		}
		if json.Unmarshal(raw, &p) != nil {
			continue
		}
		if strings.EqualFold(p.Identifier, ref) || strings.EqualFold(p.Name, ref) {
			matches = append(matches, candidate{ID: p.ID, Name: p.Name, Identifier: p.Identifier})
		}
	}
	switch len(matches) {
	case 1:
		a.noteResolved("project", ref, matches[0].ID)
		return matches[0].ID, nil
	case 0:
		return "", &resolveError{code: out.CodeNotFound,
			msg: fmt.Sprintf("no project matches %q (by identifier or name) in workspace %q", ref, cfg.Workspace)}
	default:
		return "", &resolveError{code: out.CodeAmbiguous,
			msg:     fmt.Sprintf("project %q is ambiguous (%d matches)", ref, len(matches)),
			details: map[string]any{"candidates": matches}}
	}
}

// resolveNamed resolves a name to an ID within a project-scoped list
// endpoint (states, labels, cycles, modules), matching the "name" field
// case-insensitively.
func (a *App) resolveNamed(ctx context.Context, cl *client.Client, kind, listPath, ref string) (string, error) {
	if isUUID(ref) {
		return ref, nil
	}
	if a.flagNoResolve {
		return "", &usageError{msg: fmt.Sprintf("--no-resolve is set but %s %q is not a UUID", kind, ref)}
	}
	results, _, err := cl.ListAll(ctx, listPath, nil, 0, "")
	if err != nil {
		return "", err
	}
	var matches []candidate
	for _, raw := range results {
		var v struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &v) != nil {
			continue
		}
		if strings.EqualFold(v.Name, ref) {
			matches = append(matches, candidate{ID: v.ID, Name: v.Name})
		}
	}
	switch len(matches) {
	case 1:
		a.noteResolved(kind, ref, matches[0].ID)
		return matches[0].ID, nil
	case 0:
		return "", &resolveError{code: out.CodeNotFound,
			msg: fmt.Sprintf("no %s named %q in project", kind, ref)}
	default:
		return "", &resolveError{code: out.CodeAmbiguous,
			msg:     fmt.Sprintf("%s %q is ambiguous (%d matches)", kind, ref, len(matches)),
			details: map[string]any{"candidates": matches}}
	}
}

// resolveIssue accepts an issue UUID or a PROJ-123 identifier; the latter is
// resolved via the workspace by-identifier endpoint. projectID is non-empty
// only for identifier refs (the issue's own project), letting callers skip
// --project.
func (a *App) resolveIssue(ctx context.Context, cl *client.Client, cfg *config.Config, ref string) (issueID, projectID string, err error) {
	if isUUID(ref) {
		return ref, "", nil
	}
	if a.flagNoResolve {
		return "", "", &usageError{msg: fmt.Sprintf("--no-resolve is set but issue %q is not a UUID", ref)}
	}
	if !strings.Contains(ref, "-") {
		return "", "", &usageError{msg: fmt.Sprintf("issue %q is neither a UUID nor a PROJ-123 identifier", ref)}
	}
	path := fmt.Sprintf("/api/v1/workspaces/%s/work-items/%s/",
		url.PathEscape(cfg.Workspace), url.PathEscape(strings.ToUpper(ref)))
	resp, err := cl.Do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return "", "", &resolveError{code: out.CodeNotFound,
				msg: fmt.Sprintf("no work item %q in workspace %q", ref, cfg.Workspace)}
		}
		return "", "", err
	}
	var v struct {
		ID      string `json:"id"`
		Project string `json:"project"`
	}
	if err := json.Unmarshal(resp.Body, &v); err != nil || v.ID == "" {
		return "", "", fmt.Errorf("unexpected by-identifier response for %q", ref)
	}
	a.noteResolved("issue", ref, v.ID)
	return v.ID, v.Project, nil
}

// resolveIssueList resolves a mix of UUIDs and PROJ-123 refs to issue IDs.
func (a *App) resolveIssueList(ctx context.Context, cl *client.Client, cfg *config.Config, refs []string) ([]string, error) {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		id, _, err := a.resolveIssue(ctx, cl, cfg, ref)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}
