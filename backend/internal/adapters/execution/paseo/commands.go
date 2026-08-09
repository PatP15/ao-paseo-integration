package paseo

import (
	"fmt"
	"strconv"
	"strings"
)

// WorkspaceCreateRequest contains the required branch-off workspace arguments.
type WorkspaceCreateRequest struct {
	RepoPath     string
	Branch       string
	BaseBranch   string
	WorktreeSlug string
	Title        string
}

// RunRequest contains the explicit placement and launch arguments for an agent.
type RunRequest struct {
	WorkspaceID string
	Provider    string
	Model       string
	Mode        string
	Thinking    string
	Title       string
	Labels      []string
	Prompt      string
}

func validateHost(host string) error {
	if host == "" || !strings.Contains(host, ":") {
		return fmt.Errorf("invalid Paseo host: must contain a colon")
	}
	return nil
}

func validateLabel(label string) error {
	if strings.Count(label, "=") != 1 {
		return fmt.Errorf("invalid Paseo label: expected exactly one '='")
	}
	key, value, _ := strings.Cut(label, "=")
	if key == "" || value == "" {
		return fmt.Errorf("invalid Paseo label: key and value must be non-empty")
	}
	return nil
}

func rejectAll(args []string, operation string) error {
	if operation != "stop" && operation != "delete" {
		return nil
	}
	for _, arg := range args {
		if arg == "--all" {
			return fmt.Errorf("refusing Paseo %s --all", operation)
		}
	}
	return nil
}

func hostArgs(command []string, host string) ([]string, error) {
	if err := validateHost(host); err != nil {
		return nil, err
	}
	args := append([]string(nil), command...)
	return append(args, "--host", host), nil
}

func workspaceCreateArgs(host string, req WorkspaceCreateRequest) ([]string, error) {
	args, err := hostArgs([]string{"workspace", "create"}, host)
	if err != nil {
		return nil, err
	}
	if req.RepoPath == "" || req.Branch == "" || req.BaseBranch == "" || req.WorktreeSlug == "" || req.Title == "" {
		return nil, fmt.Errorf("invalid workspace create request: all fields are required")
	}
	return append(args,
		"--isolation", "worktree", "--mode", "branch-off",
		"--path", req.RepoPath, "--new-branch", req.Branch, "--base", req.BaseBranch,
		"--worktree-slug", req.WorktreeSlug, "--title", req.Title, "--json",
	), nil
}

func runArgs(host string, req RunRequest) ([]string, error) {
	args, err := hostArgs([]string{"run"}, host)
	if err != nil {
		return nil, err
	}
	if req.WorkspaceID == "" || req.Provider == "" || req.Prompt == "" {
		return nil, fmt.Errorf("invalid run request: workspace, provider, and prompt are required")
	}
	args = append(args, "--workspace", req.WorkspaceID, "--provider", req.Provider, "-d", "--json")
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Mode != "" {
		args = append(args, "--mode", req.Mode)
	}
	if req.Thinking != "" {
		args = append(args, "--thinking", req.Thinking)
	}
	if req.Title != "" {
		args = append(args, "--title", req.Title)
	}
	seen := make(map[string]struct{}, len(req.Labels))
	for _, label := range req.Labels {
		if err := validateLabel(label); err != nil {
			return nil, err
		}
		key, _, _ := strings.Cut(label, "=")
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("invalid Paseo labels: duplicate key %q", key)
		}
		seen[key] = struct{}{}
		args = append(args, "--label", label)
	}
	return append(args, req.Prompt), nil
}

func listAgentsArgs(host, label string) ([]string, error) {
	args, err := hostArgs([]string{"ls"}, host)
	if err != nil {
		return nil, err
	}
	args = append(args, "-a", "-g", "--json")
	if label != "" {
		if err := validateLabel(label); err != nil {
			return nil, err
		}
		args = append(args, "--label", label)
	}
	return args, nil
}

func providerListArgs(host string) ([]string, error) {
	args, err := hostArgs([]string{"provider", "ls"}, host)
	if err != nil {
		return nil, err
	}
	return append(args, "--json"), nil
}

func providerModelsArgs(host, provider string) ([]string, error) {
	// The provider name becomes one argv element; the same argv rules the
	// dispatch path enforces apply here.
	if provider == "" || strings.ContainsAny(provider, " \t\r\n") || strings.HasPrefix(provider, "-") {
		return nil, fmt.Errorf("invalid provider name %q", provider)
	}
	args, err := hostArgs([]string{"provider", "models"}, host)
	if err != nil {
		return nil, err
	}
	return append(args, provider, "--json"), nil
}

func workspaceCreateLocalArgs(host, path, title string) ([]string, error) {
	if strings.TrimSpace(title) == "" {
		return nil, fmt.Errorf("local workspace requires a title")
	}
	// An explicit path is required: with no --path the CLI sends ITS OWN cwd —
	// a path on the AO machine that need not exist on the host. Callers pass
	// the home directory the maintenance channel learned, or "/" (which exists
	// on every POSIX host) before anything has been learned.
	if path == "" || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, " \t\r\n") {
		return nil, fmt.Errorf("invalid local workspace path %q", path)
	}
	args, err := hostArgs([]string{"workspace", "create"}, host)
	if err != nil {
		return nil, err
	}
	return append(args, "--isolation", "local", "--path", path, "--title", title, "--json"), nil
}

func workspaceArchiveArgs(host, workspaceID string) ([]string, error) {
	if workspaceID == "" || strings.ContainsAny(workspaceID, " \t\r\n") || strings.HasPrefix(workspaceID, "-") {
		return nil, fmt.Errorf("invalid workspace id %q", workspaceID)
	}
	args, err := hostArgs([]string{"workspace", "archive"}, host)
	if err != nil {
		return nil, err
	}
	return append(args, workspaceID), nil
}

func scheduleListArgs(host string) ([]string, error) {
	args, err := hostArgs([]string{"schedule", "ls"}, host)
	if err != nil {
		return nil, err
	}
	return append(args, "--json"), nil
}

func scheduleDeleteArgs(host, scheduleID string) ([]string, error) {
	// The id becomes one argv element; the same argv rules as everywhere else.
	if scheduleID == "" || strings.ContainsAny(scheduleID, " \t\r\n") || strings.HasPrefix(scheduleID, "-") {
		return nil, fmt.Errorf("invalid schedule id %q", scheduleID)
	}
	args, err := hostArgs([]string{"schedule", "delete"}, host)
	if err != nil {
		return nil, err
	}
	return append(args, scheduleID, "--json"), nil
}

func inspectArgs(host, agentID string) ([]string, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent id is required")
	}
	args, err := hostArgs([]string{"inspect"}, host)
	if err != nil {
		return nil, err
	}
	return append(args, agentID, "--json"), nil
}

func destructiveArgs(host, operation, id string, extra ...string) ([]string, error) {
	if id == "" {
		return nil, fmt.Errorf("%s id is required", operation)
	}
	all := append([]string{id}, extra...)
	if err := rejectAll(all, operation); err != nil {
		return nil, err
	}
	args, err := hostArgs([]string{operation}, host)
	if err != nil {
		return nil, err
	}
	return append(args, all...), nil
}

func terminalCaptureArgs(host, terminalID string, start, end int) ([]string, error) {
	if terminalID == "" || start < 0 || end < start {
		return nil, fmt.Errorf("invalid terminal capture range")
	}
	args, err := hostArgs([]string{"terminal", "capture"}, host)
	if err != nil {
		return nil, err
	}
	return append(args, terminalID, "--start", strconv.Itoa(start), "--end", strconv.Itoa(end), "--json"), nil
}
