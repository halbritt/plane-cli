package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"plane-cli/internal/out"
)

const rootLong = `plane drives a self-hosted Plane instance through its public REST API
(/api/v1, X-API-Key auth). Built for non-interactive use by agents and
systemd units: stdout carries exactly one JSON envelope per invocation,
diagnostics go to stderr, and the binary never prompts.

Output envelope:
  success: {"ok":true,"data":...,"meta":{...}}
  failure: {"ok":false,"error":{"code":...,"message":...,"http_status":...,"details":...}}

Exit codes:
  0  success
  2  usage or configuration error
  3  authentication/authorization failed (HTTP 401/403)
  4  not found (HTTP 404)
  5  validation error (other HTTP 4xx, ambiguous name resolution)
  6  server or network error (HTTP 5xx, timeouts)
  7  rate limited and retries exhausted (HTTP 429)

Configuration (precedence: flags > environment > config file):
  PLANE_BASE_URL      base URL, e.g. https://plane.example.com
  PLANE_API_KEY       API key (or PLANE_API_KEY_FILE / --api-key-file; the
                      file form is systemd LoadCredential-friendly and wins
                      over PLANE_API_KEY when both are set)
  PLANE_WORKSPACE     workspace slug
  PLANE_PROJECT       default project (name, identifier, or UUID)
  PLANE_CONFIG        config file path (default ~/.config/plane-cli/config.toml)

The API key is never accepted as a command-line argument.

Name resolution: anywhere a UUID is expected you may pass a human name
(project name/identifier, state/label/cycle/module name, or PROJ-123 issue
identifiers). Ambiguous names fail with the candidate list in error.details.
Disable with --no-resolve to require UUIDs.`

// NewRoot builds the command tree. All cobra help/usage output is routed to
// stderr so stdout stays pure JSON.
func NewRoot(a *App) *cobra.Command {
	root := &cobra.Command{
		Use:           "plane",
		Short:         "JSON-first CLI for the Plane public REST API",
		Long:          rootLong,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(a.Stderr)
	root.SetErr(a.Stderr)
	root.CompletionOptions.HiddenDefaultCmd = true

	pf := root.PersistentFlags()
	pf.StringVar(&a.flagBaseURL, "base-url", "", "Plane base URL (env PLANE_BASE_URL)")
	pf.StringVarP(&a.flagWorkspace, "workspace", "w", "", "workspace slug (env PLANE_WORKSPACE)")
	pf.StringVarP(&a.flagProject, "project", "p", "", "project name, identifier, or UUID (env PLANE_PROJECT)")
	pf.StringVar(&a.flagAPIKeyFile, "api-key-file", "", "file containing the API key (env PLANE_API_KEY_FILE)")
	pf.StringVar(&a.flagConfig, "config", "", "config file path (default ~/.config/plane-cli/config.toml)")
	pf.BoolVar(&a.flagDebug, "debug", false, "trace requests/responses to stderr (API key redacted)")
	pf.BoolVar(&a.flagNoResolve, "no-resolve", false, "disable name→ID resolution; require UUIDs")
	pf.BoolVar(&a.flagRetryUnsafe, "retry-unsafe", false, "also retry non-idempotent requests on 5xx/network errors")
	pf.DurationVar(&a.flagTimeout, "timeout", 60*time.Second, "per-request HTTP timeout")
	pf.IntVar(&a.flagMaxRetries, "max-retries", 4, "retry attempts after the first try (429/5xx/network)")

	root.AddCommand(
		newVersionCmd(a),
		newMeCmd(a),
		newWorkspaceCmd(a),
		newProjectCmd(a),
		newIssueCmd(a),
		newCommentCmd(a),
		newLinkCmd(a),
		newAttachmentCmd(a),
		newStateCmd(a),
		newLabelCmd(a),
		newCycleCmd(a),
		newModuleCmd(a),
	)
	return root
}

// Main runs the CLI and returns the process exit code.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	a := &App{Stdout: stdout, Stderr: stderr, Stdin: os.Stdin, Getenv: getenv}
	root := NewRoot(a)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return out.ExitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	// Cobra-level usage errors (unknown command/flag, bad arg count):
	// cobra already printed usage to stderr; emit the machine-readable
	// envelope on stdout.
	return out.Failure(stdout, out.ErrObj{Code: out.CodeUsage, Message: err.Error()})
}
