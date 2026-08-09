package paseo

import (
	"context"
	"errors"
	"strings"
)

// ErrorKind classifies a Paseo failure for retry and operator handling.
type ErrorKind string

// Paseo error classifications.
const (
	ErrorNetwork             ErrorKind = "network"
	ErrorAuth                ErrorKind = "auth"
	ErrorInvalidRequest      ErrorKind = "invalid_request"
	ErrorUnsupportedVersion  ErrorKind = "unsupported_version"
	ErrorProviderUnavailable ErrorKind = "provider_unavailable"
	ErrorWorkspace           ErrorKind = "workspace_error"
)

// Error is a redacted, classified Paseo command failure.
type Error struct {
	Kind    ErrorKind
	Message string
	Err     error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Err }

// Client is the version-pinned, safety-checked Paseo CLI boundary.
type Client struct {
	host    string
	version string
	runner  runner
}

// NewClient verifies the configured host and refuses unsupported CLI versions.
func NewClient(ctx context.Context, host string, cli CLIRunner) (*Client, error) {
	return newClient(ctx, host, cli)
}

func newClient(ctx context.Context, host string, run runner) (*Client, error) {
	if err := validateHost(host); err != nil {
		return nil, &Error{Kind: ErrorInvalidRequest, Message: err.Error(), Err: err}
	}
	result, err := run.Run(ctx, []string{"--version"})
	if err != nil {
		return nil, commandError(err, result, host)
	}
	version, err := parseVersion(string(result.stdout))
	if err != nil {
		return nil, &Error{Kind: ErrorUnsupportedVersion, Message: err.Error(), Err: err}
	}
	if err := checkVersion(version); err != nil {
		return nil, err
	}
	return &Client{host: host, version: version, runner: run}, nil
}

// Version returns the verified CLI version used by this client.
func (c *Client) Version() string { return c.version }

// CreateWorkspace creates one Paseo-owned worktree workspace.
func (c *Client) CreateWorkspace(ctx context.Context, req WorkspaceCreateRequest) (Workspace, error) {
	args, err := workspaceCreateArgs(c.host, req)
	return runJSON[Workspace](ctx, c, args, err)
}

// Run launches a detached agent in an explicit existing workspace.
func (c *Client) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	args, err := runArgs(c.host, req)
	return runJSON[RunResult](ctx, c, args, err)
}

// ListAgents lists global agents, including archived agents, optionally by label.
func (c *Client) ListAgents(ctx context.Context, label string) ([]Agent, error) {
	args, err := listAgentsArgs(c.host, label)
	return runJSON[[]Agent](ctx, c, args, err)
}

// ListProviders lists the host's providers with availability and mode labels.
func (c *Client) ListProviders(ctx context.Context) ([]Provider, error) {
	args, err := providerListArgs(c.host)
	return runJSON[[]Provider](ctx, c, args, err)
}

// ProviderModels lists one provider's launchable models with their thinking
// options.
func (c *Client) ProviderModels(ctx context.Context, provider string) ([]ProviderModel, error) {
	args, err := providerModelsArgs(c.host, provider)
	return runJSON[[]ProviderModel](ctx, c, args, err)
}

// CreateLocalWorkspace creates a non-worktree workspace rooted at an explicit
// host-side path, used as the container for maintenance terminals.
func (c *Client) CreateLocalWorkspace(ctx context.Context, path, title string) (Workspace, error) {
	args, err := workspaceCreateLocalArgs(c.host, path, title)
	return runJSON[Workspace](ctx, c, args, err)
}

// ArchiveWorkspace archives one explicitly identified workspace.
func (c *Client) ArchiveWorkspace(ctx context.Context, workspaceID string) error {
	args, err := workspaceArchiveArgs(c.host, workspaceID)
	return c.runNoOutput(ctx, args, err)
}

// ListSchedules lists the host's recurring schedules. Heartbeats have no
// listing in the pinned CLI, so this is deliberately not a statement about
// them.
func (c *Client) ListSchedules(ctx context.Context) ([]Schedule, error) {
	args, err := scheduleListArgs(c.host)
	return runJSON[[]Schedule](ctx, c, args, err)
}

