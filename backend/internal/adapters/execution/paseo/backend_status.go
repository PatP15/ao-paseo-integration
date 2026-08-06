package paseo

import (
	"context"
	"fmt"
)

// DaemonStatus is the identity and ownership subset of `paseo status --json`
// required before AO mutates a remote host.
type DaemonStatus struct {
	Status         string `json:"status"`
	ServerID       string `json:"serverId"`
	Hostname       string `json:"hostname"`
	Version        string `json:"version"`
	Listen         string `json:"listen"`
	DesktopManaged *bool  `json:"desktopManaged"`
}

// Status reads the targeted daemon identity. The host flag belongs to the
// status subcommand; it is deliberately not emitted as a global option.
func (c *Client) Status(ctx context.Context) (DaemonStatus, error) {
	args, err := statusArgs(c.host)
	return runJSON[DaemonStatus](ctx, c, args, err)
}

func statusArgs(host string) ([]string, error) {
	args, err := hostArgs([]string{"status"}, host)
	if err != nil {
		return nil, err
	}
	return append(args, "--json"), nil
}

// ListWorkspaces returns active workspaces so a completed create whose response
// was lost can be recovered by its AO-owned title without creating a second
// worktree.
func (c *Client) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	args, err := workspaceListArgs(c.host)
	return runJSON[[]Workspace](ctx, c, args, err)
}

func workspaceListArgs(host string) ([]string, error) {
	args, err := hostArgs([]string{"workspace", "ls"}, host)
	if err != nil {
		return nil, err
	}
	return append(args, "--json"), nil
}

func validateDaemonStatus(status DaemonStatus) error {
	if status.ServerID == "" {
		return fmt.Errorf("paseo daemon status omitted server id")
	}
	if status.Version == "" {
		return fmt.Errorf("paseo daemon status omitted version")
	}
	if status.DesktopManaged == nil {
		return fmt.Errorf("paseo daemon status omitted desktop ownership")
	}
	return nil
}
