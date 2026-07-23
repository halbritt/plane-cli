// Package cli implements the plane command tree. Commands emit exactly one
// JSON envelope on stdout and exit via the code table in internal/out.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"plane-cli/internal/client"
	"plane-cli/internal/config"
	"plane-cli/internal/out"
)

// App carries process-level dependencies and global flag state through the
// command tree. Stdout receives only JSON envelopes.
type App struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	Getenv func(string) string

	flagBaseURL     string
	flagWorkspace   string
	flagProject     string
	flagAPIKeyFile  string
	flagConfig      string
	flagDebug       bool
	flagNoResolve   bool
	flagRetryUnsafe bool
	flagTimeout     time.Duration
	flagMaxRetries  int

	cfg      *config.Config
	cl       *client.Client
	resolved out.Meta // name→ID resolutions performed for this invocation
}

// exitError signals a finished command whose envelope was already written.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// Config resolves configuration once per invocation.
func (a *App) Config() (*config.Config, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	cfg, err := config.Load(config.Flags{
		BaseURL:    a.flagBaseURL,
		Workspace:  a.flagWorkspace,
		Project:    a.flagProject,
		APIKeyFile: a.flagAPIKeyFile,
		ConfigPath: a.flagConfig,
	}, a.Getenv)
	if err != nil {
		return nil, err
	}
	a.cfg = cfg
	return cfg, nil
}

// Client validates config and returns the API client.
func (a *App) Client(needWorkspace bool) (*client.Client, *config.Config, error) {
	cfg, err := a.Config()
	if err != nil {
		return nil, nil, err
	}
	if err := cfg.Validate(needWorkspace); err != nil {
		return nil, nil, err
	}
	if a.cl == nil {
		cl := client.New(cfg.BaseURL, cfg.APIKey())
		cl.MaxRetries = a.flagMaxRetries
		cl.RetryUnsafe = a.flagRetryUnsafe
		cl.Debug = a.flagDebug
		cl.Stderr = a.Stderr
		cl.HTTP.Timeout = a.flagTimeout
		a.cl = cl
	}
	return a.cl, cfg, nil
}

// success emits the success envelope (folding in any name resolutions) and
// ends the command.
func (a *App) success(data any, meta out.Meta) error {
	if len(a.resolved) > 0 {
		if meta == nil {
			meta = out.Meta{}
		}
		meta["resolved"] = a.resolved
	}
	return &exitError{code: out.Success(a.Stdout, data, meta)}
}

// fail classifies err, emits the failure envelope, and ends the command.
func (a *App) fail(err error) error {
	return &exitError{code: out.Failure(a.Stdout, classify(err))}
}

// usageErr is for command-level argument validation (exit 2).
func (a *App) usageErr(format string, args ...any) error {
	return a.fail(&usageError{msg: fmt.Sprintf(format, args...)})
}

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func classify(err error) out.ErrObj {
	var (
		cfgErr  *config.ConfigError
		useErr  *usageError
		rlErr   *client.RateLimitedError
		apiErr  *client.APIError
		netErr  *client.NetError
		resErr  *resolveError
		exitOld *exitError
	)
	switch {
	case errors.As(err, &exitOld):
		// Defensive: should not happen; keep the original code path silent.
		return out.ErrObj{Code: out.CodeServer, Message: err.Error()}
	case errors.As(err, &useErr):
		return out.ErrObj{Code: out.CodeUsage, Message: useErr.msg}
	case errors.As(err, &cfgErr):
		return out.ErrObj{Code: out.CodeConfig, Message: cfgErr.Msg}
	case errors.As(err, &resErr):
		return out.ErrObj{Code: resErr.code, Message: resErr.msg, Details: resErr.details}
	case errors.As(err, &rlErr):
		return out.ErrObj{
			Code: out.CodeRateLimited, Message: "rate limited and retries exhausted: " + rlErr.Message(),
			HTTPStatus: rlErr.Status, Details: rawOrNil(rlErr.Details()),
		}
	case errors.As(err, &apiErr):
		return out.ErrObj{
			Code: out.CodeForHTTPStatus(apiErr.Status), Message: apiErr.Message(),
			HTTPStatus: apiErr.Status, Details: rawOrNil(apiErr.Details()),
		}
	case errors.As(err, &netErr):
		return out.ErrObj{Code: out.CodeNetwork, Message: netErr.Error()}
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return out.ErrObj{Code: out.CodeNetwork, Message: err.Error()}
	default:
		return out.ErrObj{Code: out.CodeServer, Message: err.Error()}
	}
}

func rawOrNil(r json.RawMessage) any {
	if len(r) == 0 {
		return nil
	}
	return r
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool { return uuidRe.MatchString(s) }

// noteResolved records a name→ID resolution for the envelope meta.
func (a *App) noteResolved(kind, input, id string) {
	if a.resolved == nil {
		a.resolved = out.Meta{}
	}
	a.resolved[kind] = map[string]string{"input": input, "id": id}
}