// DeleteSchedule deletes one explicitly identified schedule.
func (c *Client) DeleteSchedule(ctx context.Context, scheduleID string) (ScheduleDeleteResult, error) {
	args, err := scheduleDeleteArgs(c.host, scheduleID)
	return runJSON[ScheduleDeleteResult](ctx, c, args, err)
}

// Inspect returns strict, reconciliation-grade facts for one agent.
func (c *Client) Inspect(ctx context.Context, agentID string) (AgentDetail, error) {
	args, err := inspectArgs(c.host, agentID)
	return runJSON[AgentDetail](ctx, c, args, err)
}

// Stop requests that one explicitly identified agent stop.
func (c *Client) Stop(ctx context.Context, agentID string) error {
	args, err := destructiveArgs(c.host, "stop", agentID)
	return c.runNoOutput(ctx, args, err)
}

// Delete deletes one explicitly identified agent.
func (c *Client) Delete(ctx context.Context, agentID string) error {
	args, err := destructiveArgs(c.host, "delete", agentID)
	return c.runNoOutput(ctx, args, err)
}

// CaptureTerminal reads a bounded range from a terminal's monotonic line cursor.
func (c *Client) CaptureTerminal(ctx context.Context, terminalID string, start, end int) (TerminalCapture, error) {
	args, err := terminalCaptureArgs(c.host, terminalID, start, end)
	return runJSON[TerminalCapture](ctx, c, args, err)
}

func runJSON[T any](ctx context.Context, client *Client, args []string, buildErr error) (T, error) {
	var zero T
	if buildErr != nil {
		return zero, &Error{Kind: ErrorInvalidRequest, Message: buildErr.Error(), Err: buildErr}
	}
	result, err := client.runner.Run(ctx, args)
	if err != nil {
		return zero, commandError(err, result, client.host)
	}
	value, err := decodeStrict[T](result.stdout)
	if err != nil {
		return zero, &Error{Kind: ErrorInvalidRequest, Message: redact(err.Error(), client.host), Err: err}
	}
	return value, nil
}

func (c *Client) runNoOutput(ctx context.Context, args []string, buildErr error) error {
	if buildErr != nil {
		return &Error{Kind: ErrorInvalidRequest, Message: buildErr.Error(), Err: buildErr}
	}
	result, err := c.runner.Run(ctx, args)
	if err != nil {
		return commandError(err, result, c.host)
	}
	return nil
}

func commandError(cause error, result commandResult, secrets ...string) error {
	detail := strings.TrimSpace(string(result.stderr))
	if detail == "" {
		detail = strings.TrimSpace(string(result.stdout))
	}
	kind := classifyError(cause, detail)
	message := "Paseo command failed"
	if detail != "" {
		message += ": " + detail
	}
	return &Error{Kind: kind, Message: redact(message, secrets...), Err: cause}
}

func classifyError(cause error, detail string) ErrorKind {
	lower := strings.ToLower(detail)
	switch {
	case errors.Is(cause, context.DeadlineExceeded), strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "econnrefused"), strings.Contains(lower, "timed out"),
		strings.Contains(lower, "unable to connect"), strings.Contains(lower, "fetch failed"),
		strings.Contains(lower, "network"):
		return ErrorNetwork
	case strings.Contains(lower, "unauthorized"), strings.Contains(lower, "authentication"),
		strings.Contains(lower, "password"), strings.Contains(lower, "401"), strings.Contains(lower, "403"):
		return ErrorAuth
	case strings.Contains(lower, "provider") && (strings.Contains(lower, "unavailable") ||
		strings.Contains(lower, "disabled") || strings.Contains(lower, "not configured")):
		return ErrorProviderUnavailable
	case strings.Contains(lower, "workspace"), strings.Contains(lower, "worktree"):
		return ErrorWorkspace
	default:
		return ErrorInvalidRequest
	}
}

// IsKind reports whether err is a classified Paseo error of kind.
func IsKind(err error, kind ErrorKind) bool {
	var paseoErr *Error
	return errors.As(err, &paseoErr) && paseoErr.Kind == kind
}
